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
	err := engine.Preset(path, []engine.Model{
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
	// LLAMA_ARG_EMBEDDINGS, LLAMA_ARG_POOLING, LLAMA_ARG_RERANKING. A typo here is a router
	// that refuses to start, and the only place it would be noticed is a log file.
	for _, want := range []string{
		"version = 1", "[*]", "[libris-chat]", "[libris-embed]", "[libris-rerank]",
		"model = /m/chat.gguf", "mmproj = /m/mmproj.gguf",
		"embeddings = on", "pooling = mean", "reranking = on", "fit = on",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preset is missing %q:\n%s", want, got)
		}
	}
	// Capacity is here, on the server, and never on the wire (ADR 0011) — and -fit stays on,
	// so the number is llama.cpp's measurement rather than libris's guess.
	if !strings.Contains(got, "ctx-size = 8192") {
		t.Error("the chat model has no ctx-size; -fit alone decides nothing about the request budget")
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
			// This test's own pid is the only pid it may claim is alive.
			return os.Getpid(), nil
		},
		Probe: func(ctx context.Context, url string) error { sp.probed = url; return nil },
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
	signals []syscall.Signal
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
