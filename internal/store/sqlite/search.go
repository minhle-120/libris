package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"

	"libris/internal/domain"
)

// segmentSearchSelect is the reading-order join with the three columns only a scan
// wants: the vector itself, and the identity of the space it belongs to. It is not
// shared with the listing queries for the reason they give for not reading vectors
// at all — a vector per row is dead weight in a query that will not score it.
const segmentSearchSelect = `
	SELECT g.id, g.source_id, g.doc_id, g.kind, g.ordinal, g.text,
	       g.page, g.page_end, g.char_start, g.char_end,
	       a.id, a.path, a.mime, a.bytes,
	       g.embedding, g.dim, g.model, s.name
	  FROM segments g
	  JOIN sources s          ON s.id = g.source_id
	  JOIN notebook_sources m ON m.source_id = s.id
	  LEFT JOIN assets a      ON a.id = g.asset_id`

// SearchSegments returns the closest segments in one notebook to a query vector,
// best first, at most limit of them.
//
// It is a brute-force scan, deliberately. At 10⁴–10⁵ segments a single pass with a
// bounded result set beats keeping a second index in sync with every ingest, and
// the row set is already filtered by two things that matter more than speed:
// membership (only sources this notebook holds) and `status = 'ready'` (a
// half-ingested source must not be citable). Swapping in sqlite-vec or an ANN
// index later is a change to this function's body, not to its signature.
//
// The vectors are filtered to one `model` identity and one `dim`, which is what
// makes the comparison legitimate. A source re-embedded by another model, or by
// another prompt scheme, scores in a different space; averaging those scores
// together would look like relevance and mean nothing. Stale rows are therefore
// invisible to a search rather than wrong in it, and the re-ingest action is what
// brings them back.
//
// Scores are cosine, computed rather than assumed. The embedding server normalises
// by default, which makes the dot product enough, but that is a command-line flag
// and a dot product of unnormalised vectors silently prefers long passages.
func (db *DB) SearchSegments(ctx context.Context, notebookID string, vector []float32, model string, limit int) ([]domain.Hit, error) {
	if limit <= 0 || len(vector) == 0 {
		return nil, fmt.Errorf("store: search wants a positive limit and a non-empty vector (limit %d, dim %d)",
			limit, len(vector))
	}

	rows, err := db.sql.QueryContext(ctx, segmentSearchSelect+
		` WHERE m.notebook_id = ? AND s.status = 'ready'
		   AND g.embedding IS NOT NULL AND g.model = ? AND g.dim = ?
		 ORDER BY g.doc_id, g.ordinal`, notebookID, model, len(vector))
	if err != nil {
		return nil, fmt.Errorf("store: search segments: %w", err)
	}
	defer rows.Close()

	// The query's own norm is needed once, not per row.
	qn := norm(vector)
	if qn == 0 {
		return nil, fmt.Errorf("store: search vector has zero length")
	}

	var (
		out   []domain.Hit
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

		hit, err := scanHit(rows, vector, qn)
		if err != nil {
			return nil, err
		}
		out = insertTop(out, hit, limit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: scan segments: %w", err)
	}
	return out, nil
}

// scanHit reads one row and scores it. The blob's declared dim and its actual
// length must agree, or a truncated write would produce a short dot product that
// still computes and still ranks — the worst kind of wrong.
func scanHit(rows *sql.Rows, vector []float32, qn float64) (domain.Hit, error) {
	var (
		hit     domain.Hit
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
	if err := rows.Scan(&hit.Segment.ID, &hit.Segment.SourceID, &hit.Segment.DocID, &kind,
		&hit.Segment.Ordinal, &hit.Segment.Text,
		&hit.Segment.Span.Page, &hit.Segment.Span.PageEnd,
		&hit.Segment.Span.CharStart, &hit.Segment.Span.CharEnd,
		&assetID, &path, &mime, &size,
		&blob, &dim, &model, &source); err != nil {
		return domain.Hit{}, err
	}

	hit.Segment.Kind = domain.Kind(kind)
	hit.SourceName = source
	if assetID.Valid {
		hit.Segment.Asset = &domain.Asset{
			ID:    assetID.String,
			Path:  path.String,
			MIME:  mime.String,
			Bytes: size.Int64,
		}
	}

	if len(blob) != 4*dim {
		return domain.Hit{}, fmt.Errorf("store: segment %s declares dim %d but holds %d bytes",
			hit.Segment.ID, dim, len(blob))
	}
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	hit.Score = cosine(vector, qn, vec)
	return hit, nil
}

// insertTop keeps out sorted best-first and no longer than limit. The list is at
// most recallTopK long, so a linear insert is cheaper than a heap and much easier
// to read.
func insertTop(out []domain.Hit, hit domain.Hit, limit int) []domain.Hit {
	if len(out) == limit && hit.Score <= out[len(out)-1].Score {
		return out
	}
	if len(out) < limit {
		out = append(out, hit)
	} else {
		out[len(out)-1] = hit
	}
	for i := len(out) - 1; i > 0 && out[i].Score > out[i-1].Score; i-- {
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
