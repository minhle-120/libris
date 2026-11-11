//go:build !windows

package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// documentsDir resolves the user's Documents directory, or "" when there is no
// home directory to derive one from.
//
// XDG's user-dirs.dirs is consulted first because it is the only place that
// knows the localised name: on a German desktop the folder really is
// ~/Dokumente, and hardcoding ~/Documents would quietly create a second one
// beside it.
func documentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if dir := xdgDocumentsDir(home); usableDir(dir, home) {
		return dir
	}
	return filepath.Join(home, "Documents")
}

// xdgDocumentsDir reads XDG_DOCUMENTS_DIR out of user-dirs.dirs, the file
// xdg-user-dirs maintains so that the desktop and every toolkit agree on where
// the user's folders are. Returns "" when the file or the key is absent.
func xdgDocumentsDir(home string) string {
	path := os.Getenv("XDG_CONFIG_HOME")
	if path == "" {
		path = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(path, "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "XDG_DOCUMENTS_DIR" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		return expandPath(value, home)
	}
	return ""
}

// anchored reports whether a path names a location rather than a path relative
// to some current root. On unix IsAbs already means it starts with "/".
func anchored(dir string) bool { return strings.HasPrefix(dir, "/") }
