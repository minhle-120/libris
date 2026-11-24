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
	// EngineInstallStart begins a download and answers as soon as it is underway, with the tag
	// it settled on. An empty tag means "the newest", and the module behind this seam is what
	// turns that into a tag — the release list is not something a browser can see.
	EngineInstallStart(ctx context.Context, tag string) (string, error)
	EngineStop()
	EngineRemove(ctx context.Context) error
	EngineDiscardParts() error
	// EngineServeStart blocks until libris's own llama-server answers, which is the whole
	// first-load of a GGUF. The page polls GET /api/host/engine while it runs, so the wait
	// is the request and the state stays visible.
	EngineServeStart(ctx context.Context) (engine.Serving, error)
	// EngineServeStop answers with the state it left the process in, so the page that asked
	// does not have to ask again to find out what its own request did.
	EngineServeStop(ctx context.Context) (engine.Serving, error)
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

	// Everything about this request that is a decision — is that a tag, which tag does "the
	// newest" name, may a second download start on top of the first — belongs to the module
	// that owns the download, and is answered here as an error. What stays is the body and the
	// status. The goroutine used to live in this handler, pointed at context.Background()
	// because the download must outlive the request: that reason is the download's, not the
	// transport's, and it is the reason a half-written engine used to be a thing only the HTTP
	// layer could explain.
	tag, err := s.host.EngineInstallStart(r.Context(), req.Tag)
	if err != nil {
		s.failMachine(w, "engine install", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"tag": tag, "state": "downloading"})
}

// failMachine is the whole error-to-status mapping for the machine endpoints, written once.
// It was seven switches before, one per handler, and they drifted: a malformed tag was 400 in
// one and what ErrInvalid means elsewhere was 422 in another, and "the newest build could not
// be determined" was spelled three times. Every sentence a person can read is the app
// module's, which is where the failure was seen; what is decided here is only which status
// the failure deserves.
//
// This is deliberately not shared with the document endpoints. app.ErrInvalid is 400 there,
// where it means a body that could not be read, and 422 here, where it means a request that
// read fine and names something that cannot happen. Two callers, two answers, so two tables.
func (s *Server) failMachine(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, app.ErrUpdateChecking):
		// Not a failure and not an answer. The sentence says try again in a moment, so the
		// status must not read like the machine broke.
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, app.ErrUpdateUnknown):
		// The lookup was tried and failed; this is the one case where upstream is at fault.
		writeError(w, http.StatusBadGateway, err.Error())
	case errors.Is(err, app.ErrInvalid), errors.Is(err, app.ErrEngineMode):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, app.ErrBusy), errors.Is(err, engine.ErrBusy),
		errors.Is(err, app.ErrEngineRunning), errors.Is(err, app.ErrEngineStart),
		errors.Is(err, engine.ErrRunning):
		// Every one of these is "the world is not in the state this request assumed", and each
		// carries the sentence that says what to do about it: the other download, the port,
		// the model, the process that has the files open.
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, engine.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		s.fail(w, op, err)
	}
}

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
		s.failMachine(w, "engine remove", err)
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
		s.failMachine(w, "engine serve", err)
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

// handleEngineServeStop stops it. Deleting something that was never running is not a 404:
// the state the caller wanted is the state it gets.
func (s *Server) handleEngineServeStop(w http.ResponseWriter, r *http.Request) {
	srv, err := s.host.EngineServeStop(r.Context())
	if err != nil {
		s.failMachine(w, "engine stop", err)
		return
	}
	writeJSON(w, http.StatusOK, srv)
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
		s.failMachine(w, "model install", err)
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
		s.failMachine(w, "models", err)
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
		s.failMachine(w, "models", err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
