package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The tests name the order they are testing rather than reading this machine's: the rule is
// that Windows and Linux disagree, and a suite that only ever exercised one of the two would
// let the argument that decides it rot.

// inOrder is Bundled with the order named, so a test can prove both orders instead of only
// the one this machine happens to use.
func inOrder(dir string, first Origin) Resolver { return Resolver{dir: dir, first: first} }

func writeProgram(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name+exeExt())
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookPrefersTheBundleWhereTheBundleIsTheOnlyThingThatWorks(t *testing.T) {
	dir := t.TempDir()
	want := writeProgram(t, dir, "pdftotext")

	got := inOrder(dir, OriginBundled).Look("pdftotext")
	if got.Path != want {
		t.Errorf("Look = %q, want the bundled copy at %q", got.Path, want)
	}
	if got.Origin != OriginBundled {
		t.Errorf("Origin = %q, want %q", got.Origin, OriginBundled)
	}
	if got.Name != "pdftotext" {
		t.Errorf("Name = %q: a finding names the program by this, so it has to be set", got.Name)
	}
}

func TestLookFallsBackToPATHWhenTheBundleLacksIt(t *testing.T) {
	empty := t.TempDir()
	if _, err := os.Executable(); err != nil {
		t.Fatal(err)
	}

	// "sh" is the program every machine this suite runs on has in PATH and no download ships.
	got := inOrder(empty, OriginBundled).Look("sh")
	if got.Found() && got.Origin == OriginBundled {
		t.Fatalf("an empty bundle produced %q", got.Path)
	}
	if runtime.GOOS != "windows" && got.Path == "" {
		t.Error("Look found neither the bundle nor PATH for a program this machine certainly has")
	}
}

func TestLookReportsMissingRatherThanAnEmptyPath(t *testing.T) {
	got := FromPath().Look("definitely-not-a-real-binary-xyz")
	if got.Found() {
		t.Fatalf("Path = %q for a program that does not exist", got.Path)
	}
	if got.Origin != OriginMissing {
		t.Errorf("Origin = %q, want %q", got.Origin, OriginMissing)
	}
	if got.Name != "definitely-not-a-real-binary-xyz" {
		t.Errorf("a missing program still has to be nameable, got %q", got.Name)
	}
}

func TestLinuxTakesTheMachineOwnCopyAndWindowsTheBundle(t *testing.T) {
	// The two orders are the whole per-platform rule, so both must exist rather than only
	// the one this test machine runs on. Bundled() picks one; that pick is the argument in
	// the package comment, and this is the line that notices if it is reversed.
	if runtime.GOOS == "windows" {
		if Bundled(t.TempDir()).first != OriginBundled {
			t.Error("Windows would look in PATH first, where an MSYS poppler cannot read the paths libris writes")
		}
		return
	}
	if Bundled(t.TempDir()).first != OriginPath {
		t.Error("Linux would look in the bundle first, where poppler cannot find its own data folder once moved")
	}
}

func TestBundledHere(t *testing.T) {
	dir := t.TempDir()
	writeProgram(t, dir, "pdftotext")

	if !inOrder(dir, OriginBundled).BundledHere() {
		t.Error("a folder full of programs said it was not a bundle")
	}
	if inOrder(filepath.Join(dir, "nope"), OriginBundled).BundledHere() {
		t.Error("a folder that does not exist said it was a bundle")
	}
	if FromPath().BundledHere() {
		t.Error("a build with no folder of its own claimed one")
	}
}

func TestLibDirOnlyWhenThereAreLibrariesToFind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows resolves a DLL from the executable's own directory; nothing is named to the child")
	}

	dir := t.TempDir()
	writeProgram(t, dir, "pdftotext")
	if got := inOrder(dir, OriginBundled).Look("pdftotext"); got.LibDir != "" {
		t.Errorf("LibDir = %q for a bundle with no libraries copied: that is a search path pointing at nothing", got.LibDir)
	}

	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "libpoppler.so.13"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "lib")
	if got := inOrder(dir, OriginBundled).Look("pdftotext"); got.LibDir != want {
		t.Errorf("LibDir = %q, want %q", got.LibDir, want)
	}
}

func TestEnvPutsTheCopiedLibrariesFirst(t *testing.T) {
	prog := Program{Name: "pdftotext", Path: "/x/pdftotext", Origin: OriginBundled, LibDir: "/bundle/lib"}
	env := prog.Env([]string{"PATH=/bin", "LD_LIBRARY_PATH=/machine/lib"})

	if got := envValue(env, "LD_LIBRARY_PATH"); got != "/bundle/lib:/machine/lib" {
		t.Errorf("LD_LIBRARY_PATH = %q, want the bundle ahead of what the machine had", got)
	}
	if n := countPrefix(env, "LD_LIBRARY_PATH="); n != 1 {
		t.Errorf("%d copies of LD_LIBRARY_PATH in the environment: which one the child reads is chance", n)
	}
	if envValue(env, "PATH") != "/bin" {
		t.Error("the rest of the environment was disturbed")
	}
}

// The fonts are the other half of a relocated rasteriser: a copied poppler asks fontconfig
// where the fonts are, and a machine that never installed fontconfig has no answer to give it.
func TestEnvNamesTheFontsThatTravelWithTheBundle(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, "fonts-conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "fonts.conf"), []byte("<config/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "fonts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fonts", "DejaVuSans.ttf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeProgram(t, dir, "pdftoppm")

	prog := inOrder(dir, OriginBundled).Look("pdftoppm")
	want := filepath.Join(confDir, "fonts.conf")
	if runtime.GOOS == "windows" {
		if prog.FontConfig != "" {
			t.Skip("Windows finds fonts beside the library itself; nothing is named to the child")
		}
		return
	}
	if prog.FontConfig != want {
		t.Fatalf("FontConfig = %q, want %q: the fonts in the download would go unread", prog.FontConfig, want)
	}

	env := prog.Env([]string{"FONTCONFIG_FILE=/etc/fonts/fonts.conf"})
	if got := envValue(env, "FONTCONFIG_FILE"); got != want {
		t.Errorf("FONTCONFIG_FILE = %q, want the bundle's own", got)
	}
	if n := countPrefix(env, "FONTCONFIG_FILE="); n != 1 {
		t.Errorf("%d copies of FONTCONFIG_FILE in the environment", n)
	}
}

func TestEnvLeavesAMachineOwnProgramAlone(t *testing.T) {
	prog := Program{Name: "pdftotext", Path: "/usr/bin/pdftotext", Origin: OriginPath}
	base := []string{"PATH=/bin", "LD_LIBRARY_PATH=/machine/lib"}

	got := prog.Env(base)
	if len(got) != len(base) {
		t.Errorf("Env changed the environment of a program that arrived from PATH: %v", got)
	}
}

func countPrefix(env []string, prefix string) int {
	n := 0
	for _, kv := range env {
		if len(kv) >= len(prefix) && kv[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}
