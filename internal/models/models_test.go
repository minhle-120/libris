package models_test

import (
	"os"
	"path/filepath"
	"testing"

	"libris/internal/engine"
	"libris/internal/models"
)

func write(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The catalogue is the only thing that decides what counts as a model. A file nobody
// recognises is left out rather than offered to a router that cannot be told what it is for.
func TestScanRecognisesTheCatalogue(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Qwen3.5-9B-Uncensored-Holo2.5-Q8_0.gguf")
	write(t, dir, "Qwen3.5-9B-Uncensored-Holo2.5-mmproj-F32.gguf")
	write(t, dir, "embeddinggemma-300m-it-Q4_K_M.gguf")
	write(t, dir, "rerank-gemma-2b-it-qat-q4_K_M.gguf")
	write(t, dir, "someone-elses-experiment.gguf")

	got := models.Scan(dir)
	if len(got) != 3 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	byRole := map[models.Role]models.Model{}
	for _, m := range got {
		byRole[m.Role] = m
	}
	if byRole[models.RoleChat].Alias != engine.AliasChat {
		t.Errorf("chat alias = %q", byRole[models.RoleChat].Alias)
	}
	if byRole[models.RoleChat].MMProj == "" {
		t.Error("the chat model's projector was not paired")
	}
	if byRole[models.RoleEmbed].MMProj != "" {
		t.Errorf("embedding model paired with %q", byRole[models.RoleEmbed].MMProj)
	}
}

// Two chat models is a choice libris does not get to make silently, so the first match wins
// and the second is simply not in the list.
func TestScanTakesOneModelPerRole(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Qwen3.5-9B-Uncensored-Holo2.5a-Q8_0.gguf")
	write(t, dir, "Qwen3.5-9B-Uncensored-Holo2.5b-Q8_0.gguf")

	if got := models.Scan(dir); len(got) != 1 {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
}

// A directory that does not exist is the state a first run is in, not an error.
func TestScanMissingDirectory(t *testing.T) {
	if got := models.Scan(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Fatalf("got %+v", got)
	}
}
