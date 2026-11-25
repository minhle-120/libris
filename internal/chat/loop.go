package chat

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"libris/internal/llm"
	"libris/internal/retrieve"
)

// LLM is everything the research half needs from a model. Declared here, by the
// consumer, so a recorded transcript can drive a turn without a model in the room.
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}

// Streamer is the synthesiser's half: an answer that arrives in pieces.
type Streamer interface {
	ChatStream(ctx context.Context, req llm.ChatRequest, yield func(llm.Chunk) error) error
}

// Config is one turn's budget. It is read from settings when the turn starts and
// carried down, so a setting saved mid-answer cannot move the answer under way.
type Config struct {
	ChatModel     string
	MaxIterations int
	RecallTopK    int
	FinalTopK     int

	// Thinking decides whether the model is allowed to deliberate before it names a
	// search. Off is the default because the loop throws that writing away the moment the
	// tool call lands, and it is paid for on every iteration — unlike the answer turn,
	// where the reader is shown the deliberation and asked to trust it.
	Thinking bool
}

// templateKwargs is how that decision reaches the model. Sent on every turn rather than
// omitted when it agrees with the server, because a switch on screen that only works when
// llama.cpp happens to have been started the same way is not a switch.
func (c Config) templateKwargs() *llm.ChatTemplateKwargs {
	return &llm.ChatTemplateKwargs{EnableThinking: c.Thinking}
}

// Options is the retrieval depth this turn searches at.
func (c Config) retrieval() retrieve.Options {
	return retrieve.Options{RecallTopK: c.RecallTopK, FinalTopK: c.FinalTopK}
}

// Status is how a turn ended. The four are not variations of one outcome: they say
// something different about the library, and the UI draws them differently.
type Status int

const (
	// Synthesize means there is evidence and the answer has to be written from it.
	Synthesize Status = iota
	// NoInfo means the library does not contain the answer, and Answer says so in
	// the model's own words or ours.
	NoInfo
	// RawAnswer means the model answered in prose instead of calling a tool. The
	// prose is the answer, verbatim, and it is not grounded.
	RawAnswer
	// Canceled means the user stopped the turn.
	Canceled
)

func (s Status) String() string {
	switch s {
	case Synthesize:
		return "synthesize"
	case NoInfo:
		return "no-info"
	case RawAnswer:
		return "raw-answer"
	case Canceled:
		return "canceled"
	}
	return "unknown"
}

// Result is a finished research turn.
type Result struct {
	Status Status

	// Answer is the user-visible text for the paths that need no stream: the reason
	// from no_info_found, the depth message, or ungrounded prose. Empty for
	// Synthesize, whose answer is written by the synthesiser.
	Answer string

	// Indices are the labels as the model wrote them, before resolution: the
	// difference between len(Indices) and len(Evidence) is how many it invented.
	Indices []int

	// Evidence is what the answer must be written from: the chunks the model chose,
	// or everything that was found when the budget ran out. In document order,
	// labels intact.
	Evidence []retrieve.Evidence

	// Trace is the model's own transcript for this turn — every assistant reply and
	// tool result, without the system prompt or the seeded question. It is persisted
	// for debugging and never shown to a model again, least of all the synthesiser.
	Trace []llm.Message

	// Iterations counts assistant turns, which is not the same as tool calls: one
	// reply may carry several.
	Iterations int
}

// Research runs the tool-calling half of one question. It is per-question by
// construction: the chunk registry is born with it, so ids restart at [[Chunk 1]]
// for the next question and cannot leak between turns.
type Research struct {
	llm      LLM
	tools    *Tools
	registry *retrieve.Registry
	cfg      Config
	status   StatusFunc

	schemas []llm.Tool
	serr    error
}

// StatusFunc reports progress. It is called from the turn's goroutine and may be
// nil, which means the caller polls instead of listening.
type StatusFunc func(text string)

// NewResearch wires one turn. The Retriever is shared between turns — it holds no
// per-turn state — while the registry does not travel at all.
func NewResearch(client LLM, retriever *retrieve.Retriever, cfg Config, status StatusFunc) *Research {
	reg := retrieve.NewRegistry()
	schemas, serr := Schemas()
	return &Research{
		llm:      client,
		registry: reg,
		tools:    NewTools(retriever, reg, "", "", cfg.retrieval()),
		cfg:      cfg,
		status:   status,
		schemas:  schemas,
		serr:     serr,
	}
}

// Registry exposes the turn's evidence store, so a caller can resolve a citation the
// model wrote without re-running anything.
func (r *Research) Registry() *retrieve.Registry { return r.registry }

// Ask runs the loop.
//
// The transcript is append-only across iterations, which is what gives the model
// cross-iteration memory: by iteration three it can see that a refined query hit the
// same page it already has, and drill somewhere else instead. That costs context,
// roughly maxIterations × (turn + finalTopK × chunk tokens), and the two cheapest
// levers when the window is tight are finalTopK then maxIterations — not truncating
// older tool results, which removes exactly the memory being paid for.
func (r *Research) Ask(ctx context.Context, notebookID, question, history string) (*Result, error) {
	if r.serr != nil {
		return nil, fmt.Errorf("chat: tool schemas: %w", r.serr)
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, errors.New("chat: asked a question with no words in it")
	}

	// The tools are bound to this question: the evaluation note quotes it back at the
	// model after every search.
	r.tools.notebookID, r.tools.question = notebookID, question
	r.registry.Reset() // a fresh Research never has evidence to reset; this is for reuse
	r.report("Starting research…")

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: ResearchSystemPrompt},
		{Role: llm.RoleUser, Content: seedContext(history, question)},
	}
	traceFrom := len(messages)

	iterations := 0
	for range r.cfg.MaxIterations {
		if stopped(ctx) {
			return &Result{Status: Canceled, Iterations: iterations, Trace: cloneMessages(messages[traceFrom:])}, nil
		}
		iterations++
		r.report(fmt.Sprintf("Analyzing context (iteration %d)…", iterations))

		resp, err := r.llm.Chat(ctx, llm.ChatRequest{
			Model:    r.cfg.ChatModel,
			Messages: messages,
			Tools:    r.schemas,
			// Required, and not relaxed: with "auto" a model answers from memory,
			// which is the one failure the whole loop exists to prevent. The prose
			// fallback below is for the models that ignore this anyway.
			ToolChoice: llm.ToolChoiceRequired,
			// The turn's whole output is a tool call, so this is the one place in
			// the answer path where a model's private deliberation buys nothing —
			// see Config.Thinking for why the switch is off unless it is asked for.
			ChatTemplateKwargs: r.cfg.templateKwargs(),
		})
		if err != nil {
			// A turn the user stopped is not a failure, and reporting it as one
			// would put an error where their own click belongs. A timeout is a
			// failure and stays one.
			if stopped(ctx) {
				return &Result{Status: Canceled, Iterations: iterations, Trace: cloneMessages(messages[traceFrom:])}, nil
			}
			return nil, fmt.Errorf("chat: research iteration %d: %w", iterations, err)
		}
		if resp == nil || len(resp.Choices) == 0 {
			return nil, fmt.Errorf("chat: research iteration %d: the chat server returned no choices", iterations)
		}

		reply := resp.Choices[0].Message
		messages = append(messages, reply)

		if len(reply.ToolCalls) == 0 {
			// Local models under load ignore tool_choice. Their prose is not
			// grounded — it is the model answering from the question alone — so it is
			// returned verbatim and marked as what it is rather than quietly passed
			// off as a cited answer.
			return &Result{Status: RawAnswer, Answer: textOf(reply), Iterations: iterations, Trace: cloneMessages(messages[traceFrom:])}, nil
		}

		for _, tc := range reply.ToolCalls {
			call := Call{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}
			toolReply, err := r.tools.Execute(ctx, call)
			if err != nil {
				if stopped(ctx) {
					return &Result{Status: Canceled, Iterations: iterations, Trace: cloneMessages(messages[traceFrom:])}, nil
				}
				return nil, err
			}
			if toolReply.Status != "" {
				r.report(toolReply.Status)
			}

			if toolReply.Kind != Continue {
				// A terminal call returns immediately, dropping any later calls in
				// this reply: they were written against evidence the model has since
				// decided is enough, and running them would mint labels for chunks
				// nothing reads.
				messages = append(messages, toolMessage(call, ""))
				return r.finish(toolReply, messages[traceFrom:], iterations), nil
			}

			messages = append(messages, toolMessage(call, toolReply.Content))
		}
	}

	// The budget is spent. Everything found is still evidence, so a model that kept
	// refining until it ran out produces a grounded answer rather than an error. This
	// is safe because of the gate: evidence exists only if the model looked.
	all := r.registry.All()
	if len(all) == 0 {
		return &Result{Status: NoInfo, Answer: depthMessage, Iterations: iterations, Trace: cloneMessages(messages[traceFrom:])}, nil
	}
	indices := make([]int, len(all))
	for i, e := range all {
		indices[i] = e.Index
	}
	r.report(fmt.Sprintf("Research budget spent: answering from all %d chunks found.", len(all)))
	return &Result{
		Status:     Synthesize,
		Indices:    indices,
		Evidence:   r.registry.Only(indices),
		Iterations: iterations,
		Trace:      cloneMessages(messages[traceFrom:]),
	}, nil
}

// finish resolves what the model pointed at. Nothing is pasted from here: the
// synthesiser receives the Evidence in its own prompt, and the research trace never
// reaches it.
func (r *Research) finish(reply Reply, trace []llm.Message, iterations int) *Result {
	trace = cloneMessages(trace)
	switch reply.Kind {
	case Finalized:
		evidence := r.registry.Only(reply.Indices)
		if len(reply.Indices) > 0 && len(evidence) == 0 {
			// Every label was invented. Not a crash: the synthesiser's honesty
			// instruction covers an empty knowledge block, and an error here would
			// lose the turn entirely.
			r.report("The research agent cited chunks that do not exist; answering without evidence.")
		}
		return &Result{
			Status:     Synthesize,
			Indices:    reply.Indices,
			Evidence:   evidence,
			Iterations: iterations,
			Trace:      trace,
		}
	case GaveUp:
		return &Result{Status: NoInfo, Answer: reply.Reason, Iterations: iterations, Trace: trace}
	}
	return &Result{Status: RawAnswer, Iterations: iterations, Trace: trace}
}

func (r *Research) report(text string) {
	if r.status != nil {
		r.status(text)
	}
}

// toolMessage closes one tool call. Every call in the transcript gets one, including
// the terminal tools whose content is empty — see CloseTrace.
func toolMessage(call Call, content string) llm.Message {
	return llm.Message{Role: llm.RoleTool, Content: content, ToolCallID: call.ID, Name: call.Name}
}

// CloseTrace turns the trailing tool message into the answer, which is how a
// persisted trace stays a valid transcript: the finalize call the model ended with
// is closed by the answer the turn produced, so every tool call in the file has a
// matching tool message and no tool message floats without one.
//
// It returns the trace unchanged if the last message is not an empty tool result,
// because a caller that reorders or trims before persisting deserves to find out
// here rather than in a viewer.
func CloseTrace(trace []llm.Message, answer string) []llm.Message {
	if len(trace) == 0 {
		return trace
	}
	last := trace[len(trace)-1]
	if last.Role != llm.RoleTool {
		return trace
	}
	if text, ok := last.Content.(string); ok && text != "" {
		return trace
	}
	trace[len(trace)-1].Content = answer
	return trace
}

func cloneMessages(m []llm.Message) []llm.Message { return slices.Clone(m) }

// stopped reports whether the user stopped this turn. A deadline is deliberately
// excluded: running out of time is a failure the UI should say so about, while
// pressing stop is a request that deserves the outcome it asked for.
func stopped(ctx context.Context) bool { return errors.Is(ctx.Err(), context.Canceled) }

// textOf reads a message's text without assuming how the server chose to send it.
// Content is deliberately `+"`any`"+` on the wire — a string, a list of content parts,
// or absent when the reply was only a tool call — and a type assertion here would
// turn a server's choice into a panic in the middle of a user's turn.
func textOf(m llm.Message) string {
	switch c := m.Content.(type) {
	case string:
		return c
	case []llm.ContentPart:
		var b strings.Builder
		for _, p := range c {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}
