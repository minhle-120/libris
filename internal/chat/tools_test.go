package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"libris/internal/domain"
	"libris/internal/index"
	"libris/internal/llm"
	"libris/internal/retrieve"
)

// The tools are tested against the real retrieve.Retriever with a fake corpus: what
// is interesting here is the text a model reads and the state a turn ends in, and
// both of those change if a layer in between is mocked out.

type stubCorpus struct {
	hits []domain.Hit
	// batches answers each search in turn, for a turn whose searches find different
	// things; the last batch repeats once they run out.
	batches  [][]domain.Hit
	calls    int
	gotLimit int
	err      error
}

func (s *stubCorpus) SearchSegments(_ context.Context, notebookID string, _ []float32, _ string, limit int) ([]domain.Hit, error) {
	s.calls++
	s.gotLimit = limit
	if s.err != nil {
		return nil, s.err
	}
	if notebookID != "nb1" {
		return nil, errors.New("search escaped the notebook: " + notebookID)
	}
	if len(s.batches) > 0 {
		i := s.calls - 1
		if i >= len(s.batches) {
			i = len(s.batches) - 1
		}
		return s.batches[i], nil
	}
	return s.hits, nil
}

type stubEmbed struct{ calls int }

func (s *stubEmbed) EmbedQuery(_ context.Context, prefix, _ string) ([]float32, error) {
	s.calls++
	if prefix != index.QuerySearch {
		return nil, errors.New("query prefix = " + prefix)
	}
	return []float32{1, 0}, nil
}
func (s *stubEmbed) Identity() string { return "model+tp1" }

type stubRerank struct{ scores []float64 }

func (s *stubRerank) Score(_ context.Context, _ string, _ []llm.RerankDoc) ([]float64, error) {
	return s.scores, nil
}

func textChunk(id, docID string, ordinal int, text string) domain.Hit {
	return domain.Hit{Segment: domain.Segment{
		ID: id, DocID: docID, Kind: domain.KindText, Ordinal: ordinal, Text: text,
	}, SourceName: docID + ".pdf"}
}

func imageChunk(id, docID string, ordinal, page int) domain.Hit {
	return domain.Hit{Segment: domain.Segment{
		ID: id, DocID: docID, Kind: domain.KindImage, Ordinal: ordinal,
		Asset: &domain.Asset{ID: id + ".png", Path: "/assets/" + id + ".png", MIME: "image/png"},
		Span:  domain.Provenance{Page: page},
	}, SourceName: strings.TrimSuffix(docID, "#pages") + ".pdf"}
}

func toolsFor(t *testing.T, corpus retrieve.Corpus, reg *retrieve.Registry, opts retrieve.Options) *Tools {
	t.Helper()
	r := retrieve.New(corpus, &stubEmbed{}, nil, nil)
	return NewTools(r, reg, "nb1", "How fast is the coastline retreating?", opts)
}

// ── schemas and decoders describe the same thing ─────────────────

// The drift this prevents is invisible: the model writes the property it was shown,
// the decoder looks for the name it has, and the field arrives empty — which looks
// like a model using the tool badly and is our bug.
func TestSchemasAndDecodersAgree(t *testing.T) {
	schemas, err := Schemas()
	if err != nil {
		t.Fatalf("the tools could not be described: %v", err)
	}
	if len(schemas) != 3 {
		t.Fatalf("%d tools, want the three the reference names", len(schemas))
	}

	want := map[string]any{
		ToolSearch:   searchArgs{},
		ToolFinalize: finalizeArgs{},
		ToolNoInfo:   noInfoArgs{},
	}
	byName := map[string]llm.Tool{}
	for _, s := range schemas {
		byName[s.Function.Name] = s
	}
	for name, args := range want {
		schema, ok := byName[name]
		if !ok {
			t.Errorf("no tool named %q — the model would never be told it exists", name)
			continue
		}
		if schema.Type != "function" {
			t.Errorf("%s type = %q", name, schema.Type)
		}
		if strings.TrimSpace(schema.Function.Description) == "" {
			t.Errorf("%s has no description: the model chooses tools on it", name)
		}

		decoded := map[string]any{}
		if err := json.Unmarshal(schema.Function.Parameters, &decoded); err != nil {
			t.Fatalf("%s parameters are not valid JSON: %v", name, err)
		}
		props, _ := decoded["properties"].(map[string]any)
		fields := jsonFields(args)
		if len(props) != len(fields) {
			t.Errorf("%s schema has %v, the decoder reads %v", name, keys(props), fields)
		}
		for _, f := range fields {
			if _, ok := props[f]; !ok {
				t.Errorf("%s decodes %q but the schema never names it", name, f)
			}
			prop, _ := props[f].(map[string]any)
			if d, _ := prop["description"].(string); strings.TrimSpace(d) == "" {
				t.Errorf("%s.%s has no description: an undescribed argument is a guessed one", name, f)
			}
		}
		req, _ := decoded["required"].([]any)
		if len(req) != len(fields) {
			t.Errorf("%s requires %v of %v: every argument here is one the tool needs", name, keys2(req), fields)
		}
	}
}

// The one argument whose wire shape is looser than its Go type says so itself, and
// the schema must still promise plain integers: showing a model a union of
// integer-or-string invites it to choose.
func TestFinalizeSchemaPromisesIntegers(t *testing.T) {
	schemas, err := Schemas()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range schemas {
		if s.Function.Name != ToolFinalize {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(s.Function.Parameters, &doc); err != nil {
			t.Fatal(err)
		}
		props := doc["properties"].(map[string]any)
		idx := props["indices"].(map[string]any)
		if idx["type"] != "array" {
			t.Fatalf("indices type = %v", idx["type"])
		}
		if items := idx["items"].(map[string]any); items["type"] != "integer" {
			t.Errorf("items = %v, want integer", items["type"])
		}
		return
	}
	t.Error("no finalize tool")
}

// ── the mandatory-first-search gate ──────────────────────────────

func TestEverythingIsRefusedBeforeTheFirstSearch(t *testing.T) {
	reg := retrieve.NewRegistry()
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "erosion is 3mm a year")}}
	tr := toolsFor(t, corpus, reg, retrieve.Options{})

	for _, name := range []string{ToolFinalize, ToolNoInfo} {
		reply, err := tr.Execute(context.Background(), Call{ID: "c1", Name: name, Arguments: `{"indices":[1],"reason":"x"}`})
		if err != nil {
			t.Fatalf("%s raised: %v", name, err)
		}
		if reply.Kind != Continue {
			t.Errorf("%s ended the turn (%v) before any search: an ungrounded answer is the failure this gate exists to stop", name, reply.Kind)
		}
		if !strings.Contains(reply.Content, "must call `"+ToolSearch+"` first") {
			t.Errorf("%s got %q, want the refusal-shaped instruction", name, reply.Content)
		}
	}
	if reg.Len() != 0 {
		t.Errorf("the gate let %d chunks into the registry", reg.Len())
	}
	if corpus.calls != 0 {
		t.Errorf("%d searches ran while gating", corpus.calls)
	}
	if tr.Searched() {
		t.Error("a refusal unlocked the gate: the model could now skip searching entirely")
	}

	// A real search opens it, and the tool the model reached for now works.
	if _, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"coastal erosion","query":"How fast?"}`}); err != nil {
		t.Fatal(err)
	}
	if !tr.Searched() {
		t.Fatal("a completed search did not open the gate")
	}
	reply, err := tr.Execute(context.Background(), Call{Name: ToolNoInfo, Arguments: `{"reason":"nothing here"}`})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Kind != GaveUp {
		t.Errorf("after searching, no_info_found gave %v", reply.Kind)
	}
}

// A search that found nothing is still a search: the gate is about intent honoured,
// and a model that looked and came back empty must not be sent back to look again by
// force before it may say so.
func TestAnEmptySearchStillOpensTheGate(t *testing.T) {
	tr := toolsFor(t, &stubCorpus{}, retrieve.NewRegistry(), retrieve.Options{})
	if _, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"x","query":"y"}`}); err != nil {
		t.Fatal(err)
	}
	if !tr.Searched() {
		t.Error("an empty result closed the gate")
	}
}

// ── query_knowledge_base ─────────────────────────────────────────

func TestSearchResultIsLabelledChunksPlusTheEvaluationNote(t *testing.T) {
	reg := retrieve.NewRegistry()
	corpus := &stubCorpus{hits: []domain.Hit{
		imageChunk("p#pages:0003", "p#pages", 3, 4),
		textChunk("p:0005", "p", 5, "The coast retreats 3mm a year."),
		textChunk("p:0000", "p", 0, "Erosion was measured from 1990."),
	}}
	tr := toolsFor(t, corpus, reg, retrieve.Options{RecallTopK: 10, FinalTopK: 5})

	reply, err := tr.Execute(context.Background(), Call{
		ID:   "call_7",
		Name: ToolSearch,
		// keywords is the probe, query is the objective — the schema's whole point.
		Arguments: `{"keywords":"coastal erosion rate","query":"How fast is the coastline retreating?"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Kind != Continue {
		t.Errorf("a search ended the turn: %v", reply.Kind)
	}

	body := reply.Content
	// Registration is by relevance; the ids are what the model cites.
	if !strings.Contains(body, "[[Chunk 1]]") || !strings.Contains(body, "[[Chunk 3]]") {
		t.Fatalf("labels missing from:\n%s", body)
	}
	// Display is by document, so the model reads a passage rather than a shuffle:
	// chunk 3 (p:0000) before chunk 2 (p:0005) before chunk 1 (the raster).
	iFirst := strings.Index(body, "[[Chunk 3]]")
	iSecond := strings.Index(body, "[[Chunk 2]]")
	iRaster := strings.Index(body, "[[Chunk 1]]")
	if !(iFirst >= 0 && iFirst < iSecond && iSecond < iRaster) {
		t.Errorf("not in document order:\n%s", body)
	}
	// The note is the loop's engine — nothing else in the code asks for a second
	// search — and it has to quote the live question to do it.
	if !strings.Contains(body, "**Assistant Evaluation Note**") {
		t.Errorf("no evaluation note; the loop has no reason to iterate:\n%s", body)
	}
	if !strings.Contains(body, `"How fast is the coastline retreating?"`) {
		t.Error("the note does not quote the question")
	}
	if !strings.Contains(body, "`"+ToolFinalize+"`") || !strings.Contains(body, "`"+ToolSearch+"`") {
		t.Error("the note does not name the tools that end or continue the turn")
	}
	if corpus.gotLimit != 10 {
		t.Errorf("recalled %d, want the setting's 10", corpus.gotLimit)
	}
}

// A tool message is a string, so a page raster is named rather than shown. Its
// pixels reach the model that writes the answer, which is the one that cites them.
func TestAMediaChunkIsNamedNotInlined(t *testing.T) {
	reg := retrieve.NewRegistry()
	corpus := &stubCorpus{hits: []domain.Hit{imageChunk("p#pages:0000", "p#pages", 0, 7)}}
	tr := toolsFor(t, corpus, reg, retrieve.Options{})

	reply, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"chart","query":"what does the chart show?"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Content, "page 7 — image") {
		t.Fatalf("the raster is not described:\n%s", reply.Content)
	}
	if strings.Contains(reply.Content, "data:image") || strings.Contains(reply.Content, "iVBOR") {
		t.Error("pixels were inlined into a tool message, which is a string")
	}
}

func TestSearchWithoutKeywordsIsAnsweredInText(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	reg := retrieve.NewRegistry()
	tr := toolsFor(t, corpus, reg, retrieve.Options{})

	for _, args := range []string{`{"keywords":"  ","query":"q"}`, `{}`, `not json at all`, `{"keywords":null,"query":null}`} {
		reply, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: args})
		if err != nil {
			t.Fatalf("%s raised: %v", args, err)
		}
		if reply.Kind != Continue || !strings.Contains(reply.Content, "No keywords") {
			t.Errorf("%s → %q (%v), want a text refusal", args, reply.Content, reply.Kind)
		}
		if !strings.Contains(reply.Content, ToolSearch) {
			t.Errorf("%s → %q: a refusal that does not name the tool invites the same call again", args, reply.Content)
		}
	}
	if corpus.calls != 0 {
		t.Errorf("%d searches ran with no keywords", corpus.calls)
	}
	if reg.Len() != 0 {
		t.Errorf("%d chunks registered from nothing", reg.Len())
	}
}

// An empty library is a fact the model acts on, not an error: the text nudges it to
// refine or to give up honestly.
func TestAnEmptySearchNudges(t *testing.T) {
	tr := toolsFor(t, &stubCorpus{}, retrieve.NewRegistry(), retrieve.Options{})

	reply, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"quantum tunnelling","query":"does this exist?"}`})
	if err != nil {
		t.Fatalf("nothing to find raised: %v", err)
	}
	if reply.Kind != Continue {
		t.Errorf("an empty result ended the turn: %v", reply.Kind)
	}
	if !strings.Contains(reply.Content, "No relevant information") || !strings.Contains(reply.Content, ToolNoInfo) {
		t.Errorf("got %q, want the nudge toward refining or giving up", reply.Content)
	}
}

// A refused port is not model-shaped: it propagates, because "the embedding server
// is not running" is the one thing a model cannot recover from by rephrasing.
func TestInfrastructureFailurePropagates(t *testing.T) {
	reg := retrieve.NewRegistry()
	r := retrieve.New(&stubCorpus{}, failingEmbed{}, nil, nil)
	tr := NewTools(r, reg, "nb1", "q", retrieve.Options{})

	if _, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"x","query":"y"}`}); err == nil {
		t.Fatal("a dead embedding server was turned into a tool message")
	}
}

type failingEmbed struct{}

func (failingEmbed) EmbedQuery(context.Context, string, string) ([]float32, error) {
	return nil, errors.New("connection refused")
}
func (failingEmbed) Identity() string { return "m" }

// ── finalize_research_response ───────────────────────────────────

// Indices and nothing else. Returning text here would collapse the two-call design
// into one call whose answer is the research model's prose.
func TestFinalizeEndsTheTurnWithIndicesOnly(t *testing.T) {
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	reg := retrieve.NewRegistry()
	tr := toolsFor(t, corpus, reg, retrieve.Options{})

	if _, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"k","query":"q"}`}); err != nil {
		t.Fatal(err)
	}
	before := corpus.calls

	reply, err := tr.Execute(context.Background(), Call{Name: ToolFinalize, Arguments: `{"indices":[3,1,3]}`})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Kind != Finalized {
		t.Errorf("Kind = %v, want Finalized", reply.Kind)
	}
	if reply.Content != "" {
		t.Errorf("finalize returned content (%q): the answer is written by the synthesiser", reply.Content)
	}
	if got := reply.Indices; len(got) != 3 || got[0] != 3 || got[2] != 3 {
		t.Errorf("indices = %v: they are resolved later, out of band, so they are passed through as written", got)
	}
	if corpus.calls != before {
		t.Error("finalize triggered another search")
	}
}

// What models actually send, given that llama.cpp's grammar does not constrain the
// item type. A decoder that rejected any of these would throw away a researched
// answer; one that guessed by substring would keep [[Chunk 12]] as 1.
func TestFinalizeAcceptsWhatModelsSend(t *testing.T) {
	cases := []struct {
		args string
		want []int
	}{
		{`{"indices":[1,3]}`, []int{1, 3}},
		{`{"indices":["1","3"]}`, []int{1, 3}},
		{`{"indices":[1.0,3.0]}`, []int{1, 3}},
		{`{"indices":["[[Chunk 12]]"]}`, []int{12}},
		{`{"indices":[["2"]]}`, []int{2}},
		{`{"indices":"1, 3"}`, []int{1, 3}},
		{`{"indices":[1,null,{},3]}`, []int{1, 3}},
		{`{"indices":null}`, nil},
		{`{}`, nil},
		{`{"indices": [1,`, nil}, // malformed: the reference treats it as {}, not as an error
		{`{"indices":"[[Chunk 2]] and [[Chunk 5]]"}`, []int{2, 5}},
	}

	reg := retrieve.NewRegistry()
	corpus := &stubCorpus{hits: []domain.Hit{textChunk("d:0000", "d", 0, "x")}}
	tr := toolsFor(t, corpus, reg, retrieve.Options{})
	if _, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"k","query":"q"}`}); err != nil {
		t.Fatal(err)
	}

	for _, c := range cases {
		reply, err := tr.Execute(context.Background(), Call{Name: ToolFinalize, Arguments: c.args})
		if err != nil {
			t.Errorf("%s raised: %v", c.args, err)
			continue
		}
		if reply.Kind != Finalized {
			t.Errorf("%s → %v, want Finalized", c.args, reply.Kind)
		}
		if len(reply.Indices) != len(c.want) {
			t.Errorf("%s → %v, want %v", c.args, reply.Indices, c.want)
			continue
		}
		for i := range c.want {
			if reply.Indices[i] != c.want[i] {
				t.Errorf("%s → %v, want %v", c.args, reply.Indices, c.want)
				break
			}
		}
	}
}

// ── no_info_found ────────────────────────────────────────────────

func TestNoInfoCarriesTheModelReasonVerbatim(t *testing.T) {
	reg := retrieve.NewRegistry()
	tr := toolsFor(t, &stubCorpus{}, reg, retrieve.Options{})
	if _, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"k","query":"q"}`}); err != nil {
		t.Fatal(err)
	}

	const reason = "Searched for tidal gauge records and 2020 coastal surveys; neither is in the library."
	reply, err := tr.Execute(context.Background(), Call{Name: ToolNoInfo, Arguments: `{"reason":"` + reason + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Kind != GaveUp {
		t.Errorf("Kind = %v, want GaveUp", reply.Kind)
	}
	if reply.Reason != reason {
		t.Errorf("reason was rewritten: %q", reply.Reason)
	}
	if reply.Content != "" {
		t.Errorf("no_info_found returned content %q", reply.Content)
	}
}

// The reason is the user-visible answer, so an empty one is filled in: a blank
// message is not an outcome worth preserving.
func TestNoInfoWithoutAReasonStillAnswers(t *testing.T) {
	reg := retrieve.NewRegistry()
	tr := toolsFor(t, &stubCorpus{}, reg, retrieve.Options{})
	if _, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"k","query":"q"}`}); err != nil {
		t.Fatal(err)
	}

	reply, err := tr.Execute(context.Background(), Call{Name: ToolNoInfo, Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Kind != GaveUp || strings.TrimSpace(reply.Reason) == "" {
		t.Errorf("%v %q, want GaveUp with something to show the user", reply.Kind, reply.Reason)
	}
}

// ── everything else ──────────────────────────────────────────────

func TestAnUnknownToolKeepsTheTurnGoing(t *testing.T) {
	tr := toolsFor(t, &stubCorpus{}, retrieve.NewRegistry(), retrieve.Options{})
	if _, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"k","query":"q"}`}); err != nil {
		t.Fatal(err)
	}

	reply, err := tr.Execute(context.Background(), Call{Name: "delegate_to_diagram", Arguments: `{}`})
	if err != nil {
		t.Fatalf("an unknown tool raised: %v", err)
	}
	if reply.Kind != Continue {
		t.Errorf("an unknown tool ended the turn: %v", reply.Kind)
	}
	if !strings.Contains(reply.Content, "delegate_to_diagram") {
		t.Errorf("the model is not told which name it got wrong: %q", reply.Content)
	}
	if !strings.Contains(reply.Content, ToolSearch) {
		t.Errorf("the model is not told what exists instead: %q", reply.Content)
	}
}

// One assistant reply may carry several calls; the count is what the trace and any
// budget beyond iterations needs, and it is not the iteration count.
func TestEveryCallIsCountedIncludingRefusals(t *testing.T) {
	tr := toolsFor(t, &stubCorpus{}, retrieve.NewRegistry(), retrieve.Options{})
	for _, name := range []string{ToolFinalize, ToolSearch, "nonsense"} {
		if _, err := tr.Execute(context.Background(), Call{Name: name, Arguments: `{"keywords":"k","query":"q"}`}); err != nil {
			t.Fatal(err)
		}
	}
	if tr.Calls() != 3 {
		t.Errorf("Calls() = %d, want 3: a gated refusal is still a call the transcript carries", tr.Calls())
	}
}

// The settings a user saved reach the search the next turn, not the next restart.
func TestOptionsTravelPerTurn(t *testing.T) {
	corpus := &stubCorpus{}
	tr := toolsFor(t, corpus, retrieve.NewRegistry(), retrieve.Options{RecallTopK: 42, FinalTopK: 1})
	if _, err := tr.Execute(context.Background(), Call{Name: ToolSearch, Arguments: `{"keywords":"k","query":"q"}`}); err != nil {
		t.Fatal(err)
	}
	if corpus.gotLimit != 42 {
		t.Errorf("recalled %d, want the 42 this turn was built with", corpus.gotLimit)
	}
}

// ── test helpers ─────────────────────────────────────────────────

func jsonFields(v any) []string {
	out := []string{}
	raw, err := json.Marshal(v)
	if err != nil {
		return out
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return out
	}
	for k := range doc {
		out = append(out, k)
	}
	return out
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keys2(v []any) []string {
	out := make([]string, 0, len(v))
	for _, x := range v {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
