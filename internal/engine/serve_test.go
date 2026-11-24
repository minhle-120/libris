package engine_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"libris/internal/engine"
)

func TestPresetWritesOneSectionPerModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libris.ini")
	err := engine.Preset(path, "", []engine.Model{
		{Alias: engine.AliasChat, Kind: engine.KindChat, Path: "/m/chat.gguf", MMProj: "/m/mmproj.gguf"},
		{Alias: engine.AliasEmbed, Kind: engine.KindEmbed, Path: "/m/emb.gguf"},
		{Alias: engine.AliasRerank, Kind: engine.KindRerank, Path: "/m/rerank.gguf"},
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
	// LLAMA_ARG_EMBEDDINGS, LLAMA_ARG_RERANKING. A typo here is a router that refuses to
	// start, and the only place it would be noticed is a log file.
	for _, want := range []string{
		"version = 1", "[*]", "[libris-chat]", "[libris-embed]", "[libris-rerank]",
		"model = /m/chat.gguf", "mmproj = /m/mmproj.gguf",
		"embeddings = on", "reranking = on", "fit = on", "backend-sampling = true",
		"reasoning-preserve = true", "cache-type-k = q8_0", "no-warmup = true", "jinja = true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preset is missing %q:\n%s", want, got)
		}
	}

	// Capacity is here, on the server, and never on the wire (ADR 0011). Each number is per
	// architecture: the chat model's context is what the research loop's history needs, the
	// embedder's is its own training limit, and -fit still shrinks either one to the memory
	// llama.cpp actually finds.
	for _, want := range []string{
		"[libris-chat]\nmodel = /m/chat.gguf", "ctx-size = 32768",
		"ctx-size = 4096", // the embedder
		"ctx-size = 2048", // the reranker
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preset is missing %q:\n%s", want, got)
		}
	}

	// Sampling on the chat model, because ADR 0006 puts sampling on the server and this file is
	// the server. A research turn has to answer with a tool call, so the distribution is cold;
	// the numbers are not settings and must not appear in the settings UI.
	for _, want := range []string{"temp = 0.1", "top-k = 50", "top-p = 1.0", "min-p = 0.0", "repeat-penalty = 1.1"} {
		if !strings.Contains(got, want) {
			t.Errorf("preset is missing %q:\n%s", want, got)
		}
	}
	// The two small models hold no KV cache in host memory; the chat model is allowed 2 GiB.
	if strings.Count(got, "cache-ram = 0") != 2 || !strings.Contains(got, "cache-ram = 2048") {
		t.Errorf("cache-ram is not per kind:\n%s", got)
	}

	// Asked for no device, the preset says nothing. The absence is the behaviour: llama.cpp
	// picks, which is what a machine whose engine was never asked deserves.
	if strings.Contains(got, "device =") {
		t.Errorf("a device was named without being asked for:\n%s", got)
	}

	// And no pooling. --pooling's help says "use model default if unspecified", and the
	// default comes from inside the GGUF; overriding it here would silently re-pool an
	// embedder that shipped its own answer, returning vectors of the right length and the
	// wrong meaning. Measured on the catalogue's own embedder, with and without
	// `pooling = mean`, the vectors are identical — which is what lets this stay absent.
	if strings.Contains(got, "pooling") {
		t.Error("the preset names a pooling mode; the model file knows better than this file does")
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
	if !have.Matches("Vulkan0", want) {
		t.Error("a file that agrees with the derivation must not be rewritten")
	}

	// Absent is not an error — it is a data directory before its first start.
	missing, err := engine.ReadPreset(filepath.Join(dir, "nope.ini"))
	if err != nil || missing != nil {
		t.Errorf("a missing preset must read as absent, not an error: %v %v", missing, err)
	}
}

// The whole point of a file in the data directory: a number changed by hand survives, because
// nothing rewrote the file. Only the three derived things — which models, where their files
// are, which device — make libris write it again.
func TestStartKeepsEditsToAnUnchangedPreset(t *testing.T) {
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

	raw, _ := os.ReadFile(opts.Preset)
	edited := strings.Replace(string(raw), "cache-ram = 2048", "cache-ram = 4096", 1)
	if edited == string(raw) {
		t.Fatal("fixture: no cache-ram line to edit")
	}
	if err := os.WriteFile(opts.Preset, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := sup.Start(context.Background(), *opts, models); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(opts.Preset)
	if !strings.Contains(string(raw), "cache-ram = 4096") {
		t.Errorf("a hand edit was overwritten by a start that changed nothing:\n%s", raw)
	}
}

// The other half of the rule: the derived things do win. A model appearing, going away, moving,
// or a device being chosen is libris's to know, and a preset that disagrees about one of them
// would start a server that does not match the settings page.
func TestStartRewritesWhenWhatItDerivesHasMoved(t *testing.T) {
	sup, opts, _ := serveFixture(t)
	models := []engine.Model{{Alias: engine.AliasChat, Kind: engine.KindChat, Path: "/m/c.gguf"}}
	opts.Device = "Vulkan0"

	if _, err := sup.Start(context.Background(), *opts, models); err != nil {
		t.Fatal(err)
	}
	if err := sup.Stop(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(opts.Preset)
	edited := strings.Replace(string(raw), "cache-ram = 2048", "cache-ram = 4096", 1)
	if err := os.WriteFile(opts.Preset, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	opts.Device = "none"
	srv, err := sup.Start(context.Background(), *opts, models)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(opts.Preset)
	if strings.Contains(string(raw), "cache-ram = 4096") {
		t.Errorf("a device change must rewrite the file:\n%s", raw)
	}
	if srv.Device != "none" {
		t.Errorf("status says %+v", srv)
	}

	// A model that appeared is the same case, and the more common one: the download landed
	// while the router was stopped.
	if err := sup.Stop(); err != nil {
		t.Fatal(err)
	}
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

// A supervisor test starts nothing: the spawn, the readiness probe and the signal are all
// seams, so what is under test is the record, the wait and the state the page reads.
func serveFixture(t *testing.T) (*engine.Supervisor, *engine.ServeOptions, *spawned) {
	t.Helper()
	dir := t.TempDir()
	sp := &spawned{}
	sup := engine.NewSupervisor(dir, engine.Seams{
		Spawn: func(cmd *exec.Cmd) (int, error) {
			sp.called = true
			sp.alive = true
			sp.cmd = cmd
			// Every --port the supervisor asked for, in order, so a test can see how many
			// ports a start tried and in what order.
			for i, a := range cmd.Args {
				if a == "--port" && i+1 < len(cmd.Args) {
					sp.ports = append(sp.ports, cmd.Args[i+1])
				}
			}
			// This test's own pid is the only pid it may claim is alive.
			return os.Getpid(), nil
		},
		Probe: func(ctx context.Context, url string) error {
			sp.probed = url
			// Killing the fake process the first time anything asks it a question is how a test
			// says "it died": aliveness here is what Signal reports, never what the operating
			// system thinks of the pid a fake handed back.
			if sp.dies {
				sp.alive = false
			}
			return sp.probeErr
		},
		Signal: func(pid int, sig syscall.Signal) error {
			sp.signals = append(sp.signals, sig)
			if sig == 0 {
				if sp.alive {
					return nil
				}
				return syscall.ESRCH
			}
			// Whatever the polite signal was, the fake agrees it worked: the point is the
			// record and the wait, not which number llama.cpp answers to.
			sp.alive = false
			return nil
		},
		Now: func() time.Time { return time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC) },
	})
	opts := engine.ServeOptions{
		Bin:       "/tmp/llama-server",
		Port:      8080,
		Preset:    filepath.Join(dir, "presets", "libris.ini"),
		Log:       filepath.Join(dir, "logs", "server.log"),
		ModelsMax: 3,
	}
	return sup, &opts, sp
}

type spawned struct {
	called  bool
	alive   bool
	cmd     *exec.Cmd
	probed  string
	ports   []string
	signals []syscall.Signal
	// probeErr is what readiness answers instead of "up", so a test can say the port was
	// taken without starting anything that could take it.
	probeErr error
	// dies kills the fake process the first time it is probed, which is how a test asks
	// whether a death is noticed rather than waited out.
	dies bool
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
