package main

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

func buildInfo(version, rev, when string, dirty bool) *debug.BuildInfo {
	return &debug.BuildInfo{
		Main: debug.Module{Path: "libris/cmd/libris", Version: version},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: rev},
			{Key: "vcs.time", Value: when},
			{Key: "vcs.modified", Value: boolString(dirty)},
		},
	}
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestVersionLineNamesTheRevisionAndWhenItWasCommitted(t *testing.T) {
	bi := buildInfo("devel", "f5469dc598c6c97fa17297ae2af1535a4fae8ee1", "2026-11-23T01:36:04Z", false)

	want := "libris dev (f5469dc598c6 2026-11-23T01:36:04Z)"
	if got := versionLine(bi); got != want {
		t.Errorf("versionLine() = %q, want %q", got, want)
	}
}

func TestVersionLineSaysWhenTheTreeWasDirty(t *testing.T) {
	// The whole reason to read the version at all: a workflow builds from a
	// checkout, so anything it publishes is clean by construction.
	bi := buildInfo("devel", "f5469dc598c6c97fa17297ae2af1535a4fae8ee1", "2026-11-23T01:36:04Z", true)

	got := versionLine(bi)
	want := "libris dev (f5469dc598c6+dirty 2026-11-23T01:36:04Z)"
	if got != want {
		t.Errorf("versionLine() = %q, want %q", got, want)
	}
}

func TestVersionLineWithoutARevisionSaysSo(t *testing.T) {
	// -buildvcs=false, or a source tree exported without its git directory. The
	// answer must not be a bare "libris dev ()", which reads as a version.
	bi := &debug.BuildInfo{Main: debug.Module{Version: "devel"}}

	if got := versionLine(bi); got != "libris dev (no revision)" {
		t.Errorf("versionLine() = %q", got)
	}
}

func TestVersionLineUsesATagWhenTheCommitHasOne(t *testing.T) {
	// Verified against a real build: a tagged commit stamps the tag into the binary,
	// so the release asset names the release without anything being stamped at link
	// time. The toolchain appends "+dirty" to the tag as well; rendering it twice is
	// the bug this asserts against.
	bi := buildInfo("v0.1.0+dirty", "f5469dc598c6c97fa17297ae2af1535a4fae8ee1", "2026-11-23T01:36:04Z", true)

	got := versionLine(bi)
	want := "libris v0.1.0 (f5469dc598c6+dirty 2026-11-23T01:36:04Z)"
	if got != want {
		t.Errorf("versionLine() = %q, want %q", got, want)
	}

	bi = buildInfo("v0.1.0", "f5469dc598c6c97fa17297ae2af1535a4fae8ee1", "2026-11-23T01:36:04Z", false)
	want = "libris v0.1.0 (f5469dc598c6 2026-11-23T01:36:04Z)"
	if got := versionLine(bi); got != want {
		t.Errorf("versionLine() = %q, want %q", got, want)
	}
}

func TestVersionLineDropsAPseudoVersion(t *testing.T) {
	// An untagged commit is stamped v0.0.0-<time>-<hash>: the same revision the
	// parenthetical already carries, dressed as a version. Saying "dev" is the
	// honest reading of it.
	bi := buildInfo("v0.0.0-20261123013604-f5469dc598c6+dirty", "f5469dc598c6c97fa17297ae2af1535a4fae8ee1", "2026-11-23T01:36:04Z", true)

	got := versionLine(bi)
	want := "libris dev (f5469dc598c6+dirty 2026-11-23T01:36:04Z)"
	if got != want {
		t.Errorf("versionLine() = %q, want %q", got, want)
	}
}

func TestWriteVersionOnATestBinaryRefusesToInventARevision(t *testing.T) {
	// `go test` binaries are not stamped with VCS info — only `go build` ones are —
	// so the package's own test binary is the case where there is nothing to say.
	// It must say that, rather than print a paren pair that reads as a version.
	// A built binary is the stamped case, and it is checked by building one:
	// `make build && ./libris -version`.
	var b bytes.Buffer
	writeVersion(&b)

	if !strings.Contains(b.String(), "no revision") {
		t.Errorf("writeVersion() = %q, want the no-revision reading under go test", b.String())
	}
	t.Logf("this build: %s", b.String())
}
