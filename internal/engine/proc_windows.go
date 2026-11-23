//go:build windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Windows has no process groups and no way to ask llama-server politely to unload its
// models, so both signals mean "terminate". The grace period in Stop still runs: a process
// that exits on its own is a process that released its memory.
const (
	sigTerminate = syscall.SIGTERM
	sigForce     = syscall.SIGTERM
)

func setGroupAttr(cmd *exec.Cmd) {}

// processQueryLimitedInformation is the access right that asks "does this pid still exist"
// without needing rights over the process itself. syscall does not export it; it is one of
// the Win32 constants that does not change.
const processQueryLimitedInformation = 0x1000

// signalPID is the whole of the platform surface. Signal 0 is the liveness probe, and on
// Windows that is an OpenProcess rather than a signal: the question is whether the id still
// names something we may open, not whether it will notice a nudge.
func signalPID(pid int, sig syscall.Signal) error {
	if sig == 0 {
		h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
		if err != nil {
			return err
		}
		_ = syscall.CloseHandle(h)
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// The Windows console has no SIGINT to route to a detached child; the grace window in Stop
// is the only politeness available.
var _ = time.Now
