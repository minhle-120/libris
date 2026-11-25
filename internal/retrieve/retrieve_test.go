package retrieve

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"libris/internal/domain"
	"libris/internal/index"
	"libris/internal/llm"
)

// ── fakes ────────────────────────────────────────────────────────

type fakeCorpus struct {
	hits     []domain.Hit
	gotVec   []float32
	gotModel string
	gotLimit int
	calls    int
	err      error
}

func (f *fakeCorpus) SearchSegments(_ context.Context, _ string, vec []float32, model string, limit int) ([]domain.Hit, error) {
	f.calls++
	f.gotVec, f.gotModel, f.gotLimit = vec, model, limit
	return f.hits, f.err
}

type fakeEmbed struct {
	identity  string
	gotPrefix string
	gotText   string
	calls     int
	err       error
}

func (f *fakeEmbed) EmbedQuery(_ context.Context, prefix, text string) ([]float32, error) {
	f.calls++
	f.gotPrefix, f.gotText = prefix, text
	if f.err != nil {
		return nil, f.err
	}
	return []float32{1, 0, 0}, nil
}

func (f *fakeEmbed) Identity() string {
	if f.identity == "" {
		return "embeddinggemma-2+tp1"
	}
	return f.identity
}

type fakeAssets struct {
	files map[string][]byte
	err   error
}

func (f *fakeAssets) Read(a domain.Asset) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	raw, ok := f.files[a.Path]
	if !ok {
		return nil, errors.New("no such file")
	}
	return raw, nil
}

// ── helpers ──────────────────────────────────────────────────────

func textHit(docID string, ordinal int, text string) domain.Hit {
	return domain.Hit{
		Segment: domain.Segment{
			ID:      docID + ":" + pad(ordinal),
			DocID:   docID,
			Kind:    domain.KindText,
			Ordinal: ordinal,
			Text:    text,
		},
		SourceName: docID + ".pdf",
	}
}

func imageHit(docID string, ordinal, page int) domain.Hit {
	return domain.Hit{
		Segment: domain.Segment{
			ID:      docID + ":" + pad(ordinal),
			DocID:   docID,
			Kind:    domain.KindImage,
			Ordinal: ordinal,
			Asset:   &domain.Asset{ID: docID + "/page-" + pad(ordinal) + ".png", Path: "/assets/" + docID + "/page-" + pad(ordinal) + ".png", MIME: "image/png"},
			Span:    domain.Provenance{Page: page},
		},
		SourceName: strings.TrimSuffix(docID, "#pages") + ".pdf",
	}
}

func pad(n int) string {
	s := ""
	for i := 0; i < 4; i++ {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func ids(ev []Evidence) []int {
	out := make([]int, len(ev))
	for i, e := range ev {
		out[i] = e.Index
	}
	return out
}

func segIDs(ev []Evidence) []string {
	out := make([]string, len(ev))
	for i, e := range ev {
		out[i] = e.Segment.ID
	}
	return out
}

func partsText(parts []llm.ContentPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// hasMedia reports whether a rendered set actually carries the file, which partsText
// cannot see by construction: it flattens the words a model is shown, and pixels are
// not words.
func hasMedia(parts []llm.ContentPart) bool {
	for _, p := range parts {
		if p.Type != "text" {
			return true
		}
	}
	return false
}

func newRetriever(c *fakeCorpus, e *fakeEmbed, a Assets) *Retriever {
	return New(c, e, a, nil)
}

// ── the probe ────────────────────────────────────────────────────

// keywords is the embedding probe, and it is the only thing a search can be probed
// with. A full question embedded against term-like chunks retrieves the average of
// everything the sentence mentions — a recall loss nothing else in the path can see,
// so the probe's exact text is asserted here rather than trusted.
func TestSearchEmbedsTheKeywordsItWasGiven(t *testing.T) {
	corpus := &fakeCorpus{hits: []domain.Hit{textHit("doc", 0, "coastal erosion rates")}}
	embed := &fakeEmbed{}
	reg := NewRegistry()

	_, err := newRetriever(corpus, embed, nil).Search(
		context.Background(), reg, "nb", "coastal erosion",
		Options{RecallTopK: 7, FinalTopK: 5})
	if err != nil {
		t.Fatal(err)
	}

	if embed.gotText != "coastal erosion" {
		t.Errorf("embedded %q, want the keywords", embed.gotText)
	}
	if embed.gotPrefix != index.QuerySearch {
		t.Errorf("query prefix = %q, want the search prefix", embed.gotPrefix)
	}
	if corpus.gotLimit != 7 {
		t.Errorf("asked the corpus for %d candidates, want recallTopK 7", corpus.gotLimit)
	}
	// The store must be told which vector space to search, and the only honest
	// source for that is the embedder that wrote it.
	if corpus.gotModel != embed.Identity() {
		t.Errorf("searched space %q, want %q", corpus.gotModel, embed.Identity())
	}
}

// ── ids and ordering ─────────────────────────────────────────────

// Registration order is relevance; display order is the document. The model reads a
// passage and cites the numbers the search earned.
func TestSearchIdsFollowRelevanceAndResultsFollowDocuments(t *testing.T) {
	// Vector order puts page 5 of doc2 first, then two chunks of doc1 out of order.
	corpus := &fakeCorpus{hits: []domain.Hit{
		textHit("doc2", 0, "second document"),
		textHit("doc1", 5, "later passage"),
		textHit("doc1", 0, "opening passage"),
	}}
	reg := NewRegistry()

	got, err := newRetriever(corpus, &fakeEmbed{}, nil).Search(
		context.Background(), reg, "nb", "kw", Options{})
	if err != nil {
		t.Fatal(err)
	}

	// The ids are minted in relevance order…
	if corpus.hits[0].Segment.ID == "doc1:0000" {
		t.Fatal("fixture is not in relevance order")
	}
	// …and the result is presented in document order: doc1:0000 ([[Chunk 3]]),
	// doc1:0005 ([[Chunk 2]]), doc2:0000 ([[Chunk 1]]).
	wantSegs := []string{"doc1:0000", "doc1:0005", "doc2:0000"}
	if got := segIDs(got); strings.Join(got, ",") != strings.Join(wantSegs, ",") {
		t.Errorf("presented %v, want %v", got, wantSegs)
	}
	if got[0].Index != 3 || got[1].Index != 2 || got[2].Index != 1 {
		t.Errorf("ids %v, want [3 2 1]: ids from relevance, order from the document", ids(got))
	}
	// The registry resolves each number back to the chunk it was minted for.
	for _, e := range got {
		back, ok := reg.Resolve(e.Index)
		if !ok || back.Segment.ID != e.Segment.ID {
			t.Errorf("[[Chunk %d]] resolves to %+v, want %s", e.Index, back.Segment.ID, e.Segment.ID)
		}
	}
}

// A chunk found twice gets one id and appears once, in the turn's transcript and in
// the synthesis prompt alike.
func TestSearchDoesNotRepeatKnownChunks(t *testing.T) {
	shared := textHit("doc1", 0, "the same page")
	reg := NewRegistry()
	r := newRetriever(&fakeCorpus{hits: []domain.Hit{shared, textHit("doc1", 1, "first search only")}}, &fakeEmbed{}, nil)

	first, err := r.Search(context.Background(), reg, "nb", "a", Options{})
	if err != nil {
		t.Fatal(err)
	}

	corpus2 := &fakeCorpus{hits: []domain.Hit{textHit("doc1", 2, "second search only"), shared}}
	r2 := newRetriever(corpus2, &fakeEmbed{}, nil)
	second, err := r2.Search(context.Background(), reg, "nb", "b", Options{})
	if err != nil {
		t.Fatal(err)
	}

	if len(first) != 2 || len(second) != 1 {
		t.Fatalf("registered %d then %d, want 2 then 1: the repeat must not re-enter", len(first), len(second))
	}
	if second[0].Segment.ID != "doc1:0002" {
		t.Errorf("second search returned %v", segIDs(second))
	}
	if second[0].Index != 3 {
		t.Errorf("new chunk got id %d, want 3: ids are never reused within a turn", second[0].Index)
	}
	// The citation the model writes for the shared chunk still means the same thing
	// in a later iteration as it did in the first.
	back, ok := reg.Resolve(first[0].Index)
	if !ok || back.Segment.Text != "the same page" {
		t.Errorf("[[Chunk %d]] no longer resolves to the shared chunk", first[0].Index)
	}
	if reg.Len() != 3 {
		t.Errorf("registry holds %d chunks, want 3", reg.Len())
	}
}

func TestSearchTakesOnlyFinalTopK(t *testing.T) {
	var hits []domain.Hit
	for i := range 10 {
		// Unique words: ten chunks reading "chunk" are one chunk to a content key.
		hits = append(hits, textHit("doc", i, "distinct passage number "+pad(i)))
	}
	corpus := &fakeCorpus{hits: hits}
	reg := NewRegistry()

	got, err := newRetriever(corpus, &fakeEmbed{}, nil).Search(
		context.Background(), reg, "nb", "kw", Options{RecallTopK: 10, FinalTopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("showed %d chunks, want the setting's 3", len(got))
	}
	// Truncated before registration, so nothing is labelled that the model never
	// read — and the registry, not a display slice, is what salvage reads later.
	if reg.Len() != 3 {
		t.Errorf("registry holds %d chunks, want 3: ids exist only for shown text", reg.Len())
	}
	if got[2].Segment.Ordinal != 2 {
		t.Errorf("kept %v, want the three most relevant", segIDs(got))
	}
}

// ── refusing, not guessing ───────────────────────────────────────

func TestSearchRefusesWithoutKeywords(t *testing.T) {
	corpus := &fakeCorpus{hits: []domain.Hit{textHit("doc", 0, "x")}}
	reg := NewRegistry()

	_, err := newRetriever(corpus, &fakeEmbed{}, nil).Search(
		context.Background(), reg, "nb", "   ", Options{})
	if !errors.Is(err, ErrNoKeywords) {
		t.Errorf("err = %v, want ErrNoKeywords", err)
	}
	if corpus.calls != 0 || reg.Len() != 0 {
		t.Errorf("searched anyway: %d corpus calls, %d registered", corpus.calls, reg.Len())
	}
}

// Nothing on the subject is not an error, and it must not be dressed up as one: the
// loop's answer to an empty result is to search again or give up honestly.
func TestSearchWithNothingToFind(t *testing.T) {
	corpus := &fakeCorpus{}
	got, err := newRetriever(corpus, &fakeEmbed{}, nil).Search(
		context.Background(), NewRegistry(), "nb", "kw", Options{})
	if err != nil {
		t.Fatalf("an empty library answered with an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("%d evidence from no hits", len(got))
	}
}

func TestSearchRequiresARegistry(t *testing.T) {
	if _, err := newRetriever(&fakeCorpus{}, &fakeEmbed{}, nil).Search(
		context.Background(), nil, "nb", "kw", Options{}); err == nil {
		t.Error("a search with nowhere to mint ids was accepted")
	}
}

// An embedder failure is infrastructure, not a model-shaped problem: it propagates,
// because "the embedding server is not running" is the one thing the caller cannot
// recover from by rephrasing.
func TestSearchPropagatesEmbeddingFailure(t *testing.T) {
	embed := &fakeEmbed{err: errors.New("connection refused")}
	_, err := newRetriever(&fakeCorpus{}, embed, nil).Search(
		context.Background(), NewRegistry(), "nb", "kw", Options{})
	if err == nil {
		t.Fatal("an embedder failure was swallowed")
	}
}

// ── rendering ────────────────────────────────────────────────────

func TestRenderTextChunks(t *testing.T) {
	reg := NewRegistry()
	reg.Register(textHit("doc1", 0, "the sea rose 3mm a year"))

	parts, err := newRetriever(&fakeCorpus{}, &fakeEmbed{}, nil).Render(reg.All())
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("%d parts, want 1", len(parts))
	}
	if want := "[[Chunk 1]]\nthe sea rose 3mm a year"; parts[0].Text != want {
		t.Errorf("rendered %q, want %q", parts[0].Text, want)
	}
}

// An image needs to say what it is. A bare [[Chunk 2]] followed by pixels gives the
// model nothing to decide with when it asks "does this page answer the question?"
func TestRenderNamesThePageForAnImage(t *testing.T) {
	raster := imageHit("sha256abc#pages", 3, 4)
	assets := &fakeAssets{files: map[string][]byte{raster.Segment.Asset.Path: []byte("PNG")}}
	reg := NewRegistry()
	reg.Register(raster)

	parts, err := newRetriever(&fakeCorpus{}, &fakeEmbed{}, assets).Render(reg.All())
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("%d parts, want a label then the image: %+v", len(parts), parts)
	}
	if want := "[[Chunk 1]] sha256abc.pdf page 4 — image\n"; parts[0].Text != want {
		t.Errorf("label = %q, want %q", parts[0].Text, want)
	}
	if parts[1].Type != "image_url" || !strings.Contains(parts[1].ImageURL.URL, base64.StdEncoding.EncodeToString([]byte("PNG"))) {
		t.Errorf("second part is %+v, want the raster itself", parts[1])
	}
}

// Without an asset reader the evidence is still citable — the model is told the
// image exists and that it cannot see it, which beats pasting an empty string.
func TestRenderWithoutAnAssetReader(t *testing.T) {
	reg := NewRegistry()
	reg.Register(imageHit("doc1#pages", 0, 1))

	parts, err := newRetriever(&fakeCorpus{}, &fakeEmbed{}, nil).Render(reg.All())
	if err != nil {
		t.Fatal(err)
	}
	body := partsText(parts)
	if !strings.Contains(body, "[[Chunk 1]]") || !strings.Contains(body, "not available") {
		t.Errorf("rendered %q, want the label and an honest note", body)
	}
}

func TestRenderNothing(t *testing.T) {
	parts, err := newRetriever(&fakeCorpus{}, &fakeEmbed{}, nil).Render(nil)
	if err != nil || parts != nil {
		t.Errorf("rendering nothing gave %v, %v", parts, err)
	}
}

// One renderer for both consumers is the whole design: the research model that decides
// what to cite is shown the same pixels, under the same label, as the model that writes
// the answer. A mixed set is the interesting case, because it is where a text chunk, a
// page, and the blank line between them have to come out in the right order.
func TestRenderShowsTextAndMediaUnderOneLabelScheme(t *testing.T) {
	raster := imageHit("doc1#pages", 3, 4)
	assets := &fakeAssets{files: map[string][]byte{raster.Segment.Asset.Path: []byte("PNG")}}
	reg := NewRegistry()
	reg.Register(textHit("doc1", 0, "the coast retreats 3mm a year"))
	reg.Register(raster)

	parts, err := newRetriever(&fakeCorpus{}, &fakeEmbed{}, assets).Render(reg.All())
	if err != nil {
		t.Fatal(err)
	}

	if got := partsText(parts); !strings.Contains(got, "[[Chunk 1]]\nthe coast retreats 3mm a year") {
		t.Errorf("text chunk rendered as:\n%s", got)
	}
	if got := partsText(parts); !strings.Contains(got, "[[Chunk 2]] doc1.pdf page 4 — image") {
		t.Errorf("media chunk not labelled:\n%s", got)
	}
	if !hasMedia(parts) {
		t.Error("a page the reader can see was left out of what the model is shown")
	}

	// The divider goes between chunks, never between a page and its label.
	if len(parts) != 4 {
		t.Fatalf("%d parts, want label, divider, label, image: %+v", len(parts), parts)
	}
	if parts[1].Type != "text" || strings.TrimSpace(parts[1].Text) != "" {
		t.Errorf("part between the chunks = %+v, want the blank line", parts[1])
	}
	if parts[2].Type != "text" || !strings.Contains(parts[2].Text, "page 4") ||
		parts[3].Type != "image_url" {
		t.Errorf("a page was split from its label: %+v", parts[2:])
	}
}

// A model started without the projector for a modality throws on the request, so
// showing it a page is not a smaller prompt — it is no prompt. The refusal has to name
// the model and count what it cost, because that is the sentence a person can act on.
func TestRenderRefusesMediaTheServingModelCannotSee(t *testing.T) {
	raster := imageHit("doc1#pages", 3, 4)
	assets := &fakeAssets{files: map[string][]byte{raster.Segment.Asset.Path: []byte("PNG")}}
	reg := NewRegistry()
	reg.Register(textHit("doc1", 0, "the coast retreats 3mm a year"))
	reg.Register(raster)

	_, err := New(&fakeCorpus{}, &fakeEmbed{}, assets, &llm.Caps{Model: "libris-chat"}).Render(reg.All())

	var want *llm.MediaUnsupported
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *llm.MediaUnsupported", err)
	}
	if want.Kind != llm.MediaImage || want.Count != 1 || want.Model != "libris-chat" {
		t.Errorf("refusal = %+v", want)
	}
	if !strings.Contains(err.Error(), "vision") || !strings.Contains(err.Error(), "libris-chat") {
		t.Errorf("the refusal does not say what to go and change: %v", err)
	}

	// The text half is still renderable, and a caller that asked for text only should
	// get it: the refusal is about the media this set holds, not about the set.
	_, err = New(&fakeCorpus{}, &fakeEmbed{}, assets, &llm.Caps{Model: "libris-chat", Vision: true}).
		Render(reg.All())
	if err != nil {
		t.Errorf("a model that can see was refused: %v", err)
	}
}

// Not asking is not the same as knowing the answer is no. A server with no /props — one
// libris does not manage — gets the media and answers for itself, rather than having
// its pages withheld by a guess made here.
func TestRenderSendsMediaWhenTheModelNeverSaid(t *testing.T) {
	raster := imageHit("doc1#pages", 0, 1)
	assets := &fakeAssets{files: map[string][]byte{raster.Segment.Asset.Path: []byte("PNG")}}
	reg := NewRegistry()
	reg.Register(raster)

	parts, err := New(&fakeCorpus{}, &fakeEmbed{}, assets, nil).Render(reg.All())
	if err != nil {
		t.Fatalf("a server that never answered for itself withheld the page: %v", err)
	}
	if !hasMedia(parts) {
		t.Errorf("no pixels in %+v", parts)
	}
}
