package models_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"libris/internal/engine"
	"libris/internal/models"
)

// The catalogue is the product. These assertions are about the pin, because a pin is the only
// thing here that a person cannot fix from the settings page.
func TestCatalogIsPinned(t *testing.T) {
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)

	c := models.Catalog()
	if len(c) == 0 {
		t.Fatal("an empty catalogue offers nothing to download")
	}

	seen := map[string]bool{}
	roles := map[models.Role]bool{}
	for _, e := range c {
		if seen[e.ID] {
			t.Errorf("duplicate id %q — an id is what the API asks for", e.ID)
		}
		seen[e.ID] = true
		roles[e.Role] = true

		if !commit.MatchString(e.Rev) {
			t.Errorf("%s: revision %q is not a commit sha; a moving branch would change the model under a saved corpus", e.ID, e.Rev)
		}
		for _, p := range e.Parts() {
			if !sha.MatchString(p.SHA256) {
				t.Errorf("%s/%s: digest %q is not a sha256", e.ID, p.Name, p.SHA256)
			}
			if p.Bytes <= 0 {
				t.Errorf("%s/%s: %d bytes — the page shows this number", e.ID, p.Name, p.Bytes)
			}
			if want := "https://huggingface.co/" + e.Repo + "/resolve/" + e.Rev + "/" + p.Name; e.URL(p) != want {
				t.Errorf("URL = %q, want %q", e.URL(p), want)
			}
		}
		if e.Label == "" || e.Quant == "" || e.Note == "" {
			t.Errorf("%s: the page has nothing to say about it", e.ID)
		}
		// Every folder is one entry's own, so two entries can never want the same filename —
		// which mmproj-BF16.gguf would otherwise do.
		if dir := e.Dir("/data/models"); dir != filepath.Join("/data/models", e.ID) {
			t.Errorf("%s: folder %q is not its own under the models directory", e.ID, dir)
		}
	}

	for _, role := range []models.Role{models.RoleChat, models.RoleEmbed} {
		if !roles[role] {
			t.Errorf("no entry fills %s, so the answer path has nothing to run", role)
		}
	}
}

// A test entry, because writing 1.4 GB of sparse files to prove a size comparison would make
// the test about the filesystem.
func entry() models.Entry {
	return models.Entry{
		ID:     "test-model",
		Role:   models.RoleChat,
		Label:  "Test",
		Quant:  "Q8_0",
		Repo:   "someone/test-gguf",
		Rev:    "0000000000000000000000000000000000000000",
		Model:  models.Part{Name: "test-q8_0.gguf", Bytes: 4, SHA256: "aaaa"},
		MMProj: models.Part{Name: "mmproj-BF16.gguf", Bytes: 2, SHA256: "bbbb"},
	}
}

func touch(t *testing.T, path string, bytes int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(int64(bytes)); err != nil {
		t.Fatal(err)
	}
}

func TestStatusOfCountsParts(t *testing.T) {
	dir := t.TempDir()
	e := entry()

	st := models.StatusOf(dir, e)
	if st.Complete || len(st.Missing) != 2 {
		t.Fatalf("an empty disk reads as %+v", st)
	}

	// One part, right size: present, but the entry is not runnable.
	touch(t, e.Path(dir, e.Model), int(e.Model.Bytes))
	st = models.StatusOf(dir, e)
	if st.Complete || len(st.Have) != 1 || len(st.Missing) != 1 {
		t.Fatalf("half a model reads as %+v", st)
	}
	if st.Verified {
		t.Error("a file with no record is called verified; libris never hashed it")
	}

	touch(t, e.Path(dir, e.MMProj), int(e.MMProj.Bytes))
	st = models.StatusOf(dir, e)
	if !st.Complete || len(st.Missing) != 0 {
		t.Fatalf("a complete entry reads as %+v", st)
	}
	if st.Verified {
		t.Error("still unverified, and correctly so")
	}

	if err := models.MarkVerified(e.Path(dir, e.Model), e.Model.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := models.MarkVerified(e.Path(dir, e.MMProj), e.MMProj.SHA256); err != nil {
		t.Fatal(err)
	}
	if st = models.StatusOf(dir, e); !st.Verified {
		t.Error("files libris hashed and recorded still read as unverified")
	}
}

// A size that disagrees is the interesting case: a truncated download is the common failure,
// and calling it present would tell the router to load a file that cannot be loaded.
func TestStatusOfCountsAPartAndADifferentSize(t *testing.T) {
	dir := t.TempDir()
	e := entry()

	touch(t, e.Path(dir, e.Model), int(e.Model.Bytes)-1)
	if err := os.WriteFile(models.PartPath(e.Path(dir, e.MMProj)), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	st := models.StatusOf(dir, e)
	if st.Complete {
		t.Fatal("a short file and a part file read as a model")
	}
	if st.PartialBytes != 1 {
		t.Errorf("PartialBytes = %d, want the 1 byte the part file holds", st.PartialBytes)
	}
	if len(st.Missing) != 2 {
		t.Errorf("Missing = %v, want both parts", st.Missing)
	}
}

func TestRolesAndModelsFollowCompleteness(t *testing.T) {
	dir := t.TempDir()
	e := entry()

	empty := []models.Status{models.StatusOf(dir, e)}
	if empty[0].Complete {
		t.Error("an empty disk holds nothing complete")
	}
	if got := models.Models(empty); got != nil {
		t.Errorf("Models on an empty disk = %+v, want nothing to put in a preset", got)
	}

	touch(t, e.Path(dir, e.Model), int(e.Model.Bytes))
	touch(t, e.Path(dir, e.MMProj), int(e.MMProj.Bytes))

	have := []models.Status{models.StatusOf(dir, e)}
	if !have[0].Complete {
		t.Error("a downloaded and verified entry should report itself complete")
	}

	got := models.Models(have)
	if len(got) != 1 {
		t.Fatalf("Scan = %+v, want one model", got)
	}
	m := got[0]
	if m.Alias != engine.AliasChat || m.Kind != engine.KindChat {
		t.Errorf(" %+v: a chat entry must arrive as the chat alias and kind", m)
	}
	if m.Path != e.Path(dir, e.Model) || m.MMProj != e.Path(dir, e.MMProj) {
		t.Errorf("paths are wrong: %+v", m)
	}
}

func TestDeleteRemovesTheFolder(t *testing.T) {
	dir := t.TempDir()
	e := entry()
	touch(t, e.Path(dir, e.Model), int(e.Model.Bytes))
	if err := os.WriteFile(models.PartPath(e.Path(dir, e.MMProj)), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := models.Delete(dir, e); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(e.Dir(dir)); !os.IsNotExist(err) {
		t.Errorf("the folder survived: %v", err)
	}
	if err := models.Delete(dir, e); err != nil {
		t.Errorf("deleting what is already gone: %v", err)
	}
}

func TestLook(t *testing.T) {
	for _, e := range models.Catalog() {
		if got, ok := models.Look(e.ID); !ok || got.ID != e.ID {
			t.Fatalf("Look(%q) = %+v, %v", e.ID, got, ok)
		}
	}
	if _, ok := models.Look("not-in-the-catalogue"); ok {
		t.Error("an id nobody owns resolved")
	}
}
