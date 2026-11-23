package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"libris/internal/config"
	"libris/internal/deviceinfo"
	"libris/internal/engine"
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
	proxies   []string
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

func (f *fakeEngine) SetProxy(prefix string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proxies = append(f.proxies, prefix)
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
func (f *fakeEngine) seen() (checks, forced, removed, discarded, stopped int, tags, proxies []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.forced, f.removed, f.discarded, f.stopped,
		append([]string(nil), f.tags...), append([]string(nil), f.proxies...)
}

// hostSettings is the one method of the Settings interface these tests care about, held as a
// value so a test can change the build selection and see the effect on what is fetched.
type hostSettings struct{ cfg config.Config }

func (h *hostSettings) Get() config.Config { return h.cfg }

func newTestHost(t *testing.T, eng EngineManager, cfg config.Config) (*Host, string) {
	t.Helper()
	dir := t.TempDir()
	cache := filepath.Join(dir, "devices.json")
	h, err := NewHost(HostOptions{
		Engine:      eng,
		Settings:    &hostSettings{cfg: cfg},
		DeviceCache: cache,
		Probe: func(context.Context, deviceinfo.Options) (deviceinfo.Report, error) {
			return deviceinfo.Report{
				OS:      "linux",
				Arch:    "amd64",
				Devices: []deviceinfo.Device{{Kind: "gpu", Name: "Vulkan0", Description: "NVIDIA GeForce RTX 3070", Source: "llama-server"}},
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

func TestEngineInstallResolvesTheTagAndCarriesTheSettings(t *testing.T) {
	eng := &fakeEngine{
		knownOK:    true,
		knownRel:   engine.Release{Tag: "b12649"},
		knownAsset: engine.Asset{Name: "asset.tar.gz"},
		installed:  &engine.Installed{Tag: "b12649", Backend: engine.Vulkan},
	}
	cfg := config.Default()
	cfg.EngineProxy = "http://mirror.internal:3128"
	cfg.EngineBackend = "cpu"
	h, cache := newTestHost(t, eng, cfg)

	// The install was asked for with no tag — the label on the button is "Download", not a
	// version — so the tag has to be resolved here, and the backend that decided it is the
	// one in settings, not the compiled default.
	if _, err := h.EngineInstall(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _, tags, proxies := eng.seen()
	if len(tags) != 1 || tags[0] != "b12649" {
		t.Errorf("Install was asked for %v, want the newest tag", tags)
	}
	if len(proxies) != 1 || proxies[0] != cfg.EngineProxy {
		t.Errorf("the proxy set on the manager was %v, want the one in settings — a download that used a proxy frozen at boot makes the settings page a lie", proxies)
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
	checks, _, _, _, _, _, _ := eng.seen()
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
	_, forced, _, _, _, _, _ := eng.seen()
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
	if checks, _, _, _, _, _, _ := eng.seen(); checks != 1 {
		t.Errorf("%d checks for 13 visits, want the failure left alone", checks)
	}

	// Pressing the button is a different act, and ignores the backoff.
	if _, err := h.Engine(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	settled(t, h)
	if checks, forced, _, _, _, _, _ := eng.seen(); checks != 2 || forced != 2 {
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
					Devices:     []deviceinfo.Device{{Kind: "gpu", Name: "Vulkan0", Source: "sysfs"}},
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
		t.Errorf("probes = %d, want ?refresh=1 to run the collectors again", probes)
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
	// The installed engine is the best probe there is, so its path is handed to the collectors.
	// A test that asserted this on the *result* would be asserting a merge rule that belongs
	// to deviceinfo; what belongs to Host is that it passes the path through at all.
	eng := &fakeEngine{installed: &engine.Installed{Tag: "b12649", Path: "/data/bin/b12649/bin/llama-server"}}

	var seen string
	h, err := NewHost(HostOptions{
		Engine: eng, Settings: &hostSettings{cfg: config.Default()},
		Probe: func(_ context.Context, o deviceinfo.Options) (deviceinfo.Report, error) {
			seen = o.EngineBin
			return deviceinfo.Report{OS: "linux"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Devices(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if seen != "/data/bin/b12649/bin/llama-server" {
		t.Errorf("the collectors were given %q, want the installed engine", seen)
	}

	// With no engine, they are given nothing, and no attempt is made to run a binary that
	// is not there.
	eng.installed = nil
	if _, err := h.Devices(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if seen != "" {
		t.Errorf("with no engine installed the collectors were given %q", seen)
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
	_, _, removed, discarded, stopped, _, _ := eng.seen()
	if discarded != 1 || removed != 0 {
		t.Errorf("discarded=%d removed=%d, want discarding a stopped download to leave the engine alone",
			discarded, removed)
	}
	h.EngineStop()
	_, _, _, _, stopped, _, _ = eng.seen()
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
	next.EngineBackend = "cpu"
	if !h.SettingsChanged(prev, next) {
		t.Error("changing the build selection did not, so the page keeps offering the other asset")
	}
	proxied := config.Default()
	proxied.EngineProxy = "http://mirror:3128"
	if !h.SettingsChanged(prev, proxied) {
		t.Error("changing the mirror did not, and the size shown is the mirror's file")
	}
}
