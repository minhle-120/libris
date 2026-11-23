package engine

import (
	"path/filepath"
	"strings"
)

// running reports whether llama-server is executing from the build about to be deleted.
//
// A process manager would be the easy answer, and does not exist yet: this phase is the
// settings page and the download, so libris has no children to ask. The operating system is
// asked instead, because the alternative — assuming nothing is running — would let the
// Delete button remove the binaries an answer is being streamed from, which on Windows
// fails halfway and leaves a tree that is neither installed nor removable.
//
// It is a variable so the tests can answer "yes" without starting a process: a unit test may
// not launch llama-server, and one that skipped on that ground would defend nothing.
var running = processesUsing

// exeOf is how a path is compared against what the OS reports: cleaned, and on Windows
// case-insensitively, because a path typed by a person and a path returned by WMI are the
// same file spelled differently.
func exeOf(p string) string {
	p = filepath.Clean(p)
	if isWindows {
		return strings.ToLower(p)
	}
	return p
}

// psArgs reads `ps -eo args=` looking for a program whose argv[0] is the engine. A name
// match would be wrong here: a llama-server the user started themselves from /opt is not
// libris's to block a deletion over, and libris's own copy is exactly this path.
func psArgs(out []byte, exePath string) bool {
	want := exeOf(exePath)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if exeOf(fields[0]) == want {
			return true
		}
	}
	return false
}
