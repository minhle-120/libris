package main

import (
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"time"
)

// writeVersion prints what this binary was built from.
//
// Nothing is stamped in at link time on purpose. The Go toolchain already writes
// the VCS revision, its commit time and whether the working tree was dirty into
// every binary — even under -trimpath — so a -X version string would be a second
// place for the same fact to live, and the second place is the one that can
// disagree with the code beside it.
//
// It matters because a release asset is a file on someone else's disk with no git
// directory anywhere near it, and because `libris` answers questions about
// sources: "which build produced this?" is the first thing a bug report has to
// answer.
func writeVersion(w io.Writer) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		// Only a build with -buildvcs=false, or a source tree exported without
		// its git directory, gets here.
		fmt.Fprintln(w, "libris (no build information)")
		return
	}
	fmt.Fprintln(w, versionLine(bi))
}

// versionLine renders build info the way git describes a commit: a name when
// there is a trustworthy one, then the revision, then when it happened.
//
// The "+dirty" suffix is the reason to read this at all. A workflow builds from a
// checkout, so its binaries are always clean; one that reports +dirty did not come
// from the workflow, whatever its filename claims.
func versionLine(bi *debug.BuildInfo) string {
	// `go build` of a main package always reports "devel": the command knows the
	// source directory, not the tag on it. Nothing to hide, so say "dev" rather
	// than a word that reads like a broken build, and let the revision identify
	// the build exactly.
	// The toolchain stamps a real tag here when the commit has one — so the tag
	// reaches the binary for free, with nothing to keep in sync — and a
	// pseudo-version (v0.0.0-<time>-<hash>) when it does not. The pseudo-version is
	// the revision wearing a version's clothes, so it is dropped rather than
	// printed twice; and "+dirty" is appended to the stamp by the toolchain itself,
	// which is why it is trimmed here and rendered once below.
	name := strings.TrimSuffix(bi.Main.Version, "+dirty")
	if name == "" || name == "devel" || name == "(devel)" || strings.HasPrefix(name, "v0.0.0-") {
		name = "dev"
	}

	rev, when, dirty := "", "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = shortRevision(s.Value)
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
				when = " " + t.UTC().Format(time.RFC3339)
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	if rev == "" {
		return "libris " + name + " (no revision)"
	}
	if dirty {
		rev += "+dirty"
	}
	return "libris " + name + " (" + rev + when + ")"
}

// shortRevision is how git shortens one: long enough to be unique in a repository
// of this size, short enough to type into a bug report.
func shortRevision(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
