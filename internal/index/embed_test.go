package index

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"libris/internal/domain"
	"libris/internal/llm"
)

// capture is an embedding server that records every request body and replies
// with vectors whose length is dim, in the shape llama.cpp uses.
type capture struct {
	srv       *httptest.Server
	bodies    []map[string]any
	calls     int
	dim       int
	mismatch  bool
	wrongLen  bool
	skipIndex bool
}

func newCapture(t *testing.T) *capture {
	t.Helper()
	c := &capture{dim: 4}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %s, want /v1/embeddings", r.URL.Path)
		}
		body := map[string]any{}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		c.bodies = append(c.bodies, body)
		c.calls++

		inputs, _ := body["input"].([]any)
		data := make([]map[string]any, 0, len(inputs))
		for i := range inputs {
			row := map[string]any{"embedding": vec(c.dim)}
			if !c.skipIndex {
				row["index"] = i
			}
			data = append(data, row)
		}
		if c.wrongLen {
			data = data[:max(0, len(data)-1)]
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"model": "m", "object": "list", "data": data})
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *capture) embedder(t *testing.T, wantDim int) *Embedder {
	t.Helper()
	client, err := llm.New(llm.Config{ChatURL: c.srv.URL, EmbedURL: c.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return NewEmbedder(client, c.srv.URL, "embeddinggemma-2", wantDim)
}

func vec(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(i) / float32(n)
	}
	return out
}

// inputs returns the "input" array of the nth request.
func (c *capture) inputs(t *testing.T, call int) []any {
	t.Helper()
	if call >= len(c.bodies) {
		t.Fatalf("only %d requests were made", len(c.bodies))
	}
	in, ok := c.bodies[call]["input"].([]any)
	if !ok {
		t.Fatalf("request %d has no input array: %v", call, c.bodies[call])
	}
	return in
}

// docTitle is the document name every test embeds under, so the prefix path is
// the one exercised by default rather than only by its own test.
const docTitle = "Two Page Notes"

func TestEmbedsTextAsSteeredStrings(t *testing.T) {
	c := newCapture(t)
	segs := []domain.Segment{
		{ID: "a:0000", Kind: domain.KindText, Text: "the sky is blue"},
		{ID: "a:0001", Kind: domain.KindText, Text: "water is wet"},
	}

	got, err := c.embedder(t, 0).Embed(context.Background(), docTitle, segs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d embeddings, want 2", len(got))
	}

	in := c.inputs(t, 0)
	if len(in) != 2 {
		t.Fatalf("sent %d inputs, want 2", len(in))
	}
	// Text still goes as a JSON string, not a content object: the object form is the
	// llama.cpp extension and not every server accepts it for plain text. It carries
	// the model's document prompt, which is what the card asks for.
	want := []string{
		DocumentPrompt(docTitle, "the sky is blue"),
		DocumentPrompt(docTitle, "water is wet"),
	}
	if in[0] != want[0] || in[1] != want[1] {
		t.Errorf("input = %v, want %v", in, want)
	}

	body := c.bodies[0]
	if body["model"] != "embeddinggemma-2" {
		t.Errorf("model = %v", body["model"])
	}
	if body["encoding_format"] != "float" {
		t.Errorf("encoding_format = %v", body["encoding_format"])
	}

	if got[0].SegmentID != "a:0000" || got[1].SegmentID != "a:0001" {
		t.Errorf("segment ids out of order: %v, %v", got[0].SegmentID, got[1].SegmentID)
	}
	if got[0].Model != ModelIdentity("embeddinggemma-2") || got[0].Dim != 4 || len(got[0].Vector) != 4 {
		t.Errorf("embedding = %+v", got[0])
	}
}

func TestBatchesSegments(t *testing.T) {
	c := newCapture(t)
	var segs []domain.Segment
	for i := range 20 {
		segs = append(segs, domain.Segment{
			ID:   string(rune('a'+i)) + ":0000",
			Kind: domain.KindText,
			Text: "passage " + string(rune('a'+i)),
		})
	}

	got, err := c.embedder(t, 0).Embed(context.Background(), docTitle, segs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Fatalf("got %d embeddings, want 20", len(got))
	}
	if c.calls != 3 {
		t.Errorf("made %d requests, want 3 (8+8+4)", c.calls)
	}
	for want, call := range []int{8, 8, 4} {
		if n := len(c.inputs(t, want)); n != call {
			t.Errorf("request %d sent %d inputs, want %d", want, n, call)
		}
	}
	// Order is the citation contract: vector i must belong to segment i.
	for i, e := range got {
		if e.SegmentID != segs[i].ID {
			t.Fatalf("vector %d belongs to %s, want %s", i, e.SegmentID, segs[i].ID)
		}
	}
}

func TestEmbedsImageAsDataURL(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "page-0001.png")
	raw := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
	if err := os.WriteFile(png, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c := newCapture(t)

	seg := domain.Segment{
		ID:    "s#pages:0000",
		Kind:  domain.KindImage,
		Asset: &domain.Asset{ID: "s/pages/page-0001.png", Path: png, MIME: "image/png"},
	}
	if _, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{seg}); err != nil {
		t.Fatal(err)
	}

	part := firstPart(t, c.inputs(t, 0)[0])
	if part["type"] != "image_url" {
		t.Fatalf("part type = %v, want image_url", part["type"])
	}
	url := part["image_url"].(map[string]any)["url"].(string)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	if url != want {
		t.Errorf("url = %.60s…, want the file inline as a data URL", url)
	}
	// A file:// path would demand --media-path on the server, which is the whole
	// reason this sends bytes instead.
	if strings.HasPrefix(url, "file://") {
		t.Error("sent a file:// reference; that needs --media-path")
	}
}

func TestImageWithRenditionSendsBothParts(t *testing.T) {
	dir := t.TempDir()
	jpg := filepath.Join(dir, "scan.jpg")
	if err := os.WriteFile(jpg, []byte{0xff, 0xd8, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	c := newCapture(t)

	seg := domain.Segment{
		ID:    "s#pages:0000",
		Kind:  domain.KindImage,
		Text:  "caption: figure 3",
		Asset: &domain.Asset{ID: "s/pages/scan.jpg", Path: jpg, MIME: "image/jpeg"},
	}
	if _, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{seg}); err != nil {
		t.Fatal(err)
	}

	parts, ok := c.inputs(t, 0)[0].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatal("image input is not a content object")
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want image + rendition in one content object", len(parts))
	}
	if parts[0].(map[string]any)["type"] != "image_url" ||
		parts[1].(map[string]any)["type"] != "text" {
		t.Errorf("parts = %v", parts)
	}
}

func TestEmbedsAudioAsRawBase64(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "slice-0001.wav")
	raw := []byte("RIFF....WAVEfmt ")
	if err := os.WriteFile(wav, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c := newCapture(t)

	seg := domain.Segment{
		ID:    "s#audio:0000",
		Kind:  domain.KindAudio,
		Asset: &domain.Asset{ID: "s/audio/slice-0001.wav", Path: wav, MIME: "audio/wav"},
	}
	if _, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{seg}); err != nil {
		t.Fatal(err)
	}

	part := firstPart(t, c.inputs(t, 0)[0])
	audio, ok := part["input_audio"].(map[string]any)
	if !ok {
		t.Fatalf("part = %v, want input_audio", part)
	}
	if got := audio["data"].(string); got != base64.StdEncoding.EncodeToString(raw) {
		t.Error("input_audio.data is not the slice as raw base64")
	}
	if _, hasURL := audio["url"]; hasURL {
		t.Error("sent a url; raw base64 needs no --media-path")
	}
}

func firstPart(t *testing.T, input any) map[string]any {
	t.Helper()
	obj, ok := input.(map[string]any)
	if !ok {
		t.Fatalf("input is %T, want a content object", input)
	}
	parts, ok := obj["content"].([]any)
	if !ok || len(parts) == 0 {
		t.Fatalf("input has no content parts: %v", obj)
	}
	return parts[0].(map[string]any)
}

func TestEmptyTextInputIsAnError(t *testing.T) {
	c := newCapture(t)
	_, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{
		{ID: "a:0007", Kind: domain.KindText, Text: "   \n  "},
	})
	if err == nil {
		t.Fatal("an empty text segment must not be sent as an empty embedding")
	}
	if !strings.Contains(err.Error(), "a:0007") {
		t.Errorf("error should name the segment: %v", err)
	}
}

func TestMissingAssetIsAnError(t *testing.T) {
	c := newCapture(t)
	_, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{
		{ID: "a:0000", Kind: domain.KindImage},
	})
	if err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Errorf("err = %v", err)
	}
}

func TestMissingAssetFileIsAnError(t *testing.T) {
	c := newCapture(t)
	_, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{{
		ID:    "a:0000",
		Kind:  domain.KindImage,
		Asset: &domain.Asset{ID: "gone", Path: filepath.Join(t.TempDir(), "gone.png"), MIME: "image/png"},
	}})
	if err == nil {
		t.Fatal("a vanished asset must fail loudly, not embed nothing")
	}
}

func TestDimensionMismatchAgainstSettings(t *testing.T) {
	c := newCapture(t) // server returns 4
	c.dim = 3072
	_, err := c.embedder(t, 1024).Embed(context.Background(), docTitle, []domain.Segment{
		{ID: "a:0000", Kind: domain.KindText, Text: "hello"},
	})
	if err == nil {
		t.Fatal("want a dimension error")
	}
	for _, want := range []string{"3072", "1024", "embeddingModel"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestShortResponseIsAnError(t *testing.T) {
	c := newCapture(t)
	c.wrongLen = true
	_, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{
		{ID: "a:0000", Kind: domain.KindText, Text: "hello"},
		{ID: "a:0001", Kind: domain.KindText, Text: "world"},
	})
	if err == nil || !strings.Contains(err.Error(), "1 vectors for 2 segments") {
		t.Errorf("err = %v", err)
	}
}

func TestUnorderedResponseStillMapsByID(t *testing.T) {
	c := newCapture(t)
	c.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		n := len(body["input"].([]any))
		// Deliberately reversed.
		data := make([]map[string]any, 0, n)
		for i := n - 1; i >= 0; i-- {
			data = append(data, map[string]any{"index": i, "embedding": vec(2)})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})

	got, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{
		{ID: "a:0000", Kind: domain.KindText, Text: "one"},
		{ID: "a:0001", Kind: domain.KindText, Text: "two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].SegmentID != "a:0000" || got[1].SegmentID != "a:0001" {
		t.Errorf("ordering lost: %v %v", got[0].SegmentID, got[1].SegmentID)
	}
}

func TestMissingIndexesFallBackToOrder(t *testing.T) {
	c := newCapture(t)
	c.skipIndex = true // every row arrives with index 0 by omission
	got, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{
		{ID: "a:0000", Kind: domain.KindText, Text: "one"},
		{ID: "a:0001", Kind: domain.KindText, Text: "two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].SegmentID != "a:0000" || got[1].SegmentID != "a:0001" {
		t.Errorf("positional fallback failed: %v %v", got[0].SegmentID, got[1].SegmentID)
	}
}

func TestUnreachableServerNamesTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	client, err := llm.New(llm.Config{ChatURL: url, EmbedURL: url})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewEmbedder(client, url, "m", 0).Embed(context.Background(), docTitle, []domain.Segment{
		{ID: "a:0000", Kind: domain.KindText, Text: "hello"},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	// This string is what the user reads in the UI, so it has to say which server.
	if !strings.Contains(err.Error(), url) {
		t.Errorf("error %q should name %q", err, url)
	}
}

func TestEmptyInputIsNotARequest(t *testing.T) {
	c := newCapture(t)
	got, err := c.embedder(t, 0).Embed(context.Background(), docTitle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || c.calls != 0 {
		t.Errorf("got %d embeddings after %d calls, want none", len(got), c.calls)
	}
}

func TestProbeReportsDimensions(t *testing.T) {
	c := newCapture(t)
	c.dim = 3072
	dim, err := c.embedder(t, 0).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dim != 3072 {
		t.Errorf("dim = %d, want 3072", dim)
	}
}

func TestBusyServerErrorIsPreserved(t *testing.T) {
	c := newCapture(t)
	c.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":{"message":"all inference slots are busy"}}`)
	})

	_, err := c.embedder(t, 0).Embed(context.Background(), docTitle, []domain.Segment{
		{ID: "a:0000", Kind: domain.KindText, Text: "hello"},
	})
	if !llm.IsBusy(err) {
		t.Errorf("a 503 must stay recognisable through the embedder: %v", err)
	}
}

// textPartOf returns the text carried inside a media content object, or "" when
// the object has none.
func textPartOf(t *testing.T, input any) string {
	t.Helper()
	parts, ok := input.(map[string]any)["content"].([]any)
	if !ok {
		t.Fatal("media input is not a content object")
	}
	for _, p := range parts {
		part, _ := p.(map[string]any)
		if part["type"] == "text" {
			if s, ok := part["text"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// The model card is specific: "Prefixes apply to text only. Pass images, video,
// and audio without any prefix." A page raster is the common case in libris, and a
// prefix there would describe a document the vector never saw.
func TestMediaInputsAreNeverPrefixed(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, bytes []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	c := newCapture(t)
	segs := []domain.Segment{
		{ID: "s#pages:0000", Kind: domain.KindImage, Text: "figure 3: latency",
			Asset: &domain.Asset{ID: "a", Path: write("p.png", []byte{0x89, 'P', 'N', 'G'}), MIME: "image/png"}},
		{ID: "s#audio:0000", Kind: domain.KindAudio, Text: "and then the speaker paused",
			Asset: &domain.Asset{ID: "b", Path: write("s.wav", []byte("RIFFxxxx")), MIME: "audio/wav"}},
	}
	if _, err := c.embedder(t, 0).Embed(context.Background(), docTitle, segs); err != nil {
		t.Fatal(err)
	}

	inputs := c.inputs(t, 0)
	for i, want := range []string{"figure 3: latency", "and then the speaker paused"} {
		if got := textPartOf(t, inputs[i]); got != want {
			t.Errorf("media %d carries %q, want the rendition verbatim and unprefixed", i, got)
		}
	}
}

// The wording is the card's, not ours, so it is pinned literally: an edit here is
// a decision to re-embed the corpus, not a refactor.
func TestDocumentPromptUsesTheCardWording(t *testing.T) {
	for _, tc := range []struct{ title, text, want string }{
		{title: "Smith 2020", text: "the sky is blue", want: "title: Smith 2020 | text: the sky is blue"},
		{title: "", text: "water is wet", want: "title: none | text: water is wet"},
		{title: "   ", text: "x", want: "title: none | text: x"},
	} {
		if got := DocumentPrompt(tc.title, tc.text); got != tc.want {
			t.Errorf("DocumentPrompt(%q, %q) = %q, want %q", tc.title, tc.text, got, tc.want)
		}
	}

	// Query side, verbatim from the same table. SearchQuery and QuestionAnswering
	// share the document prefix above, which is why one embedding serves both.
	if QuerySearch != "task: search result | query: " {
		t.Errorf("QuerySearch = %q", QuerySearch)
	}
	if QueryAnswer != "task: question answering | query: " {
		t.Errorf("QueryAnswer = %q", QueryAnswer)
	}
	if got := SteerQuery(QuerySearch, "  why?  "); got != "task: search result | query: why?" {
		t.Errorf("SteerQuery = %q", got)
	}
}

// A source name is user input, and the prompt has structure: a pipe opens a second
// field, a newline ends the line, and an endless name steals the input's tokens.
func TestDocumentPromptCleansTitlesThatWouldBreakIt(t *testing.T) {
	long := strings.Repeat("ü", maxTitleRunes+50)

	for _, tc := range []struct{ title, want string }{
		{title: "a | b", want: "title: a / b | text: t"},
		{title: "two\nlines\tand  spaces", want: "title: two lines and spaces | text: t"},
		{title: "  padded  ", want: "title: padded | text: t"},
	} {
		if got := DocumentPrompt(tc.title, "t"); got != tc.want {
			t.Errorf("DocumentPrompt(%q, \"t\") = %q, want %q", tc.title, got, tc.want)
		}
	}

	got := DocumentPrompt(long, "t")
	if n := len([]rune(got)); n != len("title: ")+maxTitleRunes+len(" | text: t") {
		t.Errorf("a %d-rune title produced %d runes: %s", maxTitleRunes+50, n, got)
	}
	if !strings.HasPrefix(got, "title: ü") {
		t.Errorf("truncation cut a multi-byte rune: %q", got)
	}
}

// The prefix is a wire concern. What the store keeps, and what a citation quotes,
// is the passage exactly as it was chunked.
func TestEmbeddingNeverRewritesStoredText(t *testing.T) {
	c := newCapture(t)
	segs := []domain.Segment{{ID: "a:0000", Kind: domain.KindText, Text: "the sky is blue"}}

	if _, err := c.embedder(t, 0).Embed(context.Background(), docTitle, segs); err != nil {
		t.Fatal(err)
	}
	if segs[0].Text != "the sky is blue" {
		t.Errorf("segment text became %q", segs[0].Text)
	}
	if !strings.Contains(fmt.Sprint(c.inputs(t, 0)[0]), "the sky is blue") {
		t.Error("the passage did not reach the wire either")
	}
}

// The prompt scheme is part of the vector space's identity, so a corpus embedded
// one way must not look like a corpus embedded another.
func TestModelIdentityRecordsThePromptScheme(t *testing.T) {
	got := ModelIdentity("embeddinggemma-2")
	if got != "embeddinggemma-2+"+PromptScheme {
		t.Errorf("ModelIdentity = %q, want the model plus the scheme", got)
	}
	if ModelIdentity("") == "" {
		t.Error("a blank model name erased the scheme too; nothing would identify the vectors")
	}
}
