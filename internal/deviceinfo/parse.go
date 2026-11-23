package deviceinfo

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Every parser here is pure and takes recorded output. That is not ceremony: nvidia-smi
// needs an NVIDIA driver, rocm-smi an AMD one, Win32_VideoController a Windows box, and
// system_profiler a Mac. A unit test that required the hardware would be skipped on the
// machine it was meant to protect.

// classify names a vendor from the two things every platform agrees on: a vendor id where
// there is one, and otherwise words in the adapter's own name. Vendor ids win because a
// name is a marketing string — "AMD Radeon Graphics" on a laptop with an Intel iGPU is
// both true at once, and the PCI id knows which one this row is.
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

// parseLspci reads `lspci -nn`. Only display controllers are of interest — the class code
// in brackets is what selects them, because "VGA compatible controller" is a label that
// changed wording across lspci versions while 03xx did not.
//
// A typical line:
//
//	01:00.0 VGA compatible controller [0300]: NVIDIA Corporation GA104 [GeForce RTX 3070] (rev a1)
func parseLspci(out []byte) []Device {
	var dev []Device
	for _, line := range strings.Split(string(out), "\n") {
		bus, rest, ok := strings.Cut(line, " ")
		if !ok || !strings.Contains(rest, "[03") {
			continue
		}
		_, after, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		full := strings.TrimSpace(strings.TrimSuffix(after, "\r"))
		if full == "" {
			continue
		}

		d := Device{BusID: bus, Source: "lspci"}
		// The model people recognise is the bracketed part; the text before it is the
		// board name, which repeats the vendor and says "Silicon Motion" about a device
		// everybody calls by its chip.
		if i := strings.LastIndex(full, "["); i >= 0 {
			if j := strings.LastIndex(full, "]"); j > i {
				d.Name = strings.TrimSpace(full[i+1 : j])
			}
		}
		if d.Name == "" {
			d.Name = full
		}
		if i := strings.Index(full, "Corporation"); i > 0 {
			d.BusVendor = "0x10de" // only NVIDIA ships that exact vendor string
			d.Name = firstWordAfter(full, "Corporation", d.Name)
		}
		d.Kind = classify(d.Name, d.BusVendor)
		dev = append(dev, d)
	}
	return dev
}

// firstWordAfter drops a leading vendor word from a device name, so lspci and nvidia-smi
// do not produce two rows for one card because one of them said "NVIDIA" first.
func firstWordAfter(full, marker, fallback string) string {
	rest := strings.TrimSpace(full[strings.Index(full, marker)+len(marker):])
	rest = strings.TrimSpace(strings.TrimPrefix(rest, ","))
	if i := strings.Index(rest, "["); i > 0 {
		if j := strings.LastIndex(rest, "]"); j > i {
			return strings.TrimSpace(rest[i+1 : j])
		}
	}
	if rest == "" {
		return fallback
	}
	return rest
}

// parseNvidiaSmi reads the CSV of
//
//	nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader,nounits
//
// nounits is why the second field is parsed as MiB. With units it arrives as "8192 MiB"
// and the parse would have to know which locale the driver prints in.
func parseNvidiaSmi(out []byte) []Device {
	var dev []Device
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimSpace(line), ", ")
		if len(f) < 2 || strings.TrimSpace(f[0]) == "" {
			continue
		}
		mib, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64)
		if err != nil || mib <= 0 {
			// MIG slices and some vGPUs report "N/A" here. A row with a name and no size
			// is still worth showing, and the warning about an unknown size covers it.
			mib = 0
		}
		d := Device{Kind: Nvidia, Name: strings.TrimSpace(f[0]), TotalBytes: mib * mibToBytes, Source: "nvidia-smi"}
		if len(f) > 2 {
			d.Driver = strings.TrimSpace(f[2])
		}
		dev = append(dev, d)
	}
	return dev
}

// parseRocmSmi reads `rocm-smi --showmeminfo vram --json`. Two key spellings appear across
// releases ("VRAM Total Memory (B)" and "Total VRAM Memory (B)"), which is the whole
// reason this is a lookup over a map rather than a struct.
func parseRocmSmi(out []byte) []Device {
	var raw map[string]map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil
	}
	var dev []Device
	for card, fields := range raw {
		var bytes int64
		for _, k := range []string{"VRAM Total Memory (B)", "Total VRAM Memory (B)", "VRAM Total Used Memory (B)"} {
			if s, ok := fields[k].(string); ok {
				if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 && !strings.Contains(k, "Used") {
					bytes = n
					break
				}
			}
		}
		name := strings.TrimSpace(card)
		if v, ok := fields["Card ID"].(string); ok && strings.TrimSpace(v) != "" {
			name = strings.TrimSpace(v) + " (" + card + ")"
		}
		d := Device{Kind: AMD, Name: "AMD " + name, TotalBytes: bytes, Source: "rocm-smi"}
		if v, ok := fields["Driver Version"].(string); ok {
			d.Driver = strings.TrimSpace(v)
		}
		dev = append(dev, d)
	}
	return dev
}

// parseWinVideo reads Win32_VideoController through PowerShell's ConvertTo-Json, which
// emits a bare object for one adapter and an array for several — hence json.RawMessage
// and two shapes.
//
// AdapterRAM is a uint32 in WMI. On a card with 8 GB it reports either 4294967295 or
// 4294841344, i.e. "as much as I can count", not 8 GB. Those are turned into 0 (unknown)
// with a warning rather than shown as 4.0 GB, because a person choosing a 6 GB quant
// against a reported 4.0 GB would be talking themselves out of a card that fits it.
func parseWinVideo(out []byte) []Device {
	type row struct {
		Name          string `json:"Name"`
		AdapterRAM    any    `json:"AdapterRAM"`
		DriverVersion string `json:"DriverVersion"`
		PNPDeviceID   string `json:"PNPDeviceID"`
		VideoProcess  any    `json:"VideoProcessor"`
	}
	var rows []row
	if err := json.Unmarshal(out, &rows); err != nil {
		var one row
		if err := json.Unmarshal(out, &one); err != nil {
			return nil
		}
		rows = []row{one}
	}

	dev := make([]Device, 0, len(rows))
	for _, r := range rows {
		if strings.TrimSpace(r.Name) == "" {
			continue
		}
		d := Device{
			Name:   strings.TrimSpace(r.Name),
			Driver: strings.TrimSpace(r.DriverVersion),
			Source: "wmi",
		}
		if n := toInt64(r.AdapterRAM); n > 0 && !uint32Ceiling(n) {
			d.TotalBytes = n
		}
		// PCI\VEN_10DE&DEV_... is the vendor id in the only form Windows gives without a
		// second query.
		if i := strings.Index(strings.ToUpper(r.PNPDeviceID), "VEN_"); i >= 0 {
			id := r.PNPDeviceID[i+4:]
			if j := strings.IndexAny(id, "&\\"); j > 0 {
				id = id[:j]
			}
			d.BusVendor = "0x" + strings.ToLower(strings.TrimSpace(id))
			d.BusID = strings.ToUpper(r.PNPDeviceID)
		}
		d.Kind = classify(d.Name, d.BusVendor)
		dev = append(dev, d)
	}
	return dev
}

// uint32Ceiling recognises the WMI value that means "bigger than I can report".
func uint32Ceiling(n int64) bool { return n >= 0xFFF00000 }

// parseMacDisplays reads `system_profiler SPDisplaysDataType -json`. Apple silicon has no
// separate VRAM at all — the model shares system memory — so a blank TotalBytes is the
// correct answer and is not warned about on its own (see warn()).
func parseMacDisplays(out []byte) []Device {
	var doc struct {
		Displays []struct {
			Name    string `json:"_name"`
			VRAM    string `json:"spdisplays_vram"`
			Vendor  string `json:"spdisplays_vendor"`
			Metal   string `json:"spdisplays_metal"`
			Chipset string `json:"spdisplays_chipsettype"`
		} `json:"SPDisplaysDataType"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil
	}
	var dev []Device
	for _, d := range doc.Displays {
		row := Device{Name: strings.TrimSpace(d.Name), Source: "system_profiler"}
		row.Kind = classify(row.Name, "")
		if row.Kind == Other {
			row.Kind = classify(d.Vendor+" "+d.Chipset, "")
		}
		if n := parseSizeWords(d.VRAM); n > 0 {
			row.TotalBytes = n
		}
		dev = append(dev, row)
	}
	return dev
}

// parseSizeWords reads what macOS prints for memory: "8 GB", "16 GB (shared)", "2 GB".
func parseSizeWords(s string) int64 {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) < 2 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || n <= 0 {
		return 0
	}
	// Binary units, matching how every other size in this package is computed and how
	// formatBytes displays them; macOS prints "8 GB" meaning 8 GiB for VRAM.
	switch strings.ToUpper(strings.Trim(fields[1], "()")) {
	case "TB":
		return int64(n * 1024 * float64(gib))
	case "GB":
		return int64(n * float64(gib))
	case "MB":
		return int64(n * float64(mib))
	}
	return 0
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i
	}
	return 0
}

const (
	mib          = int64(1 << 20)
	gib          = int64(1 << 30)
	mibToBytes   = mib
	pciClassMask = "03"
)
