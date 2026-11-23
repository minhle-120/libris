package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrRunning means a llama-server is using the files the caller wanted to change. It is a
// sentinel because the caller's answer is a status code, not a message: the HTTP layer turns
// it into 409, and the settings page into a sentence that says what to do about it.
var ErrRunning = errors.New("engine: llama-server is still running from that build")

// probeSuffix is the temporary name the Windows probe moves an executable to. A dotfile-ish
// name on purpose: a half-finished probe must not look like a second server binary to the
// launcher's walk for llama-server, which is a directory scan of exactly this folder.
const probeSuffix = ".libris-probe"

// running reports whether some process has this executable open.
//
// It is a var so the tests can say what the machine reports without launching one: a test that
// started a real llama-server to prove a refusal works would be skipped on the machine it was
// written to protect, and slower than the thing it checks. The platform answers are in
// running_unix.go and running_windows.go.
//
// The reason it exists at all is Windows, where deleting a tree whose executable is running
// does not fail cleanly — it leaves part of the directory behind, so the build neither runs
// nor uninstalls and the next launch is a mystery. Refusing before the attempt is the only
// answer that leaves the disk in a state a person can reason about. On the Unix side the same
// check is cheap and exact, so it is worth doing there too.
var running = processesUsing

// busyRename is the Windows answer: a mapped executable image cannot be renamed, so an
// attempt that fails means something has the file open. It renames and renames back.
//
// The window between the two is the honest weakness — a launch landing inside it would fail —
// and it is far narrower than the window a failed delete leaves behind. Moving the file back
// failing is treated as busy as well, because reporting success while the executable is
// sitting under a .libris-probe name would be worse than the lock we were looking for.
func busyRename(path string) bool {
	tmp := path + probeSuffix
	if err := rename(path, tmp); err != nil {
		return true
	}
	if err := rename(tmp, path); err != nil {
		return true
	}
	return false
}

// rename is os.Rename, overridable so the probe's two failure paths are testable without
// holding a file open from another process.
var rename = os.Rename

// exeOf resolves a path to the file it actually is, so that the two spellings of one file
// compare equal: /proc/<pid>/exe reports a target that may differ textually from the path we
// were given (a symlinked data directory, or the " (deleted)" marker Linux appends when the
// file was unlinked under a running process — which is exactly the state a half-completed
// uninstall leaves, so it has to count as the same file).
func exeOf(path string) string {
	path = strings.TrimSuffix(path, " (deleted)")
	if path == "" {
		return ""
	}
	if p, err := filepath.EvalSymlinks(path); err == nil {
		return p
	}
	return filepath.Clean(path)
}

// sameExe is exeOf equality, named so the tests and the collectors say the same thing.
func sameExe(a, b string) bool {
	want, got := exeOf(a), exeOf(b)
	return want != "" && want == got
}

// psArgs is the fallback for a system with no /proc: "ps -eo args=" prints one command line
// per process, and the first field is the program.
//
// Weaker than /proc, and stated as such: a server launched by relative path is not found here.
// It is used only where nothing better exists, and a miss on macOS costs an uninstall that
// half-failed, where the same miss on Windows costs a broken tree.
func psArgs(out []byte, exePath string) bool {
	want := exeOf(exePath)
	if want == "" {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		program := strings.Fields(line)[0]
		if strings.HasSuffix(program, "/") {
			continue
		}
		if exeOf(program) == want {
			return true
		}
	}
	return false
}
