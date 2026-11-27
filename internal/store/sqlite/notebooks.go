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

// DeleteNotebook removes a notebook with its chats and memberships, and collects
// every source the last membership was: a file that no notebook lists after this
// has no reason to sit on the disk forever. Sources another notebook still uses
// stay, rows and rasters alike — that is what content-addressing bought.
//
// The answer lists the files to unlink, because the transaction that removes the
// rows cannot also remove the bytes: a file deleted while its citation still
// resolves is a broken answer, so the commit has to come first.
func (db *DB) DeleteNotebook(ctx context.Context, id string) ([]domain.Orphan, error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// The members have to be read before the cascade erases them; a DELETE of a
	// notebook takes notebook_sources with it, and the sweep needs to know which
	// sources it just made possible to delete.
	rows, err := tx.QueryContext(ctx, `SELECT source_id FROM notebook_sources WHERE notebook_id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list notebook members: %w", err)
	}
	var members []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, err
		}
		members = append(members, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	res, err := tx.ExecContext(ctx, `DELETE FROM notebooks WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("store: delete notebook: %w", err)
	}
	if err := one(res, id); err != nil {
		return nil, err
	}
	orphans, err := collectOrphans(ctx, tx, members)
	if err != nil {
		return nil, err
	}
	return orphans, tx.Commit()
}

// notebookSelect is the shared projection of the two notebook queries. The counts
// ride along rather than costing a round trip per row: one is what the sidebar lists
// beside every notebook, the other is what the delete confirm is allowed to promise
// about.
const notebookSelect = `
	SELECT n.id, n.name, n.created_at,
	       (SELECT COUNT(*) FROM notebook_sources m WHERE m.notebook_id = n.id),
	       (SELECT COUNT(*) FROM chats c WHERE c.notebook_id = n.id)
	  FROM notebooks n`

// GetNotebook fetches one by id.
func (db *DB) GetNotebook(ctx context.Context, id string) (domain.Notebook, error) {
	return scanNotebook(db.sql.QueryRowContext(ctx, notebookSelect+` WHERE n.id = ?`, id))
}

// ListNotebooks returns every notebook, oldest first.
func (db *DB) ListNotebooks(ctx context.Context) ([]domain.Notebook, error) {
	rows, err := db.sql.QueryContext(ctx,
		notebookSelect+` ORDER BY n.created_at, n.id`)
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
	err := row.Scan(&n.ID, &n.Name, &n.CreatedAt, &n.SourceCount, &n.ChatCount)
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
