package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"libris/internal/config"
	"libris/internal/deviceinfo"
	"libris/internal/engine"
)

// fakeEngine is the manager without a network, a disk, or a GPU. Every branch the page can
// reach is one of its fields, because the interesting states of this screen are the ones that
// involve something going wrong: offline, busy, half-downloaded, running elsewhere.
type fakeEngine struct {
	installed *engine.Installed
	rel       engine.Release
	asset     engine.Asset
	checkErr  error
	prog      engine.Progress
	dir       string
	parts     int64

	checks     int
	forced     int
	installed_ []string // tags Install was asked for
	proxies    []string
	removed    int
	removeErr  error
	discarded  int
	stopped    int
}

func (f *fakeEngine) Status() (engine.Installed, bool) {
	if f.installed == nil {
		return engine.Installed{}, false
	}
	return *f.installed, true
}

func (f *fakeEngine) Check(ctx context.Context, b engine.Backend, force bool) (engine.Release, engine.Asset, error) {
	f.checks++
	if force {
		f.forced++
	}
	if f.checkErr != nil {
		return engine.Release{}, engine.Asset{}, f.checkErr
	}
	return f.rel, f.asset, nil
}

func (f *fakeEngine) SetProxy(prefix string) { f.proxies = append(f.proxies, prefix) }

func (f *fakeEngine) Install(ctx context.Context, tag string, b engine.Backend) (engine.Installed, error) {
	f.installed_ = append(f.installed_, tag)
	if f.installed == nil {
		return engine.Installed{}, errors.New("fake: nothing to install")
	}
	return *f.installed, nil
}

func (f *fakeEngine) Remove(context.Context) error {
	f.removed++
	if f.removeErr != nil {
		return f.removeErr
	}
	f.installed = nil
	return nil
}
func (f *fakeEngine) Stop()                     { f.stopped++ }
func (f *fakeEngine) Progress() engine.Progress { return f.prog }
func (f *fakeEngine) Dir() string               { return f.dir }
func (f *fakeEngine) PartsBytes() int64         { return f.parts }
func (f *fakeEngine) ClearParts() error         { f.discarded++; return nil }

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
	}
	h, _ := newTestHost(t, eng, config.Default())

	v, err := h.Engine(context.Background(), false)
	if err != nil {
		t.Fatalf("offline: %v", err)
	}
	// The page's primary fact survives an outage. Losing the whole panel because GitHub did
	// not answer is the failure this assertion defends.
	if v.Installed == nil || v.Installed.Tag != "b12600" {
		t.Errorf("installed = %+v, want the row from disk", v.Installed)
	}
	if v.CheckError == "" {
		t.Error("an offline check reported no error, so the page would say nothing is wrong")
	}
	if v.Update {
		t.Error("offeline, an update was claimed")
	}
	if v.Dir != "/data/bin" {
		t.Errorf("dir = %q, want the directory a download would use", v.Dir)
	}
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
			installed: &engine.Installed{Tag: c.installed, Backend: engine.Vulkan},
			rel:       engine.Release{Tag: c.latest},
			asset:     engine.Asset{Name: "asset.tar.gz", Size: 35 << 20},
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
		rel:       engine.Release{Tag: "b12649"},
		asset:     engine.Asset{Name: "asset.tar.gz"},
		installed: &engine.Installed{Tag: "b12649", Backend: engine.Vulkan},
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
	if len(eng.installed_) != 1 || eng.installed_[0] != "b12649" {
		t.Errorf("Install was asked for %v, want the newest tag", eng.installed_)
	}
	if len(eng.proxies) != 1 || eng.proxies[0] != cfg.EngineProxy {
		t.Errorf("the proxy set on the manager was %v, want the one in settings — a download that used a proxy frozen at boot makes the settings page a lie", eng.proxies)
	}
	// The engine is a device probe once it exists, so the cached report about the machine has
	// to go: otherwise the page keeps saying what the machine could run *before* the build
	// that knows arrived.
	if _, err := os.Stat(cache); !errors.Is(err, os.ErrNotExist) {
		t.Error("installing the engine did not invalidate the device report")
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
	if eng.discarded != 1 || eng.removed != 0 {
		t.Errorf("discarded=%d removed=%d, want discarding a stopped download to leave the engine alone",
			eng.discarded, eng.removed)
	}
	h.EngineStop()
	if eng.stopped != 1 {
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
