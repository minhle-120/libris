package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"libris/internal/config"
	"libris/internal/deviceinfo"
	"libris/internal/engine"
	"libris/internal/models"
)

// fakeEngine is the manager without a network, a disk, or a GPU. Every branch the page can
// reach is one of its fields, because the interesting states of this screen are the ones that
// involve something going wrong: offline, busy, half-downloaded, running elsewhere.
//
// Its counters are mutex-guarded because Host calls Check from a background goroutine: an
// unguarded counter here is a data race in the fixture, which is the one kind of test failure
// that teaches you to distrust the test rather than the code.
type fakeEngine struct {
	installed *engine.Installed
	rel       engine.Release
	asset     engine.Asset
	checkErr  error
	prog      engine.Progress
	dir       string
	parts     int64
	// installErr is what Install answers with, when the caller wants the failure path.
	installErr error

	// The cached answer a check would leave behind. The real manager writes its cache file
	// inside Check, so a fake that did not would make a successful update check look like it
	// had no effect at all.
	knownRel   engine.Release
	knownAsset engine.Asset
	knownOK    bool

	// checked, when set, receives the outcome every time the manager is asked; delay holds
	// the answer open so a test that polls the page is provably polling while the check is
	// still in flight.
	checked chan error
	delay   time.Duration

	mu        sync.Mutex
	checks    int
	forced    int
	tags      []string // what Install was asked for
	removed   int
	removeErr error
	discarded int
	stopped   int
}

func (f *fakeEngine) Status() (engine.Installed, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.installed == nil {
		return engine.Installed{}, false
	}
	return *f.installed, true
}

func (f *fakeEngine) Known(b engine.Backend) (engine.Release, engine.Asset, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.knownRel, f.knownAsset, f.knownOK
}

func (f *fakeEngine) Check(ctx context.Context, b engine.Backend, force bool) (engine.Release, engine.Asset, error) {
	f.mu.Lock()
	f.checks++
	if force {
		f.forced++
	}
	err, rel, asset, delay := f.checkErr, f.rel, f.asset, f.delay
	checked := f.checked
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if checked != nil {
		checked <- err
	}
	if err != nil {
		return engine.Release{}, engine.Asset{}, err
	}

	f.mu.Lock()
	f.knownOK, f.knownRel, f.knownAsset = true, rel, asset
	f.mu.Unlock()
	return rel, asset, nil
}

func (f *fakeEngine) Install(ctx context.Context, tag string, b engine.Backend) (engine.Installed, error) {
	f.mu.Lock()
	f.tags = append(f.tags, tag)
	inst, err := f.installed, f.installErr
	f.mu.Unlock()
	if err != nil {
		return engine.Installed{}, err
	}
	if inst == nil {
		return engine.Installed{}, errors.New("fake: nothing to install")
	}
	return *inst, nil
}

func (f *fakeEngine) Remove(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed++
	if f.removeErr != nil {
		return f.removeErr
	}
	f.installed = nil
	return nil
}

func (f *fakeEngine) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
}

func (f *fakeEngine) Progress() engine.Progress {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prog
}

func (f *fakeEngine) Dir() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dir
}

func (f *fakeEngine) PartsBytes() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.parts
}

func (f *fakeEngine) ClearParts() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discarded++
	return nil
}

// seen reports the counters the assertions read, under the same lock the manager's goroutine
// takes to write them.
func (f *fakeEngine) seen() (checks, forced, removed, discarded, stopped int, tags []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.forced, f.removed, f.discarded, f.stopped,
		append([]string(nil), f.tags...)
}

// hostSettings is the one method of the Settings interface these tests care about, held as a
// value so a test can change the build selection and see the effect on what is fetched.
type hostSettings struct{ cfg config.Config }

func (h *hostSettings) Get() config.Config { return h.cfg }

func newTestHost(t *testing.T, eng EngineManager, cfg config.Config) (*Host, string) {
	t.Helper()
	h, cache := newTestHostWith(t, eng, cfg, HostOptions{})
	return h, cache
}

// newTestHostWith is the same host with the managed half switched on: a models directory, a
// fake downloader, a fake server. Those three are what every managed-mode decision in this
// file is about, and none of them needed to be real to test it.
func newTestHostWith(t *testing.T, eng EngineManager, cfg config.Config, extra HostOptions) (*Host, string) {
	t.Helper()
	dir := t.TempDir()
	cache := filepath.Join(dir, "devices.json")
	if extra.ModelsDir == "" {
		extra.ModelsDir = filepath.Join(dir, "models")
	}
	if extra.PresetPath == "" {
		extra.PresetPath = filepath.Join(dir, "presets", "libris.ini")
	}
	h, err := NewHost(HostOptions{
		Engine:      eng,
		Settings:    &hostSettings{cfg: cfg},
		Serve:       extra.Serve,
		Downloads:   extra.Downloads,
		ModelsDir:   extra.ModelsDir,
		PresetPath:  extra.PresetPath,
		DeviceCache: cache,
		Probe: func(context.Context, deviceinfo.Options) (deviceinfo.Report, error) {
			return deviceinfo.Report{
				OS:      "linux",
				Arch:    "amd64",
				Devices: []deviceinfo.Device{{Kind: "nvidia", Name: "Vulkan0", Description: "NVIDIA GeForce RTX 3070", Source: "llama-server"}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, cache
}

func TestEngineViewReportsWhatIsInstalledEvenOffline(t *testing.T) {
	eng := &fakeEngine{
		dir: "/data/bin",
		installed: &engine.Installed{
			Tag: "b12600", Backend: engine.Vulkan, Path: "/data/bin/b12600/bin/llama-server",
		},
		checkErr: errors.New("engine: the update check failed: dial tcp: lookup api.github.com"),
		checked:  make(chan error, 4),
		delay:    20 * time.Millisecond,
	}
	h, _ := newTestHost(t, eng, config.Default())

	// The first answer is the local one, and it is immediate: what is installed, where it
	// lives, and an admission that the newest build is not known yet.
	v, err := h.Engine(context.Background(), false)
	if err != nil {
		t.Fatalf("offline: %v", err)
	}
	if v.Installed == nil || v.Installed.Tag != "b12600" {
		t.Errorf("installed = %+v, want the row from disk", v.Installed)
	}
	if !v.Checking {
		t.Error("a cold view did not say a check was running, so the absence of a release reads as a dead end")
	}
	if v.Latest != nil {
		t.Error("a cold view invented a release")
	}

	// Then the check fails, and the next view says so in words rather than going quiet.
	v = settled(t, h)
	if v.CheckError == "" {
		t.Error("a failed check reported no error, so the page would say nothing is wrong")
	}
	if v.Checking {
		t.Error("a finished check was still reported as running")
	}
	if v.Update {
		t.Error("offline, an update was claimed")
	}
	if v.Dir != "/data/bin" {
		t.Errorf("dir = %q, want the directory a download would use", v.Dir)
	}
}

// settled polls the view until the background check stops reporting itself running. The
// observable is polled rather than a goroutine signalled, because a test that sleeps on an
// internal channel is a test that passes on a fast machine and fails on a loaded one.
func settled(t *testing.T, h *Host) *EngineView {
	t.Helper()
	for range 400 {
		v, err := h.Engine(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		if !v.Checking {
			return v
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the update check never finished")
	return nil
}

func TestEngineViewOffersAnUpdateOnlyNumerically(t *testing.T) {
	cases := []struct {
		installed, latest string
		want              bool
		why               string
	}{
		{"b12600", "b12649", true, "a newer nightly"},
		{"b12649", "b12649", false, "already current"},
		{"b12649", "b9999", false, "a downgrade offered as an upgrade by string comparison"},
	}
	for _, c := range cases {
		eng := &fakeEngine{
			installed:  &engine.Installed{Tag: c.installed, Backend: engine.Vulkan},
			knownOK:    true,
			knownRel:   engine.Release{Tag: c.latest},
			knownAsset: engine.Asset{Name: "asset.tar.gz", Size: 35 << 20},
		}
		h, _ := newTestHost(t, eng, config.Default())
		v, err := h.Engine(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		if v.Update != c.want {
			t.Errorf("installed %s, latest %s: update=%v, want %v (%s)",
				c.installed, c.latest, v.Update, c.want, c.why)
		}
		if v.Latest == nil || v.Asset == nil {
			t.Error("the page was given neither the release nor the asset to offer")
		}
	}
}

func TestEngineInstallResolvesTheTag(t *testing.T) {
	eng := &fakeEngine{
		knownOK:    true,
		knownRel:   engine.Release{Tag: "b12649"},
		knownAsset: engine.Asset{Name: "asset.tar.gz"},
		installed:  &engine.Installed{Tag: "b12649", Backend: engine.Vulkan},
	}
	h, cache := newTestHost(t, eng, config.Default())

	// The install was asked for with no tag — the label on the button is "Download", not a
	// version — so the tag has to be resolved here, and the backend that decided it is the
	// one in settings, not the compiled default.
	if _, err := h.EngineInstall(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _, tags := eng.seen()
	if len(tags) != 1 || tags[0] != "b12649" {
		t.Errorf("Install was asked for %v, want the newest tag", tags)
	}
	// The engine is a device probe once it exists, so the cached report about the machine has
	// to go: otherwise the page keeps saying what the machine could run *before* the build
	// that knows arrived.
	if _, err := os.Stat(cache); !errors.Is(err, os.ErrNotExist) {
		t.Error("installing the engine did not invalidate the device report")
	}
}

func TestUpdateCheckRunsOnceInTheBackgroundAndLands(t *testing.T) {
	eng := &fakeEngine{
		installed: &engine.Installed{Tag: "b12600"},
		rel:       engine.Release{Tag: "b12649"},
		asset:     engine.Asset{Name: "asset.tar.gz", Size: 35 << 20},
		checked:   make(chan error, 4),
	}
	h, _ := newTestHost(t, eng, config.Default())

	first, err := h.Engine(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Checking || first.Latest != nil {
		t.Fatalf("first view: checking=%v latest=%+v, want the local answer plus an admission", first.Checking, first.Latest)
	}

	// Poll the page the way two open tabs would, all of it while the check is held open.
	for range 20 {
		v, err := h.Engine(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		if !v.Checking {
			break // the check finished between two polls; the rest of the test still holds
		}
	}

	v := settled(t, h)
	// Two pollers, one question. Without the in-flight guard this is a loop that spends the
	// hourly unauthenticated GitHub allowance on a page nobody is looking at.
	checks, _, _, _, _, _ := eng.seen()
	if checks != 1 {
		t.Errorf("%d update checks ran, want one", checks)
	}
	if v.Latest == nil || v.Latest.Tag != "b12649" || !v.Update {
		t.Errorf("after the check: latest=%+v update=%v", v.Latest, v.Update)
	}
}

func TestForcedCheckIsAlsoNonBlocking(t *testing.T) {
	eng := &fakeEngine{
		knownOK:  true,
		knownRel: engine.Release{Tag: "b12600"},
		checked:  make(chan error, 4),
	}
	h, _ := newTestHost(t, eng, config.Default())

	// "Check for updates" is a button. Blocking the request on it would make the page wait on
	// an external server for as long as that server likes, which is the reason nothing here
	// does — the press answers at once, and the answer arrives in a later poll.
	v, err := h.Engine(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Checking {
		t.Error("a forced check did not report itself running")
	}
	if v.Latest == nil || v.Latest.Tag != "b12600" {
		t.Error("the forced check threw away the cached answer while it worked")
	}
	settled(t, h)
	_, forced, _, _, _, _ := eng.seen()
	if forced != 1 {
		t.Errorf("forced=%d, want the background refresh to ask upstream", forced)
	}
}

func TestAFailedCheckIsNotRetriedOnEveryVisit(t *testing.T) {
	eng := &fakeEngine{
		installed: &engine.Installed{Tag: "b12600"},
		checkErr:  errors.New("engine: the update check failed: dial tcp"),
	}
	h, _ := newTestHost(t, eng, config.Default())

	settled(t, h) // the first attempt, which fails

	// Twelve more visits — a page left open, a second tab, a reload. A machine that cannot
	// reach GitHub must not be made to ask it again on every one of them.
	for range 12 {
		v, err := h.Engine(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		if v.Checking {
			t.Fatal("a failed check was restarted by an ordinary visit")
		}
		if v.CheckError == "" {
			t.Error("the failure stopped being reported, so the page went quiet for no stated reason")
		}
	}
	if checks, _, _, _, _, _ := eng.seen(); checks != 1 {
		t.Errorf("%d checks for 13 visits, want the failure left alone", checks)
	}

	// Pressing the button is a different act, and ignores the backoff.
	if _, err := h.Engine(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	settled(t, h)
	if checks, forced, _, _, _, _ := eng.seen(); checks != 2 || forced != 2 {
		t.Errorf("checks=%d forced=%d, want the button to force a second attempt", checks, forced)
	}
}

func TestEngineRemovePropagatesTheRefusal(t *testing.T) {
	eng := &fakeEngine{installed: &engine.Installed{Tag: "b12649"}}
	h, _ := newTestHost(t, eng, config.Default())

	// A manager that refuses is not a Host that lies: the transport maps this error to 409 and
	// the page to "stop the model servers first".
	eng.removeErr = errors.New("engine: llama-server is still running from that build")
	if err := h.EngineRemove(context.Background()); err == nil {
		t.Fatal("the refusal was swallowed")
	}
}

func TestDevicesAreCachedUntilTheFingerprintChanges(t *testing.T) {
	eng := &fakeEngine{}
	dir := t.TempDir()
	cache := filepath.Join(dir, "devices.json")

	probes := 0
	newHost := func() *Host {
		h, err := NewHost(HostOptions{
			Engine: eng, Settings: &hostSettings{cfg: config.Default()}, DeviceCache: cache,
			Probe: func(context.Context, deviceinfo.Options) (deviceinfo.Report, error) {
				probes++
				return deviceinfo.Report{
					OS:          "linux",
					Arch:        "amd64",
					Devices:     []deviceinfo.Device{{Kind: "nvidia", Name: "Vulkan0", Description: "NVIDIA GeForce RTX 3070", Source: "llama-server"}},
					Fingerprint: "fp-1",
				}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	if _, err := newHost().Devices(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("probes = %d, want the first visit to run once", probes)
	}

	// A second visit, from a fresh Host, without asking again. What the settings page does.
	if _, err := newHost().Devices(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Errorf("probes = %d, want the second visit served from the cache", probes)
	}

	// Except when the machine says so.
	if _, err := newHost().Devices(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if probes != 2 {
		t.Errorf("probes = %d, want ?refresh=1 to ask the engine again", probes)
	}

	// The cached document is the one the page renders, so it has to be the whole report.
	raw, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	var stored deviceinfo.Report
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Devices) != 1 || stored.Devices[0].Name != "Vulkan0" {
		t.Errorf("cached report = %+v, want the devices in it", stored.Devices)
	}
	if stored.CheckedAt.IsZero() {
		t.Error("the cached report has no timestamp, so nothing can say how old the answer is")
	}
}

func TestDevicesAskTheEngineOnceItExists(t *testing.T) {
	// The installed engine is the only thing worth asking, so its path and its tag are handed
	// on. A test that asserted this on the *result* would be asserting parse and merge rules
	// that belong to deviceinfo; what belongs to Host is that it passes both through at all —
	// the tag because the cache is keyed on the build that answered.
	eng := &fakeEngine{installed: &engine.Installed{Tag: "b12649", Path: "/data/bin/b12649/bin/llama-server"}}

	var seenBin, seenTag string
	h, err := NewHost(HostOptions{
		Engine: eng, Settings: &hostSettings{cfg: config.Default()},
		Probe: func(_ context.Context, o deviceinfo.Options) (deviceinfo.Report, error) {
			seenBin, seenTag = o.EngineBin, o.EngineTag
			return deviceinfo.Report{OS: "linux"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Devices(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if seenBin != "/data/bin/b12649/bin/llama-server" {
		t.Errorf("the engine was given as %q, want the installed binary", seenBin)
	}
	if seenTag != "b12649" {
		t.Errorf("the engine was given with tag %q, want the installed build", seenTag)
	}

	// With no engine, nothing is handed over, and no attempt is made to run a binary that is
	// not there. This is the state a first run is in, and deviceinfo says so in words rather
	// than answering a question nobody asked.
	eng.installed = nil
	if _, err := h.Devices(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if seenBin != "" || seenTag != "" {
		t.Errorf("with no engine installed the probe was given %q %q", seenBin, seenTag)
	}
}

// The trade of making the engine the only device probe, tested where the caching
// lives: a report from a machine that had
// nothing installed must not be served back once something is. The old document is not an
// answer about the hardware — it is the admission that nothing was asked — so serving it
// after a download would be the settings page lying about a machine it has now looked at.
func TestDevicesCacheIsNotServedOnceAnEngineExists(t *testing.T) {
	eng := &fakeEngine{}
	cache := filepath.Join(t.TempDir(), "devices.json")

	probes := 0
	newHost := func() *Host {
		h, err := NewHost(HostOptions{
			Engine: eng, Settings: &hostSettings{cfg: config.Default()}, DeviceCache: cache,
			Probe: func(context.Context, deviceinfo.Options) (deviceinfo.Report, error) {
				probes++
				return deviceinfo.Report{
					OS: "linux", Arch: "amd64",
					Devices: []deviceinfo.Device{{Kind: "cpu", Name: "x86-64 CPU"}},
				}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	if _, err := newHost().Devices(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	eng.installed = &engine.Installed{Tag: "b12649", Path: "/data/bin/b12649/llama-server"}
	if _, err := newHost().Devices(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if probes != 2 {
		t.Errorf("probes = %d, want the download to make the cached report stale",
			probes)
	}
}

func TestProgressIsPassedThroughVerbatim(t *testing.T) {
	// The progress record is assembled by the manager, which owns the clock. Host is allowed
	// to add nothing to it — a second view of the same numbers is how the two start
	// disagreeing — so this test is short by design.
	eng := &fakeEngine{prog: engine.Progress{
		State: engine.StateDownloading, Tag: "b12649", Asset: "asset.tar.gz",
		Done: 12 << 20, Total: 35 << 20, Since: "2026-11-23T10:00:00Z",
	}}
	h, _ := newTestHost(t, eng, config.Default())
	v, err := h.Engine(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if v.Progress.State != engine.StateDownloading || v.Progress.Done != 12<<20 {
		t.Errorf("progress = %+v, want the manager's record unchanged", v.Progress)
	}
	if v.Progress.Since != "2026-11-23T10:00:00Z" {
		t.Error("the start timestamp was reformatted on the way through")
	}
}

func TestPartsAreSeparateFromTheEngine(t *testing.T) {
	eng := &fakeEngine{parts: 9 << 20}
	h, _ := newTestHost(t, eng, config.Default())

	v, err := h.Engine(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if v.PartsBytes != 9<<20 {
		t.Errorf("parts = %d, want the page to be able to show what discarding frees", v.PartsBytes)
	}
	if err := h.EngineDiscardParts(); err != nil {
		t.Fatal(err)
	}
	_, _, removed, discarded, stopped, _ := eng.seen()
	if discarded != 1 || removed != 0 {
		t.Errorf("discarded=%d removed=%d, want discarding a stopped download to leave the engine alone",
			discarded, removed)
	}
	h.EngineStop()
	_, _, _, _, stopped, _ = eng.seen()
	if stopped != 1 {
		t.Error("Stop did not reach the manager, so the button would do nothing")
	}
}

func TestSettingsChangedNoticesOnlyWhatMatters(t *testing.T) {
	h, _ := newTestHost(t, &fakeEngine{}, config.Default())
	prev := config.Default()

	if h.SettingsChanged(prev, config.Default()) {
		t.Error("saving unrelated settings asks the page to refetch the engine")
	}
	next := config.Default()
	next.EnginePort = 9090
	if !h.SettingsChanged(prev, next) {
		t.Error("changing the port did not, so the page still reports the server on the old one")
	}
}

// A start asked for while libris is configured to talk to somebody else's servers is refused
// rather than obeyed: running a second llama.cpp because a stale page pressed a button is the
// "both at once" state the switch exists to make impossible.
func TestEngineServeRespectsTheMode(t *testing.T) {
	cfg := config.Default()
	cfg.EngineMode = config.EngineExternal
	h, _ := newTestHost(t, &fakeEngine{dir: t.TempDir()}, cfg)

	if _, err := h.EngineServeStart(context.Background()); !errors.Is(err, ErrEngineMode) {
		t.Fatalf("got %v, want ErrEngineMode", err)
	}
	if err := h.EngineServeStop(context.Background()); !errors.Is(err, ErrEngineMode) {
		t.Fatalf("stop: got %v, want ErrEngineMode", err)
	}
}

// Managed mode with nothing downloaded does not spawn anything. The page's answer is the
// download button, and a start that got as far as exec would be a crash report instead.
func TestEngineServeNeedsAnInstalledEngine(t *testing.T) {
	cfg := config.Default()
	cfg.EngineMode = config.EngineManaged
	h, _ := newTestHost(t, &fakeEngine{dir: t.TempDir(), installed: nil}, cfg)
	h.serve = engine.NewSupervisor(t.TempDir(), engine.Seams{})

	if _, err := h.EngineServeStart(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
}

// The machine section is the page that changes when the mode changes, so a save that flipped
// it must refetch it — otherwise external mode keeps showing a device list for a server
// libris will never run.
func TestSettingsChangedWatchesTheMode(t *testing.T) {
	h, _ := newTestHost(t, &fakeEngine{dir: t.TempDir()}, config.Default())
	prev := config.Default()

	next := prev
	next.EngineMode = config.EngineExternal
	if !h.SettingsChanged(prev, next) {
		t.Error("a mode change did not ask the machine section to refetch")
	}

	port := prev
	port.EnginePort = 9100
	if !h.SettingsChanged(prev, port) {
		t.Error("a port change did not ask the machine section to refetch")
	}

	same := prev
	same.ChatModel = "renamed"
	if h.SettingsChanged(prev, same) {
		t.Error("a model name change refetched the machine section for no reason")
	}
}

// fakeFetch is the model downloader without a network. What it records is what the use cases
// are judged on: which entry was asked for, and whether a stop or a discard went to the right
// one.
type fakeFetch struct {
	mu        sync.Mutex
	started   []string
	stopped   int
	discarded []string
	progress  models.Progress
	startErr  error
}

func (f *fakeFetch) Start(e models.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, e.ID)
	return nil
}

func (f *fakeFetch) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
}

func (f *fakeFetch) Discard(e models.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discarded = append(f.discarded, e.ID)
	return nil
}

func (f *fakeFetch) Progress() models.Progress {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.progress
}

func (f *fakeFetch) seen() (started []string, stopped int, discarded []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.started...), f.stopped, append([]string(nil), f.discarded...)
}

// fakeServer is libris's own llama-server, without the process.
type fakeServer struct {
	status engine.Serving
	starts int
	specs  []engine.Model
	// opts is the last thing the app asked to run with. The path of the preset file and the
	// device it carries are facts the page shows, so they are worth asserting.
	opts    engine.ServeOptions
	stopErr error
	// startErr is what a start answers with when it is refused — already running, or a port
	// somebody else holds — so the sentence the page gets can be tested without a process.
	startErr error
}

func (s *fakeServer) Start(_ context.Context, o engine.ServeOptions, mods []engine.Model) (engine.Serving, error) {
	s.starts++
	s.opts = o
	s.specs = mods
	if s.startErr != nil {
		// The refused start still reports what is running, because "already running on port
		// 41234" is only useful if the answer carries the port.
		return s.status, s.startErr
	}
	return s.status, nil
}

func (s *fakeServer) Stop() error { return s.stopErr }

func (s *fakeServer) Status() engine.Serving { return s.status }

// The catalogue is the page. An empty disk still has to produce the two rows, their sizes and
// where they would go, because that is the screen a person sees before they have downloaded
// anything, and a card that renders nothing explains nothing about the 2.4 GB it wants.
// assertDescribed is what every row of the card must satisfy, whether or not anything has
// been downloaded: enough words and numbers to decide what to press.
func assertDescribed(t *testing.T, e ModelEntryView) {
	t.Helper()
	if e.ID == "" || e.Label == "" || e.Quant == "" || e.Note == "" || e.Repo == "" {
		t.Errorf("an entry the page cannot describe: %+v", e)
	}
	if e.Dir == "" || len(e.Parts) == 0 {
		t.Errorf("%s: no folder, no files: %+v", e.ID, e)
	}
	for _, p := range e.Parts {
		if p.Name == "" || p.Bytes <= 0 {
			t.Errorf("%s: a file with no size: %+v", e.ID, p)
		}
	}
}

func TestModelCatalogListsTheCatalogueOnAnEmptyDisk(t *testing.T) {
	fetch := &fakeFetch{}
	h, _ := newTestHostWith(t, &fakeEngine{}, config.Default(), HostOptions{Downloads: fetch})

	v := h.ModelCatalog()
	if len(v.Entries) != len(models.Catalog()) {
		t.Fatalf("%d entries, want the whole catalogue", len(v.Entries))
	}

	var total int64
	for _, e := range v.Entries {
		assertDescribed(t, e)
		if e.Complete || e.Verified {
			t.Errorf("%s: an empty disk is neither complete nor verified", e.ID)
		}
		for _, p := range e.Parts {
			if p.Present {
				t.Errorf("%s/%s: present on an empty disk", e.ID, p.Name)
			}
		}
		total += e.Bytes
	}
	if v.BytesTotal != total {
		t.Errorf("BytesTotal = %d, want %d", v.BytesTotal, total)
	}

	// The ids are what the POST sends, so this is the round trip that has to agree.
	if _, err := h.ModelInstall(v.Entries[0].ID); err != nil {
		t.Errorf("installing an entry the view just listed: %v", err)
	}
	started, _, _ := fetch.seen()
	if len(started) != 1 || started[0] != v.Entries[0].ID {
		t.Errorf("started %v, want the one id the view named", started)
	}
}

// The progress bar belongs to one row. A card that marked every row busy on the strength of
// one download would be showing the reader two progress bars for one of them.
func TestModelCatalogMarksOnlyTheRowThatIsDownloading(t *testing.T) {
	running := models.Progress{ID: models.Catalog()[0].ID, State: models.StateDownloading, Done: 4 << 20, Total: 1 << 30}
	fetch := &fakeFetch{progress: running}
	h, _ := newTestHostWith(t, &fakeEngine{}, config.Default(), HostOptions{Downloads: fetch})

	v := h.ModelCatalog()
	if !v.Entries[0].Downloading {
		t.Error("the entry being downloaded does not say so")
	}
	for _, e := range v.Entries[1:] {
		if e.Downloading {
			t.Errorf("%s claims the download that belongs to %s", e.ID, running.ID)
		}
	}
	if v.Progress.State != models.StateDownloading || v.Progress.Done != running.Done {
		t.Errorf("the card lost the progress: %+v", v.Progress)
	}

	// The same fetcher, having finished: nothing is running, so no row has a bar — the bytes
	// it left are reported as bytes instead, by the next test.
	fetch.progress = models.Progress{ID: running.ID, State: models.StateIdle, Done: running.Done}
	for _, e := range h.ModelCatalog().Entries {
		if e.Downloading {
			t.Errorf("%s still shows a bar after the download ended", e.ID)
		}
	}
}

// A page can only ask for a model that exists. This is the boundary where a typo in the
// frontend becomes a sentence rather than a 500.
func TestModelUseCasesRejectAnUnknownID(t *testing.T) {
	fetch := &fakeFetch{}
	h, _ := newTestHostWith(t, &fakeEngine{}, config.Default(), HostOptions{Downloads: fetch})

	for name, call := range map[string]func() error{
		"install": func() error { _, err := h.ModelInstall("not-in-the-catalogue"); return err },
		"discard": func() error { _, err := h.ModelDiscard("not-in-the-catalogue"); return err },
		"remove":  func() error { _, err := h.ModelRemove("not-in-the-catalogue"); return err },
	} {
		if err := call(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	started, _, discarded := fetch.seen()
	if len(started) != 0 || len(discarded) != 0 {
		t.Errorf("an unknown id reached the downloader anyway: %v %v", started, discarded)
	}
}

// Two downloads at once is a busy answer, not a bug: the fetcher says so, and the page needs
// the 429 rather than a red box.
func TestModelInstallPassesBusyThrough(t *testing.T) {
	fetch := &fakeFetch{startErr: models.ErrBusy}
	h, _ := newTestHostWith(t, &fakeEngine{}, config.Default(), HostOptions{Downloads: fetch})

	if _, err := h.ModelInstall(models.Catalog()[0].ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v, want ErrBusy", err)
	}
}

// A download with no data directory to write into is the build's problem, not the user's, and
// it must not read as a missing model.
func TestModelInstallWithoutADownloader(t *testing.T) {
	h, _ := newTestHostWith(t, &fakeEngine{}, config.Default(), HostOptions{})
	if _, err := h.ModelInstall(models.Catalog()[0].ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v, want an ErrInvalid-shaped sentence", err)
	}
}

// Deleting a model the running server has open is refused. The reason is the same one the
// engine gives for the same refusal, and the answer names the act that would make it safe.
func TestModelRemoveRefusesWhileTheServerRuns(t *testing.T) {
	srv := &fakeServer{status: engine.Serving{Running: true, PID: 4242, Port: 8080}}
	h, _ := newTestHostWith(t, &fakeEngine{}, config.Default(), HostOptions{Serve: srv})

	id := models.Catalog()[0].ID
	if _, err := h.ModelRemove(id); !errors.Is(err, ErrBusy) {
		t.Fatalf("%v, want ErrBusy naming the server", err)
	}
	if _, err := os.Stat(filepath.Join(models.Catalog()[0].Dir(h.modelsDir))); !os.IsNotExist(err) {
		t.Error("the refusal deleted the model anyway")
	}
}

// With the server down, the same call removes the whole folder — partial bytes and the digest
// records included, because a removal that leaves debris behind is a removal the user has to
// finish by hand.
func TestModelRemoveDeletesTheWholeFolder(t *testing.T) {
	h, _ := newTestHostWith(t, &fakeEngine{}, config.Default(), HostOptions{Serve: &fakeServer{}})

	e := models.Catalog()[0]
	dir := e.Dir(h.modelsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	part := models.PartPath(filepath.Join(dir, e.Model.Name))
	if err := os.WriteFile(part, []byte("half a model"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := h.ModelRemove(e.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder survived: %v", err)
	}
}

// A model that arrived while the router was up is not loaded by it: the preset is read at
// start. The page has to say so, or the row claims a model nothing is serving.
func TestModelCatalogSaysWhenTheServerMustBeRestarted(t *testing.T) {
	e := models.Catalog()[0]
	dir := t.TempDir()
	for _, p := range e.Parts() {
		path := e.Path(dir, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		// Sparse: the sizes are the point, the bytes are not.
		if err := f.Truncate(p.Bytes); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	srv := &fakeServer{status: engine.Serving{Running: true, PID: 1, Port: 8080, Models: []string{}}}
	h, _ := newTestHostWith(t, &fakeEngine{}, config.Default(),
		HostOptions{Serve: srv, ModelsDir: dir})

	v := h.ModelCatalog()
	var mine *ModelEntryView
	for i := range v.Entries {
		if v.Entries[i].ID == e.ID {
			mine = &v.Entries[i]
		}
	}
	if mine == nil || !mine.Complete {
		t.Fatalf("the entry I just placed on disk does not read as complete: %+v", v.Entries)
	}
	if !v.NeedsRestart {
		t.Error("a running server that loaded nothing does not ask for a restart")
	}

	srv.status.Models = []string{string(e.Role.Alias())}
	if h.ModelCatalog().NeedsRestart {
		t.Error("every complete model loaded, and it still wants a restart")
	}
}

// The device a model server runs on is a slot name from the installed build's own
// --list-devices, and a name that build does not know is not a fallback — llama.cpp answers
// `invalid device: CUDA0` and exits. So a saved name is honoured only while it still exists,
// and the fallback is stated in the log rather than silently taken.
func TestPresetDeviceHonoursASavedNameOnlyWhileItExists(t *testing.T) {
	twoGPUs := deviceinfo.Report{Devices: []deviceinfo.Device{
		{Kind: "amd", Name: "Vulkan0", Description: "AMD Radeon 660M", Source: "llama-server"},
		{Kind: "nvidia", Name: "Vulkan1", Description: "NVIDIA GeForce RTX 3050 Ti", Source: "llama-server"},
	}}

	cases := []struct {
		name     string
		saved    string
		report   deviceinfo.Report
		want     string
		wantWhy  bool
		noProbe  bool
		probeErr bool
	}{
		{name: "blank takes the first device the build can use", report: twoGPUs, want: "Vulkan0"},
		{name: "the case a hand-edited preset uses is accepted", saved: "vulkan1", report: twoGPUs, want: "Vulkan1"},
		{
			name:  "a device that has gone away falls back, and says so",
			saved: "Vulkan7", report: twoGPUs, want: "Vulkan0", wantWhy: true,
		},
		{
			name:  "CPU-only is llama.cpp's own word and needs no lookup",
			saved: deviceinfo.DeviceNone, report: twoGPUs, want: deviceinfo.DeviceNone, noProbe: true,
		},
		{
			name: "no Vulkan device is the CPU", saved: "",
			report: deviceinfo.Report{Devices: []deviceinfo.Device{{Kind: "nvidia", Name: "CUDA0"}}},
			want:   deviceinfo.DeviceNone,
		},
		{
			name: "a machine that cannot be asked gets no device key at all", saved: "Vulkan0",
			report: twoGPUs, want: "", wantWhy: true, probeErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.EngineMode = config.EngineManaged
			cfg.EngineDevice = tc.saved
			h, _ := newTestHost(t, &fakeEngine{}, cfg)

			probed := 0
			h.probe = func(context.Context, deviceinfo.Options) (deviceinfo.Report, error) {
				probed++
				if tc.probeErr {
					return deviceinfo.Report{}, errors.New("no engine to ask")
				}
				return tc.report, nil
			}

			got, why := h.presetDevice(context.Background(), h.settings.Get())
			if got != tc.want {
				t.Errorf("device = %q, want %q", got, tc.want)
			}
			if (why != "") != tc.wantWhy {
				t.Errorf("why = %q, want a reason: %v", why, tc.wantWhy)
			}
			if tc.noProbe && probed != 0 {
				t.Errorf("looked up a device name %d times when the answer is not a lookup", probed)
			}
		})
	}
}

// TestPresetIsWrittenWhereItWasToldAndNotBesideTheEngine pins where the model servers'
// configuration lands.
//
// It used to live in <data>/bin/presets, beside the llama-server that reads it, which read as
// tidy until "Remove" was pressed: that button deletes the engine directory, and with it the
// one file where a person's hand-edited cache-ram or ctx-size existed — and that file *is*
// the configuration, so deleting it with the binary deletes a fact the app still
// believes. The app is told the path now, and the engine directory is a sibling of the data
// directory below — so a regression back to filepath.Join(engine.Dir(), "presets") cannot pass
// by both happening to sit under one tree.
func TestPresetIsWrittenWhereItWasToldAndNotBesideTheEngine(t *testing.T) {
	dir := t.TempDir()
	engDir := filepath.Join(dir, "bin") // where Remove deletes to
	modelsDir := filepath.Join(dir, "models")
	presetPath := filepath.Join(dir, "presets", "libris.ini")

	// One complete model on disk, or the start refuses for a better reason than this tests.
	completeModel(t, modelsDir)

	srv := &fakeServer{status: engine.Serving{Running: true, Port: 8080}}
	eng := &fakeEngine{
		installed: &engine.Installed{Tag: "b1", Path: filepath.Join(engDir, "b1", "llama-server")},
		dir:       engDir,
	}
	h, _ := newTestHostWith(t, eng, config.Config{EngineMode: config.EngineManaged, EnginePort: 8080}, HostOptions{
		Serve:      srv,
		ModelsDir:  modelsDir,
		PresetPath: presetPath,
	})

	if _, err := h.EngineServeStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.opts.Preset != presetPath {
		t.Errorf("Preset = %q, want the path the app was given (%q)", srv.opts.Preset, presetPath)
	}
	if strings.HasPrefix(srv.opts.Preset, engDir+string(filepath.Separator)) {
		t.Errorf("the preset %q is under the engine directory %q — Remove deletes that tree",
			srv.opts.Preset, engDir)
	}
}

// TestPresetPathIsUnderTheDataDirectory checks the locator itself: cmd computes it from the
// -data flag, and nothing else in this repo would notice a wrong answer until a button
// deleted a file nobody expected to lose.
func TestPresetPathIsUnderTheDataDirectory(t *testing.T) {
	got := engine.PresetPath("/data/libris", engine.RouterID)
	if want := filepath.Join("/data/libris", "presets", "libris.ini"); got != want {
		t.Errorf("PresetPath = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, filepath.Join("/data/libris", "bin")) {
		t.Errorf("%q is under bin/, which the engine owns and Remove deletes", got)
	}
}

// The address the page is told is the address a request would succeed against. Under Auto the
// stored setting is 0 and says nothing about the running process, so the process answers. With
// nothing running, Auto names no address at all rather than a port that does not exist yet —
// while a pinned one is still worth showing, because that is what libris would bind.
func TestEngineEndpointFollowsTheRunningProcess(t *testing.T) {
	cases := []struct {
		name     string
		stored   int
		serving  engine.Serving
		want     string
		wantPort int
	}{
		{"auto, running on a port nobody chose", 0,
			engine.Serving{Running: true, Port: 41234}, "http://127.0.0.1:41234", 41234},
		{"auto, nothing running", 0, engine.Serving{}, "", 0},
		{"pinned, nothing running", 9000, engine.Serving{}, "http://127.0.0.1:9000", 9000},
		// The record wins over the setting even when both name a number: the setting is what
		// was asked for, the record is what answered, and an answer came out of the process.
		// The port line follows the same rule as the address line — it used to read the stored
		// number while the address came from the process, so the page contradicted itself.
		{"pinned, running on another port", 9000,
			engine.Serving{Running: true, Port: 41234}, "http://127.0.0.1:41234", 41234},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &fakeEngine{dir: t.TempDir()}
			h, _ := newTestHostWith(t, eng,
				config.Config{EngineMode: config.EngineManaged, EnginePort: tc.stored},
				HostOptions{Serve: &fakeServer{status: tc.serving}})

			v, err := h.Engine(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			if v.Endpoint != tc.want {
				t.Errorf("Endpoint = %q, want %q", v.Endpoint, tc.want)
			}
			if v.Port != tc.wantPort {
				t.Errorf("Port = %d, want %d — the page must name the process it is describing", v.Port, tc.wantPort)
			}
			if got := v.Aliases; len(got) != 2 || got[0] != "libris-chat" || got[1] != "libris-embed" {
				t.Errorf("Aliases = %v, want the two names from the preset", got)
			}
		})
	}
}

// A start on Auto must not be told to move to another port: the setting already says to ask,
// so the only thing a person can do is stop what is running. And the port in the sentence is
// the one the running process holds, which is the fact they need in order to stop the right
// thing.
func TestAlreadyRunningSaysWhatToStop(t *testing.T) {
	serve := &fakeServer{status: engine.Serving{Running: true, Port: 41234}, startErr: engine.ErrAlreadyServing}
	dir := t.TempDir()
	modelsDir := filepath.Join(dir, "models")
	completeModel(t, modelsDir)
	eng := &fakeEngine{
		dir:       filepath.Join(dir, "bin"),
		installed: &engine.Installed{Tag: "b1", Path: "/tmp/llama-server"},
	}
	for _, tc := range []struct {
		stored int
		want   string
		deny   string
	}{{0, "41234", "Settings"}, {9000, "41234", ""}} {
		h, _ := newTestHostWith(t, eng,
			config.Config{EngineMode: config.EngineManaged, EnginePort: tc.stored},
			HostOptions{Serve: serve, ModelsDir: modelsDir})
		_, err := h.EngineServeStart(context.Background())
		if !errors.Is(err, ErrEngineRunning) {
			t.Fatalf("stored port %d: %v, want ErrEngineRunning", tc.stored, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("stored port %d said %q, which does not name the port in use", tc.stored, err)
		}
		if tc.deny != "" && strings.Contains(err.Error(), tc.deny) {
			t.Errorf("stored port %d said %q — Auto has no port to change in Settings", tc.stored, err)
		}
	}
}

// completeModel puts one complete catalogue entry on disk, because EngineServeStart refuses to
// start a router with nothing to run — and that refusal is worth more than what these tests
// would otherwise see. "Complete" means each part is exactly the size the manifest says and
// has its hash sibling, so these files are sparse: truncate costs no blocks and no time, and
// the app's only question is whether the sizes agree and the digest was ever checked.
func completeModel(t *testing.T, modelsDir string) {
	t.Helper()

	md := filepath.Join(modelsDir, "lfm3-vl-2b")
	if err := os.MkdirAll(md, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		name  string
		bytes int64
	}{
		{"LFM3-VL-2B-QAD-Q4_0.gguf", 1_049_615_648},
		{"mmproj-LFM3-VL-2B-BF16.gguf", 378_996_032},
	} {
		path := filepath.Join(md, p.name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(p.bytes); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := models.MarkVerified(path, "0000000000000000000000000000000000000000000000000000000000000000"); err != nil {
			t.Fatal(err)
		}
	}
}
