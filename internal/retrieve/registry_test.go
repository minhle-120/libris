package retrieve

import (
	"strings"
	"testing"
)

// The whole citation scheme is "small integer, stable for the turn". Everything here
// is that claim stated four ways.
func TestRegistryMintsDenseIds(t *testing.T) {
	reg := NewRegistry()

	first := reg.Register(textHit("doc1", 0, "one"))
	second := reg.Register(textHit("doc1", 1, "two"))
	third := reg.Register(textHit("doc2", 0, "three"))

	if first.Index != 1 || second.Index != 2 || third.Index != 3 {
		t.Errorf("ids %d %d %d, want 1 2 3", first.Index, second.Index, third.Index)
	}
	if reg.Len() != 3 {
		t.Errorf("Len() = %d", reg.Len())
	}
	for _, bad := range []int{0, -1, 4} {
		if _, ok := reg.Resolve(bad); ok {
			t.Errorf("[[Chunk %d]] resolved, want not-found: a model that invents an index must shorten the context, not crash the turn", bad)
		}
	}
}

// Two searches, one chunk: one label. This is what keeps a four-search turn from
// asking the answer model to hold the same page four times.
func TestRegistryDeduplicatesOnText(t *testing.T) {
	reg := NewRegistry()

	a := reg.Register(textHit("doc1", 0, "  the same passage  "))
	b := reg.Register(textHit("doc1", 0, "the same passage")) // trimmed: the same chunk
	c := reg.Register(textHit("doc9", 7, "the same passage")) // a different copy, same words

	if a.Index != b.Index {
		t.Errorf("the same chunk got %d and %d", a.Index, b.Index)
	}
	if c.Index != a.Index {
		t.Errorf("identical text in another document got a new id %d: dedupe is on content, which is what makes it cheap", c.Index)
	}
	if reg.Len() != 1 {
		t.Errorf("Len() = %d, want 1", reg.Len())
	}
}

// The bug this exists to prevent. A page raster has no text, so keying on content
// would give every page image in the corpus the same key: the first becomes
// [[Chunk 4]] and every later page silently returns it, so the citation opens the
// wrong page and the model believes it has one image where it found five.
func TestRegistryDoesNotCollapsePageImages(t *testing.T) {
	reg := NewRegistry()

	ids := map[int]string{}
	for page := 1; page <= 5; page++ {
		ev := reg.Register(imageHit("doc1#pages", page, page))
		if ev.Segment.Text != "" {
			t.Fatal("fixture: a raster should have no text")
		}
		if _, dup := ids[ev.Index]; dup {
			t.Fatalf("page %d reused [[Chunk %d]] — every empty string collapsed onto one key", page, ev.Index)
		}
		ids[ev.Index] = ev.Segment.ID
	}
	if len(ids) != 5 {
		t.Errorf("%d ids for 5 distinct pages, want 5", len(ids))
	}

	// The same page found again — by a second search, say — is still one chunk.
	again := reg.Register(imageHit("doc1#pages", 3, 3))
	if again.Segment.ID != ids[again.Index] {
		t.Errorf("page 3 came back as %+v", again)
	}
}

// The two key namespaces are tagged, so a passage whose words happen to name a
// segment id cannot steal that segment's evidence.
func TestRegistryKeepsTheKeyNamespacesApart(t *testing.T) {
	reg := NewRegistry()

	text := reg.Register(textHit("doc1", 0, "s:doc2:0000"))
	raster := imageHit("doc2", 0, 1)
	raster.Segment.ID = "doc2:0000"
	img := reg.Register(raster)

	if text.Index == img.Index {
		t.Errorf("a passage reading %q took the raster's id", text.Segment.Text)
	}
}

// Ids are per turn. Carrying them across turns would let a citation in old chat
// history point at a chunk from a later question.
func TestRegistryResetRestartsAtOne(t *testing.T) {
	reg := NewRegistry()
	reg.Register(textHit("doc1", 0, "turn one"))
	reg.Reset()

	if reg.Len() != 0 {
		t.Fatalf("Reset left %d chunks", reg.Len())
	}
	if _, ok := reg.Resolve(1); ok {
		t.Error("[[Chunk 1]] still resolves after Reset")
	}
	next := reg.Register(textHit("doc2", 0, "turn two"))
	if next.Index != 1 {
		t.Errorf("the next turn starts at [[Chunk %d]], want 1", next.Index)
	}
}

// All() is the salvage path: research that spent its budget without finalizing still
// gets to answer from everything it found, in the order it found it.
func TestRegistryAllIsInIdOrderAndACopy(t *testing.T) {
	reg := NewRegistry()
	reg.Register(textHit("doc1", 0, "a"))
	reg.Register(textHit("doc1", 1, "b"))

	all := reg.All()
	if len(all) != 2 || all[0].Index != 1 || all[1].Index != 2 {
		t.Fatalf("All() = %+v", all)
	}
	all[0].Segment.Text = "mutated"
	if got, _ := reg.Resolve(1); got.Segment.Text != "a" {
		t.Error("All() handed out the registry's own slice")
	}
}

// Evidence carries the source's display name from the moment it is minted, because
// the label and the citation both need it and neither should have to ask the store.
func TestEvidenceCarriesItsSourceName(t *testing.T) {
	reg := NewRegistry()
	ev := reg.Register(textHit("smith2020", 0, "erosion"))

	if ev.SourceName != "smith2020.pdf" {
		t.Errorf("SourceName = %q", ev.SourceName)
	}
	if !strings.Contains(label(ev), "[[Chunk 1]]") {
		t.Errorf("label = %q", label(ev))
	}
}

// A model that writes [[Chunk 99]] after four searches is misremembering, not
// inventing a chunk — and the answer it would otherwise lose was grounded.
func TestRegistryOnlyResolvesWhatItHas(t *testing.T) {
	reg := NewRegistry()
	reg.Register(textHit("doc1", 5, "third found"))  // [[Chunk 1]]
	reg.Register(textHit("doc1", 0, "second found")) // [[Chunk 2]]
	reg.Register(textHit("doc2", 0, "first found"))  // [[Chunk 3]]

	got := reg.Only([]int{3, 1, 2, 99, 3, 0, -1})
	if len(got) != 3 {
		t.Fatalf("resolved %v from [3 1 2 99 3 0 -1]: duplicates collapse, unknown labels drop", segIDs(got))
	}
	// Document order, not the order the model listed them in, so the synthesis
	// prompt reads like a passage however the model shuffled.
	want := []string{"doc1:0000", "doc1:0005", "doc2:0000"}
	if strings.Join(segIDs(got), ",") != strings.Join(want, ",") {
		t.Errorf("resolved %v, want %v", segIDs(got), want)
	}
	for _, e := range got {
		if e.Index < 1 || e.Index > 3 {
			t.Errorf("evidence carries label %d, which the model never saw", e.Index)
		}
	}

	if len(reg.Only(nil)) != 0 {
		t.Error("no indices resolved to something")
	}
}
