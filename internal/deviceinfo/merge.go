package deviceinfo

import (
	"context"
	"os"
	"strings"
)

// Options additions used only by the collectors, kept in one place.
func (o Options) readDir(name string) ([]os.DirEntry, error) {
	if o.ReadDir != nil {
		return o.ReadDir(name)
	}
	return os.ReadDir(name)
}

// merge folds a probe's rows into the rows already on the table.
//
// It exists because the probes see the same card differently: lspci names it
// "GeForce RTX 3070" with no size, nvidia-smi names it "NVIDIA GeForce RTX 3070" with a
// size and a driver version, and a report listing both claims the machine has two GPUs —
// which is exactly the warning this package must not raise by mistake.
//
// Two rows are one card when the bus id is the same, or when the names are equal after
// normName. Not when one name contains the other, which was the first attempt and is
// wrong: "RTX 3060" is contained in "RTX 3060 Ti", so a containment rule merges two
// physical cards into one row and the second GPU disappears from the report silently. A
// missed merge leaves two rows and a visibly wrong "several adapters" warning; a false
// merge leaves a machine that looks smaller than it is. Only the first is recoverable.
//
// A size only ever fills a blank, never overwrites a number another probe measured.
func merge(dst, src []Device) []Device {
	for _, s := range src {
		i := -1
		for j := range dst {
			if sameBus(dst[j].BusID, s.BusID) {
				// A bus id is the only identifier two independent probes are guaranteed to
				// agree on, so it outranks any name comparison.
				i = j
				break
			}
			if sn, dn := normName(s.Name), normName(dst[j].Name); sn != "" && sn == dn {
				i = j
				break
			}
		}
		if i < 0 {
			dst = append(dst, s)
			continue
		}
		d := &dst[i]
		if d.TotalBytes == 0 {
			d.TotalBytes = s.TotalBytes
		}
		// Free memory only a live probe knows, so it arrives from one source and from no
		// other; filling a blank is the whole rule, as with a size.
		if d.FreeBytes == 0 {
			d.FreeBytes = s.FreeBytes
		}
		if d.Driver == "" {
			d.Driver = s.Driver
		}
		if d.BusID == "" {
			d.BusID = s.BusID
		}
		if d.Kind == Other {
			d.Kind = s.Kind
		}
		if s.Source != "" && !strings.Contains(d.Source, s.Source) {
			d.Source += "+" + s.Source
		}
	}
	return dst
}

// normName is the comparison form of a device name: lower case, no punctuation, and with
// the vendor word removed since one probe includes it and the other does not. Model
// numbers and suffixes survive — an RTX 3060 and an RTX 3060 Ti are two cards and must
// stay two rows.
func normName(s string) string {
	s = strings.ToLower(s)
	for _, r := range "(),'\"." {
		s = strings.ReplaceAll(s, string(r), " ")
	}
	out := make([]string, 0, 6)
	for _, w := range strings.Fields(s) {
		switch w {
		case "nvidia", "geforce", "amd", "ati", "intel", "corporation", "inc", "radeon", "graphics", "series":
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// sysfsDevice is one entry under /sys/bus/pci/devices. Only display classes matter: the
// class code's first byte is 03 for display and 04 for multimedia, and a capture card
// is a 04xx device a user does not want listed as a GPU.
func sysfsDevice(busID, class, vendor string) (Device, bool) {
	if len(class) < 5 || !strings.HasSuffix(class[:4][:2]+class[2:4], pciClassMask) {
		return Device{}, false
	}
	d := Device{BusID: busID, BusVendor: strings.ToLower(strings.TrimSpace(vendor)), Source: "sysfs"}
	d.Kind = classify(d.Name, d.BusVendor)
	return d, true
}

// attachVendors fills the vendor id onto rows that came from lspci, which prints no vendor
// id. Matching is by the bus id both use, so no name fuzzy-matching is involved here.
func attachVendors(rows, byBus []Device) []Device {
	for i := range rows {
		if rows[i].BusVendor != "" {
			continue
		}
		for _, v := range byBus {
			if sameBus(rows[i].BusID, v.BusID) {
				rows[i].BusVendor = v.BusVendor
				if rows[i].Kind == Other {
					rows[i].Kind = v.Kind
				}
				break
			}
		}
	}
	return rows
}

// sameBus tolerates the domain prefix: lspci prints "01:00.0", sysfs directories are
// named "0000:01:00.0".
func sameBus(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	t := func(s string) string {
		if i := strings.LastIndex(s, ":"); i > 0 && len(s)-i == 7 {
			return s[i+1:]
		}
		return s
	}
	return t(a) == t(b)
}

// hasVendor reports whether any row belongs to a vendor id, which is how the AMD path
// decides whether rocm-smi is worth running at all.
func hasVendor(rows []Device, vendor string) bool {
	for _, r := range rows {
		if r.BusVendor == vendor {
			return true
		}
	}
	return false
}

var _ = context.Background
