// Package deviceinfo answers one question: what can this machine run a model on.
//
// There is one source: an installed llama-server, asked with --list-devices. The OS probes
// this package used to run — lspci, sysfs, nvidia-smi, rocm-smi, WMI, system_profiler — are
// gone (ADR 0019). What they bought was a report on a machine that had not been downloaded
// to yet, and what they cost was a merge between sources that disagree: lspci calls a card
// "GeForce RTX 3070" and says nothing about memory, nvidia-smi calls the same card "NVIDIA
// GeForce RTX 3070" and caps at 4 GiB on Windows, and a report listing both tells a user to
// choose between one GPU and itself. The engine's answer is the only one that is about the
// program being run: it lists the backends that build can actually use, with the memory it
// can actually allocate right now.
//
// The cost is paid where the user sees it: with nothing installed there is nothing to ask,
// so the report says "this machine has not been asked yet" instead of reading the PCI bus
// and guessing. It is still a leaf — no config, llm, engine or store; the binary to ask
// arrives as a path.
//
// Nothing here estimates a GPU layer count or a context size. llama.cpp fits a model to
// the memory it finds with -fit, and a number computed here would be a second, worse
// answer to a question that is already answered downstream (ADR 0011: a setting, or a
// claim, is only allowed where the app can apply it). The report exists to warn a person
// before they download, and to name the graphics adapter a driver problem lives on.
package deviceinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Kind is what a device is, in the vocabulary libris shows a user. CPU is a real entry:
// a report with only a CPU is the answer "this runs on CPU", not "nothing was found".
type Kind string

const (
	CPU    Kind = "cpu"
	Nvidia Kind = "nvidia"
	AMD    Kind = "amd"
	Intel  Kind = "intel"
	Apple  Kind = "apple"
	Other  Kind = "other"
)

// Device is one thing llama.cpp offered to run a model on.
//
// Name is the slot ggml gave it ("Vulkan0", "CUDA0") — the string that would be passed to
// -dev, and therefore not something to reword. Description is the card's own words
// ("NVIDIA GeForce RTX 3070"), which is what a person recognises, and what a reader is
// shown. TotalBytes is zero when the line had no memory in it, which a build can do; a
// blank cell beats an invented number, and warn() says the cell is blank.
type Device struct {
	Kind       Kind   `json:"kind"`
	Name       string `json:"name"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	// Description is the adapter's own words, when the line had them: ggml prints
	// "NVIDIA GeForce RTX 3070" next to the slot name "Vulkan0", and the second is useless
	// to a person while the first is how they recognise their machine.
	Description string `json:"description,omitempty"`
	// FreeBytes is what is unused at the moment of asking, not the card's capacity. Only a
	// live ggml init sees what another program is holding, and it is deliberately kept apart
	// from TotalBytes: a browser with three tabs open changes this number and not that one.
	FreeBytes int64 `json:"free_bytes,omitempty"`
	// Source names where the row came from, because a user debugging a wrong number needs
	// to know which thing was wrong: "llama-server" for every row the engine reported, "go"
	// for the CPU row this package states without asking anything.
	Source string `json:"source,omitempty"`
}

// Report is the whole answer, with the reasons it might be incomplete.
type Report struct {
	OS      string   `json:"os"`
	Arch    string   `json:"arch"`
	Devices []Device `json:"devices"`
	// Warnings are for the user, in sentences: "Two graphics adapters are present —
	// pick the one to run on." An empty slice means the report needs no asterisk.
	Warnings []string `json:"warnings,omitempty"`
	// Fingerprint and CheckedAt are what make a cached report safe to reuse: a new engine
	// build or a reboot can change the answer.
	Fingerprint string    `json:"fingerprint,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
	// Cached is set only when reading the file, never by Detect: it is the difference
	// between "the machine has 8 GB" and "somebody's machine had 8 GB this morning".
	Cached bool `json:"cached,omitempty"`
}

// Run executes a helper binary and returns stdout. It is a field of Options rather than a
// call to exec inside this package so the engine probe can be tested against recorded
// output — no unit test may need a GPU, or an installed engine (docs/testing.md).
type Run func(ctx context.Context, name string, args ...string) ([]byte, error)

type Options struct {
	// EngineBin is an installed llama-server to probe with --list-devices, or empty when
	// there is none — which is not an error, it is the state a first run is in, and the
	// report has a sentence for it.
	EngineBin string
	// EngineTag is that install's release tag (b12649), used only to key the cache: a new
	// engine is a new answer about what the machine can run.
	EngineTag string

	Run Run

	// Now and BootID keep Detect deterministic in tests.
	Now    func() time.Time
	BootID func() string
}

func (o Options) withDefaults() Options {
	if o.Run == nil {
		o.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.BootID == nil {
		o.BootID = readBootID
	}
	return o
}

// askTimeout bounds the run of the engine's --list-devices.
//
// Not a guess at how long initialising a GPU takes — that is driver- and box-dependent, and
// a Vulkan init on a laptop that has to wake the dGPU is slow on purpose. It is the limit on
// a process that is supposed to print six lines and exit, so a hang here means a broken
// driver and the answer the user needs is "that one did not answer", not a settings page
// that never comes back.
const askTimeout = 30 * time.Second

// noEngine is what a machine with nothing installed is told, in the report's own words. It
// is deliberately not "no graphics adapter was found": on this machine nothing has been
// looked at, and a sentence that reads as a fact about the hardware would be a lie about a
// question that was never asked.
const noEngine = "Nothing has been downloaded yet, so this machine has not been asked what it can run on. Download llama.cpp below, then check again."

// Detect asks the one thing worth asking, and never fails: an unanswerable machine gets a
// report that says which question could not be put, because "nothing was asked" and "no
// GPU" are different sentences to a person deciding whether to download a 5 GB model.
func Detect(ctx context.Context, opts Options) Report {
	opts = opts.withDefaults()

	rep := Report{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Devices:  []Device{cpuDevice()},
		Warnings: []string{},
	}

	if opts.EngineBin == "" {
		rep.Warnings = append(rep.Warnings, noEngine)
	} else {
		ctx, cancel := context.WithTimeout(ctx, askTimeout)
		defer cancel()

		out, err := opts.Run(ctx, opts.EngineBin, "--list-devices")
		if err != nil {
			rep.Warnings = append(rep.Warnings,
				"The installed engine did not answer when it was asked for its devices ("+oneLine(err)+
					"), so what this machine can run on is not known.")
		} else {
			rep.Devices = append(rep.Devices, engineDevices(out)...)
			rep.Warnings = append(rep.Warnings, warn(rep.Devices)...)
		}
	}

	rep.Fingerprint = Fingerprint(opts)
	rep.CheckedAt = opts.Now().UTC()
	return rep
}

// cpuDevice is the one row not taken from the engine, and it is a constant rather than a
// probe. llama.cpp's --list-devices skips CPU devices (common/arg.cpp:1157), so a report
// that asked only the engine could not say that a CPU exists — and "this runs on the CPU"
// is the answer a person on the box with no GPU needs. Nothing is read or run: it is a
// property of the binary already executing.
func cpuDevice() Device {
	d := Device{Kind: CPU, Name: "CPU", Source: "go"}
	switch runtime.GOARCH {
	case "amd64":
		d.Name = "x86-64 CPU"
	case "arm64":
		d.Name = "ARM64 CPU"
	}
	return d
}

// warn turns the shape of a report into the sentences a user needs before downloading.
func warn(dev []Device) []string {
	var w []string

	var gpus []Device
	for _, d := range dev {
		if d.Kind != CPU {
			gpus = append(gpus, d)
		}
	}

	switch {
	case len(gpus) == 0:
		w = append(w, "No graphics adapter was reported. Answers and embeddings will run on the CPU.")
	case len(gpus) > 1:
		names := make([]string, 0, len(gpus))
		for _, d := range gpus {
			names = append(names, d.Name)
		}
		// It used to refuse to choose, which was honest and useless: nothing downstream read
		// the refusal, so the effect was that llama.cpp chose and this page declined to say
		// which. Now the report states the pick it makes, because the pick is knowable —
		// DefaultDevice is the rule the preset is written with — and because a person with a
		// 4 GiB card and a 16 GiB one needs to know which is being used, not that the app
		// washed its hands of it.
		def := defaultDevice(gpus)
		if def == DeviceNone {
			// Not "it will not choose": it chose the CPU, and the reason is that the rule it
			// chose by only knows the backend libris downloads. A person who built llama.cpp
			// with CUDA themselves sees this sentence and knows to name CUDA0.
			w = append(w, "Several graphics adapters were found ("+strings.Join(names, ", ")+
				"), but none of them is a device the build libris downloads offloads to by default, "+
				"so it will run on the CPU until the settings page names one.")
			break
		}
		w = append(w, "Several graphics adapters were found ("+strings.Join(names, ", ")+
			"). Libris runs on "+def+" unless the settings page names another.")
	}

	for _, d := range gpus {
		if d.TotalBytes == 0 {
			w = append(w, d.Name+" reported no memory size, so a model's fit against it cannot be predicted here.")
		}
	}
	return w
}

// DeviceNone is llama.cpp's own word for "do not offload" (--device none, common/arg.cpp),
// which is the only device string that means CPU. It is not "CPU": a CPU row belongs in the
// report (this package states it, because --list-devices filters CPU devices out), and
// naming it to ggml is an argument error.
const DeviceNone = "none"

// DefaultDevice is the slot libris runs on when nobody has chosen one: the first Vulkan
// device the engine reported, or DeviceNone when it reported no Vulkan device at all.
//
// Vulkan because it is the backend libris downloads (ADR 0018), and a name taken from this
// report is the only thing that build accepts — "CUDA0" on the build libris installs is a
// hard parse error, and llama.cpp exits rather than falling back to something that works.
// Falling back to DeviceNone rather than to "no device key" is deliberate: a build that
// listed no Vulkan device has no GPU it can use, and CPU is the honest answer rather than a
// second guess.
func (r Report) DefaultDevice() string { return defaultDevice(r.Devices) }

func defaultDevice(dev []Device) string {
	for _, d := range dev {
		if d.Kind == CPU {
			continue
		}
		// The slot name is what --device takes, and the Vulkan backend's is always Vulkan<n>
		// (ggml-vulkan registers it with that prefix); the description is the card's marketing
		// words and is never a device name.
		if strings.HasPrefix(d.Name, "Vulkan") {
			return d.Name
		}
	}
	return DeviceNone
}

// oneLine keeps a subprocess's complaint inside one sentence: --list-devices writes ggml's
// own startup log to stderr, and a warning rendered as a <li> is not a place for forty
// lines of it.
func oneLine(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// Fingerprint is the cache key, and it is computed from cheap inputs on purpose: this boot,
// and this engine build. Nothing is run, so a stale cache can be recognised before a probe
// is paid for — which is the only order that makes a cache worth having.
//
// It no longer enumerates the PCI bus, because nothing in the report comes from the bus any
// more. The answer moves when the engine moves — and installing or removing one already
// invalidates the cache where that happens (app.Host) — or when the machine reboots. What it
// cannot see is a driver update that survives a reboot under the same engine build, which is
// why the cache also has an age (Load's maxAge): on Windows and macOS, where there is no
// boot id to read, age is the only invalidation.
func Fingerprint(o Options) string {
	o = o.withDefaults()

	var parts []string
	if id := o.BootID(); id != "" {
		parts = append(parts, "boot="+id)
	}
	if o.EngineBin != "" {
		tag := o.EngineTag
		if tag == "" {
			// A build with no recorded tag is still identified by where it lives, and the
			// install directory carries the tag.
			tag = o.EngineBin
		}
		parts = append(parts, "engine="+tag)
	}
	parts = append(parts, runtime.GOOS+"/"+runtime.GOARCH)
	return strings.Join(parts, "|")
}

// readBootID is a linuxism that is empty everywhere else, which is the honest answer: on
// Windows and macOS this cache cannot be invalidated by a reboot, so its fingerprint
// carries the engine build and the age instead.
func readBootID() string {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// ---- cache --------------------------------------------------------------------------

// stored is the file shape. Report is embedded so the JSON a reader inspects by hand is
// the same thing the API returns.
type stored struct {
	Report
	Written time.Time `json:"written"`
}

// Load returns the cached report if it still describes this machine. A changed
// fingerprint is not an error and not a miss to retry: it is a stale cache, and Detect is
// the next call.
func Load(path, fingerprint string, maxAge time.Duration, now func() time.Time) (Report, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Report{}, false
	}
	var s stored
	if err := json.Unmarshal(raw, &s); err != nil {
		return Report{}, false
	}
	if s.Fingerprint != "" && fingerprint != "" && s.Fingerprint != fingerprint {
		return Report{}, false
	}
	if maxAge > 0 && !s.Written.After(now().Add(-maxAge)) {
		return Report{}, false
	}
	rep := s.Report
	rep.Cached = true
	return rep, true
}

// Save writes atomically at 0600: the file is machine topology, not a secret, but a
// half-written file would be read as a corrupt cache on every start forever.
func Save(path string, rep Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("deviceinfo: cache dir: %w", err)
	}
	raw, err := json.MarshalIndent(stored{Report: rep, Written: rep.CheckedAt}, "", "  ")
	if err != nil {
		return fmt.Errorf("deviceinfo: encode: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("deviceinfo: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("deviceinfo: replace: %w", err)
	}
	return nil
}
