//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
)

// sigTerminate is how llama.cpp is asked to stop: it unloads what it loaded and closes the
// socket, rather than vanishing and leaving the GPU claimed.
const sigTerminate = syscall.SIGINT

// sigForce is what is left when the polite one was ignored.
const sigForce = syscall.SIGKILL

// setGroupAttr puts the router in its own process group so that a stop is one signal to a
// negative pid, and reaches the model instances the router forked.
func setGroupAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalPID is the whole of the platform surface: a negative pid is a group, and sig 0 asks
// whether something is there without disturbing it.
func signalPID(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
