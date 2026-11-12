package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"

	"libris/internal/domain"
)

// segmentSelect is the shared projection of the two segment queries, so the
// scan below matches one place. The LEFT JOIN keeps text segments, which carry
// no asset.
const segmentSelect = `
	SELECT g.id, g.source_id, g.doc_id, g.kind, g.ordinal, g.text,
	       g.page, g.page_end, g.char_start, g.char_end, g.t0, g.t1,
	       a.id, a.path, a.mime, a.bytes
	  FROM segments g
	  LEFT JOIN assets a ON a.id = g.asset_id`

const segmentFromNotebook = `
	SELECT g.id, g.source_id, g.doc_id, g.kind, g.ordinal, g.text,
	       g.page, g.page_end, g.char_start, g.char_end, g.t0, g.t1,
	       a.id, a.path, a.mime, a.bytes
	  FROM segments g
	  JOIN sources s          ON s.id = g.source_id
	  JOIN notebook_sources m ON m.source_id = s.id
	  LEFT JOIN assets a      ON a.id = g.asset_id`

// PutAssets records derived files. Idempotent by id, so a retried ingest does
// not duplicate rows for the same page raster.
func (db *DB) PutAssets(ctx context.Context, sourceID string, assets []domain.Asset) error {
	if len(assets) == 0 {
		return nil
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, a := range assets {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO assets (id, source_id, path, mime, bytes) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (id) DO UPDATE
			   SET path = excluded.path, mime = excluded.mime, bytes = excluded.bytes`,
			a.ID, sourceID, a.Path, a.MIME, a.Bytes); err != nil {
			return fmt.Errorf("store: put asset %s: %w", a.ID, err)
		}
	}
	return tx.Commit()
}

// PutSegments writes a source's segments in one transaction, replacing any
// previous set for that source.
//
// Chunks are re-derived wholesale rather than diffed, so the old rows go first.
// That also discards every vector for the source, which is the honest outcome:
// a vector for a chunk that no longer exists must not be retrieved. It is why
// ingest embeds in the same job, right after this call — a re-ingest that left
// the source `ready` with no vectors would answer questions with a silent slice
// of itself.
//
// The embedding, dim and model columns are deliberately not written here: they
// belong to PutEmbeddings, so "has segments" and "has vectors" stay separate
// facts rather than one half-written row.
func (db *DB) PutSegments(ctx context.Context, sourceID string, segments []domain.Segment) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM segments WHERE source_id = ?`, sourceID); err != nil {
		return fmt.Errorf("store: clear segments: %w", err)
	}
	for _, s := range segments {
		var assetID any
		if s.Asset != nil {
			assetID = s.Asset.ID
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO segments
			   (id, source_id, doc_id, kind, ordinal, text,
			    page, page_end, char_start, char_end, t0, t1, asset_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.ID, sourceID, s.DocID, string(s.Kind), s.Ordinal, s.Text,
			s.Span.Page, s.Span.PageEnd, s.Span.CharStart, s.Span.CharEnd,
			s.Span.T0, s.Span.T1, assetID); err != nil {
			return fmt.Errorf("store: put segment %s: %w", s.ID, err)
		}
	}
	return tx.Commit()
}

// encodeVector packs a vector as little-endian float32s. That is the layout the
// cosine scan reads, and `dim` stores the count so a truncated blob is a detect-
// able error rather than a silently short dot product.
func encodeVector(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// PutEmbeddings stores one vector per segment. The caller has just written those
// segments, so a missing row is not a no-op: it means the source was re-ingested
// mid-job and the vectors are for chunks that no longer exist.
func (db *DB) PutEmbeddings(ctx context.Context, embeddings []domain.Embedding) error {
	if len(embeddings) == 0 {
		return nil
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`UPDATE segments SET embedding = ?, dim = ?, model = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("store: prepare embedding write: %w", err)
	}
	defer stmt.Close()

	for _, e := range embeddings {
		res, err := stmt.ExecContext(ctx, encodeVector(e.Vector), e.Dim, e.Model, e.SegmentID)
		if err != nil {
			return fmt.Errorf("store: put embedding %s: %w", e.SegmentID, err)
		}
		// SQLite counts a matching row whose values were identical as affected, so
		// zero really does mean "no such segment".
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: put embedding %s: %w", e.SegmentID, err)
		}
		if n == 0 {
			return fmt.Errorf("%w: segment %s vanished mid-ingest", ErrNotFound, e.SegmentID)
		}
	}
	return tx.Commit()
}

// ListSegments returns a source's segments in reading order. Text and page
// rasters are separate documents, so ordering by document then ordinal keeps
// each of them contiguous.
//
// It does not read the embedding columns on purpose: a source's chunks are listed
// for a person to read, and hauling 3072 floats per row through SQLite for that
// would make the inspector slow for no reason.
func (db *DB) ListSegments(ctx context.Context, sourceID string) ([]domain.Segment, error) {
	rows, err := db.sql.QueryContext(ctx, segmentSelect+
		` WHERE g.source_id = ?
		  ORDER BY g.doc_id, g.ordinal`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("store: list segments: %w", err)
	}
	defer rows.Close()
	return scanSegments(rows)
}

// SegmentsForNotebook returns every segment belonging to a notebook's ready
// sources. This is the retrieval scope: the whole notebook, nothing outside it.
// Brute-force cosine over 10⁴–10⁵ rows is what the embedding stage will scan.
func (db *DB) SegmentsForNotebook(ctx context.Context, notebookID string) ([]domain.Segment, error) {
	rows, err := db.sql.QueryContext(ctx, segmentFromNotebook+
		` WHERE m.notebook_id = ? AND s.status = 'ready'
		  ORDER BY g.doc_id, g.ordinal`, notebookID)
	if err != nil {
		return nil, fmt.Errorf("store: notebook segments: %w", err)
	}
	defer rows.Close()
	return scanSegments(rows)
}

func scanSegments(rows *sql.Rows) ([]domain.Segment, error) {
	var out []domain.Segment
	for rows.Next() {
		var (
			s       domain.Segment
			kind    string
			assetID sql.NullString
			path    sql.NullString
			mime    sql.NullString
			size    sql.NullInt64
		)
		if err := rows.Scan(&s.ID, &s.SourceID, &s.DocID, &kind, &s.Ordinal, &s.Text,
			&s.Span.Page, &s.Span.PageEnd, &s.Span.CharStart, &s.Span.CharEnd,
			&s.Span.T0, &s.Span.T1, &assetID, &path, &mime, &size); err != nil {
			return nil, err
		}
		s.Kind = domain.Kind(kind)
		if assetID.Valid {
			s.Asset = &domain.Asset{
				ID:    assetID.String,
				Path:  path.String,
				MIME:  mime.String,
				Bytes: size.Int64,
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetAsset looks up one derived file by its sink name. The HTTP layer uses this
// to serve a page raster without accepting a filesystem path from the client.
func (db *DB) GetAsset(ctx context.Context, id string) (domain.Asset, error) {
	var a domain.Asset
	err := db.sql.QueryRowContext(ctx,
		`SELECT id, path, mime, bytes FROM assets WHERE id = ?`, id).
		Scan(&a.ID, &a.Path, &a.MIME, &a.Bytes)
	if err == sql.ErrNoRows {
		return domain.Asset{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return a, err
}

// SourceIDsForNotebook is the cheap membership query, used to answer "is this
// file already here" without touching the segment tables.
func (db *DB) SourceIDsForNotebook(ctx context.Context, notebookID string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT source_id FROM notebook_sources WHERE notebook_id = ?`, notebookID)
	if err != nil {
		return nil, fmt.Errorf("store: notebook source ids: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
