package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"libris/internal/domain"
)

// legacyAudioVersion is the schema_migrations version recording that the recordings an
// older build wrote have been purged. The number is reserved in that table rather than
// tracked in its own one, because "has this already happened" is exactly what the table
// is for, and because the purge is content-dependent: it finds rows by kind, and once
// the audio path exists again the rows it matches are new ones. Without a marker the
// next start would delete this build's own clips.
const legacyAudioVersion = 5

// PurgeLegacyAudio deletes the audio segments a build from before ADR 0003 wrote, once.
//
// ADR 0003 left those rows in place — recallable, and readable by nothing, since no
// binary any more defined the kind they carry. That was harmless while the kind did not
// exist. It stops being harmless the moment it does: the rows wake as thirty-second
// windows, which score worse than the fifteen-second ones this build cuts and sit at the
// batch ceiling the old preset was written around, ranked in the same list beside them.
// Rather than ship a corpus with two windowing rules inside it, the old rows go.
//
// This is the only place libris deletes a person's data on its own initiative, so the
// scope is stated: segments whose kind is `audio`, written by a build that no longer
// exists, plus the sources that hold nothing else. A source is only in scope if every
// one of its segments is audio, which is what the query below says out loud — ingest
// writes one kind per source, so a mixed source cannot exist, and if one somehow did it
// would be left whole rather than have its pages caught in the cascade.
//
// The files travel with the answer rather than being unlinked here, which is the same
// bargain DetachSource makes: rows first, in one transaction, then app.Service.collect
// unlinks. A file deleted while a citation still resolves is a broken answer; one left
// behind is only disk.
//
// It returns how many legacy segments went, and the sources that went with them.
func (db *DB) PurgeLegacyAudio(ctx context.Context) (int, []domain.Orphan, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()

	var done int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations WHERE version = ?`, legacyAudioVersion).
		Scan(&done); err != nil {
		return 0, nil, fmt.Errorf("store: read audio purge marker: %w", err)
	}
	if done > 0 {
		return 0, nil, nil
	}

	// Candidates first and rows closed before anything is written: an open Rows holds
	// the connection, and a second statement on the transaction then waits on it.
	ids, err := legacyAudioSources(ctx, tx)
	if err != nil {
		return 0, nil, err
	}

	var purged int
	if len(ids) > 0 {
		// Bound into the IN clauses below, whose placeholders it must match.
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}

		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM segments WHERE source_id IN (`+placeholders(len(ids))+`) AND kind = ?`,
			append(append([]any{}, args...), legacyAudioKind)...).
			Scan(&purged); err != nil {
			return 0, nil, fmt.Errorf("store: count legacy audio: %w", err)
		}

		// Memberships first, so collectOrphans sees these as memberless and reports
		// their files: segments and asset rows leave with the source by cascade, which
		// is why no DELETE here names either table.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM notebook_sources WHERE source_id IN (`+placeholders(len(ids))+`)`, args...); err != nil {
			return 0, nil, fmt.Errorf("store: detach legacy audio: %w", err)
		}
	}

	orphans, err := collectOrphans(ctx, tx, ids)
	if err != nil {
		return 0, nil, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%s','now'))`,
		legacyAudioVersion); err != nil {
		return 0, nil, fmt.Errorf("store: record audio purge: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return purged, orphans, nil
}

// legacyAudioKind is the value the old build wrote into segments.kind. Named once so
// the query below and the marker beside it cannot drift apart.
const legacyAudioKind = "audio"

// legacyAudioSources lists the sources made entirely of legacy audio segments.
//
// "Entirely" is the guard, not an optimisation: it keeps a source that somehow carries
// both a clip and a page out of the cascade that deletes it.
func legacyAudioSources(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT s.id FROM sources s
		  WHERE EXISTS (SELECT 1 FROM segments g WHERE g.source_id = s.id AND g.kind = ?)
		    AND NOT EXISTS (SELECT 1 FROM segments g WHERE g.source_id = s.id AND g.kind <> ?)
		  ORDER BY s.id`, legacyAudioKind, legacyAudioKind)
	if err != nil {
		return nil, fmt.Errorf("store: find legacy audio sources: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// placeholders is `?, ?, ?` for a list bound into an IN clause.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
