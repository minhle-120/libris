package chat

import (
	"context"
	"fmt"
	"strings"

	"libris/internal/llm"
	"libris/internal/retrieve"
)

// Synthesizer writes the answer. It is a separate model call from the research turn,
// and the separation is the design: it sees no tools, no search reasoning and no
// trace — only the question, the conversation, and the chunks the research agent
// pointed at.
//
// Two reasons it stays separate. A model that has just spent four iterations
// reasoning about which passages matter keeps reasoning out loud instead of
// answering, and pays for the privilege in every later turn's context. And the
// evidence set is small on purpose: a turn that retrieved twenty chunks may answer
// from six, which is what makes the loop affordable to run at all.
type Synthesizer struct {
	llm      Streamer
	retrieve *retrieve.Retriever
	model    string
	status   StatusFunc
}

func NewSynthesizer(client Streamer, retriever *retrieve.Retriever, model string, status StatusFunc) *Synthesizer {
	return &Synthesizer{llm: client, retrieve: retriever, model: model, status: status}
}

// Delta is one piece of a streaming answer. Both fields may be empty; a server that
// thinks separately reports its thinking in Reasoning first. It is the thinking and
// not the answer: the caller shows it as progress, stores it in a field of its own
// beside the answer, and never merges it into the prose.
type Delta struct {
	Text      string
	Reasoning string
}

// Answer is what a finished answer amounts to.
type Answer struct {
	// Text is the complete answer, citations included. It is assembled here rather
	// than trusted from the stream, so a caller that showed deltas as they arrived
	// and a caller that saved the final text cannot disagree about what was said.
	Text string

	// Reasoning is whatever the model thought out loud, kept apart so it cannot be
	// mistaken for the answer by anything storing this turn.
	Reasoning string

	Usage *llm.Usage
}

// Write streams the answer for a question, from evidence, with chat history.
//
// yield may be nil, which means the caller only wants the finished text — the same
// prompt either way, so a caller cannot get a different answer by listening.
func (s *Synthesizer) Write(
	ctx context.Context,
	history []llm.Message,
	evidence []retrieve.Evidence,
	question string,
	yield func(Delta) error,
) (*Answer, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, fmt.Errorf("chat: synthesising an empty question")
	}

	user, err := s.knowledge(evidence, question)
	if err != nil {
		return nil, err
	}

	messages := make([]llm.Message, 0, len(history)+2)
	messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: SynthesisSystemPrompt})
	messages = append(messages, history...)
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: user})

	if s.status != nil {
		s.status(fmt.Sprintf("Writing the answer from %d chunk(s)…", len(evidence)))
	}

	var out Answer
	err = s.llm.ChatStream(ctx, llm.ChatRequest{Model: s.model, Messages: messages, Stream: true}, func(c llm.Chunk) error {
		d := Delta{Text: c.Delta.Content, Reasoning: c.Delta.ReasoningContent}
		out.Text += d.Text
		out.Reasoning += d.Reasoning
		if c.Usage != nil {
			u := *c.Usage
			out.Usage = &u
		}
		if yield == nil {
			return nil
		}
		if d.Text == "" && d.Reasoning == "" {
			return nil
		}
		return yield(d)
	})
	if err != nil {
		return nil, fmt.Errorf("chat: synthesis: %w", err)
	}
	return &out, nil
}

// knowledge is the synthesiser's whole world: the evidence block and the question,
// in one user message.
//
// Only the model-selected chunks are pasted, in document order, with their labels
// intact — the labels survive the re-sorting so the [[Chunk N]] the answer cites
// still resolves against the registry the turn was built from. The research trace is
// deliberately absent: reasoning about what to search next is not background
// knowledge, and pasting it would let the answer model repeat a conclusion it never
// found evidence for.
func (s *Synthesizer) knowledge(evidence []retrieve.Evidence, question string) ([]llm.ContentPart, error) {
	parts := []llm.ContentPart{llm.TextPart("### Knowledge Chunks:\n")}

	if len(evidence) == 0 {
		// An empty knowledge block is not an error. The synthesiser's honesty
		// instruction turns it into "the provided documents do not contain…", which
		// is a truer answer than anything this code could invent.
		parts = append(parts, llm.TextPart("(no chunks were retrieved)"))
	} else {
		// One call with the whole set: the labels, the order and the blank lines between
		// chunks are retrieve's to decide, and it is the same call the research tools
		// make — so the answer is written from what the researcher was shown.
		rendered, err := s.retrieve.Render(evidence)
		if err != nil {
			return nil, fmt.Errorf("chat: inline evidence: %w", err)
		}
		parts = append(parts, rendered...)
	}

	parts = append(parts, llm.TextPart("\n\n### User Query:\n"+question))
	return parts, nil
}
