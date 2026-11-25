// Package retrieve answers one question: which of a notebook's chunks should a
// model be shown, and under what label?
//
// It is recall → evidence registry, and nothing else. No prompts, no LLM turns,
// no knowledge that a research loop exists — chat owns the tools and the wording,
// and this package owns the two properties the loop depends on:
//
//   - Stable labels. Every recalled segment gets a dense, 1-based id for the rest
//     of the turn, so [[Chunk 3]] means the same passage in iteration 1 and in the
//     final answer, and the answer's citations resolve without anyone re-searching.
//   - One score scale. Every candidate is ordered by the vector scan's similarity
//     and by nothing else, so the order in a tool result means one thing.
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

// Retriever is a wired-up set of those three things. It is stateless: the per-turn
// state lives in the Registry the caller passes to Search.
type Retriever struct {
	corpus Corpus
	embed  Embedder
	assets Assets // may be nil, which means media is described rather than shown
	caps   *llm.Caps
}

// New wires a retriever. Two of the four arguments may be nil, and each nil means
// something different:
//
//   - assets nil: the derived files cannot be read here, so a media chunk is
//     labelled and described instead of shown.
//   - caps nil: nobody asked the serving model what it takes, so media is sent and a
//     server that cannot take it answers with its own error. See llm.Caps.
func New(corpus Corpus, embed Embedder, assets Assets, caps *llm.Caps) *Retriever {
	return &Retriever{corpus: corpus, embed: embed, assets: assets, caps: caps}
}

// Options is one search's depth, passed per call rather than read from a snapshot:
// a saved setting takes effect on the next job, and a search is a job.
type Options struct {
	RecallTopK int // candidates pulled from the vector scan
	FinalTopK  int // what one tool result may show, matching the setting of that name
}

// Evidence is a chunk the model may cite, with the label it must use.
type Evidence struct {
	Index      int // the number in [[Chunk N]]; dense, 1-based, per turn
	Segment    domain.Segment
	SourceName string // the source's display name, for labels and for citations
	Score      float64
}

// Search runs recall and registration, and returns what it added to the registry,
// in document order.
//
// keywords is the embedding probe: short, term-like, better vector hits than the
// whole question would give. A full sentence embedded against term-like chunks is
// the trade a reranker would have bought back, and there is no reranker.
//
// An already-registered chunk does not re-enter the returned list: it keeps its
// first id and the model sees it once. That is what keeps a turn that searched four
// times from pasting the same page four times.
func (r *Retriever) Search(
	ctx context.Context,
	reg *Registry,
	notebookID, keywords string,
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

	// Truncation happens before registration, so the registry holds exactly the
	// chunks a model was shown. A label for a passage nobody read would be a
	// citation the answer could make and no one could check.
	cands := dedupe(hits, reg)
	if opts.FinalTopK > 0 && len(cands) > opts.FinalTopK {
		cands = cands[:opts.FinalTopK]
	}

	return Order(r.register(reg, cands)), nil
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

// ChunkSeparator sits between two chunks of one rendered set, so a search result
// reads as separate passages rather than one run-on page. It is exported because the
// research tools append their own note after a rendered set and the note wants the
// same gap the chunks have from each other.
const ChunkSeparator = "\n\n"

// Render turns evidence into the content a model is shown. Both consumers are built
// here — the research turn's tool result and the answer's knowledge block — and that
// is the point: the model deciding what to cite sees what the model writing the
// answer will see, pixels included.
//
// A text chunk becomes "[[Chunk N]]" over its text. A media chunk becomes a label
// naming the page and the source, then the file itself, because the model can read a
// raster and a description of one is not the same thing.
//
// Two properties are this function's to keep, which is why the callers do not do
// either for themselves:
//
//   - Order. The parts come back in evidence order and must stay there: the server
//     pairs each media file with the marker it leaves in the rendered prompt by
//     position (see llm.MediaPart), so reordering would attach one page's pixels to
//     another page's label.
//   - Separation. Chunks are divided here rather than by the caller, because a raster
//     is two parts — its label, then its pixels — and a divider added between them
//     would leave a page's label detached from its image.
//
// Evidence holding media the serving model was never started to take is an error
// rather than a silently smaller prompt: see canShow.
func (r *Retriever) Render(evidence []Evidence) ([]llm.ContentPart, error) {
	if len(evidence) == 0 {
		return nil, nil
	}
	if err := r.canShow(evidence); err != nil {
		return nil, err
	}

	parts := make([]llm.ContentPart, 0, 2*len(evidence))
	for i, e := range evidence {
		if i > 0 {
			parts = append(parts, llm.TextPart(ChunkSeparator))
		}
		switch {
		case e.Segment.Kind == domain.KindText || e.Segment.Asset == nil:
			parts = append(parts, llm.TextPart(label(e)+"\n"+e.Segment.Text))
		default:
			if r.assets == nil {
				parts = append(parts, llm.TextPart(label(e)+
					"\n("+string(e.Segment.Kind)+" itself is not available to this model)"))
				continue
			}
			kind, ok := wireKind(e.Segment.Kind)
			if !ok {
				return nil, fmt.Errorf("retrieve: no wire shape for segment kind %q", e.Segment.Kind)
			}
			raw, err := r.assets.Read(*e.Segment.Asset)
			if err != nil {
				return nil, err
			}
			mime := e.Segment.Asset.MIME
			if mime == "" {
				mime = "image/png" // what a page raster renders to; see ingest's PDF stage
			}
			media, err := llm.MediaPart(kind, mime, base64.StdEncoding.EncodeToString(raw))
			if err != nil {
				return nil, err
			}
			parts = append(parts, llm.TextPart(label(e)+"\n"), media)
		}
	}
	return parts, nil
}

// canShow refuses an evidence set holding media the serving model cannot be given.
//
// Stopping here rather than sending it is the whole reason: a model started without
// the projector for a modality throws on the request, and the account of that failure
// is a sentence from C++ about somebody's mmproj — arriving where a person expected an
// answer. This says which model, which modality, and how many pieces of evidence it
// cost.
//
// The count is over the whole set because Render is handed the whole set: one refusal
// per turn, naming every page the model would have missed. A nil Caps is allowed
// through on purpose — not asking is not the same as knowing the answer is no.
func (r *Retriever) canShow(evidence []Evidence) error {
	if r.assets == nil {
		return nil // nothing here would have carried pixels anyway
	}
	for _, e := range evidence {
		kind, ok := wireKind(e.Segment.Kind)
		if !ok || r.caps.Allows(kind) {
			continue
		}
		n := 0
		for _, o := range evidence {
			if k, ok := wireKind(o.Segment.Kind); ok && k == kind && o.Segment.Asset != nil {
				n++
			}
		}
		return &llm.MediaUnsupported{Kind: kind, Model: r.caps.Model, Count: n}
	}
	return nil
}

// wireKind maps a segment's modality onto the one the wire speaks. A kind domain knows
// that llm has no part type for is refused where it would be rendered, never turned
// into pixels under a guessed type.
func wireKind(k domain.Kind) (llm.MediaKind, bool) {
	if !k.Media() {
		return "", false
	}
	switch k {
	case domain.KindImage:
		return llm.MediaImage, true
	}
	return "", false
}

// label is the citation handle plus, for media, what it is: a bare [[Chunk N]] over an
// image gives the model nothing to reason about when it decides whether this evidence
// answers the question — the pixels are the content, and the only thing a label can
// point at is where they came from.
func label(e Evidence) string {
	if e.Segment.Kind == domain.KindText {
		return fmt.Sprintf("[[Chunk %d]]", e.Index)
	}
	page := ""
	if e.Segment.Span.Page > 0 {
		page = fmt.Sprintf(" page %d", e.Segment.Span.Page)
	}
	return fmt.Sprintf("[[Chunk %d]] %s%s — %s", e.Index, e.SourceName, page, e.Segment.Kind)
}
