//go:build windows

package engine

import (
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

// processesUsing asks WMI, which is the only way on Windows to ask what is running without
// a driver or a cgo dependency. The filter is built server-side so the whole process table
// is not pulled into a pipe to be scanned in Go.
func processesUsing(exePath string) bool {
	const query = `Get-CimInstance Win32_Process -Filter "Name='llama-server.exe'" | ` +
		`Select-Object -ExpandProperty ExecutablePath`

	for _, shell := range []string{"pwsh", "powershell"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		out, err := exec.Command(shell, "-NoProfile", "-NonInteractive", "-Command", query).Output()
		if err != nil {
			continue
		}
		if psArgs(out, exePath) {
			return true
		}
		// WMI answers with the path in its own spelling; compare on both sides.
		want := exeOf(exePath)
		for _, line := range strings.Split(string(out), "\n") {
			if exeOf(strings.TrimSpace(line)) == want {
				return true
			}
		}
		return false
	}
	// No PowerShell to ask with: err toward not deleting.
	return true
}

// freeBytes calls GetDiskFreeSpaceEx, which unlike the free space in the drive's root
// reports what *this user* may write — the number that matters when a quota is what makes a
// download fail.
func freeBytes(dir string) (int64, bool) {
	var free, total, freeBytesAvailableToCaller uint64
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	r1, _, e := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW").Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeBytesAvailableToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&free)))
	if r1 == 0 {
		_ = e
		return 0, false
	}
	return int64(freeBytesAvailableToCaller), true
}
