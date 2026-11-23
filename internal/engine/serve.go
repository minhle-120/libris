package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
// chose itself (ADR 0020).
//
// The models themselves are configured by a preset file written here, not by flags
// assembled at a call site. That distinction matters for ADR 0011: capacity and sampling are
// not settings, and they are not on the wire either. They are in this file, which libris
// writes and which a person may edit with an editor — a file in the data directory, not a
// field in a form that would promise to apply it before the next restart.

// Alias is the name libris gives a model in the preset, and therefore the name it sends in
// the "model" field. They are libris's to choose, which is what lets managed mode fill in
// the model configuration instead of asking for it.
type Alias string

const (
	AliasChat   Alias = "libris-chat"
	AliasEmbed  Alias = "libris-embed"
	AliasRerank Alias = "libris-rerank"
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
// LLAMA_ARG_POOLING, LLAMA_ARG_RERANKING), checked rather than remembered.
type ModelKind string

const (
	KindChat   ModelKind = "chat"
	KindEmbed  ModelKind = "embed"
	KindRerank ModelKind = "rerank"
)

// Model is one entry in the preset. MMProj is the vision projector for a multimodal model,
// and empty when there is none.
type Model struct {
	Alias  Alias
	Kind   ModelKind
	Path   string
	MMProj string
}

// Preset writes the INI file the router reads. Its shape is the one upstream documents
// (tools/server/README.md, "Model presets"): a version line, a [*] section shared by every
// model, then one section per model whose keys are command-line arguments without the dashes.
//
// The globals are the ones a local notebook wants: flash attention on, jinja templates so a
// chat template is not guessed, no warmup (the first question is the user's, not a benchmark),
// and -fit left on so llama.cpp chooses the context that fits the memory it finds rather than
// libris claiming a number it cannot measure (ADR 0011, and the reason ADR 0019's device
// report stops at reporting).
//
// Per-kind choices are not style: an embedding model needs --embeddings with a pooling mode,
// a reranker needs --reranking, and neither is useful as a chat model. cache-ram = 0 on the
// two small models keeps them from holding host memory the chat model needs.
func Preset(path string, models []Model) error {
	var b strings.Builder
	b.WriteString("; Written by libris. Do not add a model here and expect it to stay:\n")
	b.WriteString("; the list follows what is in the models directory.\n")
	b.WriteString("; Capacity and sampling belong to the server, not to a request (ADR 0006, ADR 0011).\n")
	b.WriteString("version = 1\n\n[*]\n")
	b.WriteString("flash-attn = on\n")
	b.WriteString("jinja = true\n")
	b.WriteString("no-warmup = true\n")
	b.WriteString("fit = on\n")
	b.WriteString("parallel = 1\n")
	b.WriteString("cache-type-k = q8_0\n")
	b.WriteString("cache-type-v = q8_0\n\n")

	for _, m := range models {
		if m.Path == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s]\n", m.Alias)
		fmt.Fprintf(&b, "model = %s\n", m.Path)
		if m.MMProj != "" {
			fmt.Fprintf(&b, "mmproj = %s\n", m.MMProj)
		}
		switch m.Kind {
		case KindEmbed:
			b.WriteString("embeddings = on\npooling = mean\ncache-ram = 0\nctx-size = 2048\n")
		case KindRerank:
			b.WriteString("reranking = on\ncache-ram = 0\nctx-size = 2048\n")
		default:
			b.WriteString("ctx-size = 8192\n")
		}
		b.WriteString("load-on-startup = true\n\n")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("engine: preset dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("engine: write preset: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("engine: replace preset: %w", err)
	}
	return nil
}

// Serving is the truth about libris's own llama-server, as the settings page shows it.
type Serving struct {
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
	// StartedAt is when libris last spawned the process, not when the machine booted: a
	// server that has been up for six days is a fact worth showing next to "restart it".
	StartedAt time.Time `json:"started_at,omitempty"`
	Port      int       `json:"port"`
	Models    []string  `json:"models,omitempty"`

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
	Bin    string
	Host   string
	Port   int
	Preset string
	Log    string

	// ModelsMax caps how many model instances the router keeps. Three is every model libris
	// knows about; the cap exists so a preset with more sections is not a memory incident.
	ModelsMax int
	// MemoryMarginMiB is what the router keeps free on each device, unloading the least
	// recently used model until a new one fits. On a 4 GB card this is the difference between
	// three models and three models thrashing.
	MemoryMarginMiB int
}

func (o ServeOptions) withDefaults() ServeOptions {
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	if o.ModelsMax <= 0 {
		o.ModelsMax = 3
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
}

// Supervisor owns one llama-server process for one data directory.
type Supervisor struct {
	dir    string
	log    *serveLog
	spawn  func(cmd *exec.Cmd) (int, error)
	probe  func(ctx context.Context, url string) error
	signal func(pid int, sig syscall.Signal) error
	now    func() time.Time
}

// Seams are the parts of running a process that a test may not do: spawn llama-server, wait
// on its socket, signal a pid, read a clock. Zero values mean the real ones.
type Seams struct {
	// Spawn starts the process and reports its pid. Returning the pid rather than reading it
	// off the command is what lets a test answer without starting anything.
	Spawn  func(cmd *exec.Cmd) (int, error)
	Probe  func(ctx context.Context, url string) error
	Signal func(pid int, sig syscall.Signal) error
	Now    func() time.Time
}

// NewSupervisor points a supervisor at the directory it owns — the record of the running
// process lives there, beside the binaries it runs.
func NewSupervisor(dir string, seams Seams) *Supervisor {
	sup := &Supervisor{dir: dir, log: &serveLog{path: filepath.Join(dir, "serve.json")}}
	sup.spawn = seams.Spawn
	if sup.spawn == nil {
		sup.spawn = defaultSpawn
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
// the process is ours, so "which host" is not a question (§2.9).
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
		Log:       rec.Log,
	}
	if !srv.Running {
		// The number is not the fact. Keeping a dead pid visible would make "restart" the
		// only way out of a state the page claims is fine.
		srv.PID = 0
	}
	return srv
}

// Start writes the preset, spawns the router, and does not return until the router answers
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
	if err := Preset(o.Preset, models); err != nil {
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

	// The router is llama-server with no model of its own. --models-preset names the file
	// that says which models exist and how each is loaded; --models-max and
	// --models-memory-margin are what keep three of them on one card.
	args := []string{
		"--models-preset", o.Preset,
		"--host", o.Host,
		"--port", strconv.Itoa(o.Port),
		"--models-max", strconv.Itoa(o.ModelsMax),
		"--no-ui",
	}
	if o.MemoryMarginMiB > 0 {
		args = append(args, "--models-memory-margin", strconv.Itoa(o.MemoryMarginMiB))
	}

	// Not CommandContext: the request that asked for the start goes away in a second, and a
	// model server that dies when a browser tab closes is not the thing that was asked for.
	// The supervisor owns this process from here, and Stop is what ends it.
	cmd := exec.Command(o.Bin, args...)
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
	rec := serveFile{PID: pid, Port: o.Port, StartedAt: s.now(), Log: o.Log}
	for _, m := range models {
		if m.Path != "" {
			rec.Models = append(rec.Models, string(m.Alias))
		}
	}
	if err := s.log.write(rec); err != nil {
		// The process is up. Losing the record means the next libris cannot find it, so the
		// honest answer is to stop what was just started rather than run something unowned.
		s.killGroup(pid)
		return Serving{}, err
	}

	url := Endpoint(o.Host, o.Port) + "/models"
	if err := s.waitReady(ctx, url); err != nil {
		// Whatever the router said on its way out is the answer the user needs — a port
		// already taken, a preset key it refused — and it is in the log and nowhere else.
		tail := tailOf(o.Log, 4096)
		s.Stop()
		if portTaken(tail) {
			return Serving{Port: o.Port, Log: o.Log, Error: portSentence(o.Port)}, ErrPortTaken
		}
		why := lastLine(tail)
		if why == "" {
			why = oneSentence(err.Error())
		}
		return Serving{Port: o.Port, Log: o.Log, Error: why},
			fmt.Errorf("engine: the engine did not come up: %s", why)
	}
	return s.Status(), nil
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
func defaultSpawn(cmd *exec.Cmd) (int, error) {
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	if cmd.Process == nil {
		return 0, errors.New("started without a process id")
	}
	return cmd.Process.Pid, nil
}

// waitReady polls until the router answers. The router answers /models as soon as it is up —
// before any model has finished loading, which is the point: a model that will not load is a
// fact about that model, and the page shows it from /models rather than by never coming back.
func (s *Supervisor) waitReady(ctx context.Context, url string) error {
	deadline := time.Now().Add(readyTimeout)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
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
func portTaken(log string) bool {
	l := strings.ToLower(log)
	return strings.Contains(l, "address already in use") ||
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

func tailOf(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := st.Size() - n
	if off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return ""
		}
	}
	raw, _ := io.ReadAll(f)
	return string(raw)
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
