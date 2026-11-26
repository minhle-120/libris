package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"libris/internal/app"
	"libris/internal/domain"
)

// API is the use-case surface the browser needs. Declared by the transport so
// the handlers depend on behaviour rather than on app's concrete type, and so a
// build without a database can pass nil and serve only settings.
type API interface {
	CreateNotebook(context.Context, string) (domain.Notebook, error)
	RenameNotebook(context.Context, string, string) error
	DeleteNotebook(context.Context, string) error
	GetNotebook(context.Context, string) (domain.Notebook, error)
	ListNotebooks(context.Context) ([]domain.Notebook, error)

	AddUpload(context.Context, string, io.Reader, string) (domain.Source, error)
	DetachSource(context.Context, string, string) error
	GetSource(context.Context, string) (domain.Source, error)
	ListSources(context.Context, string) ([]domain.Source, error)
	ListSegments(context.Context, string) ([]domain.Segment, error)
	ReingestSource(context.Context, string) (domain.Source, error)
	StopSource(context.Context, string) (domain.Source, error)
	ResumeSource(context.Context, string) (domain.Source, error)
	GetAsset(context.Context, string) (domain.Asset, error)

	Chats(context.Context, string) ([]domain.Chat, error)
	NewChat(context.Context, string, string) (domain.Chat, error)
	RenameChat(context.Context, string, string, string) error
	DeleteChat(context.Context, string, string) error
	ChatMessages(context.Context, string, string) ([]domain.Message, error)
	Ask(context.Context, string, string, string, app.AskEvents) (domain.Message, error)
}

// handleStopSource cancels the work in flight for one source. The row comes back as it
// is at the moment of the ask, which is usually still "processing": the run notices the
// cancellation a heartbeat later and writes "stopped", and the UI is polling until it
// does. What the answer guarantees is that the work has been stopped, not that the row
// has caught up.
func (s *Server) handleStopSource(w http.ResponseWriter, r *http.Request) {
	source, err := s.app.StopSource(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "stop source", err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

// handleResumeSource puts a stopped source back on the queue. It answers 202 like the
// other calls that start work rather than report it, with the row now sitting at
// "processing" — which is the status the UI polls on, and the reason the row cannot be
// left showing "stopped" while work is happening.
func (s *Server) handleResumeSource(w http.ResponseWriter, r *http.Request) {
	source, err := s.app.ResumeSource(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "resume source", err)
		return
	}
	writeJSON(w, http.StatusAccepted, source)
}

// uploadHeadroom is how much of a multipart body is kept in memory before Go
// spills the rest to a temporary file. The file is streamed to its final home
// either way; this only keeps small uploads off the disk twice.
const uploadHeadroom = 4 << 20

// addAppRoutes registers the application endpoints.
func (s *Server) addAppRoutes(a API) {
	if a == nil {
		return
	}
	s.app = a

	s.mux.HandleFunc("POST /api/notebooks", s.handleCreateNotebook)
	s.mux.HandleFunc("GET /api/notebooks", s.handleListNotebooks)
	s.mux.HandleFunc("GET /api/notebooks/{id}", s.handleGetNotebook)
	s.mux.HandleFunc("PATCH /api/notebooks/{id}", s.handleRenameNotebook)
	s.mux.HandleFunc("DELETE /api/notebooks/{id}", s.handleDeleteNotebook)

	s.mux.HandleFunc("POST /api/notebooks/{id}/sources", s.handleUpload)
	s.mux.HandleFunc("GET /api/notebooks/{id}/sources", s.handleListSources)
	s.mux.HandleFunc("DELETE /api/notebooks/{id}/sources/{sourceID}", s.handleDetachSource)

	s.mux.HandleFunc("GET /api/sources/{id}", s.handleGetSource)
	s.mux.HandleFunc("GET /api/sources/{id}/segments", s.handleListSegments)
	s.mux.HandleFunc("POST /api/sources/{id}/reingest", s.handleReingestSource)
	s.mux.HandleFunc("POST /api/sources/{id}/stop", s.handleStopSource)
	s.mux.HandleFunc("POST /api/sources/{id}/resume", s.handleResumeSource)
	s.mux.HandleFunc("GET /api/sources/{id}/original", s.handleOriginal)

	// Asset ids contain slashes ("<sha>/pages/page-0001.png"), so this is the
	// one wildcard route. The id is looked up in the database and never
	// resolved as a filesystem path.
	s.mux.HandleFunc("GET /api/assets/{id...}", s.handleAsset)

	s.addChatRoutes()
}

// ── notebooks ────────────────────────────────────────────────────

func (s *Server) handleCreateNotebook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	notebook, err := s.app.CreateNotebook(r.Context(), body.Name)
	if err != nil {
		s.fail(w, "create notebook", err)
		return
	}
	writeJSON(w, http.StatusCreated, notebook)
}

func (s *Server) handleListNotebooks(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.ListNotebooks(r.Context())
	if err != nil {
		s.fail(w, "list notebooks", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetNotebook(w http.ResponseWriter, r *http.Request) {
	notebook, err := s.app.GetNotebook(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "get notebook", err)
		return
	}
	writeJSON(w, http.StatusOK, notebook)
}

func (s *Server) handleRenameNotebook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.RenameNotebook(r.Context(), r.PathValue("id"), body.Name); err != nil {
		s.fail(w, "rename notebook", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteNotebook(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteNotebook(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, "delete notebook", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── sources ──────────────────────────────────────────────────────

// handleUpload accepts one file under the multipart field "file" and answers 202
// as soon as the bytes are stored. Extraction takes minutes for a long PDF, so
// the caller polls the source's status instead of holding the connection.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	// The size cap is configuration, so this only bounds what is buffered.
	if err := r.ParseMultipartForm(uploadHeadroom); err != nil {
		writeError(w, http.StatusBadRequest, "a multipart form is required: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) || errors.Is(err, http.ErrNotMultipart) {
			writeError(w, http.StatusBadRequest, `a multipart field named "file" is required`)
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer file.Close()

	source, err := s.app.AddUpload(r.Context(), r.PathValue("id"), file, header.Filename)
	if err != nil {
		s.fail(w, "upload", err)
		return
	}
	// Same shape as GET /api/sources/{id}, so one client call reads both.
	writeJSON(w, http.StatusAccepted, source)
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.ListSources(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "list sources", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleDetachSource(w http.ResponseWriter, r *http.Request) {
	err := s.app.DetachSource(r.Context(), r.PathValue("id"), r.PathValue("sourceID"))
	if err != nil {
		s.fail(w, "detach source", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetSource(w http.ResponseWriter, r *http.Request) {
	source, err := s.app.GetSource(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "get source", err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

// handleReingestSource puts a source back through the pipeline from its kept
// original. 202, not 200: the call starts work rather than reporting a finished
// one, and the row it answers with is what the UI polls from.
func (s *Server) handleReingestSource(w http.ResponseWriter, r *http.Request) {
	source, err := s.app.ReingestSource(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "reingest source", err)
		return
	}
	writeJSON(w, http.StatusAccepted, source)
}

func (s *Server) handleListSegments(w http.ResponseWriter, r *http.Request) {
	segments, err := s.app.ListSegments(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "list segments", err)
		return
	}
	writeJSON(w, http.StatusOK, segments)
}

// ── stored files ─────────────────────────────────────────────────

// handleOriginal streams the kept copy, so the browser's own PDF and image
// viewers can open a citation. The path comes from the database row, never from
// the request.
func (s *Server) handleOriginal(w http.ResponseWriter, r *http.Request) {
	source, err := s.app.GetSource(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "get source", err)
		return
	}
	s.serveFile(w, r, source.OriginalPath, source.Name, mimeByFilename(source.Name))
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	// For a {id...} wildcard the value is read under the bare name, and it
	// re-joins with the slashes intact.
	asset, err := s.app.GetAsset(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, "get asset", err)
		return
	}
	s.serveFile(w, r, asset.Path, filepath.Base(asset.ID), asset.MIME)
}

// serveFile streams a stored file. http.ServeContent handles Range, which is what
// lets a PDF viewer render page 40 without fetching the whole document.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, filePath, name, contentType string) {
	f, err := os.Open(filePath)
	if err != nil {
		// The row outlived the file. Report that plainly: the client sent no
		// path, so a bare 404 would be a dead end.
		s.log.Warn("stored file missing", "path", filePath, "err", err)
		writeError(w, http.StatusNotFound, "the stored file is missing from disk")
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "the stored file is missing from disk")
		return
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", inlineDisposition(name))
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// inlineDisposition keeps PDFs and images rendering in the tab while still
// naming the file for downloads. Quotes and newlines are stripped because the
// name came from a client.
func inlineDisposition(name string) string {
	clean := strings.NewReplacer(`"`, "", "\r", "", "\n", "").Replace(name)
	if clean == "" {
		clean = "file"
	}
	return `inline; filename="` + clean + `"`
}

// mimeByFilename covers originals, which have no MIME recorded in the database.
// The fallbacks are the types the stdlib table does not know.
func mimeByFilename(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if got := mime.TypeByExtension(ext); got != "" {
		return got
	}
	if got := fallbackMIME[ext]; got != "" {
		return got
	}
	return "application/octet-stream"
}

var fallbackMIME = map[string]string{
	".md":   "text/markdown; charset=utf-8",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".opus": "audio/ogg",
	".ogg":  "audio/ogg",
	".txt":  "text/plain; charset=utf-8",
	".csv":  "text/csv; charset=utf-8",
}

// ── shared ───────────────────────────────────────────────────────

// decodeJSON reads a small JSON body into target.
func decodeJSON(r *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSettingsBody))
	if err != nil {
		return errors.New("could not read the request body")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return errors.New("a JSON body is required")
	}
	if err := json.Unmarshal(body, target); err != nil {
		return errors.New("that is not JSON")
	}
	return nil
}

// fail maps a use-case error to a status. Anything the user can cause is 4xx;
// the rest is ours, and gets logged rather than described.
func (s *Server) fail(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "nothing with that id")
	case errors.Is(err, app.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(5))
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, app.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, app.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.log.Error(what, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
