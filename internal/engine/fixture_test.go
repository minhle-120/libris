package engine_test

// The fixture every supervisor test draws from: the spawn, the readiness probe and the signal
// are all seams, so what a test exercises is the record, the wait, and the state the page reads
// — never a process. It lives here rather than beside the tests because the drift rule now has
// its own file, and these helpers belong to the process half, not to either test file.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"libris/internal/engine"
)

// A supervisor test starts nothing: the spawn, the readiness probe and the signal are all
// seams, so what is under test is the record, the wait and the state the page reads.
func serveFixture(t *testing.T) (*engine.Supervisor, *engine.ServeOptions, *spawned) {
	t.Helper()
	dir := t.TempDir()
	sp := &spawned{}
	sup := engine.NewSupervisor(dir, engine.Seams{
		Spawn: func(cmd *exec.Cmd) (int, error) {
			sp.called = true
			sp.alive = true
			sp.cmd = cmd
			// A real llama.cpp writes its own complaint into the log after it is spawned — a
			// port it could not bind, a key it refused — and that is the only way the fact can
			// ever reach a caller. A test that wants a collision has to have the fake say it the
			// same way, after the start began, rather than pre-loading the file.
			if sp.logOnSpawn != "" {
				f, err := os.OpenFile(filepath.Join(dir, "logs", "server.log"),
					os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
				if err == nil {
					_, _ = f.WriteString(sp.logOnSpawn)
					_ = f.Close()
				}
			}
			// Every --port the supervisor asked for, in order, so a test can see how many
			// ports a start tried and in what order.
			for i, a := range cmd.Args {
				if a == "--port" && i+1 < len(cmd.Args) {
					sp.ports = append(sp.ports, cmd.Args[i+1])
				}
			}
			// This test's own pid is the only pid it may claim is alive.
			return os.Getpid(), nil
		},
		Probe: func(ctx context.Context, url string) error {
			sp.probed = url
			// Killing the fake process the first time anything asks it a question is how a test
			// says "it died": aliveness here is what Signal reports, never what the operating
			// system thinks of the pid a fake handed back.
			if sp.dies {
				sp.alive = false
			}
			return sp.probeErr
		},
		Signal: func(pid int, sig syscall.Signal) error {
			sp.signals = append(sp.signals, sig)
			if sig == 0 {
				if sp.alive {
					return nil
				}
				return syscall.ESRCH
			}
			// Whatever the polite signal was, the fake agrees it worked: the point is the
			// record and the wait, not which number llama.cpp answers to.
			sp.alive = false
			return nil
		},
		Now: func() time.Time { return time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC) },
	})
	opts := engine.ServeOptions{
		Bin:       "/tmp/llama-server",
		Port:      8080,
		Preset:    filepath.Join(dir, "presets", "libris.ini"),
		Log:       filepath.Join(dir, "logs", "server.log"),
		ModelsMax: 3,
	}
	return sup, &opts, sp
}

type spawned struct {
	called  bool
	alive   bool
	cmd     *exec.Cmd
	probed  string
	ports   []string
	signals []syscall.Signal
	// probeErr is what readiness answers instead of "up", so a test can say the port was
	// taken without starting anything that could take it.
	probeErr error
	// dies kills the fake process the first time it is probed, which is how a test asks
	// whether a death is noticed rather than waited out.
	dies bool
	// logOnSpawn is appended to the log by the fake process once it exists, which is how a
	// test makes a fact this attempt's own rather than a predecessor's.
	logOnSpawn string
}
