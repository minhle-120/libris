package sqlite

import (
	"context"
	"testing"

	"libris/internal/domain"
)

// seed puts one notebook's worth of vectors on disk: two sources, both ready, one
// of them also holding a page raster with no text.
func seed(t *testing.T, model string) (*DB, string) {
	t.Helper()
	ctx := context.Background()
	db := openDB(t)
	nb := notebook(t, db, "A")

	for _, id := range []string{"sha1", "sha2"} {
		db.AddSource(ctx, nb, source(id, id+".pdf"))
		segs := pdfSegments(id)
		if err := db.PutAssets(ctx, id, []domain.Asset{*segs[1].Asset}); err != nil {
			t.Fatal(err)
		}
		if err := db.PutSegments(ctx, id, segs); err != nil {
			t.Fatal(err)
		}
		if err := db.PutEmbeddings(ctx, []domain.Embedding{
			{SegmentID: id + ":0000", Model: model, Dim: 3, Vector: []float32{1, 0, 0}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSourceStatus(ctx, id, domain.StatusReady, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	return db, nb
}

func TestSearchSegmentsOrdersByCosine(t *testing.T) {
	ctx := context.Background()
	db, nb := seed(t, "m+tp1")

	// A third source whose chunk points the other way, so ordering can fail.
	db.AddSource(ctx, nb, source("sha3", "third.pdf"))
	db.PutSegments(ctx, "sha3", []domain.Segment{
		{ID: "sha3:0000", SourceID: "sha3", DocID: "sha3", Kind: domain.KindText, Ordinal: 0, Text: "off"},
	})
	db.PutEmbeddings(ctx, []domain.Embedding{
		{SegmentID: "sha3:0000", Model: "m+tp1", Dim: 3, Vector: []float32{0, 1, 0}},
	})
	db.SetSourceStatus(ctx, "sha3", domain.StatusReady, "", nil)

	hits, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0}, "m+tp1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("%d hits, want 3: %+v", len(hits), hits)
	}
	if hits[0].Segment.ID == "sha3:0000" || hits[2].Segment.ID != "sha3:0000" {
		t.Errorf("ordered %+v, want the perpendicular chunk last", hitIDs(hits))
	}
	if got := hits[0].Score; got < 0.999 || got > 1.001 {
		t.Errorf("top score = %v, want ~1.0 (cosine over aligned unit vectors)", got)
	}
	if hits[2].Score > 1e-6 {
		t.Errorf("perpendicular chunk scored %v, want 0", hits[2].Score)
	}
	// The name the citation will show comes with the row; retrieval must not have
	// to ask for it again.
	if hits[0].SourceName == "" {
		t.Error("hit carries no source name")
	}
}

func TestSearchSegmentsScopesTheCorpus(t *testing.T) {
	ctx := context.Background()
	db, nb := seed(t, "m+tp1")

	// A notebook that does not hold sha1 must not read its chunks.
	other := notebook(t, db, "B")
	db.AddSource(ctx, other, source("shax", "elsewhere.pdf"))
	db.PutSegments(ctx, "shax", []domain.Segment{
		{ID: "shax:0000", SourceID: "shax", DocID: "shax", Kind: domain.KindText, Ordinal: 0, Text: "private"},
	})
	db.PutEmbeddings(ctx, []domain.Embedding{{SegmentID: "shax:0000", Model: "m+tp1", Dim: 3, Vector: []float32{1, 0, 0}}})
	db.SetSourceStatus(ctx, "shax", domain.StatusReady, "", nil)

	// A source still working must be invisible even in its own notebook.
	db.AddSource(ctx, nb, source("sha9", "busy.pdf"))
	db.PutSegments(ctx, "sha9", []domain.Segment{
		{ID: "sha9:0000", SourceID: "sha9", DocID: "sha9", Kind: domain.KindText, Ordinal: 0, Text: "half-written"},
	})
	db.PutEmbeddings(ctx, []domain.Embedding{{SegmentID: "sha9:0000", Model: "m+tp1", Dim: 3, Vector: []float32{1, 0, 0}}})

	// A chunk whose vectors were written by another model — a different space.
	db.AddSource(ctx, nb, source("shastale", "stale.pdf"))
	db.PutSegments(ctx, "shastale", []domain.Segment{
		{ID: "shastale:0000", SourceID: "shastale", DocID: "shastale", Kind: domain.KindText, Ordinal: 0, Text: "old space"},
	})
	db.PutEmbeddings(ctx, []domain.Embedding{{SegmentID: "shastale:0000", Model: "other-model+tp1", Dim: 3, Vector: []float32{1, 0, 0}}})
	db.SetSourceStatus(ctx, "shastale", domain.StatusReady, "", nil)

	own, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0}, "m+tp1", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, h := range own {
		got[h.Segment.ID] = true
	}
	for _, want := range []string{"sha1:0000", "sha2:0000"} {
		if !got[want] {
			t.Errorf("%s missing from its own notebook's search: %v", want, hitIDs(own))
		}
	}
	for _, no := range []string{"shax:0000", "sha9:0000", "shastale:0000"} {
		if got[no] {
			t.Errorf("%s was recalled but must not be (other notebook / processing / other vector space)", no)
		}
	}

	theirs, err := db.SearchSegments(ctx, other, []float32{1, 0, 0}, "m+tp1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 1 || theirs[0].Segment.ID != "shax:0000" {
		t.Errorf("notebook B recalled %v, only its own source", hitIDs(theirs))
	}
}

// A page raster has no text. If the scan dropped textless rows — or if anything
// downstream keyed on their content — a corpus of scanned PDFs would silently stop
// being searchable.
func TestSearchSegmentsRecallsPageRasters(t *testing.T) {
	ctx := context.Background()
	db, nb := seed(t, "m+tp1")
	db.PutEmbeddings(ctx, []domain.Embedding{
		{SegmentID: "sha1:0001", Model: "m+tp1", Dim: 3, Vector: []float32{1, 0, 0}},
	})

	hits, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0}, "m+tp1", 10)
	if err != nil {
		t.Fatal(err)
	}
	var raster *domain.Hit
	for i, h := range hits {
		if h.Segment.ID == "sha1:0001" {
			raster = &hits[i]
		}
	}
	if raster == nil {
		t.Fatalf("the page raster was not recalled: %v", hitIDs(hits))
	}
	if raster.Segment.Text != "" {
		t.Errorf("raster text = %q, want empty", raster.Segment.Text)
	}
	if raster.Segment.Asset == nil || raster.Segment.Asset.Path == "" {
		t.Error("raster came back without its asset, so the model could not be shown the page")
	}
}

func TestSearchSegmentsHonoursLimit(t *testing.T) {
	ctx := context.Background()
	db, nb := seed(t, "m+tp1")

	hits, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0}, "m+tp1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("%d hits, want 1: %v", len(hits), hitIDs(hits))
	}
	// Identical vectors tie, so the survivor is whichever came first in document
	// order — which is why the sort is stable and the scan is ordered.
	if hits[0].Segment.ID != "sha1:0000" {
		t.Errorf("limit 1 kept %s, want the first in document order", hits[0].Segment.ID)
	}
}

// A blob shorter than the dim it declares would give a short dot product that still
// computes. That is the one corruption a score cannot reveal, so it is an error.
func TestSearchSegmentsRejectsTruncatedVector(t *testing.T) {
	ctx := context.Background()
	db, nb := seed(t, "m+tp1")

	if _, err := db.Raw().ExecContext(ctx,
		`UPDATE segments SET embedding = X'00000000' WHERE id = 'sha1:0000'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0}, "m+tp1", 10); err == nil {
		t.Fatal("a truncated vector scanned without complaint")
	}
}

func TestSearchSegmentsRefusesBadArguments(t *testing.T) {
	ctx := context.Background()
	db, nb := seed(t, "m+tp1")

	if _, err := db.SearchSegments(ctx, nb, nil, "m+tp1", 10); err == nil {
		t.Error("an empty query vector was accepted")
	}
	if _, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0}, "m+tp1", 0); err == nil {
		t.Error("a limit of 0 was accepted")
	}
	// A zero-length query has no direction: every cosine would be undefined, so it
	// is refused rather than answered with a corpus of zeros.
	if _, err := db.SearchSegments(ctx, nb, []float32{0, 0, 0}, "m+tp1", 10); err == nil {
		t.Error("a zero-length query vector was accepted")
	}
}

func hitIDs(hits []domain.Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Segment.ID
	}
	return out
}
