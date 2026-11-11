package main

import (
	"os"
	"path/filepath"
	"strings"
)

// defaultDataDir is where libris keeps its database, kept originals and
// derived assets when -data is not given: a "libris" folder inside the user's
// Documents directory, so the corpus is somewhere a person can see, back up and
// delete without knowing where the operating system hides application state.
//
// It is a default and not a rule. -data wins outright, and nothing here moves
// data between locations — legacyDataDir is the hint the binary logs instead.
func defaultDataDir() string {
	if docs := documentsDir(); docs != "" {
		return filepath.Join(docs, "libris")
	}
	// No usable home directory: a service account, a container with no $HOME.
	// The config directory is the next place a user would look.
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "libris")
	}
	return "data"
}

// legacyDataDir returns the directory builds before the Documents default used,
// if it actually holds a database. A returning user would otherwise find an
// empty library and conclude the sources were deleted; the originals are all
// still there, so the right response is to say where they are.
func legacyDataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	old := filepath.Join(dir, "libris")
	if old == "" {
		return ""
	}
	if _, err := os.Stat(filepath.Join(old, "libris.db")); err != nil {
		return ""
	}
	return old
}

// expandPath resolves the forms a Documents path arrives in from the registry
// and from XDG's user-dirs.dirs: `%USERPROFILE%\Documents`, `$HOME/Documents`,
// `~/Documents`, or a plain absolute path on either platform. An unknown
// variable collapses to empty rather than being left literal, so the caller's
// absoluteness check rejects the result instead of creating a directory whose
// name contains a dollar sign.
func expandPath(raw, home string) string {
	raw = strings.TrimSpace(raw)
	expanded, ok := expandPercent(raw, home)
	if !ok {
		return ""
	}
	raw = os.Expand(expanded, func(name string) string {
		if name == "HOME" {
			return home
		}
		return os.Getenv(name)
	})
	if raw == "~" || strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`) {
		raw = home + raw[1:]
	}
	if raw == "" {
		return ""
	}
	return filepath.Clean(raw)
}

// expandPercent substitutes Windows-style %NAME% references. USERPROFILE is
// passed in rather than looked up, because the caller already resolved the home
// directory and the registry value is read before anything else here runs. A
// reference that expands to nothing makes the whole path unusable: the result
// would otherwise be a drive-relative fragment like `\Documents`, which Windows
// reports as an absolute path, and the corpus would be written to whichever
// drive the process happened to start on.
func expandPercent(raw, home string) (string, bool) {
	for {
		at := strings.IndexByte(raw, '%')
		if at < 0 {
			return raw, true
		}
		end := strings.IndexByte(raw[at+1:], '%')
		if end < 0 {
			return raw, true // a stray % in a folder name is not a variable to expand
		}
		name, value := raw[at+1:at+1+end], ""
		if name == "USERPROFILE" {
			value = home
		} else {
			value = os.Getenv(name)
		}
		if value == "" {
			return "", false
		}
		raw = raw[:at] + value + raw[at+1+end+1:]
	}
}

// usableDir rejects a resolved Documents directory that would put the corpus
// somewhere absurd. Broken desktop configurations really do write XDG_DOCUMENTS
// _DIR=/ or point it at the home directory itself, and a libris folder at the
// root of the filesystem is not a thing to discover later.
func usableDir(dir, home string) bool {
	if dir == "" || !filepath.IsAbs(dir) || !anchored(dir) {
		return false
	}
	if filepath.VolumeName(dir) == dir || dir == string(filepath.Separator) {
		return false
	}
	if home != "" && dir == filepath.Clean(home) {
		return false
	}
	return true
}
