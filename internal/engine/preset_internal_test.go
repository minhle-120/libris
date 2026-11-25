package engine

// The write rule, tested where it lives: write the file from what libris derives, read it back,
// and answer with what the file now says — at every start, whether or not anything moved.
//
// Every one of these is a file and a string — no spawn, no readiness probe, no port — which is
// what the rule was worth taking out of Start for. It was testable before, but only by starting a
// process and reading the file afterwards, so the test described the supervisor rather than the
// rule, and a change to how a process is spawned could have broken it for no reason.
//
// These are in package engine because refreshPreset is not part of what the package offers: the
// supervisor is its only caller, and making the rule public would put a second way to rewrite a
// user's configuration on the interface for no caller.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func oneChat() []Model {
	return []Model{{Alias: AliasChat, Kind: KindChat, Path: "/m/c.gguf"}}
}

func TestRefreshPresetWritesWhatIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets", "libris.ini")

	got, err := refreshPreset(path, "Vulkan0", oneChat())
	if err != nil {
		t.Fatal(err)
	}
	// A data directory before its first start is not a broken one, and the answer the caller
	// gets is the file's, not a nil it has to interpret.
	if got == nil || got.Device != "Vulkan0" || len(got.Models) != 1 {
		t.Fatalf("answered with %+v", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	if !strings.Contains(string(raw), "device = Vulkan0") || !strings.Contains(string(raw), "["+string(AliasChat)+"]") {
		t.Errorf("the file does not say what was derived:\n%s", raw)
	}
}

// TestRefreshPresetWritesWhetherOrNotAnythingMoved is the rule in one table: there is no start
// that leaves the file as it found it.
//
// The first row is the one that matters, and it is the row the code this replaced existed to
// make possible — same device, same models, a hand-edited cache-ram, and the file untouched.
// Losing that edit is the deliberate trade, argued on refreshPreset: the comparison that granted
// it looked only at what libris *reports*, so a change to what libris *writes* — a context size,
// the default number of slots — never reached a machine that had started once already. A test
// that asserts the rewrite is therefore a test that a preset change actually ships.
func TestRefreshPresetWritesWhetherOrNotAnythingMoved(t *testing.T) {
	for _, tc := range []struct {
		name   string
		device string
		models []Model
	}{
		{"nothing moved at all", "Vulkan0", oneChat()},
		{"a device was chosen", "none", oneChat()},
		{"a model downloaded", "Vulkan0", append(oneChat(),
			Model{Alias: AliasEmbed, Kind: KindEmbed, Path: "/m/e.gguf"})},
		{"a model went away", "Vulkan0", []Model{{Alias: AliasEmbed, Kind: KindEmbed, Path: "/m/e.gguf"}}},
		{"a model moved", "Vulkan0", []Model{{Alias: AliasChat, Kind: KindChat, Path: "/elsewhere/c.gguf"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writePreset(t, "Vulkan0", oneChat())
			edited := strings.Replace(string(readFile(t, path)), "cache-ram = 2048", "cache-ram = 4096", 1)
			if edited == string(readFile(t, path)) {
				t.Fatal("fixture: no cache-ram line to edit")
			}
			if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
				t.Fatal(err)
			}

			got, err := refreshPreset(path, tc.device, tc.models)
			if err != nil {
				t.Fatal(err)
			}
			after := string(readFile(t, path))
			if strings.Contains(after, "cache-ram = 4096") {
				t.Errorf("%s: the hand edit outlived the start, so nothing was written:\n%s", tc.name, after)
			}
			if !strings.Contains(after, "cache-ram = 2048") {
				t.Errorf("%s: the new file does not say what libris derives:\n%s", tc.name, after)
			}

			// The answer is read back from the new file, so a caller reporting it cannot
			// report a derivation the router never got.
			if got.Device != tc.device {
				t.Errorf("%s: answered device %q, want %q", tc.name, got.Device, tc.device)
			}
			want := 0
			for _, m := range tc.models {
				if m.Path != "" {
					want++
				}
			}
			if len(got.Models) != want {
				t.Errorf("%s: answered %d models, want %d: %+v", tc.name, len(got.Models), want, got.Models)
			}
		})
	}
}

// A start that cannot write its preset says so, rather than spawning a router that would read
// whatever the file happened to hold. The fixture is a directory where the file belongs: a
// temporary file cannot be renamed over one, and the directory has to still be there afterwards —
// deleting it on the way out would turn a failed start into a missing file.
func TestRefreshPresetFailsWhenThePresetCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets", "libris.ini")
	if err := os.MkdirAll(path, 0o755); err != nil { // a directory where the file belongs
		t.Fatal(err)
	}

	if _, err := refreshPreset(path, "Vulkan0", oneChat()); err == nil {
		t.Fatal("a preset that cannot be written was accepted")
	} else if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want the write failure rather than an absence", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the thing that could not be replaced was removed on the way out: %v", err)
	}
}

// ---- fixture ----------------------------------------------------------------

func writePreset(t *testing.T, device string, models []Model) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "presets", "libris.ini")
	if err := Preset(path, device, models); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
