package sqlite

import (
	"context"
	"errors"
	"testing"

	"libris/internal/domain"
)

// fill writes the rows a finished source has — segments and the derived files
// they point at — so a test about deletion is testing a source that looks real.
func fill(t *testing.T, db *DB, id string, assets ...string) {
	t.Helper()
	ctx := context.Background()
	var rows []domain.Asset
	for i, p := range assets {
		rows = append(rows, domain.Asset{
			ID: id + "/pages/page-000" + string(rune('1'+i)) + ".png",
			// Path is what the collector hands back, so it has to be the real one.
			Path: p, MIME: "image/png", Bytes: 4096,
		})
	}
	if err := db.PutAssets(ctx, id, rows); err != nil {
		t.Fatalf("PutAssets: %v", err)
	}
	segs := []domain.Segment{{ID: id + ":0000", SourceID: id, DocID: id, Kind: domain.KindText, Text: "page one"}}
	for i := range assets {
		asset := rows[i]
		segs = append(segs, domain.Segment{
			ID: asset.ID, SourceID: id, DocID: id, Kind: domain.KindImage, Asset: &asset,
			Span: domain.Provenance{Page: i + 1, PageEnd: i + 1},
		})
	}
	if err := db.PutSegments(ctx, id, segs); err != nil {
		t.Fatalf("PutSegments: %v", err)
	}
}

// The last membership is the deletion. A source no notebook lists is not waiting
// for anything, and the rows it leaves behind would answer no query and cite no
// file anyone can reach.
func TestDetachSourceCollectsTheLastMembership(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	db.AddSource(ctx, a, source("sha1", "paper.pdf"))
	fill(t, db, "sha1", "/data/assets/sha1/pages/page-0001.png", "/data/assets/sha1/pages/page-0002.png")

	orphans, err := db.DetachSource(ctx, a, "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 {
		t.Fatalf("orphans = %d, want the one source that just lost its last notebook", len(orphans))
	}
	got := orphans[0]
	if got.ID != "sha1" || got.Name != "paper.pdf" {
		t.Errorf("orphan = %+v, want it named so a log can say what went", got)
	}
	if got.Original != "/data/originals/sha1" {
		t.Errorf("original = %q, want the kept original's path", got.Original)
	}
	want := []string{
		"/data/assets/sha1/pages/page-0001.png",
		"/data/assets/sha1/pages/page-0002.png",
	}
	if len(got.Assets) != len(want) {
		t.Fatalf("assets = %v, want %v", got.Assets, want)
	}
	for i := range want {
		if got.Assets[i] != want[i] {
			t.Errorf("assets[%d] = %q, want %q", i, got.Assets[i], want[i])
		}
	}

	// Rows first, everywhere: segments and assets go with the source, which is
	// what the schema's cascade guarantees and what stops a query finding a
	// citation pointing at a file that was never offered.
	for _, q := range []string{
		`SELECT COUNT(*) FROM sources WHERE id = 'sha1'`,
		`SELECT COUNT(*) FROM segments WHERE source_id = 'sha1'`,
		`SELECT COUNT(*) FROM assets WHERE source_id = 'sha1'`,
	} {
		if n := mustInt(t, db, q); n != 0 {
			t.Errorf("%s = %d, want the source collected", q, n)
		}
	}
	if _, err := db.GetSource(ctx, "sha1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSource err = %v, want ErrNotFound", err)
	}
}

// Deleting a notebook is a batch of detachments at once: the sources it shared
// stay, and only the ones it was the last to list are collected.
func TestDeleteNotebookCollectsOnlyTheSourcesItLeftAlone(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	b := notebook(t, db, "B")

	db.AddSource(ctx, a, source("shared", "shared.pdf"))
	db.AddSource(ctx, b, source("shared", "shared.pdf"))
	db.AddSource(ctx, a, source("mine", "mine.pdf"))
	fill(t, db, "shared", "/data/assets/shared/pages/page-0001.png")
	fill(t, db, "mine", "/data/assets/mine/pages/page-0001.png")

	orphans, err := db.DeleteNotebook(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].ID != "mine" {
		t.Fatalf("orphans = %+v, want only the source B does not list", orphans)
	}
	if orphans[0].Original != "/data/originals/mine" {
		t.Errorf("original = %q", orphans[0].Original)
	}

	// The shared one is untouched in every table, rasters included — re-opening
	// it in B must not cost a second upload or a second pass through poppler.
	if n := mustInt(t, db, `SELECT COUNT(*) FROM sources WHERE id = 'shared'`); n != 1 {
		t.Errorf("shared source rows = %d, want 1", n)
	}
	if n := mustInt(t, db, `SELECT COUNT(*) FROM segments WHERE source_id = 'shared'`); n != 2 {
		t.Errorf("shared segments = %d, want them kept", n)
	}
	if n := mustInt(t, db, `SELECT COUNT(*) FROM assets WHERE source_id = 'shared'`); n != 1 {
		t.Errorf("shared assets = %d, want it kept", n)
	}
	if n := mustInt(t, db, `SELECT COUNT(*) FROM sources WHERE id = 'mine'`); n != 0 {
		t.Errorf("orphan rows = %d, want it collected", n)
	}
	if list, _ := db.ListSources(ctx, b); len(list) != 1 || list[0].ID != "shared" {
		t.Errorf("B's sources = %+v, want just the shared one", list)
	}
}

// A source that never had a second notebook is collected by the notebook's own
// deletion — the case the confirmation dialog warns about.
func TestDeleteNotebookCollectsASourceItWasTheOnlyUserOf(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	db.AddSource(ctx, a, source("only", "only.pdf"))
	fill(t, db, "only", "/data/assets/only/pages/page-0001.png")

	orphans, err := db.DeleteNotebook(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].ID != "only" {
		t.Fatalf("orphans = %+v, want the sole source", orphans)
	}
	if n := mustInt(t, db, `SELECT COUNT(*) FROM segments`); n != 0 {
		t.Errorf("segments = %d, want them gone with the source", n)
	}
}

// NotebooksUsing is what lets the UI promise the right thing: the row that says
// "1" is the row whose removal costs a file.
func TestSourcesReportHowManyNotebooksUseThem(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	b := notebook(t, db, "B")
	db.AddSource(ctx, a, source("shared", "shared.pdf"))
	db.AddSource(ctx, a, source("mine", "mine.pdf"))
	db.AddSource(ctx, b, source("shared", "shared.pdf"))

	for _, want := range []struct {
		id   string
		uses int
	}{{"shared", 2}, {"mine", 1}} {
		got, err := db.GetSource(ctx, want.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.NotebooksUsing != want.uses {
			t.Errorf("%s: NotebooksUsing = %d, want %d", want.id, got.NotebooksUsing, want.uses)
		}
	}
	// The notebook's own list carries the same number, so a row can say "this is
	// the only copy" without a second request per source.
	list, err := db.ListSources(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListSources = %d, want 2", len(list))
	}
	for _, s := range list {
		want := 1
		if s.ID == "shared" {
			want = 2
		}
		if s.NotebooksUsing != want {
			t.Errorf("%s in A: NotebooksUsing = %d, want %d", s.ID, s.NotebooksUsing, want)
		}
	}
}
