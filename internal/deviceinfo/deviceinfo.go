// Package deviceinfo answers one question: what can this machine run a model on.
//
// It is a leaf, and deliberately stubborn about staying one — no imports of config,
// llm, engine or store. That is what lets a machine that has no llama.cpp installed,
// and therefore no way to ask it anything, still produce a device report: the settings
// page has to be able to say "this box has no GPU" before anything is downloaded.
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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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

// Device is one graphics adapter, or the CPU package when there is nothing else worth
// naming. TotalBytes is zero when no source on this machine would say — a VBIOS does not
// report VRAM to Linux without a driver, and inventing a number would be worse than a
// blank cell (see UnknownSize).
type Device struct {
	Kind       Kind   `json:"kind"`
	Name       string `json:"name"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	BusID      string `json:"bus_id,omitempty"`
	// BusVendor is a PCI vendor id ("0x10de"), the only field on which two probes' names
	// for one card can be reconciled without guessing.
	BusVendor string `json:"bus_vendor,omitempty"`
	Driver    string `json:"driver,omitempty"`
	// Description is the adapter's own words, when a probe had them: ggml prints
	// "NVIDIA GeForce RTX 3070" next to the slot name "Vulkan0", and the second is useless
	// to a person while the first is how they recognise their machine.
	Description string `json:"description,omitempty"`
	// FreeBytes is what is unused at the moment of asking, not the card's capacity. Only a
	// live probe (llama.cpp's) can know it, and it is deliberately kept apart from
	// TotalBytes: a browser with three tabs open changes this number and not that one.
	FreeBytes int64 `json:"free_bytes,omitempty"`
	// Source names the probe that produced this row, because the probes disagree and a
	// user debugging a wrong number needs to know which one was wrong: "lspci",
	// "sysfs", "nvidia-smi", "rocm-smi", "wmi", "system_profiler".
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
	// BootID and Fingerprint are what make a cached report safe to reuse: a driver update
	// or a reboot can change the answer, and a card in the other slot changes it too.
	Fingerprint string    `json:"fingerprint,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
	// Cached is set only when reading the file, never by Detect: it is the difference
	// between "the machine has 8 GB" and "somebody's machine had 8 GB this morning".
	Cached bool `json:"cached,omitempty"`
}

// Run executes a helper binary and returns stdout. It is a field of Options rather than a
// call to exec inside this package so every collector here can be tested against recorded
// output — no unit test may need nvidia-smi, a GPU, or a Windows box (docs/testing.md).
type Run func(ctx context.Context, name string, args ...string) ([]byte, error)

type Options struct {
	// LookPath decides whether a probe's binary is present. Injectable for the same
	// reason Run is.
	LookPath func(name string) (string, error)
	Run      Run
	// ReadFile and ReadDir are os.ReadFile and os.ReadDir, for the sysfs and /proc
	// collectors, which are the only part of this package that reads a filesystem.
	ReadFile func(path string) ([]byte, error)
	ReadDir  func(name string) ([]os.DirEntry, error)
	// EngineBin is an installed llama-server to probe with --list-devices, when there is
	// one. It is the best source available — it reports the backends that build can actually
	// use — so Detect asks it last and merges the answer. Empty means no engine installed,
	// which is the common case this phase: the settings page must still describe the machine.
	EngineBin string

	// Now and BootID keep Detect deterministic in tests.
	Now    func() time.Time
	BootID func() string
}

func (o Options) withDefaults() Options {
	if o.ReadDir == nil {
		o.ReadDir = os.ReadDir
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Run == nil {
		o.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	if o.ReadFile == nil {
		o.ReadFile = os.ReadFile
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.BootID == nil {
		o.BootID = readBootID
	}
	return o
}

// Detect runs this platform's collectors. It never fails: a machine where every probe is
// missing is reported as CPU-only with a warning, because "no data" and "no GPU" are
// different sentences to a person deciding whether to download a 5 GB model.
func Detect(ctx context.Context, opts Options) Report {
	opts = opts.withDefaults()

	rep := Report{OS: runtime.GOOS, Arch: runtime.GOARCH, Devices: []Device{}, Warnings: []string{}}
	rep.Devices = append(rep.Devices, cpuDevice(opts))
	rep.Devices = append(rep.Devices, gpuDevices(ctx, opts)...)
	rep.Devices = dedupe(rep.Devices)
	rep.Warnings = append(rep.Warnings, warn(rep.Devices)...)

	rep.Fingerprint = fingerprint(rep, opts)
	rep.CheckedAt = opts.Now().UTC()
	return rep
}

// gpuDevices is implemented per platform.
func cpuDevice(o Options) Device {
	d := Device{Kind: CPU, Name: "This machine's CPU", Source: "go"}
	switch runtime.GOARCH {
	case "amd64":
		d.Name = "x86-64 CPU"
	case "arm64":
		d.Name = "ARM64 CPU"
	}
	// /proc/cpuinfo's model name is the one CPU string worth showing: it is what a person
	// recognises from the box their machine came in.
	if raw, err := o.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(line, "model name"); ok {
				if _, v, ok = strings.Cut(v, ":"); ok {
					d.Name = strings.TrimSpace(v)
					d.Source = "proc"
				}
				break
			}
		}
	}
	if runtime.GOOS == "darwin" {
		// Apple silicon reports its memory to the CPU, not to a separate adapter.
		d.Kind = Apple
		d.Name = "Apple " + runtime.GOARCH
	}
	return d
}

func dedupe(in []Device) []Device {
	seen := map[string]bool{}
	out := make([]Device, 0, len(in))
	for _, d := range in {
		key := strings.ToLower(strings.TrimSpace(d.Name)) + "\x00" + fmt.Sprint(d.TotalBytes)
		if key == "\x000" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}
	return out
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
		w = append(w, "Several graphics adapters were found ("+strings.Join(names, ", ")+
			"). Libris will not guess which one to run on.")
	}

	for _, d := range gpus {
		if d.TotalBytes == 0 {
			w = append(w, d.Name+" reported no memory size, so a model's fit against it cannot be predicted here.")
		}
	}
	return w
}

// fingerprint is the cache key: a driver update, a reboot, or a card moving slot must
// produce a different string, or a stale report would sit in front of a changed machine.
func fingerprint(r Report, o Options) string {
	parts := make([]string, 0, len(r.Devices)+2)
	for _, d := range r.Devices {
		parts = append(parts, fmt.Sprintf("%s/%s/%d/%s", d.Kind, d.Name, d.TotalBytes, d.Driver))
	}
	sort.Strings(parts)
	parts = append(parts, r.OS+"/"+r.Arch, "boot="+o.BootID())
	return strings.Join(parts, "|")
}

// readBootID is a linuxism that is empty everywhere else, which is the honest answer: on
// Windows and macOS this cache cannot be invalidated by a reboot, so its fingerprint
// carries the driver versions and the age instead.
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

// ErrNoProbe means the one binary a collector needed is not installed. It is never
// returned to a caller — Detect swallows it into a warning — but it exists so a test can
// tell "absent" from "failed".
var ErrNoProbe = errors.New("deviceinfo: probe not installed")
