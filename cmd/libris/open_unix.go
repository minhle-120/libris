//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts the helper in its own process group. Without it the helper is in
// libris's group, so the SIGINT that Ctrl-C sends to libris is delivered to a
// browser launcher too — and a browser that is still starting up is then killed
// for a reason that has nothing to do with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
