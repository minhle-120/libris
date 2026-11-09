// Package httpapi is the browser transport: JSON endpoints, the embedded
// single-page app, and later SSE. It holds no business logic — handlers decode,
// call one thing, and encode.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"libris/internal/config"
)

const maxSettingsBody = 256 << 10

type Options struct {
	Settings *config.FileStore
	Assets   fs.FS // built frontend, rooted where index.html lives; may be nil
	Logger   *slog.Logger
}

type Server struct {
	opts Options
	log  *slog.Logger
	mux  *http.ServeMux
}

func New(opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	s := &Server{opts: opts, log: log, mux: http.NewServeMux()}

	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	s.mux.Handle("GET /", s.staticHandler())

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGetSettings returns the effective configuration.
func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.opts.Settings.Get())
}

// handlePutSettings applies a partial update: absent keys keep their current
// value, unknown keys are rejected.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxSettingsBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}

	next, err := s.opts.Settings.Apply(body)
	if err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			writeError(w, http.StatusBadRequest, "body is not valid JSON")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, next)
}

// staticHandler serves the built app, falling back to index.html so client-side
// navigation works on refresh.
func (s *Server) staticHandler() http.Handler {
	if s.opts.Assets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "no frontend embedded in this build")
		})
	}

	files := http.FileServerFS(s.opts.Assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(s.opts.Assets, name); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
