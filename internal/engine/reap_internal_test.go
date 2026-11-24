package engine

import (
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestTheDefaultSpawnerReapsWhatDiesOnItsOwn is the whole reason the default spawner waits.
//
// A child nobody waits on turns into a zombie when it exits, and a zombie answers kill(2): so
// `alive` says true, Status says running, Start says "already running", and Stop spends its
// ten-second grace period signalling a process that is already gone. That is not hypothetical
// for a model server — a GGUF it cannot load, an out-of-memory kill, or a Vulkan driver that
// refused the device all look exactly like this, and libris would report them as healthy for as
// long as it runs.
//
// The loop is the assertion, not a wait for a race: it ends the moment the pid stops answering,
// which on a working reap is a millisecond or two. It fails only if nobody waited on the child,
// which is the defect.
func TestTheDefaultSpawnerReapsWhatDiesOnItsOwn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows process handle is not a zombie; the Unix process table is what this checks")
	}
	sup := NewSupervisor(t.TempDir(), Seams{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	cmd := exec.Command("/bin/sh", "-c", "exit 3")
	pid, err := sup.reap(cmd)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("reap returned pid %d", pid)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid, signalPID) {
			return // reaped: the process table no longer answers for this pid
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("pid %d still answers signals after 5s: nobody waited on it, so Status would call a dead server running", pid)
}

// TestADeliberateStopIsNotLoggedAsADeath keeps the two endings apart. Stop signals the group and
// the reaper's Wait then sees a non-nil exit — the same shape as a crash — and only one of them
// is worth a line in the log.
func TestADeliberateStopIsNotLoggedAsADeath(t *testing.T) {
	var logged int
	sup := NewSupervisor(t.TempDir(), Seams{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	sup.exited = func(int, error) { logged++ }

	cmd := exec.Command("/bin/sh", "-c", "exit 7")
	pid, err := sup.reap(cmd)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	sup.mu.Lock()
	sup.stopping = true
	sup.mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid, signalPID) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if logged != 0 {
		t.Errorf("a stop libris asked for was logged as a death %d time(s)", logged)
	}
}
