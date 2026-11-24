package engine

// The drift rule, tested where it now lives: read the file, compare the three things libris
// derives, write only when one of them moved, and answer with what the file says.
//
// Every one of these is a file and a string — no spawn, no readiness probe, no port — which is
// what the rule was worth taking out of Start for. It was testable before, but only by starting
// a process and reading the file afterwards, so the test described the supervisor rather than
// the rule, and a change to how a process is spawned could have broken it for no reason.
//
// These are in package engine because refreshPreset is not part of what the package offers:
// the supervisor is its only caller, and making the rule public would put a second way to
// rewrite a user's configuration on the interface for no caller.

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

func TestRefreshPresetLeavesAFileThatAlreadyAgrees(t *testing.T) {
	path := writePreset(t, "Vulkan0", oneChat())
	// The whole point of a file in the data directory: a number changed by hand survives,
	// because nothing rewrote the file.
	edited := strings.Replace(string(readFile(t, path)), "cache-ram = 2048", "cache-ram = 4096", 1)
	if edited == string(readFile(t, path)) {
		t.Fatal("fixture: no cache-ram line to edit")
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := refreshPreset(path, "Vulkan0", oneChat())
	if err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, path)) != edited {
		t.Errorf("a start that changed nothing rewrote the file:\n%s", readFile(t, path))
	}
	// And the answer carries the edit, not the derivation: the file is what the router reads.
	if got.Device != "Vulkan0" || len(got.Models) != 1 || got.Models[0].Path != "/m/c.gguf" {
		t.Errorf("answered with %+v", got)
	}
}

func TestRefreshPresetRewritesWhenADerivedThingMoves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		device string
		models []Model
	}{
		{"a device was chosen", "none", oneChat()},
		{"a model downloaded", "Vulkan0", append(oneChat(),
			Model{Alias: AliasEmbed, Kind: KindEmbed, Path: "/m/e.gguf"})},
		{"a model went away", "Vulkan0", []Model{{Alias: AliasEmbed, Kind: KindEmbed, Path: "/m/e.gguf"}}},
		{"a model moved", "Vulkan0", []Model{{Alias: AliasChat, Kind: KindChat, Path: "/elsewhere/c.gguf"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writePreset(t, "Vulkan0", oneChat())
			edited := strings.Replace(string(readFile(t, path)), "cache-ram = 2048", "cache-ram = 4096", 1)
			if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
				t.Fatal(err)
			}

			got, err := refreshPreset(path, tc.device, tc.models)
			if err != nil {
				t.Fatal(err)
			}
			after := string(readFile(t, path))
			if strings.Contains(after, "cache-ram = 4096") {
				t.Errorf("%s: the file was not rewritten:\n%s", tc.name, after)
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

// Unreadable is not absent. A file the program cannot read may be a preset somebody edited into
// something this build does not understand — and rewriting it would delete an edit nobody has
// been told about, which is the one thing this module's reason for existing forbids.
func TestRefreshPresetRefusesToOverwriteWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets", "libris.ini")
	if err := os.MkdirAll(path, 0o755); err != nil { // a directory where the file belongs
		t.Fatal(err)
	}

	if _, err := refreshPreset(path, "Vulkan0", oneChat()); err == nil {
		t.Fatal("a preset that cannot be read was accepted as one that is missing")
	} else if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want the read failure rather than an absence", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the unreadable thing was removed on the way out: %v", err)
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
