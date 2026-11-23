package deviceinfo

import (
	"context"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// The fixtures are the spec's worked examples (references/gpu-detection-and-model-loading-spec.md
// §4.1): a single NVIDIA card, and an Ubuntu laptop with both an iGPU and a dGPU. They are
// real command output shapes, not shapes invented to make an assertion pass.

const lspciOneGPU = `0000:00:02.0 System peripheral [0880]: Intel Corporation Raptor Lake PCIe Root Ports (rev 01)
0000:01:00.0 VGA compatible controller [0300]: NVIDIA Corporation GA104 [GeForce RTX 3070] (rev a1)
0000:01:00.1 Audio device [0403]: NVIDIA Corporation GA104 High Definition Audio Controller (rev a1)
0000:00:1f.3 ISA bridge [0601]: Intel Corporation Device 7a51 (rev 10)
`

// The Optimus pair, exactly as the spec records Ubuntu 24.04 printing it.
const lspciOptimus = `00:02.0 VGA compatible controller: Intel Corporation Raptor Lake-P [UHD Graphics] (rev 04)
01:00.0 VGA compatible controller: Advanced Micro Devices, Inc. [AMD/ATI] Navi 33 [Radeon RX 7600S/7600M/7600M XT Pro] (rev c1)
`

const nvidiaSmiOne = "NVIDIA GeForce RTX 3070, 8192, 535.129.03\n"

const cpuinfo = `processor	: 0
vendor_id	: GenuineIntel
model name	: 13th Gen Intel(R) Core(TM) i7-13700K
`

// fakeMachine is a whole machine as far as this package can see one: a /sys tree, an
// optional lspci, and whatever vendor tools are installed.
type fakeMachine struct {
	files fstest.MapFS
	tools map[string]bool
	out   map[string]string // joined argv -> stdout
	errs  map[string]error
}

func newMachine() *fakeMachine {
	return &fakeMachine{
		files: fstest.MapFS{
			"proc/cpuinfo":                   &fstest.MapFile{Data: []byte(cpuinfo)},
			"proc/sys/kernel/random/boot_id": &fstest.MapFile{Data: []byte("boot-1\n")},
		},
		tools: map[string]bool{},
		out:   map[string]string{},
		errs:  map[string]error{},
	}
}

func (m *fakeMachine) pci(bus, class, vendor, uevent string) {
	base := "sys/bus/pci/devices/" + bus
	m.files[base+"/class"] = &fstest.MapFile{Data: []byte(class + "\n")}
	m.files[base+"/vendor"] = &fstest.MapFile{Data: []byte(vendor + "\n")}
	if uevent != "" {
		m.files[base+"/uevent"] = &fstest.MapFile{Data: []byte(uevent + "\n")}
	}
}

func (m *fakeMachine) tool(name, argv, stdout string) {
	m.tools[name] = true
	m.out[argv] = stdout
}

func (m *fakeMachine) options() Options {
	return Options{
		LookPath: func(name string) (string, error) {
			if m.tools[name] {
				return "/usr/bin/" + name, nil
			}
			return "", os.ErrNotExist
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			key := strings.Join(append([]string{name}, args...), " ")
			if err, ok := m.errs[key]; ok {
				return nil, err
			}
			if out, ok := m.out[key]; ok {
				return []byte(out), nil
			}
			// An unrecorded invocation is a test bug, not a silent empty answer: the
			// alternative is a test that passes because a collector was never reached.
			panic("unrecorded probe: " + key)
		},
		ReadFile: func(p string) ([]byte, error) { return fs.ReadFile(m.files, path.Clean(strings.TrimPrefix(p, "/"))) },
		ReadDir: func(p string) ([]fs.DirEntry, error) {
			return fs.ReadDir(m.files, path.Clean(strings.TrimPrefix(p, "/")))
		},
		Now: func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) },
	}
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

func TestDetectSingleCardIsOneRowNotThree(t *testing.T) {
	m := newMachine()
	m.pci("0000:01:00.0", "0x030000", "0x10de", "DRIVER=nvidia")
	m.pci("0000:01:00.1", "0x040300", "0x10de", "DRIVER=snd_hda_intel") // audio function, not a GPU
	m.pci("0000:00:02.0", "0x088000", "0x8086", "")                     // system peripheral, not a GPU
	m.tool("lspci", "lspci -nn", lspciOneGPU)
	m.tool("nvidia-smi", "nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader,nounits", nvidiaSmiOne)

	rep := Detect(context.Background(), m.options())

	g := gpus(rep)
	if len(g) != 1 {
		t.Fatalf("want 1 GPU row, got %d: %+v", len(g), g)
	}
	d := g[0]
	if d.Name != "GeForce RTX 3070" {
		t.Errorf("name = %q, want the bracketed model lspci prints", d.Name)
	}
	if d.Kind != Nvidia {
		t.Errorf("kind = %q, want nvidia (from the PCI vendor id, not the name)", d.Kind)
	}
	if d.TotalBytes != 8192*mib {
		t.Errorf("total = %d, want %d", d.TotalBytes, 8192*mib)
	}
	if d.Driver != "535.129.03" {
		t.Errorf("driver = %q, want the version nvidia-smi reported", d.Driver)
	}
	if !strings.Contains(d.Source, "lspci") || !strings.Contains(d.Source, "nvidia-smi") {
		t.Errorf("source = %q, want both probes named after a merge", d.Source)
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "Several graphics adapters") {
			t.Errorf("one card reported as several: %q — the merge failed", w)
		}
	}
}

// The machine with an engine installed: lspci named the card first, and --list-devices then
// reports the same card under its ggml slot name. Two rows here would mean a spurious
// "several adapters" warning and a user being told to choose between one GPU and itself.
func TestDetectWithEngineIsStillOneCard(t *testing.T) {
	m := newMachine()
	m.pci("0000:01:00.0", "0x030000", "0x10de", "DRIVER=nvidia")
	m.tool("lspci", "lspci -nn", lspciOneGPU)
	m.tool("nvidia-smi", "nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader,nounits", nvidiaSmiOne)

	o := m.options()
	o.EngineBin = "/data/libris/bin/b12649/llama-server"
	m.out[o.EngineBin+" --list-devices"] = `Available devices:
  Vulkan0: NVIDIA GeForce RTX 3070 (8192 MiB, 7600 MiB free)
`

	rep := Detect(context.Background(), o)

	g := gpus(rep)
	if len(g) != 1 {
		t.Fatalf("engine plus lspci plus nvidia-smi produced %d rows: %+v", len(g), g)
	}
	if g[0].FreeBytes != 7600*mib {
		t.Errorf("free = %d, want the engine's free figure — no OS probe knows it", g[0].FreeBytes)
	}
	if !strings.Contains(g[0].Source, "llama-server") {
		t.Errorf("source = %q, want the engine named as a contributor", g[0].Source)
	}

	// And with no engine: the same card, from the OS probes alone.
	rep2 := Detect(context.Background(), m.options())
	if g2 := gpus(rep2); len(g2) != 1 || g2[0].FreeBytes != 0 {
		t.Errorf("without the engine: %+v", g2)
	}
}

func TestDetectOptimusWarnsInsteadOfChoosing(t *testing.T) {
	m := newMachine()
	m.pci("0000:00:02.0", "0x030000", "0x8086", "DRIVER=i915")
	m.pci("0000:01:00.0", "0x030000", "0x1002", "DRIVER=amdgpu")
	m.tool("lspci", "lspci -nn", lspciOptimus)
	// No rocm-smi installed: the AMD row is still reported, without a size.

	rep := Detect(context.Background(), m.options())

	if len(gpus(rep)) != 2 {
		t.Fatalf("Optimus laptop must report both adapters: %+v", gpus(rep))
	}
	var joined = strings.Join(rep.Warnings, " | ")
	if !strings.Contains(joined, "will not guess") {
		t.Errorf("want a warning that refuses to pick a device, got %q", rep.Warnings)
	}
	if !strings.Contains(joined, "reported no memory size") {
		t.Errorf("want a warning that the AMD size is unknown, got %q", rep.Warnings)
	}
}

func TestDetectNoGPUIsAnAnswerNotAFailure(t *testing.T) {
	m := newMachine() // a container: no PCI devices at all

	rep := Detect(context.Background(), m.options())

	if len(gpus(rep)) != 0 {
		t.Fatalf("want no GPUs, got %+v", gpus(rep))
	}
	if len(rep.Warnings) == 0 || !strings.Contains(rep.Warnings[0], "run on the CPU") {
		t.Fatalf("want the CPU-only sentence, got %q", rep.Warnings)
	}
	if rep.Devices[0].Name != "13th Gen Intel(R) Core(TM) i7-13700K" {
		t.Errorf("cpu name = %q, want the model name from /proc/cpuinfo", rep.Devices[0].Name)
	}
}

// A probe that is present and broken must not turn into "no such adapter".
func TestDetectProbeFailureIsNotAbsence(t *testing.T) {
	m := newMachine()
	m.pci("0000:01:00.0", "0x030000", "0x10de", "DRIVER=nvidia")
	m.tools["lspci"] = true
	m.errs["lspci -nn"] = os.ErrPermission

	rep := Detect(context.Background(), m.options())

	g := gpus(rep)
	if len(g) != 1 {
		t.Fatalf("sysfs must still report the card when lspci fails: %+v", g)
	}
	if !strings.Contains(g[0].Name, "NVIDIA display controller") {
		t.Errorf("name = %q, want the honest vendor-plus-address form", g[0].Name)
	}
}

func TestParseWindowsVideoCeilingIsNotAMemorySize(t *testing.T) {
	// 8 GB of VRAM through a uint32 field: the ceiling, not a size.
	fix := `[{"Name":"NVIDIA GeForce RTX 4070","AdapterRAM":4294967295,"DriverVersion":"31.0.15.3183","PNPDeviceID":"PCI\\VEN_10DE&DEV_2704&REV_A1"}]`

	g := parseWinVideo([]byte(fix))
	if len(g) != 1 {
		t.Fatalf("want 1 row, got %d", len(g))
	}
	if g[0].TotalBytes != 0 {
		t.Errorf("total = %d, want 0: 4294967295 is WMI saying it cannot count that high", g[0].TotalBytes)
	}
	if g[0].Kind != Nvidia {
		t.Errorf("kind = %q, want nvidia from VEN_10DE", g[0].Kind)
	}

	// One adapter arrives as a bare object, not an array.
	one := `{"Name":"Intel(R) UHD Graphics 770","AdapterRAM":134217728,"DriverVersion":"31.0.101.2121"}`
	if g := parseWinVideo([]byte(one)); len(g) != 1 || g[0].Kind != Intel {
		t.Errorf("single-object form: %+v", g)
	}
}

func TestMergeKeepsCardsApartThatDifferByModel(t *testing.T) {
	rows := merge(nil, []Device{{Kind: Nvidia, Name: "NVIDIA GeForce RTX 3060"}})
	rows = merge(rows, []Device{{Kind: Nvidia, Name: "GeForce RTX 3060 Ti"}})
	if len(rows) != 2 {
		t.Errorf("3060 and 3060 Ti merged into one row: %+v", rows)
	}

	rows = merge(rows, []Device{{Kind: Nvidia, Name: "NVIDIA GeForce RTX 3060 Ti", TotalBytes: 12288 * mib}})
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[1].TotalBytes != 12288*mib {
		t.Errorf("3060 Ti size = %d, want it filled from the second probe", rows[1].TotalBytes)
	}
}

func TestCacheRejectsChangedFingerprintAndOldAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	rep := Report{Fingerprint: "fp-a", CheckedAt: now, Devices: []Device{{Kind: Nvidia, Name: "RTX 3070"}}}

	if err := Save(path.Join(dir, "cache.json"), rep); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(path.Join(dir, "cache.json"), "fp-a", 24*time.Hour, func() time.Time { return now.Add(time.Hour) }); !ok {
		t.Error("a same-day report with the same fingerprint should be reused")
	}
	if _, ok := Load(path.Join(dir, "cache.json"), "fp-b", 24*time.Hour, func() time.Time { return now }); ok {
		t.Error("a driver update changed the fingerprint; the cache must be refused")
	}
	if _, ok := Load(path.Join(dir, "cache.json"), "fp-a", 24*time.Hour, func() time.Time { return now.Add(48 * time.Hour) }); ok {
		t.Error("a two-day-old report is not this morning's answer")
	}
	if got, ok := Load(path.Join(dir, "nothing-here.json"), "fp-a", 0, func() time.Time { return now }); ok || got.Devices != nil {
		t.Error("a missing cache is a miss, not an error, and not an empty report")
	}
}
