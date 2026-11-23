//go:build linux

package deviceinfo

import (
	"context"
	"strings"
)

// collect walks what Linux actually exposes, in the order of how much each source knows.
//
// sysfs is read first rather than as a fallback because it is the only source that is
// guaranteed present and that carries a PCI vendor id — lspci gives friendlier names but
// prints no vendor id, and the vendor id is what lets two rows be recognised as one card.
// nvidia-smi and rocm-smi come last because they are the only ones that report memory, and
// they are optional: a machine with the proprietary driver uninstalled, or a ROCm-less AMD
// box, still has GPUs and still deserves a row.
func collect(ctx context.Context, o Options) []Device {
	byBus := sysfsRows(o)

	var rows []Device
	if _, err := o.LookPath("lspci"); err == nil {
		if out, err := o.Run(ctx, "lspci", "-nn"); err == nil {
			rows = merge(rows, parseLspci(out))
		}
	}

	if len(rows) == 0 {
		// No lspci, or a lspci that printed nothing usable: sysfs alone. Its rows have no
		// names a person would recognise, so the report stays honest by saying which
		// vendor's card it is rather than inventing a model.
		rows = merge(rows, sysfsNamed(byBus))
	} else {
		rows = attachVendors(rows, byBus)
	}

	if _, err := o.LookPath("nvidia-smi"); err == nil {
		if out, err := o.Run(ctx, "nvidia-smi",
			"--query-gpu=name,memory.total,driver_version", "--format=csv,noheader,nounits"); err == nil {
			rows = merge(rows, parseNvidiaSmi(out))
		}
	}

	// rocm-smi is only asked when a card that could use it exists: it is slow on a machine
	// without it and some builds hang waiting for /dev/kfd.
	if hasVendor(rows, "0x1002") || hasKind(rows, AMD) {
		if _, err := o.LookPath("rocm-smi"); err == nil {
			if out, err := o.Run(ctx, "rocm-smi", "--showmeminfo", "vram", "--json"); err == nil {
				rows = merge(rows, parseRocmSmi(out))
			}
		}
	}

	// A downloaded engine is the best probe on the machine, so it is asked last: ggml
	// initialises the backends it was built with, which is exactly the set llama.cpp can
	// use, and it prints free memory as well as capacity. Merged rather than appended, so a
	// RTX 3070 known from lspci and from Vulkan is one row and not a two-GPU warning.
	if o.EngineBin != "" {
		if out, err := o.Run(ctx, o.EngineBin, "--list-devices"); err == nil {
			rows = merge(rows, engineDevices(ParseDevices(out)))
		}
	}

	// The kernel driver reports a version even when no tool does, and that string is the
	// first thing anybody asks for when a model crashes at load.
	if raw, err := o.ReadFile("/proc/driver/nvidia/version"); err == nil {
		if v := nvidiaDriverVersion(raw); v != "" {
			for i := range rows {
				if rows[i].Kind == Nvidia && rows[i].Driver == "" {
					rows[i].Driver = v
				}
			}
		}
	}

	return rows
}

// sysfsRows reads /sys/bus/pci/devices without needing any helper binary installed.
func sysfsRows(o Options) []Device {
	entries, err := o.readDir("/sys/bus/pci/devices")
	if err != nil {
		return nil
	}
	var rows []Device
	for _, e := range entries {
		base := "/sys/bus/pci/devices/" + e.Name()
		class, err := o.ReadFile(base + "/class")
		if err != nil {
			continue
		}
		vendor, _ := o.ReadFile(base + "/vendor")
		d, ok := sysfsDevice(e.Name(), string(class), string(vendor))
		if !ok {
			continue
		}
		// DRIVER= in uevent is the bound kernel module: amdgpu, i915, nouveau, nvidia.
		if raw, err := o.ReadFile(base + "/uevent"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if v, ok := strings.CutPrefix(line, "DRIVER="); ok {
					d.Driver = strings.TrimSpace(v)
					break
				}
			}
		}
		rows = append(rows, d)
	}
	return rows
}

// sysfsNamed gives a name to rows that came from sysfs only. A vendor id is not a name, so
// the row says what it is: "AMD display controller (0000:03:00.0)". Better than a blank,
// and it does not pretend to know the model.
func sysfsNamed(in []Device) []Device {
	for i := range in {
		if in[i].Name != "" {
			continue
		}
		who := map[Kind]string{Nvidia: "NVIDIA", AMD: "AMD", Intel: "Intel", Apple: "Apple"}[in[i].Kind]
		if who == "" {
			who = "Unknown"
		}
		in[i].Name = who + " display controller (" + in[i].BusID + ")"
	}
	return in
}

// nvidiaDriverVersion reads the version out of the header line
// "NVRM version: NVIDIA UNIX x86_64 Kernel Module  535.129.03  …".
func nvidiaDriverVersion(raw []byte) string {
	for _, f := range strings.Fields(string(raw)) {
		if strings.Count(f, ".") >= 2 && strings.Contains(f, ".") {
			return f
		}
	}
	return ""
}

// engineDevices prepares --list-devices rows for merging. ggml's "Vulkan0" is a slot name
// a user cannot do anything with, so the card's own description becomes the name and the
// slot is kept as the bus-ish identifier; a CPU row is dropped because Detect always
// reports the CPU from its own side and two CPU rows would be two machines.
func engineDevices(in []Device) []Device {
	var out []Device
	for _, d := range in {
		if d.Kind == CPU {
			continue
		}
		if d.Description != "" {
			d.BusID = d.Name
			d.Name = d.Description
			d.Description = ""
		}
		out = append(out, d)
	}
	return out
}

func hasKind(rows []Device, k Kind) bool {
	for _, r := range rows {
		if r.Kind == k {
			return true
		}
	}
	return false
}
