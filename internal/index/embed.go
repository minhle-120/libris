// Package index turns segments into vectors, and will later answer "which of
// these are closest to this question". Only the embedding half exists today.
//
// It is the only package that decides how a segment reaches an embedding model.
// The decision is worth stating, because llama.cpp is picky about it:
//
//   - text goes as a plain string input;
//   - a page raster goes as a base64 data URL, which works against a plain
//     llama-server, where a file:// path would demand --media-path;
//   - an audio slice goes as raw base64 in input_audio.data;
//   - a media segment with a text rendition (a caption or transcript) sends both
//     parts in one content object, so the vector covers what is on the page and
//     what was said about it.
//
// The server normalises every vector (Euclidean norm), so nothing here does.
package index

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"libris/internal/domain"
	"libris/internal/llm"
)

// batchLimit bounds how many segments go into one request. Multimodal inputs
// carry whole page rasters, so a large batch is a large body and a long stall on
// a server with one slot; eight is enough to keep the round trips down without
// making a request that a busy server must refuse outright.
const batchLimit = 8

// maxAssetBytes refuses to inline a media file this large. It is not a tuning
// knob: past this the request is the problem, not the model.
const maxAssetBytes = 32 << 20

// Embedder produces a vector for every segment it is given, or returns an error.
// It never returns fewer vectors than it was asked for: a source with some
// vectors and not others would answer questions with a silent slice of itself.
type Embedder struct {
	client  *llm.Client
	baseURL string // only for error messages that name the server the user must fix
	model   string
	wantDim int // from settings; 0 accepts whatever the model returns
}

// NewEmbedder wires an embedder to one server. wantDim is the configured
// embeddingDimensions, checked against every response when non-zero.
func NewEmbedder(client *llm.Client, baseURL, model string, wantDim int) *Embedder {
	return &Embedder{client: client, baseURL: baseURL, model: model, wantDim: wantDim}
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
	for start := 0; start < len(segments); start += batchLimit {
		end := min(start+batchLimit, len(segments))
		batch := segments[start:end]

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
				start+1, end, len(segments), e.baseURL, err)
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
				Model:     ModelIdentity(e.model),
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
		if len(raw) > maxAssetBytes {
			return llm.EmbedInput{}, fmt.Errorf("asset %s is %d bytes, over the %d byte inline limit",
				seg.Asset.ID, len(raw), maxAssetBytes)
		}
		encoded := base64.StdEncoding.EncodeToString(raw)

		parts := make([]llm.ContentPart, 0, 2)
		if seg.Kind == domain.KindImage {
			mime := seg.Asset.MIME
			if mime == "" {
				mime = "image/png"
			}
			parts = append(parts, llm.ImageBase64(mime, encoded))
		} else {
			parts = append(parts, llm.AudioBase64(encoded))
		}
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
