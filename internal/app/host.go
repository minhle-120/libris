package app

// The machine libris is running on.
//
// Everything else in this package is about the user's documents. This file is about the
// computer: which llama.cpp build is on this disk, which one upstream would offer, how the
// download went, and what the machine has to run a model on. They arrive together because a
// person opens the settings page to answer one question — "will this work here" — and the
// answer needs all four.
//
// Both halves are behind interfaces declared here, so the HTTP transport and the later MCP
// server call the same implementation, and so a test can drive the whole page with a fake
// that has no network and no GPU.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"libris/internal/config"
	"libris/internal/deviceinfo"
	"libris/internal/engine"
	"libris/internal/models"
)

// deviceCacheAge bounds how long a device report stays reusable without asking again. The
// fingerprint catches a reboot and a new engine build; this catches what neither can see — a
// driver updated in place, a card moved slot — and keeps the settings page from running a
// subprocess on every visit.
const deviceCacheAge = 24 * time.Hour

// checkBudget is how long a background update check may run before it is given up on. It is
// longer than the manager's own HTTP deadline on purpose: this is the outer bound, and the
// inner one produces the sentence the page shows.
const checkBudget = 30 * time.Second

// checkRetry is how long a failed update check is left alone. Without it, a machine that is
// offline sees a fresh attempt on every poll of the settings page — the page would say
// "checking" forever, and something with a captive portal would spend the day hammering a
// server that is not answering. A deliberate "Check for updates" ignores this.
const checkRetry = 5 * time.Minute

// EngineManager is what Host needs from the binary manager — the whole of it, because every
// method here is a button on the page. engine.Manager satisfies it; a test does not have to.
type EngineManager interface {
	Status() (engine.Installed, bool)
	// Known is the cached answer, without asking anyone. It is what lets this page load at
	// the speed of the local disk.
	Known(b engine.Backend) (engine.Release, engine.Asset, bool)
	Check(ctx context.Context, b engine.Backend, force bool) (engine.Release, engine.Asset, error)
	Install(ctx context.Context, tag string, b engine.Backend) (engine.Installed, error)
	Remove(ctx context.Context) error
	Stop()
	Progress() engine.Progress
	Dir() string
	PartsBytes() int64
	ClearParts() error
}

// HostOptions is the wiring. DeviceCache is a file path under the data directory; an empty
// one means the report is produced every time and never remembered.
type HostOptions struct {
	Engine      EngineManager
	Settings    Settings
	DeviceCache string
	Logger      *slog.Logger

	// Serve and ModelsDir are the managed half (ADR 0020): the supervisor that owns libris's
	// own llama-server, and the directory its models come from. Either may be nil/empty in a
	// build without a data directory; managed mode then says so rather than pretending.
	Serve     *engine.Supervisor
	ModelsDir string

	// Probe asks the installed engine what it can run on. It is a field so the caching around it can be tested on a
	// machine with neither a GPU nor permission to run anything; nil means the real one.
	Probe func(context.Context, deviceinfo.Options) (deviceinfo.Report, error)
}

type Host struct {
	engine    EngineManager
	settings  Settings
	devCache  string
	serve     *engine.Supervisor
	modelsDir string
	probe     func(context.Context, deviceinfo.Options) (deviceinfo.Report, error)
	log       *slog.Logger
	now       func() time.Time

	// The update check is a background act, so its state is process state: one in flight, and
	// the sentence from the last one that failed. Guarded because the page can be open in two
	// tabs, which is two pollers.
	mu          sync.Mutex
	checking    bool
	lastErr     string
	lastAttempt time.Time
}

func NewHost(opts HostOptions) (*Host, error) {
	if opts.Engine == nil {
		return nil, errors.New("app: Host needs an EngineManager")
	}
	if opts.Settings == nil {
		return nil, errors.New("app: Host needs Settings")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	probe := opts.Probe
	if probe == nil {
		probe = func(ctx context.Context, o deviceinfo.Options) (deviceinfo.Report, error) {
			return deviceinfo.Detect(ctx, o), nil
		}
	}

	return &Host{
		engine:    opts.Engine,
		settings:  opts.Settings,
		devCache:  opts.DeviceCache,
		serve:     opts.Serve,
		modelsDir: opts.ModelsDir,
		probe:     probe,
		log:       log,
		now:       func() time.Time { return time.Now().UTC() },
	}, nil
}

// EngineView is the page's whole answer about the binaries. It is a view rather than
// engine.Manager's own types because the three questions the page asks do not line up with
// the three the manager answers, and a frontend should not have to assemble them.
type EngineView struct {
	// Mode is which half of this section the page is showing: "managed" when libris runs
	// llama.cpp, "external" when somebody else does. It is in the view because a page that
	// inferred it from the settings response could disagree with the server about whether
	// there is a device list to show at all (ADR 0020).
	Mode string `json:"mode"`

	// Endpoint, Port and Aliases are what managed mode derives instead of asking someone to
	// type: one loopback address, one port, three names from the preset libris wrote. Served
	// rather than stored, so the page cannot show an address that disagrees with the process.
	Endpoint string   `json:"endpoint,omitempty"`
	Port     int      `json:"port,omitempty"`
	Aliases  []string `json:"aliases,omitempty"`

	// Serve is libris's own llama-server. Absent in external mode, where there is nothing
	// here to run — and saying "stopped" about a server this mode never starts is exactly
	// the kind of field that made the old page confusing.
	Serve *engine.Serving `json:"serve,omitempty"`

	// Models are the GGUFs on this disk that libris knows how to use, in role order. A role
	// with no file is absent rather than empty, so the page can offer the pattern to put one
	// there.
	Models []ModelView `json:"models,omitempty"`

	// Installed is the copy on this disk, or nil — which is the state a first run is in, and
	// has to be able to say so out loud rather than as an empty object.
	Installed *engine.Installed `json:"installed,omitempty"`
	Backend   string            `json:"backend"`

	// Latest is what upstream would offer for this platform. Absent when the machine is
	// offline and nothing was cached: CheckError then says why in one sentence.
	Latest     *engine.Release `json:"latest,omitempty"`
	Asset      *engine.Asset   `json:"asset,omitempty"`
	Update     bool            `json:"update_available"`
	CheckError string          `json:"check_error,omitempty"`

	// Checking means the answer about what is newest is not in yet, and will be: the check
	// runs in the background and the page asks again. Without it the absence of Latest reads
	// as "we could not find out", which is a different claim and a dead end for the reader.
	Checking bool `json:"checking,omitempty"`

	Progress engine.Progress `json:"progress"`

	// Dir and PartsBytes are shown so the space a download will use, and the space a
	// cancelled one is still holding, are numbers on the screen rather than guesses.
	Dir        string `json:"dir"`
	PartsBytes int64  `json:"parts_bytes"`
}

// Engine reads the current state. force asks upstream even if the cached answer is young —
// the difference between opening the page and pressing "Check".
//
// A failed update check is not an error here. The page's primary fact is what is installed,
// and that is knowable offline; losing the whole panel because api.github.com did not answer
// would be a worse page than one with a sentence where the update button was.
func (h *Host) Engine(ctx context.Context, force bool) (*EngineView, error) {
	cfg := h.settings.Get()
	// One build for every machine: Vulkan presents itself on NVIDIA, AMD and Intel alike, and
	// on pure CPU when no Vulkan device will open. There is no setting here because there is
	// no choice to make (ADR 0020) — which is the same reason ADR 0019 made the engine the
	// only device probe.
	const backend = engine.Vulkan

	v := &EngineView{
		Mode:       string(cfg.EngineMode),
		Backend:    string(backend),
		Endpoint:   h.endpoint(cfg),
		Dir:        h.engine.Dir(),
		Progress:   h.engine.Progress(),
		PartsBytes: h.engine.PartsBytes(),
	}
	if cfg.EngineMode == config.EngineManaged {
		v.Port = cfg.EnginePort
		v.Aliases = aliasNames
		v.Models = h.modelViews()
		if h.serve != nil {
			srv := h.serve.Status()
			v.Serve = &srv
		} else {
			v.Serve = &engine.Serving{Port: cfg.EnginePort, Error: "This build has no data directory to run an engine from."}
		}
	}
	if in, ok := h.engine.Status(); ok {
		have := in
		v.Installed = &have
		v.Backend = string(in.Backend)
	}

	rel, asset, known := h.engine.Known(backend)
	if known {
		v.Latest = &rel
		v.Asset = &asset
		// Comparison is numeric, not lexical: "b9999" sorts after "b12649" as a string and is
		// the older build, and an app that offered that as an update would be offering a
		// downgrade.
		if v.Installed != nil && engine.CompareTags(v.Installed.Tag, rel.Tag) < 0 {
			v.Update = true
		}
	}
	h.startCheck(backend, force, !known)
	// The last failure, if there was one. It stays until a check succeeds, because a page
	// that silently goes back to showing nothing explains nothing about the download button
	// that stopped appearing.
	//
	// Checking reports the state, not this call's contribution to it. Reading only whether
	// *this* call started a check made a polling client see true once and false forever —
	// which is not "still working", it is a lie that the answer is final.
	h.mu.Lock()
	v.Checking = h.checking
	v.CheckError = h.lastErr
	h.mu.Unlock()
	return v, nil
}

// startCheck asks upstream in the background, at most once at a time.
//
// The settings page does not wait for this. A measured round trip to the release API on a
// real network here is seven seconds, and a page whose first paint is somebody else's server
// is a page that shows a spinner on a machine that answered instantly. So the view reports
// what is on disk now, says a check is running, and the browser asks again — the same shape
// the ingest queue already uses, for the same reason.
// force is whether a person asked; needed is whether the page has nothing to show. They are
// separate because the backoff below answers only to the first: an ordinary visit may be
// deferred, a button press may not.
func (h *Host) startCheck(b engine.Backend, force, needed bool) bool {
	h.mu.Lock()
	switch {
	case h.checking:
		h.mu.Unlock()
		return false
	case !force && !needed:
		h.mu.Unlock()
		return false
	case !force && !h.lastAttempt.IsZero() && h.now().Sub(h.lastAttempt) < checkRetry:
		// Recently failed, and nobody asked for another go. The page still shows the failure
		// sentence and the button that retries it.
		h.mu.Unlock()
		return false
	}
	h.checking = true
	h.lastAttempt = h.now()
	h.mu.Unlock()

	go func() {
		// Not the request's context: the check belongs to the machine's state, and a browser
		// that reloads the page while it is in flight must not abort it and start another.
		ctx, cancel := context.WithTimeout(context.Background(), checkBudget)
		defer cancel()

		_, _, err := h.engine.Check(ctx, b, true)

		h.mu.Lock()
		h.checking = false
		switch {
		case err != nil:
			h.lastErr = err.Error()
			h.log.Debug("engine: update check failed", "err", err)
		default:
			h.lastErr = ""
		}
		h.mu.Unlock()
	}()
	return true
}

// EngineInstall fetches a tag the page offered. An empty tag means "whatever is newest",
// which is the label on the one button, so it is resolved here rather than in a browser that
// cannot see the release list.
func (h *Host) EngineInstall(ctx context.Context, tag string) (engine.Installed, error) {
	const backend = engine.Vulkan

	if tag == "" {
		rel, _, ok := h.engine.Known(backend)
		if !ok {
			// Nothing on disk to offer. Ask once, here, where an answer is required — the
			// page keeps its Download button disabled until it has a release to name, so this
			// is the rare path and not the common one.
			asked, _, err := h.engine.Check(ctx, backend, false)
			if err != nil {
				return engine.Installed{}, err
			}
			rel = asked
		}
		tag = rel.Tag
	}
	in, err := h.engine.Install(ctx, tag, backend)
	if err != nil {
		return engine.Installed{}, err
	}
	// The engine is the only thing worth asking about the machine, so this is the moment a
	// report about it becomes possible at all — and the cached answer, if there was one, is
	// now about a build that is no longer here.
	h.invalidateDevices("engine installed")
	return in, nil
}

// EngineStop interrupts a download. The partial file is kept on purpose — Stop is usually
// "not now", and starting the 35 MB again is not what that means.
func (h *Host) EngineStop() { h.engine.Stop() }

// EngineRemove deletes the installed build. The error it returns is the one the page shows,
// which matters because the interesting case is the refusal: a running llama-server holding
// the files, where attempting the delete is worse than declining it.
func (h *Host) EngineRemove(ctx context.Context) error {
	err := h.engine.Remove(ctx)
	if err != nil {
		return err
	}
	h.invalidateDevices("engine removed")
	return nil
}

// EngineDiscardParts throws away a stopped download's bytes — the button that means "I did
// mean cancel", as distinct from Stop, which means "not now".
func (h *Host) EngineDiscardParts() error { return h.engine.ClearParts() }

// Devices reports what this machine can run a model on, from the cache if it still describes
// the machine.
//
// The order is the cheap one: the cache key is this boot and this engine build, so a stale
// entry is recognised without running anything. Only then is the engine asked — and with
// nothing installed it cannot be asked at all, which the report says instead of guessing
// from what the operating system exposes (ADR 0019).
func (h *Host) Devices(ctx context.Context, refresh bool) (deviceinfo.Report, error) {
	probe := h.engineProbe()
	fp := deviceinfo.Fingerprint(probe)

	if !refresh && h.devCache != "" {
		if rep, ok := deviceinfo.Load(h.devCache, fp, deviceCacheAge, h.now); ok {
			return rep, nil
		}
	}

	rep, err := h.probe(ctx, probe)
	if err != nil {
		return deviceinfo.Report{}, err
	}
	// The stamp belongs here, not in the probe. A probe reports what it saw; *when* it
	// saw it, and which machine configuration the answer describes, are properties of the
	// cache entry, and a probe that filled them in itself could not be tested against a
	// clock.
	rep.Fingerprint = fp
	if rep.CheckedAt.IsZero() {
		rep.CheckedAt = h.now()
	}
	if h.devCache != "" {
		if err := deviceinfo.Save(h.devCache, rep); err != nil {
			// A cache that cannot be written is not a failure of the question. The report is
			// still the answer, and the next visit just asks again.
			h.log.Debug("could not cache the device report", "path", h.devCache, "err", err)
		}
	}
	return rep, nil
}

// engineProbe is who to ask: the installed engine's own path, which is the only thing worth
// asking — the same binary libris will later start, reporting the same backends it will
// start them with — plus its tag, so a new download is a different cache entry rather than
// an old answer about a build that is no longer there. An empty Options means no engine, and
// deviceinfo has a sentence for that.
func (h *Host) engineProbe() deviceinfo.Options {
	in, ok := h.engine.Status()
	if !ok {
		return deviceinfo.Options{}
	}
	return deviceinfo.Options{EngineBin: in.Path, EngineTag: in.Tag}
}

func (h *Host) invalidateDevices(because string) {
	if h.devCache == "" {
		return
	}
	if err := os.Remove(h.devCache); err != nil && !errors.Is(err, os.ErrNotExist) {
		h.log.Debug("could not invalidate the device cache", "because", because, "err", err)
		return
	}
	h.log.Debug("device cache invalidated", "because", because)
}

// SettingsChanged reports whether a saved settings change means the machine section should be
// refetched: the mode decides which half of the page is showing, and the port decides what
// libris would bind if it ran the server.
func (h *Host) SettingsChanged(prev, next config.Config) bool {
	return prev.EngineMode != next.EngineMode ||
		prev.EnginePort != next.EnginePort
}

// ModelView is one GGUF on this disk, as the page shows it.
type ModelView struct {
	Role   string `json:"role"`
	Alias  string `json:"alias"`
	File   string `json:"file"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	MMProj string `json:"mmproj,omitempty"`
}

// aliasNames is what managed mode fills the model configuration in as. Three names libris
// chose, rather than three fields asking a person to copy what the preset already says.
var aliasNames = []string{
	string(engine.AliasChat),
	string(engine.AliasEmbed),
	string(engine.AliasRerank),
}

// endpoint is the one address managed mode talks to. External mode has none: the three
// addresses are in the settings the user typed, and inventing a single one there would be
// the old confusion in a new place.
func (h *Host) endpoint(cfg config.Config) string {
	if cfg.EngineMode != config.EngineManaged {
		return ""
	}
	return engine.Endpoint("127.0.0.1", cfg.EnginePort)
}

// modelViews reads the models directory. A directory that does not exist is not an error:
// "no models yet, and here is where one would go" is a state the page has to be able to show.
func (h *Host) modelViews() []ModelView {
	if h.modelsDir == "" {
		return nil
	}
	var out []ModelView
	for _, m := range models.Scan(h.modelsDir) {
		out = append(out, ModelView{
			Role: string(m.Role), Alias: string(m.Alias), File: m.Name,
			Path: m.Path, Bytes: m.Size, MMProj: m.MMProj,
		})
	}
	return out
}

// ErrEngineMode is a request that belongs to the other half of the switch — a start asked for
// while libris is configured to talk to someone else's servers. It is ErrInvalid-shaped
// because the fix is a setting, not a retry.
var ErrEngineMode = errors.New("app: libris is set to talk to servers you run yourself")

// EngineServe starts libris's own llama-server: one router process, every model on disk, one
// port (ADR 0020). It blocks until the router answers or the wait runs out, because a page
// that said "running" about a process that dies while loading a GGUF would be a page that
// lies — and the first load of a 3 GB model is exactly when it does.
func (h *Host) EngineServeStart(ctx context.Context) (engine.Serving, error) {
	cfg := h.settings.Get()
	if cfg.EngineMode != config.EngineManaged {
		return engine.Serving{}, ErrEngineMode
	}
	if h.serve == nil {
		return engine.Serving{}, fmt.Errorf("%w: this build has no data directory", ErrEngineMode)
	}
	inst, ok := h.engine.Status()
	if !ok {
		return engine.Serving{}, fmt.Errorf("%w: download the engine first", ErrInvalid)
	}

	mods := models.Scan(h.modelsDir)
	var specs []engine.Model
	for _, m := range mods {
		kind := engine.KindChat
		switch m.Role {
		case models.RoleEmbed:
			kind = engine.KindEmbed
		case models.RoleRerank:
			kind = engine.KindRerank
		}
		specs = append(specs, engine.Model{Alias: m.Alias, Kind: kind, Path: m.Path, MMProj: m.MMProj})
	}
	if len(specs) == 0 {
		return engine.Serving{}, fmt.Errorf("%w: no model in %s — a chat model named %s",
			ErrInvalid, h.modelsDir, models.Pattern(models.RoleChat))
	}

	srv, err := h.serve.Start(ctx, engine.ServeOptions{
		Bin:             inst.Path,
		Port:            cfg.EnginePort,
		Preset:          filepath.Join(h.engine.Dir(), "presets", "libris.ini"),
		Log:             filepath.Join(h.engine.Dir(), "logs", "server.log"),
		MemoryMarginMiB: memoryMarginMiB,
	}, specs)
	if err != nil {
		h.log.Warn("engine serve", "err", err)
		// A port conflict and a dead model are both the user's to fix, and both are
		// sentences; only an unhandled one should read as a bug.
		if errors.Is(err, engine.ErrAlreadyServing) || errors.Is(err, engine.ErrPortTaken) ||
			errors.Is(err, context.DeadlineExceeded) {
			return srv, fmt.Errorf("%w: %s", ErrBusy, srv.Error)
		}
		return srv, fmt.Errorf("%w: %s", ErrInvalid, srv.Error)
	}
	return srv, nil
}

// EngineAutostart brings the router up on boot when managed mode asked for a server and one
// can be run. It is quiet by design — a debug line on success, a warning on failure — because
// it runs beside the listener, and a machine that cannot start a model server still gets a
// working app and a settings page that says what happened.
func (h *Host) EngineAutostart(ctx context.Context) {
	cfg := h.settings.Get()
	if cfg.EngineMode != config.EngineManaged || h.serve == nil {
		return
	}
	if cur := h.serve.Status(); cur.Running {
		return
	}
	if _, ok := h.engine.Status(); !ok {
		return // nothing downloaded; the page's download button is the answer
	}
	if len(models.Scan(h.modelsDir)) == 0 {
		return // no model to run; the page names the file it is looking for
	}
	srv, err := h.EngineServeStart(ctx)
	if err != nil {
		h.log.Warn("engine: not running; the settings page says why", "err", err, "said", srv.Error)
		return
	}
	h.log.Debug("engine: router up", "port", srv.Port, "models", len(srv.Models))
}

// EngineServeStop stops the process. Stopping is not the same act as cancelling a job or a
// download (AGENTS §11, and the difference EngineStop above makes): the process is ours, and
// the answer to "then stop it" is a signal. Not running is not an error — the button that
// asked has already got the state it wanted.
func (h *Host) EngineServeStop(ctx context.Context) error {
	if h.serve == nil {
		return fmt.Errorf("%w: this build has no data directory", ErrEngineMode)
	}
	if err := h.serve.Stop(); err != nil && !errors.Is(err, engine.ErrNotServing) {
		return fmt.Errorf("app: stop the engine: %w", err)
	}
	return nil
}

// ServeStatus reports libris's own server, for the page that shows whether anything is
// running at all.
func (h *Host) ServeStatus() engine.Serving {
	if h.serve == nil {
		return engine.Serving{}
	}
	return h.serve.Status()
}

// memoryMarginMiB is what the router keeps free on each device before unloading the least
// recently used model. It is a constant rather than a setting because it is not a preference
// and not an ability libris applies — it is a number chosen for three models on a small card,
// and a settings field would imply the page knows what the card can hold (ADR 0011).
const memoryMarginMiB = 512
