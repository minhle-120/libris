package sqlite

import (
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"math"
	"path/filepath"
	"testing"

	"libris/internal/domain"
)

func TestSegmentsHaveVectorColumns(t *testing.T) {
	db := openDB(t)

	// Migration 0002 adds the model column; the vector columns were there from
	// the start and nullable, which is what let ingest work with no model.
	for _, want := range []string{"embedding", "dim", "model"} {
		var have int
		if err := db.sql.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM pragma_table_info('segments') WHERE name = ?`, want).Scan(&have); err != nil {
			t.Fatal(err)
		}
		if have != 1 {
			t.Errorf("segments has no %q column", want)
		}
	}
}

func TestPutEmbeddingsStoresVectorDimAndModel(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	db.AddSource(ctx, a, source("sha1", "paper.pdf"))
	segs := pdfSegments("sha1")
	if err := db.PutAssets(ctx, "sha1", []domain.Asset{*segs[1].Asset}); err != nil {
		t.Fatal(err) // the FK onto assets is enforced
	}
	if err := db.PutSegments(ctx, "sha1", segs); err != nil {
		t.Fatal(err)
	}

	vec := []float32{0.5, -0.25, 1.0 / 3.0}
	err := db.PutEmbeddings(ctx, []domain.Embedding{
		{SegmentID: "sha1:0000", Model: "embeddinggemma-2", Dim: len(vec), Vector: vec},
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		blob  []byte
		dim   int
		model string
	)
	if err := db.sql.QueryRowContext(ctx,
		`SELECT embedding, dim, model FROM segments WHERE id = 'sha1:0000'`).
		Scan(&blob, &dim, &model); err != nil {
		t.Fatal(err)
	}

	if dim != 3 || model != "embeddinggemma-2" {
		t.Errorf("dim = %d model = %q", dim, model)
	}
	if len(blob) != 4*len(vec) {
		t.Fatalf("blob is %d bytes, want %d", len(blob), 4*len(vec))
	}
	// Little-endian float32, which is the layout the cosine scan reads.
	for i, want := range vec {
		if got := math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:])); got != want {
			t.Errorf("component %d = %v, want %v", i, got, want)
		}
	}

	// The other segment keeps its NULL: partial coverage must stay visible.
	var missing any
	if err := db.sql.QueryRowContext(ctx,
		`SELECT embedding FROM segments WHERE id = 'sha1:0001'`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Error("a segment that was not embedded should still read as NULL")
	}
}

func TestPutEmbeddingsRejectsUnknownSegment(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	db.AddSource(ctx, a, source("sha1", "paper.pdf"))
	segs := pdfSegments("sha1")
	if err := db.PutAssets(ctx, "sha1", []domain.Asset{*segs[1].Asset}); err != nil {
		t.Fatal(err) // the FK onto assets is enforced
	}
	if err := db.PutSegments(ctx, "sha1", segs); err != nil {
		t.Fatal(err)
	}

	// Mid-job re-ingest is the case this catches: the vectors would describe
	// chunks that no longer exist.
	err := db.PutEmbeddings(ctx, []domain.Embedding{
		{SegmentID: "sha1:0000", Dim: 1, Vector: []float32{1}},
		{SegmentID: "sha1:0009", Dim: 1, Vector: []float32{1}},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	// And the write that did match must not have landed.
	var embedded int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM segments WHERE embedding IS NOT NULL`).Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != 0 {
		t.Errorf("%d vectors stored after a failed batch; the write is not atomic", embedded)
	}
}

func TestPutSegmentsDiscardsVectors(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	db.AddSource(ctx, a, source("sha1", "paper.pdf"))
	segs := pdfSegments("sha1")
	if err := db.PutAssets(ctx, "sha1", []domain.Asset{*segs[1].Asset}); err != nil {
		t.Fatal(err) // the FK onto assets is enforced
	}
	if err := db.PutSegments(ctx, "sha1", segs); err != nil {
		t.Fatal(err)
	}
	if err := db.PutEmbeddings(ctx, []domain.Embedding{
		{SegmentID: "sha1:0000", Model: "m", Dim: 1, Vector: []float32{1}},
	}); err != nil {
		t.Fatal(err)
	}

	// Re-ingesting replaces the rows, so the vectors go with them. Ingest embeds
	// again in the same job; nothing may read this source as searchable in between.
	if err := db.PutSegments(ctx, "sha1", segs); err != nil {
		t.Fatal(err)
	}
	var embedded int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM segments WHERE embedding IS NOT NULL`).Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if embedded != 0 {
		t.Errorf("%d vectors survived a replace; a stale vector must never be retrieved", embedded)
	}
}

func TestPutEmbeddingsEmptyIsNoop(t *testing.T) {
	db := openDB(t)
	if err := db.PutEmbeddings(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsApplyOnceEach(t *testing.T) {
	// Open takes a database file, and creates the directory above it.
	path := filepath.Join(t.TempDir(), "libris.db")

	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := mustInt(t, db, `SELECT COUNT(*) FROM schema_migrations`); got != len(files) {
			t.Fatalf("%d migrations after a reopen, want %d exactly once each", got, len(files))
		}
		db.Close()
	}
}

// embeddedWithModel counts a source's vectors that carry a given model identity.
func embeddedWithModel(t *testing.T, db *DB, sourceID, model string) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM segments WHERE source_id = ? AND model = ? AND embedding IS NOT NULL`,
		sourceID, model).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// twoEmbeddedSources writes two sources, each with a text chunk and a page raster,
// all four carrying a vector from the old space.
func twoEmbeddedSources(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	a := notebook(t, db, "A")

	for _, id := range []string{"sha1", "sha2"} {
		if _, _, err := db.AddSource(ctx, a, source(id, id+".pdf")); err != nil {
			t.Fatal(err)
		}
		segs := pdfSegments(id)
		var (
			assets []domain.Asset
			vecs   []domain.Embedding
		)
		for _, s := range segs {
			if s.Asset != nil {
				assets = append(assets, *s.Asset)
			}
			vecs = append(vecs, domain.Embedding{SegmentID: s.ID, Model: "old", Dim: 2, Vector: []float32{1, 0}})
		}
		if err := db.PutAssets(ctx, id, assets); err != nil {
			t.Fatal(err) // the FK onto assets is enforced
		}
		if err := db.PutSegments(ctx, id, segs); err != nil {
			t.Fatal(err)
		}
		if err := db.PutEmbeddings(ctx, vecs); err != nil {
			t.Fatal(err)
		}
	}
}

// Re-embedding one source has to leave that source in one vector space — including
// the document that holds its page rasters — and must not touch any other source.
func TestReplaceEmbeddingsSwapsOneSource(t *testing.T) {
	db := openDB(t)
	twoEmbeddedSources(t, db)

	err := db.ReplaceEmbeddings(context.Background(), "sha1", []domain.Embedding{
		{SegmentID: "sha1:0000", Model: "new+tp1", Dim: 4, Vector: []float32{0, 1, 0, 1}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if n := embeddedWithModel(t, db, "sha1", "old"); n != 0 {
		t.Errorf("%d chunks of a re-embedded source still answer to the old space", n)
	}
	if n := embeddedWithModel(t, db, "sha1", "new+tp1"); n != 1 {
		t.Errorf("%d new vectors, want the 1 supplied", n)
	}
	if n := embeddedWithModel(t, db, "sha2", "old"); n != 2 {
		t.Errorf("another source lost %d of its vectors", 2-n)
	}
}

// The swap clears before it writes, so a rejected write must take the clear with
// it. Losing a source's vectors because one id was wrong is worse than not having
// swapped them at all.
func TestReplaceEmbeddingsRollsBackAcrossSources(t *testing.T) {
	db := openDB(t)
	twoEmbeddedSources(t, db)

	err := db.ReplaceEmbeddings(context.Background(), "sha1", []domain.Embedding{
		{SegmentID: "sha1:0000", Model: "new", Dim: 2, Vector: []float32{1, 0}},
		// A chunk that exists — but belongs to sha2. Writing it would put sha2's
		// segment in sha1's vector space, which nothing downstream can see.
		{SegmentID: "sha2:0000", Model: "new", Dim: 2, Vector: []float32{1, 0}},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReplaceEmbeddings = %v, want ErrNotFound", err)
	}

	if n := embeddedWithModel(t, db, "sha1", "old"); n != 2 {
		t.Errorf("a failed swap left %d of 2 vectors standing", n)
	}
	if n := embeddedWithModel(t, db, "sha1", "new"); n != 0 {
		t.Errorf("%d vectors from the rejected swap were written", n)
	}
}
