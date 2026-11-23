//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// The creation flags Go's syscall package does not export. CREATE_NEW_PROCESS_GROUP
// keeps a Ctrl-C in the console that launched libris from reaching the helper, and
// DETACHED_PROCESS stops rundll32 from inheriting that console — which matters most
// for the double-click case, where there is no console to inherit and a helper that
// asks for one would flash a window at the user.
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}
}
