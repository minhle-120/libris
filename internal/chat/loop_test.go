package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"libris/internal/domain"
	"libris/internal/llm"
	"libris/internal/retrieve"
)

// The loop is driven by a scripted model, exactly as the reference recommends: no
// model in the room, assertions on how the turn ended, what the transcript holds and
// what the next call would have seen.

type fakeLLM struct {
	replies  []llm.ChatResponse
	requests []llm.ChatRequest
	calls    int
	err      error
}

func (f *fakeLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.calls++
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.calls > len(f.replies) {
		return nil, fmt.Errorf("fake model ran out of replies after %d calls: the loop did not stop when it should have", f.calls)
	}
	reply := f.replies[f.calls-1]
	return &reply, nil
}

func toolCallReply(calls ...llm.ToolCall) llm.ChatResponse {
	return llm.ChatResponse{Choices: []llm.ChatChoice{{
		Message:      llm.Message{Role: llm.RoleAssistant, ToolCalls: calls},
		FinishReason: "tool_calls",
	}}}
}

func textReply(text string) llm.ChatResponse {
	return llm.ChatResponse{Choices: []llm.ChatChoice{{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: text},
		FinishReason: "stop",
	}}}
}

func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Type: "function", Function: llm.FunctionCall{Name: name, Arguments: args}}
}

func searchCall(id, keywords string) llm.ToolCall {
	return call(id, ToolSearch, `{"keywords":"`+keywords+`","query":"why?"}`)
}

func defaultConfig() Config {
	return Config{ChatModel: "test-chat", MaxIterations: 5, RecallTopK: 10, FinalTopK: 5}
}

type turn struct {
	res      *Result
	err      error
	status   []string
	llm      *fakeLLM
	corpus   *stubCorpus
	research *Research
}

func (t *turn) evidenceText() string {
	var b strings.Builder
	for _, e := range t.res.Evidence {
		b.WriteString(e.Segment.ID)
		b.WriteString(" ")
	}
	return b.String()
}

func run(t *testing.T, corpus *stubCorpus, replies ...llm.ChatResponse) *turn {
	t.Helper()
	model := &fakeLLM{replies: replies}
	var status []string
	ret := retrieve.New(corpus, &stubEmbed{}, nil, nil)
	r := NewResearch(model, ret, defaultConfig(), func(s string) { status = append(status, s) })
	res, err := r.Ask(context.Background(), "nb1", "How fast is the coastline retreating?", "")
	if err != nil {
		t.Helper()
	}
	return &turn{res: res, err: err, status: status, llm: model, corpus: corpus, research: r}
}

func mustResult(t *testing.T, tr *turn) *Result {
	t.Helper()
	if tr.err != nil {
		t.Fatalf("the turn raised: %v", tr.err)
	}
	if tr.res == nil {
		t.Fatal("no result and no error")
	}
	return tr.res
}

// ── the six ways a turn ends ─────────────────────────────────────

// Path 1: search, then finalize on two of the three chunks it found.
func TestSearchThenFinalize(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{
		textChunk("doc:0000", "doc", 0, "3mm a year"),
		textChunk("doc:0005", "doc", 5, "since 1990"),
		textChunk("doc:0009", "doc", 9, "unneeded"),
	}}
	tr := run(t, corpus,
		toolCallReply(searchCall("c1", "coastal erosion")),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[1,3]}`)))

	res := mustResult(t, tr)
	if res.Status != Synthesize {
		t.Fatalf("Status = %v, want Synthesize", res.Status)
	}
	if len(res.Indices) != 2 || res.Indices[0] != 1 || res.Indices[1] != 3 {
		t.Errorf("indices = %v, want [1 3]", res.Indices)
	}
	// The answer is written from the two the model chose, not from all three it saw.
	if len(res.Evidence) != 2 || res.Evidence[0].Segment.ID != "doc:0000" || res.Evidence[1].Segment.ID != "doc:0009" {
		t.Errorf("evidence = %s, want the two cited chunks", tr.evidenceText())
	}
	if res.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", res.Iterations)
	}
}

// Path 2: no_info_found, with the model's reason kept word for word — it is the
// answer the user reads.
func TestNoInfoFound(t *testing.T) {
	const reason = "Searched for tidal gauges and 2020 surveys; the library has neither."
	tr := run(t, &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "waves")}},
		toolCallReply(searchCall("c1", "tidal gauge")),
		toolCallReply(call("c2", ToolNoInfo, `{"reason":"`+reason+`"}`)))

	res := mustResult(t, tr)
	if res.Status != NoInfo {
		t.Fatalf("Status = %v, want NoInfo", res.Status)
	}
	if res.Answer != reason {
		t.Errorf("the reason was rewritten: %q", res.Answer)
	}
	if len(res.Evidence) != 0 {
		t.Errorf("a no-info turn carries %d chunks of evidence", len(res.Evidence))
	}
}

// Path 3: the budget runs out with chunks in hand, so the turn still answers from
// everything found instead of failing. This is the safety net.
func TestBudgetSpentStillSynthesizesFromEverything(t *testing.T) {
	corpus := &stubCorpus{batches: [][]domain.Hit{
		{textChunk("a:0000", "a", 0, "first")},
		{textChunk("b:0000", "b", 0, "second")},
		{textChunk("c:0000", "c", 0, "third")},
	}}
	var replies []llm.ChatResponse
	for i := range 5 {
		replies = append(replies, toolCallReply(searchCall(fmt.Sprintf("s%d", i), "more")))
	}

	tr := run(t, corpus, replies...)
	res := mustResult(t, tr)
	if res.Status != Synthesize {
		t.Fatalf("Status = %v, want Synthesize: a model that keeps refining must not cost the user the answer", res.Status)
	}
	if len(res.Evidence) != 3 {
		t.Errorf("salvaged %d chunks, want all 3 found: %s", len(res.Evidence), tr.evidenceText())
	}
	if res.Iterations != 5 {
		t.Errorf("Iterations = %d, want 5", res.Iterations)
	}
	if !slices.Contains(tr.status, "The research budget spent: answering from all 3 chunks found.") &&
		!containsContaining(tr.status, "budget spent") {
		t.Errorf("the user was not told the answer was salvaged: %v", tr.status)
	}
}

// Path 4: five empty searches is an honest "it is not here", in our words.
func TestBudgetSpentWithNothingFound(t *testing.T) {
	var replies []llm.ChatResponse
	for i := range 5 {
		replies = append(replies, toolCallReply(searchCall(fmt.Sprintf("s%d", i), "quantum")))
	}

	tr := run(t, &stubCorpus{}, replies...)
	res := mustResult(t, tr)
	if res.Status != NoInfo {
		t.Fatalf("Status = %v, want NoInfo", res.Status)
	}
	if !strings.Contains(res.Answer, "maximum research depth") {
		t.Errorf("Answer = %q, want the depth message", res.Answer)
	}
	if tr.llm.calls != 5 {
		t.Errorf("%d model calls, want exactly the budget of 5", tr.llm.calls)
	}
}

// Path 5: the model answered in prose. tool_choice says required; local models under
// load ignore it, so this is handled — and the prose is marked as what it is.
func TestProseInsteadOfAToolCall(t *testing.T) {
	const prose = "Coastal erosion averages about 3mm a year, I believe."
	tr := run(t, &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}, textReply(prose))

	res := mustResult(t, tr)
	if res.Status != RawAnswer {
		t.Fatalf("Status = %v, want RawAnswer", res.Status)
	}
	if res.Answer != prose {
		t.Errorf("prose was altered: %q", res.Answer)
	}
	if len(res.Evidence) != 0 {
		t.Errorf("an ungrounded answer carries %d chunks it never retrieved", len(res.Evidence))
	}
	if tr.llm.calls != 1 || tr.corpus.calls != 0 {
		t.Errorf("%d model calls and %d searches: a prose answer must end the turn immediately", tr.llm.calls, tr.corpus.calls)
	}
}

// Path 6: canceled at the top of an iteration.
func TestCanceledBeforeTheFirstCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	model := &fakeLLM{replies: []llm.ChatResponse{toolCallReply(searchCall("c1", "x"))}}
	r := NewResearch(model, retrieve.New(&stubCorpus{}, &stubEmbed{}, nil, nil), defaultConfig(), nil)
	res, err := r.Ask(ctx, "nb1", "q", "")
	if err != nil {
		t.Fatalf("cancellation raised: %v", err)
	}
	if res.Status != Canceled {
		t.Errorf("Status = %v, want Canceled", res.Status)
	}
	if model.calls != 0 {
		t.Errorf("%d model calls after cancellation", model.calls)
	}
}

// Cancellation is checked per iteration, but the model call takes the context too,
// so a user who stops a turn during a thirty-second CPU turn does not wait for it.
func TestCanceledDuringAModelCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	model := &cancelOnSecondCall{cancel: cancel}
	r := NewResearch(model, retrieve.New(&stubCorpus{}, &stubEmbed{}, nil, nil), defaultConfig(), nil)
	res, err := r.Ask(ctx, "nb1", "q", "")
	if err != nil {
		t.Fatalf("cancellation raised: %v", err)
	}
	if res.Status != Canceled {
		t.Errorf("Status = %v, want Canceled", res.Status)
	}
}

// cancelOnSecondCall is the user pressing stop while a model turn is in flight: the
// context is canceled from under the loop, and the call comes back failed.
type cancelOnSecondCall struct {
	n      int
	cancel context.CancelFunc
}

func (c *cancelOnSecondCall) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	c.n++
	if c.n == 2 {
		c.cancel()
		return nil, context.Canceled
	}
	resp := toolCallReply(searchCall("c1", "nothing"))
	return &resp, nil
}

// ── the gate, the unknown and the malformed ──────────────────────

// Path A2: a model that reaches for finalize first is refused in a tool message,
// the registry stays empty, and the search follows on the next iteration.
func TestGateSendsTheModelBackInsteadOfAborting(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "3mm a year")}}
	tr := run(t, corpus,
		toolCallReply(call("c1", ToolFinalize, `{"indices":[1]}`)),
		toolCallReply(searchCall("c2", "erosion")),
		toolCallReply(call("c3", ToolFinalize, `{"indices":[1]}`)))

	res := mustResult(t, tr)
	if res.Status != Synthesize {
		t.Fatalf("Status = %v, want Synthesize", res.Status)
	}
	if corpus.calls != 1 {
		t.Errorf("%d searches, want exactly the one the model made after the refusal", corpus.calls)
	}
	if res.Iterations != 3 {
		t.Errorf("Iterations = %d, want 3: the refusal costs an iteration, which is its price", res.Iterations)
	}

	var refusals int
	for _, m := range res.Trace {
		text := textOf(m)
		if !strings.Contains(text, "must call `"+ToolSearch+"` first") {
			continue
		}
		refusals++
		// The refusal is a scolding, not a shortcut: it must not hand over evidence
		// the model had to search for, or the gate teaches the model to skip searching.
		if strings.Contains(text, "[[Chunk") {
			t.Errorf("the refusal leaked evidence: %q", text)
		}
	}
	if refusals != 1 {
		t.Errorf("%d refusals, want exactly 1 for a model that misbehaved once", refusals)
	}
}

// A hallucinated index is misremembering, not invented evidence: dropped, and the
// grounded answer survives.
func TestFinalizeWithAHallucinatedIndex(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "3mm a year")}}
	tr := run(t, corpus,
		toolCallReply(searchCall("c1", "erosion")),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[1,99]}`)))

	res := mustResult(t, tr)
	if res.Status != Synthesize {
		t.Fatalf("Status = %v", res.Status)
	}
	if len(res.Indices) != 2 {
		t.Errorf("Indices = %v, want the model's list as written", res.Indices)
	}
	if len(res.Evidence) != 1 {
		t.Errorf("%d chunks resolved from [1 99], want 1", len(res.Evidence))
	}
}

// Every label invented is not a crash either: the synthesiser's honesty instruction
// turns an empty knowledge block into a truthful answer.
func TestFinalizeWithNothingReal(t *testing.T) {
	tr := run(t, &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}},
		toolCallReply(searchCall("c1", "k")),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[7,8]}`)))

	res := mustResult(t, tr)
	if res.Status != Synthesize || len(res.Evidence) != 0 {
		t.Fatalf("%v with %d chunks, want Synthesize with none", res.Status, len(res.Evidence))
	}
	if !containsContaining(tr.status, "do not exist") {
		t.Errorf("nothing was logged about invented citations: %v", tr.status)
	}
}

// An unknown tool is reported to the model and the turn continues.
func TestUnknownToolKeepsTheTurnAlive(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	tr := run(t, corpus,
		toolCallReply(searchCall("c1", "k")),
		toolCallReply(call("c2", "delegate_to_diagram", `{}`)),
		toolCallReply(call("c3", ToolFinalize, `{"indices":[1]}`)))

	res := mustResult(t, tr)
	if res.Status != Synthesize {
		t.Fatalf("Status = %v, want Synthesize", res.Status)
	}
	if !strings.Contains(toolContents(tr.llm), "Unknown tool delegate_to_diagram") {
		t.Errorf("the model was not told the name it got wrong: %q", toolContents(tr.llm))
	}
}

// Malformed arguments are the empty object, and the loop keeps going.
func TestMalformedArgumentsAreNotFatal(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	tr := run(t, corpus,
		toolCallReply(searchCall("c1", "k")),
		toolCallReply(call("c2", ToolFinalize, `{"indices": [`)),
	)

	res := mustResult(t, tr)
	if res.Status != Synthesize {
		t.Fatalf("Status = %v, want Synthesize: a stray brace must not lose a researched answer", res.Status)
	}
	if len(res.Evidence) != 0 {
		t.Errorf("%d chunks resolved from unparseable indices", len(res.Evidence))
	}
	// A search with garbage arguments is refused in text rather than run empty.
	tr2 := run(t, corpus,
		toolCallReply(call("c1", ToolSearch, `{"keywords"`)),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[]}`)))
	if corpus.calls != 1 {
		t.Errorf("%d searches ran on malformed arguments, want 0", corpus.calls)
	}
	if !strings.Contains(toolContents(tr2.llm), "No keywords") {
		t.Errorf("malformed search arguments produced %q", toolContents(tr2.llm))
	}
}

// One reply may carry several calls. They run in array order, and a terminal call
// ends the turn immediately — later calls were written against evidence the model
// has already decided is sufficient.
func TestATerminalCallDropsTheRestOfItsReply(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	tr := run(t, corpus,
		toolCallReply(searchCall("c1", "k")),
		toolCallReply(
			call("c2", ToolFinalize, `{"indices":[1]}`),
			searchCall("c3", "another"),
		))

	res := mustResult(t, tr)
	if res.Status != Synthesize {
		t.Fatalf("Status = %v", res.Status)
	}
	if corpus.calls != 1 {
		t.Errorf("%d searches, want 1: the search after finalize was never wanted", corpus.calls)
	}
}

// ── ids, transcripts and settings ────────────────────────────────

// Chunk ids are per turn. Carrying them across turns would let a citation in old
// chat history point at a chunk from a later question.
func TestIdsRestartEachTurn(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	first := run(t, corpus,
		toolCallReply(searchCall("c1", "one")),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[1]}`)))
	second := run(t, corpus,
		toolCallReply(searchCall("c3", "two")),
		toolCallReply(call("c4", ToolFinalize, `{"indices":[1]}`)))

	if first.res.Evidence[0].Index != 1 || second.res.Evidence[0].Index != 1 {
		t.Errorf("ids %d and %d, want both 1", first.res.Evidence[0].Index, second.res.Evidence[0].Index)
	}

	// Including when one object is reused: the registry is reset, not carried.
	model := &fakeLLM{replies: []llm.ChatResponse{
		toolCallReply(searchCall("a", "one")),
		toolCallReply(call("b", ToolFinalize, `{"indices":[1]}`)),
		toolCallReply(searchCall("c", "two")),
		toolCallReply(call("d", ToolFinalize, `{"indices":[1]}`)),
	}}
	r := NewResearch(model, retrieve.New(corpus, &stubEmbed{}, nil, nil), defaultConfig(), nil)
	if _, err := r.Ask(context.Background(), "nb1", "q1", ""); err != nil {
		t.Fatal(err)
	}
	res, err := r.Ask(context.Background(), "nb1", "q2", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Evidence) != 1 || res.Evidence[0].Index != 1 {
		t.Errorf("a reused turn carried %v, want one chunk labelled [[Chunk 1]]", res.Evidence)
	}
}

// A4/A8 together: a chunk two searches both find is labelled once, and salvage
// counts it once.
func TestRepeatedEvidenceIsLabelledOnceAcrossTurn(t *testing.T) {
	shared := textChunk("d:0000", "d", 0, "the same page")
	corpus := &stubCorpus{batches: [][]domain.Hit{
		{shared, textChunk("d:0001", "d", 1, "unique to the first search")},
		{shared, textChunk("d:0002", "d", 2, "unique to the second")},
	}}
	var replies []llm.ChatResponse
	for i := range 5 {
		replies = append(replies, toolCallReply(searchCall(fmt.Sprintf("s%d", i), "more")))
	}

	tr := run(t, corpus, replies...)
	res := mustResult(t, tr)
	seen := map[int]string{}
	for _, e := range res.Evidence {
		if prev, dup := seen[e.Index]; dup {
			t.Fatalf("[[Chunk %d]] names both %q and %q", e.Index, prev, e.Segment.ID)
		}
		seen[e.Index] = e.Segment.ID
	}
	// Five searches, each finding the shared page plus one new chunk: three labels,
	// not seven. The shared page is [[Chunk 1]] because it was found first.
	if len(seen) != 3 {
		t.Errorf("salvage holds %d chunks, want 3 unique across five overlapping searches", len(seen))
	}
	if seen[1] != "d:0000" {
		t.Errorf("[[Chunk 1]] is %q, want the page every search found", seen[1])
	}
}

// The transcript that gets persisted has to be replayable: every tool call closed,
// no orphan tool messages, and the finalize call closed by the answer itself.
func TestTraceIsAValidTranscript(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	tr := run(t, corpus,
		toolCallReply(searchCall("c1", "erosion")),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[1]}`)))

	res := mustResult(t, tr)
	assertTranscript(t, res.Trace)

	// The seeded prompt and the system prompt are not part of the trace: the model's
	// own work starts at its first reply.
	if res.Trace[0].Role != llm.RoleAssistant {
		t.Errorf("trace starts with %q, want the model's first reply", res.Trace[0].Role)
	}
	for _, m := range res.Trace {
		if m.Role == llm.RoleSystem {
			t.Error("the system prompt leaked into the trace")
		}
	}

	CloseTrace(res.Trace, "The coast retreats 3mm a year [[Chunk 1]].")
	assertTranscript(t, res.Trace)
	last := res.Trace[len(res.Trace)-1]
	if text, ok := last.Content.(string); !ok || !strings.Contains(text, "[[Chunk 1]]") {
		t.Errorf("the finalize call was not closed by the answer: %+v", last)
	}

	// CloseTrace refuses to overwrite a real tool result.
	other := []llm.Message{{Role: llm.RoleTool, Content: "search results", ToolCallID: "c1"}}
	if got := textOf(CloseTrace(other, "overwrite")[0]); got != "search results" {
		t.Errorf("CloseTrace overwrote a tool result: %q", got)
	}
}

func assertTranscript(t *testing.T, trace []llm.Message) {
	t.Helper()
	calls := map[string]bool{}
	answers := map[string]bool{}
	for _, m := range trace {
		for _, tc := range m.ToolCalls {
			if tc.ID == "" {
				t.Error("a tool call has no id, so nothing can answer it")
			}
			if calls[tc.ID] {
				t.Errorf("duplicate tool call id %q", tc.ID)
			}
			calls[tc.ID] = true
		}
		if m.Role != llm.RoleTool {
			continue
		}
		if !calls[m.ToolCallID] {
			t.Errorf("a tool message answers %q, which was never called", m.ToolCallID)
		}
		if answers[m.ToolCallID] {
			t.Errorf("tool call %q was closed twice", m.ToolCallID)
		}
		answers[m.ToolCallID] = true
	}
	for id := range calls {
		if !answers[id] {
			t.Errorf("tool call %q was never closed: the transcript cannot be replayed", id)
		}
	}
}

// tool_choice is required on every research call, with all three tools offered. The
// reference is blunt about why: "auto" lets a model answer from memory, which
// defeats grounding — so the only thing that relaxes it is a code change here.
func TestEveryResearchCallDemandsAToolCall(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	tr := run(t, corpus,
		toolCallReply(searchCall("c1", "k")),
		toolCallReply(searchCall("c2", "k2")),
		toolCallReply(call("c3", ToolFinalize, `{"indices":[1]}`)))
	mustResult(t, tr)

	for i, req := range tr.llm.requests {
		if req.ToolChoice != llm.ToolChoiceRequired {
			t.Errorf("call %d: tool_choice = %q, want required", i, req.ToolChoice)
		}
		if len(req.Tools) != 3 {
			t.Errorf("call %d offers %d tools, want 3", i, len(req.Tools))
		}
		if req.Model != "test-chat" {
			t.Errorf("call %d model = %q", i, req.Model)
		}
		if req.Stream {
			t.Errorf("call %d streams: the research turn is a planning call, its prose is discarded", i)
		}
		first, _ := req.Messages[0].Content.(string)
		if first != ResearchSystemPrompt {
			t.Errorf("call %d did not open with the research prompt", i)
		}
	}
}

// The transcript is append-only across iterations: iteration three sees the earlier
// tool results, which is the memory that lets a model drill deeper instead of
// re-searching what it already has.
func TestTranscriptGrowsAcrossIterations(t *testing.T) {
	corpus := &stubCorpus{batches: [][]domain.Hit{
		{textChunk("d:0000", "d", 0, "unique words from the first search")},
		{textChunk("d:0001", "d", 1, "unique words from the second search")},
	}}
	tr := run(t, corpus,
		toolCallReply(searchCall("c1", "one")),
		toolCallReply(searchCall("c2", "two")),
		toolCallReply(call("c3", ToolFinalize, `{"indices":[1,2]}`)))
	mustResult(t, tr)

	if len(tr.llm.requests) != 3 {
		t.Fatalf("%d calls, want 3", len(tr.llm.requests))
	}
	last := tr.llm.requests[2].Messages
	if n := len(last); n <= len(tr.llm.requests[0].Messages) {
		t.Fatalf("the transcript did not grow: %d then %d messages", len(tr.llm.requests[0].Messages), n)
	}
	if !containsMessage(last, llm.RoleTool, "unique words from the first search") {
		t.Error("iteration 3 cannot see what iteration 1 found: cross-iteration memory is the design, not an accident")
	}
	if containsMessage(last, llm.RoleTool, "Assistant Evaluation Note") == false {
		t.Error("the evaluation note is missing from the transcript the model reasons over")
	}
}

func containsMessage(msgs []llm.Message, role llm.Role, substr string) bool {
	for _, m := range msgs {
		if m.Role == role && strings.Contains(textOf(m), substr) {
			return true
		}
	}
	return false
}

// Two settings the user can see have to reach the request the turn builds: recall
// depth at the corpus, and the iteration budget at the loop.
func TestSettingsShapeTheTurn(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	model := &fakeLLM{replies: []llm.ChatResponse{
		toolCallReply(searchCall("c1", "k")),
		toolCallReply(searchCall("c2", "k again")),
	}}
	cfg := defaultConfig()
	cfg.MaxIterations = 2
	cfg.RecallTopK = 33
	r := NewResearch(model, retrieve.New(corpus, &stubEmbed{}, nil, nil), cfg, nil)
	res, err := r.Ask(context.Background(), "nb1", "q", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Iterations != 2 || model.calls != 2 {
		t.Errorf("%d iterations for a budget of 2 (model called %d times)", res.Iterations, model.calls)
	}
	if res.Status != Synthesize || len(res.Evidence) != 1 {
		t.Errorf("%v with %d chunks: the budget spent with chunks in hand salvages", res.Status, len(res.Evidence))
	}
	if corpus.gotLimit != 33 {
		t.Errorf("recalled %d, want the saved 33", corpus.gotLimit)
	}
}

// An infrastructure failure propagates rather than becoming a tool message: a dead
// chat server is not something the model can rephrase its way out of.
func TestModelFailurePropagates(t *testing.T) {
	model := &fakeLLM{err: errors.New("connection refused")}
	r := NewResearch(model, retrieve.New(&stubCorpus{}, &stubEmbed{}, nil, nil), defaultConfig(), nil)
	if _, err := r.Ask(context.Background(), "nb1", "q", ""); err == nil {
		t.Fatal("a dead chat server was swallowed")
	} else if !strings.Contains(err.Error(), "iteration 1") {
		t.Errorf("the error does not say which iteration failed: %v", err)
	}
}

// An empty reply is a server problem described as one, not a panic on Choices[0].
func TestServerReturningNothingIsAnError(t *testing.T) {
	model := &fakeLLM{replies: []llm.ChatResponse{{}}}
	r := NewResearch(model, retrieve.New(&stubCorpus{}, &stubEmbed{}, nil, nil), defaultConfig(), nil)
	if _, err := r.Ask(context.Background(), "nb1", "q", ""); err == nil {
		t.Fatal("an empty completion was accepted")
	}
}

// A turn needs a question. Not a blank one, and not a nil panic later.
func TestAskingNothing(t *testing.T) {
	r := NewResearch(&fakeLLM{}, retrieve.New(&stubCorpus{}, &stubEmbed{}, nil, nil), defaultConfig(), nil)
	if _, err := r.Ask(context.Background(), "nb1", "   ", ""); err == nil {
		t.Fatal("an empty question was researched")
	}
}

// The history the user sees is flattened into the seeded user message, with the
// current question last and labelled — otherwise a follow-up researches the last
// thing in the block instead of the question being asked.
func TestHistoryIsSeededIntoTheTurn(t *testing.T) {
	tr := runWithHistory(t, "Query: Who measured the erosion?\n\nSynthesizer answer: Smith in 1990.",
		toolCallReply(searchCall("c1", "Smith 1990 erosion")),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[1]}`)))
	mustResult(t, tr)

	seed, _ := tr.llm.requests[0].Messages[1].Content.(string)
	if !strings.Contains(seed, "Synthesizer answer: Smith in 1990.") {
		t.Errorf("history was not carried: %q", seed)
	}
	if !strings.HasSuffix(seed, "Current query: How fast is the coastline retreating?") {
		t.Errorf("the question is not last and labelled: %q", seed)
	}
}

func runWithHistory(t *testing.T, history string, replies ...llm.ChatResponse) *turn {
	t.Helper()
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	model := &fakeLLM{replies: replies}
	var status []string
	r := NewResearch(model, retrieve.New(corpus, &stubEmbed{}, nil, nil), defaultConfig(), func(s string) { status = append(status, s) })
	res, err := r.Ask(context.Background(), "nb1", "How fast is the coastline retreating?", history)
	return &turn{res: res, err: err, status: status, llm: model, corpus: corpus, research: r}
}

// Progress is what makes a minute of CPU inference survivable, and the search status
// line says which words were looked for rather than that something happened.
func TestStatusLines(t *testing.T) {
	tr := run(t, &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}},
		toolCallReply(searchCall("c1", "coastal erosion")),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[1]}`)))
	mustResult(t, tr)

	for _, want := range []string{"Starting research", "iteration 1", `Searching library for "coastal erosion"`} {
		if !containsContaining(tr.status, want) {
			t.Errorf("no status line containing %q in %v", want, tr.status)
		}
	}
	if !containsContaining(tr.status, "iteration 2") {
		t.Errorf("the second iteration was silent: %v", tr.status)
	}
}

// The model's prose, when a server sends content parts instead of a string, is still
// read rather than panicking on a type assertion.
func TestContentPartsFromTheModel(t *testing.T) {
	resp := llm.ChatResponse{Choices: []llm.ChatChoice{{Message: llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentPart{llm.TextPart("part one "), llm.TextPart("part two")},
	}}}}
	tr := run(t, &stubCorpus{}, resp)
	res := mustResult(t, tr)
	if res.Status != RawAnswer || res.Answer != "part one part two" {
		t.Errorf("%v %q, want the parts concatenated", res.Status, res.Answer)
	}
}

// A tool call whose arguments are a JSON document with extra keys still decodes.
func TestExtraArgumentKeysAreIgnored(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	tr := run(t, corpus,
		toolCallReply(call("c1", ToolSearch, `{"keywords":"erosion","query":"q","confidence":0.9,"thinking":"let me see"}`)),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[1],"summary":"3mm a year"}`)))
	res := mustResult(t, tr)
	if res.Status != Synthesize || len(res.Evidence) != 1 {
		t.Errorf("%v with %d chunks: a model adding keys it was not asked for must not break the turn", res.Status, len(res.Evidence))
	}
	if corpus.calls != 1 {
		t.Errorf("%d searches, want 1", corpus.calls)
	}
}

// What the model is promised and what it gets, for the one number that bounds the
// answer's context: FINAL_TOP_K.
func TestEvidencePerSearchIsHonoured(t *testing.T) {
	var hits []domain.Hit
	for i := range 12 {
		hits = append(hits, textChunk(fmt.Sprintf("d:%04d", i), "d", i, fmt.Sprintf("distinct passage %d", i)))
	}
	corpus := &stubCorpus{hits: hits}
	cfg := defaultConfig()
	cfg.FinalTopK = 4
	model := &fakeLLM{replies: []llm.ChatResponse{
		toolCallReply(searchCall("c1", "k")),
		toolCallReply(call("c2", ToolFinalize, `{"indices":[1,2,3,4,5]}`)),
	}}
	r := NewResearch(model, retrieve.New(corpus, &stubEmbed{}, nil, nil), cfg, nil)
	res, err := r.Ask(context.Background(), "nb1", "q", "")
	if err != nil {
		t.Fatal(err)
	}
	// Only four labels exist, so the fifth citation is dropped rather than answered
	// with a chunk that was never shown.
	if len(res.Evidence) != 4 {
		t.Errorf("%d chunks resolved, want the 4 this turn's setting could have shown", len(res.Evidence))
	}
	var body string
	for _, m := range res.Trace {
		body += textOf(m)
	}
	if got := strings.Count(body, "[[Chunk "); got != 4 {
		t.Errorf("%d labels in the trace, want exactly the 4 the setting could show:\n%s", got, body)
	}
}

// ── helpers ──────────────────────────────────────────────────────

// toolContents is everything the model was told, concatenated — the thing to assert
// on when the question is what the model could have read.
func toolContents(f *fakeLLM) string {
	var b strings.Builder
	for _, req := range f.requests {
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool {
				b.WriteString(textOf(m))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func containsContaining(lines []string, substr string) bool {
	for _, l := range lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// The loop's one output is a tool call, so the writing a model does before naming that
// search is thrown away — which is why the switch is off. It is still sent when it is off:
// a setting that only works when llama.cpp was started the same way is not a setting.
func TestThinkingSwitchTravelsOnEveryResearchRequest(t *testing.T) {
	for _, thinking := range []bool{false, true} {
		cfg := defaultConfig()
		cfg.Thinking = thinking
		model := &fakeLLM{replies: []llm.ChatResponse{textReply("answering from memory")}}
		r := NewResearch(model, retrieve.New(&stubCorpus{}, &stubEmbed{}, nil, nil), cfg, nil)
		if _, err := r.Ask(context.Background(), "nb1", "How fast is the coastline retreating?", ""); err != nil {
			t.Fatalf("Thinking=%v: the turn raised: %v", thinking, err)
		}
		if len(model.requests) != 1 {
			t.Fatalf("Thinking=%v: %d requests reached the model, want 1", thinking, len(model.requests))
		}
		kw := model.requests[0].ChatTemplateKwargs
		if kw == nil {
			t.Fatalf("Thinking=%v: no chat_template_kwargs sent, so the server default answered for the user", thinking)
		}
		if kw.EnableThinking != thinking {
			t.Errorf("Thinking=%v sent enable_thinking=%v", thinking, kw.EnableThinking)
		}
	}
}

var _ = json.Marshal
