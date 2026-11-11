package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDataDirIsDocumentsLibris(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DOCUMENTS_DIR", "")

	want := filepath.Join(home, "Documents", "libris")
	if got := defaultDataDir(); got != want {
		t.Errorf("defaultDataDir() = %q, want %q", got, want)
	}
}

func TestDefaultDataDirFollowsTheLocalisedDocumentsFolder(t *testing.T) {
	// A German desktop puts the folder in ~/Dokumente and records that in
	// user-dirs.dirs. Creating ~/Documents beside it would be the bug.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(home, ".config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "# user-dirs.dirs\nXDG_DESKTOP_DIR=\"$HOME/Desktop\"\n" +
		"XDG_DOCUMENTS_DIR=\"$HOME/Dokumente\"\n"
	if err := os.WriteFile(filepath.Join(dir, "user-dirs.dirs"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(home, "Dokumente", "libris")
	if got := defaultDataDir(); got != want {
		t.Errorf("defaultDataDir() = %q, want %q", got, want)
	}
}

func TestDefaultDataDirRejectsABrokenDocumentsEntry(t *testing.T) {
	// Desktop configurations really do write XDG_DOCUMENTS_DIR="/". The corpus
	// does not belong at the root of the filesystem, so fall back instead.
	for _, value := range []string{`"/"`, `"$HOME"`, `""`, `"relative/path"`} {
		t.Run(value, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")

			dir := filepath.Join(home, ".config")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			line := "XDG_DOCUMENTS_DIR=" + value + "\n"
			if err := os.WriteFile(filepath.Join(dir, "user-dirs.dirs"), []byte(line), 0o644); err != nil {
				t.Fatal(err)
			}

			want := filepath.Join(home, "Documents", "libris")
			if got := defaultDataDir(); got != want {
				t.Errorf("defaultDataDir() = %q, want the fallback %q", got, want)
			}
		})
	}
}

func TestDefaultDataDirWithoutAHomeDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	// os.UserHomeDir and os.UserConfigDir both need $HOME on unix, so there is
	// nowhere sensible left to put the corpus and the CWD-relative name is used.
	if got := defaultDataDir(); got != "data" {
		t.Errorf("defaultDataDir() = %q, want the last-resort %q", got, "data")
	}
}

func TestExpandPath(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "minh")
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	// The comparison folds separators because expandPath itself only substitutes
	// variables here; whether `\` is a separator at all is filepath's and the
	// syscall layer's business, covered by usableDir and the platform files.
	slash := func(p string) string {
		return filepath.ToSlash(strings.ReplaceAll(p, `\`, "/"))
	}

	for _, tc := range []struct {
		raw, want string
	}{
		{raw: "$HOME/Documents", want: filepath.Join(home, "Documents")},
		{raw: `${HOME}/Dokumente`, want: filepath.Join(home, "Dokumente")},
		{raw: "~/文档", want: filepath.Join(home, "文档")},
		{raw: "%USERPROFILE%\\Documents", want: filepath.Join(home, "Documents")},
		{raw: "%XDG_DATA_HOME%\\Documents", want: filepath.Join(home, ".local", "share", "Documents")},
		// An unknown variable makes the whole result unusable rather than leaving
		// a drive-relative fragment behind, which Windows would call absolute.
		{raw: "%NO_SUCH_VAR%\\Documents", want: ""},
		// A stray % is a character in a folder name, not a variable.
		{raw: "100%\\Documents", want: "100%\\Documents"},
		{raw: "  $HOME/Documents  ", want: filepath.Join(home, "Documents")},
	} {
		if got := expandPath(tc.raw, home); slash(got) != slash(tc.want) {
			t.Errorf("expandPath(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestUsableDir(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "minh")

	for _, tc := range []struct {
		dir    string
		ok     bool
		reason string
	}{
		{dir: filepath.Join(home, "Documents"), ok: true, reason: "the normal case"},
		{dir: "/data/Documents", ok: true, reason: "Documents on another mount"},
		{dir: "/", ok: false, reason: "the filesystem root"},
		{dir: home, ok: false, reason: "the home directory itself"},
		{dir: "Documents", ok: false, reason: "relative to the working directory"},
		{dir: "", ok: false, reason: "nothing at all"},
	} {
		if got := usableDir(tc.dir, home); got != tc.ok {
			t.Errorf("usableDir(%q) = %v, want %v (%s)", tc.dir, got, tc.ok, tc.reason)
		}
	}
}

func TestLegacyDataDirOnlyPointsAtARealDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	if got := legacyDataDir(); got != "" {
		t.Fatalf("legacyDataDir() = %q on a machine with no old data", got)
	}

	old := filepath.Join(home, ".config", "libris")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := legacyDataDir(); got != "" {
		t.Fatalf("legacyDataDir() = %q with no libris.db in it", got)
	}

	if err := os.WriteFile(filepath.Join(old, "libris.db"), []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := legacyDataDir(); got != old {
		t.Errorf("legacyDataDir() = %q, want %q", got, old)
	}
}
