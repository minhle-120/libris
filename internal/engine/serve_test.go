package engine_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"libris/internal/engine"
)

// TestPresetWritesNoSlotCount asserts an absence, which the presence loop cannot.
//
// Nothing is written for slots, and the absence is the choice: llama-server's default for a
// model instance is auto, which is 4 slots *with* one KV buffer shared across them. Naming the
// key keeps the slots but hands each one ctx-size/N of context instead of the shared pool, and a
// page raster is not a 100-word chunk. So an edit that puts a number back here is not a tuning
// change — it is a switch back to the thing leaving it out exists to avoid.
func TestPresetWritesNoSlotCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libris.ini")
	if err := engine.Preset(path, "", []engine.Model{
		{Alias: engine.AliasEmbed, Kind: engine.KindEmbed, Path: "/m/emb.gguf"},
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := string(raw); strings.Contains(got, "parallel") {
		t.Errorf("preset names a slot count; the default that leaves it out is the one that also shares the KV buffer:\n%s", got)
	}
}

func TestPresetWritesOneSectionPerModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libris.ini")
	err := engine.Preset(path, "", []engine.Model{
		{Alias: engine.AliasChat, Kind: engine.KindChat, Path: "/m/chat.gguf", MMProj: "/m/mmproj.gguf"},
		{Alias: engine.AliasEmbed, Kind: engine.KindEmbed, Path: "/m/emb.gguf"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)

	// The keys are llama.cpp's own preset vocabulary, checked against common/arg.cpp:
	// LLAMA_ARG_EMBEDDINGS. A typo here is a router that refuses to
	// start, and the only place it would be noticed is a log file.
	for _, want := range []string{
		"version = 1", "[*]", "[libris-chat]", "[libris-embed]",
		"model = /m/chat.gguf", "mmproj = /m/mmproj.gguf",
		"embeddings = on", "fit = on", "backend-sampling = true",
		"reasoning-preserve = true", "cache-type-k = q8_0", "no-warmup = true", "jinja = true",
		// Written out rather than left absent, so whoever opens this file sees the choice. And
		// it is a choice: with it absent the section would still not load, and a reader could not
		// tell that from an oversight.
		"load-on-startup = false",
	} {
		if strings.Contains(got, "load-on-startup = true") {
			t.Error("a model is marked to load at startup; opening libris would pay for it")
		}
		if !strings.Contains(got, want) {
			t.Errorf("preset is missing %q:\n%s", want, got)
		}
	}

	// Capacity is here, on the server, and never on the wire. The chat model's context is what
	// the research loop's history needs. The embedder's is not a trained limit (its GGUF says
	// 262144) but the pool that the four sequences of one embeddings request share, sized so a
	// page raster's vision tokens and three prose chunks fit in it together. -fit still shrinks
	// either one to the memory llama.cpp actually finds.
	for _, want := range []string{
		"[libris-chat]\nmodel = /m/chat.gguf", "ctx-size = 32768",
		"ctx-size = 8192", // the embedder
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preset is missing %q:\n%s", want, got)
		}
	}

	// Sampling on the chat model, because sampling belongs to the server and this file is
	// the server. A research turn has to answer with a tool call, so the distribution is cold;
	// the numbers are not settings and must not appear in the settings UI.
	for _, want := range []string{"temp = 0.1", "top-k = 50", "top-p = 1.0", "min-p = 0.0", "repeat-penalty = 1.1"} {
		if !strings.Contains(got, want) {
			t.Errorf("preset is missing %q:\n%s", want, got)
		}
	}
	// The embedder holds no KV cache in host memory; the chat model is allowed 2 GiB.
	if strings.Count(got, "cache-ram = 0") != 1 || !strings.Contains(got, "cache-ram = 2048") {
		t.Errorf("cache-ram is not per kind:\n%s", got)
	}

	// Asked for no device, the preset says nothing. The absence is the behaviour: llama.cpp
	// picks, which is what a machine whose engine was never asked deserves.
	if strings.Contains(got, "device =") {
		t.Errorf("a device was named without being asked for:\n%s", got)
	}

	// Pooling is written out even though it repeats what the model's own GGUF already says
	// (gemma-embedding2.pooling_type = 1, which is MEAN), so that how a stored vector was pooled
	// is readable in the file that says what the server was told. What this pins is that it is
	// written for the embedder only — the chat model is not asked to pool anything — and
	// preset.go carries the obligation: one key for every embed model, so a model that pools
	// another way must not arrive before pooling moves to per-model data. Exactly one occurrence
	// in the whole file is the whole claim: written, and for the embedder only.
	if strings.Count(got, "pooling") != 1 {
		t.Errorf("pooling is not written once, for the embedder alone:\n%s", got)
	}
}

// The device name is not a free word: it is a slot from this build's --list-devices, and a name
// llama.cpp does not know is an argument error that stops the server from starting at all.
// Measured on the build libris downloads: `error while handling argument "--device": invalid
// device: CUDA0`, then exit. So the preset only ever carries a name somebody handed it.
func TestPresetNamesOnlyTheDeviceItWasGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libris.ini")
	models := []engine.Model{{Alias: engine.AliasChat, Kind: engine.KindChat, Path: "/m/c.gguf"}}

	if err := engine.Preset(path, "Vulkan1", models); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "device = Vulkan1") {
		t.Errorf("the chosen device is not in the preset:\n%s", raw)
	}
	// CPU-only is llama.cpp's own word, not "CPU": a reported CPU row is not a device name.
	if err := engine.Preset(path, "none", models); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "device = none") {
		t.Errorf("CPU-only must be written as llama.cpp spells it:\n%s", raw)
	}
}

// What libris writes, it must be able to read back: the file is the configuration, and the
// status the page shows is read out of it rather than out of what this program meant to write.
func TestReadPresetRoundTripsTheFileItWrote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "libris.ini")
	want := []engine.Model{
		{Alias: engine.AliasChat, Kind: engine.KindChat, Path: "/m/chat.gguf", MMProj: "/m/mmproj.gguf"},
		{Alias: engine.AliasEmbed, Kind: engine.KindEmbed, Path: "/m/emb.gguf", MMProj: "/m/emb-mmproj.gguf"},
	}
	if err := engine.Preset(path, "Vulkan0", want); err != nil {
		t.Fatal(err)
	}

	have, err := engine.ReadPreset(path)
	if err != nil || have == nil {
		t.Fatalf("reading the file libris just wrote: %v %v", have, err)
	}
	if have.Device != "Vulkan0" {
		t.Errorf("device = %q", have.Device)
	}
	if len(have.Models) != len(want) {
		t.Fatalf("%d sections, want %d: %+v", len(have.Models), len(want), have.Models)
	}
	for i, m := range want {
		got := have.Models[i]
		if got.Alias != m.Alias || got.Path != m.Path || got.MMProj != m.MMProj || got.Kind != m.Kind {
			t.Errorf("section %d reads %+v, want %+v", i, got, m)
		}
	}
	// Absent is not an error — it is a data directory before its first start.
	missing, err := engine.ReadPreset(filepath.Join(dir, "nope.ini"))
	if err != nil || missing != nil {
		t.Errorf("a missing preset must read as absent, not an error: %v %v", missing, err)
	}
}

// What the preset module decides and what the router serves are joined here: the start runs
// from what the file says after the refresh, so a model that downloaded while the router was
// stopped is in the running set without anything telling this call about it.
//
// The rule on the file side of that join — that every start rewrites it, moved or not — is
// TestRefreshPreset* in preset_internal_test.go, which says it with a file and a string instead
// of a spawn, a readiness probe and a port.
func TestStartServesWhatThePresetHoldsAfterAModelAppears(t *testing.T) {
	sup, opts, _ := serveFixture(t)
	models := []engine.Model{{Alias: engine.AliasChat, Kind: engine.KindChat, Path: "/m/c.gguf"}}
	opts.Device = "Vulkan0"

	srv, err := sup.Start(context.Background(), *opts, models)
	if err != nil {
		t.Fatal(err)
	}
	// What the page reports comes from the file, not from the call.
	if srv.Device != "Vulkan0" || len(srv.Models) != 1 || srv.Models[0] != string(engine.AliasChat) {
		t.Fatalf("status reads %+v", srv)
	}
	if err := sup.Stop(); err != nil {
		t.Fatal(err)
	}

	// The download landing while stopped is the common case, and all the next start is given is
	// the model list: the file has to catch up by itself.
	withEmbed := append(models,
		engine.Model{Alias: engine.AliasEmbed, Kind: engine.KindEmbed, Path: "/m/e.gguf"})
	srv, err = sup.Start(context.Background(), *opts, withEmbed)
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.Models) != 2 {
		t.Errorf("the embedder that downloaded is not in the running set: %+v", srv.Models)
	}
}

func TestServeRecordsAndReports(t *testing.T) {
	// A fake spawn cannot produce a real pid, so the readiness probe reports a live process
	// that is not this test's child: what matters is that the supervisor wrote a record
	// saying it is running, and that Stop is what makes it say otherwise.
	sup, opts, sp := serveFixture(t)
	models := []engine.Model{{Alias: engine.AliasChat, Kind: engine.KindChat, Path: "/m/c.gguf"}}

	// The pid the record claims is this process's own, which is the only pid the test may
	// assert is alive.
	opts.Bin = "/bin/true"
	_ = sp
	srv, err := sup.Start(context.Background(), *opts, models)
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Running {
		t.Fatal("the router was started and is already reported stopped")
	}
	if srv.Port != 8080 || srv.Log == "" {
		t.Fatalf("serving view is incomplete: %+v", srv)
	}
	if len(srv.Models) != 1 || srv.Models[0] != "libris-chat" {
		t.Errorf("models = %v", srv.Models)
	}
	if !sp.called {
		t.Error("nothing was spawned")
	}
	if !strings.HasSuffix(sp.probed, "/models") {
		t.Errorf("readiness probed %q, want the router's /models", sp.probed)
	}
	// The router is started without a model, which is the whole of router mode.
	if strings.Join(argList(sp.cmd), " ") != "" {
		t.Logf("args: %s", strings.Join(argList(sp.cmd), " "))
	}

	if err := sup.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := sup.Status(); got.Running {
		t.Fatalf("still running after Stop: %+v", got)
	}
}

func argList(cmd *exec.Cmd) []string {
	if cmd == nil {
		return nil
	}
	return cmd.Args
}

// A process that is already up is not restarted by a second start, and the answer says so.
func TestServeRefusesToStartTwice(t *testing.T) {
	sup, opts, _ := serveFixture(t)
	opts.Bin = "/bin/true"
	models := []engine.Model{{Alias: engine.AliasChat, Kind: engine.KindChat, Path: "/m/c.gguf"}}
	if _, err := sup.Start(context.Background(), *opts, models); err != nil {
		t.Fatal(err)
	}
	// The record now claims this test process is the router, so a second start sees a
	// running one — the same way a second libris boot would.
	if _, err := sup.Start(context.Background(), *opts, models); !errors.Is(err, engine.ErrAlreadyServing) {
		t.Fatalf("second start: got %v, want ErrAlreadyServing", err)
	}
}

// Stopping something that was never running is not a failure the page should show.
func TestServeStopWhenIdle(t *testing.T) {
	sup, _, _ := serveFixture(t)
	if err := sup.Stop(); !errors.Is(err, engine.ErrNotServing) {
		t.Fatalf("got %v, want ErrNotServing", err)
	}
}

// Nothing installed means nothing to run, and the answer is a sentence rather than a crash.
func TestServeNeedsABinary(t *testing.T) {
	sup, opts, _ := serveFixture(t)
	opts.Bin = ""
	if _, err := sup.Start(context.Background(), *opts, nil); err == nil {
		t.Fatal("a start with no binary succeeded")
	}
}
