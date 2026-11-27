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
	"strconv"
	"strings"
	"testing"
)

func oneChat() []Model {
	return []Model{{Alias: AliasChat, Kind: KindChat, Path: "/m/c.gguf"}}
}

func TestRefreshPresetWritesWhatIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets", "libris.ini")

	got, err := refreshPreset(path, "Vulkan0", "", oneChat())
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
			edited := strings.Replace(string(readFile(t, path)), "cache-ram = "+strconv.Itoa(chatCacheRAMMiB), "cache-ram = 4096", 1)
			if edited == string(readFile(t, path)) {
				t.Fatal("fixture: no cache-ram line to edit")
			}
			if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
				t.Fatal(err)
			}

			got, err := refreshPreset(path, tc.device, "", tc.models)
			if err != nil {
				t.Fatal(err)
			}
			after := string(readFile(t, path))
			if strings.Contains(after, "cache-ram = 4096") {
				t.Errorf("%s: the hand edit outlived the start, so nothing was written:\n%s", tc.name, after)
			}
			if !strings.Contains(after, "cache-ram = "+strconv.Itoa(chatCacheRAMMiB)) {
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

// TestRefreshPresetRewritesNumbersTheOldFileStates is the tripwire for the bug that made the
// skip rule a release blocker rather than a nicety: a machine that had started once under an old
// preset kept serving it, because nothing in the rule for noticing looked at what libris *writes*
// — and every key in the file comes from what libris writes.
//
// The fixture is such a file, in the format an installed machine was left holding: a slot count
// stated in the global block, and the embedder's smaller pool. The derivation beside it produces
// neither. Neither the model list nor the device moved, which is precisely the state the old rule
// called up to date. Any future rule for skipping has to fail this test, or it is that bug again
// under a different name.
func TestRefreshPresetRewritesNumbersTheOldFileStates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "presets", "libris.ini")
	const old = "[*]\ndevice = Vulkan0\nparallel = 1\n\n" +
		"[libris-embed]\nmodel = /m/e.gguf\nembeddings = on\nctx-size = 4096\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := refreshPreset(path, "Vulkan0", "", []Model{{Alias: AliasEmbed, Kind: KindEmbed, Path: "/m/e.gguf"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Device != "Vulkan0" {
		t.Errorf("answered device %q, want what the file now says", got.Device)
	}

	after := string(readFile(t, path))
	if strings.Contains(after, "parallel") {
		t.Errorf("a key the code stopped writing survived:\n%s", after)
	}
	if want := "ctx-size = " + strconv.Itoa(embedCtx); !strings.Contains(after, want) {
		t.Errorf("the embedder's pool is not what the code derives (%s):\n%s", want, after)
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

	if _, err := refreshPreset(path, "Vulkan0", "", oneChat()); err == nil {
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
	if err := Preset(path, device, "", models); err != nil {
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

// The draft is a file that belongs to one model, so the keys naming it belong to one section.
// An MTP layer is trained against the network it drafts for, so a section that has none must
// not be told to guess — and a section that has one must name it, because the default
// speculative type is lookup, which is not what the file is.
//
// These five keys are checked against common/arg.cpp rather than remembered: three tag the
// server as their example, and the two cache types inherit it from llama.cpp's common set.
// That check matters because a key the server does not know is not ignored — the process exits,
// and the only place it says so is a log file nobody opens.
func TestPresetWritesTheDraftOnlyWhereTheDraftLives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets", "libris.ini")
	if _, err := refreshPreset(path, "", "", []Model{
		{Alias: AliasChat, Kind: KindChat, Path: "/m/c.gguf", Draft: "/m/mtp.gguf"},
		{Alias: AliasEmbed, Kind: KindEmbed, Path: "/m/e.gguf"},
	}); err != nil {
		t.Fatal(err)
	}

	text := string(readFile(t, path))
	chat, embed := section(t, text, "libris-chat"), section(t, text, "libris-embed")
	for _, want := range []string{
		"spec-type = draft-mtp",
		"spec-draft-model = /m/mtp.gguf",
		"spec-draft-n-max = " + specDraftNMax,
		"spec-draft-type-k = " + specDraftType,
		"spec-draft-type-v = " + specDraftType,
	} {
		if !strings.Contains(chat, want) {
			t.Errorf("the drafting model's section is missing %q:\n%s", want, chat)
		}
	}
	if strings.Contains(embed, "spec-") {
		t.Errorf("a model with no draft was told to speculate:\n%s", embed)
	}

	// Read back, because the settings page reports what the file says rather than what the code
	// hoped to write, and a key read back as nothing is a file the page lies about.
	got, err := ReadPreset(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 2 || got.Models[0].Draft != "/m/mtp.gguf" {
		t.Errorf("ReadPreset lost the draft: %+v", got.Models)
	}
	if got.Models[1].Draft != "" {
		t.Errorf("the embedder gained a draft it was never given: %+v", got.Models[1])
	}
}

// The device is per section because an instance's offload set is fixed when it is allocated:
// the same file is two instances when it is to be used two ways, one on the card for a thousand
// chunks and one on the host for one question. Only the second says anything about the device,
// and it says "none" — which is llama.cpp's own word (common/arg.cpp, LLAMA_ARG_DEVICE: "none =
// don't offload"), because writing nothing means the backend chooses, and what it chooses is the
// card. The host copy carries no projector either: a question embeds text.
func TestPresetPinsOneSectionToTheHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "presets", "libris.ini")
	specs := []Model{
		{Alias: AliasChat, Kind: KindChat, Path: "/m/c.gguf", MMProj: "/m/c-mmproj.gguf"},
		{Alias: AliasEmbed, Kind: KindEmbed, Path: "/m/e.gguf", MMProj: "/m/e-mmproj.gguf"},
		{Alias: AliasEmbedCPU, Kind: KindEmbed, Path: "/m/e.gguf", Device: "none"},
	}
	if _, err := refreshPreset(path, "Vulkan0", "", specs); err != nil {
		t.Fatal(err)
	}

	text := string(readFile(t, path))
	device, host := section(t, text, "libris-embed"), section(t, text, "libris-embed-cpu")
	if strings.Contains(device, "device =") {
		t.Errorf("the model on the chosen device should inherit the global, not repeat it:\n%s", device)
	}
	if !strings.Contains(host, "device = none") {
		t.Errorf("the host instance does not say it stays off the card:\n%s", host)
	}
	if !strings.Contains(host, "embeddings = on") || !strings.Contains(host, "pooling = mean") {
		t.Errorf("the host instance is not an embedder:\n%s", host)
	}
	if strings.Contains(host, "mmproj") {
		t.Errorf("a section that only ever embeds text was given a vision projector:\n%s", host)
	}
	if strings.Count(text, "device = ") != 2 {
		t.Errorf("expected the global device and one section pinned to the host, got:\n%s", text)
	}

	// Read back, because the page reports the file: a section whose device is read back empty is
	// a machine telling the user the host copy is somewhere it is not.
	got, err := ReadPreset(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Device != "Vulkan0" {
		t.Errorf("the file's global device reads back %q", got.Device)
	}
	if len(got.Models) != 3 || got.Models[2].Device != "none" || got.Models[2].Kind != KindEmbed {
		t.Errorf("ReadPreset lost the host instance: %+v", got.Models)
	}
	if got.Models[1].Device != "" {
		t.Errorf("the instance on the chosen device gained a device of its own: %+v", got.Models[1])
	}
}

// section cuts one model's block out of the preset text, so a claim about one model cannot be
// satisfied by another model's line.
func section(t *testing.T, text, name string) string {
	t.Helper()
	marker := "[" + name + "]"
	i := strings.Index(text, marker)
	if i < 0 {
		t.Fatalf("no %s section in:\n%s", marker, text)
	}
	if j := strings.Index(text[i+len(marker):], "\n["); j >= 0 {
		return text[i : i+len(marker)+j]
	}
	return text[i:]
}
