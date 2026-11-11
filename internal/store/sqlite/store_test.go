package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"libris/internal/domain"
)

func openDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "libris.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func notebook(t *testing.T, db *DB, name string) string {
	t.Helper()
	n, err := db.CreateNotebook(context.Background(), name)
	if err != nil {
		t.Fatalf("CreateNotebook: %v", err)
	}
	return n.ID
}

func source(id, name string) domain.Source {
	return domain.Source{
		ID: id, Name: name, Kind: domain.SourcePDF, SHA256: id, Bytes: 1234,
		OriginalPath: "/data/originals/" + id, Status: domain.StatusProcessing,
	}
}

func TestOpenMakesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "nested")
	db, err := Open(filepath.Join(dir, "libris.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()

	// Reopening is the real test: migrations must be a no-op the second time.
	again, err := Open(filepath.Join(dir, "libris.db"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	if got := mustInt(t, again, `SELECT COUNT(*) FROM schema_migrations`); got != 1 {
		t.Fatalf("migrations applied = %d, want 1", got)
	}
}

func TestOpenPragmas(t *testing.T) {
	db := openDB(t)
	if got := mustText(t, db, `PRAGMA journal_mode`); got != "wal" {
		t.Errorf("journal_mode = %q, want wal", got)
	}
	if got := mustInt(t, db, `PRAGMA foreign_keys`); got != 1 {
		t.Errorf("foreign_keys = %d, want 1", got)
	}
}

func mustInt(t *testing.T, db *DB, q string) int {
	t.Helper()
	var n int
	if err := db.Raw().QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func mustText(t *testing.T, db *DB, q string) string {
	t.Helper()
	var s string
	if err := db.Raw().QueryRow(q).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s
}

// ── notebooks ────────────────────────────────────────────────────

// TestNotebookSourceCount covers the count the sidebar shows. A just-created
// notebook reports 0 without a query, so the read path is what proves it.
func TestNotebookSourceCount(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)

	a, err := db.CreateNotebook(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.CreateNotebook(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}

	// One file in both notebooks, and a second file in the first.
	for _, nb := range []string{a.ID, b.ID} {
		if _, _, err := db.AddSource(ctx, nb, source("one", "one.pdf")); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := db.AddSource(ctx, a.ID, source("two", "two.pdf")); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	list, err := db.ListNotebooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, nb := range list {
		counts[nb.ID] = nb.SourceCount
	}
	if counts[a.ID] != 2 || counts[b.ID] != 1 {
		t.Errorf("counts = %v, want 2 for the first notebook and 1 for the second", counts)
	}

	// A shared source counts once per notebook, not once per copy.
	got, err := db.GetNotebook(ctx, b.ID)
	if err != nil || got.SourceCount != 1 {
		t.Errorf("GetNotebook = %+v, %v", got, err)
	}

	// Deleting a source's membership drops the count; the source survives.
	if err := db.DetachSource(ctx, a.ID, "one"); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetNotebook(ctx, a.ID); err != nil || got.SourceCount != 1 {
		t.Errorf("after detach = %+v, %v", got, err)
	}
	if _, err := db.GetSource(ctx, "one"); err != nil {
		t.Errorf("a detached source must still be readable: %v", err)
	}
}

func TestNotebookLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)

	n, err := db.CreateNotebook(ctx, "Dissertation")
	if err != nil {
		t.Fatal(err)
	}
	if n.ID == "" || n.CreatedAt == 0 {
		t.Fatalf("notebook = %+v", n)
	}
	if other, _ := newID(); other == n.ID {
		t.Fatal("ids collide")
	}

	got, err := db.GetNotebook(ctx, n.ID)
	if err != nil || got.Name != "Dissertation" {
		t.Fatalf("GetNotebook = %+v, %v", got, err)
	}

	if err := db.RenameNotebook(ctx, n.ID, "Chapter 3"); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListNotebooks(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "Chapter 3" {
		t.Fatalf("ListNotebooks = %+v, %v", list, err)
	}

	if err := db.DeleteNotebook(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetNotebook(ctx, n.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	for _, fn := range []func() error{
		func() error { return db.RenameNotebook(ctx, "nope", "x") },
		func() error { return db.DeleteNotebook(ctx, "nope") },
	} {
		if err := fn(); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	}
	if _, err := db.GetNotebook(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetNotebook err = %v, want ErrNotFound", err)
	}
}

// ── sources and dedupe ───────────────────────────────────────────

func TestAddSourceDedupesAcrossNotebooks(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	b := notebook(t, db, "B")

	first, added, err := db.AddSource(ctx, a, source("sha1", "paper.pdf"))
	if err != nil || !added {
		t.Fatalf("added = %v, err = %v", added, err)
	}
	if first.Status != domain.StatusProcessing {
		t.Fatalf("status = %q", first.Status)
	}

	// Same bytes, second notebook: one row, no re-ingest.
	second, added, err := db.AddSource(ctx, b, source("sha1", "paper.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("second insert should have been a no-op")
	}
	if second.OriginalPath != "/data/originals/sha1" {
		t.Errorf("original path = %q", second.OriginalPath)
	}
	if got := mustInt(t, db, `SELECT COUNT(*) FROM sources`); got != 1 {
		t.Fatalf("sources = %d, want 1", got)
	}
	if got := mustInt(t, db, `SELECT COUNT(*) FROM notebook_sources`); got != 2 {
		t.Fatalf("memberships = %d, want 2", got)
	}

	// Same file twice in one notebook is also harmless.
	if _, _, err := db.AddSource(ctx, a, source("sha1", "paper.pdf")); err != nil {
		t.Fatal(err)
	}
	if got := mustInt(t, db, `SELECT COUNT(*) FROM notebook_sources`); got != 2 {
		t.Fatalf("memberships = %d, want 2", got)
	}

	if list, _ := db.ListSources(ctx, a); len(list) != 1 {
		t.Errorf("notebook A has %d sources", len(list))
	}
	if list, _ := db.ListSources(ctx, b); len(list) != 1 {
		t.Errorf("notebook B has %d sources", len(list))
	}
	if ids, _ := db.SourceIDsForNotebook(ctx, a); len(ids) != 1 || ids[0] != "sha1" {
		t.Errorf("ids = %v", ids)
	}
}

func TestAddSourceKeepsExistingStatus(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	b := notebook(t, db, "B")

	if _, _, err := db.AddSource(ctx, a, source("sha1", "paper.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSourceStatus(ctx, "sha1", domain.StatusReady, "", nil); err != nil {
		t.Fatal(err)
	}

	got, added, err := db.AddSource(ctx, b, source("sha1", "paper.pdf"))
	if err != nil || added {
		t.Fatalf("added = %v err = %v", added, err)
	}
	if got.Status != domain.StatusReady {
		t.Fatalf("status = %q, want the existing ready", got.Status)
	}
}

func TestSetSourceStatusWarnings(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	if _, _, err := db.AddSource(ctx, a, source("sha1", "x.pdf")); err != nil {
		t.Fatal(err)
	}

	warnings := []string{"pdftoppm not found", "no transcript"}
	if err := db.SetSourceStatus(ctx, "sha1", domain.StatusReady, "", warnings); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetSource(ctx, "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 2 || got.Warnings[0] != warnings[0] {
		t.Fatalf("warnings = %q", got.Warnings)
	}

	if err := db.SetSourceStatus(ctx, "nope", domain.StatusReady, "", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if err := db.SetSourceStatus(ctx, "sha1", "nonsense", "", nil); err == nil {
		t.Error("expected a bad status to be refused")
	}
}

func TestDetachKeepsTheSource(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	b := notebook(t, db, "B")
	db.AddSource(ctx, a, source("sha1", "paper.pdf"))
	db.AddSource(ctx, b, source("sha1", "paper.pdf"))
	db.PutSegments(ctx, "sha1", []domain.Segment{{ID: "sha1:0000", SourceID: "sha1", DocID: "sha1", Kind: domain.KindText}})
	if err := db.PutSegments(ctx, "sha1", []domain.Segment{{
		ID: "bad", SourceID: "sha1", DocID: "sha1", Kind: domain.KindImage,
		Asset: &domain.Asset{ID: "never/inserted.png"},
	}}); err == nil {
		t.Error("a segment naming an asset that does not exist should be refused")
	}

	if err := db.DetachSource(ctx, a, "sha1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.ListSources(ctx, a); len(list) != 0 {
		t.Errorf("A still lists %d sources", len(list))
	}
	// The other notebook keeps everything, files included.
	if list, _ := db.ListSources(ctx, b); len(list) != 1 {
		t.Errorf("B has %d sources", len(list))
	}
	if segs, _ := db.ListSegments(ctx, "sha1"); len(segs) != 1 {
		t.Errorf("segments = %d, want them kept", len(segs))
	}
	if err := db.DetachSource(ctx, a, "sha1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second detach err = %v", err)
	}
}

func TestDeleteNotebookKeepsSourcesDropsChats(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	b := notebook(t, db, "B")
	db.AddSource(ctx, a, source("sha1", "paper.pdf"))
	db.AddSource(ctx, b, source("sha1", "paper.pdf"))

	if _, err := db.Raw().ExecContext(ctx,
		`INSERT INTO chats (id, notebook_id, title, created_at) VALUES ('c1', ?, 'chat', 1)`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Raw().ExecContext(ctx,
		`INSERT INTO messages (chat_id, role, content, created_at) VALUES ('c1', 'user', 'hi', 1)`); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteNotebook(ctx, a); err != nil {
		t.Fatal(err)
	}
	if got := mustInt(t, db, `SELECT COUNT(*) FROM sources WHERE id = 'sha1'`); got != 1 {
		t.Errorf("sources kept = %d, want 1", got)
	}
	if got := mustInt(t, db, `SELECT COUNT(*) FROM messages`); got != 0 {
		t.Errorf("messages = %d, want the cascade to have removed them", got)
	}
	if list, _ := db.ListSources(ctx, b); len(list) != 1 {
		t.Errorf("B lost its source: %d", len(list))
	}
}

func TestFailStaleSources(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	db.AddSource(ctx, a, source("busy", "a.pdf"))
	db.AddSource(ctx, a, source("done", "b.pdf"))
	db.SetSourceStatus(ctx, "done", domain.StatusReady, "", nil)

	n, err := db.FailStaleSources(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	got, _ := db.GetSource(ctx, "busy")
	if got.Status != domain.StatusFailed || got.Detail == "" {
		t.Fatalf("source = %+v", got)
	}
	// Idempotent: a ready source is never touched again.
	if n, _ := db.FailStaleSources(ctx); n != 0 {
		t.Errorf("second sweep = %d, want 0", n)
	}
}

func TestAttachSource(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	b := notebook(t, db, "B")
	db.AddSource(ctx, a, source("sha1", "paper.pdf"))

	if err := db.AttachSource(ctx, b, "sha1"); err != nil {
		t.Fatal(err)
	}
	if err := db.AttachSource(ctx, b, "sha1"); err != nil {
		t.Errorf("attaching twice should be harmless: %v", err)
	}
	if err := db.AttachSource(ctx, b, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if err := db.AttachSource(ctx, "missing-notebook", "sha1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// ── segments and assets ──────────────────────────────────────────

// mustNoErr keeps the one-line form readable without swallowing an error.
func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func pdfSegments(sourceID string) []domain.Segment {
	return []domain.Segment{
		{
			ID: sourceID + ":0000", SourceID: sourceID, DocID: sourceID, Kind: domain.KindText,
			Ordinal: 0, Text: "first chunk",
			Span: domain.Provenance{Page: 1, CharStart: 0, CharEnd: 11},
		},
		{
			ID: sourceID + ":0001", SourceID: sourceID, DocID: sourceID + "#pages", Kind: domain.KindImage,
			Ordinal: 0,
			Asset:   &domain.Asset{ID: sourceID + "/pages/page-0001.png", Path: "/data/assets/" + sourceID + "/pages/page-0001.png", MIME: "image/png", Bytes: 24000},
			Span:    domain.Provenance{Page: 1},
		},
	}
}

func TestPutSegmentsAndAssetsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	db.AddSource(ctx, a, source("sha1", "paper.pdf"))
	db.SetSourceStatus(ctx, "sha1", domain.StatusReady, "", nil)

	segs := pdfSegments("sha1")
	if err := db.PutAssets(ctx, "sha1", []domain.Asset{*segs[1].Asset}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSegments(ctx, "sha1", segs); err != nil {
		t.Fatal(err)
	}

	got, err := db.ListSegments(ctx, "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("segments = %d", len(got))
	}
	// Text document sorts before the "#pages" raster document.
	if got[0].Kind != domain.KindText || got[0].Text != "first chunk" {
		t.Errorf("segment 0 = %+v", got[0])
	}
	if got[0].Asset != nil {
		t.Errorf("text segment has an asset")
	}
	if got[1].Kind != domain.KindImage || got[1].Asset == nil || got[1].Asset.Bytes != 24000 {
		t.Errorf("segment 1 = %+v", got[1])
	}
	if got[1].Span.Page != 1 {
		t.Errorf("page = %d", got[1].Span.Page)
	}

	asset, err := db.GetAsset(ctx, "sha1/pages/page-0001.png")
	if err != nil || asset.MIME != "image/png" {
		t.Fatalf("asset = %+v, %v", asset, err)
	}
	if _, err := db.GetAsset(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}

	// The denormalised count the list view reads.
	if list, _ := db.ListSources(ctx, a); list[0].SegmentCount != 2 {
		t.Errorf("segment count = %d", list[0].SegmentCount)
	}
}

func TestPutSegmentsReplacesAndKeepsProvenance(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	if _, _, err := db.AddSource(ctx, a, source("sha1", "paper.pdf")); err != nil {
		t.Fatal(err)
	}
	mustNoErr(t, db.PutAssets(ctx, "sha1", []domain.Asset{*pdfSegments("sha1")[1].Asset}))
	mustNoErr(t, db.PutSegments(ctx, "sha1", pdfSegments("sha1")))
	if err := db.PutSegments(ctx, "sha1", []domain.Segment{{
		ID: "sha1:0000", SourceID: "sha1", DocID: "sha1", Kind: domain.KindText,
		Text: "regrown", Span: domain.Provenance{Page: 1, PageEnd: 2, CharStart: 900, CharEnd: 120},
	}}); err != nil {
		t.Fatal(err)
	}

	got, _ := db.ListSegments(ctx, "sha1")
	if len(got) != 1 {
		t.Fatalf("segments = %d, want the old set replaced", len(got))
	}
	if got[0].Text != "regrown" || got[0].Span.PageEnd != 2 || got[0].Span.CharEnd != 120 {
		t.Fatalf("segment = %+v", got[0])
	}
	// The asset row survives; only the segment reference went away with its row.
	if _, err := db.GetAsset(ctx, "sha1/pages/page-0001.png"); err != nil {
		t.Errorf("asset: %v", err)
	}
}

func TestSegmentsForNotebookIsScoped(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	b := notebook(t, db, "B")

	for _, id := range []string{"shared", "private"} {
		notebookID := a
		if id == "private" {
			notebookID = b
		}
		if _, _, err := db.AddSource(ctx, notebookID, source(id, id+".pdf")); err != nil {
			t.Fatal(err)
		}
		segs := pdfSegments(id)
		if err := db.PutAssets(ctx, id, []domain.Asset{*segs[1].Asset}); err != nil {
			t.Fatal(err)
		}
		if err := db.PutSegments(ctx, id, segs); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSourceStatus(ctx, id, domain.StatusReady, "", nil); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		notebook string
		want     int
	}{
		{a, 2},
		{b, 2},
		{"unknown", 0},
	} {
		segs, err := db.SegmentsForNotebook(ctx, tc.notebook)
		if err != nil {
			t.Fatal(err)
		}
		if len(segs) != tc.want {
			t.Errorf("notebook %s: %d segments, want %d", tc.notebook, len(segs), tc.want)
		}
		for _, s := range segs {
			if s.SourceID == "private" && tc.notebook == a {
				t.Errorf("notebook A can see a private segment: %+v", s)
			}
		}
	}

	// A source that is not ready must not be answerable from.
	if _, _, err := db.AddSource(ctx, a, source("pending", "p.pdf")); err != nil {
		t.Fatal(err)
	}
	db.PutSegments(ctx, "pending", pdfSegments("pending"))
	if segs, _ := db.SegmentsForNotebook(ctx, a); len(segs) != 2 {
		t.Errorf("processing source leaked into retrieval: %d segments", len(segs))
	}
}

func TestPutAssetsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	db.AddSource(ctx, a, source("sha1", "paper.pdf"))

	asset := domain.Asset{ID: "sha1/pages/page-0001.png", Path: "/a/b.png", MIME: "image/png", Bytes: 1}
	db.PutAssets(ctx, "sha1", []domain.Asset{asset})
	asset.Bytes = 2
	asset.Path = "/a/c.png"
	if err := db.PutAssets(ctx, "sha1", []domain.Asset{asset}); err != nil {
		t.Fatal(err)
	}
	if got := mustInt(t, db, `SELECT COUNT(*) FROM assets`); got != 1 {
		t.Errorf("assets = %d, want 1", got)
	}
	if got, _ := db.GetAsset(ctx, asset.ID); got.Path != "/a/c.png" || got.Bytes != 2 {
		t.Errorf("asset = %+v, want the update applied", got)
	}
	if err := db.PutAssets(ctx, "sha1", nil); err != nil {
		t.Errorf("empty put: %v", err)
	}
}
