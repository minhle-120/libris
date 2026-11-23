//go:build !linux && !windows && !darwin

package engine

// freeBytes reports "unknown" rather than a number. The caller treats !ok as "we could not
// check", and proceeds — which is the honest order: a failed statfs is not a reason to
// refuse a download on a platform nobody builds for.
func freeBytes(string) (int64, bool) { return 0, false }
