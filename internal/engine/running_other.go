//go:build !linux && !windows && !darwin

package engine

// processesUsing for a platform libris does not ship: it declines to claim anything is
// running, and freeBytes declines to invent a number, so the settings page shows an
// "unknown" rather than a figure nobody measured.
func processesUsing(string) bool { return false }
