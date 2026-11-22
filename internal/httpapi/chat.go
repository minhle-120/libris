package httpapi

// The conversation endpoints, including the only streaming response in the app.
//
// A question is answered over SSE rather than a request and a response because a
// local model takes tens of seconds, and the user needs three things during that
// time: proof that something is happening, the sources the answer will rest on before
// the prose arrives, and the prose as it is written. A turn also has to be stoppable,
// and the cheapest honest way to do that is to make the client's own disconnect the
// cancellation — closing the stream ends the turn, with no second endpoint to call
// and no orphaned "stop" button to get out of sync.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"libris/internal/app"
	"libris/internal/domain"
)

func (s *Server) addChatRoutes() {
	s.mux.HandleFunc("GET /api/notebooks/{id}/chats", s.handleListChats)
	s.mux.HandleFunc("POST /api/notebooks/{id}/chats", s.handleCreateChat)
	s.mux.HandleFunc("PATCH /api/notebooks/{id}/chats/{chatID}", s.handleRenameChat)
	s.mux.HandleFunc("DELETE /api/notebooks/{id}/chats/{chatID}", s.handleDeleteChat)
	s.mux.HandleFunc("GET /api/notebooks/{id}/chats/{chatID}/messages", s.handleListMessages)
	s.mux.HandleFunc("POST /api/notebooks/{id}/chats/{chatID}/ask", s.handleAsk)
}

func (s *Server) handleListChats(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Chats(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "list chats", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	chat, err := s.app.NewChat(r.Context(), r.PathValue("id"), body.Title)
	if err != nil {
		s.fail(w, "create chat", err)
		return
	}
	writeJSON(w, http.StatusCreated, chat)
}

// handleRenameChat takes a title from the reader. 204, not the chat back: the caller
// typed the title, so there is nothing to learn — and a body would be a second place
// the same string has to be right.
func (s *Server) handleRenameChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.RenameChat(r.Context(), r.PathValue("id"), r.PathValue("chatID"), body.Title); err != nil {
		s.fail(w, "rename chat", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteChat(r.Context(), r.PathValue("id"), r.PathValue("chatID")); err != nil {
		s.fail(w, "delete chat", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.ChatMessages(r.Context(), r.PathValue("id"), r.PathValue("chatID"))
	if err != nil {
		s.fail(w, "list messages", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleAsk runs one question and streams what happens.
//
// The frames, in the order the turn produces them:
//
//	event: status      {"text": "Searching…"}                 progress, never part of the answer
//	event: citations   [{…}]                                  once, before the answer streams
//	event: delta       {"text": "…", "reasoning": "…"}        the answer arriving
//	event: done        {message}                              the row as stored
//	event: error       {"error": "…"}                         the turn failed
//
// Two things this makes deliberate. Citations arrive before the prose, so the UI can
// show what an answer is resting on while it is still being written — and because
// they are persisted first, they would have arrived even if the process died mid-
// answer. And reasoning is a separate field on the delta: a model that thinks out loud
// is worth showing as progress, and worth never mistaking for the answer.
//
// After the stream opens there is no status code left to set, so a failure is reported
// in band. Before it opens, the usual mapping applies.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Question string `json:"question"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Question) == "" {
		writeError(w, http.StatusBadRequest, "a question needs words in it")
		return
	}

	// The request's context is the cancellation. When the browser closes the stream —
	// stop button, navigated away, laptop lid — it ends, and the turn ends with it.
	ctx := r.Context()

	out, ok := openStream(w, ctx)
	if !ok {
		// No streaming support means no way to answer this properly; say so before
		// committing the client to an event stream that would never end.
		writeError(w, http.StatusInternalServerError, "this server cannot stream responses")
		return
	}

	msg, askErr := s.app.Ask(ctx, r.PathValue("id"), r.PathValue("chatID"), body.Question, app.AskEvents{
		Status: func(text string) {
			out.event("status", map[string]string{"text": text})
		},
		Citations: func(list []domain.Citation) {
			out.event("citations", list)
		},
		Delta: func(text, reasoning string) {
			out.event("delta", map[string]string{"text": text, "reasoning": reasoning})
		},
	})

	if askErr != nil {
		// A cancelled turn is not a failure to report: the client asked for it and is,
		// by definition, no longer there to be told.
		if !out.alive() {
			return
		}
		s.log.Info("question failed", "notebook", r.PathValue("id"), "err", askErr)
		out.event("error", map[string]string{"error": askErr.Error()})
		return
	}
	out.event("done", msg)
}

// ── the stream itself ────────────────────────────────────────────

// eventStream writes SSE frames and notices when the reader has gone.
type eventStream struct {
	w       http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context
}

// openStream commits the response to an event stream. It reports false when the
// ResponseWriter cannot be flushed, which is a server that cannot answer a question
// at all rather than one that answers slowly.
func openStream(w http.ResponseWriter, ctx context.Context) (*eventStream, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// Loopback, so there is no proxy to disable — but a user who puts libris behind
	// one should not have to discover that their answers arrive all at once.
	h.Set("X-Accel-Buffering", "no")

	flusher.Flush() // the headers are the opening of the stream; do not sit on them
	return &eventStream{w: w, flusher: flusher, ctx: ctx}, true
}

func (e *eventStream) event(name string, payload any) {
	if !e.alive() {
		return
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		// A payload this package built cannot fail to encode; if it ever does, the
		// answer is already stored and a broken frame must not destroy the stream.
		return
	}

	// One frame per write, and flushed: a buffered event is an answer the user has
	// not been given yet.
	if _, err := e.w.Write([]byte("event: " + name + "\ndata: " + string(raw) + "\n\n")); err != nil {
		e.ctx = nil // the reader is gone; nothing else gets through
		return
	}
	e.flusher.Flush()
}

// alive reports whether there is still anyone to write to. Both the client hanging up
// and the request ending count, and after either there is nothing to gain by trying.
func (e *eventStream) alive() bool {
	return e.ctx != nil && e.ctx.Err() == nil
}
