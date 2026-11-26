// Package index turns segments into vectors, and will later answer "which of
// these are closest to this question". Only the embedding half exists today.
//
// It is the only package that decides how a segment reaches an embedding model.
// The decision is worth stating, because llama.cpp is picky about it:
//
//   - text goes as a plain string input;
//   - a page raster goes as a base64 data URL, which works against a plain
//     llama-server, where a file:// path would demand --media-path;
//   - a page raster with a caption sends image and text in one content object, so
//     the vector covers what is on the page and what was said about it;
//   - a clip of a recording goes the same way, as audio, and alone in its request
//     (see batches). All three forms are unprefixed: the card's steering prefixes are
//     for text inputs.
//
// The server normalises every vector (Euclidean norm), so nothing here does.
package index

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"libris/internal/domain"
	"libris/internal/llm"
)

// batchLimit bounds how many segments go into one request. llama.cpp makes one task per input
// and queues them together, so this is how many the instance may have in hand, not how many it
// runs at once — and eight deliberately sits ahead of the four slots a model instance is given
// by default. A batch that exactly fills the slots empties the queue at every round trip, and
// the GPU then waits on this file while the next request is assembled. Multimodal inputs carry
// whole page rasters, so a far larger batch is a large body and a long stall, and eight stays
// small enough that a busy server never has to refuse a request outright.
const batchLimit = 8

// batch is a run of segments that travel in one request, as a half-open range of the input.
type batch struct{ start, end int }

// batches cuts the work into requests. Two rules decide the cuts.
//
//   - at most batchLimit segments in one request — see batchLimit for why eight;
//   - never more than one audio clip in a request. A 15 s clip is ~480 KB of PCM and ~640 KB
//     base64, so a full batch of clips is a ten-megabyte body for one round trip, and one
//     clip the server dislikes would fail the seven others in the same request. Text and
//     page rasters keep batching together, because the thing that produces clips — an hour
//     of a recording — produces hundreds of them.
func batches(segments []domain.Segment) []batch {
	var out []batch
	for start := 0; start < len(segments); {
		if segments[start].Kind == domain.KindAudio {
			out = append(out, batch{start, start + 1})
			start++
			continue
		}
		end := start + 1
		for end < len(segments) && end-start < batchLimit &&
			segments[end].Kind != domain.KindAudio {
			end++
		}
		out = append(out, batch{start, end})
		start = end
	}
	return out
}

// MaxAssetBytes refuses to inline a media file this large. It is not a tuning
// knob: past this the request is the problem, not the model. It is exported
// because retrieval inlines rasters too — a page image sent to the answer model is
// the same hazard, arriving by a different path, and deserves the same one limit.
const MaxAssetBytes = 32 << 20

// Embedder produces a vector for every segment it is given, or returns an error.
// It never returns fewer vectors than it was asked for: a source with some
// vectors and not others would answer questions with a silent slice of itself.
type Embedder struct {
	client  *llm.Client
	baseURL string // only for error messages that name the server the user must fix
	// model is the name carried on the request, which is how a router with more than one
	// instance of the same weights is told which one to answer.
	model string
	// identity is what every vector this embedder makes is stamped with, and a search filters on
	// exactly that string. It is usually the same as model. It is not always: the two names can
	// part when one file is served twice, and a query embedder that stamped the instance it
	// dialled instead of the model that made the vectors would search for vectors nothing ever
	// wrote — every notebook reading as empty, with nothing anywhere to say so.
	identity string
	wantDim  int // from settings; 0 accepts whatever the model returns
}

// NewEmbedder wires an embedder to one server. wantDim is the configured
// embeddingDimensions, checked against every response when non-zero.
func NewEmbedder(client *llm.Client, baseURL, model string, wantDim int) *Embedder {
	return NewEmbedderStamping(client, baseURL, model, model, wantDim)
}

// NewEmbedderStamping asks for `dial` and records `stamps` beside what it gets back. It exists
// for the one case where those are different things: the embedding model is loaded twice, once
// on the device and once on the host, and a question's vector must be findable next to the
// vectors an upload made. Both names are libris's own; neither is a setting.
func NewEmbedderStamping(client *llm.Client, baseURL, dial, stamps string, wantDim int) *Embedder {
	return &Embedder{client: client, baseURL: baseURL, model: dial, identity: stamps, wantDim: wantDim}
}

// Probe asks the server for one vector and reports its length. Called at startup
// so a wrong model or URL is a log line rather than a hundred failed uploads, and
// so the dimension the settings promise can be checked.
func (e *Embedder) Probe(ctx context.Context) (int, error) {
	// Probed through the document form rather than a bare string, so the length
	// discovered belongs to the shape the pipeline will actually send.
	vecs, err := e.Embed(ctx, "", []domain.Segment{{
		ID:   "probe",
		Kind: domain.KindText,
		Text: "libris embedding probe",
	}})
	if err != nil {
		return 0, err
	}
	return vecs[0].Dim, nil
}

// Embed returns one vector per segment, in input order.
//
// title is the document's display name — the source name the user typed or the
// filename libris kept — and is used to build the model's document prompt. Empty
// means there is none. It never reaches the stored text: citations quote the
// segment, not the prompt.
func (e *Embedder) Embed(ctx context.Context, title string, segments []domain.Segment) ([]domain.Embedding, error) {
	if len(segments) == 0 {
		return nil, nil
	}

	out := make([]domain.Embedding, 0, len(segments))
	for _, b := range batches(segments) {
		batch := segments[b.start:b.end]

		inputs := make([]llm.EmbedInput, len(batch))
		for i, seg := range batch {
			in, err := e.inputFor(title, seg)
			if err != nil {
				return nil, fmt.Errorf("index: segment %s: %w", seg.ID, err)
			}
			inputs[i] = in
		}

		resp, err := e.client.Embed(ctx, llm.EmbedRequest{
			Model:          e.model,
			Input:          inputs,
			EncodingFormat: "float",
		})
		if err != nil {
			// Name the server: this string becomes the detail a user reads in the
			// UI, and "connection refused" alone does not say which of three URLs.
			return nil, fmt.Errorf("index: embedding %d-%d of %d through %s: %w",
				b.start+1, b.end, len(segments), e.baseURL, err)
		}
		if len(resp.Data) != len(batch) {
			return nil, fmt.Errorf("index: %s returned %d vectors for %d segments",
				e.baseURL, len(resp.Data), len(batch))
		}

		for k, row := range rowFor(resp.Data, len(batch)) {
			vec := resp.Data[row].Embedding
			if err := e.checkDim(batch[k].ID, vec); err != nil {
				return nil, err
			}
			out = append(out, domain.Embedding{
				SegmentID: batch[k].ID,
				Model:     ModelIdentity(e.identity),
				Dim:       len(vec),
				Vector:    vec,
			})
		}
	}

	if len(out) != len(segments) {
		return nil, fmt.Errorf("index: produced %d vectors for %d segments", len(out), len(segments))
	}
	return out, nil
}

// EmbedQuery vectors one retrieval string and returns the vector without storing
// it anywhere. It takes the query-side prefix so the two halves of the pair cannot
// drift: the document side is always DocumentPrompt, and nothing that goes through
// this function may reach the store, because a query prefix in a stored vector
// quietly moves that vector into a space the documents were never in.
//
// prefix is index.QuerySearch or index.QueryAnswer — the choice is retrieval
// policy, not this package's, so it is a parameter. An empty prefix embeds the
// bare text, which is legal and worse.
func (e *Embedder) EmbedQuery(ctx context.Context, prefix, text string) ([]float32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("index: refusing to embed an empty query")
	}

	resp, err := e.client.Embed(ctx, llm.EmbedRequest{
		Model:          e.model,
		Input:          []llm.EmbedInput{llm.TextInput(SteerQuery(prefix, text))},
		EncodingFormat: "float",
	})
	if err != nil {
		return nil, fmt.Errorf("index: embedding query through %s: %w", e.baseURL, err)
	}
	if len(resp.Data) != 1 {
		return nil, fmt.Errorf("index: %s returned %d vectors for 1 query", e.baseURL, len(resp.Data))
	}
	vec := resp.Data[0].Embedding
	if err := e.checkDim("query", vec); err != nil {
		return nil, err
	}
	return vec, nil
}

// Identity is what this embedder writes into the store's model column. The search
// side has to ask the same question the write side answered, so the value is read
// from here rather than rebuilt from settings next door — two places holding a
// model name is how a search starts filtering for a space nothing was written in.
func (e *Embedder) Identity() string { return ModelIdentity(e.identity) }

// checkDim rejects a response that cannot be stored next to the others. Mixing
// dimensions is silent corruption: cosine over ragged vectors still computes,
// it just computes something meaningless.
func (e *Embedder) checkDim(segmentID string, vec []float32) error {
	if len(vec) == 0 {
		return fmt.Errorf("index: %s returned an empty vector for segment %s", e.baseURL, segmentID)
	}
	if e.wantDim > 0 && len(vec) != e.wantDim {
		return fmt.Errorf("index: %s returned %d dimensions, settings promise %d — check embeddingModel",
			e.baseURL, len(vec), e.wantDim)
	}
	return nil
}

// rowFor answers, for each segment in the batch, which response row produced it.
//
// llama.cpp labels rows with index, and that label is trusted only when the rows
// form a permutation of the batch. The fallback is positional because a server may
// leave index unset — every row reads 0 — and trusting that would scramble which
// chunk a citation points at, which is exactly the failure this pipeline exists to
// avoid. The mapping is segment → row, not the reverse; getting that backwards
// still produces the right *set* of vectors in the wrong order, and nothing
// downstream would notice.
func rowFor(data []llm.Embedding, n int) []int {
	row := make([]int, n)
	seen := make([]bool, n)
	ok := true

	for i, d := range data {
		if d.Index < 0 || d.Index >= n || seen[d.Index] {
			ok = false
			break
		}
		seen[d.Index] = true
		row[d.Index] = i
	}
	if ok {
		return row
	}

	for i := range row {
		row[i] = i
	}
	return row
}

// MediaForm is how a segment's bytes reach a model: the modality to name them by on the
// wire, and the MIME to claim when the asset itself does not say. It is exported because
// both halves of the path ask the same question — the embedder putting a vector on a page
// or a clip, and retrieval handing the same bytes to the answering model — and a kind that
// embeds but cannot be displayed, or the other way round, is a bug nobody would see until
// a turn failed.
//
// A media kind llm has no part type for is refused rather than guessed at, which is the
// same rule llm.MediaPart applies one level down.
func MediaForm(kind domain.Kind) (media llm.MediaKind, mime string, ok bool) {
	switch kind {
	case domain.KindImage:
		// What a page raster renders to; see ingest's PDF stage.
		return llm.MediaImage, "image/png", true
	case domain.KindAudio:
		// The one shape ingest ever writes: 16 kHz mono PCM. See ingest.Audio.
		return llm.MediaAudio, "audio/wav", true
	}
	return "", "", false
}

// inputFor decides the wire form of one segment.
func (e *Embedder) inputFor(title string, seg domain.Segment) (llm.EmbedInput, error) {
	switch seg.Kind {
	case domain.KindText:
		if strings.TrimSpace(seg.Text) == "" {
			return llm.EmbedInput{}, fmt.Errorf("text segment has nothing to embed")
		}
		return llm.TextInput(DocumentPrompt(title, seg.Text)), nil

	case domain.KindImage, domain.KindAudio:
		if seg.Asset == nil {
			return llm.EmbedInput{}, fmt.Errorf("%s segment has no asset", seg.Kind)
		}
		raw, err := os.ReadFile(seg.Asset.Path)
		if err != nil {
			return llm.EmbedInput{}, fmt.Errorf("read asset %s: %w", seg.Asset.ID, err)
		}
		if len(raw) > MaxAssetBytes {
			return llm.EmbedInput{}, fmt.Errorf("asset %s is %d bytes, over the %d byte inline limit",
				seg.Asset.ID, len(raw), MaxAssetBytes)
		}

		wire, mimeDefault, ok := MediaForm(seg.Kind)
		if !ok {
			return llm.EmbedInput{}, fmt.Errorf("no wire shape for segment kind %q", seg.Kind)
		}
		mime := seg.Asset.MIME
		if mime == "" {
			mime = mimeDefault
		}
		media, err := llm.MediaPart(wire, mime, base64.StdEncoding.EncodeToString(raw))
		if err != nil {
			return llm.EmbedInput{}, err
		}

		parts := []llm.ContentPart{media}
		// A rendition travels with the media it describes, in the same content
		// object, so one vector covers both — and it stays unprefixed. The card's
		// prefixes are for text inputs; "pass images, video, and audio without any
		// prefix" covers the whole content object, not just the media part.
		if strings.TrimSpace(seg.Text) != "" {
			parts = append(parts, llm.TextPart(seg.Text))
		}
		return llm.PartsInput(parts...), nil

	default:
		return llm.EmbedInput{}, fmt.Errorf("unknown segment kind %q", seg.Kind)
	}
}
