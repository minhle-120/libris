// Package retrieve answers one question: which of a notebook's chunks should a
// model be shown, and under what label?
//
// It is recall → rerank → evidence registry, and nothing else. No prompts, no LLM
// turns, no knowledge that a research loop exists — chat owns the tools and the
// wording, and this package owns the two properties the loop depends on:
//
//   - Stable labels. Every recalled segment gets a dense, 1-based id for the rest
//     of the turn, so [[Chunk 3]] means the same passage in iteration 1 and in the
//     final answer, and the answer's citations resolve without anyone re-searching.
//   - One score scale. Recall and reranking disagree about what "relevant" means,
//     so a search is ordered by one of them, never by a blend.
package retrieve

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"libris/internal/domain"
	"libris/internal/index"
	"libris/internal/llm"
)

// ErrNoKeywords means the caller asked for a search with nothing to search on. It
// is distinct from an empty result because the two need different answers: one is
// a model that forgot to fill in the arguments, the other is a library that has
// nothing on the subject.
var ErrNoKeywords = errors.New("retrieve: no keywords provided")

// Corpus is the store surface recall needs, declared by the consumer.
type Corpus interface {
	// SearchSegments returns the closest segments in one notebook to vector, best
	// first. model is the identity of the space to search — only vectors written by
	// that model are comparable, so the store filters the rest out.
	SearchSegments(ctx context.Context, notebookID string, vector []float32, model string, limit int) ([]domain.Hit, error)
}

// Embedder vectors the keyword probe. Only the query side of the pair is embedded
// here; the document side was embedded at ingest.
type Embedder interface {
	EmbedQuery(ctx context.Context, prefix, text string) ([]float32, error)
	Identity() string
}

// Reranker orders candidates against the full question. Scores come back one per
// document, in the order the documents were given, so the caller never has to
// reason about the wire format's index juggling.
type Reranker interface {
	Score(ctx context.Context, question string, docs []llm.RerankDoc) ([]float64, error)
}

// Assets reads the derived files a segment points at, so a page raster can be
// shown as pixels rather than described as text.
type Assets interface {
	Read(asset domain.Asset) ([]byte, error)
}

// FileAssets reads assets from the paths the store holds. Those paths are written
// by our own sink — a directory under the data dir, names we generate, no ".." and
// never a path that crosses the API — so reading them back is a lookup, not a
// parser. The size guard is the one index already applies to the same bytes.
type FileAssets struct{}

func (FileAssets) Read(asset domain.Asset) ([]byte, error) {
	if asset.Path == "" {
		return nil, fmt.Errorf("retrieve: asset %s has no path", asset.ID)
	}
	raw, err := os.ReadFile(asset.Path)
	if err != nil {
		return nil, fmt.Errorf("retrieve: read asset %s: %w", asset.ID, err)
	}
	if len(raw) > index.MaxAssetBytes {
		return nil, fmt.Errorf("retrieve: asset %s is %d bytes, over the %d byte inline limit",
			asset.ID, len(raw), index.MaxAssetBytes)
	}
	return raw, nil
}

// Retriever is a wired-up set of those four things. It is stateless: the per-turn
// state lives in the Registry the caller passes to Search.
type Retriever struct {
	corpus Corpus
	embed  Embedder
	rerank Reranker // may be nil, which means pure vector order
	assets Assets   // may be nil, which means media is described rather than shown
}

func New(corpus Corpus, embed Embedder, rerank Reranker, assets Assets) *Retriever {
	return &Retriever{corpus: corpus, embed: embed, rerank: rerank, assets: assets}
}

// Options is one search's depth, passed per call rather than read from a snapshot:
// a saved setting takes effect on the next job, and a search is a job.
type Options struct {
	RecallTopK int // candidates pulled from the vector scan
	FinalTopK  int // what one tool result may show, matching the setting of that name
	Rerank     bool
}

// Evidence is a chunk the model may cite, with the label it must use.
type Evidence struct {
	Index      int // the number in [[Chunk N]]; dense, 1-based, per turn
	Segment    domain.Segment
	SourceName string // the source's display name, for labels and for citations
	Score      float64
}

// Search runs recall → rerank → registration and returns what this search added to
// the registry, in document order.
//
// The two inputs are not interchangeable and the split is the whole design:
// keywords is the embedding probe — short, term-like, better vector hits — and
// question is the reranking objective — a full sentence, better ordering. Embedding
// the question or reranking on keywords both work and both cost recall.
//
// An already-registered chunk does not re-enter the returned list: it keeps its
// first id and the model sees it once. That is what keeps a turn that searched four
// times from pasting the same page four times.
func (r *Retriever) Search(
	ctx context.Context,
	reg *Registry,
	notebookID, keywords, question string,
	opts Options,
) ([]Evidence, error) {
	if reg == nil {
		return nil, errors.New("retrieve: search without a registry")
	}
	keywords = strings.TrimSpace(keywords)
	if keywords == "" {
		return nil, ErrNoKeywords
	}
	if opts.RecallTopK <= 0 {
		opts.RecallTopK = 10
	}
	if opts.FinalTopK <= 0 {
		opts.FinalTopK = 5
	}

	vector, err := r.embed.EmbedQuery(ctx, index.QuerySearch, keywords)
	if err != nil {
		return nil, err
	}

	hits, err := r.corpus.SearchSegments(ctx, notebookID, vector, r.embed.Identity(), opts.RecallTopK)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}

	if opts.Rerank && r.rerank != nil {
		reranked, err := r.rerankHits(ctx, question, hits)
		if err == nil {
			hits = reranked
		}
		// On error, vector order stands. A turn is not worth losing over an optional
		// second model, and half the result reranked and half not would be a merged
		// list whose order means nothing — worse than a coarser ordering that means
		// one thing. The caller that logs sees it in the returned order.
	}

	// Truncation happens before registration, so the registry holds exactly the
	// chunks a model was shown. A label for a passage nobody read would be a
	// citation the answer could make and no one could check.
	cands := dedupe(hits, reg)
	if opts.FinalTopK > 0 && len(cands) > opts.FinalTopK {
		cands = cands[:opts.FinalTopK]
	}

	return Order(r.register(reg, cands)), nil
}

// rerankHits scores every candidate against the question. A text chunk goes as
// text; a page raster goes as the raster itself, because a score computed from its
// sibling page's text would be a claim about a document the citation does not point
// at.
//
// Any failure is returned to the caller, which falls back to vector order for the
// whole search — including the case where the rerank server has no mmproj and so
// cannot accept images at all.
func (r *Retriever) rerankHits(ctx context.Context, question string, hits []domain.Hit) ([]domain.Hit, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		// Nothing to rank against. The caller keeps vector order rather than
		// scoring every document against an empty string.
		return nil, errors.New("retrieve: no question to rerank against")
	}

	docs := make([]llm.RerankDoc, len(hits))
	for i, h := range hits {
		doc, err := r.doc(h)
		if err != nil {
			return nil, err
		}
		docs[i] = doc
	}

	scores, err := r.rerank.Score(ctx, question, docs)
	if err != nil {
		return nil, err
	}
	if len(scores) != len(hits) {
		return nil, fmt.Errorf("retrieve: reranker returned %d scores for %d documents", len(scores), len(hits))
	}

	out := make([]domain.Hit, len(hits))
	copy(out, hits)
	for i := range out {
		out[i].Score = scores[i]
	}
	slices.SortStableFunc(out, func(a, b domain.Hit) int {
		return compareFloats(b.Score, a.Score)
	})
	return out, nil
}

func (r *Retriever) doc(h domain.Hit) (llm.RerankDoc, error) {
	if h.Segment.Kind == domain.KindText || h.Segment.Asset == nil {
		if strings.TrimSpace(h.Segment.Text) == "" {
			return llm.RerankDoc{}, fmt.Errorf("retrieve: %s segment %s has neither text nor an asset",
				h.Segment.Kind, h.Segment.ID)
		}
		return llm.TextDoc(h.Segment.Text), nil
	}
	if r.assets == nil {
		return llm.RerankDoc{}, fmt.Errorf("retrieve: segment %s is a %s and no asset reader is wired",
			h.Segment.ID, h.Segment.Kind)
	}
	raw, err := r.assets.Read(*h.Segment.Asset)
	if err != nil {
		return llm.RerankDoc{}, err
	}
	mime := h.Segment.Asset.MIME
	if mime == "" {
		mime = "image/png"
	}
	return llm.ImageDoc("data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)), nil
}

// dedupe drops every candidate the registry already has a label for — whether this
// search produced it twice or an earlier one did.
//
// That is a deliberate difference from the reference, which re-pastes a known chunk
// into the tool result with its original id. Here the transcript is append-only, so
// re-pasting costs the same tokens in every later iteration of the turn, and the
// signal it carries — "your refined query found the same page" — is worth less than
// the context it burns.
func dedupe(hits []domain.Hit, reg *Registry) []domain.Hit {
	seen := make(map[string]bool, len(hits))
	out := make([]domain.Hit, 0, len(hits))
	for _, h := range hits {
		if reg.Holds(h) || seen[key(h)] {
			continue
		}
		seen[key(h)] = true
		out = append(out, h)
	}
	return out
}

// register gives each candidate its id and returns the new evidence. Registration
// order is relevance order — the ids the model gets reflect what the search thought
// most useful, which is why a chunk that arrives a second time keeps the id it was
// first given.
func (r *Retriever) register(reg *Registry, hits []domain.Hit) []Evidence {
	out := make([]Evidence, 0, len(hits))
	for _, h := range hits {
		out = append(out, reg.Register(h))
	}
	return out
}

// Order puts evidence in reading order: document, then position in it.
//
// Registration order and display order differ on purpose. The ids say "what the
// search found", the ordering the model reads says "what the document says", so a
// search result reads like a passage instead of a shuffle.
func Order(evidence []Evidence) []Evidence {
	slices.SortStableFunc(evidence, func(a, b Evidence) int {
		if a.Segment.DocID != b.Segment.DocID {
			return strings.Compare(a.Segment.DocID, b.Segment.DocID)
		}
		return a.Segment.Ordinal - b.Segment.Ordinal
	})
	return evidence
}

// Render turns evidence into the content of a tool result.
//
// A text chunk becomes "[[Chunk N]]" over its text. A page raster becomes a label
// naming the page and the source, then the image itself, because the model can read
// a raster and a description of one is not the same thing.
//
// The parts come back in evidence order and must stay there: llama.cpp pairs each
// image with the media marker in the rendered prompt by position, so reordering
// parts would attach a page's pixels to another page's label.
func (r *Retriever) Render(evidence []Evidence) ([]llm.ContentPart, error) {
	if len(evidence) == 0 {
		return nil, nil
	}

	parts := make([]llm.ContentPart, 0, len(evidence))
	for _, e := range evidence {
		if e.Segment.Kind == domain.KindText || e.Segment.Asset == nil {
			parts = append(parts, llm.TextPart(label(e)+"\n"+e.Segment.Text))
			continue
		}
		if r.assets == nil {
			parts = append(parts, llm.TextPart(label(e)+"\n(the image itself is not available to this model)"))
			continue
		}
		raw, err := r.assets.Read(*e.Segment.Asset)
		if err != nil {
			return nil, err
		}
		mime := e.Segment.Asset.MIME
		if mime == "" {
			mime = "image/png"
		}
		parts = append(parts,
			llm.TextPart(label(e)+"\n"),
			llm.ImageBase64(mime, base64.StdEncoding.EncodeToString(raw)),
		)
	}
	return parts, nil
}

// label is the citation handle plus, for media, what it is: a bare [[Chunk N]] over
// an image gives the model nothing to reason about when it decides whether this
// evidence answers the question.
func label(e Evidence) string {
	if e.Segment.Kind == domain.KindText {
		return fmt.Sprintf("[[Chunk %d]]", e.Index)
	}
	page := ""
	if e.Segment.Span.Page > 0 {
		page = fmt.Sprintf(" page %d", e.Segment.Span.Page)
	}
	return fmt.Sprintf("[[Chunk %d]] %s%s — image", e.Index, e.SourceName, page)
}

func compareFloats(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
