package deviceinfo

import (
	"regexp"
	"strconv"
	"strings"
)

// ParseDevices reads what `llama-server --list-devices` prints.
//
// The spec that asked for this function says the output is JSON. It is not — and it cites
// common/arg.cpp, which is exactly where you find out. common_print_available_devices
// (common/arg.cpp:1149) prints:
//
//	Available devices:
//	  CUDA0: NVIDIA GeForce RTX 3070 (8192 MiB, 7000 MiB free)
//
// skipping CPU devices (common/arg.cpp:1157), and printing "(none)" when there are no
// non-CPU devices. So: parse lines, and treat "(none)" as a real answer rather than a parse
// failure — it is how a build without GPU support says so, and "no GPU" is the one thing a
// user needs this report to be able to say.
//
// The two numbers are total and free-right-now. Only this source knows the second, because
// only a live ggml init sees what another program is holding — which is why this is the
// source libris keeps and the OS probes it dropped are not (ADR 0019).
func ParseDevices(out []byte) []Device {
	var rows []Device
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || line == "Available devices:" || line == "(none)" {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name, rest = strings.TrimSpace(name), strings.TrimSpace(rest)
		if name == "" || rest == "" {
			continue
		}

		d := Device{Name: name, Source: "llama-server"}
		// ggml names a device <Backend><index>: CUDA0, HIP0, Vulkan1, CPU. The backend says
		// the vendor far more reliably than the description's wording does — except for
		// Vulkan and Metal, which describe the card instead of labelling a backend, and then
		// the description is all there is.
		switch strings.TrimRight(name, "0123456789") {
		case "CPU":
			// Checked before classify, not by it: the CPU backend's description is the CPU
			// model string, and on an Intel box that reads "Intel(R) Core(TM) i7-14700K" —
			// name matching alone reports that as an Intel GPU. What ggml named the slot
			// outranks any word in a description.
			d.Kind = CPU
		case "CUDA":
			d.Kind = Nvidia
		case "HIP", "ROCm":
			d.Kind = AMD
		case "SYCL", "Level0":
			d.Kind = Intel
		default:
			d.Kind = classify(rest, "")
		}

		if m := deviceLine.FindStringSubmatch(rest); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				d.TotalBytes = n * mib
			}
			if n, err := strconv.ParseInt(m[2], 10, 64); err == nil {
				d.FreeBytes = n * mib
			}
			d.Description = strings.TrimSpace(rest[:len(rest)-len(m[0])])
		} else {
			// A build whose memory line we cannot read still has a device worth listing;
			// a size of 0 is then honest, and warn() says so.
			d.Description = rest
		}
		rows = append(rows, d)
	}
	return rows
}

// engineDevices is the report's view of what the engine listed: the CPU rows go, since this
// package states the CPU itself and two CPU rows read as two machines — and a build whose
// ggml ever starts listing the CPU backend would otherwise put a "CPU: Intel Core i7" chip
// next to the generic one. Then one physical card stays one row.
func engineDevices(out []byte) []Device {
	var rows []Device
	for _, d := range ParseDevices(out) {
		if d.Kind == CPU {
			continue
		}
		rows = mergeBackends(rows, d)
	}
	return rows
}

// mergeBackends folds two rows that are one card seen through two backends.
//
// A build with both CUDA and Vulkan loaded prints both, and they are the same RTX 3070:
// "CUDA0: NVIDIA GeForce RTX 3070 (8192 MiB, …)" and "Vulkan0: NVIDIA GeForce RTX 3070
// (8192 MiB, …)". Listed twice, warn() tells the user to pick between one GPU and itself —
// the exact mistake the merge in this package existed to prevent, and the reason it survived
// the reduction to a single source.
//
// Two rows are one card when the card's own words and its size agree. Slot names are never
// compared: "CUDA0" and "Vulkan0" are different by construction, and that is the whole
// reason the name is not the key. The kept row's Name lists both slots, because a slot name
// is what would be handed to -dev and the choice between them is a real one — it is the
// number of cards that must not be wrong.
func mergeBackends(rows []Device, d Device) []Device {
	key := cardKey(d)
	for i := range rows {
		if cardKey(rows[i]) != key {
			continue
		}
		r := &rows[i]
		if !strings.Contains(r.Name, d.Name) {
			r.Name += " / " + d.Name
		}
		// Two backends measure the same card at two moments; the one that found more free
		// is closer to what a fresh load could take, and either is a snapshot.
		if d.FreeBytes > r.FreeBytes {
			r.FreeBytes = d.FreeBytes
		}
		if r.Kind == Other {
			r.Kind = d.Kind
		}
		return rows
	}
	return append(rows, d)
}

// cardKey is "the same physical card" as far as one source's lines can tell: what the card
// is called in its own words, at one size. A 3060 and a 3060 Ti differ in the first part,
// so they stay two rows; a 3070 through CUDA and through Vulkan does not, so it stays one.
func cardKey(d Device) string {
	name := strings.ToLower(d.Description)
	if name == "" {
		name = strings.ToLower(d.Name)
	}
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(d.TotalBytes, 10))
	return b.String()
}

// deviceLine matches the memory suffix: "(8192 MiB, 7000 MiB free)".
var deviceLine = regexp.MustCompile(`\((\d+) MiB,\s*(\d+) MiB free\)$`)

const mib = int64(1 << 20)

// classify names a vendor from the two things every source agrees on: a vendor id where
// there is one, and otherwise words in the adapter's own name. Vendor ids win because a name
// is a marketing string — "AMD Radeon Graphics" on a laptop with an Intel iGPU is both true
// at once, and the PCI id knows which one this row is. Only the second is available now that
// the engine is the only source: a Vulkan row has no vendor id, so the card's wording is the
// whole evidence, and that is why the word list is long.
func classify(name, busVendor string) Kind {
	switch strings.ToLower(strings.TrimSpace(busVendor)) {
	case "0x10de", "10de":
		return Nvidia
	case "0x1002", "1002":
		return AMD
	case "0x8086", "8086":
		return Intel
	case "0x106b", "106b":
		return Apple
	}

	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "nvidia"), strings.Contains(n, "geforce"), strings.Contains(n, "quadro"), strings.Contains(n, "tesla"):
		return Nvidia
	case strings.Contains(n, "radeon"), strings.Contains(n, "amd "), strings.Contains(n, "ati "), strings.Contains(n, "rae "),
		strings.HasPrefix(n, "amd"):
		return AMD
	case strings.Contains(n, "intel"), strings.Contains(n, "iris"), strings.Contains(n, "uhd"), strings.Contains(n, "arc"):
		return Intel
	case strings.Contains(n, "apple"), strings.Contains(n, "m1"), strings.Contains(n, "m2"), strings.Contains(n, "m3"),
		strings.Contains(n, "m4"), strings.Contains(n, "m5"):
		return Apple
	default:
		return Other
	}
}
