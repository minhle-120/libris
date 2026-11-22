package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"libris/internal/app"
	"libris/internal/domain"
)

// The transport is tested against a fake API and asserts only the HTTP mapping: what
// a turn contains is the app's business, and what the browser receives is this file's.

func chatServer(t *testing.T, f *fakeAPI) *Server {
	t.Helper()
	if f == nil {
		f = &fakeAPI{}
	}
	return New(Options{
		Settings: settingsStore(t),
		App:      f,
		Logger:   slog.New(slog.NewTextHandler(nopWriter{}, nil)),
	})
}

// ── the fake's chat half (fakeAPI itself lives in sources_test.go) ──

// askScript is what the fake turn emits, in order.
type askScript struct {
	statuses  []string
	citations []domain.Citation
	deltas    [][2]string

	err error
	msg domain.Message
}

func (f *fakeAPI) Chats(context.Context, string) ([]domain.Chat, error) {
	return f.chats, f.err
}

func (f *fakeAPI) NewChat(_ context.Context, notebookID, title string) (domain.Chat, error) {
	f.chatTitle = title
	f.notebookOf = notebookID
	if f.err != nil {
		return domain.Chat{}, f.err
	}
	return domain.Chat{ID: "chat-1", NotebookID: notebookID, Title: title, CreatedAt: 1}, nil
}

func (f *fakeAPI) RenameChat(_ context.Context, notebookID, chatID, title string) error {
	f.notebookOf, f.chatID, f.chatTitle = notebookID, chatID, title
	return f.err
}

func (f *fakeAPI) DeleteChat(_ context.Context, notebookID, chatID string) error {
	f.notebookOf, f.chatID = notebookID, chatID
	return f.err
}

func (f *fakeAPI) ChatMessages(_ context.Context, notebookID, chatID string) ([]domain.Message, error) {
	f.notebookOf, f.chatID = notebookID, chatID
	return f.messages, f.err
}

func (f *fakeAPI) Ask(ctx context.Context, notebookID, chatID, question string, ev app.AskEvents) (domain.Message, error) {
	f.notebookOf, f.chatID, f.question = notebookID, chatID, question

	s := f.script
	for _, text := range s.statuses {
		emit(ev.Status, text)
	}
	if len(s.citations) > 0 {
		emitCitations(ev.Citations, s.citations)
	}
	for _, d := range s.deltas {
		emitDelta(ev.Delta, d[0], d[1])
	}

	if ctx.Err() != nil {
		return domain.Message{}, ctx.Err()
	}
	if s.err != nil {
		return domain.Message{}, s.err
	}
	return s.msg, nil
}

// emit calls an optional callback. The fake would otherwise be asserting the shape of
// its own script rather than the server's handling of a nil-tolerant event set.
func emit(fn func(string), text string) {
	if fn != nil {
		fn(text)
	}
}

func emitCitations(fn func([]domain.Citation), list []domain.Citation) {
	if fn != nil {
		fn(list)
	}
}

func emitDelta(fn func(text, reasoning string), text, reasoning string) {
	if fn != nil {
		fn(text, reasoning)
	}
}

func citationsForTest() []domain.Citation {
	return []domain.Citation{{
		Chunk: 1, Segment: "sha1:0000", SourceID: "sha1", SourceName: "paper.pdf",
		Kind: domain.KindText, Quote: "Smith measured 4.2 K.",
		Span:  domain.Provenance{Page: 7, CharStart: 1200, CharEnd: 1640},
		Asset: &domain.Asset{ID: "sha1/pages/page-0007.png", MIME: "image/png", Bytes: 4096},
	}}
}

// ── tests ────────────────────────────────────────────────────────

func TestChatCollectionRoutes(t *testing.T) {
	f := &fakeAPI{
		chats:    []domain.Chat{{ID: "c1", NotebookID: "nb", Title: "What did Smith measure?", CreatedAt: 1}},
		messages: []domain.Message{{ID: 1, ChatID: "c1", Role: domain.RoleUser, Content: "hi", CreatedAt: 1}},
	}
	srv := chatServer(t, f)

	rec := do(t, srv, "GET", "/api/notebooks/nb/chats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list chats = %d", rec.Code)
	}
	var chats []domain.Chat
	if err := json.Unmarshal(rec.Body.Bytes(), &chats); err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Title != "What did Smith measure?" {
		t.Errorf("chats = %+v", chats)
	}

	rec = do(t, srv, "POST", "/api/notebooks/nb/chats", `{"title":"New thread"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create chat = %d, want 201", rec.Code)
	}
	if f.chatTitle != "New thread" || f.notebookOf != "nb" {
		t.Errorf("use case saw (%q, %q)", f.notebookOf, f.chatTitle)
	}
	var created domain.Chat
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Errorf("created = %+v, err %v — the UI needs the id to ask in it", created, err)
	}

	rec = do(t, srv, "GET", "/api/notebooks/nb/chats/c1/messages", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d", rec.Code)
	}
	var msgs []domain.Message
	if err := json.Unmarshal(rec.Body.Bytes(), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hi" {
		t.Errorf("messages = %+v", msgs)
	}
	if f.notebookOf != "nb" || f.chatID != "c1" {
		t.Errorf("scope = (%q, %q)", f.notebookOf, f.chatID)
	}

	rec = do(t, srv, "DELETE", "/api/notebooks/nb/chats/c1", "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete chat = %d, want 204", rec.Code)
	}
}

// Renaming answers nothing. The caller typed the title, so a body would only be a
// second place the same string has to be right — and the notebook and chat ids have to
// reach the use case together, or a chat in one notebook could be renamed through
// another.
func TestChatRenameRoute(t *testing.T) {
	f := &fakeAPI{}
	rec := do(t, chatServer(t, f), "PATCH", "/api/notebooks/nb/chats/c1", `{"title":"Why the tail is long"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("rename chat = %d, want 204", rec.Code)
	}
	if f.notebookOf != "nb" || f.chatID != "c1" || f.chatTitle != "Why the tail is long" {
		t.Errorf("use case saw (%q, %q, %q)", f.notebookOf, f.chatID, f.chatTitle)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a rename answered %q, want an empty body", rec.Body.String())
	}
}

// A chat from another notebook reads as absent, which is what the use case reports; the
// transport must not turn it into a 500.
func TestChatErrorsMapToStatuses(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		err                error
		want               int
	}{
		{"GET", "/api/notebooks/nb/chats/nope/messages", "", domain.ErrNotFound, http.StatusNotFound},
		{"DELETE", "/api/notebooks/nb/chats/nope", "", domain.ErrNotFound, http.StatusNotFound},
		{"PATCH", "/api/notebooks/nb/chats/nope", `{"title":"x"}`, domain.ErrNotFound, http.StatusNotFound},
		// A title of nothing but spaces is the reader's mistake, and answers like one.
		{"PATCH", "/api/notebooks/nb/chats/c1", `{"title":"  "}`, app.ErrInvalid, http.StatusBadRequest},
		{"GET", "/api/notebooks/nb/chats/nope/messages", "", errors.New("disk on fire"), http.StatusInternalServerError},
	} {
		f := &fakeAPI{err: tc.err}
		rec := do(t, chatServer(t, f), tc.method, tc.path, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d (%s)", tc.method, tc.path, rec.Code, tc.want, tc.err)
		}
	}

	// A body that is not JSON, and a chat created with no title at all.
	f := &fakeAPI{}
	if rec := do(t, chatServer(t, f), "POST", "/api/notebooks/nb/chats", "not json"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad body = %d, want 400", rec.Code)
	}
}

// The frame order is the contract: progress, then the sources, then the prose. A
// client that learned the citations after reading the answer could not have shown
// them while it was being written.
func TestAskStreamsFramesInOrder(t *testing.T) {
	f := &fakeAPI{script: askScript{
		statuses:  []string{"Starting research…", "Searching…"},
		citations: citationsForTest(),
		deltas:    [][2]string{{"Smith measured ", ""}, {"4.2 K [[Chunk 1]].", ""}},
		msg: domain.Message{
			ID: 9, ChatID: "c1", Role: domain.RoleAssistant,
			Content: "Smith measured 4.2 K [[Chunk 1]].", Status: domain.MessageAnswered,
			Citations: citationsForTest(), CreatedAt: 2,
		},
	}}
	rec := do(t, chatServer(t, f), "POST", "/api/notebooks/nb/ask", `{"question":"What did Smith measure?","chat_id":"c1"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("ask = %d, want 200 before any of it has happened", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type = %q", ct)
	}
	if f.notebookOf != "nb" || f.chatID != "c1" || f.question != "What did Smith measure?" {
		t.Errorf("use case saw (%q, %q, %q)", f.notebookOf, f.chatID, f.question)
	}

	body := rec.Body.String()
	names := frames(t, body)
	want := []string{"status", "status", "citations", "delta", "delta", "done"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("frames = %v, want %v\n%s", names, want, body)
	}

	if pos := strings.Index(body, "event: citations"); pos < 0 ||
		strings.Index(body, "event: delta") < pos {
		t.Error("an answer arrived before the evidence it rests on")
	}

	// The citation frame carries what the route is built from, and no absolute path.
	cit := frameData(t, body, "citations")
	var list []domain.Citation
	if err := json.Unmarshal(cit, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Chunk != 1 || list[0].Span.Page != 7 || list[0].Span.CharStart != 1200 {
		t.Fatalf("citations = %+v", list)
	}
	if list[0].Asset == nil || list[0].Asset.ID == "" {
		t.Errorf("asset = %+v, want the file a citation opens", list[0].Asset)
	}
	if strings.Contains(body, "/data/") || strings.Contains(body, `"path"`) {
		t.Error("an absolute path reached the browser")
	}

	status := frameData(t, body, "status")
	var s struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(status, &s); err != nil || s.Text != "Starting research…" {
		t.Errorf("status frame = %s", status)
	}

	delta := frameData(t, body, "delta")
	if err := json.Unmarshal(delta, &s); err != nil || s.Text != "Smith measured " {
		t.Errorf("delta frame = %s", delta)
	}

	done := frameData(t, body, "done")
	var msg domain.Message
	if err := json.Unmarshal(done, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.ID != 9 || msg.Status != domain.MessageAnswered || len(msg.Citations) != 1 {
		t.Errorf("done = %+v, want the finished row so the UI can reconcile with the server", msg)
	}

	// Every frame is terminated by a blank line, which is what lets a reader split on
	// it rather than guess where one event ends.
	for _, frame := range strings.Split(strings.TrimRight(body, "\n"), "\n\n") {
		if !strings.HasPrefix(frame, "event: ") || !strings.Contains(frame, "\ndata: ") {
			t.Errorf("malformed frame: %q", frame)
		}
	}
}

// Reasoning travels apart. A model that thinks out loud is worth showing as progress
// and must never be appended to the answer.
func TestAskKeepsReasoningApart(t *testing.T) {
	f := &fakeAPI{script: askScript{
		deltas: [][2]string{{"", "counting the pages…"}, {"There are seven.", ""}},
		msg:    domain.Message{ID: 1, Content: "There are seven.", Status: domain.MessageAnswered},
	}}
	body := do(t, chatServer(t, f), "POST", "/api/notebooks/nb/ask", `{"question":"how many pages?","chat_id":"c1"}`).Body.String()

	frames := frames(t, body)
	if len(frames) != 3 || frames[0] != "delta" {
		t.Fatalf("frames = %v", frames)
	}
	var d struct {
		Text      string `json:"text"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal(frameData(t, body, "delta"), &d); err != nil {
		t.Fatal(err)
	}
	if d.Text != "" || d.Reasoning != "counting the pages…" {
		t.Errorf("first delta = %+v, want the reasoning on its own", d)
	}
}

// There is no status code left to set once the stream has opened, so a failed turn is
// reported in band — and a client that rendered the frames it got must not be left
// staring at a half answer with no explanation.
func TestAskReportsFailureInBand(t *testing.T) {
	f := &fakeAPI{script: askScript{
		statuses: []string{"Searching…"},
		err:      app.ErrInvalid,
	}}
	body := do(t, chatServer(t, f), "POST", "/api/notebooks/nb/ask", `{"question":"anything","chat_id":"c1"}`).Body.String()

	names := frames(t, body)
	if names[len(names)-1] != "error" {
		t.Fatalf("frames = %v, want the turn to end with an error", names)
	}
	if strings.Contains(body, "event: done") {
		t.Error("a failed turn also reported that it finished")
	}

	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(frameData(t, body, "error"), &payload); err != nil || payload.Error == "" {
		t.Errorf("error frame = %s", frameData(t, body, "error"))
	}
}

// A question that names no chat is the use case's to place, and the empty id is what
// says so. It stays empty rather than being invented here because the chat that gets
// created is titled from the question, and a client that made the row first would have
// to duplicate that rule to give it a name worth reading. The id comes back inside the
// done frame's row, which is how the browser learns which line just appeared in its list.
func TestAskWithNoChatIDRevealsTheOneItCreated(t *testing.T) {
	f := &fakeAPI{script: askScript{
		deltas: [][2]string{{"Because of the long tail.", ""}},
		msg: domain.Message{
			ID: 3, ChatID: "fresh", Role: domain.RoleAssistant,
			Content: "Because of the long tail.", Status: domain.MessageAnswered, CreatedAt: 2,
		},
	}}
	rec := do(t, chatServer(t, f), "POST", "/api/notebooks/nb/ask", `{"question":"Why the long tail?"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ask = %d, want 200", rec.Code)
	}
	if f.notebookOf != "nb" || f.chatID != "" {
		t.Errorf("use case saw (%q, %q), want the notebook and an empty chat", f.notebookOf, f.chatID)
	}

	var msg domain.Message
	if err := json.Unmarshal(frameData(t, rec.Body.String(), "done"), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.ChatID != "fresh" {
		t.Errorf("done carries chat %q, want the id of the chat this turn created", msg.ChatID)
	}
}

// A turn the client stopped is not a failure to report: the reader is by definition
// gone, and there is nothing to gain from writing a frame nobody will read.
func TestAskStopsWritingAfterTheReaderGoes(t *testing.T) {
	f := &fakeAPI{script: askScript{
		statuses: []string{"Searching…"},
		err:      context.Canceled,
	}}
	srv := chatServer(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/api/notebooks/nb/ask",
		strings.NewReader(`{"question":"what now"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	cancel() // the reader hung up before the turn began
	srv.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "event: error") {
		t.Errorf("a stopped turn reported an error:\n%s", body)
	}
}

// A question with no words in it never opens a stream: it is a bad request, and that
// deserves a status code the browser can read rather than an event nobody asked for.
func TestAskRefusesAnEmptyQuestionBeforeStreaming(t *testing.T) {
	srv := chatServer(t, &fakeAPI{})
	rec := do(t, srv, "POST", "/api/notebooks/nb/ask", `{"question":"   ","chat_id":"c1"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty question = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content type = %q, want a normal JSON error", ct)
	}
	if strings.Contains(rec.Body.String(), "event:") {
		t.Error("an error response opened an event stream")
	}
}

// A build without a database still serves settings and the UI, so the chat routes must
// simply not exist rather than answer with a nil dereference.
func TestChatRoutesAbsentWithoutAnApp(t *testing.T) {
	srv := New(Options{Settings: settingsStore(t)})
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/notebooks/nb/chats"},
		{"GET", "/api/notebooks/nb/chats/c1/messages"},
		{"POST", "/api/notebooks/nb/ask"},
	} {
		rec := do(t, srv, tc.method, tc.path, `{"question":"hi"}`)
		if rec.Code == http.StatusOK || rec.Code == http.StatusNoContent {
			t.Errorf("%s %s = %d, want the route to be absent", tc.method, tc.path, rec.Code)
		}
	}
}

// ── frame parsing ────────────────────────────────────────────────

// frames lists the event names in the order they appeared.
func frames(t *testing.T, body string) []string {
	t.Helper()
	var names []string
	for _, frame := range splitFrames(body) {
		names = append(names, eventName(t, frame))
	}
	return names
}

func splitFrames(body string) []string {
	trimmed := strings.TrimRight(body, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n\n")
}

func eventName(t *testing.T, frame string) string {
	t.Helper()
	line := strings.SplitN(frame, "\n", 2)[0]
	name, ok := strings.CutPrefix(line, "event: ")
	if !ok {
		t.Fatalf("frame does not start with an event name: %q", frame)
	}
	return name
}

func frameData(t *testing.T, body, want string) []byte {
	t.Helper()
	for _, frame := range splitFrames(body) {
		if eventName(t, frame) != want {
			continue
		}
		_, data, ok := strings.Cut(frame, "\ndata: ")
		if !ok {
			t.Fatalf("frame has no data: %q", frame)
		}
		return []byte(strings.TrimSuffix(data, "\n"))
	}
	t.Fatalf("no %q frame in:\n%s", want, body)
	return nil
}
