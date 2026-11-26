package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"testing"

	"libris/internal/domain"
)

// The tests in this file exist for one claim: splitting a scan across shards is a
// speed change and changes nothing a caller can see — not the set of hits, not
// their order, not which of two equal-scoring chunks survives the cut. Everything
// here is built to make that claim fail loudly if it ever stops being true.

// scanModel is the embedding identity the corpora below are written under.
const scanModel = "m+tp1"

// scanDirs are the only three vectors the big corpus uses, repeated until the
// corpus is the size it needs to be.
//
// Repetition is the point. Identical blobs score identically, so cosine ties are
// the common case here rather than a corner nobody visits: with the query below,
// every scanDir[0] chunk scores exactly 1, and there are far more of them than the
// limit. Which 40 of them come back is decided entirely by the tie-break — the
// doc_id, ordinal, rowid order every shard sorts by — so a shard that lost a term
// of it, or a merge that divided the limit, shows up as a different hit list.
var scanDirs = [][]float32{{1, 0, 0, 0}, {1, 1, 0, 0}, {0, 1, 0, 0}}

// seedScanCorpus writes docsPerDoc chunks across nDocs sources, all ready, all
// embedded under scanModel.
//
// Docs are inserted in an order that is neither their id order nor anything a scan
// sorts by, so rowid order and doc_id order disagree across the corpus. Ordinals
// repeat inside a document, so a document holds several chunks with the same
// doc_id, the same ordinal and the same score — distinguishable only by rowid,
// which is the last term of the order and the one most likely to be dropped.
func seedScanCorpus(t *testing.T, db *DB, notebookID, model string, nDocs, perDoc int) int {
	t.Helper()
	ctx := context.Background()

	written := 0
	for x := range nDocs {
		d := (x * 7) % nDocs
		id := fmt.Sprintf("doc-%02d", d)

		if _, _, err := db.AddSource(ctx, notebookID, source(id, id+".pdf")); err != nil {
			t.Fatal(err)
		}

		segs := make([]domain.Segment, 0, perDoc)
		embs := make([]domain.Embedding, 0, perDoc)
		for o := range perDoc {
			sid := fmt.Sprintf("%s:%04d", id, o)
			segs = append(segs, domain.Segment{
				ID: sid, SourceID: id, DocID: id, Kind: domain.KindText,
				Ordinal: o % 40,
				Text:    fmt.Sprintf("body of %s ordinal %d", sid, o%40),
				Span:    domain.Provenance{Page: 1, CharStart: o, CharEnd: o + 1},
			})
			embs = append(embs, domain.Embedding{
				SegmentID: sid, Model: model, Dim: 4,
				Vector: scanDirs[(o+d)%len(scanDirs)],
			})
		}

		if err := db.PutSegments(ctx, id, segs); err != nil {
			t.Fatal(err)
		}
		if err := db.PutEmbeddings(ctx, embs); err != nil {
			t.Fatal(err)
		}
		if err := db.SetSourceStatus(ctx, id, domain.StatusReady, "", nil); err != nil {
			t.Fatal(err)
		}
		written += len(segs)
	}
	return written
}

func hitsEqual(want, got []domain.Hit) bool {
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		a, b := want[i], got[i]
		if a.Segment.ID != b.Segment.ID || a.Score != b.Score ||
			a.Segment.Text != b.Segment.Text || a.Segment.Ordinal != b.Segment.Ordinal ||
			a.SourceName != b.SourceName {
			return false
		}
	}
	return true
}

// TestScanShardsReturnTheSameHitsAsOneScan is the whole no-quality-drop claim in
// one test: the same search, run one-shard and run across two to eight, must give
// the identical list. Identical including order and text, because the list's order
// is what dedupe and the chunk budget cut on.
func TestScanShardsReturnTheSameHitsAsOneScan(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	nb := notebook(t, db, "A")

	const (
		docs   = 8
		perDoc = 600
		limit  = 40
	)
	rows := seedScanCorpus(t, db, nb, scanModel, docs, perDoc)

	// A shard count high enough to be worth comparing needs a corpus per shard far
	// below the 4096 the real scan waits for; see DB.rowsPerShard.
	db.rowsPerShard = 300

	// The corpus has to be big enough that the cut actually falls inside a run of
	// ties, or this test passes for corpora too small to split.
	if rows <= limit*len(scanDirs) {
		t.Fatalf("corpus of %d rows is too small to put the limit inside a tie band", rows)
	}

	db.shards = 1
	want, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0, 0}, scanModel, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != limit {
		t.Fatalf("%d hits, want %d", len(want), limit)
	}
	if want[0].Score != 1 {
		t.Fatalf("top score %v, want 1: the tie band never reached the limit", want[0].Score)
	}

	for _, shards := range []int{2, 3, 5, 8} {
		db.shards = shards
		if plan := db.scanPlan(rows); plan < 2 {
			t.Fatalf("shards %d: plan is %d, so the sharded path never ran", shards, plan)
			return
		}
		got, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0, 0}, scanModel, limit)
		if err != nil {
			t.Fatalf("shards %d: %v", shards, err)
			return
		}
		if !hitsEqual(want, got) {
			t.Fatalf("shards %d changed the result.\n one scan: %s\n  sharded: %s",
				shards, scoredIDs(want), scoredIDs(got))
			return
		}
	}
}

// scoredIDs names a result compactly: which chunk, at which score.
func scoredIDs(hits []domain.Hit) string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, fmt.Sprintf("%s/%g", h.Segment.ID, h.Score))
	}
	return fmt.Sprint(out)
}

// legacyScan is the scan as it was before any of this: one query that carried the
// text of every candidate in the notebook, ordered by (doc_id, ordinal), keeping a
// top limit. It is kept here, in a test, so the claim it is equivalent to stays
// checkable rather than argued.
//
// The old loop kept its top limit by inserting each candidate into a sorted list
// and only passing a candidate it was strictly better than — which is the same list
// a stable sort by score gives over rows in arrival order. That restatement is what
// is written down here, because it is the same result and readable at a glance.
func legacyScan(t testing.TB, db *DB, notebookID string, vector []float32, model string, limit int) []domain.Hit {
	t.Helper()

	rows, err := db.sql.QueryContext(context.Background(), `
		SELECT g.id, g.source_id, g.doc_id, g.kind, g.ordinal, g.text,
		       g.page, g.page_end, g.char_start, g.char_end,
		       a.id, a.path, a.mime, a.bytes,
		       g.embedding, g.dim, g.model, s.name
		  FROM segments g
		  JOIN sources s          ON s.id = g.source_id
		  JOIN notebook_sources m ON m.source_id = s.id
		  LEFT JOIN assets a      ON a.id = g.asset_id
		 WHERE m.notebook_id = ? AND s.status = 'ready'
		   AND g.embedding IS NOT NULL AND g.model = ? AND g.dim = ?
		 ORDER BY g.doc_id, g.ordinal`, notebookID, model, len(vector))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	qn := norm(vector)
	var out []domain.Hit
	for rows.Next() {
		var (
			hit     domain.Hit
			kind    string
			assetID sql.NullString
			path    sql.NullString
			mime    sql.NullString
			size    sql.NullInt64
			blob    []byte
			dim     int
			mdl     string
		)
		if err := rows.Scan(&hit.Segment.ID, &hit.Segment.SourceID, &hit.Segment.DocID, &kind,
			&hit.Segment.Ordinal, &hit.Segment.Text,
			&hit.Segment.Span.Page, &hit.Segment.Span.PageEnd,
			&hit.Segment.Span.CharStart, &hit.Segment.Span.CharEnd,
			&assetID, &path, &mime, &size,
			&blob, &dim, &mdl, &hit.SourceName); err != nil {
			t.Fatal(err)
		}
		if len(blob) != 4*dim {
			t.Fatalf("segment %s declares dim %d but holds %d bytes", hit.Segment.ID, dim, len(blob))
		}
		vec := make([]float32, dim)
		for i := range vec {
			vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
		}
		hit.Score = cosine(vector, qn, vec)
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// TestScanStillFindsWhatTheOldScanFound compares the new search against that old
// query on a corpus whose ties are everywhere, with the scan split across shards.
func TestScanStillFindsWhatTheOldScanFound(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	nb := notebook(t, db, "A")

	const (
		docs   = 6
		perDoc = 400
		limit  = 25
	)
	rows := seedScanCorpus(t, db, nb, scanModel, docs, perDoc)
	db.rowsPerShard = 200

	want := legacyScan(t, db, nb, []float32{0, 1, 0, 0}, scanModel, limit)
	if len(want) != limit {
		t.Fatalf("the old scan returned %d, want %d", len(want), limit)
	}

	for _, shards := range []int{1, 2, 4} {
		db.shards = shards
		if plan := db.scanPlan(rows); plan < 2 && shards > 1 {
			t.Fatalf("shards %d: plan is %d, so the sharded path never ran", shards, plan)
			return
		}
		got, err := db.SearchSegments(ctx, nb, []float32{0, 1, 0, 0}, scanModel, limit)
		if err != nil {
			t.Fatalf("shards %d: %v", shards, err)
			return
		}
		if !hitsEqual(want, got) {
			t.Fatalf("shards %d: the new scan found something else.\n old: %s\n new: %s",
				shards, scoredIDs(want), scoredIDs(got))
			return
		}
	}
}

func TestScanPlanArithmetic(t *testing.T) {
	db := &DB{shards: 8, rowsPerShard: 4096}
	for _, tc := range []struct {
		candidates int
		want       int
	}{
		{0, 1},    // nothing to scan is not a fan-out
		{1, 1},    // one row is not worth a connection
		{4095, 1}, // under one shard's worth, still one
		{4096, 1}, // exactly one shard's worth
		{4097, 2}, // the first split
		{8192, 2},
		{12288, 3},
		{12289, 4},
		{100000, 8}, // the machine caps it long before the corpus does
	} {
		if got := db.scanPlan(tc.candidates); got != tc.want {
			t.Errorf("scanPlan(%d) = %d, want %d", tc.candidates, got, tc.want)
		}
	}

	db.shards = 1
	if got := db.scanPlan(1_000_000); got != 1 {
		t.Errorf("a single-core machine planned %d shards for a million rows", got)
	}
}

func TestSearchShardsFitTheMachine(t *testing.T) {
	n := searchShards()
	if n < 1 {
		t.Fatalf("searchShards() = %d, want at least 1", n)
	}
	if cpus := runtime.NumCPU(); n > cpus {
		t.Errorf("searchShards() = %d, more than the %d CPUs this process has", n, cpus)
	}
	if n > searchShardCap {
		t.Errorf("searchShards() = %d, above the cap %d", n, searchShardCap)
	}
}

// TestScanShardsReportTheRealError puts one row with a short vector in the corpus.
// Its shard has to surface that as a corrupt-blob error, not as the cancellation
// its siblings stopped on — otherwise a sharded scan would hide the one failure
// worth reading about.
func TestScanShardsReportTheRealError(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	nb := notebook(t, db, "A")
	seedScanCorpus(t, db, nb, scanModel, 4, 300)
	db.rowsPerShard = 100
	db.shards = 4

	// dim stays correct so the row is still a candidate; the blob is too short to
	// fill it, which is the mismatch scanScored refuses.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE segments SET embedding = X'010203', dim = 4 WHERE id = 'doc-03:0007'`); err != nil {
		t.Fatal(err)
	}

	_, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0, 0}, scanModel, 40)
	if err == nil {
		t.Fatal("a short vector passed the scan")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("the scan reported the cancellation instead of the cause: %v", err)
	}
	if want := "declares dim 4 but holds 3 bytes"; !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want it to say %q", err, want)
	}
}

// TestScanShardsStopOnCancel checks the fan-out honours a cancelled question: every
// shard stops, and the caller gets the cancellation back rather than a partial
// answer it never asked for.
func TestScanShardsStopOnCancel(t *testing.T) {
	db := openDB(t)
	nb := notebook(t, db, "A")
	seedScanCorpus(t, db, nb, scanModel, 4, 300)
	db.rowsPerShard = 100
	db.shards = 4

	cancelled, stop := context.WithCancel(context.Background())
	stop()

	_, err := db.SearchSegments(cancelled, nb, []float32{1, 0, 0, 0}, scanModel, 40)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context canceled", err)
	}
}

// TestFillMissingDropsAVanishedSegment covers the race the text refetch can lose:
// the scan saw a chunk, the ingest deleted it before its text was read. A chunk
// that no longer exists must not be handed to a model as a citation.
func TestFillMissingDropsAVanishedSegment(t *testing.T) {
	db := openDB(t)
	nb := notebook(t, db, "A")
	seedScanCorpus(t, db, nb, scanModel, 2, 10)

	hits := []domain.Hit{
		{Segment: domain.Segment{ID: "doc-00:0000"}},
		{Segment: domain.Segment{ID: "no-longer-here"}},
	}
	got, err := db.fillMissing(context.Background(), hits)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Segment.ID != "doc-00:0000" {
		t.Fatalf("got %s, want just doc-00:0000", hitIDs(got))
	}
	if got[0].Segment.Text == "" {
		t.Fatal("the survivor came back with no text")
	}
}

// TestSearchSegmentsReadsText proves the scan's SELECT no longer carrying text did
// not turn into hits without text — the one way the split could reach a caller.
func TestSearchSegmentsReadsText(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	nb := notebook(t, db, "A")
	seedScanCorpus(t, db, nb, scanModel, 3, 500)
	db.rowsPerShard = 200
	db.shards = 4

	hits, err := db.SearchSegments(ctx, nb, []float32{1, 0, 0, 0}, scanModel, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 30 {
		t.Fatalf("%d hits, want 30", len(hits))
	}
	for i, h := range hits {
		if h.Segment.Text == "" {
			t.Fatalf("hit %d (%s) has no text", i, h.Segment.ID)
			return
		}
		if i > 0 && h.Segment.ID != hits[i-1].Segment.ID && h.Segment.Text == hits[i-1].Segment.Text {
			t.Fatalf("hits %d and %d are different chunks with the same text", i-1, i)
			return
		}
	}
}
