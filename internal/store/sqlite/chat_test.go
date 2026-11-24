package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"libris/internal/domain"
)

func chatIn(t *testing.T, db *DB, notebookID, title string) domain.Chat {
	t.Helper()
	c, err := db.CreateChat(context.Background(), notebookID, title)
	if err != nil {
		t.Fatalf("CreateChat(%s): %v", title, err)
	}
	return c
}

func TestChatLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")
	b := notebook(t, db, "B")

	first := chatIn(t, db, a, "What did Smith measure?")
	chatIn(t, db, a, "And the follow-up?")
	chatIn(t, db, b, "Elsewhere")

	got, err := db.GetChat(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.NotebookID != a || got.Title != "What did Smith measure?" {
		t.Errorf("got %+v", got)
	}

	list, err := db.ListChats(ctx, a)
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("A has %d chats, want 2 — B's must not leak in", len(list))
	}
	if list[0].ID == first.ID {
		t.Error("newest chat should come first; the UI opens list[0]")
	}
	if got, _ := db.ListChats(ctx, b); len(got) != 1 || got[0].Title != "Elsewhere" {
		t.Errorf("B's chats = %+v, want only B's", got)
	}

	if err := db.DeleteChat(ctx, first.ID); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}
	if _, err := db.GetChat(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetChat after delete = %v, want ErrNotFound", err)
	}

	// Deleting the conversation leaves the sources it cited alone.
	if _, err := db.DeleteNotebook(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.ListChats(ctx, b); len(got) != 0 {
		t.Errorf("deleting the notebook left %d chats", len(got))
	}
}

// A reader renames a conversation. What matters to the store is that the rename names
// exactly one row: an id that is not there is an error, not a no-op, and the chat next
// to it in the same list is not involved.
func TestUpdateChatTitle(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	a := notebook(t, db, "A")

	target := chatIn(t, db, a, "Why does the long tail fall off so slowly?")
	neighbour := chatIn(t, db, a, "And the follow-up?")

	if err := db.UpdateChatTitle(ctx, target.ID, "Why the tail is long"); err != nil {
		t.Fatalf("UpdateChatTitle: %v", err)
	}

	got, err := db.ListChats(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d chats", len(got))
	}
	for _, c := range got {
		switch c.ID {
		case target.ID:
			if c.Title != "Why the tail is long" {
				t.Errorf("renamed chat is still %q", c.Title)
			}
		case neighbour.ID:
			if c.Title != "And the follow-up?" {
				t.Errorf("the other chat was renamed too: %q", c.Title)
			}
		}
	}

	if err := db.UpdateChatTitle(ctx, "no-such-chat", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("renaming an absent chat = %v, want ErrNotFound", err)
	}
}

func TestCreateChatUnknownNotebook(t *testing.T) {
	if _, err := openDB(t).CreateChat(context.Background(), "nope", "title"); !errors.Is(err, ErrNotFound) {
		t.Errorf("CreateChat = %v, want ErrNotFound — a chat must not be orphaned", err)
	}
}

// A citation is frozen, not referenced: the text, the span and the asset are copied
// out of the segment when the answer is written, because a re-ingest deletes and
// rewrites the segment rows a citation would otherwise point at.
func TestCitationSurvivesReingest(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	n := notebook(t, db, "A")
	c := chatIn(t, db, n, "q")

	db.AddSource(ctx, n, source("sha1", "paper.pdf"))
	if err := db.PutSegments(ctx, "sha1", []domain.Segment{{
		ID: "sha1:0000", SourceID: "sha1", DocID: "sha1", Kind: domain.KindText,
		Ordinal: 0, Text: "the first chunk",
		Span: domain.Provenance{Page: 7, CharStart: 1200, CharEnd: 1640},
	}}); err != nil {
		t.Fatal(err)
	}

	frozen, err := db.ListSegments(ctx, "sha1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AppendMessage(ctx, domain.Message{
		ChatID: c.ID, Role: domain.RoleAssistant,
		Citations: []domain.Citation{domain.CitationFrom(1, "paper.pdf", frozen[0])},
	}); err != nil {
		t.Fatal(err)
	}

	// What every later build of the store does to this source — new chunks, new
	// ids, the old rows gone.
	if err := db.PutSegments(ctx, "sha1", []domain.Segment{{
		ID: "sha1:0000", SourceID: "sha1", DocID: "sha1", Kind: domain.KindText,
		Ordinal: 0, Text: "rechunked differently",
	}}); err != nil {
		t.Fatal(err)
	}

	list, err := db.ListMessages(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Citations) != 1 {
		t.Fatalf("messages = %+v", list)
	}
	got := list[0].Citations[0]
	if got.Quote != "the first chunk" {
		t.Errorf("quote = %q, want the text as it was retrieved", got.Quote)
	}
	if got.Span.Page != 7 || got.Span.CharStart != 1200 || got.Span.CharEnd != 1640 {
		t.Errorf("span = %+v, want the page-local range the citation route is built from", got.Span)
	}
	if got.Chunk != 1 || got.SourceName != "paper.pdf" || got.SourceID != "sha1" {
		t.Errorf("citation = %+v", got)
	}
}

// A citation is stored as JSON, and an ordinal of zero is the only citation a
// one-segment document has — so a round-trip that dropped zero-valued fields
// would silently drop the reference the reader clicked. The asset must survive
// too, with its path kept out.
func TestCitationKeepsZeroOrdinal(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	n := notebook(t, db, "A")
	c := chatIn(t, db, n, "q")

	_, err := db.AppendMessage(ctx, domain.Message{
		ChatID: c.ID, Role: domain.RoleAssistant,
		Citations: []domain.Citation{{
			Chunk: 1, Segment: "sha2:0000", Kind: domain.KindImage, Ordinal: 0,
			Span:  domain.Provenance{Page: 0},
			Asset: &domain.Asset{ID: "sha2/pages/page-0001.png", MIME: "image/png", Bytes: 960},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	list, err := db.ListMessages(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	cit := list[0].Citations[0]
	if cit.Asset == nil || cit.Asset.ID == "" {
		t.Fatalf("asset = %+v, want the raster a citation opens", cit.Asset)
	}
	if cit.Asset.Path != "" {
		t.Errorf("asset path leaked: %q", cit.Asset.Path)
	}
	if cit.Ordinal != 0 || cit.Kind != domain.KindImage {
		t.Errorf("citation = %+v", cit)
	}
}

func TestAppendAndUpdateMessage(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	n := notebook(t, db, "A")
	c := chatIn(t, db, n, "q")

	// The assistant row exists before the answer streams, holding only its citations.
	row, err := db.AppendMessage(ctx, domain.Message{ChatID: c.ID, Role: domain.RoleAssistant})
	if err != nil {
		t.Fatal(err)
	}
	if row.ID == 0 || row.CreatedAt == 0 {
		t.Fatalf("row = %+v, want the id and timestamp filled in", row)
	}
	if mid, _ := db.ListMessages(ctx, c.ID); len(mid) != 1 || mid[0].Content != "" {
		t.Fatalf("mid-stream messages = %+v, want one empty row", mid)
	}

	if err := db.UpdateMessage(ctx, row.ID, domain.MessageUpdate{
		Content: "Smith measured 4.2 K [[Chunk 1]].",
		Status:  domain.MessageAnswered,
		Citations: []domain.Citation{{
			Chunk: 1, Segment: "sha1:0000", SourceID: "sha1", SourceName: "paper.pdf",
			Kind: domain.KindText, Quote: "Smith measured 4.2 K",
			Span: domain.Provenance{Page: 3, CharStart: 40, CharEnd: 63},
		}},
		Trace: `[{"role":"assistant"}]`,
	}); err != nil {
		t.Fatal(err)
	}

	list, err := db.ListMessages(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := list[0]
	if got.Status != domain.MessageAnswered {
		t.Errorf("status = %q", got.Status)
	}
	if len(got.Citations) != 1 || got.Citations[0].Chunk != 1 {
		t.Fatalf("citations = %+v", got.Citations)
	}

	// The transcript is stored, and it is not read back by the query that lists
	// messages — same reason the listing queries do not read vectors.
	if stored := mustText(t, db, fmt.Sprintf(`SELECT trace_json FROM messages WHERE id = %d`, row.ID)); stored == "" {
		t.Error("trace was not stored")
	}
	if got.Citations[0].Quote == "" {
		t.Error("quote lost")
	}

	if err := db.UpdateMessage(ctx, row.ID+1000, domain.MessageUpdate{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateMessage unknown id = %v, want ErrNotFound", err)
	}
}

func TestRecentMessagesSkipsExcludedAndOrders(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	n := notebook(t, db, "A")
	c := chatIn(t, db, n, "q")

	appended := []domain.Message{}
	for _, m := range []domain.Message{
		{Role: domain.RoleUser, Content: "q1"},
		{Role: domain.RoleAssistant, Content: "a1"},
		{Role: domain.RoleUser, Content: "q2"},
		{Role: domain.RoleAssistant, Content: "nothing on 2031", Status: domain.MessageNoInfo},
	} {
		m.ChatID = c.ID
		row, err := db.AppendMessage(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		appended = append(appended, row)

		if m.Role == domain.RoleAssistant && m.Status == domain.MessageNoInfo {
			if err := db.UpdateMessage(ctx, row.ID, domain.MessageUpdate{
				Content: m.Content, Status: m.Status,
			}); err != nil {
				t.Fatal(err)
			}
			// A turn is excluded whole — the question with its answer. Leaving "q2"
			// behind would keep the dead end while deleting only its outcome.
			if err := db.ExcludeMessages(ctx, appended[len(appended)-2].ID, row.ID); err != nil {
				t.Fatal(err)
			}
		}
	}

	got, err := db.RecentMessages(ctx, c.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	for _, m := range got {
		contents = append(contents, m.Content)
	}
	if strings.Join(contents, " ") != "q1 a1" {
		t.Errorf("history = %v, want the dead-end turn gone whole — question included", contents)
	}
	if got[0].Role != domain.RoleUser {
		t.Errorf("oldest = %+v, want the question first", got[0])
	}

	// A window takes the newest, but hands back the oldest first.
	window, err := db.RecentMessages(ctx, c.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 1 || window[0].Content != "a1" {
		t.Errorf("window = %+v, want [a1] — the last eligible turn, not the last row", window)
	}

	if none, _ := db.RecentMessages(ctx, c.ID, 0); none != nil {
		t.Errorf("limit 0 = %+v, want nothing rather than everything", none)
	}

	if err := db.ExcludeMessages(ctx); err != nil {
		t.Errorf("ExcludeMessages with no ids: %v", err)
	}
}

func TestNoCitationsIsNotAnError(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	n := notebook(t, db, "A")
	c := chatIn(t, db, n, "q")

	if _, err := db.AppendMessage(ctx, domain.Message{
		ChatID: c.ID, Role: domain.RoleUser, Content: "hi", Citations: []domain.Citation{},
	}); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListMessages(ctx, c.ID)
	if err != nil || len(list) != 1 || len(list[0].Citations) != 0 {
		t.Fatalf("list = %+v, err = %v", list, err)
	}

	// A nil slice is stored as "null" and an empty one as "[]"; both decode back to
	// nothing rather than an error, because which one a caller writes is not
	// something the reader should have to know.
	if _, err = db.AppendMessage(ctx, domain.Message{
		ChatID: c.ID, Role: domain.RoleUser, Content: "again",
	}); err != nil {
		t.Fatal(err)
	}
	if list, err = db.ListMessages(ctx, c.ID); err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
}
