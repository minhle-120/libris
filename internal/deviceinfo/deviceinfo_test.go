package deviceinfo

import (
	"context"
	"errors"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The fixtures are the shape common/arg.cpp:1159 prints, read from the printf rather than
// remembered: "  %s: %s (%zu MiB, %zu MiB free)\n" over the device name and description,
// with "  (none)" when nothing but a CPU backend was found.

const engineOneGPU = `Available devices:
  Vulkan0: NVIDIA GeForce RTX 3070 (8192 MiB, 7600 MiB free)
`

const engineNone = `Available devices:
  (none)
`

const engineTwoGPUs = `Available devices:
  CUDA0: NVIDIA GeForce RTX 3070 (8192 MiB, 7600 MiB free)
  HIP0: AMD Radeon RX 6600 (8192 MiB, 7100 MiB free)
`

// engineVulkanTwo is verbatim what the build libris downloads printed with --list-devices on
// the machine this was written on: the 16 GB shared Radeon first, the 4 GB NVIDIA second. The
// order is ggml's, not alphabetical, and it is why the default picks Vulkan0 rather than the
// card a person might expect to be named first.
const engineVulkanTwo = `Available devices:
  Vulkan0: AMD Radeon 660M (RADV REMBRANDT) (16142 MiB, 14914 MiB free)
  Vulkan1: NVIDIA GeForce RTX 3050 Ti Laptop GPU (4096 MiB, 3764 MiB free)
`

// engineOneCardTwoBackends is one physical card through two loaded backends. The row is
// still one card, and a report that says otherwise tells a user to choose between a GPU and
// itself.
const engineOneCardTwoBackends = `Available devices:
  CUDA0: NVIDIA GeForce RTX 3070 (8192 MiB, 7600 MiB free)
  Vulkan0: NVIDIA GeForce RTX 3070 (8192 MiB, 7000 MiB free)
`

// engineCPUBackend is what a ggml that lists its CPU backend would print. --list-devices
// filters it out today (common/arg.cpp:1157); the fixture is here so that if a build ever
// stops doing so, the report does not gain a second CPU row — and does not call a CPU an
// Intel GPU because the description says "Intel(R)".
const engineCPUBackend = `Available devices:
  CPU: Intel(R) Core(TM) i7-14700K (65166 MiB, 52000 MiB free)
`

const engineNoMemoryLine = `Available devices:
  Vulkan0: Some Accelerator
`

const bootID = "boot-1"

// machine is a whole host as far as this package can see one: a boot id, and an engine that
// either answers --list-devices or does not exist.
type machine struct {
	out    string
	err    error
	ran    []string
	called bool
}

func (m *machine) options(bin, tag string) Options {
	return Options{
		EngineBin: bin,
		EngineTag: tag,
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			m.called = true
			m.ran = append(m.ran, strings.Join(append([]string{name}, args...), " "))
			return []byte(m.out), m.err
		},
		Now:    func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) },
		BootID: func() string { return bootID },
	}
}

func detect(m *machine, bin, tag string) Report {
	return Detect(context.Background(), m.options(bin, tag))
}

// gpus drops the CPU row, which every report carries.
func gpus(r Report) []Device {
	var out []Device
	for _, d := range r.Devices {
		if d.Kind != CPU {
			out = append(out, d)
		}
	}
	return out
}

func TestDetectAsksTheEngineAndKeepsItsAnswerVerbatim(t *testing.T) {
	m := &machine{out: engineOneGPU}
	rep := detect(m, "/data/libris/bin/b12649/llama-server", "b12649")

	if !m.called {
		t.Fatal("nothing was run")
	}
	if len(m.ran) != 1 || m.ran[0] != "/data/libris/bin/b12649/llama-server --list-devices" {
		t.Fatalf("ran %v, want one --list-devices on the installed engine", m.ran)
	}

	g := gpus(rep)
	if len(g) != 1 {
		t.Fatalf("want 1 GPU row, got %d: %+v", len(g), g)
	}
	d := g[0]
	if d.Name != "Vulkan0" {
		t.Errorf("name = %q, want the slot ggml printed — it is what -dev takes", d.Name)
	}
	if d.Description != "NVIDIA GeForce RTX 3070" {
		t.Errorf("description = %q, want the card's own words", d.Description)
	}
	if d.Kind != Nvidia {
		t.Errorf("kind = %q, want nvidia from the card's own string", d.Kind)
	}
	if d.TotalBytes != 8192*mib || d.FreeBytes != 7600*mib {
		t.Errorf("sizes = %d/%d, want 8192/7600 MiB — free is the figure only a live init knows",
			d.TotalBytes, d.FreeBytes)
	}
	if d.Source != "llama-server" {
		t.Errorf("source = %q, want the engine to name itself", d.Source)
	}

	if len(rep.Devices) != 2 || rep.Devices[0].Kind != CPU {
		t.Errorf("devices = %+v, want a CPU row first and the card after it", rep.Devices)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings = %q, want none for one card with a size", rep.Warnings)
	}
}

// "(none)" is how a build without GPU support answers, and it is the one thing a user needs
// this report to be able to say out loud.
func TestDetectNoneIsAnAnswerNotAFailure(t *testing.T) {
	rep := detect(&machine{out: engineNone}, "/bin/llama-server", "b1")

	if len(gpus(rep)) != 0 {
		t.Fatalf("want no GPUs, got %+v", gpus(rep))
	}
	if len(rep.Warnings) == 0 || !strings.Contains(rep.Warnings[0], "run on the CPU") {
		t.Fatalf("want the CPU-only sentence, got %q", rep.Warnings)
	}
	if rep.Devices[0].Kind != CPU || rep.Devices[0].Name == "" {
		t.Errorf("cpu row = %+v, want a generic one", rep.Devices[0])
	}
}

// The two-Vulkan case is this repository's own machine, printed by the build libris downloads.
// The report used to refuse to choose, which was honest and useless: nothing downstream read the
// refusal, so llama.cpp chose anyway and the page declined to say which.
func TestDetectTwoCardsNamesTheOneItWillUse(t *testing.T) {
	rep := detect(&machine{out: engineVulkanTwo}, "/bin/llama-server", "b1")

	if len(gpus(rep)) != 2 {
		t.Fatalf("both adapters must be reported: %+v", gpus(rep))
	}
	if got := rep.DefaultDevice(); got != "Vulkan0" {
		t.Errorf("the first Vulkan device is the default, got %q", got)
	}
	joined := strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "Libris runs on Vulkan0") {
		t.Errorf("the warning must name the device it picked, got %q", rep.Warnings)
	}
}

// A report with no Vulkan row is a build libris did not download, and the default is then the
// CPU — stated as such rather than left as an empty pick. The device picker still offers these
// names, because a person who built llama.cpp with CUDA themselves can name CUDA0.
func TestDefaultDeviceWithoutVulkanIsTheCPU(t *testing.T) {
	rep := detect(&machine{out: engineTwoGPUs}, "/bin/llama-server", "b1")

	if got := rep.DefaultDevice(); got != DeviceNone {
		t.Errorf("no Vulkan device means do not offload; got %q", got)
	}
	joined := strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "run on the CPU") {
		t.Errorf("want a sentence saying the CPU is what it will use, got %q", rep.Warnings)
	}

	// A box with no GPU at all says the same thing, from the Go-stated CPU row alone.
	only := Report{Devices: []Device{cpuDevice()}}
	if got := only.DefaultDevice(); got != DeviceNone {
		t.Errorf("%q", got)
	}
}

// One RTX 3070 through two loaded backends is one card. Two rows here is the mistake the
// merge existed to prevent, so it survived the reduction to a single source.
func TestDetectOneCardTwoBackendsIsStillOneCard(t *testing.T) {
	rep := detect(&machine{out: engineOneCardTwoBackends}, "/bin/llama-server", "b1")

	g := gpus(rep)
	if len(g) != 1 {
		t.Fatalf("one card listed as %d rows: %+v", len(g), g)
	}
	for _, slot := range []string{"CUDA0", "Vulkan0"} {
		if !strings.Contains(g[0].Name, slot) {
			t.Errorf("name = %q, want both slot names kept — they are what -dev takes", g[0].Name)
		}
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "Several graphics adapters") {
			t.Errorf("one card reported as several: %q", w)
		}
	}
}

func TestDetectCPUBackendDoesNotAddASecondCPU(t *testing.T) {
	rep := detect(&machine{out: engineCPUBackend}, "/bin/llama-server", "b1")

	if len(rep.Devices) != 1 || rep.Devices[0].Kind != CPU {
		t.Fatalf("devices = %+v, want the one generic CPU row", rep.Devices)
	}
	if len(gpus(rep)) != 0 {
		t.Errorf("a CPU backend's description is a CPU model string, not an Intel GPU: %+v", gpus(rep))
	}
}

// A line with no memory in it still names a device worth listing — and says so, rather than
// showing a blank cell in silence.
func TestDetectNoMemoryLineIsHonest(t *testing.T) {
	rep := detect(&machine{out: engineNoMemoryLine}, "/bin/llama-server", "b1")

	g := gpus(rep)
	if len(g) != 1 || g[0].TotalBytes != 0 {
		t.Fatalf("devices = %+v, want one row with no size", g)
	}
	joined := strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "reported no memory size") {
		t.Errorf("want the blank-size sentence, got %q", rep.Warnings)
	}
}

// The trade this package now accepts, stated as a test so it stays a decision and does not
// quietly become a bug: with nothing installed there is nothing to ask, and the report must
// not answer the question it did not ask.
func TestDetectWithNoEngineAsksNothing(t *testing.T) {
	m := &machine{out: engineOneGPU}
	rep := detect(m, "", "")

	if m.called {
		t.Error("Detect ran something with no engine installed")
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "not been asked") {
		t.Fatalf("warnings = %q, want the sentence that says nothing was asked", rep.Warnings)
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "No graphics adapter") {
			t.Errorf("claimed a fact about hardware it never looked at: %q", w)
		}
	}
	if len(rep.Devices) != 1 || rep.Devices[0].Kind != CPU {
		t.Errorf("devices = %+v, want the CPU row alone", rep.Devices)
	}
}

// An engine that will not answer is not a machine with no GPU. The sentence has to say which
// of the two happened, because they lead to different fixes.
func TestDetectEngineFailureIsNotAbsence(t *testing.T) {
	m := &machine{err: errors.New("exit status 1: ggml_init failed: cudaMalloc failed")}
	rep := detect(m, "/bin/llama-server", "b1")

	if len(gpus(rep)) != 0 {
		t.Fatalf("a failed probe invented devices: %+v", gpus(rep))
	}
	joined := strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "did not answer") {
		t.Fatalf("warnings = %q, want the sentence that names the failed probe", rep.Warnings)
	}
	if strings.Contains(joined, "No graphics adapter") {
		t.Errorf("a probe that failed is not a machine with nothing plugged in: %q", rep.Warnings)
	}
	if !strings.Contains(joined, "cudaMalloc") {
		t.Errorf("the reason was dropped, so the reader cannot fix it: %q", rep.Warnings)
	}
}

// A probe's complaint can be forty lines of ggml's startup log; a warning is one sentence.
func TestDetectWarningIsOneShortLine(t *testing.T) {
	long := errors.New("exit status 1\n" + strings.Repeat("ggml_backend_load: could not load\n", 40))
	rep := detect(&machine{err: long}, "/bin/llama-server", "b1")

	w := strings.Join(rep.Warnings, " | ")
	if strings.Contains(w, "\n") {
		t.Errorf("a warning contains a newline, so it is not a sentence: %q", w)
	}
	if len(w) > 400 {
		t.Errorf("warning is %d bytes: %q", len(w), w)
	}
}

func TestFingerprintIsThisBootAndThisEngine(t *testing.T) {
	o := func(bin, tag, boot string) Options {
		return Options{
			EngineBin: bin,
			EngineTag: tag,
			BootID:    func() string { return boot },
		}
	}

	a := Fingerprint(o("/data/bin/b12649/llama-server", "b12649", bootID))
	if !strings.Contains(a, "boot="+bootID) {
		t.Errorf("fingerprint %q, want it keyed to this boot", a)
	}
	if !strings.Contains(a, "engine=b12649") {
		t.Errorf("fingerprint %q, want it keyed to the engine build that answered", a)
	}

	// A new download is a new answer about what this machine can run on.
	if b := Fingerprint(o("/data/bin/b12700/llama-server", "b12700", bootID)); b == a {
		t.Error("a different engine build produced the same fingerprint")
	}
	// And so is a reboot, which is all a machine with no engine has to go on.
	c := Fingerprint(o("", "", bootID))
	if strings.Contains(c, "engine=") {
		t.Errorf("fingerprint %q, want no engine part when none is installed", c)
	}
	if d := Fingerprint(o("", "", "boot-2")); d == c {
		t.Error("a different boot produced the same fingerprint")
	}
	// A machine that cannot report a boot id — Windows, macOS — is still not confused with
	// another machine on the same architecture.
	if e := Fingerprint(o("", "", "")); strings.Contains(e, "boot=") {
		t.Errorf("fingerprint %q, want no boot part when there is no boot id to read", e)
	}
}

// TestDetectAgainstARealEngine is the only test in this package that asks an actual
// llama.cpp, and it is skipped by default: no unit test may need a 37 MB download or a
// GPU. It exists because every fixture here is a transcription of
// common/arg.cpp:1159, and the first time this package was written from a remembered shape
// it got the output format wrong. Against the b12651 vulkan build on the development laptop
// it prints, on stdout with an empty stderr in 1.7 s:
//
//	Available devices:
//	  Vulkan0: AMD Radeon 660M (RADV REMBRANDT) (16142 MiB, 14663 MiB free)
//	  Vulkan1: NVIDIA GeForce RTX 3050 Ti Laptop GPU (4096 MiB, 3765 MiB free)
//
// which is also the case a fixture must not flatter: the description carries its own
// parentheses, so the memory suffix has to be anchored to the end of the line rather than
// matched as "the last parenthesised group".
//
//	LIBRIS_ENGINE_BIN=/tmp/llama-b12651/llama-server go test ./internal/deviceinfo/ -run Real -v
func TestDetectAgainstARealEngine(t *testing.T) {
	bin := os.Getenv("LIBRIS_ENGINE_BIN")
	if bin == "" {
		t.Skip("LIBRIS_ENGINE_BIN is unset: no engine installed to ask")
	}

	rep := Detect(context.Background(), Options{EngineBin: bin, EngineTag: "as-given"})
	t.Logf("devices: %+v", rep.Devices)
	t.Logf("warnings: %q", rep.Warnings)

	joined := strings.Join(rep.Warnings, " | ")
	if strings.Contains(joined, "did not answer") {
		t.Fatalf("the engine was asked and did not reply: %q", joined)
	}
	if len(rep.Devices) == 0 || rep.Devices[0].Kind != CPU {
		t.Errorf("devices = %+v, want the generic CPU row first", rep.Devices)
	}

	for _, d := range gpus(rep) {
		if !engineSlot.MatchString(d.Name) {
			t.Errorf("name %q is not a ggml slot name", d.Name)
		}
		if d.Description == "" {
			t.Errorf("%q has no description, so the line did not parse as arg.cpp prints it", d.Name)
		}
		if d.TotalBytes <= 0 {
			t.Errorf("%q has no size, but --list-devices always prints one", d.Name)
		}
		if d.Kind == Other {
			t.Errorf("%q (%q) classified as other", d.Name, d.Description)
		}
	}
}

// engineSlot is what ggml names a device: a backend word and an index, and after this
// package's merge possibly two of them joined by " / ".
var engineSlot = regexp.MustCompile(`^[A-Za-z]+[0-9]*( / [A-Za-z]+[0-9]*)*$`)

func TestCacheRejectsChangedFingerprintAndOldAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	rep := Report{Fingerprint: "fp-a", CheckedAt: now, Devices: []Device{{Kind: Nvidia, Name: "Vulkan0", Description: "RTX 3070"}}}

	if err := Save(path.Join(dir, "cache.json"), rep); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(path.Join(dir, "cache.json"), "fp-a", 24*time.Hour, func() time.Time { return now.Add(time.Hour) }); !ok {
		t.Error("a same-day report with the same fingerprint should be reused")
	}
	if _, ok := Load(path.Join(dir, "cache.json"), "fp-b", 24*time.Hour, func() time.Time { return now }); ok {
		t.Error("a new engine build changed the fingerprint; the cache must be refused")
	}
	if _, ok := Load(path.Join(dir, "cache.json"), "fp-a", 24*time.Hour, func() time.Time { return now.Add(48 * time.Hour) }); ok {
		t.Error("a two-day-old report is not this morning's answer")
	}
	if got, ok := Load(path.Join(dir, "nothing-here.json"), "fp-a", 0, func() time.Time { return now }); ok || got.Devices != nil {
		t.Error("a missing cache is a miss, not an error, and not an empty report")
	}
}
