package app

// The answer path: one question, two model calls, one persisted turn.
//
// Everything here is orchestration, which is why it lives in app rather than in
// chat or httpapi — the browser API and the later MCP server must run the same
// sequence, and the ordering rules below are the part that decides whether a
// citation in a user's history still opens something a month later.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"libris/internal/chat"
	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/llm"
	"libris/internal/retrieve"
)

// Model is the model side of one question: the research turn's completion and the
// answer's stream. They are one object because they are one server, and the split
// is only what each half of chat is allowed to ask for — a package that can
// complete cannot be tricked into streaming, and vice versa.
type Model interface {
	chat.LLM
	chat.Streamer
}

// Models is everything a question needs from the model servers, resolved from the
// settings saved when the question started. A saved setting takes effect on the
// next question, not the one under way.
type Models struct {
	// ChatModel is the model named in settings, sent per request.
	ChatModel string
	// Chat completes the research turn and streams the answer.
	Chat Model

	// Probe vectors the keyword half of a search. Without it a notebook cannot be
	// searched at all, which is a configuration the user can fix in Settings — so
	// Ask says so rather than answering from the question alone.
	Probe retrieve.Embedder

	// Caps is what the serving chat model said it can be shown, read when the question
	// started. Nil means the server would not say: media then goes out as-is and a
	// server that cannot take it answers with its own error, which is the honest
	// alternative to guessing from here. See llm.Caps.
	Caps *llm.Caps
}

// ModelsFor builds the model side for one configuration. A factory for the same
// reason ProcessorFor and EmbedderFor are: the URLs can change while the process
// runs, and a question must use the server the user has configured now.
type ModelsFor func(config.Config) (Models, error)

// AskEvents is how a question reports progress while it runs. Every callback is
// optional, and every one is called from the goroutine running Ask.
//
// The three are not interchangeable. Status is a sentence for a spinner; it is
// never part of the answer and never persisted. Citations fires once, before the
// answer streams, so the UI can draw the sources a turn rests on while the prose is
// still arriving. Delta is the answer arriving.
type AskEvents struct {
	Status    func(text string)
	Citations func([]domain.Citation)
	Delta     func(text, reasoning string)
}

func (e AskEvents) status(text string) {
	if e.Status != nil {
		e.Status(text)
	}
}

func (e AskEvents) citations(list []domain.Citation) {
	if e.Citations != nil && len(list) > 0 {
		e.Citations(list)
	}
}

func (e AskEvents) delta(d chat.Delta) {
	if e.Delta != nil && (d.Text != "" || d.Reasoning != "") {
		e.Delta(d.Text, d.Reasoning)
	}
}

// Ask runs one question against a notebook and returns the assistant message it
// produced, as stored.
//
// The order of the writes is the point of this function:
//
//  1. the conversation is read, then the user's message is stored, so a question
//     cannot be lost and cannot appear inside its own history;
//  2. the research turn runs and chooses its evidence;
//  3. the assistant row is stored *with its citations* before the answer streams;
//  4. the answer streams, then the row is finished with its text and trace.
//
// Step 3 before step 4 is not tidiness. A citation is a claim about what a chunk
// said, and if the process dies mid-stream the claim must still be in the database
// with the half of the answer that arrived. The reverse order would lose the
// evidence of an answer the user read.
//
// chat's own ordering rules apply underneath: the research trace never reaches the
// synthesiser, and a turn that found nothing is excluded from later history.
func (s *Service) Ask(ctx context.Context, notebookID, chatID, question string, ev AskEvents) (domain.Message, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return domain.Message{}, fmt.Errorf("%w: a question needs words in it", ErrInvalid)
	}
	if s.chats == nil || s.search == nil || s.models == nil {
		// A Service wired for ingest only. Not the user's problem to infer: the
		// transport reports it like any other unusable request.
		return domain.Message{}, fmt.Errorf("%w: this build cannot answer questions", ErrInvalid)
	}

	if _, err := s.store.GetNotebook(ctx, notebookID); err != nil {
		return domain.Message{}, err
	}

	c, err := s.chatFor(ctx, notebookID, chatID, question)
	if err != nil {
		return domain.Message{}, err
	}

	cfg := s.settings.Get()
	models, err := s.models(cfg)
	if err != nil {
		return domain.Message{}, err
	}
	if err := usable(models); err != nil {
		return domain.Message{}, err
	}

	// The conversation is read *before* the question is stored, or the question being
	// asked turns up inside its own history — which a follow-up would then treat as
	// something this library had already established.
	past, err := s.history(ctx, c.ID, cfg.HistoryMessages)
	if err != nil {
		return domain.Message{}, err
	}

	// (1) The question is kept whatever happens next, including a model server that
	// is not running: a user who asked deserves to see that they asked.
	userRow, err := s.chats.AppendMessage(ctx, domain.Message{
		ChatID: c.ID, Role: domain.RoleUser, Content: question,
	})
	if err != nil {
		return domain.Message{}, err
	}

	retriever := retrieve.New(s.search, models.Probe, retrieve.FileAssets{}, models.Caps)
	research := chat.NewResearch(models.Chat, retriever, chatConfig(cfg, models), ev.status)

	res, err := research.Ask(ctx, notebookID, question, chat.ResearchHistory(past, cfg.HistoryMessages))
	if err != nil {
		// The turn did not end, so nothing is written for it. The question stays in
		// the transcript, because a user who asked deserves to see that they asked.
		return domain.Message{}, err
	}

	citations := citationsFor(res.Evidence)

	// (3) Evidence is on disk before a word of the answer exists.
	assistant, err := s.chats.AppendMessage(ctx, domain.Message{
		ChatID: c.ID, Role: domain.RoleAssistant, Citations: citations,
	})
	if err != nil {
		return domain.Message{}, err
	}
	ev.citations(citations)

	// finish closes the assistant's row. Every path through a turn ends here, which is
	// what keeps the two things a finished turn decides — its text, and whether it is
	// knowledge — from being written by some paths and forgotten by others.
	finish := func(u domain.MessageUpdate) (domain.Message, error) {
		// Not the turn's context. That one is the user's, and the common reason for
		// arriving here with it already over is that they pressed stop — the record of
		// what happened still has to be written afterwards. Bounded, because a wedged
		// disk must not hold the process open either.
		done, cancel := persist(ctx)
		defer cancel()

		u.Trace = traceJSON(chat.CloseTrace(res.Trace, u.Content))
		if err := s.chats.UpdateMessage(done, assistant.ID, u); err != nil {
			return domain.Message{}, err
		}
		if notKnowledge(u.Status) {
			// Exclusion is a property of the turn, so the question is marked with its
			// answer. The answer is already stored by now, so failing to mark it is
			// worth a log line and not a lost turn.
			if err := s.chats.ExcludeMessages(done, userRow.ID, assistant.ID); err != nil {
				s.log.Warn("could not exclude a turn from history", "chat", c.ID, "err", err)
			}
		}
		return s.chats.GetMessage(done, assistant.ID)
	}

	if res.Status != chat.Synthesize {
		// The paths with no stream to write: the reason from no_info_found, the depth
		// message, or prose the model answered with instead of searching. The text
		// still arrives as a delta, because a UI that renders the answer from deltas
		// would otherwise show an empty bubble and wait for one.
		status := answerStatus(res.Status)
		ev.delta(chat.Delta{Text: res.Answer})
		return finish(domain.MessageUpdate{
			Content:   res.Answer,
			Status:    status,
			Citations: citations,
		})
	}

	// (4) The answer. Accumulated here as it arrives so an interrupted stream can
	// still be finished with what the user actually saw.
	var streamed strings.Builder

	// The thinking is collected for the reader, not for the next turn.
	var thought thinking

	answer, err := chat.NewSynthesizer(models.Chat, retriever, models.ChatModel, ev.status).Write(
		ctx,
		chat.SynthesisHistory(past, cfg.HistoryMessages),
		res.Evidence,
		question,
		func(d chat.Delta) error {
			thought.add(d)
			streamed.WriteString(d.Text)
			ev.delta(d)
			return nil
		})

	switch {
	case err == nil:
		// The synthesiser's own assembly of the text, not the accumulation: a caller
		// that showed deltas and a caller that reads the row cannot then disagree
		// about what was said. The thinking is taken the same way, for the same reason.
		return finish(domain.MessageUpdate{
			Content:     answer.Text,
			Status:      domain.MessageAnswered,
			Citations:   citations,
			Reasoning:   answer.Reasoning,
			ReasoningMS: thought.close(),
		})
	case stopped(ctx):
		// The accumulation is all there is: the stream was cut off before the
		// synthesiser could assemble either half.
		return finish(domain.MessageUpdate{
			Content:     streamed.String(),
			Status:      domain.MessageCanceled,
			Citations:   citations,
			Reasoning:   thought.String(),
			ReasoningMS: thought.close(),
		})
	default:
		// The partial answer is kept and labelled: losing text the user read is worse
		// than showing it with a warning. It is still not knowledge — half an answer,
		// cut off by a dead server, is exactly the claim a later turn would repeat
		// without being able to check it.
		s.log.Error("answer stream failed", "chat", c.ID, "err", err)
		_, _ = finish(domain.MessageUpdate{
			Content:     streamed.String(),
			Status:      domain.MessageInterrupted,
			Citations:   citations,
			Reasoning:   thought.String(),
			ReasoningMS: thought.close(),
		})
		return domain.Message{}, err
	}
}

// thinking accumulates the reasoning a synthesis stream carries, with the time it took
// before the first sentence arrived. A type rather than three locals inside Ask, which
// already decides more than its share — and the two are only meaningful together: the
// length is measured from the model's first word of thinking, not from the first byte
// of the request, so a slow round trip of research is not billed to the thinking.
type thinking struct {
	text strings.Builder
	at   time.Time
	ms   int64
}

// add folds one delta in. The clock stops at the first sentence, because the number
// labels the pause the reader waited through: a model that thinks again mid-answer is
// not measured twice.
func (t *thinking) add(d chat.Delta) {
	if d.Reasoning != "" {
		if t.at.IsZero() {
			t.at = time.Now()
		}
		t.text.WriteString(d.Reasoning)
	}
	if d.Text != "" && t.ms == 0 && !t.at.IsZero() {
		t.ms = time.Since(t.at).Milliseconds()
	}
}

// close ends the measurement, for the streams that never reached a sentence: stopped
// by the user, or cut off by a dead server. 0 when nothing was thought, which the UI
// reads as no claim at all rather than as thinking for no time.
func (t *thinking) close() int64 {
	if t.ms == 0 && !t.at.IsZero() {
		t.ms = time.Since(t.at).Milliseconds()
	}
	return t.ms
}

func (t *thinking) String() string { return t.text.String() }

// answerStatus translates how the loop ended into what a reader learns. They are not
// one vocabulary: "synthesize" is control flow — evidence in hand, prose not yet
// written — and nobody outside this function cares.
func answerStatus(s chat.Status) domain.MessageStatus {
	switch s {
	case chat.NoInfo:
		return domain.MessageNoInfo
	case chat.RawAnswer:
		return domain.MessageUngrounded
	case chat.Canceled:
		return domain.MessageCanceled
	}
	return domain.MessageAnswered
}

// notKnowledge is the one place that decides a turn must not reach a later turn's
// history, recorded by whoever finished the turn while it still knows why. The rule
// is not "the status is unusual"; it is that a turn which produced no grounded
// conclusion is not evidence.
//
// The no-info case is the one that matters. Left in the transcript, an earlier "I
// couldn't find that" reads to the next turn as proof that the fact is absent from
// the world rather than from this corpus. A turn the user stopped, and an answer cut
// off by a dead server, are excluded for the same reason in miniature: neither is a
// conclusion anything should rely on twice.
func notKnowledge(s domain.MessageStatus) bool {
	return s != domain.MessageAnswered
}

// chatFor finds the conversation, making one when the caller did not name it.
// NotebookLM opens a chat with a title already in it, and a question is a better
// title than "Chat 3": it is what the user will remember the conversation by.
func (s *Service) chatFor(ctx context.Context, notebookID, chatID, question string) (domain.Chat, error) {
	if chatID == "" {
		return s.chats.CreateChat(ctx, notebookID, chatTitle(question))
	}

	c, err := s.chats.GetChat(ctx, chatID)
	if err != nil {
		return domain.Chat{}, err
	}
	// A chat in another notebook is reported as absent rather than as somebody else's:
	// the route already says which notebook this request is for.
	if c.NotebookID != notebookID {
		return domain.Chat{}, fmt.Errorf("%w: chat %s", domain.ErrNotFound, chatID)
	}
	return c, nil
}

// history is the conversation a new question is asked inside.
//
// The store has already dropped the turns excluded from history. What is left is
// mapped into chat's shape; the two role vocabularies are the same strings and
// TestRoleVocabulariesAgree pins that rather than leaving it to coincidence.
// persist is the context for a finished turn's writes.
//
// A turn's own context ends when the user stops it or when it runs out of time, and
// both of those are moments that still produce a row to write: a canceled answer, a
// partial one, and the citations recorded before the stream. Doing the final writes on
// the dead context would throw away exactly the record the user stopped to look at.
func persist(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

// stopped reports whether the user stopped this turn. A deadline is deliberately
// excluded: running out of time is a failure the UI should say so about, while
// pressing stop is a request that deserves the outcome it asked for.
func stopped(ctx context.Context) bool { return errors.Is(ctx.Err(), context.Canceled) }

func (s *Service) history(ctx context.Context, chatID string, window int) ([]chat.HistoryMessage, error) {
	rows, err := s.chats.RecentMessages(ctx, chatID, window)
	if err != nil {
		return nil, err
	}

	out := make([]chat.HistoryMessage, 0, len(rows))
	for _, m := range rows {
		role, ok := historyRole(m.Role)
		if !ok {
			continue
		}
		out = append(out, chat.HistoryMessage{Role: role, Text: m.Content})
	}
	return out, nil
}

func historyRole(r domain.Role) (llm.Role, bool) {
	switch r {
	case domain.RoleUser:
		return llm.RoleUser, true
	case domain.RoleAssistant:
		return llm.RoleAssistant, true
	}
	return "", false
}

// chatConfig is one turn's budget, read from the settings saved now. maxIterations
// and historyMessages are settings rather than constants because a number the user
// cannot see does not exist, and both are the things worth measuring against a real
// model.
//
// One of these fields is not read from the settings, and the difference is not cosmetic.
// The model's name is a fact about the server libris is talking to, and in managed mode it is
// derived when Models is built — from the router's aliases — so `Models` is the only place it
// is correct. From the settings, the research call sent whatever a person had typed as a chat
// model's name while pointing libris at somebody else's machine, and the router answered
// `model 'qwen2.5-7b-instruct' not found` for a model it lists as `libris-chat` — while the synthesis
// call, which reads `Models.ChatModel`, kept working. Two calls, one server, one of them
// reading the wrong side.
func chatConfig(cfg config.Config, models Models) chat.Config {
	return chat.Config{
		ChatModel:     models.ChatModel,
		MaxIterations: cfg.MaxIterations,
		TopK:          cfg.TopK,
		Thinking:      cfg.ResearchThinking,
	}
}

// usable states the configuration a question cannot be answered under, in terms the
// user can act on. These are not bugs and not the model's fault; they are Settings
// that have not been filled in yet.
func usable(m Models) error {
	switch {
	case m.Chat == nil:
		return fmt.Errorf("%w: no chat server is reachable", ErrInvalid)
	case m.ChatModel == "":
		return fmt.Errorf("%w: Settings ▸ Models ▸ chat model is empty", ErrInvalid)
	case m.Probe == nil:
		return fmt.Errorf("%w: no embedding server is configured, so this notebook cannot be searched", ErrInvalid)
	}
	return nil
}

// citationsFor freezes what the model chose into something a later read can render
// and open, in the order the evidence was ordered.
func citationsFor(evidence []retrieve.Evidence) []domain.Citation {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]domain.Citation, 0, len(evidence))
	for _, e := range evidence {
		out = append(out, domain.CitationFrom(e.Index, e.SourceName, e.Segment))
	}
	return out
}

// traceJSON encodes a turn's research transcript for the row. It is written and
// never read back into a model or the API: a turn should be replayable after a
// prompt change without anything being able to mistake the reasoning for knowledge.
// A transcript that will not encode is reported in the trace field rather than
// failing an answer the user has already read.
// traceJSON encodes the research transcript into the assistant's row.
//
// Media is dropped on the way in. The pixels did their job — they went to the model —
// and this column is write-only by design (store/sqlite/chat.go says trace_json is
// never selected), so keeping a page raster here is a couple of megabytes of base64
// nobody will ever open. The label part in front of it stays, which is what still
// tells a reader that a page was shown and which one the answer cites.
func traceJSON(trace []llm.Message) string {
	saved := make([]llm.Message, 0, len(trace))
	for _, m := range trace {
		if parts, ok := m.Content.([]llm.ContentPart); ok {
			kept := make([]llm.ContentPart, 0, len(parts))
			for _, p := range parts {
				if p.Type == llm.PartText {
					kept = append(kept, p)
				}
			}
			m.Content = kept
		}
		saved = append(saved, m)
	}

	raw, err := json.Marshal(saved)
	if err != nil {
		return `[{"error":"trace could not be encoded"}]`
	}
	return string(raw)
}

// chatTitle is the first question, on one line, short enough to put in a list.
func chatTitle(question string) string {
	one := strings.Join(strings.Fields(question), " ")
	const limit = 72
	if utf8.RuneCountInString(one) <= limit {
		return one
	}
	runes := []rune(one)
	return strings.TrimRight(string(runes[:limit]), " ,;:.") + "…"
}

// NewChat starts an empty conversation. Ask creates one too, when the caller did not
// name it, so this exists for the deliberate "new chat" rather than for necessity.
func (s *Service) NewChat(ctx context.Context, notebookID, title string) (domain.Chat, error) {
	if s.chats == nil {
		return domain.Chat{}, fmt.Errorf("%w: this build cannot answer questions", ErrInvalid)
	}
	if _, err := s.store.GetNotebook(ctx, notebookID); err != nil {
		return domain.Chat{}, err
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = "Untitled"
	}
	return s.chats.CreateChat(ctx, notebookID, title)
}

// Chats lists a notebook's conversations, newest first.
func (s *Service) Chats(ctx context.Context, notebookID string) ([]domain.Chat, error) {
	if s.chats == nil {
		return nil, nil
	}
	if _, err := s.store.GetNotebook(ctx, notebookID); err != nil {
		return nil, err
	}
	return s.chats.ListChats(ctx, notebookID)
}

// RenameChat gives a conversation a title a reader chose, rather than the one its
// first question produced. That title is honest but often a rambling sentence, and a
// list of them is hard to tell apart — which is the whole reason the list exists.
//
// The blank is refused here, not at the transport, so that no transport can store a
// chat with no name. Whitespace is trimmed rather than collapsed the way NewChat does
// it, because this title is typed rather than pasted, and RenameNotebook has the same
// rule for the same reason.
func (s *Service) RenameChat(ctx context.Context, notebookID, chatID, title string) error {
	if s.chats == nil {
		return fmt.Errorf("%w: this build cannot answer questions", ErrInvalid)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("%w: a chat needs a title", ErrInvalid)
	}
	if c, err := s.chats.GetChat(ctx, chatID); err != nil {
		return err
	} else if c.NotebookID != notebookID {
		return fmt.Errorf("%w: chat %s", domain.ErrNotFound, chatID)
	}
	return s.chats.UpdateChatTitle(ctx, chatID, title)
}

// DeleteChat removes one conversation with its messages. Like deleting a notebook,
// it touches no source and no file: another chat may cite the same page, and nothing
// re-derives a raster cheaply.
func (s *Service) DeleteChat(ctx context.Context, notebookID, chatID string) error {
	if s.chats == nil {
		return fmt.Errorf("%w: this build cannot answer questions", ErrInvalid)
	}
	if c, err := s.chats.GetChat(ctx, chatID); err != nil {
		return err
	} else if c.NotebookID != notebookID {
		return fmt.Errorf("%w: chat %s", domain.ErrNotFound, chatID)
	}
	return s.chats.DeleteChat(ctx, chatID)
}

// ChatMessages returns a conversation oldest first — the order a reader wants. The
// research transcripts are not in it: the store does not read them back for a
// listing, and no route serves them.
func (s *Service) ChatMessages(ctx context.Context, notebookID, chatID string) ([]domain.Message, error) {
	if s.chats == nil {
		return nil, nil
	}
	if c, err := s.chats.GetChat(ctx, chatID); err != nil {
		return nil, err
	} else if c.NotebookID != notebookID {
		return nil, fmt.Errorf("%w: chat %s", domain.ErrNotFound, chatID)
	}
	return s.chats.ListMessages(ctx, chatID)
}
