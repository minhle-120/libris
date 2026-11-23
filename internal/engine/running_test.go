package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRunningIsExactAboutWhichBuild covers the reason the check compares resolved paths and
// not process names: a llama-server the user started from a different build is not *this*
// build, and deleting or replacing it would be a serious mistake.
func TestRunningIsExactAboutWhichBuild(t *testing.T) {
	dir := t.TempDir()
	ours := filepath.Join(dir, "b12649", "bin", "llama-server")
	theirs := filepath.Join(dir, "elsewhere", "llama-server")
	for _, p := range []string{ours, theirs} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	was := running
	defer func() { running = was }()
	running = func(exe string) bool { return exe == theirs }

	if running(ours) {
		t.Error("a different build's llama-server was reported as ours")
	}
	if !running(theirs) {
		t.Error("the build that is running was not reported")
	}

	// " (deleted)" is the state a half-finished uninstall leaves: the file is gone from the
	// directory and a server is still executing it. It has to be the same file, or the refusal
	// is skipped and the rest of the tree is deleted under a running process.
	if !sameExe(theirs, theirs+" (deleted)") {
		t.Error("a binary unlinked from under a running process was a different file")
	}
}

// TestInstallRefusesToReplaceARunningBuild is the same rule one step earlier than the delete:
// unpacking over a running binary is the failure that leaves a server whose code and libraries
// no longer match.
func TestInstallRefusesToReplaceARunningBuild(t *testing.T) {
	f := newFake(t)
	m := Open(t.TempDir(), nil)
	m.api = f.addr + "/api"

	in, err := m.Install(context.Background(), "b12649", Vulkan)
	if err != nil {
		t.Fatal(err)
	}
	installed := in.Path

	was := running
	defer func() { running = was }()
	running = func(exe string) bool { return exe == installed }

	// Asking again for what is installed is fine while it runs, because nothing changes: it
	// answers from the record and never opens the tree.
	if _, err := m.Install(context.Background(), "b12649", Vulkan); err != nil {
		t.Errorf("re-asking for the installed tag while it runs: %v", err)
	}

	// Now the case the guard exists for, which needs a *different* tag: upstream has moved on
	// to a newer nightly, which is what an update looks like.
	f.offerNewer = true
	if _, err := m.Install(context.Background(), f.newerTag(), Vulkan); !errors.Is(err, ErrRunning) {
		t.Errorf("Install over a running build = %v, want ErrRunning", err)
	}
	if have, ok := m.Status(); !ok || have.Tag != "b12649" {
		t.Errorf("a refused update left the install at %+q ok=%v, want the tag it had", have.Tag, ok)
	}

}

// TestBusyRenameIsReversible exercises the Windows probe's branches on a filesystem where
// nothing holds the file open, because the branch that matters is the one that can be produced
// without a second process.
func TestBusyRenameIsReversible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llama-server")
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	was := rename
	defer func() { rename = was }()

	if busyRename(path) {
		t.Error("a free file was reported as busy")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the probe did not put the file back")
	}
	if _, err := os.Stat(path + probeSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Error("the probe left its temporary name behind")
	}

	// A locked executable cannot be renamed at all: that is the case being imitated.
	rename = func(from, to string) error { return os.ErrPermission }
	if !busyRename(path) {
		t.Error("a file that could not be renamed was reported as free")
	}

	// The rename back failing is the worse case. It must still report busy rather than let a
	// caller proceed while the executable is sitting under a probe name.
	calls := 0
	rename = func(from, to string) error {
		calls++
		if calls == 1 {
			return os.Rename(from, to)
		}
		return os.ErrPermission
	}
	if !busyRename(path) {
		t.Error("a file left under its probe name was reported as free")
	}
	if _, err := os.Stat(path + probeSuffix); err != nil {
		t.Error("the second rename never happened, so this never reached the branch")
	}
}
