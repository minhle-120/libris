//go:build darwin

package deviceinfo

import "context"

// collect on macOS. Not a release target (the workflow builds linux and windows
// amd64), but the code is cheap, it keeps this package honest on a development machine,
// and Apple silicon has no separate VRAM at all — the model shares system memory, so a
// blank TotalBytes is the correct answer there rather than a probe that failed.
func collect(ctx context.Context, o Options) []Device {
	if _, err := o.LookPath("system_profiler"); err != nil {
		return nil
	}
	out, err := o.Run(ctx, "system_profiler", "SPDisplaysDataType", "-json")
	if err != nil {
		return nil
	}
	return parseMacDisplays(out)
}
