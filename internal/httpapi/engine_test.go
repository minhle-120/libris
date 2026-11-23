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
	view      *app.EngineView
	viewErr   error
	forced    bool
	installs  chan string // the tag each install was started with
	removeErr error
	installed engine.Installed

	refreshed bool
	devices   deviceinfo.Report

	stopped, discarded int
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

func (f *fakeHost) EngineInstall(ctx context.Context, tag string) (engine.Installed, error) {
	f.installs <- tag
	return f.installed, nil
}

func (f *fakeHost) EngineStop()                        { f.stopped++ }
func (f *fakeHost) EngineRemove(context.Context) error { return f.removeErr }
func (f *fakeHost) EngineDiscardParts() error          { f.discarded++; return nil }

func (f *fakeHost) Devices(ctx context.Context, refresh bool) (deviceinfo.Report, error) {
	f.refreshed = refresh
	return f.devices, nil
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

func TestEngineInstallResolvesAnEmptyTagFromTheRelease(t *testing.T) {
	h := newFakeHost()
	srv := hostServer(t, h)

	code, body := hostCall(t, srv, http.MethodPost, "/api/host/engine", `{}`)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", code, body)
	}
	select {
	case tag := <-h.installs:
		// The browser never sees the release list, so "the newest" is resolved server-side —
		// and it resolves to the tag this same page is showing, not to a second opinion.
		if tag != "b12649" {
			t.Errorf("install was started for %q, want the newest release the view reported", tag)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no install was started")
	}
}

func TestEngineInstallRefusesBeforeStartingSomethingImpossible(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"a path is not a version", `{"tag":"../../etc"}`},
		{"a version nobody built", `{"tag":"b1"}`},
	}
	h := newFakeHost()
	srv := hostServer(t, h)
	for _, c := range cases {
		code, body := hostCall(t, srv, http.MethodPost, "/api/host/engine", c.body)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", c.name, code, body)
		}
		if !strings.Contains(body, "nightly") {
			t.Errorf("%s: body = %s, want it to say what a tag looks like", c.name, body)
		}
		select {
		case tag := <-h.installs:
			t.Errorf("%s: an install was started for %q anyway", c.name, tag)
		default:
		}
	}
}

func TestEngineInstallWhileTheCheckIsStillRunning(t *testing.T) {
	h := newFakeHost()
	// A first visit on a fresh machine: nothing cached, so no release yet, nothing failed,
	// and the check is in flight. This is not an offline machine and the response must not
	// read like one.
	h.view.Latest, h.view.Asset, h.view.CheckError, h.view.Checking = nil, nil, "", true
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
	// What an offline machine produces: the installed row, no release, and a sentence why.
	h.view.Latest, h.view.Asset = nil, nil
	h.view.CheckError = "engine: the update check failed: dial tcp"
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
