package httpapi

// The machine endpoints: what engine build is on this disk, what upstream would offer, how a
// download is going, and what the machine has to run a model on.
//
// They are a separate file because they answer a different question from the rest of the API.
// Everything under /api/notebooks is about the user's documents; these are about the computer
// the app is on, and none of them touch the database.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"libris/internal/app"
	"libris/internal/deviceinfo"
	"libris/internal/engine"
)

// Host is the use case behind these routes, declared by the transport that calls it.
// app.Host satisfies it. It is nil-able like App: a build without an engine manager still
// serves everything else, and the settings page renders the section as unavailable rather
// than the app refusing to start.
type Host interface {
	Engine(ctx context.Context, force bool) (*app.EngineView, error)
	EngineInstall(ctx context.Context, tag string) (engine.Installed, error)
	EngineStop()
	EngineRemove(ctx context.Context) error
	EngineDiscardParts() error
	Devices(ctx context.Context, refresh bool) (deviceinfo.Report, error)
}

// maxInstallBody is the whole request: an optional tag. The cap exists so a handler that
// decodes a body cannot be used to make the process hold an arbitrary amount of memory.
const maxInstallBody = 1 << 10

func (s *Server) addHostRoutes(h Host) {
	if h == nil {
		return
	}
	s.host = h

	s.mux.HandleFunc("GET /api/host/engine", s.handleEngine)
	s.mux.HandleFunc("POST /api/host/engine", s.handleEngineInstall)
	s.mux.HandleFunc("POST /api/host/engine/stop", s.handleEngineStop)
	s.mux.HandleFunc("DELETE /api/host/engine", s.handleEngineRemove)
	s.mux.HandleFunc("DELETE /api/host/engine/parts", s.handleEngineParts)
	s.mux.HandleFunc("GET /api/host/devices", s.handleDevices)
}

// handleEngine is one round trip for the whole page: what is installed, what is new, how the
// download is going, and how much space the parts of a stopped one are holding.
//
// ?check=1 is "Check for updates" — it pays an API call now instead of accepting the cached
// answer. Every other visit is free, which is the reason there is a cache at all: an
// unauthenticated GitHub client gets sixty calls an hour per address.
func (s *Server) handleEngine(w http.ResponseWriter, r *http.Request) {
	view, err := s.host.Engine(r.Context(), r.URL.Query().Get("check") == "1")
	if err != nil {
		s.fail(w, "engine", err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// installRequest is the body. An empty tag means "the newest", which is the label on the
// button; the tag is resolved here rather than in a browser that cannot see the release list.
type installRequest struct {
	Tag string `json:"tag"`
}

// handleEngineInstall starts a download and answers as soon as it is underway.
//
// A 35 MB fetch over a slow link is minutes long, and a request held open for that long buys
// nothing: the progress endpoint already says where it is, and Stop is a different request
// that must be answerable while it runs. So the work is handed to a goroutine with its own
// context — hanging up does not cancel an install, because a half-written engine is useless
// either way and the bytes are resumable.
func (s *Server) handleEngineInstall(w http.ResponseWriter, r *http.Request) {
	var req installRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, maxInstallBody)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "body must be a JSON object with an optional tag")
			return
		}
	}
	req.Tag = strings.TrimSpace(req.Tag)
	if req.Tag != "" && !tagPattern.MatchString(req.Tag) {
		writeError(w, http.StatusBadRequest, "tag must look like a llama.cpp nightly, e.g. b12649")
		return
	}

	// The tag is resolved before the response, so a machine that cannot reach GitHub is told
	// that now, in the request that asked, rather than by a progress bar that never moves.
	if req.Tag == "" {
		view, err := s.host.Engine(r.Context(), false)
		if err != nil {
			s.fail(w, "engine", err)
			return
		}
		if view.Latest == nil {
			writeError(w, http.StatusBadGateway, "the newest build could not be determined: "+view.CheckError)
			return
		}
		req.Tag = view.Latest.Tag
	}

	go func() {
		// Not r.Context(): the download outlives this request by design. Stop is how it ends.
		if _, err := s.host.EngineInstall(context.Background(), req.Tag); err != nil {
			s.log.Warn("engine install failed", "tag", req.Tag, "err", err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"tag": req.Tag, "state": "downloading"})
}

// tagPattern is the shape of a llama.cpp nightly tag, repeated here rather than imported from
// engine so a request cannot probe the internals of that package. It is deliberately narrow:
// anything else is a path, not a version.
var tagPattern = regexp.MustCompile(`^b[0-9]{4,7}$`)

func (s *Server) handleEngineStop(w http.ResponseWriter, r *http.Request) {
	s.host.EngineStop()
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true})
}

// handleEngineRemove deletes the installed build.
//
// 409 is the interesting status: the engine refuses to delete files a running llama-server
// has open, because on Windows that leaves a half-tree that neither runs nor uninstalls.
// Refusing is the honest answer, and the message names the file it is holding.
func (s *Server) handleEngineRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.host.EngineRemove(r.Context()); err != nil {
		switch {
		case errors.Is(err, engine.ErrRunning), errors.Is(err, engine.ErrBusy):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, engine.ErrNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		default:
			s.fail(w, "engine", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

// handleEngineParts discards the bytes a stopped download left behind. Separate from the
// delete above because the two mean different things to the person who pressed them:
// removing an engine, and clearing space a cancelled one is still holding.
func (s *Server) handleEngineParts(w http.ResponseWriter, r *http.Request) {
	if err := s.host.EngineDiscardParts(); err != nil {
		s.fail(w, "engine", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"parts_removed": true})
}

// handleDevices reports the machine. The body is the stored deviceinfo.Report verbatim, so
// the same document a support request would carry is what the page renders.
//
// ?refresh=1 skips the cache. Without it a report is reused while its fingerprint still
// describes the machine, which keeps a subprocess off every settings visit.
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	rep, err := s.host.Devices(r.Context(), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		s.fail(w, "engine", err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
