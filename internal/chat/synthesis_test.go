package chat

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"libris/internal/domain"
	"libris/internal/llm"
	"libris/internal/retrieve"
)

type fakeStream struct {
	chunks     []llm.Chunk
	requests   []llm.ChatRequest
	err        error
	yielded    []Delta
	yieldCalls int
}

func (f *fakeStream) ChatStream(_ context.Context, req llm.ChatRequest, yield func(llm.Chunk) error) error {
	f.requests = append(f.requests, req)
	for _, c := range f.chunks {
		f.yieldCalls++
		if err := yield(c); err != nil {
			return err
		}
	}
	return f.err
}

func textChunks(parts ...string) []llm.Chunk {
	var out []llm.Chunk
	for _, p := range parts {
		out = append(out, llm.Chunk{Delta: llm.Delta{Content: p}})
	}
	out = append(out, llm.Chunk{FinishReason: "stop"})
	return out
}

type stubAssets struct{ files map[string][]byte }

func (s stubAssets) Read(a domain.Asset) ([]byte, error) {
	raw, ok := s.files[a.Path]
	if !ok {
		return nil, errors.New("no such file")
	}
	return raw, nil
}

// partsText is everything a message says in words, which is what an assertion about
// prompt content needs; images are represented by their type in the failure text.
func partsText(parts []llm.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

func synthesizer(stream Streamer, assets retrieve.Assets) *Synthesizer {
	// Only Render is exercised here, so the retrieval collaborators are nil: a
	// synthesiser writes from evidence and never searches.
	return NewSynthesizer(stream, retrieve.New(nil, nil, assets), "test-chat", nil)
}

const (
	chosen   = "The coast retreats 3mm a year."
	alsoSeen = "A chunk the research agent found but did not cite."
)

func evidenceSet(t *testing.T) []retrieve.Evidence {
	t.Helper()
	reg := retrieve.NewRegistry()
	reg.Register(textChunk("doc:0005", "doc", 5, "Later in the document."))
	reg.Register(textChunk("doc:0000", "doc", 0, chosen))
	return reg.Only([]int{1, 2}) // model order: relevance
}

// The synthesiser's prompt is the whole of its world. It contains exactly the chunks
// the model chose, in document order, labelled — and none of the reasoning that
// found them.
func TestSynthesisPrompt(t *testing.T) {
	stream := &fakeStream{chunks: textChunks("The coast retreats 3mm [[Chunk 1]].")}
	synth := synthesizer(stream, nil)

	history := []llm.Message{{Role: llm.RoleUser, Content: "Who measured it?"}}
	_, err := synth.Write(context.Background(), history, evidenceSet(t), "How fast is the coastline retreating?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.requests) != 1 {
		t.Fatalf("%d calls, want 1", len(stream.requests))
	}
	req := stream.requests[0]

	if req.Messages[0].Role != llm.RoleSystem || req.Messages[0].Content != SynthesisSystemPrompt {
		t.Error("the synthesiser did not open with its own system prompt")
	}
	if len(req.Messages) != 3 {
		t.Fatalf("%d messages, want system + history + the evidence block", len(req.Messages))
	}
	if got, _ := req.Messages[1].Content.(string); got != "Who measured it?" {
		t.Errorf("history was not carried through as a real turn: %q", got)
	}
	if len(req.Tools) != 0 {
		t.Error("the synthesiser was given tools: it has nothing to retrieve with")
	}
	if !req.Stream {
		t.Error("the answer call does not stream, so the user watches a spinner")
	}

	body := partsText(req.Messages[2].Content.([]llm.ContentPart))
	if !strings.HasPrefix(body, "### Knowledge Chunks:") {
		t.Errorf("no knowledge block:\n%s", body)
	}
	if !strings.Contains(body, "### User Query:") || !strings.HasSuffix(body, "How fast is the coastline retreating?") {
		t.Errorf("the question is not where the model looks for it:\n%s", body)
	}

	// Exactly the chosen chunks, in document order: doc:0000 before doc:0005,
	// whatever order the model listed them in.
	iFirst := strings.Index(body, chosen)
	iSecond := strings.Index(body, "Later in the document.")
	if iFirst < 0 || iSecond < 0 {
		t.Fatalf("a chosen chunk is missing:\n%s", body)
	}
	if iFirst > iSecond {
		t.Errorf("not in document order:\n%s", body)
	}
	for _, label := range []string{"[[Chunk 1]]", "[[Chunk 2]]"} {
		if !strings.Contains(body, label) {
			t.Errorf("%s missing: without the label the answer cannot cite anything", label)
		}
	}
}

// A four-search turn may register twenty chunks and answer from six. The trace is not
// in this call at all — a model that has just been reasoning about what to search
// next will repeat a conclusion it never found evidence for.
func TestSynthesisPromptCarriesNeitherUnchosenEvidenceNorTheTrace(t *testing.T) {
	stream := &fakeStream{chunks: textChunks("done")}
	synth := synthesizer(stream, nil)

	reg := retrieve.NewRegistry()
	reg.Register(textChunk("doc:0000", "doc", 0, chosen))
	reg.Register(textChunk("doc:0001", "doc", 1, alsoSeen))
	evidence := reg.Only([]int{1}) // the model cited one of the two

	_, err := synth.Write(context.Background(), nil, evidence, "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	body := partsText(stream.requests[0].Messages[1].Content.([]llm.ContentPart))

	if strings.Contains(body, alsoSeen) {
		t.Errorf("a chunk the model did not choose was pasted:\n%s", body)
	}

	// Everything the research turn produced that is not evidence.
	for _, trace := range []string{"Assistant Evaluation Note", ToolSearch, ToolFinalize, "Searching library"} {
		if strings.Contains(body, trace) {
			t.Errorf("the research trace leaked into the answer prompt (%q)", trace)
		}
	}
}

// An empty knowledge block is not an error. The honesty instruction in the system
// prompt turns it into "the provided documents do not contain…", which is truer than
// anything this code could invent.
func TestSynthesisWithoutEvidence(t *testing.T) {
	stream := &fakeStream{chunks: textChunks("The provided documents do not contain information about that.")}
	synth := synthesizer(stream, nil)

	got, err := synth.Write(context.Background(), nil, nil, "What about the 2031 projections?", func(Delta) error { return nil })
	if err != nil {
		t.Fatalf("no evidence raised: %v", err)
	}
	body := partsText(stream.requests[0].Messages[1].Content.([]llm.ContentPart))
	if !strings.Contains(body, "(no chunks were retrieved)") {
		t.Errorf("knowledge block:\n%s", body)
	}
	if !strings.Contains(got.Text, "do not contain") {
		t.Errorf("answer = %q", got.Text)
	}
}

func TestSynthesisStreamsAndAssembles(t *testing.T) {
	stream := &fakeStream{chunks: []llm.Chunk{
		{Delta: llm.Delta{Role: llm.RoleAssistant}},
		{Delta: llm.Delta{ReasoningContent: "The first chunk gives "}},
		{Delta: llm.Delta{Content: "The coast retreats "}},
		{Delta: llm.Delta{Content: "3mm a year [[Chunk 1]]."}},
		{FinishReason: "stop", Usage: &llm.Usage{PromptTokens: 900, CompletionTokens: 12, TotalTokens: 912}},
	}}
	synth := synthesizer(stream, nil)

	var seen []Delta
	got, err := synth.Write(context.Background(), nil, evidenceSet(t), "q", func(d Delta) error {
		seen = append(seen, d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "The coast retreats 3mm a year [[Chunk 1]]." {
		t.Errorf("assembled text = %q", got.Text)
	}
	// Thinking is kept apart so nothing storing this turn can mistake it for the
	// answer, and it is still shown as progress while it arrives.
	if got.Reasoning != "The first chunk gives " {
		t.Errorf("reasoning = %q", got.Reasoning)
	}
	if len(seen) != 3 {
		t.Errorf("%d deltas forwarded, want 3: the empty role and terminator frames are not content", len(seen))
	}
	if got.Usage == nil || got.Usage.TotalTokens != 912 {
		t.Errorf("usage = %+v", got.Usage)
	}
}

// A page raster reaches the model as pixels, after its label, in the order the
// evidence came in: llama.cpp pairs each image with the marker in the rendered prompt
// by position, so a shuffled part would attach one page's pixels to another's label.
func TestSynthesisShowsAPageRaster(t *testing.T) {
	raster := imageChunk("doc#pages:0003", "doc#pages", 3, 4)
	reg := retrieve.NewRegistry()
	reg.Register(textChunk("doc:0000", "doc", 0, chosen)) // [[Chunk 1]]
	reg.Register(raster)                                  // [[Chunk 2]]

	assets := stubAssets{files: map[string][]byte{raster.Segment.Asset.Path: []byte("PNGBYTES")}}
	stream := &fakeStream{chunks: textChunks("See [[Chunk 2]].")}
	synth := synthesizer(stream, assets)

	if _, err := synth.Write(context.Background(), nil, reg.All(), "what does the chart show?", nil); err != nil {
		t.Fatalf("inline evidence failed: %v", err)
	}
	parts := stream.requests[0].Messages[1].Content.([]llm.ContentPart)

	imageAt := -1
	for i, p := range parts {
		if p.Type == llm.PartImage {
			imageAt = i
			want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PNGBYTES"))
			if p.ImageURL == nil || p.ImageURL.URL != want {
				t.Errorf("image part = %+v", p)
			}
		}
	}
	if imageAt < 1 {
		t.Fatalf("no raster in the answer prompt: %+v", parts)
	}
	if label := partsText([]llm.ContentPart{parts[imageAt-1]}); !strings.Contains(label, "[[Chunk 2]] doc.pdf page 4 — image") {
		t.Errorf("the raster is not preceded by its label: %q", label)
	}
	// The blank line separating chunks must land between chunks, never between a
	// page's label and its pixels.
	if sep := partsText([]llm.ContentPart{parts[imageAt-2]}); strings.TrimSpace(sep) != "" {
		t.Errorf("expected the chunk separator at %d, got %q", imageAt-2, sep)
	}
	if body := partsText(parts[:imageAt-2]); !strings.Contains(body, chosen) {
		t.Errorf("the text chunk is not ahead of the raster:\n%s", body)
	}
	if last := parts[len(parts)-1]; !strings.Contains(last.Text, "### User Query:") {
		t.Error("the question no longer follows the evidence")
	}
}

// If a raster cannot be read, the turn fails rather than answering from a partial
// knowledge block — a missing page is not the same evidence set.
func TestSynthesisFailsLouderThanAMissingPage(t *testing.T) {
	raster := imageChunk("doc#pages:0000", "doc#pages", 0, 1)
	reg := retrieve.NewRegistry()
	reg.Register(raster)

	stream := &fakeStream{}
	synth := synthesizer(stream, stubAssets{}) // empty: the file is gone

	_, err := synth.Write(context.Background(), nil, reg.All(), "q", nil)
	if err == nil {
		t.Fatal("an unreadable page was answered around")
	}
	if !strings.Contains(err.Error(), "inline evidence") {
		t.Errorf("err = %v", err)
	}
	if len(stream.requests) != 0 {
		t.Error("the model was called with a knowledge block missing a page")
	}
}

// A server that refuses is described as what it is, with the failing stage attached:
// "chat: synthesis: …" is what tells a user the answering model was unreachable, not
// the search.
func TestSynthesisReportsServerFailures(t *testing.T) {
	stream := &fakeStream{err: errors.New("503 all inference slots are busy")}
	synth := synthesizer(stream, nil)

	_, err := synth.Write(context.Background(), nil, evidenceSet(t), "q", nil)
	if err == nil {
		t.Fatal("a busy server was accepted")
	}
	if !strings.HasPrefix(err.Error(), "chat: synthesis:") || !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v", err)
	}
}

// A caller that stops the turn mid-answer propagates: the fake honours the error the
// yield returns, so nothing more arrives after the user pressed stop.
func TestStoppingMidAnswerStopsTheStream(t *testing.T) {
	stream := &fakeStream{chunks: textChunks("one ", "two ", "three ")}
	synth := synthesizer(stream, nil)

	stop := errors.New("canceled by the user")
	var got []string
	_, err := synth.Write(context.Background(), nil, evidenceSet(t), "q", func(d Delta) error {
		got = append(got, d.Text)
		if len(got) == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the caller's stop to surface", err)
	}
	if strings.Join(got, "") != "one two " {
		t.Errorf("deltas after the stop: %v", got)
	}
	if stream.yieldCalls != 2 {
		t.Errorf("%d chunks pushed after the caller stopped", stream.yieldCalls)
	}
}

// Listening is optional and must not change the prompt or the answer.
func TestSynthesisWithoutListening(t *testing.T) {
	stream := &fakeStream{chunks: textChunks("answer")}
	synth := synthesizer(stream, nil)

	got, err := synth.Write(context.Background(), nil, evidenceSet(t), "q", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "answer" || stream.yieldCalls != 2 {
		t.Errorf("text %q after %d frames", got.Text, stream.yieldCalls)
	}
}

// An empty question is not a turn.
func TestSynthesisRefusesAnEmptyQuestion(t *testing.T) {
	synth := synthesizer(&fakeStream{}, nil)
	if _, err := synth.Write(context.Background(), nil, evidenceSet(t), "  ", nil); err == nil {
		t.Fatal("an empty question was answered")
	}
}
