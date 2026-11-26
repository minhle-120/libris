package sqlite

import (
	"context"
	"fmt"
)

// stoppedStatusVersion is the schema_migrations version recording that the sources
// table knows the status `stopped`. Like the audio purge's marker, the number lives in
// that table rather than in a file of its own, because "has this already happened" is
// what that table is for.
const stoppedStatusVersion = 6

// goMigrations is how many migrations the schema carries in Go rather than in a .sql
// file, so the tests that count schema_migrations do not have to know which ones.
const goMigrations = 1

// allowStoppedStatus rebuilds the sources table so a status the person who uploaded
// the file asked for is one the column admits.
//
// It is a rebuild and not an ALTER because SQLite cannot change a CHECK: the old shape
// is created beside the table it replaces, the rows are copied across, and the names
// are swapped. That is only safe with foreign keys off. With them on, dropping the
// parent runs its ON DELETE CASCADE and takes the source's segments, its assets and
// every notebook membership with it — which the copy would faithfully restore on the
// next start, as an empty source.
//
// Foreign keys are a per-connection setting and the pool hands connections out as it
// likes, so this runs on a single connection held for the duration: that one is
// switched off, does the work, and is switched back before it goes, so no other
// connection ever sees a database without them.
func (db *DB) allowStoppedStatus(ctx context.Context) error {
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: status migration: %w", err)
	}
	defer conn.Close()

	var done int
	if err := conn.QueryRowContext(ctx,
		`SELECT count(*) FROM schema_migrations WHERE version = ?`, stoppedStatusVersion).
		Scan(&done); err != nil {
		return fmt.Errorf("store: read status migration marker: %w", err)
	}
	if done > 0 {
		return nil
	}

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("store: status migration: foreign keys off: %w", err)
	}
	// The connection goes back to the pool as it was found, including on the paths
	// where the rebuild itself fails.
	defer conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: status migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, stoppedStatusRebuild); err != nil {
		return fmt.Errorf("store: status migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%s','now'))`,
		stoppedStatusVersion); err != nil {
		return fmt.Errorf("store: record status migration: %w", err)
	}
	return tx.Commit()
}

// stoppedStatusRebuild is the table as 0001 made it, with one more word in the list.
// The columns are named on both sides of the copy so a future migration that adds one
// fails loudly here rather than shifting every value by a column.
const stoppedStatusRebuild = `
	CREATE TABLE sources_new (
	  id            TEXT PRIMARY KEY,
	  name          TEXT NOT NULL,
	  kind          TEXT NOT NULL,
	  sha256        TEXT NOT NULL,
	  bytes         INTEGER NOT NULL,
	  original_path TEXT NOT NULL,
	  status        TEXT NOT NULL CHECK (status IN ('processing','ready','unsupported','failed','stopped')),
	  detail        TEXT NOT NULL DEFAULT '',
	  warnings      TEXT NOT NULL DEFAULT '[]',
	  created_at    INTEGER NOT NULL
	);

	INSERT INTO sources_new (id, name, kind, sha256, bytes, original_path, status, detail, warnings, created_at)
	  SELECT id, name, kind, sha256, bytes, original_path, status, detail, warnings, created_at FROM sources;

	DROP TABLE sources;

	ALTER TABLE sources_new RENAME TO sources;
`
