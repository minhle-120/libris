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

// A catalogue entry with no draft must hand the preset nothing rather than the folder its
// files live in: a path built from an empty part name is that directory, and a directory named
// as a model file is a router that exits at start.
func TestAnAbsentPartNamesNothing(t *testing.T) {
	dir := t.TempDir()
	e := entry()
	e.Draft = models.Part{}
	e.MMProj = models.Part{}

	touch(t, e.Path(dir, e.Model), int(e.Model.Bytes))
	st := models.StatusOf(dir, e)
	if !st.Complete {
		t.Fatalf("a weights-only entry reads as %+v", st)
	}
	if st.DraftPath != "" || st.MMProjPath != "" {
		t.Errorf("absent parts still got paths: %+v", st)
	}

	got := models.Models([]models.Status{st})
	if len(got) != 1 || got[0].Draft != "" || got[0].MMProj != "" {
		t.Errorf("the preset was handed a part that does not exist: %+v", got)
	}
}

// The chat model libris ships speculates, and nothing but this check says so: the preset is
// given draft keys only when an entry names a draft file, so an entry arriving without one
// answers every question more slowly, silently, with no failing test anywhere to say why.
func TestTheChatEntryCarriesItsDraft(t *testing.T) {
	for _, e := range models.Catalog() {
		if e.Role != models.RoleChat {
			continue
		}
		if e.Draft.Name == "" || e.Draft.Bytes <= 0 || len(e.Draft.SHA256) != 64 {
			t.Errorf("%s: the chat model has no pinned draft (%+v), so the router will run it without speculation", e.ID, e.Draft)
		}
	}
}

// A test entry, because writing gigabytes of sparse files to prove a size comparison would make
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
		Draft:  models.Part{Name: "mtp-test.gguf", Bytes: 1, SHA256: "cccc"},
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
	if st.Complete || len(st.Missing) != 3 {
		t.Fatalf("an empty disk reads as %+v", st)
	}

	// One part, right size: present, but the entry is not runnable.
	touch(t, e.Path(dir, e.Model), int(e.Model.Bytes))
	st = models.StatusOf(dir, e)
	if st.Complete || len(st.Have) != 1 || len(st.Missing) != 2 {
		t.Fatalf("half a model reads as %+v", st)
	}
	if st.Verified {
		t.Error("a file with no record is called verified; libris never hashed it")
	}

	// The weights and their projector are still not the whole entry: a part nobody downloaded is
	// a file the preset would name and the router would fail to open.
	touch(t, e.Path(dir, e.MMProj), int(e.MMProj.Bytes))
	st = models.StatusOf(dir, e)
	if st.Complete || len(st.Missing) != 1 {
		t.Fatalf("two of three parts read as %+v", st)
	}
	if st.Verified {
		t.Error("still unverified, and correctly so")
	}

	touch(t, e.Path(dir, e.Draft), int(e.Draft.Bytes))
	if st = models.StatusOf(dir, e); !st.Complete || len(st.Missing) != 0 {
		t.Fatalf("a complete entry reads as %+v", st)
	}

	if err := models.MarkVerified(e.Path(dir, e.Model), e.Model.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := models.MarkVerified(e.Path(dir, e.MMProj), e.MMProj.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := models.MarkVerified(e.Path(dir, e.Draft), e.Draft.SHA256); err != nil {
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
	if len(st.Missing) != 3 {
		t.Errorf("Missing = %v, want every part", st.Missing)
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
	touch(t, e.Path(dir, e.Draft), int(e.Draft.Bytes))

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
	if m.Path != e.Path(dir, e.Model) || m.MMProj != e.Path(dir, e.MMProj) || m.Draft != e.Path(dir, e.Draft) {
		t.Errorf("paths are wrong: %+v", m)
	}
}

// The migration one build carries: a folder for a model this catalogue no longer has. Nothing
// can delete it from the settings page, because the page lists the catalogue — so a machine that
// installed the old model keeps the bytes forever, and shows them as free space nothing owns.
func TestRemoveRetiredClearsWhatNothingNames(t *testing.T) {
	dir := t.TempDir()

	// Nothing there yet is the normal case, and the normal case says nothing.
	if gone, err := models.RemoveRetired(dir); err != nil || len(gone) != 0 {
		t.Fatalf("RemoveRetired on a fresh directory = %v, %v; want nothing and no error", gone, err)
	}

	old := filepath.Join(dir, "lfm3-vl-2b")
	touch(t, filepath.Join(old, "LFM3-VL-2B-QAD-Q4_0.gguf"), 4)
	if err := os.WriteFile(models.PartPath(filepath.Join(old, "mmproj-BF16.gguf")), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A folder that is in the catalogue stays: this is a migration, not a cleaner, and the
	// entry's own files are what the download button, the preset and "Restart" all point at.
	keep := entry()
	touch(t, keep.Path(dir, keep.Model), int(keep.Model.Bytes))

	gone, err := models.RemoveRetired(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0] != "lfm3-vl-2b" {
		t.Errorf("gone = %v, want the one folder that was actually there", gone)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the retired folder survived: %v", err)
	}
	if _, err := os.Stat(keep.Dir(dir)); err != nil {
		t.Errorf("a catalogue entry's folder was taken as well: %v", err)
	}

	// And the second start has nothing left to say, which is what keeps this out of every log.
	if gone, err := models.RemoveRetired(dir); err != nil || len(gone) != 0 {
		t.Errorf("second RemoveRetired = %v, %v; want nothing", gone, err)
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
