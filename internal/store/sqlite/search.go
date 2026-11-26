package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"

	"libris/internal/domain"
)

// segmentSearchSelect is the scan's join: the vector itself and the identity of
// the space it belongs to, which is all a score needs. It is not shared with the
// listing queries for the reason they give for not reading vectors at all — a
// vector per row is dead weight in a query that will not score it.
//
// Text is absent on purpose. Ranking a notebook means looking at every chunk in
// it and keeping a few dozen, so a text column here is every discarded chunk
// paid for twice: once through SQLite, once through the driver. fillMissing reads
// the survivors by primary key afterwards.
//
// rowid is present for the same reason the ORDER BY names it: it is the last term
// of the order every shard sorts by, so the merge below can reproduce the order a
// single-threaded scan would have produced.
const segmentSearchSelect = `
	SELECT g.id, g.source_id, g.doc_id, g.kind, g.ordinal,
	       g.page, g.page_end, g.char_start, g.char_end, g.t0, g.t1,
	       a.id, a.path, a.mime, a.bytes,
	       g.embedding, g.dim, g.model, s.name, g.rowid
	  FROM segments g
	  JOIN sources s          ON s.id = g.source_id
	  JOIN notebook_sources m ON m.source_id = s.id
	  LEFT JOIN assets a      ON a.id = g.asset_id`

// segmentSearchFilter is what makes a row a candidate at all, and the scan and the
// count below share it so the two can never disagree about the corpus.
//
// Membership (only sources this notebook holds) and `status = 'ready'` (a
// half-ingested source must not be citable) matter more than speed, which is why
// brute force is affordable here at all.
//
// The vectors are filtered to one `model` identity and one `dim`, which is what
// makes the comparison legitimate. A source re-embedded by another model, or by
// another prompt scheme, scores in a different space; averaging those scores
// together would look like relevance and mean nothing. Stale rows are therefore
// invisible to a search rather than wrong in it, and the re-ingest action is what
// brings them back.
const segmentSearchFilter = ` WHERE m.notebook_id = ? AND s.status = 'ready'
	   AND g.embedding IS NOT NULL AND g.model = ? AND g.dim = ?`

// segmentCountSelect is segmentSearchFilter's corpus measured, without the
// columns the scan wants. It decides how many goroutines the scan is worth
// splitting across; see scanPlan.
const segmentCountSelect = `
	SELECT count(*)
	  FROM segments g
	  JOIN sources s          ON s.id = g.source_id
	  JOIN notebook_sources m ON m.source_id = s.id` + segmentSearchFilter

// segmentOrder is the canonical order of the corpus. A cosine tie is broken by
// where the chunk sits in its document, then by physical row order, which is the
// order SQLite itself returned rows in before this file named it. Naming it is
// what lets a sharded scan claim the same result as an unsplit one: each shard
// sorts by it, so merging the shards sorts by it too.
const segmentOrder = " ORDER BY g.doc_id, g.ordinal, g.rowid"

// scanRowsPerShard is the corpus size a shard needs before splitting is worth it.
//
// Measured, on 1200 chunks of 768 dimensions: one shard 13.4 ms, two 7.5 ms, four
// 7.3 ms, eight 6.8 ms — so 300 chunks is enough for a shard to pay for its own
// connection and goroutine, and the number below keeps a margin over that rather
// than sitting on the edge of it. Below 150 chunks per shard the split was not
// measured, which is why this is not lower still. BenchmarkScanSplitThreshold is
// the measurement; the bigger one at 33k chunks is where 8 shards came from.
const scanRowsPerShard = 512

// scored is one candidate plus the tiebreaker the merge needs. The rowid is not
// part of domain.Hit — nothing outside this file should care which physical row a
// chunk was — but the merge cannot reproduce a single-threaded order without it.
type scored struct {
	hit   domain.Hit
	rowid int64
}

// SearchSegments returns the closest segments in one notebook to a query vector,
// best first, at most limit of them.
//
// It is a brute-force scan, deliberately. At 10⁴–10⁵ segments a pass over the
// corpus with a bounded result set beats keeping a second index in sync with every
// ingest. Swapping in an ANN index later is a change to this function's body, not
// to its signature — but note what that trade would actually cost: an ANN returns
// approximate neighbours, and there is no reranker downstream to buy the recall
// back (see retrieve.go on the same point), so the ranking this file produces is
// the ranking the model is shown.
//
// Scores are cosine, computed rather than assumed. The embedding server normalises
// by default, which makes the dot product enough, but that is a command-line flag
// and a dot product of unnormalised vectors silently prefers long passages.
//
// The scan may run split across shards, which is a speed change and is meant to be
// nothing else. What makes it exactly equivalent, and what a change here has to
// preserve:
//
//   - Shards partition by `rowid % shards`, so every row belongs to exactly one
//     shard: the union of the shards is the corpus, no row scored twice, none lost.
//     A row can only move between shards if its rowid changes underneath a scan.
//     Nothing here does that — rowids change when a row is rewritten, and this
//     store inserts, updates columns and deletes, but never a row's rowid, and
//     never runs VACUUM.
//   - Every shard keeps a full `limit`, not limit/shards. A candidate that belongs
//     in the global top limit is in at least one shard's top limit; dividing the
//     limit is how you lose one that no single shard ranked high. This is the one
//     way sharding could cost recall, so it is the rule that must not slip.
//   - Each shard's list is sorted by segmentOrder, and mergeTop merges by the same
//     comparator, so the output is the list a single-threaded scan would have
//     produced — including which of two equal-scoring chunks wins. Downstream,
//     dedupe and the chunk budget both cut by list order, so a tie that flipped
//     sides would change what the model is shown without changing any score.
func (db *DB) SearchSegments(ctx context.Context, notebookID string, vector []float32, model string, limit int) ([]domain.Hit, error) {
	if limit <= 0 || len(vector) == 0 {
		return nil, fmt.Errorf("store: search wants a positive limit and a non-empty vector (limit %d, dim %d)",
			limit, len(vector))
	}

	// The query's own norm is needed once, not per row.
	qn := norm(vector)
	if qn == 0 {
		return nil, fmt.Errorf("store: search vector has zero length")
	}

	shards := 1
	if db.shards > 1 {
		// The count is only worth a query when there is something to decide. A
		// single-core machine gets one shard whatever the corpus holds.
		var candidates int
		if err := db.sql.QueryRowContext(ctx, segmentCountSelect, notebookID, model, len(vector)).Scan(&candidates); err != nil {
			return nil, fmt.Errorf("store: count segments: %w", err)
		}
		if candidates == 0 {
			return nil, nil
		}
		shards = db.scanPlan(candidates)
	}

	var parts [][]scored
	if shards == 1 {
		part, err := db.scanShard(ctx, notebookID, model, vector, qn, limit, 1, 0)
		if err != nil {
			return nil, err
		}
		parts = [][]scored{part}
	} else {
		var err error
		if parts, err = db.scanShards(ctx, notebookID, model, vector, qn, limit, shards); err != nil {
			return nil, err
		}
	}

	hits, err := db.fillMissing(ctx, mergeTop(parts, limit))
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// searchShards is how many goroutines one scan may split across, and therefore
// how many SQLite connections the pool must hold.
//
// It comes from the machine at Open and is not a setting. A scan's cost is a pass
// over a corpus on the same cores the model server is trying to keep busy, so a
// number anyone wrote down would be wrong on the next machine. NumCPU is the CPUs
// this process is allowed to run on — the affinity mask the OS hands it, which is
// what a cpuset or a taskset changes — and GOMAXPROCS caps it again when the
// runtime itself is confined below what the machine has.
//
// The cap under the CPU count is SQLite's, not the scan's: every shard holds a
// connection, and a connection is its own copy of the library with its own page
// cache. It is also where the scan stops answering: 33k chunks measured 157 ms at
// four shards, 145 ms at six and eight, and back up to 150 ms at twelve — the scan
// is out of CPU by then and further fan-out only adds connections. Eight is inside
// that plateau without standing at its edge.
func searchShards() int {
	n := runtime.NumCPU()
	if p := runtime.GOMAXPROCS(0); p < n {
		n = p
	}
	if n < 1 {
		n = 1
	}
	return min(n, searchShardCap)
}

// searchShardCap is the most fan-out one scan is allowed, whatever the machine has.
const searchShardCap = 8

// scanPlan is how many goroutines a corpus of candidates is worth splitting
// across: at most what this machine can run (db.shards, from Open), and at most
// what the corpus can keep busy (db.rowsPerShard rows each).
func (db *DB) scanPlan(candidates int) int {
	n := (candidates + db.rowsPerShard - 1) / db.rowsPerShard
	if n > db.shards {
		n = db.shards
	}
	if n < 1 {
		n = 1
	}
	return n
}

// scanShard scores one slice of the corpus and keeps the best limit of them.
//
// shards < 2 means the whole corpus; otherwise this shard takes the rows whose
// rowid is congruent to shard modulo shards.
func (db *DB) scanShard(ctx context.Context, notebookID, model string, vector []float32, qn float64, limit, shards, shard int) ([]scored, error) {
	query := segmentSearchSelect + segmentSearchFilter
	args := []any{notebookID, model, len(vector)}
	if shards > 1 {
		query += " AND g.rowid % ? = ?"
		args = append(args, shards, shard)
	}
	query += segmentOrder

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: search segments: %w", err)
	}
	defer rows.Close()

	var (
		out   []scored
		scans int
	)
	for rows.Next() {
		// A cancelled question should stop scanning, not finish the corpus first.
		if scans&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		scans++

		cand, err := scanScored(rows, vector, qn)
		if err != nil {
			return nil, err
		}
		out = insertTop(out, cand, limit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan segments: %w", err)
	}
	return out, nil
}

// scanShards runs the shards concurrently, each on its own connection.
//
// The shards read one snapshot each, not one shared one: SQLite fixes a reader on
// the state as of its own start, and holding a transaction open across the fan-out
// is what it would cost to share a snapshot instead.
//
// So an ingest committing mid-search moves the corpus between shards. A chunk
// inserted after some shard has passed where it would belong is invisible to that
// shard and visible to the ones that had not started; a chunk deleted before a
// shard reached it is invisible to that shard. Either way the search sees a corpus
// that existed at some instant during the scan rather than one instant, which for a
// ranking over chunks being added one source at a time is not a wrong answer. What
// does have to hold — a chunk not ready is not citable — is enforced inside every
// shard's own WHERE, not once at the top.
func (db *DB) scanShards(ctx context.Context, notebookID, model string, vector []float32, qn float64, limit, shards int) ([][]scored, error) {
	// Cancelling on the first failure keeps a shard that hit a corrupt blob from
	// scanning the rest of the corpus while its siblings wait.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Each shard owns its result and hands it over on the channel; only this
	// goroutine writes the slots below, so there is nothing to lock. The buffer is
	// one per shard, so a shard that finishes early never waits for a reader — it
	// can return, and return its connection to the pool, immediately.
	type result struct {
		shard  int
		caught []scored
		err    error
	}
	ch := make(chan result, shards)

	var wg sync.WaitGroup
	for i := range shards {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := db.scanShard(ctx, notebookID, model, vector, qn, limit, shards, i)
			ch <- result{shard: i, caught: got, err: err}
		}()
	}
	wg.Wait()
	close(ch)

	parts := make([][]scored, shards)
	var fail error
	for r := range ch {
		parts[r.shard] = r.caught
		if r.err == nil {
			continue
		}
		// A shard stopped by its sibling's cancellation is not the news; the
		// cancellation's cause is. If the caller cancelled, every error is a
		// cancellation and the first of them is the right one to return.
		if fail == nil || (errors.Is(fail, context.Canceled) && !errors.Is(r.err, context.Canceled)) {
			fail = r.err
		}
	}
	if fail != nil {
		return nil, fail
	}
	return parts, nil
}

// mergeTop merges shard lists — each already best-first, ties in segmentOrder —
// into one best-first list of at most limit hits.
//
// Merging sorted lists by the same comparator every list was built with is what
// makes the result the same list a single scan would have written, ties included.
// It costs limit × shards comparisons.
func mergeTop(parts [][]scored, limit int) []domain.Hit {
	pos := make([]int, len(parts))
	out := make([]domain.Hit, 0, limit)
	for len(out) < limit {
		best := -1
		for i := range parts {
			if pos[i] >= len(parts[i]) {
				continue
			}
			if best < 0 || better(parts[i][pos[i]], parts[best][pos[best]]) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		out = append(out, parts[best][pos[best]].hit)
		pos[best]++
	}
	return out
}

// better is segmentOrder applied to scores: cosine descending, then the order
// rows arrived in. It is the whole reason rowid is in the scan's SELECT.
func better(a, b scored) bool {
	if a.hit.Score != b.hit.Score {
		return a.hit.Score > b.hit.Score
	}
	if a.hit.Segment.DocID != b.hit.Segment.DocID {
		return a.hit.Segment.DocID < b.hit.Segment.DocID
	}
	if a.hit.Segment.Ordinal != b.hit.Segment.Ordinal {
		return a.hit.Segment.Ordinal < b.hit.Segment.Ordinal
	}
	return a.rowid < b.rowid
}

// fillMissing reads the text of the survivors, by primary key.
//
// A hit whose row is gone is dropped rather than returned textless. The scan and
// this read are separate snapshots, so a source deleted while a question was in
// flight can win the race between them, and a chunk that no longer exists must not
// be citable — the same rule `status = 'ready'` enforces at the scan.
func (db *DB) fillMissing(ctx context.Context, hits []domain.Hit) ([]domain.Hit, error) {
	if len(hits) == 0 {
		return hits, nil
	}

	// Chunked because the limit is the caller's to choose, and SQLite caps how many
	// parameters one statement may carry.
	const chunk = 200
	text := make(map[string]string, len(hits))
	for start := 0; start < len(hits); start += chunk {
		end := min(start+chunk, len(hits))
		batch := hits[start:end]

		args := make([]any, len(batch))
		for i, h := range batch {
			args[i] = h.Segment.ID
		}
		query := "SELECT id, text FROM segments WHERE id IN (" +
			strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + ")"

		rows, err := db.sql.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("store: read segment text: %w", err)
		}
		for rows.Next() {
			var id, body string
			if err := rows.Scan(&id, &body); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: read segment text: %w", err)
			}
			text[id] = body
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: read segment text: %w", err)
		}
		rows.Close()
	}

	out := hits[:0]
	for _, h := range hits {
		body, ok := text[h.Segment.ID]
		if !ok {
			continue
		}
		h.Segment.Text = body
		out = append(out, h)
	}
	return out, nil
}

// scanScored reads one row and scores it. The blob's declared dim and its actual
// length must agree, or a truncated write would produce a short dot product that
// still computes and still ranks — the worst kind of wrong.
func scanScored(rows *sql.Rows, vector []float32, qn float64) (scored, error) {
	var (
		cand    scored
		kind    string
		assetID sql.NullString
		path    sql.NullString
		mime    sql.NullString
		size    sql.NullInt64
		blob    []byte
		dim     int
		model   string
		source  string
	)
	if err := rows.Scan(&cand.hit.Segment.ID, &cand.hit.Segment.SourceID, &cand.hit.Segment.DocID, &kind,
		&cand.hit.Segment.Ordinal,
		&cand.hit.Segment.Span.Page, &cand.hit.Segment.Span.PageEnd,
		&cand.hit.Segment.Span.CharStart, &cand.hit.Segment.Span.CharEnd,
		&cand.hit.Segment.Span.T0, &cand.hit.Segment.Span.T1,
		&assetID, &path, &mime, &size,
		&blob, &dim, &model, &source, &cand.rowid); err != nil {
		return scored{}, err
	}

	cand.hit.Segment.Kind = domain.Kind(kind)
	cand.hit.SourceName = source
	if assetID.Valid {
		cand.hit.Segment.Asset = &domain.Asset{
			ID:    assetID.String,
			Path:  path.String,
			MIME:  mime.String,
			Bytes: size.Int64,
		}
	}

	if len(blob) != 4*dim {
		return scored{}, fmt.Errorf("store: segment %s declares dim %d but holds %d bytes",
			cand.hit.Segment.ID, dim, len(blob))
	}
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	cand.hit.Score = cosine(vector, qn, vec)
	return cand, nil
}

// insertTop keeps out sorted best-first and no longer than limit, with ties held
// in the order the rows arrived — a new candidate only passes one it is strictly
// better than. That arrival order is segmentOrder, which is what makes the list
// each shard hands mergeTop already in the canonical order.
//
// The list is at most the caller's recall pool long — a few times the chunk budget
// — so a linear insert is cheaper than a heap and much easier to read.
func insertTop(out []scored, cand scored, limit int) []scored {
	if len(out) == limit && !better(cand, out[len(out)-1]) {
		return out
	}
	if len(out) < limit {
		out = append(out, cand)
	} else {
		out[len(out)-1] = cand
	}
	for i := len(out) - 1; i > 0 && better(out[i], out[i-1]); i-- {
		out[i], out[i-1] = out[i-1], out[i]
	}
	return out
}

func cosine(a []float32, an float64, b []float32) float64 {
	var dot, bn float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		bn += float64(b[i]) * float64(b[i])
	}
	if bn == 0 {
		return 0
	}
	return dot / (an * math.Sqrt(bn))
}

func norm(v []float32) float64 {
	var n float64
	for _, f := range v {
		n += float64(f) * float64(f)
	}
	return math.Sqrt(n)
}
