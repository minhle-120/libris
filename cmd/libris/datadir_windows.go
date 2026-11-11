//go:build windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// userShellFolders is where the shell records the real location of each of the
// user's well-known folders. The value is REG_EXPAND_SZ, so it normally reads
// `%USERPROFILE%\Documents`.
const userShellFolders = `Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders`

// documentsDir asks the shell which folder *is* the user's Documents directory
// rather than assuming %USERPROFILE%\Documents, because the two disagree for
// good reason: Known Folder redirection moves the folder into OneDrive, or onto
// another disk, and a corpus must be findable again in whichever place it is.
func documentsDir() string {
	home := os.Getenv("USERPROFILE")
	if dir := registryDocumentsDir(); usableDir(dir, home) {
		return dir
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "Documents")
}

// registryDocumentsDir reads the Personal value — the shell's name for
// Documents — out of HKCU. Registry access is not part of Go's standard surface
// for this, so it is done through syscall rather than a dependency, and any
// failure at all falls through to the %USERPROFILE% guess.
func registryDocumentsDir() string {
	subkey, err := syscall.UTF16PtrFromString(userShellFolders)
	if err != nil {
		return ""
	}
	var key syscall.Handle
	// WOW64_64KEY so a 32-bit build reads the 64-bit view rather than a
	// redirected key that a redirection-aware installer never wrote.
	err = syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, subkey, 0,
		syscall.KEY_READ|syscall.KEY_WOW64_64KEY, &key)
	if err != nil {
		return ""
	}
	defer syscall.RegCloseKey(key)

	name, err := syscall.UTF16PtrFromString("Personal")
	if err != nil {
		return ""
	}
	buf := make([]uint16, 1024)
	var typ uint32
	size := uint32(len(buf) * 2)
	err = syscall.RegQueryValueEx(key, name, nil, &typ,
		(*byte)(unsafe.Pointer(&buf[0])), &size)
	if err != nil || (typ != syscall.REG_EXPAND_SZ && typ != syscall.REG_SZ) {
		return ""
	}
	return expandPath(syscall.UTF16ToString(buf[:size/2]), os.Getenv("USERPROFILE"))
}

// anchored reports whether a path names a location rather than a location
// relative to the current drive. filepath.IsAbs is not enough on Windows: it
// calls `\Users\name\Documents` absolute, and that resolves against whichever
// drive the process started on, so the corpus would land somewhere different
// depending on how libris was launched.
func anchored(dir string) bool { return filepath.VolumeName(dir) != "" }
