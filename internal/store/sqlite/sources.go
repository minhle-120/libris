package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"libris/internal/domain"
)

// AddSource inserts a source and attaches it to a notebook, in one transaction.
//
// The source id is the content hash, so adding a file that is already known
// does not re-store it: added is false and the existing row's status is
// returned unchanged — an already-ready source stays ready and never re-runs
// poppler.
func (db *DB) AddSource(ctx context.Context, notebookID string, source domain.Source) (domain.Source, bool, error) {
	findings, err := json.Marshal(source.Findings)
	if err != nil {
		return domain.Source{}, false, err
	}
	if source.AddedAt == 0 {
		source.AddedAt = now()
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return domain.Source{}, false, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO sources (id, name, kind, sha256, bytes, original_path, status, detail, warnings, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO NOTHING`,
		source.ID, source.Name, string(source.Kind), source.SHA256, source.Bytes,
		source.OriginalPath, string(source.Status), source.Detail, string(findings), source.AddedAt)
	if err != nil {
		return domain.Source{}, false, fmt.Errorf("store: add source: %w", err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return domain.Source{}, false, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO notebook_sources (notebook_id, source_id, added_at) VALUES (?, ?, ?)
		 ON CONFLICT (notebook_id, source_id) DO NOTHING`,
		notebookID, source.ID, source.AddedAt); err != nil {
		return domain.Source{}, false, fmt.Errorf("store: attach source: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return domain.Source{}, false, err
	}

	if inserted == 0 {
		existing, err := db.GetSource(ctx, source.ID)
		return existing, false, err
	}
	source.Findings = nil
	return source, true, nil
}

func (db *DB) setMembership(ctx context.Context, notebookID, sourceID string) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO notebook_sources (notebook_id, source_id, added_at) VALUES (?, ?, ?)
		 ON CONFLICT (notebook_id, source_id) DO NOTHING`,
		notebookID, sourceID, now())
	if err != nil {
		return fmt.Errorf("store: attach source: %w", err)
	}
	return nil
}

// AttachSource puts an existing source into a notebook. This is the dedupe
// path: the second notebook to receive the same file pays one row insert.
func (db *DB) AttachSource(ctx context.Context, notebookID, sourceID string) error {
	var have int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM sources WHERE id = ?`, sourceID).Scan(&have)
	if err != nil {
		return err
	}
	if have == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, sourceID)
	}
	var ignore int
	if err := db.sql.QueryRowContext(ctx, `SELECT 1 FROM notebooks WHERE id = ?`, notebookID).Scan(&ignore); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: %s", ErrNotFound, notebookID)
		}
		return err
	}
	return db.setMembership(ctx, notebookID, sourceID)
}

// DetachSource drops one membership, and collects the source if it was the last
// one: a file no notebook lists is not pending anything, and keeping it forever
// was a decision nobody made. The returned orphans name the files to unlink —
// empty when another notebook still uses the source, which is the common case
// the shared-by-content-hash design exists for.
func (db *DB) DetachSource(ctx context.Context, notebookID, sourceID string) ([]domain.Orphan, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM notebook_sources WHERE notebook_id = ? AND source_id = ?`, notebookID, sourceID)
	if err != nil {
		return nil, fmt.Errorf("store: detach source: %w", err)
	}
	if err := one(res, sourceID); err != nil {
		return nil, err
	}
	orphans, err := collectOrphans(ctx, tx, []string{sourceID})
	if err != nil {
		return nil, err
	}
	return orphans, tx.Commit()
}

// SetSourceStatus records how ingest ended. Findings are the non-fatal things the run noticed —
// a program this machine does not have, a stage that gave up — which the UI shows next to a
// source that did produce something, and codes so the UI can say what to do rather than what was
// said. The column is still named `warnings`: migrations here are append-only and a column name
// is not part of what anyone reads, so renaming it would be a migration for nobody's benefit.
func (db *DB) SetSourceStatus(ctx context.Context, id string, status domain.SourceStatus, detail string, findings []domain.Finding) error {
	blob, err := json.Marshal(findings)
	if err != nil {
		return err
	}
	if status != domain.StatusReady && status != domain.StatusUnsupported &&
		status != domain.StatusFailed && status != domain.StatusProcessing {
		return fmt.Errorf("store: bad status %q", status)
	}

	res, err := db.sql.ExecContext(ctx,
		`UPDATE sources SET status = ?, detail = ?, warnings = ? WHERE id = ?`,
		string(status), detail, string(blob), id)
	if err != nil {
		return fmt.Errorf("store: source status: %w", err)
	}
	return one(res, id)
}

// FailStaleSources closes out work interrupted by a restart. Without it a
// source killed mid-raster would poll "processing" forever.
func (db *DB) FailStaleSources(ctx context.Context) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`UPDATE sources SET status = 'failed', detail = 'interrupted by a restart'
		 WHERE status = 'processing'`)
	if err != nil {
		return 0, fmt.Errorf("store: fail stale sources: %w", err)
	}
	return res.RowsAffected()
}

// GetSource fetches one source by content hash.
func (db *DB) GetSource(ctx context.Context, id string) (domain.Source, error) {
	return scanSource(db.sql.QueryRowContext(ctx, sourceSelect+` WHERE s.id = ?`, id))
}

const sourceSelect = `
	SELECT s.id, s.name, s.kind, s.sha256, s.bytes, s.original_path,
	       s.status, s.detail, s.warnings, s.created_at,
	       (SELECT COUNT(*) FROM segments g WHERE g.source_id = s.id),
	       (SELECT COUNT(*) FROM notebook_sources m WHERE m.source_id = s.id)
	  FROM sources s`

// ListSources returns a notebook's sources in the order they were added.
func (db *DB) ListSources(ctx context.Context, notebookID string) ([]domain.Source, error) {
	rows, err := db.sql.QueryContext(ctx, sourceSelect+
		` JOIN notebook_sources m ON m.source_id = s.id
		  WHERE m.notebook_id = ?
		  ORDER BY m.added_at, s.id`, notebookID)
	if err != nil {
		return nil, fmt.Errorf("store: list sources: %w", err)
	}
	defer rows.Close()

	var out []domain.Source
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanSource(row rowScanner) (domain.Source, error) {
	var (
		s        domain.Source
		kind     string
		status   string
		warnings string
	)
	err := row.Scan(&s.ID, &s.Name, &kind, &s.SHA256, &s.Bytes, &s.OriginalPath,
		&status, &s.Detail, &warnings, &s.AddedAt, &s.SegmentCount, &s.NotebooksUsing)
	if err == sql.ErrNoRows {
		return domain.Source{}, ErrNotFound
	}
	if err != nil {
		return domain.Source{}, err
	}
	s.Kind = domain.SourceKind(kind)
	s.Status = domain.SourceStatus(status)
	if warnings != "" && warnings != "[]" {
		// A row written before findings were typed holds prose where a code and a detail belong,
		// and that blob is left alone rather than guessed at: inventing a code for a sentence
		// would tell the UI to offer an action nobody verified. The findings of a run are
		// derivable — "run it again" re-derives them from the same original — so losing them
		// costs a line under a row and not a fact about the file.
		_ = json.Unmarshal([]byte(warnings), &s.Findings)
	}
	return s, nil
}
