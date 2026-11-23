package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFileStoreMissingFileUsesBase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	store, err := NewFileStore(path, Default())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if store.Get() != Default() {
		t.Fatal("expected defaults")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("store must not create the file until it saves")
	}
}

func TestFileStoreRejectsInvalidBase(t *testing.T) {
	bad := Default()
	bad.PDFDPI = 1
	if _, err := NewFileStore("", bad); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestFileStoreOverlaysFileOnBase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"pdfDPI": 220}`), 0o600); err != nil {
		t.Fatal(err)
	}

	base := Default()
	base.ChatURL = "http://127.0.0.1:7000"

	store, err := NewFileStore(path, base)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	got := store.Get()
	if got.PDFDPI != 220 {
		t.Errorf("file value not applied: %d", got.PDFDPI)
	}
	if got.ChatURL != base.ChatURL {
		t.Errorf("base value lost: %q", got.ChatURL)
	}
}

func TestFileStoreRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path, Default()); err == nil {
		t.Fatal("expected error for corrupt file")
	}
}

func TestFileStoreSavePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	store, err := NewFileStore(path, Default())
	if err != nil {
		t.Fatal(err)
	}

	next := Default()
	next.PDFDPI = 144
	next.RerankModel = "bge-reranker-v2"
	if err := store.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if store.Get().PDFDPI != 144 {
		t.Fatalf("memory not updated: %d", store.Get().PDFDPI)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	// Whatever a person typed ends up in this file, so it is never world readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}

	reopened, err := NewFileStore(path, Default())
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Get().RerankModel != "bge-reranker-v2" {
		t.Errorf("reopened = %+v", reopened.Get())
	}
}

func TestFileStoreSaveRejectsInvalid(t *testing.T) {
	store, _ := NewFileStore("", Default())

	bad := Default()
	bad.ChatURL = ""
	if err := store.Save(bad); err == nil {
		t.Fatal("expected validation error")
	}
	if store.Get() != Default() {
		t.Fatal("invalid config leaked into memory")
	}
}

func TestFileStoreApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, _ := NewFileStore(path, Default())

	got, err := store.Apply([]byte(`{"pdfDPI": 96}`))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.PDFDPI != 96 || store.Get().PDFDPI != 96 {
		t.Fatalf("Apply returned %d, store has %d", got.PDFDPI, store.Get().PDFDPI)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("not persisted: %v", err)
	}
	if !strings.Contains(string(raw), `"pdfDPI": 96`) {
		t.Fatalf("file content = %s", raw)
	}
}

func TestFileStoreApplyRejectsWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, _ := NewFileStore(path, Default())

	if _, err := store.Apply([]byte(`{"pdfDPI": 5000}`)); err == nil {
		t.Fatal("expected validation error")
	}
	if store.Get().PDFDPI != Default().PDFDPI {
		t.Fatal("invalid value leaked into memory")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid value was persisted")
	}
}

func TestFileStoreInMemoryOnly(t *testing.T) {
	store, err := NewFileStore("", Default())
	if err != nil {
		t.Fatal(err)
	}

	next := Default()
	next.PDFDPI = 111
	if err := store.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if store.Get().PDFDPI != 111 {
		t.Fatalf("PDFDPI = %d", store.Get().PDFDPI)
	}
}

// What an actual upgrade looks like: the file on disk was written when the app had a
// context-window and a GPU-layer setting. It loads, the stale keys are reported for
// the log, and the first save rewrites the file without them.
func TestFileStoreSurvivesRetiredSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	raw := []byte(`{"contextSize":16384,"gpuLayers":-1,"pdfDPI":120}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewFileStore(path, Default())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if want := []string{"contextSize", "gpuLayers"}; !reflect.DeepEqual(store.Dropped(), want) {
		t.Fatalf("Dropped() = %v, want %v", store.Dropped(), want)
	}
	if got := store.Get().PDFDPI; got != 120 {
		t.Fatalf("the settings that survived were not applied: pdfDPI = %d", got)
	}

	next := store.Get()
	next.PDFMode = PDFModeFallback
	if err := store.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(store.Dropped()) != 0 {
		t.Fatalf("Dropped() after a save = %v, want none", store.Dropped())
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "contextSize") {
		t.Fatal("a retired key survived the rewrite")
	}
	if !strings.Contains(string(written), `"pdfDPI": 120`) {
		t.Fatalf("the value the user set was lost: %s", written)
	}
}
