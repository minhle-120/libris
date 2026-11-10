package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
)

// withMigrations points migrate at a fake filesystem for one test, so the
// failure paths can be exercised at all — the real files never fail.
func withMigrations(t *testing.T, files map[string]string) {
	t.Helper()

	fsys := make(fstest.MapFS, len(files))
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}

	before := migrations
	t.Cleanup(func() { migrations = before })
	migrations = fsys
}

func TestMigrateRefusesBadVersion(t *testing.T) {
	withMigrations(t, map[string]string{
		"migrations/abcd_init.sql": "CREATE TABLE x (a);",
	})

	if _, err := Open(filepath.Join(t.TempDir(), "libris.db")); err == nil {
		t.Fatal("a migration file without a numeric version should fail the open")
	}
}

// rawTables reads a database file directly, because a failed migration leaves
// no usable Open to read it with.
func rawTables(t *testing.T, path string) (tables []string, versions int) {
	t.Helper()
	raw, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	rows, err := raw.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()

	if err := raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	return tables, versions
}

func TestMigrateRollsBackAFailingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libris.db")
	before := migrations
	t.Cleanup(func() { migrations = before })

	// 0002 does work and then fails, which is the case that matters: half a
	// migration must not be observable, and its version must not be recorded,
	// or the next start would skip work it never finished.
	migrations = fstest.MapFS{
		"migrations/0001_good.sql": {Data: []byte("CREATE TABLE first (a);")},
		"migrations/0002_bad.sql":  {Data: []byte("CREATE TABLE second (a);\nSELECT * FROM no_such_table;\n")},
	}

	if _, err := Open(path); err == nil {
		t.Fatal("expected the failing migration to abort Open")
	}

	tables, versions := rawTables(t, path)
	if !slices.Contains(tables, "first") {
		t.Errorf("tables = %v, want the migration that committed to have landed", tables)
	}
	if slices.Contains(tables, "second") {
		t.Error("the rolled-back migration's table must not exist")
	}
	if versions != 1 {
		t.Errorf("versions = %d, want only the migration that committed", versions)
	}

	// Restart retries it rather than skipping it.
	if _, err := Open(path); err == nil {
		t.Fatal("a migration that never completed must fail again on restart")
	}
}

func TestOpenFailsOnUnusablePath(t *testing.T) {
	dir := t.TempDir()

	// A file standing where a directory is needed.
	blocker := filepath.Join(dir, "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(blocker, "nested", "libris.db")); err == nil {
		t.Fatal("expected an error when a parent path is a file")
	}
}
