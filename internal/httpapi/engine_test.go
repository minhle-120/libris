package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"libris/internal/app"
	"libris/internal/deviceinfo"
	"libris/internal/engine"
)

// fakeHost is app.Host with no disk, no network and no GPU, so what is under test is the
// mapping from a use case to a status code and a body — nothing else.
type fakeHost struct {
	serving      engine.Serving
	serveErr     error
	serveStopErr error
	served       int
	stoppedServe int
	view         *app.EngineView
	viewErr      error
	forced       bool
	installs     chan string // the tag each install was started with
	removeErr    error
	installed    engine.Installed

	refreshed bool
	devices   deviceinfo.Report

	stopped, discarded int

	// The model half: the view every answering call returns, and what each use case was told.
	modelsView     *app.ModelsView
	installID      string
	installErr     error
	removed        []string
	modelRemoveErr error
	discarded2     []string
	modelStops     int
	// engineErr is what starting a download answers with, when the test wants the refusal
	// rather than the download. The deciding of which refusal, and why, moved to
	// internal/app: see TestEngineInstallStartRefusesWhatItCannotFetch.
	engineErr error
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		view: &app.EngineView{
			Backend: "vulkan",
			Dir:     "/data/bin",
			Latest:  &engine.Release{Tag: "b12649"},
			Asset:   &engine.Asset{Name: "llama-b12649-bin-ubuntu-vulkan-x64.tar.gz", Size: 35 << 20},
		},
		installs:  make(chan string, 4),
		installed: engine.Installed{Tag: "b12649", Backend: engine.Vulkan},
		devices: deviceinfo.Report{
			OS: "linux", Arch: "amd64",
			Devices: []deviceinfo.Device{{Kind: "nvidia", Name: "Vulkan0", Description: "NVIDIA GeForce RTX 3070"}},
		},
	}
}

func (f *fakeHost) Engine(ctx context.Context, force bool) (*app.EngineView, error) {
	f.forced = force
	if f.viewErr != nil {
		return nil, f.viewErr
	}
	return f.view, nil
}

// EngineInstallStart records the tag it was handed and answers with the tag a machine module
// would have settled on — which is the point of the seam: an empty ask is not a bug in the
// route, it is "the newest", and only the thing that can see the release list knows that tag.
// A refusal records nothing, because a start that was refused started nothing.
func (f *fakeHost) EngineInstallStart(ctx context.Context, tag string) (string, error) {
	if f.engineErr != nil {
		return "", f.engineErr
	}
	f.installs <- tag
	if tag == "" {
		return f.installed.Tag, nil
	}
	return tag, nil
}

func (f *fakeHost) EngineStop()                        { f.stopped++ }
func (f *fakeHost) EngineRemove(context.Context) error { return f.removeErr }
func (f *fakeHost) EngineDiscardParts() error          { f.discarded++; return nil }
func (f *fakeHost) EngineServeStart(context.Context) (engine.Serving, error) {
	f.served++
	return f.serving, f.serveErr
}
func (f *fakeHost) EngineServeStop(context.Context) (engine.Serving, error) {
	f.stoppedServe++
	return f.serving, f.serveStopErr
}

func (f *fakeHost) Devices(ctx context.Context, refresh bool) (deviceinfo.Report, error) {
	f.refreshed = refresh
	return f.devices, nil
}

func (f *fakeHost) ModelCatalog() *app.ModelsView {
	if f.modelsView == nil {
		f.modelsView = &app.ModelsView{Dir: "/data/models", BytesTotal: 1 << 30}
	}
	return f.modelsView
}

func (f *fakeHost) ModelInstall(id string) (*app.ModelsView, error) {
	f.installID = id
	return f.ModelCatalog(), f.installErr
}

func (f *fakeHost) ModelStop() *app.ModelsView { f.modelStops++; return f.ModelCatalog() }

func (f *fakeHost) ModelDiscard(id string) (*app.ModelsView, error) {
	f.discarded2 = append(f.discarded2, id)
	return f.ModelCatalog(), nil
}

func (f *fakeHost) ModelRemove(id string) (*app.ModelsView, error) {
	f.removed = append(f.removed, id)
	return f.ModelCatalog(), f.modelRemoveErr
}

func hostServer(t *testing.T, h Host) *Server {
	t.Helper()
	return New(Options{Host: h, Logger: slog.New(slog.NewTextHandler(nopWriter{}, nil))})
}

// hostCall is do() reduced to the two things these tests compare: the status, and the body.
func hostCall(t *testing.T, srv *Server, method, path, body string) (int, string) {
	t.Helper()
	w := do(t, srv, method, path, body)
	return w.Code, w.Body.String()
}

func TestEngineEndpointDescribesTheMachineInOneRoundTrip(t *testing.T) {
	h := newFakeHost()
	h.view.Installed = &h.installed
	h.view.Update = true
	h.view.Progress = engine.Progress{State: engine.StateDownloading, Done: 5 << 20, Total: 35 << 20}
	h.view.PartsBytes = 9 << 20
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodGet, "/api/host/engine", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var v app.EngineView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("body is not an EngineView: %v\n%s", err, body)
	}
	// One request answers the whole page. Every extra round trip is a state the two halves of
	// the screen can disagree about while the download moves.
	if v.Installed == nil || v.Installed.Tag != "b12649" {
		t.Errorf("installed = %+v, want the row from disk", v.Installed)
	}
	if v.Latest == nil || !v.Update {
		t.Error("the update the page offers is missing from the payload")
	}
	if v.Progress.State != engine.StateDownloading || v.Progress.Done != 5<<20 {
		t.Errorf("progress = %+v", v.Progress)
	}
	if v.PartsBytes != 9<<20 || v.Dir != "/data/bin" {
		t.Errorf("parts=%d dir=%q, want the numbers behind the discard button", v.PartsBytes, v.Dir)
	}
	if h.forced {
		t.Error("an ordinary visit forced an update check, which spends the hourly API allowance")
	}
}

func TestEngineEndpointChecksOnlyWhenAsked(t *testing.T) {
	h := newFakeHost()
	srv := hostServer(t, h)
	if code, body := hostCall(t, srv, http.MethodGet, "/api/host/engine?check=1", ""); code != http.StatusOK || !strings.Contains(body, "b12649") {
		t.Fatalf("status = %d body = %s", code, body)
	}
	if !h.forced {
		t.Error("?check=1 did not force the check, so the button silently does nothing")
	}
}

func TestEngineInstallAnswersBeforeTheDownloadFinishes(t *testing.T) {
	h := newFakeHost()
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodPost, "/api/host/engine", `{"tag":"b12649"}`)
	// 202, not 200: the fetch is minutes long, holding the request open buys nothing, and
	// Stop has to be answerable while it runs.
	if code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", code, body)
	}
	select {
	case tag := <-h.installs:
		if tag != "b12649" {
			t.Errorf("install was started for %q", tag)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the response came back without starting the install")
	}
}

func TestEngineInstallForwardsAnEmptyAskRatherThanNamingATag(t *testing.T) {
	h := newFakeHost()
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodPost, "/api/host/engine", `{}`)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", code, body)
	}
	select {
	case tag := <-h.installs:
		// The route does not guess which build is newest — it forwards the ask. Guessing here
		// would be a second copy of a rule that needs the release list to answer.
		if tag != "" {
			t.Errorf("install was started for %q, want the empty ask forwarded", tag)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no install was started")
	}
	// What the person is told is the tag the machine settled on, not an acknowledgment.
	if !strings.Contains(body, `"tag":"b12649"`) {
		t.Errorf("body = %s, want the tag the machine named", body)
	}
}

// The three refusals below are the machine module's decisions, told here only as the status
// and the sentence they arrive at: which of them a request earns, and what the person reads,
// is decided in internal/app (TestEngineInstallStart*). What would break if this file grew a
// switch of its own is the mapping, and only the mapping.
func TestEngineInstallRefusalIsTheMachinesSentenceAt422(t *testing.T) {
	h := newFakeHost()
	h.engineErr = fmt.Errorf("%w: %q is not a llama.cpp nightly tag, which is what a build is named by", app.ErrInvalid, "../../etc")
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodPost, "/api/host/engine", `{"tag":"../../etc"}`)
	if code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (%s)", code, body)
	}
	if !strings.Contains(body, "nightly") {
		t.Errorf("body = %s, want it to say what a tag looks like", body)
	}
	select {
	case tag := <-h.installs:
		t.Errorf("an install was started for %q anyway", tag)
	default:
	}
}

func TestEngineInstallWhileTheCheckIsStillRunning(t *testing.T) {
	h := newFakeHost()
	// A first visit on a fresh machine: nothing cached, so no release yet, nothing failed,
	// and the check is in flight. This is not an offline machine and the response must not
	// read like one.
	h.engineErr = fmt.Errorf("%w: libris is still finding out which build is newest; try again in a moment", app.ErrUpdateChecking)
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodPost, "/api/host/engine", `{}`)
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", code, body)
	}
	if !strings.Contains(body, "still") || strings.Contains(body, "failed") {
		t.Errorf("body = %s, want a wait-a-moment and no claim of failure", body)
	}
	select {
	case tag := <-h.installs:
		t.Errorf("an install was started for %q with no release to install", tag)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEngineInstallOfflineSaysSoInItsOwnRequest(t *testing.T) {
	h := newFakeHost()
	// What an offline machine produces: no release, and the upstream sentence to go with it.
	h.engineErr = fmt.Errorf("%w: the newest build could not be determined: engine: the update check failed: dial tcp", app.ErrUpdateUnknown)
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodPost, "/api/host/engine", `{}`)
	// Told now, in the request that asked — not left as a progress bar that never moves.
	if code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", code, body)
	}
	if !strings.Contains(body, "dial tcp") {
		t.Errorf("body = %s, want the reason carried through", body)
	}
	select {
	case tag := <-h.installs:
		t.Errorf("an install was started for %q with no release to install", tag)
	default:
	}
}

func TestEngineRemoveStatuses(t *testing.T) {
	// The busy case is the one that matters: on Windows the delete would half-succeed and
	// leave a tree that reads as uninstalled while a server keeps running from it.
	h := newFakeHost()
	h.removeErr = fmt.Errorf("%w — stop the model servers first", engine.ErrRunning)
	srv := hostServer(t, h)
	code, body := hostCall(t, srv, http.MethodDelete, "/api/host/engine", "")
	if code != http.StatusConflict {
		t.Errorf("busy: status = %d, want 409 (%s)", code, body)
	}
	if !strings.Contains(body, "stop the model servers") {
		t.Errorf("busy: body = %s, want the instruction the user can act on", body)
	}

	h2 := newFakeHost()
	h2.removeErr = fmt.Errorf("%w: b9", engine.ErrNotFound)
	srv2 := hostServer(t, h2)
	if code, body := hostCall(t, srv2, http.MethodDelete, "/api/host/engine", ""); code != http.StatusNotFound {
		t.Errorf("missing: status = %d, want 404 (%s)", code, body)
	}

	h3 := newFakeHost()
	srv3 := hostServer(t, h3)
	if code, body := hostCall(t, srv3, http.MethodDelete, "/api/host/engine", ""); code != http.StatusOK {
		t.Errorf("removed: status = %d (%s)", code, body)
	}
}

func TestEngineStopAndPartsAreSeparateActions(t *testing.T) {
	// Two buttons meaning two things: Stop keeps the bytes so the next attempt resumes;
	// Discard is the one that means "cancel". Collapsing them is how a Stop ends up costing a
	// fresh 35 MB download.
	h := newFakeHost()
	srv := hostServer(t, h)

	if code, body := hostCall(t, srv, http.MethodPost, "/api/host/engine/stop", ""); code != http.StatusOK {
		t.Fatalf("stop: status = %d (%s)", code, body)
	}
	if h.stopped != 1 || h.discarded != 0 {
		t.Errorf("stopped=%d discarded=%d, want Stop to keep the partial download", h.stopped, h.discarded)
	}
	if code, body := hostCall(t, srv, http.MethodDelete, "/api/host/engine/parts", ""); code != http.StatusOK {
		t.Fatalf("parts: status = %d (%s)", code, body)
	}
	if h.discarded != 1 {
		t.Error("discarding the parts did not reach the use case")
	}
}

func TestDevicesEndpoint(t *testing.T) {
	h := newFakeHost()
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodGet, "/api/host/devices", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var rep deviceinfo.Report
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatalf("body is not a Report: %v\n%s", err, body)
	}
	if len(rep.Devices) != 1 || rep.Devices[0].Name != "Vulkan0" {
		t.Errorf("devices = %+v", rep.Devices)
	}
	if h.refreshed {
		t.Error("the ordinary visit refreshed, which runs the collectors on every page open")
	}

	if _, body := hostCall(t, srv, http.MethodGet, "/api/host/devices?refresh=1", ""); !strings.Contains(body, "Vulkan0") {
		t.Fatalf("body = %s", body)
	}
	if !h.refreshed {
		t.Error("?refresh=1 did not reach the use case, so the button silently does nothing")
	}
}

func TestHostRoutesAreAbsentWithoutAHost(t *testing.T) {
	// Options.Host is nil-able like App, and a build without one must answer 404 rather than
	// panic on a nil interface the first time the settings page asks.
	srv := hostServer(t, nil)
	for _, u := range []string{"/api/host/engine", "/api/host/devices"} {
		code, _ := hostCall(t, srv, http.MethodGet, u, "")
		if code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", u, code)
		}
	}
}

func TestEngineViewErrorsFollowTheSameRuleAsEveryOtherRoute(t *testing.T) {
	// fail() answers an error that is not one of the sentinels with a generic sentence, on
	// purpose: an absolute path from the engine manager is not something to print in a
	// browser. What this asserts is that the host routes go through the same gate as the rest
	// of the API rather than hand-writing their own mapping — which is exactly where a
	// transport starts disagreeing with itself.
	h := newFakeHost()
	h.viewErr = errors.New("engine: could not read /home/user/Documents/libris/bin/index.json")
	srv := hostServer(t, h)
	code, body := hostCall(t, srv, http.MethodGet, "/api/host/engine", "")
	if code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (%s)", code, body)
	}
	if strings.Contains(body, "/home/user") {
		t.Errorf("body = %s, leaked a filesystem path", body)
	}
}

// The serve routes are the managed half of the settings page. What is under test
// is the mapping only — app.Host owns the process, and its fake here has no GPU and no
// llama-server any more than the unit tests do.
func TestEngineServeMapsTheFailuresAPageMustShow(t *testing.T) {
	h := newFakeHost()
	h.serving = engine.Serving{Running: true, Port: 8080, Models: []string{"libris-chat"}}
	srv := hostServer(t, h)
	code, body := hostCall(t, srv, http.MethodPost, "/api/host/engine/serve", "")
	if code != http.StatusOK {
		t.Fatalf("started: status = %d (%s)", code, body)
	}
	if !strings.Contains(body, "libris-chat") {
		t.Errorf("started: body = %s, want the models the router took", body)
	}

	// A port somebody else holds is 409 with the router's own sentence, because the fix —
	// change the port, or stop the other server — belongs to the person reading it.
	h2 := newFakeHost()
	h2.serving = engine.Serving{Port: 8080, Error: "Something else is already using port 8080."}
	h2.serveErr = fmt.Errorf("%w: %s", app.ErrEngineStart, h2.serving.Error)
	srv2 := hostServer(t, h2)
	code, body = hostCall(t, srv2, http.MethodPost, "/api/host/engine/serve", "")
	if code != http.StatusConflict {
		t.Errorf("port taken: status = %d, want 409 (%s)", code, body)
	}
	if !strings.Contains(body, "using port 8080") {
		t.Errorf("port taken: body = %s, want the sentence the user can act on", body)
	}

	// Pressing Start when autostart already brought the server up is the ordinary case, not an
	// error worth a red box: 409 with a sentence about the running process, and no sentence
	// about a queue nobody asked about.
	hRun := newFakeHost()
	hRun.serveErr = fmt.Errorf("%w (port 8080)", app.ErrEngineRunning)
	srvRun := hostServer(t, hRun)
	code, body = hostCall(t, srvRun, http.MethodPost, "/api/host/engine/serve", "")
	if code != http.StatusConflict {
		t.Errorf("already running: status = %d, want 409 (%s)", code, body)
	}
	if strings.Contains(body, "ingest") {
		t.Errorf("already running: body = %s, must not talk about the ingest queue", body)
	}

	// A start asked for in external mode is a 422: the request names a thing this mode does
	// not have, and a retry will not help.
	h3 := newFakeHost()
	h3.serveErr = app.ErrEngineMode
	srv3 := hostServer(t, h3)
	if code, body := hostCall(t, srv3, http.MethodPost, "/api/host/engine/serve", ""); code != http.StatusUnprocessableEntity {
		t.Errorf("wrong mode: status = %d, want 422 (%s)", code, body)
	}

	// Stopping what is not running answers with the state, not a 404.
	h4 := newFakeHost()
	srv4 := hostServer(t, h4)
	if code, body := hostCall(t, srv4, http.MethodDelete, "/api/host/engine/serve", ""); code != http.StatusOK {
		t.Errorf("stop idle: status = %d, want 200 (%s)", code, body)
	}
}

// ── the model catalogue ──────────────────────────────────────────────────────

func TestModelsGETIsTheCard(t *testing.T) {
	h := newFakeHost()
	h.modelsView = &app.ModelsView{
		Dir:        "/data/models",
		BytesTotal: 2586360320,
		Entries: []app.ModelEntryView{
			{ID: "lfm3-vl-2b", Label: "LFM3-VL 2B", Bytes: 1428611680, Complete: true, Verified: true,
				Parts: []app.ModelPartView{{Name: "LFM3-VL-2B-QAD-Q4_0.gguf", Bytes: 1049615648, Present: true}}},
		},
		NeedsRestart: true,
	}
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodGet, "/api/host/models", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var v app.ModelsView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("body is not a ModelsView: %v\n%s", err, body)
	}
	if v.BytesTotal != 2586360320 || !v.NeedsRestart || len(v.Entries) != 1 || !v.Entries[0].Verified {
		t.Errorf("the card lost something: %+v", v)
	}
}

// A 202 that only says "accepted" makes the client re-read the card to find out what its own
// click did. It carries the view instead.
func TestModelInstallAnswersWithTheStateItCaused(t *testing.T) {
	h := newFakeHost()
	h.modelsView = &app.ModelsView{Dir: "/data/models", Entries: []app.ModelEntryView{
		{ID: "embeddinggemma-2", Downloading: true},
	}}
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodPost, "/api/host/models", `{"id":"embeddinggemma-2"}`)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", code, body)
	}
	if h.installID != "embeddinggemma-2" {
		t.Errorf("the use case was told %q", h.installID)
	}
	var v app.ModelsView
	if err := json.Unmarshal([]byte(body), &v); err != nil || len(v.Entries) != 1 || !v.Entries[0].Downloading {
		t.Errorf("202 body is not the card that says so: %v %s", err, body)
	}
}

// Two at once, and a name that is not in the catalogue, are the two answers the page has to
// distinguish: one is "wait for the other bar", the other is a bug in this frontend.
func TestModelInstallStatuses(t *testing.T) {
	for _, tc := range []struct {
		what string
		err  error
		body string
		want int
	}{
		{"started", nil, `{"id":"x"}`, http.StatusAccepted},
		{"already busy", app.ErrBusy, `{"id":"x"}`, http.StatusConflict},
		{"unknown id", app.ErrInvalid, `{"id":"nope"}`, http.StatusUnprocessableEntity},
		{"no id", nil, `{}`, http.StatusBadRequest},
		{"not json", nil, `not json`, http.StatusBadRequest},
	} {
		h := newFakeHost()
		h.installErr = tc.err
		srv := hostServer(t, h)

		code, _ := hostCall(t, srv, http.MethodPost, "/api/host/models", tc.body)
		if code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.what, code, tc.want)
		}
	}
}

// Stop keeps its bytes and answers with the card; remove answers 409 while the server is up,
// and 200 with the card otherwise. The bodies are the point as much as the codes: the page
// re-renders from them rather than guessing.
func TestModelStopDiscardRemove(t *testing.T) {
	h := newFakeHost()
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodPost, "/api/host/models/stop", "")
	if code != http.StatusOK || !strings.Contains(body, `"dir"`) {
		t.Errorf("stop: %d %s", code, body)
	}
	if h.modelStops != 1 {
		t.Errorf("stop reached the fetcher %d times", h.modelStops)
	}

	code, _ = hostCall(t, srv, http.MethodPost, "/api/host/models/discard", `{"id":"lfm3-vl-2b"}`)
	if code != http.StatusOK {
		t.Errorf("discard: %d", code)
	}
	if len(h.discarded2) != 1 || h.discarded2[0] != "lfm3-vl-2b" {
		t.Errorf("discard went to %v", h.discarded2)
	}

	code, _ = hostCall(t, srv, http.MethodDelete, "/api/host/models/embeddinggemma-2", "")
	if code != http.StatusOK || len(h.removed) != 1 || h.removed[0] != "embeddinggemma-2" {
		t.Errorf("remove: %d %v", code, h.removed)
	}

	h.modelRemoveErr = app.ErrBusy
	code, body = hostCall(t, srv, http.MethodDelete, "/api/host/models/embeddinggemma-2", "")
	if code != http.StatusConflict {
		t.Errorf("remove while serving: %d %s, want 409 and the sentence", code, body)
	}
}
