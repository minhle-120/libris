package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// One llama-server, in router mode, serving every model libris has.
//
// Router mode is what makes "one engine" and "three models" the same sentence: launched
// without a model, llama-server routes on the "model" field of each request and keeps a
// process per model behind one port (tools/server/README.md, "Using multiple models"). So
// libris supervises one process, with one pid, one log and one stop button — and the three
// addresses the settings page used to ask for become one address plus three names libris
// chose itself.
//
// The models themselves are configured by a preset file written here, not by flags
// assembled at a call site. That distinction matters: capacity and sampling are
// not settings, and they are not on the wire either. They are in this file, which libris
// writes and which a person may edit with an editor — a file in the data directory, not a
// field in a form that would promise to apply it before the next restart.

// Alias is the name libris gives a model in the preset, and therefore the name it sends in
// the "model" field. They are libris's to choose, which is what lets managed mode fill in
// the model configuration instead of asking for it.
type Alias string

const (
	AliasChat  Alias = "libris-chat"
	AliasEmbed Alias = "libris-embed"

	// AliasEmbedCPU is the same file as AliasEmbed, loaded the other way: pinned to the host
	// instead of the device. It is a second name because it is a second instance — llama.cpp
	// fixes a model's offload set when it allocates it, so "the embedder on the GPU for a
	// thousand chunks and on the CPU for one question" cannot be one loaded model with two
	// settings. It is a name and not a model: nothing downloads it, nothing in the catalogue
	// has it, and the vectors it makes are stamped with the identity of the file it shares.
	AliasEmbedCPU Alias = "libris-embed-cpu"
)

// ErrAlreadyServing means a start was asked for while libris's own server was up. It is a
// sentinel because the answer is a status code and a sentence, not a stack.
var ErrAlreadyServing = errors.New("engine: the engine is already running")

// ErrNotServing is its mirror.
var ErrNotServing = errors.New("engine: the engine is not running")

// ErrPortTaken means something else answered — or refused — on the port libris wanted. The
// sentence the page shows names the port, because that is the thing the user can change.
var ErrPortTaken = errors.New("engine: the port is already in use")

// ModelKind is what a model is for, which decides the preset section it needs. The keys are
// the ones llama.cpp's own preset format expects (common/arg.cpp: LLAMA_ARG_EMBEDDINGS,
// LLAMA_ARG_POOLING), checked rather than remembered.
type ModelKind string

const (
	KindChat  ModelKind = "chat"
	KindEmbed ModelKind = "embed"
)

// Model is one entry in the preset. MMProj is the vision projector for a multimodal model, and
// Draft is the extra file that guesses tokens for it; both are empty when the model has neither.
type Model struct {
	Alias  Alias
	Kind   ModelKind
	Path   string
	MMProj string
	Draft  string

	// Device overrides the file's offload device for this one section. Empty inherits the
	// global, which is the point: the absence of a device key means llama.cpp chooses, so a
	// section that must not touch the card says so in words ("none") rather than by saying
	// nothing.
	Device string
}

// What each kind of model is given. These are not settings and not per-request: they are what
// "libris runs this model" means, and the only place they exist is this file: sampling
// belongs to the server, and capacity is not a setting.
//
// The context sizes are per architecture, not per machine: 32k for the chat model is what its
// template and the research loop's history want, and llama.cpp still shrinks the ones it has
// to with -fit. The embedder's 8k is not what a 100-word chunk needs — its own GGUF says
// gemma-embedding2.context_length = 262144, so 8k is nowhere near the trained limit, and the
// four slots of an embeddings instance share one pool rather than each getting a quarter of it.
// What shares that pool is not only prose: a page raster arrives as vision tokens, several times
// the size of the paragraph it shows. The pool is where concurrent segments collide, so the pool
// is sized for the largest of them and the others.
//
// The three sampling numbers are on the chat model because the chat model is the one thing that
// plans — and they are that model's own recipe, not a taste. A QAT checkpoint was tuned against
// exactly this distribution, so cooling it to a temperature somebody preferred samples a network
// whose weights were quantised to behave under another one.
//
// That is also why nothing else is written. The old section carried min-p and a repeat-penalty;
// penalties are absent because a tuned sampling set is one joint thing and a penalty inherited
// from a model this one replaced is a sixth key nobody tuned. What used to be argued for by a
// cold distribution — that a research turn should not invent a fifth search — is held by the
// tool contract and by the message history instead, which is where a wrong search can actually
// be corrected.
//
// They are strings because they are pasted into an INI: %v of 1.0 would write "1", and the file
// is meant to be read by a person as well as by llama.cpp.
//
// The draft numbers bound what speculation costs. n-max 3 caps how many tokens one guess may
// contribute — a guess that runs long and misses wastes the pass that made it — and the draft's
// KV cache is kept at q8_0 for the same reason the real model's is, for less memory per token
// at a cost the draft model can afford.
// ubatchSize is the biggest single input the server will hold. llama.cpp fits one media
// input — one page raster, one clip — inside one ubatch, and the 512 default fits neither. The
// consequence at 512 is not a clean refusal: an image is quietly shrunk,
// `cap image_max_tokens (original=1120) to 256 (n_ubatch = 512) … increase n_ubatch (-ub)`, so
// every page raster libris has ever sent reached the answering model at a quarter of the detail
// it was rasterised at; audio is refused outright past about twenty seconds,
// `input (754 tokens) is too large to process. increase the physical batch size (current batch
// size: 512)`. Both sentences are the pinned build's own, from its log.
//
// 4096 is not a generous round number. Audio costs 25.4 tokens a second on both models libris
// ships, so 4096 is a little over two minutes of a recording as one input — more than any clip
// ingest will ever cut, since the setting tops out at 60 s — and a page raster costs about
// 1120 of them. -b, the batch size the second message names first, is the wrong knob: it is how
// much the server runs at once, and a single input has to fit inside one ubatch.
//
// A clip of a video is the third input this has to hold, and the one the number is tightest
// for: 107 tokens a frame at 640 across, so forty frames is the most one input can be, which is
// why ingest refuses a window over 30 seconds rather than letting the server find out. The
// refusal this produces when the arithmetic is wrong is the worst-worded of the three, because
// it names a number that is not in force — measured with the flag off:
// `input (6482 tokens) is too large to process. increase the physical batch size (current batch
// size: 2048)`, from a server started with ubatch 4096. --video-fps is the flag a reader reaches
// for after that sentence, and it does not cap anything: measured at a target of 100 frames a
// second, a 15-second clip of 1-frame-a-second footage became 1,500 frames and 169,642 tokens.
// -ub remains the only knob that fits a bigger input.
const ubatchSize = 4096

const (
	chatCtx  = 32768
	embedCtx = 8192

	chatTemp = "1.0"
	chatTopK = "64"
	chatTopP = "0.95"

	specDraftNMax = "3"
	specDraftType = "q8_0"
)

// Serving is the truth about libris's own llama-server, as the settings page shows it.
type Serving struct {
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
	// StartedAt is when libris last spawned the process, not when the machine booted: a
	// server that has been up for six days is a fact worth showing next to "restart it".
	StartedAt time.Time `json:"started_at,omitempty"`
	Port      int       `json:"port"`
	Models    []string  `json:"models,omitempty"`
	// Device is what the file the router read actually says, read back after the write rather
	// than repeated from the call: a blank setting means libris picked one, and the file is what
	// went to the process. The page shows this rather than the setting, because the process is
	// what the answer came out of.
	Device string `json:"device,omitempty"`

	// Log is the file the server's own output went to. A model that fails to load says why
	// there and nowhere else, so the path is part of the answer rather than a debug detail.
	Log string `json:"log,omitempty"`
	// Error is the last start that failed, in one sentence. Cleared by a successful start,
	// so it cannot outlive the port conflict that caused it — the same trap as a failure
	// message describing a server that was never asked.
	Error string `json:"error,omitempty"`
}

// ServeOptions is who to run and with what.
type ServeOptions struct {
	// Bin is the installed llama-server. Empty means nothing is installed, which is a state
	// the page shows as a download button rather than an error.
	Bin  string
	Host string

	// Port is the loopback port to bind. Zero means Auto: the kernel is asked for a free port
	// at each attempt, and an attempt that loses the race between probing a port and
	// llama-server binding it is retried. A non-zero port is tried exactly once and a
	// collision fails with the port named, because the reason a person pins a port is usually
	// that something else was already told that address — moving it silently would break the
	// thing they pinned it for.
	Port   int
	Preset string
	Log    string

	// VideoFFmpegDir is the folder the router should look in for ffmpeg and ffprobe, which it
	// needs to decode a clip of a video. Empty tells the router to search PATH, which is what
	// it does for a libris built from source on a machine whose programs came from a package
	// manager. tools.Resolver answers it, because that is the package that knows whether this
	// install brought its own programs.
	VideoFFmpegDir string

	// ModelsMax caps how many model instances the router keeps resident. Two, which is one fewer
	// than the preset declares: the chat model and one embedder fit together, and the third
	// instance is the one that gets to be cold.
	//
	// It is a cap and not a refusal. When there is no room, llama.cpp picks a victim among the
	// instances that are idle — a model with a request in flight is never a victim — and if none
	// can be evicted the request joins a queue rather than failing (server-models-sched.cpp,
	// pick_victim and try_claim). So a question that arrives in the middle of an embedding batch
	// waits for that batch, then takes the slot and evicts whichever embed instance was touched
	// least. That is the memory behaviour wanted, paid for with a reload.
	ModelsMax int
	// Device is the --device value for every model in the preset, already resolved against
	// what this build reports. Empty means write no device key at all.
	Device string
}

func (o ServeOptions) withDefaults() ServeOptions {
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	if o.ModelsMax <= 0 {
		o.ModelsMax = 2
	}
	return o
}

// serveFile is the record of a running server. It lives in the data directory so that a
// libris which died — not quit — can find what it left behind instead of starting a second
// server on a port the first one still holds.
type serveFile struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	StartedAt time.Time `json:"started_at"`
	Log       string    `json:"log,omitempty"`
	Models    []string  `json:"models,omitempty"`
	// Device is the device key the preset the router read actually carried, so that a page
	// reopened after a restart can still say what the models are running on.
	Device string `json:"device,omitempty"`
}

// Supervisor owns one llama-server process for one data directory.
type Supervisor struct {
	dir string
	log *serveLog
	// mu guards `stopping`, which is how a stop libris asked for is told apart from a death it
	// did not: both end in the same Wait, and only one of them deserves a log line.
	mu       sync.Mutex
	stopping bool
	// logger is where an unasked-for death is said. Nil means the process default, the same
	// courtesy engine.Manager's Open extends.
	logger *slog.Logger
	// exited says, in libris's own log, that the router stopped without being asked to. A field
	// so a test can count the times it was said rather than read a log.
	exited func(pid int, err error)
	spawn  func(cmd *exec.Cmd) (int, error)
	probe  func(ctx context.Context, url string) error
	signal func(pid int, sig syscall.Signal) error
	now    func() time.Time
}

// Seams are the parts of running a process that a test may not do: spawn llama-server, wait
// on its socket, signal a pid, read a clock. Zero values mean the real ones.
type Seams struct {
	// Spawn starts the process and reports its pid. Returning the pid rather than reading it
	// off the command is what lets a test answer without starting anything. Injecting it also
	// means injecting the reaping — the default spawner waits, and a test that spawns nothing
	// has nothing to wait on.
	Spawn  func(cmd *exec.Cmd) (int, error)
	Logger *slog.Logger
	Probe  func(ctx context.Context, url string) error
	Signal func(pid int, sig syscall.Signal) error
	Now    func() time.Time
}

// NewSupervisor points a supervisor at the directory it owns — the record of the running
// process lives there, beside the binaries it runs.
func NewSupervisor(dir string, seams Seams) *Supervisor {
	sup := &Supervisor{dir: dir, log: &serveLog{path: filepath.Join(dir, "serve.json")}}
	sup.logger = seams.Logger
	if sup.logger == nil {
		sup.logger = slog.Default()
	}
	sup.exited = sup.noteExit
	sup.spawn = seams.Spawn
	if sup.spawn == nil {
		sup.spawn = sup.reap
	}
	sup.probe = seams.Probe
	if sup.probe == nil {
		sup.probe = probeModels
	}
	sup.signal = seams.Signal
	if sup.signal == nil {
		sup.signal = signalPID
	}
	sup.now = seams.Now
	if sup.now == nil {
		sup.now = func() time.Time { return time.Now().UTC() }
	}
	return sup
}

// Endpoint is the base URL libris talks to when it runs the server itself. Loopback, always:
// the process is ours, so "which host" is not a question.
func Endpoint(host string, port int) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

// Status reports the process as the record plus the machine say it is, not as the record
// alone: a pid that stopped being ours between boots would otherwise be reported as running
// forever. An unreadable record is "not running", which is the honest floor.
func (s *Supervisor) Status() Serving {
	rec, ok := s.log.read()
	if !ok {
		return Serving{}
	}
	srv := Serving{
		Running:   rec.PID > 0 && alive(rec.PID, s.signal),
		PID:       rec.PID,
		StartedAt: rec.StartedAt,
		Port:      rec.Port,
		Models:    rec.Models,
		Device:    rec.Device,
		Log:       rec.Log,
	}
	if !srv.Running {
		// The number is not the fact. Keeping a dead pid visible would make "restart" the
		// only way out of a state the page claims is fine.
		srv.PID = 0
	}
	return srv
}

// Start makes sure the preset in the data directory describes what libris has now, spawns the
// router that reads it, and does not return until the router answers
// /models or the wait runs out. Blocking is deliberate: a page that reports "running" for a
// process that dies forty seconds later while loading a GGUF is a page that lies, and the
// first load of a 3 GB model is exactly when that happens.
func (s *Supervisor) Start(ctx context.Context, o ServeOptions, models []Model) (Serving, error) {
	o = o.withDefaults()

	if o.Bin == "" {
		return Serving{}, fmt.Errorf("engine: no engine is installed to run")
	}
	if cur := s.Status(); cur.Running {
		return cur, ErrAlreadyServing
	}
	// The file the router reads is rewritten now, from what libris derives about the models and
	// the device, and what this reports afterwards is the file's own answer read back — not the
	// derivation. Why every start writes is refreshPreset's comment.
	have, err := refreshPreset(o.Preset, o.Device, o.VideoFFmpegDir, models)
	if err != nil {
		return Serving{}, err
	}
	if err := os.MkdirAll(filepath.Dir(o.Log), 0o755); err != nil {
		return Serving{}, fmt.Errorf("engine: log dir: %w", err)
	}

	logFile, err := os.OpenFile(o.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Serving{}, fmt.Errorf("engine: open the server log: %w", err)
	}
	defer logFile.Close()

	// A pinned port gets one attempt and no second guess. Auto asks the kernel each time.
	attempts := 1
	if o.Port <= 0 {
		attempts = autoPortAttempts
	}

	var (
		last    Serving
		lastErr error
	)
	for range attempts {
		port := o.Port
		if port <= 0 {
			if port, lastErr = freePort(o.Host); lastErr != nil {
				return Serving{}, lastErr
			}
		}
		if last, lastErr = s.startOnce(ctx, o, port, logFile, have); lastErr == nil {
			return s.Status(), nil
		}
		// Only a lost port is worth another try. Anything else — a preset key the build
		// refused, a model that would not load — would fail identically on the next port, and
		// retrying it would only spend another thirty seconds saying the same thing.
		if !errors.Is(lastErr, ErrPortTaken) {
			return last, lastErr
		}
	}
	if o.Port <= 0 {
		// Every attempt collided. Naming the last port would send someone to look at a socket
		// that is almost certainly free again by now, so this says what was actually tried.
		last.Error = noPortSentence
	}
	return last, lastErr
}

// startOnce runs the router on one port and decides whether it came up. The port is a
// parameter rather than o.Port because a start on Auto may try several, and the argument,
// the record and the URL waited on all have to agree on which one was used.
func (s *Supervisor) startOnce(ctx context.Context, o ServeOptions, port int, logFile *os.File, have *PresetFile) (Serving, error) {
	// The router is llama-server with no model of its own. --models-preset names the file
	// that says which models exist and how each is loaded; --models-max is what keeps three
	// sections in a preset from becoming three model instances whether or not anything asked
	// for them.
	//
	// --models-memory-margin is deliberately not passed. It is the flag that turns on the
	// router's device-memory accounting, and that accounting is paid for by launching a
	// *measurement* process per model and waiting on its answer: the log of a start that did
	// not come up said `measure process for model name=libris-chat exited with status 1 before
	// reporting` and then sat in `make_room: waiting for the memory measurement`. So the margin
	// was not protecting a small card, it was a second way for the whole router to stop. What
	// remains is the cap that does not need to measure anything: at most ModelsMax instances,
	// least recently used unloaded first.
	//
	// The port is an argument and not a key in that file on purpose: what the file holds are
	// facts about models — which exist, where their files are, what each is given — and a port
	// is not one of those. A number chosen per start belongs beside the pid and the start time,
	// which is where the serve record already keeps it, and putting it here would let a socket
	// collision rewrite what a model is given.
	args := []string{
		"--models-preset", o.Preset,
		"--host", o.Host,
		"--port", strconv.Itoa(port),
		"--models-max", strconv.Itoa(o.ModelsMax),
		"--no-ui",
	}
	// Not CommandContext: the request that asked for the start goes away in a second, and a
	// model server that dies when a browser tab closes is not the thing that was asked for.
	// The supervisor owns this process from here, and Stop is what ends it.
	cmd := exec.Command(o.Bin, args...)
	// Where this attempt's own words begin. The log is appended for the life of the data
	// directory, so whatever it already holds belongs to other starts — and reading that as if
	// it were this process's makes a machine that once had a port collision refuse every start
	// afterwards with "no free port could be found", because the stale bind failure is still in
	// the last few kilobytes. That is not a hypothetical: it is what this code did on the first
	// run after a collision, and the sentence it produced pointed at a socket that was free.
	before := logSize(logFile)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Its own process group, so the signal that stops the router reaches the model
	// instances it spawned. Killing only the router would leave children holding the GPU,
	// which is the one failure a local app must not leave behind.
	setGroupAttr(cmd)

	pid, err := s.spawn(cmd)
	if err != nil {
		return Serving{}, fmt.Errorf("engine: start %s: %w", o.Bin, err)
	}
	if pid <= 0 {
		return Serving{}, fmt.Errorf("engine: start %s: no process id", o.Bin)
	}
	rec := serveFile{PID: pid, Port: port, StartedAt: s.now(), Log: o.Log, Device: have.Device}
	for _, m := range have.Models {
		if m.Alias != "" {
			rec.Models = append(rec.Models, string(m.Alias))
		}
	}
	if err := s.log.write(rec); err != nil {
		// The process is up. Losing the record means the next libris cannot find it, so the
		// honest answer is to stop what was just started rather than run something unowned.
		s.killGroup(pid)
		return Serving{}, err
	}

	url := Endpoint(o.Host, port) + "/models"
	if err := s.waitReady(ctx, url, pid, o.Log, before); err != nil {
		// Whatever the router said on its way out is the answer the user needs — a port
		// already taken, a preset key it refused — and it is in the log and nowhere else. This
		// attempt's half of the log only: see logSize.
		tail := sinceOf(o.Log, before, 4096)
		s.Stop()
		if portTaken(tail) {
			return Serving{Port: port, Log: o.Log, Error: portSentence(port)}, ErrPortTaken
		}
		why := lastLine(tail)
		if why == "" {
			why = oneSentence(err.Error())
		}
		return Serving{Port: port, Log: o.Log, Error: why},
			fmt.Errorf("engine: the engine did not come up: %s", why)
	}
	return Serving{}, nil
}

// Stop asks the router to shut down, and waits. SIGINT (SIGTERM is what llama-server treats
// as a plain terminate) gives it a chance to unload the models it loaded; after a grace
// period the whole group is killed, because a half-exited model instance holding a GPU is
// worse than a slow stop.
func (s *Supervisor) Stop() error {
	rec, ok := s.log.read()
	if !ok || rec.PID == 0 {
		return ErrNotServing
	}
	defer s.log.clear()

	// Deliberate, before the first signal: the reaper's Wait is about to see a non-nil exit
	// error for a process libris meant to end, and that is not a death worth reporting.
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.stopping = false
		s.mu.Unlock()
	}()

	if !alive(rec.PID, s.signal) {
		return nil
	}
	// Negative pid: the process group, which is what Start made.
	_ = s.signal(-rec.PID, sigTerminate)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(rec.PID, s.signal) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = s.signal(-rec.PID, sigForce)
	return nil
}

func (s *Supervisor) killGroup(pid int) {
	_ = s.signal(-pid, sigForce)
	s.log.clear()
}

// defaultSpawn is the real thing: start the child, and report the pid the supervisor will
// signal later. A child that started without a pid is not representable here, so it is an
// error rather than a record claiming pid 0.
// exited records that the router stopped on its own. The record of a running server is left
// alone: it still points at a pid nobody owns, and Status already calls that not running once
// the child is reaped — what this adds is the sentence, because "the model server stopped" and
// "the model server stopped: signal: 9" are different things to read at 2am.
func (s *Supervisor) noteExit(pid int, err error) {
	s.logger.Warn("engine: the model server exited on its own", "pid", pid, "err", err)
}

// reap is the default spawner: start the process, and wait on it.
//
// The wait is not decoration. A child nobody waits on becomes a zombie the moment it exits,
// and a zombie answers kill(2) — so `alive` says true, the page says "running", Start refuses
// with "already running", and Stop spends its whole ten-second grace period signalling a
// process that is already gone. The way a model server actually dies is exactly this: a GGUF
// it cannot load, an out-of-memory kill, a Vulkan driver that refused the device. Without the
// wait, that death is indistinguishable from a healthy server, for as long as libris runs.
//
// It is a method rather than a package function so that a test which injects its own Spawn
// starts no goroutine and owns its own pid table.
func (s *Supervisor) reap(cmd *exec.Cmd) (int, error) {
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	if cmd.Process == nil {
		return 0, errors.New("started without a process id")
	}
	pid := cmd.Process.Pid
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		stopping := s.stopping
		s.mu.Unlock()
		if err != nil && !stopping {
			// The why belongs in the log the page already points at; here it is one line in
			// libris's own log, because "the server stopped" with no cause is the sentence a
			// person reads looking for a cause.
			s.exited(pid, err)
		}
	}()
	return pid, nil
}

// waitReady polls until the router answers. The router answers /models as soon as it is up —
// before any model has finished loading, which is the point: a model that will not load is a
// fact about that model, and the page shows it from /models rather than by never coming back.
// waitReady polls until the router answers, or until the process it was waiting for is gone.
//
// The second condition is the whole point. A router that dies on the way up — a port it could
// not bind, a preset key it refused — stops answering immediately, and a probe that only asks
// the socket would keep asking for the full readyTimeout before admitting that nobody is
// home: ninety seconds of "Starting…" for a failure that happened in two hundred milliseconds.
func (s *Supervisor) waitReady(ctx context.Context, url string, pid int, log string, from int64) error {
	deadline := time.Now().Add(readyTimeout)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !alive(pid, s.signal) {
			return fmt.Errorf("engine: the process exited before it answered")
		}
		// The log is read as well as the socket, because llama.cpp's router prints
		// "couldn't bind HTTP server socket" and then *stays alive* for the better part of a
		// minute, measuring models that will never be served. Nothing will ever answer on that
		// socket, so the one fact worth knowing this early is in the only place it is written.
		if portTaken(sinceOf(log, from, 4096)) {
			return fmt.Errorf("engine: the server said the port is already taken")
		}
		if err := s.probe(ctx, url); err == nil {
			return nil
		} else {
			last = err
			if errors.Is(err, ErrPortTaken) {
				return err
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("no answer from %s", url)
	}
	return fmt.Errorf("%w after %s", last, readyTimeout)
}

// readyTimeout is how long a first load gets. It is not a guess at model loading speed —
// router mode answers before loading, so this is time to bind a socket and read one INI file,
// and the long budget is for a machine where a 3 GB mmap has to be paged in before anything
// answers at all.
const readyTimeout = 90 * time.Second

func probeModels(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<10))
	if res.StatusCode >= 400 {
		return fmt.Errorf("%s answered %d", url, res.StatusCode)
	}
	return nil
}

// portTaken reads the router's own complaint about the port. libris does not bind the socket
// itself — the child does — so "address already in use" is only ever visible in its output.
// portTaken is whether the server's own log says the socket belonged to somebody else.
//
// The phrase that matters is llama.cpp's, not Go's: what it prints is
// `couldn't bind HTTP server socket, hostname: 127.0.0.1, port: 9999`, and it does not carry
// the errno's sentence with it. Matching only "address already in use" — what net.Listen
// writes — matched a string this program never sees, so every real collision was reported as
// a mysterious failure that would not start again, on a port that was never going to free up.
// The line here is what a real llama.cpp prints when something else holds the port; the
// fixture in port_test.go quotes it.
func portTaken(log string) bool {
	l := strings.ToLower(log)
	return strings.Contains(l, "couldn't bind http server socket") ||
		strings.Contains(l, "address already in use") ||
		strings.Contains(l, "only one usage of each socket address")
}

func portSentence(port int) string {
	return fmt.Sprintf("Something else is already using port %d. Change the port in Settings, or stop the other server.", port)
}

// lastLine is the last sentence the server managed to write. A model that will not load
// prints why, and a settings page that only said "it failed" would send the user to a
// terminal to find out what the page already knows.
func lastLine(log string) string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return oneSentence(l)
		}
	}
	return ""
}

// logSize is how many bytes a log file already holds. It is the offset a caller remembers so
// that a later read can be scoped to one attempt — the file outlives the process, and the
// words in it are not all from this start.
func logSize(f *os.File) int64 {
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}

// sinceOf reads what a log holds from a remembered offset onwards, capped at n bytes from the
// end of that region. Reading the whole tail instead is how a start gets blamed for a failure
// some earlier start reported.
func sinceOf(path string, from, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= from {
		return ""
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return ""
	}
	if st.Size()-from > n {
		if _, err := f.Seek(st.Size()-n, io.SeekStart); err != nil {
			return ""
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return ""
	}
	return string(b)
}

// oneSentence keeps a subprocess's complaint inside one line: a warning rendered as a <p> is
// not a place for a stack of ggml's startup log.
func oneSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 300
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// serveLog is the record of the running process, and the only file the supervisor writes
// other than the preset and the log it inherits.
type serveLog struct{ path string }

func (l *serveLog) read() (serveFile, bool) {
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return serveFile{}, false
	}
	var rec serveFile
	if err := json.Unmarshal(raw, &rec); err != nil || rec.PID == 0 {
		return serveFile{}, false
	}
	return rec, true
}

func (l *serveLog) write(rec serveFile) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("engine: record dir: %w", err)
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("engine: record: %w", err)
	}
	return nil
}

func (l *serveLog) clear() { _ = os.Remove(l.path) }

// alive asks whether a pid is still a process we may signal. EPERM means it is alive and
// somebody else owns it, which is still "do not start a second server on that port".
func alive(pid int, signal func(int, syscall.Signal) error) bool {
	err := signal(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
