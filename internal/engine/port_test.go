package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"libris/internal/engine"
)

// takenLog is llama.cpp's own line for a port it could not bind, quoted from a real run
// against a port held by another process (the port number in it is the one the test pins).
//
// Quoted, not invented, because a failed start is called a collision by matching these words
// and nothing else — and an earlier fixture here said "Address already in use", which is what
// Go's net package prints and what llama.cpp never does. Every test built on it passed while
// the real thing was misclassified as a mystery failure.
const takenLog = "0.00.212.652 E srv         start: couldn't bind HTTP server socket, " +
	"hostname: 127.0.0.1, port: 9999\n"

func writeTakenLog(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(takenLog), 0o644); err != nil {
		t.Fatal(err)
	}
}

func autoModels() []engine.Model {
	return []engine.Model{{Alias: engine.AliasChat, Kind: engine.KindChat, Path: "/m/c.gguf"}}
}

// Port 0 is Auto. Everything downstream — the argument, the record, the URL waited on, the
// state the page reads — has to agree on whichever number the kernel handed back, because
// that number is in no settings file and nowhere else.
func TestStartOnAutoTakesAPortAndTellsEveryoneWhich(t *testing.T) {
	sup, opts, sp := serveFixture(t)
	opts.Port = 0

	srv, err := sup.Start(context.Background(), *opts, autoModels())
	if err != nil {
		t.Fatalf("Start on Auto: %v", err)
	}
	if srv.Port <= 0 {
		t.Fatalf("a start on Auto reported port %d", srv.Port)
	}
	if len(sp.ports) != 1 {
		t.Fatalf("spawn called with %v, want one port", sp.ports)
	}
	if sp.ports[0] != strconv.Itoa(srv.Port) {
		t.Errorf("llama-server was started with --port %s while the record says %d", sp.ports[0], srv.Port)
	}
	if !strings.Contains(sp.probed, ":"+strconv.Itoa(srv.Port)) {
		t.Errorf("readiness probed %s, not the port the router was given", sp.probed)
	}
	if st := sup.Status(); !st.Running || st.Port != srv.Port {
		t.Errorf("the record says running=%v port=%d, want the port that was bound", st.Running, st.Port)
	}
}

// The window this design accepts: libris binds a port to learn it is free, lets go, and
// llama-server binds it a moment later — and something else can take it in between. What is
// tested here is the answer to that, which is another try, and not a claim that it cannot
// happen.
func TestStartOnAutoTriesAgainWhenThePortIsTaken(t *testing.T) {
	sup, opts, sp := serveFixture(t)
	opts.Port = 0
	// The generic refusal rather than a port-shaped error, because that is all the probe can
	// ever report: the socket does not know why nothing is answering. What has to classify the
	// collision is the log — and the process has to be the one that wrote the line, which is
	// what logOnSpawn is for.
	sp.logOnSpawn = takenLog
	sp.probeErr = errors.New("dial tcp 127.0.0.1: connect: connection refused")

	// Bounded so a regression fails here in seconds instead of spending the readiness budget
	// four times over before saying so.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	srv, err := sup.Start(ctx, *opts, autoModels())
	if took := time.Since(started); took > 8*time.Second {
		t.Errorf("a collision took %s to report; the log says it the moment the router prints it", took)
	}
	if !errors.Is(err, engine.ErrPortTaken) {
		t.Fatalf("Start = %v, want ErrPortTaken after every attempt collided", err)
	}
	if len(sp.ports) < 2 {
		t.Errorf("a collision on Auto gave %d attempts, want a retry", len(sp.ports))
	}
	// Not "port 41234 is taken": by the time anyone reads this the socket is almost certainly
	// free again, and the useful sentence is that nothing could be found at all.
	if !strings.Contains(srv.Error, "No free port") {
		t.Errorf("the page was told %q", srv.Error)
	}
}

// A named port is a request for that port. The reason a person names one is usually that
// something else was already told the address, so a second guess would break the thing the
// pin was for — one try, and the port in the sentence.
func TestAPinnedPortIsTriedOnceAndNamed(t *testing.T) {
	sup, opts, sp := serveFixture(t)
	opts.Port = 9999
	sp.logOnSpawn = takenLog
	sp.probeErr = errors.New("dial tcp 127.0.0.1: connect: connection refused")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	srv, err := sup.Start(ctx, *opts, autoModels())
	if took := time.Since(started); took > 8*time.Second {
		t.Errorf("a pinned collision took %s to report, want the sentence as soon as the log says it", took)
	}
	if !errors.Is(err, engine.ErrPortTaken) {
		t.Fatalf("Start = %v, want ErrPortTaken", err)
	}
	if len(sp.ports) != 1 || sp.ports[0] != "9999" {
		t.Errorf("a pinned port produced %v, want exactly one attempt on 9999", sp.ports)
	}
	if !strings.Contains(srv.Error, "9999") {
		t.Errorf("the page was told %q, which does not name the port that failed", srv.Error)
	}
}

// Anything that is not a collision is not worth another port: a preset key this build refuses
// would fail identically on all four, and the second attempt would only be a slower way of
// saying the same sentence.
func TestAStartThatIsNotACollisionDoesNotChangePorts(t *testing.T) {
	sup, opts, sp := serveFixture(t)
	opts.Port = 0
	if err := os.MkdirAll(filepath.Dir(opts.Log), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.Log, []byte("E srv: unknown argument: --models-preset\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sp.probeErr = errors.New("no answer")

	// A short deadline rather than a sleep: readiness is a wait, and the wait is the thing
	// this test is not interested in. What it is interested in is which port the next attempt
	// would use, and that is decided before the wait ends.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := sup.Start(ctx, *opts, autoModels()); err == nil {
		t.Fatal("a start whose server refused to run reported success")
	}
	if len(sp.ports) != 1 {
		t.Errorf("%d attempts on a port: %v, want one — the preset is the problem, not the port",
			len(sp.ports), sp.ports)
	}
}

// What the router is told about the card: a cap on how many models may be resident, and no
// number to keep free. --models-memory-margin is the flag that switches on llama.cpp's
// device-memory accounting, and that accounting is paid for by spawning a measurement process
// per model and waiting for its answer. A start that did not come up on a real card logged
// exactly that: `measure process for model name=libris-chat exited with status 1 before
// reporting`, then `make_room: waiting for the memory measurement`. So the margin was not
// protecting the card; it was a second way for the router to stop, and one libris cannot fix
// from here. Counting models needs no measurement, so that is what is asked for (ADR 0025).
func TestTheRouterIsNotToldToMeasureTheCard(t *testing.T) {
	sup, opts, sp := serveFixture(t)
	opts.Port = 0
	if _, err := sup.Start(context.Background(), *opts, autoModels()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	args := strings.Join(argList(sp.cmd), " ")
	if strings.Contains(args, "--models-memory-margin") {
		t.Errorf("the router was told to keep memory free anyway:\n%s", args)
	}
	for _, want := range []string{"--models-preset", "--models-max", "--host", "--port", "--no-ui"} {
		if !strings.Contains(args, want) {
			t.Errorf("the router is not told %s:\n%s", want, args)
		}
	}
}

// The log outlives the process that wrote it, and it is appended rather than truncated — a
// person reading it after a crash wants the whole story, not the last start's. So the words in
// it are not all from this attempt, and treating them as if they were makes a machine that once
// had a port collision refuse every start afterwards: "no free port could be found", pointing
// at a socket that was free. This is the regression that happened, found because the settings
// page said exactly that while nothing held anything.
func TestAnOlderRunsBindFailureIsNotBlamedOnThisOne(t *testing.T) {
	sup, opts, sp := serveFixture(t)
	opts.Port = 0
	// Another run's failure, already in the file before this start opens it.
	writeTakenLog(t, opts.Log)
	sp.probeErr = errors.New("dial tcp 127.0.0.1: connect: connection refused")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := sup.Start(ctx, *opts, autoModels())
	if err == nil {
		t.Fatal("a start whose server never answered reported success")
	}
	if errors.Is(err, engine.ErrPortTaken) {
		t.Fatalf("a start was refused as a port collision by a line another run wrote: %v", err)
	}
	if len(sp.ports) != 1 {
		t.Errorf("%d ports tried (%v); only a real collision is worth another", len(sp.ports), sp.ports)
	}
}

// A router that dies on the way up stops answering immediately. A probe that only asks the
// socket cannot tell "not yet" from "nobody is home", and would spend the whole readiness
// budget — ninety seconds — on a failure that happened in a fifth of one. This is the start
// that took 11 seconds to fail in front of a real user.
func TestADeathIsNotWaitedOut(t *testing.T) {
	sup, opts, sp := serveFixture(t)
	opts.Port = 0
	sp.probeErr = errors.New("connection refused")
	sp.dies = true

	started := time.Now()
	_, err := sup.Start(context.Background(), *opts, autoModels())
	took := time.Since(started)
	if err == nil {
		t.Fatal("a start whose process vanished reported success")
	}
	if took > 5*time.Second {
		t.Errorf("a process that died at once took %s to report — the wait should notice it is gone", took)
	}
	if len(sp.ports) != 1 {
		t.Errorf("%d ports tried (%v) for a death that is not a collision",
			len(sp.ports), sp.ports)
	}
}

// The port is passed to the process and is not a key in the file (ADR 0023): the file is
// rewritten only when the models or the device moved, and a number chosen per start would
// rewrite it on every start and stomp whatever a person edited there.
func TestTheChosenPortNeverEntersThePreset(t *testing.T) {
	sup, opts, _ := serveFixture(t)
	opts.Port = 0

	srv, err := sup.Start(context.Background(), *opts, autoModels())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(opts.Preset)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(got)), "port") {
		t.Errorf("the preset named a port, which would make every Auto start a rewrite:\n%s", got)
	}
	if strings.Contains(string(got), strconv.Itoa(srv.Port)) {
		t.Errorf("the preset holds the chosen port %d", srv.Port)
	}
}
