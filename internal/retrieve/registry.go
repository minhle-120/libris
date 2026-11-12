package retrieve

import (
	"strings"

	"libris/internal/domain"
)

// Registry is the per-turn evidence store: it gives every recalled chunk a small
// integer and holds the chunk behind it.
//
// That indirection is what lets a model cite a passage without ever copying it. The
// research agent's tool results carry chunk text, but the answer is written from a
// separate call with only the chunks it chose, and the [[Chunk N]] tags in that
// answer still resolve — because the ids were minted once, here, and nothing about
// them depends on where a chunk was found or in which iteration.
//
// Ids are dense, 1-based and monotonic for one turn, then restart. A turn is a
// conversation about one question; carrying ids across turns would mean a citation
// in old chat history pointing at a chunk in a later one.
//
// It is not safe for concurrent use. One turn, one loop, one goroutine.
type Registry struct {
	byKey map[string]int
	items []Evidence
}

func NewRegistry() *Registry {
	return &Registry{byKey: map[string]int{}}
}

// Reset returns the registry to empty so the next turn starts at [[Chunk 1]].
func (r *Registry) Reset() {
	r.byKey = make(map[string]int)
	r.items = r.items[:0]
}

// Register returns the evidence for a hit, creating it on first sight.
//
// The same chunk found by two different searches gets one id, so a turn that
// re-searches the same ground cites it once and the synthesis prompt is not asked
// to hold it twice.
func (r *Registry) Register(hit domain.Hit) Evidence {
	k := key(hit)
	if idx, ok := r.byKey[k]; ok {
		return r.items[idx-1]
	}
	ev := Evidence{
		Index:      len(r.items) + 1,
		Segment:    hit.Segment,
		SourceName: hit.SourceName,
		Score:      hit.Score,
	}
	r.byKey[k] = ev.Index
	r.items = append(r.items, ev)
	return ev
}

// Holds reports whether this chunk already has a label. Register would hand back
// the same one; search uses this to keep the known chunk out of a tool result
// instead of paying for it twice.
func (r *Registry) Holds(hit domain.Hit) bool {
	_, ok := r.byKey[key(hit)]
	return ok
}

// Resolve looks evidence up by the number the model wrote. An index that does not
// resolve is not an error: a model that invents [[Chunk 99]] is hallucinating, and
// the honest response is to answer from the chunks that do exist rather than crash.
func (r *Registry) Resolve(index int) (Evidence, bool) {
	if index < 1 || index > len(r.items) {
		return Evidence{}, false
	}
	return r.items[index-1], true
}

// All returns every chunk registered this turn, in id order. It is the fallback
// evidence set: research that ran out of budget with twenty chunks registered still
// gets to answer from them.
func (r *Registry) All() []Evidence {
	out := make([]Evidence, len(r.items))
	copy(out, r.items)
	return out
}

func (r *Registry) Len() int { return len(r.items) }

// key is how "the same chunk" is decided.
//
// Text chunks key on their trimmed text, which is what makes two searches that both
// find the same passage collapse to one id.
//
// A chunk with no text keys on its identity instead. This is not a detail: a page
// raster's Text is empty, so keying on content would give *every page image in the
// corpus the same key* — the first would become [[Chunk 4]] and every later page
// would silently return that id, and a citation would open the wrong page.
//
// The two namespaces are tagged so a passage whose text happens to read "s:abc"
// cannot collide with a segment id.
func key(hit domain.Hit) string {
	if text := strings.TrimSpace(hit.Segment.Text); text != "" {
		return "t:" + text
	}
	return "s:" + hit.Segment.ID
}

// Only turns the indices a model wrote into evidence.
//
// Duplicates collapse and an index outside the registry is dropped rather than
// failing the turn: a model that writes [[Chunk 99]] after four searches is
// misremembering, not inventing a chunk, and the answer it would otherwise lose was
// grounded. The drop is not silent to a caller that counts — len(out) < len(indices)
// is the whole signal.
//
// The result is in document order, not in the order the model listed them, so the
// synthesis prompt reads like a passage no matter how the model shuffled.
func (r *Registry) Only(indices []int) []Evidence {
	seen := make(map[int]bool, len(indices))
	out := make([]Evidence, 0, len(indices))
	for _, i := range indices {
		if seen[i] {
			continue
		}
		ev, ok := r.Resolve(i)
		if !ok {
			continue
		}
		seen[i] = true
		out = append(out, ev)
	}
	return Order(out)
}
