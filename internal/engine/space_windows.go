//go:build windows

package engine

import (
	"fmt"
	"syscall"
	"unsafe"
)

// freeBytes asks Windows for the bytes available to this directory.
//
// Neither of the calls that answer this is exported by the standard library's syscall package:
// GetDiskFreeSpaceEx (per-user quota) lives in x/sys/windows, and GetDiskFreeSpace is not
// wrapped at all. This repo carries one dependency — the SQLite driver — and a second for a
// disk-space check would be a bad trade, so the W entry point is resolved directly from
// kernel32.
//
// It is the *Ex* variant on purpose: it reports the bytes available to this user rather than
// to the volume, which is the number that matters under OneDrive and per-user quotas, and it
// takes uint64 throughout so a volume larger than 4 GiB cannot wrap the way the cluster-count
// arithmetic of the non-Ex call would.
//
// The signature is the documented one and the buffers are stack locals whose addresses are
// taken in a single call — the pattern syscall itself uses internally. If kernel32 ever
// changes, or the DLL is absent, this returns !ok and the caller proceeds: a check that cannot
// run must not become a download that refuses to start.
func freeBytes(dir string) (int64, bool) {
	kernel32, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return 0, false
	}
	proc, err := kernel32.FindProc("GetDiskFreeSpaceExW")
	if err != nil {
		return 0, false
	}

	utf16, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}

	var freeToUser, total, totalFree uint64
	r1, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(utf16)),
		uintptr(unsafe.Pointer(&freeToUser)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r1 == 0 {
		return 0, false
	}
	if callErr != syscall.Errno(0) {
		return 0, false
	}
	if int64(freeToUser) < 0 { // more than 8 EiB, or a driver lying
		return 0, false
	}
	_ = fmt.Sprint // keep fmt for the error string below, if the call ever needs one
	return int64(freeToUser), true
}
