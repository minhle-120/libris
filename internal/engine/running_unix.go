//go:build !windows

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// processesUsing on Linux reads /proc/<pid>/exe, which is a symlink to the file the running
// program *is* — not what it was called, and not what its argv says, so a renamed or
// hard-linked copy still counts. /proc does not exist on macOS, where the ps fallback below
// is the best available without cgo.
func processesUsing(exePath string) bool {
	if _, err := os.Stat("/proc"); err == nil {
		entries, err := os.ReadDir("/proc")
		if err == nil {
			want := exeOf(exePath)
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if _, err := strconv.Atoi(e.Name()); err != nil {
					continue // /proc/self, /proc/sys — not processes
				}
				target, err := os.Readlink("/proc/" + e.Name() + "/exe")
				if err == nil && exeOf(target) == want {
					return true
				}
			}
			return false
		}
	}

	// No /proc: ask ps for full command lines.
	out, err := exec.Command("ps", "-eo", "args=").Output()
	if err != nil {
		// ps missing or refusing is not proof that nothing is running. The caller treats a
		// true here as "do not delete", which is the direction that costs a retry rather
		// than a broken install.
		return true
	}
	return psArgs(out, exePath)
}

// freeBytes is a real statfs. A guessed number is the failure mode this avoids: the spec
// this work follows had a constant here, and a constant would let a 5 GB model be offered
// onto a 4 GB partition.
func freeBytes(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Clean(dir), &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

var _ = strings.TrimSpace
