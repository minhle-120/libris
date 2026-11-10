// Package sqlite is the one place that knows SQL. Everything else goes through
// app, which declares the interfaces it needs.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" driver
)

//go:embed migrations/*.sql
var embedded embed.FS

// migrations is what migrate reads. A test swaps it to exercise the failure
// paths, which the real files cannot produce.
var migrations fs.FS = embedded

// ErrNotFound is returned when an id does not exist. Callers compare with
// errors.Is rather than inspecting messages.
var ErrNotFound = errors.New("store: not found")

// DB is an opened database. One process owns the directory, so there is no
// cross-process locking beyond SQLite's own busy timeout.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the database at path and applies any pending
// migrations.
//
// WAL keeps the ingest worker's writes from blocking the status polling the UI
// does while a PDF is being rasterised. foreign_keys and busy_timeout are set
// per connection in the DSN because database/sql may open new ones at any time.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	raw, err := sql.Open("sqlite3", "file:"+path+
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One writer at a time keeps SQLITE_BUSY off the table for the writes we
	// actually make; readers still run concurrently under WAL.
	raw.SetMaxOpenConns(4)

	db := &DB{sql: raw}
	if err := db.migrate(context.Background()); err != nil {
		raw.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Close() error { return db.sql.Close() }

// Raw exposes the pool for the embedding stage's bulk writes, which want a
// single transaction over thousands of rows.
func (db *DB) Raw() *sql.DB { return db.sql }

// migrate applies embedded .sql files in filename order, each in its own
// transaction, recording the version so a re-run is a no-op.
func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
		   version    INTEGER PRIMARY KEY,
		   applied_at INTEGER NOT NULL
		 )`); err != nil {
		return fmt.Errorf("store: schema table: %w", err)
	}

	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, name := range files {
		version, err := strconv.Atoi(filepath.Base(name)[:4])
		if err != nil {
			return fmt.Errorf("store: migration %s: %w", name, err)
		}

		var have int
		err = db.sql.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&have)
		if err != nil {
			return err
		}
		if have > 0 {
			continue
		}

		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			return err
		}

		tx, err := db.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%s','now'))`,
			version); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit %s: %w", name, err)
		}
	}
	return nil
}
