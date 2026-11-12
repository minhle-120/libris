// Package chat runs the research loop: a tool-calling model that decides what to
// look for, and a synthesiser that writes the answer from what was found.
//
// This file is the half the model can see — the three tools it may call. The loop,
// the prompts and the synthesis call are separate, and retrieval is not here at all:
// chat takes a *retrieve.Retriever and a *retrieve.Registry, which is the only way
// the answer path can be driven from a recorded transcript without a model.
//
// Two rules shape everything below.
//
// First, a model-shaped problem never raises. Bad JSON, missing keys, an unknown
// tool, an empty result — each is answered with text the model can act on, because
// a turn the user waited thirty seconds for must not be lost to a stray brace. Only
// infrastructure fails: a refused port, a dead embedding server.
//
// Second, grounding is enforced by structure rather than by instruction. The first
// tool call has to be a search, and the only ways to end a turn are the two terminal
// tools — so "answered without looking" is not a shape this loop can produce.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"libris/internal/llm"
	"libris/internal/retrieve"
)

// The names the model is given. They are quoted back to it in the gate message and
// in the evaluation note, so they are prompt text, not identifiers, and they are
// carried over from the reference verbatim: three places naming one tool is one
// place too many to disagree.
const (
	ToolSearch   = "query_knowledge_base"
	ToolFinalize = "finalize_research_response"
	ToolNoInfo   = "no_info_found"
)

// Kind is what a tool call did to the turn.
type Kind int

const (
	// Continue means: append the tool message and ask the model again.
	Continue Kind = iota
	// Synthesize means: the turn is over and the answer comes from these indices.
	Synthesize
	// NoInfo means: the turn is over and the answer is Reason.
	NoInfo
)

// Call is one tool call exactly as the model emitted it: name plus the raw argument
// JSON, which is not assumed to be valid.
type Call struct {
	ID        string
	Name      string
	Arguments string
}

// Reply is what the transcript gains and what the caller does next.
//
// Content is the body of the tool message. It is empty for both terminal tools,
// which have no content to return — the caller still closes the call, because a
// transcript with a tool call and no matching tool message is not a transcript
// anything can replay.
type Reply struct {
	Content string
	Kind    Kind

	// Indices are the chunk labels the model chose, as it wrote them. They are
	// resolved to evidence by the caller, not here: finalize returns indices and
	// nothing else, which is what keeps the two-call design two calls.
	Indices []int

	// Reason is the model's own account of why the library cannot answer. It is
	// the user-visible answer for that turn, so it is carried verbatim.
	Reason string
}

// Tools are the three calls bound to one turn: one registry to mint labels in, one
// corpus to search, one question to rank against.
type Tools struct {
	retrieve   *retrieve.Retriever
	registry   *retrieve.Registry
	notebookID string
	question   string
	opts       retrieve.Options

	searched bool
	calls    int
}

// NewTools binds the tools to a turn. question is the user's own words: it is the
// reranking objective, and the evaluation note quotes it back at the model, so it
// is passed through untouched rather than paraphrased on the way in.
func NewTools(r *retrieve.Retriever, reg *retrieve.Registry, notebookID, question string, opts retrieve.Options) *Tools {
	return &Tools{
		retrieve:   r,
		registry:   reg,
		notebookID: notebookID,
		question:   question,
		opts:       opts,
	}
}

// Calls reports how many tool calls have been executed, which is not how many
// iterations the loop has spent: one reply may carry several calls.
func (t *Tools) Calls() int { return t.calls }

// Searched reports whether a real search has happened. It is the gate's state and
// the invariant underneath the salvage path: evidence exists ⇒ the model looked.
func (t *Tools) Searched() bool { return t.searched }

// ── schemas ──────────────────────────────────────────────────────

type searchArgs struct {
	Keywords string `json:"keywords" desc:"Words naming what to look for, for the initial vector retrieval. Two to six terms, not a sentence: a full question embedded as a probe retrieves the average of everything it mentions. Example: coastal erosion rate"`
	Query    string `json:"query" desc:"The question or task these passages should answer, in a full sentence, used to rerank the retrieved results for best relevance"`
}

type finalizeArgs struct {
	Indices intList `json:"indices" desc:"The indices of the chunks (e.g. [[Chunk 1]]) that are most relevant to the user query"`
}

type noInfoArgs struct {
	Reason string `json:"reason" desc:"A brief explanation of what was searched and why no information was found"`
}

// Schemas are the tools' definitions. Static: an argument the model is shown is
// never a knob that differs between turns, and the loop sends all three every time
// with tool_choice required.
func Schemas() ([]llm.Tool, error) {
	specs := []struct {
		name, desc string
		args       any
	}{
		{ToolSearch,
			"Search the library for relevant information using keywords for retrieval and a query for reranking. Always start by calling this tool first.",
			searchArgs{}},
		{ToolFinalize,
			"Call this when you have finished your research and want to provide the final textual answer. Use this ONLY for summaries and analysis.",
			finalizeArgs{}},
		{ToolNoInfo,
			"Call this when you have searched the library and found NO relevant information to answer the user query. This stops the research process and informs the user.",
			noInfoArgs{}},
	}

	out := make([]llm.Tool, 0, len(specs))
	for _, s := range specs {
		schema, err := jsonSchema(s.args)
		if err != nil {
			return nil, err
		}
		tool, err := llm.NewTool(s.name, s.desc, schema)
		if err != nil {
			return nil, err
		}
		out = append(out, tool)
	}
	return out, nil
}

// ── dispatch ─────────────────────────────────────────────────────

// Execute runs one call and says what happened to the turn.
//
// The only error it returns is an infrastructure failure. Everything the model could
// get wrong — the name, the arguments, calling a terminal tool before searching, a
// search that found nothing — comes back as a tool message it can respond to.
func (t *Tools) Execute(ctx context.Context, call Call) (Reply, error) {
	t.calls++

	// The mandatory-first-search gate. Small models habitually answer from memory,
	// or reach for the satisfying tool first, and the result is an ungrounded answer
	// that looks researched. It is enforced as a refusal-shaped tool message rather
	// than an abort: the model reads the scolding on the next iteration and complies,
	// and it never unlocks the gate, so the search still has to happen. That costs one
	// iteration, which is the intended price of not trusting a small model to start
	// correctly.
	if !t.searched && call.Name != ToolSearch {
		return Reply{Content: gateRefusal}, nil
	}

	switch call.Name {
	case ToolSearch:
		// Set before the search runs: the gate is about intent honoured, and a search
		// that found nothing is still a search. The evaluation note is what turns a
		// one-shot retriever into a loop.
		t.searched = true
		body, err := t.search(ctx, call.Arguments)
		if err != nil {
			return Reply{}, err
		}
		return Reply{Content: body}, nil

	case ToolFinalize:
		var a finalizeArgs
		toolArguments(call.Arguments, &a)
		// No content, no side effect: the answer is written later by the synthesiser
		// out of the resolved chunks. Returning text here would collapse the
		// two-call design into a one-call one, with the research model's prose as the
		// answer it was never asked to write.
		return Reply{Kind: Synthesize, Indices: []int(a.Indices)}, nil

	case ToolNoInfo:
		var a noInfoArgs
		toolArguments(call.Arguments, &a)
		reason := strings.TrimSpace(a.Reason)
		if reason == "" {
			// The reason is the user-visible answer, so an empty one is filled with
			// ours. The model's account is preferred when it gave one; a blank
			// message in the UI is not an outcome worth preserving.
			reason = noInfoFallback
		}
		return Reply{Kind: NoInfo, Reason: reason}, nil
	}

	// An unknown tool is a model-shaped problem: report it, keep the turn. This is
	// also where a tool this build does not have would arrive.
	return Reply{Content: "Error: Unknown tool " + call.Name + ". The available tools are `" +
		ToolSearch + "`, `" + ToolFinalize + "` and `" + ToolNoInfo + "`."}, nil
}

// search is recall → rerank → registration, then the tool message the model reads.
func (t *Tools) search(ctx context.Context, rawArgs string) (string, error) {
	var a searchArgs
	toolArguments(rawArgs, &a)

	if strings.TrimSpace(a.Keywords) == "" {
		// The reference returns a bare "No keywords provided."; the nudge is
		// attached because the model otherwise tends to repeat the call unchanged.
		return noKeywords, nil
	}

	evidence, err := t.retrieve.Search(ctx, t.registry, t.notebookID, a.Keywords, a.Query, t.opts)
	if err != nil {
		if errors.Is(err, retrieve.ErrNoKeywords) {
			return noKeywords, nil
		}
		return "", err
	}
	if len(evidence) == 0 {
		return nothingFound, nil
	}

	// Registration order is relevance; the body is in document order, which is what
	// retrieve.Search already did — the model reads a passage, and the ids still
	// record what the search thought was most useful.
	return retrieve.PlainText(evidence) + "\n\n" + evaluationNote(t.question), nil
}

// ── the text half of the contract ────────────────────────────────
//
// These strings are behaviour, not prose. The evaluation note in particular is the
// loop's engine: nothing in the code forces a second iteration, this nudge does. It
// lives in the tool result rather than the system prompt so it can quote the live
// question, and it is appended to every search for the same reason.

// evaluationNote is appended to every non-empty search result.
func evaluationNote(question string) string {
	return "**Assistant Evaluation Note**: Review the knowledge retrieved above. " +
		"1. Is this sufficient to answer the user query (\"" + question + "\") fully? " +
		"If yes, call `" + ToolFinalize + "` with the indices of the chunks you used. " +
		"2. Do you need more specific details, numbers, or dates that might be in other parts of the library? " +
		"If yes, call `" + ToolSearch + "` again with refined keywords."
}

const (
	gateRefusal = "ERROR: You must call `" + ToolSearch + "` first to gather information from " +
		"the library before using other tools. Please call `" + ToolSearch + "` now."

	nothingFound = "No relevant information found in the library. " +
		"Try `" + ToolSearch + "` again with different, more specific keywords, " +
		"or call `" + ToolNoInfo + "` if the library is unlikely to contain the answer."

	noKeywords = "No keywords provided. Call `" + ToolSearch + "` with a few words " +
		"in keywords and the question in query."

	noInfoFallback = "I searched the library and did not find information to answer that."
)

// ── arguments that survive a real model ──────────────────────────

// intList is a list of integers that accepts what models actually send.
//
// The schema says integer[], and llama.cpp's grammar does not enforce the item type,
// so the same list arrives as [1,3], as ["1","3"], as [1.0], and as ["[[Chunk 2]]"]
// when the model echoes the label it was shown. A decoder that rejected any of
// those would throw away a researched answer, and a decoder that guessed by
// substring would keep [[Chunk 12]] as 1.
type intList []int

func (l *intList) UnmarshalJSON(raw []byte) error {
	// The array itself may be a string ("1, 3") or absent, so it is parsed loosely
	// rather than into []json.RawMessage.
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		var one json.RawMessage
		if err2 := json.Unmarshal(raw, &one); err2 != nil {
			*l = nil
			return nil // the caller treats nil as "no indices"
		}
		items = []json.RawMessage{one}
	}

	out := make([]int, 0, len(items))
	for _, item := range items {
		out = append(out, indexNumbers(string(item))...)
	}
	*l = out
	return nil
}

// indexNumbers pulls the integers out of one array element as the model wrote it.
//
// Splitting on anything that is neither a digit nor a dot handles the useful cases
// at once — "[[Chunk 12]]", "#3", "1, 3", [1, 2] flattened by a server — while
// keeping the dot means "2.0" is one index the model hedged about, not the indices
// 2 and 0. A sign is dropped with the rest: a chunk label is never negative.
func indexNumbers(raw string) []int {
	s := strings.TrimSpace(raw)
	if len(s) >= 2 && s[0] == '"' {
		var quoted string
		if err := json.Unmarshal([]byte(s), &quoted); err != nil {
			return nil
		}
		s = quoted
	}

	var out []int
	token := make([]rune, 0, 8)
	emit := func() {
		if len(token) == 0 {
			return
		}
		// The fraction of "2.0" is the model rounding, not a second number.
		if i := slices.Index(token, '.'); i >= 0 {
			token = token[:i]
		}
		if n, err := strconv.Atoi(string(token)); err == nil {
			out = append(out, n)
		}
		token = token[:0]
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r == '.':
			token = append(token, r)
		default:
			emit()
		}
	}
	emit()
	return out
}

// JSONSchema describes the argument as an integer array, which is what it is meant
// to be. The tolerance is in the decoder, not in what the model is told: showing a
// model a union of integer-or-string invites it to choose.
func (intList) JSONSchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}
}
