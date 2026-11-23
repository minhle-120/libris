package deviceinfo

import "context"

// gpuDevices is the platform-specific half of the report. The collectors that need sysfs,
// lspci, WMI or system_profiler live in the _linux / _windows / _darwin files, so this
// file stays buildable for every target — the release workflow cross-compiles from one
// machine, and a package that only compiles natively cannot be vetted there.
func gpuDevices(ctx context.Context, o Options) []Device {
	return collect(ctx, o)
}
