package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"libris/internal/domain"
)

// newID is a notebook or chat identifier: 16 random bytes, hex. Short enough to
// put in a URL, long enough that collisions are not a consideration.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("store: new id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func now() int64 { return time.Now().Unix() }

// CreateNotebook adds a notebook and returns it.
func (db *DB) CreateNotebook(ctx context.Context, name string) (domain.Notebook, error) {
	id, err := newID()
	if err != nil {
		return domain.Notebook{}, err
	}

	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO notebooks (id, name, created_at) VALUES (?, ?, ?)`, id, name, now())
	if err != nil {
		return domain.Notebook{}, fmt.Errorf("store: create notebook: %w", err)
	}
	return domain.Notebook{ID: id, Name: name, CreatedAt: now()}, nil
}

// RenameNotebook changes a notebook's name. ErrNotFound when it is gone.
func (db *DB) RenameNotebook(ctx context.Context, id, name string) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE notebooks SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("store: rename notebook: %w", err)
	}
	return one(res, id)
}

// DeleteNotebook removes a notebook with its chats and memberships. Sources and
// their files are deliberately untouched: another notebook may cite them, and
// nothing re-derives a raster cheaply.
func (db *DB) DeleteNotebook(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM notebooks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete notebook: %w", err)
	}
	return one(res, id)
}

// GetNotebook fetches one by id.
func (db *DB) GetNotebook(ctx context.Context, id string) (domain.Notebook, error) {
	return scanNotebook(db.sql.QueryRowContext(ctx,
		`SELECT id, name, created_at FROM notebooks WHERE id = ?`, id))
}

// ListNotebooks returns every notebook, oldest first.
func (db *DB) ListNotebooks(ctx context.Context) ([]domain.Notebook, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, name, created_at FROM notebooks ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list notebooks: %w", err)
	}
	defer rows.Close()

	var out []domain.Notebook
	for rows.Next() {
		n, err := scanNotebook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanNotebook(row rowScanner) (domain.Notebook, error) {
	var n domain.Notebook
	err := row.Scan(&n.ID, &n.Name, &n.CreatedAt)
	if err == sql.ErrNoRows {
		return domain.Notebook{}, ErrNotFound
	}
	return n, err
}

// one turns a zero-row UPDATE or DELETE into ErrNotFound.
func one(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return nil
}
