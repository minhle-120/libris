package tools

import (
	"os"
	"runtime"
)

// exeExt is how this operating system names a program. Windows appends it to the name in
// PATH lookups on its own; the bundle is a folder libris builds itself, so the suffix has to
// be said out loud here or every bundled program resolves as missing on the platform that
// needs the bundle most.
func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// hasFiles says a folder is worth naming in a child's search path. An empty one is worse than
// none: it is an entry that says "look here" about a place with nothing in it.
func hasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}
