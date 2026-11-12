package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"libris/internal/domain"
)

// Chats and messages: what a user reads and comes back to.
//
// Nothing here knows about research, chunk registries or models. A message is
// prose plus what its [[Chunk N]] labels resolved to, and that is deliberate —
// the answer outlives the retrieval that produced it, the model that wrote it and
// the settings that were saved when it was asked.

// CreateChat starts a conversation in a notebook. The caller owns the title;
// today that means the first question, truncated.
func (db *DB) CreateChat(ctx context.Context, notebookID, title string) (domain.Chat, error) {
	if _, err := db.GetNotebook(ctx, notebookID); err != nil {
		return domain.Chat{}, err
	}

	id, err := newID()
	if err != nil {
		return domain.Chat{}, err
	}

	at := now()
	if _, err = db.sql.ExecContext(ctx,
		`INSERT INTO chats (id, notebook_id, title, created_at) VALUES (?, ?, ?, ?)`,
		id, notebookID, title, at); err != nil {
		return domain.Chat{}, fmt.Errorf("store: create chat: %w", err)
	}
	return domain.Chat{ID: id, NotebookID: notebookID, Title: title, CreatedAt: at}, nil
}

const chatSelect = `SELECT id, notebook_id, title, created_at FROM chats`

// GetChat fetches one chat. The caller compares NotebookID to the notebook in the
// route, so a chat id from another notebook reads as absent rather than as a
// permission answer.
func (db *DB) GetChat(ctx context.Context, id string) (domain.Chat, error) {
	return scanChat(db.sql.QueryRowContext(ctx, chatSelect+` WHERE id = ?`, id))
}

// ListChats returns a notebook's conversations, most recent first: the newest is
// the one the UI opens.
func (db *DB) ListChats(ctx context.Context, notebookID string) ([]domain.Chat, error) {
	rows, err := db.sql.QueryContext(ctx,
		// Ordered by rowid, not created_at: the timestamp has second resolution, so two
		// chats made in one second — which is what "new chat" twice is — would otherwise
		// come back in an order decided by their random ids.
		chatSelect+` WHERE notebook_id = ? ORDER BY rowid DESC`, notebookID)
	if err != nil {
		return nil, fmt.Errorf("store: list chats: %w", err)
	}
	defer rows.Close()

	var out []domain.Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteChat removes a conversation with its messages. The sources it cited are
// untouched, for the same reason deleting a notebook leaves them alone.
func (db *DB) DeleteChat(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM chats WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete chat: %w", err)
	}
	return one(res, id)
}

func scanChat(row rowScanner) (domain.Chat, error) {
	var c domain.Chat
	err := row.Scan(&c.ID, &c.NotebookID, &c.Title, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return domain.Chat{}, ErrNotFound
	}
	return c, err
}

// messageSelect is the reading projection. trace_json is absent because listing a
// chat's messages renders prose and citations, and a transcript per row is dead
// weight in a query that will not use it — the same reason the listing queries do
// not read vectors.
const messageSelect = `
	SELECT id, chat_id, role, content, status, created_at, citations
	  FROM messages`

// AppendMessage writes one turn and returns it with its id. Content is often empty
// for an assistant row: the citations are recorded before the answer streams, so a
// citation can never point at a message that was never stored, and a process that
// dies mid-stream leaves a row that reads as interrupted rather than a lie about
// what was cited.
func (db *DB) AppendMessage(ctx context.Context, m domain.Message) (domain.Message, error) {
	citations, err := json.Marshal(m.Citations)
	if err != nil {
		return domain.Message{}, fmt.Errorf("store: encode citations: %w", err)
	}

	at := now()
	// exclude_from_history is written 0 here and set by UpdateMessage. One place
	// decides it — whoever finishes a turn, while it still knows why — rather than
	// the insert re-deriving it from a status that is not final yet.
	res, err := db.sql.ExecContext(ctx,
		`INSERT INTO messages (chat_id, role, content, citations, status, created_at, exclude_from_history)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		m.ChatID, string(m.Role), m.Content, string(citations), string(m.Status), at)
	if err != nil {
		return domain.Message{}, fmt.Errorf("store: append message: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return domain.Message{}, fmt.Errorf("store: append message id: %w", err)
	}

	m.ID, m.CreatedAt = id, at
	return m, nil
}

// UpdateMessage finishes a row that AppendMessage created earlier. The update type
// is domain's, because the caller deciding what a finished turn means is app, and
// the two must not agree by accident. ErrNotFound when the id is gone.
func (db *DB) UpdateMessage(ctx context.Context, id int64, u domain.MessageUpdate) error {
	// UpdateMessage finishes a row. ErrNotFound when the id is gone.
	citations, err := json.Marshal(u.Citations)
	if err != nil {
		return fmt.Errorf("store: encode citations: %w", err)
	}

	res, err := db.sql.ExecContext(ctx,
		`UPDATE messages
		    SET content = ?, status = ?, citations = ?, trace_json = ?
		  WHERE id = ?`,
		u.Content, string(u.Status), string(citations), u.Trace, id)
	if err != nil {
		return fmt.Errorf("store: update message: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update message: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: message %d", ErrNotFound, id)
	}
	return nil
}

// ListMessages returns a chat's messages oldest first — the order a reader wants.
func (db *DB) ListMessages(ctx context.Context, chatID string) ([]domain.Message, error) {
	return db.messages(ctx, messageSelect+` WHERE chat_id = ? ORDER BY id`, chatID)
}

// GetMessage reads one finished row back. Ask returns it rather than reconstructing
// what it wrote, so the message a caller hands the browser is the one in the
// database — timestamps included.
func (db *DB) GetMessage(ctx context.Context, id int64) (domain.Message, error) {
	rows, err := db.messages(ctx, messageSelect+` WHERE id = ?`, id)
	if err != nil {
		return domain.Message{}, err
	}
	if len(rows) == 0 {
		return domain.Message{}, fmt.Errorf("%w: message %d", ErrNotFound, id)
	}
	return rows[0], nil
}

// RecentMessages returns the last `limit` messages a chat should remember, oldest
// first, skipping the turns excluded from history.
//
// The exclusion is a stored column rather than something every caller re-derives
// from the status, so the rule ("a turn that found nothing is not knowledge") is
// decided once, by whoever finished the turn, while it still knows why.
func (db *DB) RecentMessages(ctx context.Context, chatID string, limit int) ([]domain.Message, error) {
	if limit <= 0 {
		return nil, nil
	}

	out, err := db.messages(ctx,
		messageSelect+` WHERE chat_id = ? AND exclude_from_history = 0 ORDER BY id DESC LIMIT ?`,
		chatID, limit)
	if err != nil {
		return nil, err
	}

	// Read newest-first because that is how a limit works, and handed back oldest
	// first because that is how a conversation reads.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ExcludeMessages marks whole turns as not knowledge, so they reach no later turn's
// history.
//
// It takes ids rather than a message because exclusion is a property of a *turn*, not
// of a row: a question the library could not answer is excluded together with its
// answer. Leaving the query in place keeps the dead end in the conversation while
// deleting only its outcome, and the next turn inherits both.
//
// The verdict is the caller's, recorded here while it still knows why — nothing
// re-derives it from a status afterwards.
func (db *DB) ExcludeMessages(ctx context.Context, ids ...int64) error {
	if len(ids) == 0 {
		return nil
	}

	args := make([]any, 0, len(ids))
	args = args[:0]
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		placeholders = append(placeholders, "?")
	}

	if _, err := db.sql.ExecContext(ctx,
		`UPDATE messages SET exclude_from_history = 1 WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...); err != nil {
		return fmt.Errorf("store: exclude %d messages: %w", len(ids), err)
	}
	return nil
}

func (db *DB) messages(ctx context.Context, query string, args ...any) ([]domain.Message, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list messages: %w", err)
	}
	defer rows.Close()

	var out []domain.Message
	for rows.Next() {
		var (
			m         domain.Message
			role      string
			status    string
			citations string
		)
		if err := rows.Scan(&m.ID, &m.ChatID, &role, &m.Content, &status, &m.CreatedAt, &citations); err != nil {
			return nil, fmt.Errorf("store: scan message: %w", err)
		}
		m.Role, m.Status = domain.Role(role), domain.MessageStatus(status)

		// Both encodings a caller can produce mean "no citations": nil marshals to
		// "null" and an empty slice to "[]". Which one arrives is not something the
		// reader should have to know.
		if citations != "" && citations != "null" {
			if err := json.Unmarshal([]byte(citations), &m.Citations); err != nil {
				return nil, fmt.Errorf("store: decode citations for message %d: %w", m.ID, err)
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
