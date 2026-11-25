package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"libris/internal/config"

	"libris/internal/domain"
	"libris/internal/llm"
	"libris/internal/store/sqlite"
)

// The answer path is driven here with a scripted model, so the ordering rules — the
// ones that decide whether a citation still resolves a month later — are asserted
// rather than observed. The real SQLite store is used for the same reason the
// ingest tests use it: the interesting failures are how a turn's output lands in the
// schema, not what a mock would agree to.

const probeIdentity = "embeddinggemma-2"

// askModel is a slice of canned completions plus a canned answer stream. It fails
// the test when the loop asks for a reply it was not given, which is what pins the
// iteration budget from the outside.
type askModel struct {
	t       *testing.T
	replies []*llm.ChatResponse

	answer    []string
	chatErr   error
	streamErr error

	// reasoning is what a model that thinks out loud streams before its first sentence.
	reasoning []string

	// onStream runs before any delta is yielded, which is the only moment a test can
	// look at the database mid-answer.
	onStream func()

	chatReqs  []llm.ChatRequest
	synthReqs []llm.ChatRequest
}

func (m *askModel) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	m.chatReqs = append(m.chatReqs, req)
	if m.chatErr != nil {
		return nil, m.chatErr
	}
	if len(m.replies) == 0 {
		m.t.Fatalf("the research loop asked for a completion it was not given (%d sent so far)",
			len(m.chatReqs))
	}
	reply := m.replies[0]
	m.replies = m.replies[1:]
	return reply, nil
}

func (m *askModel) ChatStream(ctx context.Context, req llm.ChatRequest, yield func(llm.Chunk) error) error {
	m.synthReqs = append(m.synthReqs, req)
	if m.onStream != nil {
		m.onStream()
	}
	// A thinking model pauses before it writes. The pause is real rather than faked
	// because the length of it is a number this path measures and stores, and a fake
	// that arrived instantly could not tell a stored number from a zero.
	if len(m.reasoning) > 0 {
		for _, piece := range m.reasoning {
			if err := yield(llm.Chunk{Delta: llm.Delta{ReasoningContent: piece}}); err != nil {
				return err
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	// The answer arrives, and then the server dies: the case that decides whether a
	// partial answer is kept.
	for _, piece := range m.answer {
		// A stream the user stopped breaks where it stands; a fake that yielded the
		// rest anyway would hide exactly the case this file cares about.
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(llm.Chunk{Delta: llm.Delta{Content: piece}}); err != nil {
			return err
		}
	}
	return m.streamErr
}

// toolReply is one assistant turn that calls a tool, written in the wire shape rather
// than with local structs: what the model emits is JSON, and a fake built from Go
// values can drift from it.
func toolReply(id, name, args string) *llm.ChatResponse {
	return &llm.ChatResponse{Choices: []llm.ChatChoice{{
		Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: args},
		}}},
		FinishReason: "tool_calls",
	}}}
}

func proseReply(text string) *llm.ChatResponse {
	return &llm.ChatResponse{Choices: []llm.ChatChoice{{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: text},
		FinishReason: "stop",
	}}}
}

type askProbe struct {
	identity string
	err      error
	calls    int
}

func (p *askProbe) EmbedQuery(context.Context, string, string) ([]float32, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	// Every stored vector is [1,0,0], so the first segment is always the closest and
	// a test can say which chunk a citation must name.
	return []float32{1, 0, 0}, nil
}

func (p *askProbe) Identity() string {
	if p.identity == "" {
		return probeIdentity
	}
	return p.identity
}

// askWiring returns a Service with the answer path wired to the real store, plus the
// model it will talk to.
func askWiring(t *testing.T, replies ...*llm.ChatResponse) (*Service, *sqlite.DB, *askModel) {
	t.Helper()

	svc, db, _ := testServiceWired(t, &fakeProcessor{}, func(config.Config) (Embedder, error) {
		return &fakeEmbedder{}, nil
	})

	model := &askModel{t: t, replies: replies, answer: []string{"Smith measured ", "4.2 K [[Chunk 1]]."}}
	svc.chats = db
	svc.search = db
	svc.models = func(config.Config) (Models, error) {
		return Models{ChatModel: "test-chat", Chat: model, Probe: &askProbe{}}, nil
	}
	return svc, db, model
}

// seedReady writes a ready source with one text chunk per argument, each with a
// vector in the probe's space. Ready is what makes it retrievable at all.
func seedReady(t *testing.T, db *sqlite.DB, notebookID, sourceID string, texts ...string) {
	t.Helper()
	ctx := context.Background()

	if _, _, err := db.AddSource(ctx, notebookID, domain.Source{
		ID: sourceID, Name: "paper.pdf", Kind: domain.SourcePDF, SHA256: sourceID,
		Bytes: 100, OriginalPath: "/data/originals/" + sourceID, Status: domain.StatusReady,
	}); err != nil {
		t.Fatal(err)
	}

	segs := make([]domain.Segment, 0, len(texts))
	vecs := make([]domain.Embedding, 0, len(texts))
	for i, text := range texts {
		id := sourceID + ":" + pad(i)
		segs = append(segs, domain.Segment{
			ID: id, SourceID: sourceID, DocID: sourceID, Kind: domain.KindText,
			Ordinal: i, Text: text,
			Span: domain.Provenance{Page: i + 1, CharStart: 100 * i, CharEnd: 100*i + 60},
		})
		vecs = append(vecs, domain.Embedding{
			SegmentID: id, Model: probeIdentity, Dim: 3, Vector: []float32{1, 0, 0},
		})
	}
	if err := db.PutSegments(ctx, sourceID, segs); err != nil {
		t.Fatal(err)
	}
	if err := db.PutEmbeddings(ctx, vecs); err != nil {
		t.Fatal(err)
	}
}

func ask(t *testing.T, svc *Service, notebookID, chatID, question string) (domain.Message, []domain.Citation, []string) {
	t.Helper()

	var (
		citations []domain.Citation
		statuses  []string
		answer    strings.Builder
	)
	msg, err := svc.Ask(context.Background(), notebookID, chatID, question, AskEvents{
		Status:    func(text string) { statuses = append(statuses, text) },
		Citations: func(list []domain.Citation) { citations = list },
		Delta:     func(text, _ string) { answer.WriteString(text) },
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if answer.String() == "" && msg.Content != "" {
		t.Errorf("nothing streamed, yet the row says %q", msg.Content)
	}
	return msg, citations, statuses
}

func rows(t *testing.T, db *sqlite.DB, chatID string) []domain.Message {
	t.Helper()
	list, err := db.ListMessages(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// The saved transcript is write-only — store/sqlite/chat.go says trace_json is never
// selected — so pixels kept here are weight without a reader: the model was already
// sent them. The label in front of each one stays, because that is what still says a
// page was shown and which one the answer cites.
func TestSavedTraceKeepsTheWordsAndDropsThePixels(t *testing.T) {
	trace := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "query_knowledge_base"}},
		}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: []llm.ContentPart{
			llm.TextPart("[[Chunk 3]] scan.pdf page 2 — image\n"),
			mustMedia(t, "image/png", "AAAA"),
			llm.TextPart("\n\n**Assistant Evaluation Note**: …"),
		}},
	}

	raw := traceJSON(trace)
	if strings.Contains(raw, "data:image") || strings.Contains(raw, "image_url") {
		t.Errorf("the saved trace carries pixels:\n%s", raw)
	}
	if !strings.Contains(raw, "[[Chunk 3]] scan.pdf page 2 — image") {
		t.Errorf("the saved trace lost which page was shown:\n%s", raw)
	}
	if !strings.Contains(raw, "Assistant Evaluation Note") {
		t.Errorf("the saved trace lost the research turn's prose:\n%s", raw)
	}

	// Encoding is a read, not a rewrite: the same messages are what went out to the
	// model, and a caller that reuses them must not find them quietly smaller.
	parts, ok := trace[1].Content.([]llm.ContentPart)
	if !ok || len(parts) != 3 {
		t.Errorf("the caller's transcript was edited by encoding: %v", trace[1].Content)
	}
}

// mustMedia builds a media part with the one constructor libris uses, so this test is
// about what the trace does to a real part rather than about a shape invented here.
func mustMedia(t *testing.T, mime, data string) llm.ContentPart {
	t.Helper()
	part, err := llm.MediaPart(llm.MediaImage, mime, data)
	if err != nil {
		t.Fatalf("MediaPart: %v", err)
	}
	return part
}

func traceOf(t *testing.T, db *sqlite.DB, id int64) string {
	t.Helper()
	var raw string
	if err := db.Raw().QueryRowContext(context.Background(),
		`SELECT trace_json FROM messages WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func excluded(t *testing.T, db *sqlite.DB, id int64) bool {
	t.Helper()
	var n int
	if err := db.Raw().QueryRowContext(context.Background(),
		`SELECT exclude_from_history FROM messages WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n != 0
}

// A question is two calls to one server, so both must name the same model — and in managed
// mode that name is a fact about the router process, derived when Models is built.
// Read from the settings instead, the research turn was refused by the very server the answer
// eventually came from: `model 'LFM3-VL-2B' not found`, for a model the router lists as
// `libris-chat`. The stored name is what someone typed while pointing libris at a different
// machine, and it is legitimately still there; it is simply not what to ask this one for.
func TestBothCallsNameTheServedModel(t *testing.T) {
	svc, db, model := askWiring(t,
		toolReply("c1", "query_knowledge_base", `{"keywords":"smith measurement","query":"what did smith measure"}`),
		toolReply("c2", "finalize_research_response", `{"indices":[1]}`),
	)

	stored := config.Default()
	stored.ChatModel = "LFM3-VL-2B"
	svc.settings = constSettings(stored)
	svc.models = func(config.Config) (Models, error) {
		return Models{ChatModel: "libris-chat", Chat: model, Probe: &askProbe{}}, nil
	}

	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-a", "Smith measured 4.2 K.", "An unrelated page.")
	ask(t, svc, nb, "", "What did Smith measure?")

	if len(model.chatReqs) == 0 || len(model.synthReqs) == 0 {
		t.Fatalf("want both calls; got %d research, %d synthesis", len(model.chatReqs), len(model.synthReqs))
	}
	for i, req := range model.chatReqs {
		if req.Model != "libris-chat" {
			t.Errorf("research call %d names %q, want the name this server answers to", i, req.Model)
		}
	}
	for i, req := range model.synthReqs {
		if req.Model != "libris-chat" {
			t.Errorf("synthesis call %d names %q", i, req.Model)
		}
	}
}

// A question is researched, cited, streamed and stored. The assertions are mostly
// about what must *not* be where: the research trace must not reach the synthesiser,
// and the synthesiser's citations must be the chunks the research turn chose.
func TestAskAnswersFromEvidence(t *testing.T) {
	svc, db, model := askWiring(t,
		toolReply("c1", "query_knowledge_base", `{"keywords":"smith measurement","query":"what did smith measure"}`),
		toolReply("c2", "finalize_research_response", `{"indices":[1]}`),
	)
	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-a", "Smith measured 4.2 K.", "An unrelated page.")

	msg, citations, statuses := ask(t, svc, nb, "", "What did Smith measure?")

	if msg.Status != domain.MessageAnswered {
		t.Errorf("status = %q, want answered", msg.Status)
	}
	if msg.Content != "Smith measured 4.2 K [[Chunk 1]]." {
		t.Errorf("content = %q", msg.Content)
	}
	if len(citations) != 1 || citations[0].Chunk != 1 {
		t.Fatalf("citations = %+v, want the one chunk the model chose", citations)
	}
	cit := citations[0]
	if cit.Quote != "Smith measured 4.2 K." || cit.SourceName != "paper.pdf" {
		t.Errorf("citation = %+v, want the frozen quote and source name", cit)
	}
	if cit.Span.Page != 1 || cit.Span.CharStart != 0 || cit.Span.CharEnd != 60 {
		t.Errorf("span = %+v, want the page-local range the citation route is built from", cit.Span)
	}
	if len(statuses) == 0 {
		t.Error("no progress was reported, so the UI would show a spinner and nothing else")
	}

	list := rows(t, db, msg.ChatID)
	if len(list) != 2 {
		t.Fatalf("%d rows, want the question and the answer", len(list))
	}
	if list[0].Role != domain.RoleUser || list[0].Content != "What did Smith measure?" {
		t.Errorf("first row = %+v, want the user's question first", list[0])
	}
	if list[1].Role != domain.RoleAssistant || list[1].Status != domain.MessageAnswered {
		t.Errorf("second row = %+v", list[1])
	}
	if len(list[1].Citations) != 1 {
		t.Errorf("stored citations = %+v", list[1].Citations)
	}

	// The transcript is stored, closed by the answer, and never sent anywhere else.
	trace := traceOf(t, db, msg.ID)
	if !strings.Contains(trace, "query_knowledge_base") {
		t.Error("trace does not contain the research turn")
	}
	if !strings.Contains(trace, "Smith measured 4.2 K [[Chunk 1]].") {
		t.Error("the finalize call was not closed by the answer, so the trace cannot be replayed")
	}

	// The synthesiser's world: system, evidence, question. No tool names, no tool
	// messages, no evaluation note — reasoning about what to search next is not
	// knowledge, and pasted in it lets the answer repeat a conclusion it never found.
	if len(model.synthReqs) != 1 {
		t.Fatalf("%d synthesis calls, want 1", len(model.synthReqs))
	}
	var joined strings.Builder
	for _, m := range model.synthReqs[0].Messages {
		if m.Role == llm.RoleTool || len(m.ToolCalls) > 0 {
			t.Fatalf("synthesis prompt carries a tool-shaped message: %+v", m)
		}
		joined.WriteString(textContent(m))
	}
	prompt := joined.String()
	for _, forbidden := range []string{"finalize_research_response", "no_info_found", "tool_call_id"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("synthesis prompt contains %q", forbidden)
		}
	}
	if !strings.Contains(prompt, "Smith measured 4.2 K.") || !strings.Contains(prompt, "[[Chunk 1]]") {
		t.Errorf("synthesis prompt lacks the evidence or its label:\n%s", prompt)
	}
	if strings.Contains(prompt, "An unrelated page.") {
		t.Error("the synthesiser was shown a chunk the research turn never chose")
	}
	if !strings.Contains(prompt, "What did Smith measure?") {
		t.Error("synthesis prompt does not end with the question")
	}
}

// The thinking a reasoning model streams is stored beside its answer, so the disclosure
// on an answer still opens a week later. The second half of this test is what makes the
// first half safe: nothing re-reads the thinking into a later prompt, because thinking
// about how to look something up is not a fact this library established, and a turn that
// repeated it would be repeating a conclusion it never checked.
func TestAskStoresTheThinkingWithTheAnswer(t *testing.T) {
	svc, db, model := askWiring(t,
		toolReply("c1", "query_knowledge_base", `{"keywords":"smith","query":"what did smith measure"}`),
		toolReply("c2", "finalize_research_response", `{"indices":[1]}`),
		toolReply("c3", "query_knowledge_base", `{"keywords":"unit","query":"what unit was the figure in"}`),
		toolReply("c4", "finalize_research_response", `{"indices":[1]}`),
	)
	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-a", "Smith measured 4.2 K.", "An unrelated page.")

	const thought = "The question names Smith, and the first chunk holds a figure. "
	model.reasoning = []string{"The question names Smith, ", "and the first chunk holds a figure. "}

	var streamed strings.Builder
	msg, err := svc.Ask(context.Background(), nb, "", "What did Smith measure?", AskEvents{
		Delta: func(_, reasoning string) { streamed.WriteString(reasoning) },
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	// Streamed first, so the reader watching the answer arrive can read it now.
	if streamed.String() != thought {
		t.Errorf("the reader was streamed %q as thinking, want the thinking", streamed.String())
	}
	// And stored, so the same reader can ask a week later.
	if msg.Reasoning != thought {
		t.Errorf("reasoning = %q, want what the stream carried", msg.Reasoning)
	}
	if msg.ReasoningMS <= 0 {
		t.Errorf("reasoning_ms = %d, want the pause between the thinking and the first sentence", msg.ReasoningMS)
	}
	if msg.Content != "Smith measured 4.2 K [[Chunk 1]]." {
		t.Errorf("content = %q, want the answer on its own and the thinking off it", msg.Content)
	}

	list := rows(t, db, msg.ChatID)
	if len(list) != 2 {
		t.Fatalf("%d rows, want the question and the answer", len(list))
	}
	if list[1].Reasoning != thought || list[1].ReasoningMS != msg.ReasoningMS {
		t.Errorf("stored row = %+v, want the thinking and its length read back for the disclosure", list[1])
	}

	// The turn after this one is given its prose and none of its thinking.
	ask(t, svc, nb, msg.ChatID, "And what unit was that?")
	if len(model.synthReqs) != 2 {
		t.Fatalf("%d synthesis calls, want 2", len(model.synthReqs))
	}
	var prompt strings.Builder
	for _, m := range model.synthReqs[1].Messages {
		prompt.WriteString(textContent(m))
	}
	if strings.Contains(prompt.String(), "The question names Smith") {
		t.Error("a previous turn's thinking reached the next turn's prompt")
	}
	// The prose does arrive — with its chunk labels stripped, since the next turn's
	// registry numbers nothing the last turn used.
	if !strings.Contains(prompt.String(), "Smith measured 4.2 K") {
		t.Error("a previous turn's answer did not reach the next turn's prompt")
	}
}

// Citations are written before the answer exists. Not tidiness: a citation is a claim
// about what a chunk said, and if the process dies mid-stream the claim has to be in
// the database with the half of the answer the user read.
func TestAskWritesCitationsBeforeTheAnswerStreams(t *testing.T) {
	svc, db, model := askWiring(t,
		toolReply("c1", "query_knowledge_base", `{"keywords":"smith","query":"what did smith measure"}`),
		toolReply("c2", "finalize_research_response", `{"indices":[1]}`),
	)
	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-order", "Smith measured 4.2 K.")

	var (
		gotCitations int
		gotContent   = "unset"
		gotStatus    = "unset"
	)
	model.onStream = func() {
		var content, status string
		var n int
		if err := db.Raw().QueryRowContext(context.Background(),
			`SELECT content, status, (SELECT COUNT(*) FROM json_each(citations)) FROM messages
			  WHERE role = 'assistant'`,
		).Scan(&content, &status, &n); err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("reading the row mid-stream: %v", err)
		}
		gotCitations, gotContent, gotStatus = n, content, status
	}

	ask(t, svc, nb, "", "What did Smith measure?")

	if gotContent == "unset" {
		t.Fatal("the synthesis call never happened, so this test proved nothing")
	}
	if gotCitations == 0 {
		t.Error("the answer was streaming with no citations stored yet")
	}
	if gotContent != "" || gotStatus != "" {
		t.Errorf("mid-stream row = (%q, %q), want the text and status written by the finish",
			gotContent, gotStatus)
	}
}

// A turn the library could not answer is excluded whole. Its reason is shown to the
// user, and it is not knowledge: left in a later prompt, "I couldn't find X" reads as
// evidence that X is absent from the world rather than from this corpus.
func TestAskExcludesANoInfoTurnFromHistory(t *testing.T) {
	svc, db, model := askWiring(t,
		toolReply("s1", "query_knowledge_base", `{"keywords":"2031","query":"projections for 2031"}`),
		toolReply("s2", "no_info_found", `{"reason":"Nothing in the corpus covers 2031."}`),
	)
	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-ni", "A page about something else.")

	msg, _, _ := ask(t, svc, nb, "", "What about 2031?")
	if msg.Status != domain.MessageNoInfo {
		t.Fatalf("status = %q, want no-info", msg.Status)
	}
	if msg.Content != "Nothing in the corpus covers 2031." {
		t.Errorf("content = %q, want the model's own reason verbatim", msg.Content)
	}
	if len(msg.Citations) != 0 {
		t.Errorf("citations = %+v, want none", msg.Citations)
	}
	if !excluded(t, db, msg.ID) {
		t.Error("a turn that found nothing is not excluded from history")
	}

	// A second question in the same chat: neither model may see the dead end.
	seedReady(t, db, nb, "sha-ni2", "Smith measured 4.2 K.")
	model.replies = append(model.replies,
		toolReply("s3", "query_knowledge_base", `{"keywords":"smith","query":"what did smith measure"}`),
		toolReply("s4", "finalize_research_response", `{"indices":[1]}`),
	)

	msg2, _, _ := ask(t, svc, nb, msg.ChatID, "And what did Smith measure?")
	if msg2.Status != domain.MessageAnswered {
		t.Fatalf("status = %q", msg2.Status)
	}

	// The first turn ended without an answer, so the offsets have to be counted from
	// the end rather than hand-guessed.
	var synthesis strings.Builder
	for _, m := range model.synthReqs[len(model.synthReqs)-1].Messages {
		synthesis.WriteString(textContent(m))
	}

	for label, content := range map[string]string{
		"the researcher":  textContent(model.chatReqs[len(model.chatReqs)-2].Messages[1]),
		"the synthesiser": synthesis.String(),
	} {
		if strings.Contains(content, "2031") {
			t.Errorf("%s was shown the dead end:\n%s", label, content)
		}
	}
}

// Both models get the past, in different shapes: flattened text for the researcher so
// old tool calls cannot be mistaken for pending ones, real role messages for the
// synthesiser so a pronoun resolves.
func TestAskCarriesHistoryIntoBothCalls(t *testing.T) {
	svc, db, model := askWiring(t,
		toolReply("h1", "query_knowledge_base", `{"keywords":"smith","query":"what did smith measure"}`),
		toolReply("h2", "finalize_research_response", `{"indices":[1]}`),
	)
	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-h", "Smith measured 4.2 K.")

	first, _, _ := ask(t, svc, nb, "", "What did Smith measure?")

	researchReqs := len(model.chatReqs)
	synthReqs := len(model.synthReqs)
	model.replies = append(model.replies,
		toolReply("h3", "query_knowledge_base", `{"keywords":"his follow-up","query":"what followed from it"}`),
		toolReply("h4", "finalize_research_response", `{"indices":[1]}`),
	)

	second, _, _ := ask(t, svc, nb, first.ChatID, "And what did he conclude after that?")
	_ = second

	if len(model.chatReqs) != researchReqs+2 || len(model.synthReqs) != synthReqs+1 {
		t.Fatalf("model calls = %d research, %d synthesis", len(model.chatReqs), len(model.synthReqs))
	}

	// Research: flattened, and the new question is the last thing labelled — after a
	// follow-up an unlabelled block is indistinguishable from the question to answer.
	probe := textContent(model.chatReqs[researchReqs].Messages[1])
	if !strings.Contains(probe, "Synthesizer answer: Smith measured 4.2 K") {
		t.Errorf("research history lost the prior answer:\n%s", probe)
	}
	if strings.Count(probe, "Current query:") != 1 {
		t.Errorf("research seed has %d current-query labels, want exactly one:\n%s",
			strings.Count(probe, "Current query:"), probe)
	}
	if !strings.HasSuffix(strings.TrimSpace(probe), "And what did he conclude after that?") {
		t.Errorf("the question is not last:\n%s", probe)
	}
	// Chunk labels are stripped from history: a follow-up would otherwise cite a
	// chunk from a turn whose registry no longer exists.
	if strings.Contains(probe, "[[Chunk") {
		t.Error("a chunk label survived into the next turn's history")
	}

	// Synthesis: real messages, in order, with the earlier answer as an assistant turn.
	var roles []string
	for _, m := range model.synthReqs[synthReqs].Messages {
		roles = append(roles, string(m.Role))
	}
	want := []string{"system", "user", "assistant", "user"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Errorf("synthesis roles = %v, want %v", roles, want)
	}

	// The question being asked is not inside its own history, or a follow-up would
	// treat it as something this library had already established.
	if strings.Count(probe, "And what did he conclude after that?") != 1 {
		t.Errorf("the current question appears twice in its own history:\n%s", probe)
	}
	_ = db
}

func TestAskReportsAUngroundedAnswer(t *testing.T) {
	svc, db, _ := askWiring(t, proseReply("Smith probably measured about four kelvin."))
	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-raw", "Smith measured 4.2 K.")

	msg, citations, _ := ask(t, svc, nb, "", "What did Smith measure?")

	if msg.Status != domain.MessageUngrounded {
		t.Errorf("status = %q, want ungrounded", msg.Status)
	}
	if msg.Content != "Smith probably measured about four kelvin." {
		t.Errorf("content = %q, want the prose verbatim", msg.Content)
	}
	if len(citations) != 0 {
		t.Errorf("citations = %+v, want none for an answer that never searched", citations)
	}
	if rows(t, db, msg.ChatID)[1].Content == "" {
		t.Error("the prose was not stored")
	}
}

func TestCanceledAskKeepsWhatArrived(t *testing.T) {
	svc, db, model := askWiring(t,
		toolReply("x1", "query_knowledge_base", `{"keywords":"smith","query":"what did smith measure"}`),
		toolReply("x2", "finalize_research_response", `{"indices":[1]}`),
	)
	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-cancel", "Smith measured 4.2 K.")

	ctx, cancel := context.WithCancel(context.Background())
	model.onStream = cancel // the user pressed stop mid-answer

	var streamed strings.Builder
	msg, err := svc.Ask(ctx, nb, "", "What did Smith measure?", AskEvents{
		Delta: func(text, _ string) { streamed.WriteString(text) },
	})
	if err != nil {
		t.Fatalf("a stopped turn is an outcome, not a failure: %v", err)
	}
	if msg.Status != domain.MessageCanceled {
		t.Errorf("status = %q, want canceled", msg.Status)
	}
	if streamed.Len() != 0 {
		t.Errorf("%d bytes arrived after cancellation was requested", streamed.Len())
	}
	if got := rows(t, db, msg.ChatID)[1].Content; got != "" {
		t.Errorf("stored %q for a turn stopped before any text arrived", got)
	}
}

// A model server that dies mid-answer leaves the partial text behind, labelled.
// Losing prose the user read is worse than showing it with a warning — and the error
// still reaches the transport, because the turn genuinely failed.
func TestFailedStreamKeepsThePartialAnswer(t *testing.T) {
	svc, db, model := askWiring(t,
		toolReply("f1", "query_knowledge_base", `{"keywords":"smith","query":"what did smith measure"}`),
		toolReply("f2", "finalize_research_response", `{"indices":[1]}`),
	)
	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-fail", "Smith measured 4.2 K.")

	model.answer = []string{"Smith measured "}
	model.streamErr = errors.New("llm: connection reset")

	_, err := svc.Ask(context.Background(), nb, "", "What did Smith measure?", AskEvents{})
	if err == nil {
		t.Fatal("a failed stream must still report the failure")
	}

	list := rows(t, db, listChatID(t, db))
	if len(list) != 2 {
		t.Fatalf("%d rows", len(list))
	}
	got := list[1]
	if got.Status != domain.MessageInterrupted {
		t.Errorf("status = %q, want interrupted", got.Status)
	}
	if got.Content != "Smith measured " {
		t.Errorf("content = %q, want the partial answer kept", got.Content)
	}
	if len(got.Citations) != 1 {
		t.Errorf("citations = %+v, want the evidence still attached", got.Citations)
	}
}

func TestAskRefusesWhatItCannotAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		question string
		models   Models
		want     error
	}{
		"empty question": {question: "   ", want: ErrInvalid},
		"no chat server": {question: "anything", models: Models{ChatModel: "m"}, want: ErrInvalid},
		"no chat model": {question: "anything",
			models: Models{Probe: &askProbe{}, Chat: &askModel{t: t}}, want: ErrInvalid},
		"no embedding server": {question: "anything",
			models: Models{ChatModel: "m", Chat: &askModel{t: t}}, want: ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			svc, db, _ := testServiceWired(t, &fakeProcessor{}, func(config.Config) (Embedder, error) {
				return &fakeEmbedder{}, nil
			})
			nb := notebookID(t, svc)
			svc.chats, svc.search = db, db

			models := tc.models
			svc.models = func(config.Config) (Models, error) { return models, nil }

			_, err := svc.Ask(context.Background(), nb, "", tc.question, AskEvents{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// Ask creates the conversation when the caller did not name one, and titles it after
// the question, because that is what the user will remember it by.
func TestAskCreatesAChatNamedAfterTheQuestion(t *testing.T) {
	svc, db, _ := askWiring(t,
		toolReply("t1", "query_knowledge_base", `{"keywords":"x","query":"why does the long tail fall"}`),
		toolReply("t2", "finalize_research_response", `{"indices":[1]}`),
	)
	nb := notebookID(t, svc)
	seedReady(t, db, nb, "sha-title", "Because of the long tail.")

	question := "Why   does\n the long tail fall so slowly in a dense corpus, and does the same hold for images?"
	msg, _, _ := ask(t, svc, nb, "", question)

	chats, err := svc.Chats(context.Background(), nb)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 {
		t.Fatalf("%d chats, want the one this question created", len(chats))
	}
	if chats[0].ID != msg.ChatID {
		t.Errorf("chat = %q, want the message's own", chats[0].ID)
	}
	if !strings.HasPrefix(chats[0].Title, "Why does the long tail fall") {
		t.Errorf("title = %q, want the question on one line", chats[0].Title)
	}
	if len([]rune(chats[0].Title)) > 73 {
		t.Errorf("title is %d runes, too long for a list: %q", len([]rune(chats[0].Title)), chats[0].Title)
	}
}

func TestAskDoesNotCrossNotebooks(t *testing.T) {
	svc, db, _ := askWiring(t)
	a := notebookID(t, svc)
	b := notebookID(t, svc)

	// A chat belonging to a, named in a request for b, reads as absent rather than as
	// somebody else's.
	chat, err := svc.NewChat(context.Background(), a, "in a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Ask(context.Background(), b, chat.ID, "hello?", AskEvents{}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := svc.ChatMessages(context.Background(), b, chat.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ChatMessages across notebooks = %v", err)
	}
	if _, err := svc.Ask(context.Background(), "no-such-notebook", "", "hello?", AskEvents{}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown notebook = %v", err)
	}

	// Deleting it leaves the source and the other notebook alone.
	if err := svc.DeleteChat(context.Background(), a, chat.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Chats(context.Background(), a); len(got) != 0 {
		t.Errorf("%d chats after delete", len(got))
	}
	if err := svc.DeleteChat(context.Background(), b, chat.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleting across notebooks = %v", err)
	}
	_ = db
}

// A conversation's title is the first question until a reader says otherwise. The trim
// is the same one RenameNotebook makes; the blank is refused because a chat with no
// name is the thing the list exists to prevent, and a chat named in the wrong notebook
// is absent here too, as everywhere else in this file.
func TestRenameChat(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := askWiring(t)
	a := notebookID(t, svc)
	b := notebookID(t, svc)

	chat, err := svc.NewChat(ctx, a, "Why does the long tail fall off so slowly?")
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.RenameChat(ctx, a, chat.ID, "  Why the tail is long  "); err != nil {
		t.Fatalf("RenameChat: %v", err)
	}
	title := func() string {
		t.Helper()
		list, err := svc.Chats(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 {
			t.Fatalf("%d chats", len(list))
		}
		return list[0].Title
	}
	if title() != "Why the tail is long" {
		t.Errorf("title = %q", title())
	}

	if err := svc.RenameChat(ctx, a, chat.ID, "   "); !errors.Is(err, ErrInvalid) {
		t.Errorf("blank title = %v, want ErrInvalid", err)
	}
	if title() != "Why the tail is long" {
		t.Errorf("a rejected rename changed the title to %q", title())
	}

	if err := svc.RenameChat(ctx, b, chat.ID, "elsewhere"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("renaming across notebooks = %v, want ErrNotFound", err)
	}
	if err := svc.RenameChat(ctx, a, "no-such-chat", "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("renaming an absent chat = %v, want ErrNotFound", err)
	}
	if title() != "Why the tail is long" {
		t.Errorf("a rejected rename changed the title to %q", title())
	}
}

// The two packages name roles with the same strings, and the history builders key off
// that. It is cheap to pin and expensive to get wrong: a role that fails to map is a
// silently missing turn, which reads as a model that forgot.
func TestRoleVocabulariesAgree(t *testing.T) {
	if string(domain.RoleUser) != string(llm.RoleUser) ||
		string(domain.RoleAssistant) != string(llm.RoleAssistant) {
		t.Errorf("domain roles %q/%q do not match llm %q/%q",
			domain.RoleUser, domain.RoleAssistant, llm.RoleUser, llm.RoleAssistant)
	}
}

// ── helpers ──────────────────────────────────────────────────────

func notebookID(t *testing.T, svc *Service) string {
	t.Helper()
	n, err := svc.CreateNotebook(context.Background(), "nb-"+t.Name())
	if err != nil {
		t.Fatal(err)
	}
	return n.ID
}

func listChatID(t *testing.T, db *sqlite.DB) string {
	t.Helper()
	var id string
	if err := db.Raw().QueryRowContext(context.Background(),
		`SELECT chat_id FROM messages LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// textContent flattens whatever a message's content arrived as: a string for text,
// the parts for anything multimodal.
func textContent(m llm.Message) string {
	if s, ok := m.Content.(string); ok {
		return s
	}
	var b strings.Builder
	if parts, ok := m.Content.([]llm.ContentPart); ok {
		for _, p := range parts {
			b.WriteString(p.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func textContentOf(req llm.ChatRequest, i int) string { return textContent(req.Messages[i]) }
