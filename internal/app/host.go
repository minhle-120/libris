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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"libris/internal/config"
	"libris/internal/deviceinfo"
	"libris/internal/engine"
	"libris/internal/models"
	"libris/internal/serving"
	"libris/internal/setup"
	"libris/internal/tools"
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

// ModelFetcher is what Host needs of the model downloader — four methods, because those are
// the four things the page can do to a download. models.Fetcher satisfies it. It is declared
// here rather than used as a concrete type for the same reason EngineManager is: a test of
// the settings page must not need a 2.4 GB network.
type ModelFetcher interface {
	Start(models.Entry) error
	Stop()
	Discard(models.Entry) error
	Progress() models.Progress
}

// Server is what Host needs of the thing that owns libris's own llama-server: start it, stop
// it, and ask what it says. engine's Supervisor satisfies it.
//
// It is declared here rather than used as a concrete type because every managed-mode decision
// in this file is a decision *about* that process — which mode may start one, whether a model
// file may be deleted underneath it, whether the preset on disk still describes what is
// loaded — and with the concrete type in the struct, none of them could be tested without a
// llama.cpp binary and a GPU.
type Server interface {
	Start(ctx context.Context, o engine.ServeOptions, models []engine.Model) (engine.Serving, error)
	Stop() error
	Status() engine.Serving
}

// HostOptions is the wiring. DeviceCache is a file path under the data directory; an empty
// one means the report is produced every time and never remembered.
type HostOptions struct {
	Engine      EngineManager
	Settings    Settings
	DeviceCache string
	Logger      *slog.Logger

	// Serve, ModelsDir and Downloads are the managed half: the supervisor that owns
	// libris's own llama-server, the directory its models come from, and the thing that fills
	// that directory. Any of them may be nil/empty in a build without a data directory;
	// managed mode then says so rather than pretending.
	Serve     Server
	ModelsDir string
	// PresetPath is the file libris constructs for llama-server to read: the model servers'
	// configuration, kept in the data directory rather than beside the binary, because Remove
	// deletes the binary and must not delete what a person hand-tuned. The format
	// and the file name are engine's; the app only says where libris keeps its files.
	PresetPath string
	Downloads  ModelFetcher

	// Probe asks the installed engine what it can run on. It is a field so the caching around it can be tested on a
	// machine with neither a GPU nor permission to run anything; nil means the real one.
	Probe func(context.Context, deviceinfo.Options) (deviceinfo.Report, error)

	// Programs resolves the programs libris runs but did not write. It is a resolver rather
	// than a snapshot so the answer is for now: the folder beside the executable can move,
	// and the page that says what is missing should not be quoting a boot from days ago.
	Programs tools.Resolver
}

type Host struct {
	engine     EngineManager
	programs   tools.Resolver
	settings   Settings
	devCache   string
	serve      Server
	presetPath string
	fetch      ModelFetcher
	modelsDir  string
	probe      func(context.Context, deviceinfo.Options) (deviceinfo.Report, error)
	log        *slog.Logger
	now        func() time.Time

	// The update check is a background act, so its state is process state: one in flight, and
	// the sentence from the last one that failed. Guarded because the page can be open in two
	// tabs, which is two pollers.
	mu          sync.Mutex
	checking    bool
	lastErr     string
	lastAttempt time.Time

	// autostart says start-up has taken responsibility for bringing the router up in this
	// process, which is the difference between a router on its way and a router nobody is
	// starting. Unlocked because the page reads it beside the answer it qualifies.
	autostart atomic.Bool
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
		engine:     opts.Engine,
		programs:   opts.Programs,
		settings:   opts.Settings,
		devCache:   opts.DeviceCache,
		serve:      opts.Serve,
		fetch:      opts.Downloads,
		modelsDir:  opts.ModelsDir,
		presetPath: opts.PresetPath,
		probe:      probe,
		log:        log,
		now:        func() time.Time { return time.Now().UTC() },
	}, nil
}

// ToolsView is what this machine provides to run the files libris accepts, and whether it
// brought those programs with itself. It is a separate answer from the engine's because the
// two fail differently: an absent engine is a download button, and an absent pdftotext is
// either a download button or a folder left behind when somebody moved libris — which of the
// two it is depends only on whether this install came with a tools folder, and the page cannot
// tell without being told.
type ToolsView struct {
	// Bundled says this install carries its own folder of programs beside the executable.
	Bundled bool `json:"bundled"`
	// Dir is that folder, so a sentence about a folder can name the one it means.
	Dir      string        `json:"dir,omitempty"`
	Programs []ProgramView `json:"programs"`
}

// ProgramView is one program: what it is called, whether it was found, and where from.
type ProgramView struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	Path   string `json:"path,omitempty"`
}

// Tools reads the machine as it is now, not as it was when libris started: a program can be
// installed, or the folder moved, while the page is open.
func (h *Host) Tools() *ToolsView {
	view := &ToolsView{Bundled: h.programs.BundledHere(), Dir: h.programs.Dir()}
	for _, p := range h.programs.All() {
		view.Programs = append(view.Programs, ProgramView{Name: p.Name, Origin: string(p.Origin), Path: p.Path})
	}
	return view
}

// EngineView is the page's whole answer about the binaries. It is a view rather than
// engine.Manager's own types because the three questions the page asks do not line up with
// the three the manager answers, and a frontend should not have to assemble them.
type EngineView struct {
	// Mode is which half of this section the page is showing: "managed" when libris runs
	// llama.cpp, "external" when somebody else does. It is in the view because a page that
	// inferred it from the settings response could disagree with the server about whether
	// there is a device list to show at all.
	Mode string `json:"mode"`

	// Endpoint, Port and Aliases are what managed mode derives instead of asking someone to
	// type: one loopback address, one port, the two names from the preset libris wrote. All
	// three come from internal/serving, which is also what the client dials through, so the
	// page cannot show an address that disagrees with the process answering questions.
	//
	// Port is the live number when a router is running and the stored one when it is not;
	// showing the setting while a process answers elsewhere is how the page used to end up
	// contradicting its own endpoint line.
	Endpoint string   `json:"endpoint,omitempty"`
	Port     int      `json:"port,omitempty"`
	Aliases  []string `json:"aliases,omitempty"`

	// Setup is whether this machine can answer a question yet, and what is standing in the
	// way — the same answer libris's own start-up obeys, so the screen cannot call a step
	// unfinished that the program has already acted on, or finish one it has not.
	Setup *setup.View `json:"setup,omitempty"`

	// DeviceAckNow is what confirming the device row records today: a key naming this
	// engine build on this machine. The screen sends it back unchanged, which keeps the
	// meaning of "confirmed against what" in one program rather than two.
	DeviceAckNow string `json:"device_ack_now,omitempty"`

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
	// no choice to make — which is the same reason the engine is the only device
	// probe.
	const backend = engine.Vulkan

	// The view wants the same answer the client gets, including the part that is only
	// true when nothing is running — "stopped, and it would live here" — so the error is
	// read as a fact about the router rather than a failure of this request.
	served, _ := serving.Endpoints(h.serve, cfg)

	v := &EngineView{
		Mode:       string(cfg.EngineMode),
		Backend:    string(backend),
		Endpoint:   served.Advertised,
		Dir:        h.engine.Dir(),
		Progress:   h.engine.Progress(),
		PartsBytes: h.engine.PartsBytes(),
	}
	if cfg.EngineMode == config.EngineManaged {
		v.Port = served.Port
		v.Aliases = serving.Aliases()
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
	setupIn, ackNow := h.setupInputs(cfg)
	view := setup.For(setupIn)
	v.Setup = &view
	v.DeviceAckNow = ackNow

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

// EngineInstallStart begins fetching a build the page offered and answers as soon as the
// fetch is underway, with the tag it settled on. An empty tag means "whatever is newest",
// which is the label on the one button, so that is resolved here rather than in a browser
// that cannot see the release list.
//
// The job is started here, by the module that owns the machine, rather than by whoever
// forwarded the request. Two reasons that are really one: a finished install has to
// invalidate the device report, because the cached answer describes the build that was just
// replaced — a step only this module can take; and the model downloads already work this way
// (models.Fetcher.Start answers at once), so a machine whose two long jobs are owned by
// different modules is a machine where shutdown has to know both.
//
// The answer is not the finished engine: 35 MB over hotel wifi is minutes, and the only way
// to end it is EngineStop, so it must not be cancellable by the thing that asked. A closed
// tab, a proxy idle timeout or a laptop lid would otherwise be the act that stops the bytes.
// Progress and any failure are read back from the view the page polls.
func (h *Host) EngineInstallStart(ctx context.Context, tag string) (string, error) {
	const backend = engine.Vulkan

	if tag != "" && !engine.ValidTag(tag) {
		return "", fmt.Errorf("%w: %q is not a llama.cpp nightly tag, which is what a build is named by", ErrInvalid, tag)
	}
	if tag == "" {
		got, err := h.newest(ctx, backend)
		if err != nil {
			return "", err
		}
		tag = got
	}
	// Checked before the goroutine, because the caller is being told what just happened. The
	// manager refuses twice-over anyway — the race this leaves is one request that starts
	// nothing and reads a bar that belongs to the other.
	if p := h.engine.Progress(); p.State == engine.StateDownloading || p.State == engine.StateVerifying || p.State == engine.StateUnpacking {
		return "", fmt.Errorf("%w: another download is still running", ErrBusy)
	}

	go func() {
		// WithoutCancel: the values of this request travel with the job, its cancellation does
		// not. Hanging up is not Stop.
		in, err := h.engineInstall(context.WithoutCancel(ctx), tag, backend)
		if err != nil {
			h.log.Warn("engine install failed", "tag", tag, "err", err)
			return
		}
		h.log.Debug("engine installed", "tag", in.Tag, "path", in.Path)
		// A router that is up is holding the files of the build it loaded, and refuses to be
		// replaced. The page says so; restarting is a thing a person does on purpose.
		if srv := h.serveStatus(); srv.Running {
			h.log.Debug("the running model server still has the old build; a restart is what loads the new one")
		}
	}()
	return tag, nil
}

// engineInstall is the blocking half: fetch, verify, unpack, and put the machine's report
// away afterwards.
func (h *Host) engineInstall(ctx context.Context, tag string, backend engine.Backend) (engine.Installed, error) {
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

// newest is the build the one button would fetch. The cached answer is enough when there is
// one; otherwise this asks once, in the request that needs an answer, rather than starting a
// download pointed at nothing. It says so in three parts, because the three read differently
// to a person watching a button: a check is already running, the check failed and here is why
// — or there is simply no answer and no reason given.
func (h *Host) newest(ctx context.Context, b engine.Backend) (string, error) {
	if rel, _, ok := h.engine.Known(b); ok && rel.Tag != "" {
		return rel.Tag, nil
	}
	h.mu.Lock()
	checking := h.checking
	h.mu.Unlock()
	if checking {
		return "", fmt.Errorf("%w: libris is still finding out which build is newest; try again in a moment", ErrUpdateChecking)
	}
	rel, _, err := h.engine.Check(ctx, b, false)
	if err != nil {
		return "", fmt.Errorf("%w: the newest build could not be determined: %s", ErrUpdateUnknown, err)
	}
	if rel.Tag == "" {
		return "", fmt.Errorf("%w: the newest build could not be determined", ErrUpdateUnknown)
	}
	return rel.Tag, nil
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
// from what the operating system exposes.
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
	Draft  string `json:"draft,omitempty"`
}

// modelViews is what the router would load right now: one row per complete entry. A directory
// that does not exist is not an error — "nothing here yet, and here is where it would go" is
// a state the page has to be able to show.
func (h *Host) modelViews() []ModelView {
	if h.modelsDir == "" {
		return nil
	}
	var out []ModelView
	for _, st := range models.Statuses(h.modelsDir) {
		if !st.Complete {
			continue
		}
		out = append(out, ModelView{
			Role: string(st.Role), Alias: string(st.Role.Alias()), File: filepath.Base(st.Path),
			Path: st.Path, Bytes: st.Bytes(), MMProj: st.MMProjPath, Draft: st.DraftPath,
		})
	}
	return out
}

// ModelPartView is one file of an entry: the weights, or what belongs beside them — a vision
// projector, a draft model.
type ModelPartView struct {
	Name    string `json:"name"`
	Bytes   int64  `json:"bytes"`
	Present bool   `json:"present"`
}

// ModelEntryView is one catalogue entry as the page shows it. The catalogue is what makes the
// download button possible at all: without it the page would be asking a person to paste a
// URL and hope the file is the one their corpus was embedded with.
type ModelEntryView struct {
	ID       string          `json:"id"`
	Role     string          `json:"role"`
	Label    string          `json:"label"`
	Quant    string          `json:"quant"`
	Note     string          `json:"note"`
	Repo     string          `json:"repo"`
	Revision string          `json:"revision"`
	Bytes    int64           `json:"bytes"`
	Dir      string          `json:"dir"`
	Parts    []ModelPartView `json:"parts"`

	// Complete is "every file is here at the right size". Verified is the stronger claim —
	// that libris wrote them and hashed them on the way in — and the difference is not
	// pedantic: a file copied in by hand with a truncated tail would otherwise be called
	// ready, and the router is the thing that would notice.
	Complete bool `json:"complete"`
	Verified bool `json:"verified"`

	// PartialBytes is what a stopped download of *this* entry is holding.
	PartialBytes int64 `json:"partialBytes"`
	// Downloading marks the entry a download is running for, so the row can show one bar
	// rather than the whole card claiming to be busy.
	Downloading bool `json:"downloading"`
}

// ModelsView is the model half of the settings page.
type ModelsView struct {
	Dir          string           `json:"dir"`
	Entries      []ModelEntryView `json:"entries"`
	BytesTotal   int64            `json:"bytesTotal"`
	PartialBytes int64            `json:"partialBytes"`

	// Progress is the running download, or the last one's failure. It names its entry, so a
	// row knows whether the bar belongs to it.
	Progress models.Progress `json:"progress"`
	Serve    *engine.Serving `json:"serve,omitempty"`

	// NeedsRestart says what it sounds like: something complete on disk is not loaded by the
	// running server. Repeating the preset is not enough — the router reads it at start.
	NeedsRestart bool `json:"needsRestart"`
}

// ModelCatalog reads the disk and reports. It is a stat over a handful of files, so it is a page load
// rather than a job: nothing here hashes, and nothing here asks a server anything.
func (h *Host) ModelCatalog() *ModelsView {
	statuses := models.Statuses(h.modelsDir)
	v := &ModelsView{Dir: h.modelsDir, BytesTotal: 0}

	prog := models.Progress{}
	if h.fetch != nil {
		prog = h.fetch.Progress()
	}
	v.Progress = prog

	for _, st := range statuses {
		e := ModelEntryView{
			ID: st.ID, Role: string(st.Role), Label: st.Label, Quant: st.Quant, Note: st.Note,
			Repo: st.Repo, Revision: st.Rev, Bytes: st.Bytes(), Dir: st.Folder,
			Complete: st.Complete, Verified: st.Verified, PartialBytes: st.PartialBytes,
			Downloading: prog.Running() && prog.ID == st.ID,
		}
		present := map[string]bool{}
		for _, name := range st.Have {
			present[name] = true
		}
		for _, p := range st.Parts() {
			e.Parts = append(e.Parts, ModelPartView{Name: p.Name, Bytes: p.Bytes, Present: present[p.Name]})
		}
		v.Entries = append(v.Entries, e)
		v.BytesTotal += e.Bytes
		v.PartialBytes += e.PartialBytes
	}

	if h.serve != nil {
		srv := h.serve.Status()
		v.Serve = &srv
		if srv.Running {
			loaded := map[string]bool{}
			for _, m := range srv.Models {
				loaded[m] = true
			}
			for _, st := range statuses {
				if st.Complete && !loaded[string(st.Role.Alias())] {
					v.NeedsRestart = true
					break
				}
			}
		}
	}
	return v
}

// ErrNoModelsDir is a managed-mode request that this build cannot carry out, because nothing
// pointed it at a data directory. It is phrased as a fact about the build, not about the user.
var ErrNoModelsDir = errors.New("app: this build has no models directory")

// ModelInstall starts a download and answers immediately, with the state it just caused.
//
// It does not hold the request open for the length of the download. A gigabyte body over a
// hotel wifi is an hour, and an hour is longer than any proxy, browser tab or HTTP/2 idle
// timeout is patient about — and a dropped request must not be what stops the bytes, because
// that would make closing a tab the same act as pressing Cancel. So the page polls the
// catalogue instead, which is the arrangement the ingest queue already uses.
func (h *Host) ModelInstall(id string) (*ModelsView, error) {
	e, ok := models.Look(id)
	if !ok {
		return h.ModelCatalog(), fmt.Errorf("%w: no model named %q", ErrInvalid, id)
	}
	if h.fetch == nil {
		return h.ModelCatalog(), fmt.Errorf("%w: download the engine first", ErrInvalid)
	}
	// Downloading is allowed in external mode on purpose. These are files in libris's own
	// data directory; what the mode decides is whether libris *runs* them. Refusing here
	// would make the switch decide whether a person may fill their own disk.
	if err := h.fetch.Start(e); err != nil {
		if errors.Is(err, models.ErrBusy) {
			return h.ModelCatalog(), fmt.Errorf("%w: another model is still downloading", ErrBusy)
		}
		return h.ModelCatalog(), fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	return h.ModelCatalog(), nil
}

// ModelStop interrupts the running download and keeps its bytes. Stop is "not now"; the
// button that means "throw them away" is ModelDiscard, and the difference is the whole point
// of having two.
func (h *Host) ModelStop() *ModelsView {
	if h.fetch != nil {
		h.fetch.Stop()
	}
	return h.ModelCatalog()
}

// ModelDiscard throws away a stopped download's partial bytes for one entry.
func (h *Host) ModelDiscard(id string) (*ModelsView, error) {
	e, ok := models.Look(id)
	if !ok {
		return h.ModelCatalog(), fmt.Errorf("%w: no model named %q", ErrInvalid, id)
	}
	if h.fetch == nil {
		return h.ModelCatalog(), ErrNoModelsDir
	}
	if err := h.fetch.Discard(e); err != nil {
		return h.ModelCatalog(), fmt.Errorf("app: discard the partial download: %w", err)
	}
	return h.ModelCatalog(), nil
}

// ModelRemove deletes an entry's files.
//
// It refuses while the server is up, for the same reason removing the engine does: the running
// llama-server has the file open, and deleting the ground out from under a process is not a
// thing to do because a button was pressed. The answer names the act that would make it
// possible rather than doing it quietly.
func (h *Host) ModelRemove(id string) (*ModelsView, error) {
	e, ok := models.Look(id)
	if !ok {
		return h.ModelCatalog(), fmt.Errorf("%w: no model named %q", ErrInvalid, id)
	}
	if h.modelsDir == "" {
		return h.ModelCatalog(), ErrNoModelsDir
	}
	if h.serve != nil && h.serve.Status().Running {
		return h.ModelCatalog(), fmt.Errorf("%w: the model server is running and has %s open — stop it first",
			ErrBusy, e.Label)
	}
	// A download of this entry has to be interrupted first, or the fetcher would recreate the
	// folder the moment the delete finished.
	if h.fetch != nil {
		if p := h.fetch.Progress(); p.Running() && p.ID == id {
			h.fetch.Stop()
		}
	}
	if err := models.Delete(h.modelsDir, e); err != nil {
		return h.ModelCatalog(), fmt.Errorf("app: remove %s: %w", e.ID, err)
	}
	h.log.Debug("model removed", "id", e.ID, "dir", e.Dir(h.modelsDir))
	return h.ModelCatalog(), nil
}

// ErrEngineMode is a request that belongs to the other half of the switch — a start asked for
// while libris is configured to talk to someone else's servers. It is ErrInvalid-shaped
// because the fix is a setting, not a retry.
var ErrEngineMode = errors.New("app: libris is set to talk to servers you run yourself")

// ErrEngineRunning is a start asked for while libris's own server is already up — which the
// autostart on boot makes an everyday event rather than a race. It is deliberately not ErrBusy:
// that sentence is about the ingest queue, and answering "ingest queue is full" to a person who
// pressed Start on a server they can see is running tells them to wait for something that was
// never running.
var ErrEngineRunning = errors.New("app: the model server is already running")

// ErrEngineStart is a start that got as far as llama.cpp and was refused: somebody else holds
// the port, or a model would not load. The sentence a person can act on is already in
// Serving.Error, taken from the server's own log; this sentinel only says which status code that
// sentence deserves.
var ErrEngineStart = errors.New("app: the model server did not start")

// Managed mode with no server running is serving.ErrNotRunning, not a value from here: the
// fact belongs to the module that asks the process, and a caller that invents its own address
// for a port nobody holds fails as a socket error blaming a server which was never asked.

// EngineServe starts libris's own llama-server: one router process, every model on disk, one
// port. It blocks until the router answers or the wait runs out, because a page
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

	specs := models.Models(models.Statuses(h.modelsDir))
	if len(specs) == 0 {
		return engine.Serving{}, fmt.Errorf("%w: nothing to run in %s — download a model first",
			ErrInvalid, h.modelsDir)
	}

	device, why := h.presetDevice(ctx, cfg)
	if why != "" {
		h.log.Warn("engine: not using the device that was asked for", "why", why)
	}

	srv, err := h.serve.Start(ctx, engine.ServeOptions{
		Bin:    inst.Path,
		Port:   cfg.EnginePort,
		Preset: h.presetPath,
		Log:    filepath.Join(h.engine.Dir(), "logs", "server.log"),
		Device: device,
		// The router decodes a clip of a video with the same two programs libris cut it with —
		// which, in a downloaded install, are not on PATH.
		VideoFFmpegDir: h.programs.VideoFFmpegDir(),
	}, specs)
	if err != nil {
		h.log.Warn("engine serve", "err", err)
		// A port somebody else holds, a model that would not load, and a server that is already
		// up are all the user's to fix, and each is a sentence; only an unhandled one should read
		// as a bug.
		if errors.Is(err, engine.ErrAlreadyServing) {
			// The port is the one the running process holds, which under Auto is not the
			// setting — so "choose another" is only said when there is a setting to choose in.
			if cfg.EnginePort > 0 {
				return srv, fmt.Errorf("%w on port %d — stop it first, or choose another port in Settings",
					ErrEngineRunning, srv.Port)
			}
			return srv, fmt.Errorf("%w on port %d — stop it first", ErrEngineRunning, srv.Port)
		}
		if errors.Is(err, engine.ErrPortTaken) || errors.Is(err, context.DeadlineExceeded) {
			return srv, fmt.Errorf("%w: %s", ErrEngineStart, srv.Error)
		}
		return srv, fmt.Errorf("%w: %s", ErrInvalid, srv.Error)
	}
	return srv, nil
}

// presetDevice is the --device value the preset gets, and the reason it is not the one the
// setting names, if there is one.
//
// A saved name is honoured only while the installed engine still reports it. Device slots are
// not stable facts: an eGPU goes unplugged, a re-downloaded build renumbers its backends, and
// llama.cpp does not degrade gracefully about a name it does not know — `invalid device: CUDA0`
// is an argument error and the server exits. So a name that has gone away falls back to what
// this build can use, loudly in the log, rather than to a preset that cannot start.
//
// "none" is llama.cpp's own word for "do not offload", and it is never a reported slot name, so
// it passes through without being looked up.
func (h *Host) presetDevice(ctx context.Context, cfg config.Config) (device, why string) {
	want := strings.TrimSpace(cfg.EngineDevice)
	if want == deviceinfo.DeviceNone {
		return deviceinfo.DeviceNone, ""
	}

	rep, err := h.Devices(ctx, false)
	if err != nil {
		// Nothing was asked, so nothing is known about what this build can use. Writing a
		// guess here would be worse than writing nothing: an absent device key means llama.cpp
		// decides, which is what it would do on a machine libris cannot interrogate anyway.
		return "", "the machine could not be asked what it can run on, so no device was set"
	}

	if want == "" {
		return rep.DefaultDevice(), ""
	}
	for _, d := range rep.Devices {
		if strings.EqualFold(d.Name, want) {
			return d.Name, ""
		}
	}
	def := rep.DefaultDevice()
	return def, fmt.Sprintf("%q is not among the devices the installed engine reports; running on %s instead", want, def)
}

// ExpectEngineAutostart records that start-up is about to take the router on, and is called
// before the goroutine that does it runs: the first page read must not see a settled machine
// as one waiting on a person, which is what made a set-up box open its own wizard at every
// cold start. EngineAutostart clears the promise on every path that leaves the router down.
func (h *Host) ExpectEngineAutostart() {
	h.autostart.Store(true)
}

// EngineAutostart brings the router up on boot when managed mode asked for a server and one
// can be run. It is quiet by design — a debug line on success, a warning on failure — because
// it runs beside the listener, and a machine that cannot start a model server still gets a
// working app and a settings page that says what happened.
func (h *Host) EngineAutostart(ctx context.Context) {
	cfg := h.settings.Get()
	if cfg.EngineMode != config.EngineManaged || h.serve == nil {
		h.autostart.Store(false)
		return
	}
	if cur := h.serve.Status(); cur.Running {
		h.autostart.Store(false)
		return
	}
	// The same rule the settings screen renders, read from the same facts. It used to be a
	// shorter rule kept here — engine on disk and any one model complete — which is how a
	// router came up on a machine whose device choice nobody had confirmed, holding an
	// embedder and no chat model, while the screen still called the setup unfinished.
	// Startable is yes only when the model server is the single thing missing.
	in, _ := h.setupInputs(cfg)
	if !setup.Startable(in) {
		// The reason is the rule's own answer, not a second copy of it: what is missing is
		// written once, and the log repeats it.
		h.autostart.Store(false)
		h.log.Debug("engine: not started", "missing", setup.For(in).Blocking)
		return
	}
	srv, err := h.EngineServeStart(ctx)
	if err != nil {
		h.autostart.Store(false)
		h.log.Warn("engine: not running; the settings page says why", "err", err, "said", srv.Error)
		return
	}
	h.log.Debug("engine: router up", "port", srv.Port, "models", len(srv.Models))
}

// ErrUpdateChecking is "which build is newest" being found out right now, which is not a
// failure and not an answer either. A person watching a spinner who is told the machine
// could not be reached goes and presses the same button again.
var ErrUpdateChecking = errors.New("app: the newest build is still being looked up")

// ErrUpdateUnknown is "which build is newest" with no answer: the usual cause is a machine
// that cannot reach GitHub, and the sentence after the colon is what it said back.
var ErrUpdateUnknown = errors.New("app: the newest build could not be determined")

// EngineServeStop stops the process and answers with the state it left, because that is the
// thing the caller wanted to know and asking again for it is a second question with a window
// in it. Stopping is not the same act as cancelling a job or a download (and note the
// difference EngineStop above makes): the process is ours, and the answer to "then stop it"
// is a signal. Not running is not an error — the button that asked has already got the state
// it wanted.
func (h *Host) EngineServeStop(ctx context.Context) (engine.Serving, error) {
	if h.serve == nil {
		return engine.Serving{}, fmt.Errorf("%w: this build has no data directory", ErrEngineMode)
	}
	if err := h.serve.Stop(); err != nil && !errors.Is(err, engine.ErrNotServing) {
		return h.serveStatus(), fmt.Errorf("app: stop the engine: %w", err)
	}
	// A stop the person asked for ends any start still said to be on its way, so the gate
	// cannot open on a server that was just put down.
	h.autostart.Store(false)

	srv := h.serveStatus()
	if srv.Running {
		// The one outcome stopping exists to prevent, said by the module that would have to
		// explain it later: a libris that exits with llama-server holding a GPU leaves an
		// orphan the settings page cannot stop and the next start cannot claim.
		h.log.Warn("the model server is still up after the stop ask", "pid", srv.PID, "port", srv.Port)
	}
	return srv, nil
}

// setupInputs gathers what the one readiness rule reads, and returns the key a device
// confirmation would record today alongside it.
//
// The key is the engine build's tag with the machine's own id, which is what the
// confirmation is a claim about: a new build reports a different device list, and a data
// directory copied onto other hardware has never been shown this one. It is AckKey and not
// the device cache's Fingerprint, because that one is keyed to this start-up on purpose and
// would reopen the wizard on every boot. Keeping the formula here rather than in the screen
// is the point — a courier that computes what it carries is a second copy of the rule.
func (h *Host) setupInputs(cfg config.Config) (setup.Input, string) {
	in := setup.Input{
		Managed: cfg.EngineMode == config.EngineManaged,
		Ack:     cfg.EngineDeviceAck,
	}
	if installed, ok := h.engine.Status(); ok {
		in.EngineInstalled = true
		in.AckNow = installed.Tag + ":" + deviceinfo.AckKey(h.engineProbe())
	}
	if h.serve != nil {
		in.Serving = h.serve.Status().Running
	}
	in.Autostart = h.autostart.Load()
	if h.modelsDir != "" {
		for _, st := range models.Statuses(h.modelsDir) {
			if !st.Complete {
				continue
			}
			switch st.Role {
			case models.RoleChat:
				in.ChatComplete = true
			case models.RoleEmbed:
				in.EmbedComplete = true
			}
		}
	}
	return in, in.AckNow
}

// serveStatus reports libris's own server, for the module's own questions about it. It is not
// part of what a caller asks: Engine publishes the state inside the view, and EngineServeStop
// answers with the state it left, so nothing outside this module has a reason to ask
// separately — which is why asking separately is no longer offered.
func (h *Host) serveStatus() engine.Serving {
	if h.serve == nil {
		return engine.Serving{}
	}
	return h.serve.Status()
}
