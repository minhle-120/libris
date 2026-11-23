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
// skipping CPU devices, and printing "(none)" when there are no non-CPU devices. So:
// parse lines, and treat "(none)" as a real answer rather than a parse failure — it is how
// a build without GPU support says so, and "no GPU" is the one thing a user needs this
// report to be able to say.
//
// The two numbers are total and free-right-now. Only this source knows the second, because
// only a live ggml init sees what another program is holding; the OS probes report capacity
// alone. That is why this function exists alongside them rather than instead of them.
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

// deviceLine matches the memory suffix: "(8192 MiB, 7000 MiB free)".
var deviceLine = regexp.MustCompile(`\((\d+) MiB,\s*(\d+) MiB free\)$`)
