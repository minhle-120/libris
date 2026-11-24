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
	// EngineServeStart blocks until libris's own llama-server answers, which is the whole
	// first-load of a GGUF. The page polls GET /api/host/engine while it runs, so the wait
	// is the request and the state stays visible.
	EngineServeStart(ctx context.Context) (engine.Serving, error)
	EngineServeStop(ctx context.Context) error
	ServeStatus() engine.Serving
	Devices(ctx context.Context, refresh bool) (deviceinfo.Report, error)

	// The models the catalogue offers, and the four things a person can do to one. Each of
	// the answering calls returns the whole view, so a 202 carries the state it just caused
	// rather than an acknowledgment the client has to go and re-read.
	ModelCatalog() *app.ModelsView
	ModelInstall(id string) (*app.ModelsView, error)
	ModelStop() *app.ModelsView
	ModelDiscard(id string) (*app.ModelsView, error)
	ModelRemove(id string) (*app.ModelsView, error)
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
	s.mux.HandleFunc("POST /api/host/engine/serve", s.handleEngineServe)
	s.mux.HandleFunc("DELETE /api/host/engine/serve", s.handleEngineServeStop)
	s.mux.HandleFunc("GET /api/host/devices", s.handleDevices)

	s.mux.HandleFunc("GET /api/host/models", s.handleModels)
	s.mux.HandleFunc("POST /api/host/models", s.handleModelInstall)
	s.mux.HandleFunc("POST /api/host/models/stop", s.handleModelStop)
	s.mux.HandleFunc("POST /api/host/models/discard", s.handleModelDiscard)
	s.mux.HandleFunc("DELETE /api/host/models/{id}", s.handleModelRemove)
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
			switch {
			case view.Checking:
				// Nothing has failed. The answer has not arrived yet, and a machine that is
				// told "could not be determined" here will go and press the same button again
				// while the page is still waiting — which is the opposite of what a person
				// watching a spinner wants to read.
				writeError(w, http.StatusConflict, "libris is still finding out which build is newest; try again in a moment")
			case view.CheckError != "":
				writeError(w, http.StatusBadGateway, "the newest build could not be determined: "+view.CheckError)
			default:
				// No answer, no check in flight, no reason given: the view is inconsistent,
				// and an error message that ends in a colon and a space is worse than no
				// sentence at all.
				writeError(w, http.StatusBadGateway, "the newest build could not be determined")
			}
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

// handleEngineServe runs the router. Everything about the answer is the process's
// own: the port it bound, the models it took, and — when it would not come up — the sentence
// it wrote to its log, which app.Host already put in Serving.Error.
func (s *Server) handleEngineServe(w http.ResponseWriter, r *http.Request) {
	srv, err := s.host.EngineServeStart(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, app.ErrEngineMode), errors.Is(err, app.ErrInvalid):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, app.ErrEngineRunning), errors.Is(err, app.ErrEngineStart):
			// Both are "the world is not in the state this request assumed", which is what 409
			// means, and both carry a sentence about the port or the model to fix.
			writeError(w, http.StatusConflict, err.Error())
		default:
			s.fail(w, "engine", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

// handleEngineServeStop stops it. Deleting something that was never running is not a 404:
// the state the caller wanted is the state it gets.
func (s *Server) handleEngineServeStop(w http.ResponseWriter, r *http.Request) {
	if err := s.host.EngineServeStop(r.Context()); err != nil {
		if errors.Is(err, app.ErrEngineMode) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		s.fail(w, "engine", err)
		return
	}
	writeJSON(w, http.StatusOK, s.host.ServeStatus())
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

// ── the model catalogue ──────────────────────────────────────────────────────

// handleModels is the card's whole read: every entry the catalogue offers, which of its files
// are here, how much a stopped download is holding, and what the running server has loaded.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.host.ModelCatalog())
}

// modelRequest names one catalogue entry. The id is the only handle the page has, and it is
// the server that says whether it names anything — a frontend cannot make an entry exist.
type modelRequest struct {
	ID string `json:"id"`
}

// modelID reads the id out of a body, or answers false. The caller writes the 400: every one
// of these handlers wants the same sentence, and a request that names nothing is a client bug
// rather than a user's mistake.
func modelID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req modelRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, maxInstallBody)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "body must be a JSON object with an id")
			return "", false
		}
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeError(w, http.StatusBadRequest, "this request names no model")
		return "", false
	}
	return id, true
}

// handleModelInstall starts a download and answers immediately.
//
// This is not a long request held open for a gigabyte and a half: the only way to cancel is a
// different endpoint, and if the download rode this request then a laptop closing its lid, a
// proxy idle-timeout or a closed tab would be the act that stopped it. So the work runs with
// its own context and the answer is the state this request caused.
func (s *Server) handleModelInstall(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	view, err := s.host.ModelInstall(id)
	if err != nil {
		switch {
		case errors.Is(err, app.ErrBusy):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, app.ErrInvalid):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		default:
			s.fail(w, "models", err)
		}
		return
	}
	// The bytes arrive after this response. What the answer carries is the row that says so.
	writeJSON(w, http.StatusAccepted, view)
}

// handleModelStop interrupts the running download. The bytes stay: stop is "not now", and
// handleModelDiscard is the button that means the other thing.
func (s *Server) handleModelStop(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.host.ModelStop())
}

func (s *Server) handleModelDiscard(w http.ResponseWriter, r *http.Request) {
	id, ok := modelID(w, r)
	if !ok {
		return
	}
	view, err := s.host.ModelDiscard(id)
	if err != nil {
		s.reportModel(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleModelRemove deletes an entry's files. 409 is the interesting answer: the running
// server has the model open, and pulling the ground out from under a process is not what a
// delete button means.
func (s *Server) handleModelRemove(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "this request names no model")
		return
	}
	view, err := s.host.ModelRemove(id)
	if err != nil {
		s.reportModel(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// reportModel maps the two statuses a model call can mean on their own, and hands the rest to
// the shared mapping.
func (s *Server) reportModel(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrBusy):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, app.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		s.fail(w, "models", err)
	}
}
