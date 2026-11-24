package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"libris/internal/domain"
)

// collectOrphans deletes every source in ids that has no notebook member left,
// and reports the files that were theirs.
//
// It runs inside the caller's transaction, which is the point: the memberships
// that were just dropped and the source rows they were protecting are decided in
// one commit, so there is no window in which a notebook has stopped listing a
// file and the file has already been promised to the collector. Nothing here
// touches the filesystem — see app.Service.collect, which unlinks after the
// commit and explains why that order is the safe one.
//
// Sources are content-addressed, so "no members" is the only thing that can make
// one disposable. A source that is merely unused by *this* notebook stays, with
// its rasters, because content-addressing is what makes sharing safe.
func collectOrphans(ctx context.Context, tx *sql.Tx, ids []string) ([]domain.Orphan, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Candidates first, in a query that is closed before anything is written:
	// database/sql gives one connection to an open Rows, so a second statement
	// on the same transaction while these rows are live would block forever.
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")

	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM sources
		  WHERE id IN (`+placeholders+`)
		    AND NOT EXISTS (SELECT 1 FROM notebook_sources m WHERE m.source_id = sources.id)
		  ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: find orphans: %w", err)
	}
	var dead []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		dead = append(dead, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var orphans []domain.Orphan
	for _, id := range dead {
		var o domain.Orphan
		o.ID = id
		if err := tx.QueryRowContext(ctx,
			`SELECT name, original_path FROM sources WHERE id = ?`, id).
			Scan(&o.Name, &o.Original); err != nil {
			return nil, fmt.Errorf("store: read orphan %s: %w", id, err)
		}

		assets, err := tx.QueryContext(ctx,
			`SELECT path FROM assets WHERE source_id = ? ORDER BY id`, id)
		if err != nil {
			return nil, fmt.Errorf("store: list orphan files: %w", err)
		}
		for assets.Next() {
			var p string
			if err := assets.Scan(&p); err != nil {
				assets.Close()
				return nil, err
			}
			o.Assets = append(o.Assets, p)
		}
		if err := assets.Err(); err != nil {
			assets.Close()
			return nil, err
		}
		assets.Close()

		// Segments and asset rows go with it: the schema cascades both, which is
		// why a source can never be deleted out from under a citation — the chat
		// row keeps the text and the locator it was written from.
		if _, err := tx.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id); err != nil {
			return nil, fmt.Errorf("store: delete orphan %s: %w", id, err)
		}
		orphans = append(orphans, o)
	}
	return orphans, nil
}
