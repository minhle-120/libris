package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}

	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skip("cannot change permissions")
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	if _, err := NewFileStore(path, Default()); err == nil {
		t.Fatal("expected read error")
	}
}

func TestFileStoreWriteErrors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("mkdir", func(t *testing.T) {
		// The parent path component is a regular file.
		store := &FileStore{path: filepath.Join(blocker, "settings.json"), current: Default()}
		next := Default()
		next.PDFDPI = 100
		if err := store.Save(next); err == nil {
			t.Fatal("expected mkdir error")
		}
		if store.Get() != Default() {
			t.Fatal("memory updated despite failed write")
		}
	})

	t.Run("create", func(t *testing.T) {
		path := filepath.Join(dir, "settings.json")
		// A directory squatting on the temporary name makes the write fail.
		if err := os.MkdirAll(path+".tmp", 0o755); err != nil {
			t.Fatal(err)
		}
		store := &FileStore{path: path, current: Default()}
		if err := store.Save(Default()); err == nil {
			t.Fatal("expected write error")
		}
	})

	t.Run("rename", func(t *testing.T) {
		path := filepath.Join(dir, "rename", "settings.json")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err) // can't create the file: rename onto a directory fails
		}
		store := &FileStore{path: path, current: Default()}
		err := store.Save(Default())
		if err == nil {
			t.Fatal("expected rename error")
		}
		if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
			t.Fatal("temporary file left behind")
		}
	})
}

func TestFileStoreApplyWriteError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &FileStore{path: filepath.Join(blocker, "settings.json"), current: Default()}
	if _, err := store.Apply([]byte(`{"pdfDPI": 200}`)); err == nil {
		t.Fatal("expected write error")
	}
	if store.Get() != Default() {
		t.Fatal("memory updated despite failed write")
	}
}
