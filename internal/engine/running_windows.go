//go:build windows

package engine

// processesUsing on Windows asks whether the executable can be moved. There is no stdlib call
// that names the process holding a file open, and a tool that does would be a dependency this
// repo does not want for one safety check.
func processesUsing(exePath string) bool { return busyRename(exePath) }
