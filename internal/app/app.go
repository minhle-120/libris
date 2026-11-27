// Package app holds the use cases. Both transports — the browser API and the
// later MCP server — call the same methods, so behaviour cannot drift between
// them.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/ingest"
	"libris/internal/retrieve"
)

// Store is what the use cases need from persistence. Declared by the consumer
// and narrow: the sqlite package may grow methods freely without widening this.
type Store interface {
	CreateNotebook(context.Context, string) (domain.Notebook, error)
	RenameNotebook(context.Context, string, string) error
	// Deleting either one answers with the sources it left without a notebook,
	// so the caller can delete their files after the database agreed.
	DeleteNotebook(context.Context, string) ([]domain.Orphan, error)
	GetNotebook(context.Context, string) (domain.Notebook, error)
	ListNotebooks(context.Context) ([]domain.Notebook, error)

	AddSource(context.Context, string, domain.Source) (domain.Source, bool, error)
	AttachSource(context.Context, string, string) error
	DetachSource(context.Context, string, string) ([]domain.Orphan, error)
	GetSource(context.Context, string) (domain.Source, error)
	ListSources(context.Context, string) ([]domain.Source, error)
	SetSourceStatus(context.Context, string, domain.SourceStatus, string, []domain.Finding) error

	PutAssets(context.Context, string, []domain.Asset) error
	PutSegments(context.Context, string, []domain.Segment) error
	PutEmbeddings(context.Context, []domain.Embedding) error
	ListSegments(context.Context, string) ([]domain.Segment, error)
	ListSegmentsPage(context.Context, string, int, int) ([]domain.Segment, map[domain.Kind]int, error)
	SegmentsWithoutEmbeddings(context.Context, string) ([]domain.Segment, error)
	GetAsset(context.Context, string) (domain.Asset, error)

	// Both of these are repair for work a previous process left behind: rows an older
	// build wrote that this one must not retrieve, and sources killed mid-flight.
	PurgeLegacyAudio(context.Context) (int, []domain.Orphan, error)
	FailStaleSources(context.Context) (int64, error)
}

// Chats is the persistence a conversation needs, kept off Store because ingest
// should not have to carry it: a build that only files sources away has no use for
// a message row, and a fake built to test ingest should not grow methods it never
// calls.
type Chats interface {
	CreateChat(context.Context, string, string) (domain.Chat, error)
	GetChat(context.Context, string) (domain.Chat, error)
	ListChats(context.Context, string) ([]domain.Chat, error)
	UpdateChatTitle(context.Context, string, string) error
	DeleteChat(context.Context, string) error

	AppendMessage(context.Context, domain.Message) (domain.Message, error)
	UpdateMessage(context.Context, int64, domain.MessageUpdate) error
	// ExcludeMessages marks whole turns — a question and its answer together — as not
	// knowledge, so they reach no later turn's history.
	ExcludeMessages(context.Context, ...int64) error
	GetMessage(context.Context, int64) (domain.Message, error)
	ListMessages(context.Context, string) ([]domain.Message, error)
	RecentMessages(context.Context, string, int) ([]domain.Message, error)
}

// Settings is the live configuration. Ingest reads it per job, so changing the
// PDF DPI takes effect on the next upload rather than at the next restart.
type Settings interface{ Get() config.Config }

// Ingestor is the file-processing pipeline.
type Ingestor interface {
	Process(ctx context.Context, path string) (*ingest.Result, error)
}

// Embedder produces one vector per segment, in input order, or an error. It is
// declared here rather than borrowed from internal/index because this package is
// the caller: app must be able to run against a recorded fake or a hosted API
// without knowing which.
//
// Returning fewer vectors than it was given is an error, not a partial success.
// A source with some vectors and not others answers questions with a silent
// slice of itself, which is the failure this contract exists to prevent. Handing
// the vectors over as they are finished does not weaken it: the source stays
// unready until every one of them has arrived and been written.
type Embedder interface {
	// Embed vectors a batch of segments belonging to one document. title is that
	// document's display name, used by models that want their inputs steered; an
	// embedder that has no use for it ignores it.
	//
	// onBatch, which may be nil, is given each finished batch as it lands — in no
	// order, from as many goroutines as the embedder has requests in flight. It is
	// how a four-thousand chunk upload can say how far it has got instead of
	// reporting nothing until the last vector. An error from it stops the run.
	Embed(ctx context.Context, title string, segments []domain.Segment, onBatch func([]domain.Embedding) error) ([]domain.Embedding, error)
}

// EmbedderFor builds an embedder for one configuration. It is a factory for the
// same reason Processor is: settings can change while the process runs, and a
// job must use the server the user has configured now, not the one that was set
// when libris started.
type EmbedderFor func(cfg config.Config) (Embedder, error)

// ProcessorFor builds an ingestor for one configuration.
type ProcessorFor func(config.Config, ingest.Sink) (Ingestor, error)

// queueDepth is the number of uploads that may wait. Ingest is deliberately
// serial — poppler and the model server both want RAM — so a burst of
// uploads queues rather than thrashing the machine. The cap exists so a runaway
// client cannot grow memory without bound; it is not tuned.
const queueDepth = 64

// jobTimeout bounds one ingest end to end. Nothing else bounds it: poppler runs
// outside our process, and Close waits for the job in flight, so
// without a deadline a wedged helper could hold shutdown open forever. Generous
// rather than tuned — a large scanned PDF is the case it must not interrupt.
const jobTimeout = 30 * time.Minute

// embedIdleWindow is how long the embedding model stays on the card after the last upload
// finishes, and it is a var only so the test does not sleep through it.
//
// The release itself is the point; this number is the grace. A person dropping twenty files
// sends twenty requests, and the queue is drained one at a time, so releasing the moment the
// queue empties would put the model to sleep between files and pay for its load twenty times —
// a cost paid entirely by the uploads that were always coming. Long enough to cover the pause
// between one file's request and the next, short enough that a machine left alone gives the
// memory back while nobody is looking at it.
var embedIdleWindow = 60 * time.Second

// Errors the transports map to statuses. Each is wrapped with context by the
// code that returns it, so errors.Is stays the way to test for them.
var (
	// ErrBusy means the ingest queue is full and the upload should be retried.
	ErrBusy = errors.New("ingest queue is full")
	// ErrTooLarge means the file passed the configured size cap.
	ErrTooLarge = errors.New("file is over the size limit")
	// ErrInvalid means the caller's request was not usable as sent.
	ErrInvalid = errors.New("invalid request")
)

// Options wires a Service.
type Options struct {
	Store     Store
	Settings  Settings
	Processor ProcessorFor
	Embedder  EmbedderFor

	// ReleaseEmbedder is optional. It tells the program that runs the model server that no
	// upload is coming for a while, and it may give back what the embedding model was holding on
	// the way to an answer.
	//
	// It is a function and not a field of some engine type because the app does not know a model
	// server exists — it knows uploads need embedding, and that the thing which embeds them may
	// be holding something expensive. Nil means there is nobody to tell: a server somebody else
	// started is not ours to put to sleep.
	ReleaseEmbedder func(ctx context.Context) error

	// RoomForIngest is optional, and it is ReleaseEmbedder's mirror: it tells the program that
	// runs the model server that an upload is about to want the card, and it may give back what
	// the model that answers questions is holding. The two want the same memory, and a file that
	// is being read cannot use what an idle answer is keeping warm.
	//
	// The worker calls it before a job touches the file, and the request that queued the job
	// never does — an upload's answer in the browser should not wait on a request to another
	// process. It is asked and not demanded: a refusal is a Warn and the job runs anyway, because
	// the alternative is refusing somebody's file over a memory arrangement that is only a
	// convenience. Nil means there is nobody to ask, which is what external mode is.
	RoomForIngest func(ctx context.Context) error

	// Chats, Models and Search are the answer path. All three are optional: a
	// Service wired for ingest alone is a legitimate build, and Ask says plainly
	// that this build cannot answer rather than failing somewhere underneath.
	//
	// Search is retrieve's own interface rather than a copy of it here. The store
	// satisfies it, retrieve declared it, and a second declaration of the same shape
	// in app would be two definitions of one thing.
	Chats     Chats
	Models    ModelsFor
	Search    retrieve.Corpus
	Originals string // where kept originals live; created if absent
	Assets    string // where derived files live; created if absent
	Logger    *slog.Logger
}

// Service owns the ingest queue and the use cases around it.
type Service struct {
	store     Store
	settings  Settings
	processor ProcessorFor
	embedder  EmbedderFor
	chats     Chats
	models    ModelsFor
	search    retrieve.Corpus
	originals Originals
	assetsDir string
	log       *slog.Logger
	jobs      chan job
	stop      chan struct{}
	done      chan struct{}
	jobCtx    context.Context
	jobCancel context.CancelFunc

	// work is what is happening to which source, so stopping one file leaves every
	// other upload alone. It holds the runs in flight and the ids still waiting in the
	// queue, because a channel has no contents to look at and "is this one still
	// merely queued?" has to be answerable to stop it.
	workMu sync.Mutex
	work   work

	release func(ctx context.Context) error
	room    func(ctx context.Context) error
	// idle is guarded by idleMu because it is written by the worker and cleared by the goroutine
	// the timer fires its callback in — the one place in this file where two goroutines own a
	// field, and the reason for the lock rather than a comment saying "only the worker touches
	// this". That comment would be false the day it needed a timer.
	idleMu sync.Mutex
	idle   *time.Timer
}

// New checks the wiring. Nothing is created on disk yet.
func New(opts Options) (*Service, error) {
	switch {
	case opts.Store == nil:
		return nil, errors.New("app: Store is required")
	case opts.Settings == nil:
		return nil, errors.New("app: Settings is required")
	case opts.Processor == nil:
		return nil, errors.New("app: Processor is required")
	case opts.Embedder == nil:
		// No opt-out: "ready" means searchable, so a Service that
		// cannot embed would have to report a state that does not exist.
		return nil, errors.New("app: Embedder is required")
	case opts.Originals == "":
		return nil, errors.New("app: Originals directory is required")
	case opts.Assets == "":
		return nil, errors.New("app: Assets directory is required")
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	return &Service{
		store:     opts.Store,
		settings:  opts.Settings,
		processor: opts.Processor,
		embedder:  opts.Embedder,
		release:   opts.ReleaseEmbedder,
		room:      opts.RoomForIngest,
		chats:     opts.Chats,
		models:    opts.Models,
		search:    opts.Search,
		originals: Originals{Dir: opts.Originals},
		assetsDir: opts.Assets,
		log:       log,
		jobs:      make(chan job, queueDepth),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		work:      work{running: map[string]*run{}, queued: map[string]bool{}},
	}, nil
}

// Start closes out work left by a previous run and launches the ingest worker.
// Jobs do not run on the caller's context: an upload's request ending, or the
// process shutting down, must not abandon a half-rasterised PDF.
func (s *Service) Start(ctx context.Context) error {
	// What an older build wrote goes before anything can read it. ADR 0003 left its
	// recordings in the database as rows no binary defined; this build defines the kind,
	// so those thirty-second clips would rank beside the fifteen-second ones it cuts and
	// look like the same thing. Their files follow once the rows are committed.
	dropped, orphans, err := s.store.PurgeLegacyAudio(ctx)
	if err != nil {
		return err
	}
	s.collect(orphans)
	if dropped > 0 {
		s.log.Info("discarded clips sliced by an older build",
			"segments", dropped, "sources", len(orphans))
	}

	n, err := s.store.FailStaleSources(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.Info("closed out interrupted sources", "count", n)
	}

	s.jobCtx, s.jobCancel = context.WithCancel(context.Background())
	go s.worker()
	return nil
}

// Close stops dequeuing and waits for the job in flight — including its writes.
//
// It deliberately does not cancel that job: cancelling the context it ingests
// with would discard the results of work that was about to finish, leaving a
// source "processing" forever. The job deadline is what bounds the wait.
func (s *Service) Close() {
	if s.done == nil {
		return
	}
	// A release still pending is not worth running: it is a courtesy to a machine that is
	// shutting down, and it would be talking to a server about to be stopped anyway.
	s.cancelIdleRelease()

	select {
	case <-s.stop:
		// Already closed.
	default:
		close(s.stop)
	}
	<-s.done
	if s.jobCancel != nil {
		s.jobCancel()
		s.jobCancel = nil
	}
}

// ── notebooks ────────────────────────────────────────────────────

func (s *Service) CreateNotebook(ctx context.Context, name string) (domain.Notebook, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.Notebook{}, fmt.Errorf("%w: a notebook needs a name", ErrInvalid)
	}
	return s.store.CreateNotebook(ctx, name)
}

func (s *Service) RenameNotebook(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: a notebook needs a name", ErrInvalid)
	}
	return s.store.RenameNotebook(ctx, id, name)
}

func (s *Service) DeleteNotebook(ctx context.Context, id string) error {
	orphans, err := s.store.DeleteNotebook(ctx, id)
	if err != nil {
		return err
	}
	s.collect(orphans)
	return nil
}

func (s *Service) GetNotebook(ctx context.Context, id string) (domain.Notebook, error) {
	return s.store.GetNotebook(ctx, id)
}

func (s *Service) ListNotebooks(ctx context.Context) ([]domain.Notebook, error) {
	return s.store.ListNotebooks(ctx)
}

// ── sources ──────────────────────────────────────────────────────

// AddUpload stores an uploaded file and queues it for ingest. It returns as soon
// as the bytes are on disk: rasterising a long PDF takes minutes, and the caller
// polls the source's status.
//
// Dedupe is by content hash. A file the notebook already has costs one row
// insert and no processing; a file another notebook has reuses the stored
// original, its rasters and its segments. The shared row keeps the name it was
// first offered under — labelling the same file differently per notebook needs
// an alias on the membership, which is not decided yet.
func (s *Service) AddUpload(ctx context.Context, notebookID string, r io.Reader, filename string) (domain.Source, error) {
	if err := s.requireNotebook(ctx, notebookID); err != nil {
		return domain.Source{}, err
	}

	cfg := s.settings.Get()
	saved, err := s.originals.Save(r, filename, cfg.MaxFileBytes())
	if err != nil {
		return domain.Source{}, err
	}

	source := domain.Source{
		ID:           saved.SHA256,
		Name:         filename,
		Kind:         saved.Kind,
		SHA256:       saved.SHA256,
		Bytes:        saved.Bytes,
		OriginalPath: saved.Path,
		Status:       domain.StatusProcessing,
		AddedAt:      time.Now().Unix(),
	}

	stored, added, err := s.store.AddSource(ctx, notebookID, source)
	if err != nil {
		return domain.Source{}, err
	}

	switch {
	case added:
		return stored, s.enqueue(job{sourceID: stored.ID})
	case stored.Status == domain.StatusFailed:
		// Re-offering a file that failed is the cheapest way to retry it.
		s.log.Info("re-queueing a failed source", "source", stored.ID, "was", stored.Detail)
		return s.requeue(ctx, stored, false)
	default:
		return stored, nil
	}
}

// ReingestSource puts a source back through the pipeline from its kept original:
// text extracted, pages rasterised, chunks split and vectors written again, as if
// the file had just been offered. One action covers the two reasons a source gets
// redone — it failed the first time, and the world under it changed (a new
// embedding model or --pooling, a new prompt scheme, new chunk settings).
//
// Re-running from the file rather than only re-embedding is the user's choice and
// the safer one: derived data is replaced as a whole, so a source can never end up
// half from one chunker and half from another. `PutSegments` already deletes the
// source's old rows, which deletes their vectors with them, and the source spends
// the whole job `processing` — so it is out of retrieval scope while it is
// incomplete, which is what `ready` promises.
//
// Sources are content-addressed and shared between notebooks, so this refreshes
// the one copy every notebook citing this file reads from.
func (s *Service) ReingestSource(ctx context.Context, id string) (domain.Source, error) {
	source, err := s.store.GetSource(ctx, id)
	if err != nil {
		return domain.Source{}, err
	}
	if source.Status == domain.StatusProcessing {
		// A job is already writing this source's rows. Queuing a second one would
		// interleave two pipelines over one set of segments.
		return source, fmt.Errorf("%w: this source is already being rebuilt", ErrInvalid)
	}
	return s.requeue(ctx, source, false)
}

// requeue marks a source as working again, puts it back in the queue, and answers with
// the source as it now is. `resume` is what the worker is told to do with it when its
// turn comes: build the file from nothing, or finish what a stop left half done.
//
// The returned value matters: it is the body the UI writes into the row, and a row
// still saying "failed" after a retry that started is worse than no answer at all
// — the UI decides whether to keep polling from that status, so a stale one stops
// the poll and the row never updates by itself.
//
// The status is written before enqueueing on purpose. A source re-ingests by
// deleting its segments, which deletes their vectors with them, so a `ready`
// source sitting in the queue is a source answering questions with nothing while the UI says it is searchable.

// ResumeSource puts a stopped source back on the queue without throwing anything away.
//
// What it can continue is bounded by what the store actually holds, and the bound is
// worth stating plainly: a run stopped during embedding leaves every chunk of the file
// sitting there with its text, its ordinal and its page image, and only the vectors
// missing — so that is all a resume does, which on a long upload is nearly the whole
// job. A run stopped mid-extraction leaves nothing, because extraction commits a
// source's chunks in one transaction at the end; for that file the only honest resume
// is the whole pipeline again, and that is what it becomes.
func (s *Service) ResumeSource(ctx context.Context, id string) (domain.Source, error) {
	source, err := s.store.GetSource(ctx, id)
	if err != nil {
		return domain.Source{}, err
	}

	switch source.Status {
	case domain.StatusStopped:
	case domain.StatusProcessing:
		return domain.Source{}, fmt.Errorf("%w: this source is already being built", ErrInvalid)
	default:
		return domain.Source{}, fmt.Errorf("%w: only a stopped source can be resumed", ErrInvalid)
	}

	return s.requeue(ctx, source, true)
}

func (s *Service) requeue(ctx context.Context, source domain.Source, resume bool) (domain.Source, error) {
	// A resume keeps the warnings the first run recorded, because the stage that
	// produced them may be skipped this time — and a finding nobody recomputes is a
	// finding quietly deleted. A rerun starts from nothing, so it starts from no
	// findings either.
	var findings []domain.Finding
	if resume {
		findings = source.Findings
	}

	if err := s.store.SetSourceStatus(ctx, source.ID, domain.StatusProcessing, "", findings); err != nil {
		return source, err
	}
	if err := s.enqueue(job{sourceID: source.ID, resume: resume}); err != nil {
		// Undo, or the row waits at "processing" for the next start's sweep.
		if restore := s.store.SetSourceStatus(ctx, source.ID, source.Status, source.Detail, source.Findings); restore != nil {
			s.log.Warn("could not restore status after a refused retry",
				"source", source.ID, "status", source.Status, "err", restore)
		}
		return source, err
	}

	// Mirrors the row just written, rather than reading it back: the queue worker
	// may already have moved it on, and either way the caller only needs to know
	// that work has started.
	source.Status = domain.StatusProcessing
	source.Detail = ""
	source.Findings = nil
	return source, nil
}

// AttachNotebook puts a source the user already uploaded into a notebook.
func (s *Service) AttachSource(ctx context.Context, notebookID, sourceID string) error {
	if err := s.requireNotebook(ctx, notebookID); err != nil {
		return err
	}
	return s.store.AttachSource(ctx, notebookID, sourceID)
}

// DetachSource removes a source from a notebook, keeping the file and every
// derived artefact: another notebook may be citing them.
// DetachSource removes a source from one notebook, and deletes it outright when
// that was the only notebook using it. The two are one action because a file no
// notebook lists is not waiting for anything; the shared-by-hash design means the
// usual case is the quiet one, where the source and its rasters stay for the
// notebooks that still cite them.
func (s *Service) DetachSource(ctx context.Context, notebookID, sourceID string) error {
	orphans, err := s.store.DetachSource(ctx, notebookID, sourceID)
	if err != nil {
		return err
	}
	s.collect(orphans)
	return nil
}

// collect unlinks the files the orphans owned. It runs after the transaction
// that dropped their rows committed, and that order is the whole design: a file
// deleted while its citations still resolve takes an answer's evidence with it,
// whereas a file left behind by a crash between commit and unlink is only disk —
// the failure mode this app has had all along.
//
// A failed unlink is logged and skipped. Nothing here can be retried by the user,
// so nothing here may fail an action that already worked.
func (s *Service) collect(orphans []domain.Orphan) {
	for _, o := range orphans {
		// The subtree before the listed files: a raster written by an ingest that
		// died before its rows exist appears in no directory listing the database
		// can give, and the directory named after the source hash is the only
		// thing that can find it.
		s.removeTree(s.assetsDir, o.ID)
		for _, p := range o.Assets {
			s.removeFile(s.assetsDir, p)
		}
		s.removeFile(s.originals.Dir, o.Original)
		s.log.Info("collected a source no notebook uses",
			"source", o.ID, "name", o.Name, "files", len(o.Assets)+1)
	}
}

// removeFile deletes one path, and only if it lies inside root. The path came
// from the database, which is the one place a stale row, an older build's layout
// or a hand-edited file can point somewhere the user never uploaded. This is the
// only code in libris that deletes a person's bytes, so it refuses rather than
// trusts — the same rule DirSink applies to names.
func (s *Service) removeFile(root, path string) {
	if !under(root, path) {
		if path != "" {
			s.log.Warn("refusing to delete a file outside the data directory", "path", path)
		}
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("could not delete a file", "path", path, "err", err)
	}
}

// removeTree deletes a directory the app owns outright, named by source id. The
// id is content-addressed, so it is checked as one: a row whose id is not the
// 64 hex characters a hash is gets the refusal, not a recursive delete.
func (s *Service) removeTree(root, id string) {
	if !hexID(id) {
		if id != "" {
			s.log.Warn("refusing to delete a directory named by a non-hash id", "id", id)
		}
		return
	}
	dir := filepath.Join(root, id)
	if !under(root, dir) {
		return
	}
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("could not delete a source's directory", "dir", dir, "err", err)
	}
}

// under reports whether an absolute path lies inside root.
func under(root, path string) bool {
	if root == "" || path == "" || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// hexID is the shape of a sha256 hex digest, which is the only thing allowed to
// name a directory under the asset root.
func hexID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (s *Service) GetSource(ctx context.Context, id string) (domain.Source, error) {
	return s.store.GetSource(ctx, id)
}

func (s *Service) ListSources(ctx context.Context, notebookID string) ([]domain.Source, error) {
	if err := s.requireNotebook(ctx, notebookID); err != nil {
		return nil, err
	}
	return s.store.ListSources(ctx, notebookID)
}

// ListSegmentsPage is the chunk viewer's read: the window it can show, and what the whole
// file holds.
func (s *Service) ListSegmentsPage(ctx context.Context, sourceID string, offset, limit int) ([]domain.Segment, map[domain.Kind]int, error) {
	return s.store.ListSegmentsPage(ctx, sourceID, offset, limit)
}

// GetAsset resolves an asset name for serving. The path comes from the database
// rather than the request, which is what keeps the file server honest.
func (s *Service) GetAsset(ctx context.Context, id string) (domain.Asset, error) {
	return s.store.GetAsset(ctx, id)
}

func (s *Service) requireNotebook(ctx context.Context, id string) error {
	if _, err := s.store.GetNotebook(ctx, id); err != nil {
		return err
	}
	return nil
}

// work is the queue's idea of what is happening: the runs in flight keyed by source,
// and the ids still waiting their turn. Both are needed to stop a file — a run can be
// cancelled, a queued one can only be marked, because it has not begun.
type work struct {
	running map[string]*run
	queued  map[string]bool
}

// job is one source waiting in the queue, and whether its turn is a first run or a
// resume. The queue carries this rather than a bare id because "build this file" and
// "finish this file" are different promises, and a channel has no room to remember
// which one it was sent.
type job struct {
	sourceID string
	resume   bool
}

// run is one source's ingest in flight: how to cancel it, and whether the cancellation
// came from a person rather than from a shutdown. That flag is the whole difference
// between a row that reads `stopped` and one left processing for the next start's
// sweep to close out as failed.
type run struct {
	cancel  context.CancelFunc
	stopped atomic.Bool
}

// begin takes a queued id and starts its run, in one step: a stop landing between the
// two would otherwise find the source neither queued nor running, and answer "this
// source is not being built" about work that is happening.
func (s *Service) begin(sourceID string, r *run) {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	delete(s.work.queued, sourceID)
	s.work.running[sourceID] = r
}

// end forgets a run that has finished.
func (s *Service) end(sourceID string) {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	delete(s.work.running, sourceID)
}

// stopping marks a queued source stopped in the database, and answers whether it was
// queued at all. The worker reads the row before it starts, so a source marked here is
// skipped rather than built — and its chunks, which no stage wrote yet, stay absent.
func (s *Service) stopping(ctx context.Context, sourceID string) bool {
	s.workMu.Lock()
	queued := s.work.queued[sourceID]
	if queued {
		delete(s.work.queued, sourceID)
	}
	s.workMu.Unlock()

	if !queued {
		return false
	}
	s.setStatus(ctx, sourceID, domain.StatusStopped, "stopped before it started", nil)
	return true
}

// recordStopped closes a cancelled run's row. It cannot use the run's own context —
// that is what was just cancelled — so it writes on one detached from it, with a
// deadline, because detached must not mean forever.
func (s *Service) recordStopped(sourceID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.jobCtx), 10*time.Second)
	defer cancel()
	s.setStatus(ctx, sourceID, domain.StatusStopped, "stopped", nil)
}

// StopSource cancels the work in flight for one source.
//
// Everything the run had already written stays: chunks, page rasters, clips, and the
// vectors written so far. None of it is citable while the row is `stopped` — the scan
// asks for `status = 'ready'` — so a stopped source is inert rather than half
// searchable. Resume picks the rest of it up where it stopped; Rerun throws it away
// and rebuilds.
func (s *Service) StopSource(ctx context.Context, id string) (domain.Source, error) {
	source, err := s.store.GetSource(ctx, id)
	if err != nil {
		return domain.Source{}, err
	}

	s.workMu.Lock()
	r := s.work.running[id]
	s.workMu.Unlock()

	if r != nil {
		// The flag first, then the cancellation: the run reads the flag to tell a stop
		// it was asked for from a shutdown it was caught in.
		r.stopped.Store(true)
		r.cancel()
		return source, nil
	}
	if s.stopping(ctx, id) {
		return source, nil
	}
	return source, fmt.Errorf("%w: this source is not being built", ErrInvalid)
}

func (s *Service) enqueue(j job) error {
	select {
	case s.jobs <- j:
		s.workMu.Lock()
		s.work.queued[j.sourceID] = true
		s.workMu.Unlock()
		return nil
	default:
		return fmt.Errorf("%w: %d uploads waiting", ErrBusy, len(s.jobs))
	}
}

// ── worker ───────────────────────────────────────────────────────

func (s *Service) worker() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case j := <-s.jobs:
			// The grace period restarts on work, not on completion: the point is that an
			// upload has arrived, which is known the moment it is taken off the queue.
			s.cancelIdleRelease()

			r := &run{}
			// jobCtx stays the parent: shutting the process down must still reach a run in
			// flight, and that cancellation is the one a run must not mistake for a person
			// pressing stop.
			runCtx, stopRun := context.WithCancel(s.jobCtx)
			r.cancel = stopRun
			s.begin(j.sourceID, r)
			s.makeRoom(runCtx)

			ctx, cancel := context.WithTimeout(runCtx, jobTimeout)
			s.ingest(ctx, j.sourceID, r, j.resume)
			cancel()
			stopRun()
			s.end(j.sourceID)

			s.armIdleRelease()
		}
	}
}

// roomTimeout bounds the ask below, and it is short because what it asks for is a courtesy: a
// router that has stopped answering is a reason to start the job without the memory, not a
// reason to hold every upload behind it. It is far longer than a loopback round trip and far
// shorter than loading a model — which is the work the ask is trying to make room for.
const roomTimeout = 10 * time.Second

// makeRoom asks whoever runs the model server to put the answering model down, before this job's
// ffmpeg, poppler and embedding passes want the device it is sitting on.
//
// Unlike the idle release, this waits. The answer to the ask is the memory the job is about to
// use, so a worker that fired it off and moved on would start decoding before the device had been
// handed back — and the drain the router performs is the point of doing it here rather than after.
//
// Once per job, not once per upload batch: the first job frees the model, and every job after it
// finds it already down and asks only the cheap question of whether that is still true.
func (s *Service) makeRoom(ctx context.Context) {
	if s.room == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, roomTimeout)
	defer cancel()
	if err := s.room(ctx); err != nil {
		// A Warn because the symptom of this failing is a machine that runs out of memory halfway
		// through a big PDF, with nothing in the interface to explain why.
		s.log.Warn("could not make room for ingest; running anyway", "err", err)
	}
}

// armIdleRelease starts the clock on the embedding model's hold on the card, and does nothing
// while more uploads are waiting. Called by the worker between jobs, which is the one moment
// "no upload is running" is knowable without a second source of truth.
func (s *Service) armIdleRelease() {
	if s.release == nil || len(s.jobs) > 0 {
		return
	}
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	if s.idle != nil {
		s.idle.Stop()
	}
	// Read here, by the goroutine that arms the clock, and handed to the callback: a timer that
	// went back for the number when it fired would be reading a value that belongs to whatever
	// happened to the world in between.
	waited := embedIdleWindow
	s.idle = time.AfterFunc(waited, func() { s.releaseEmbedder(waited) })
}

// cancelIdleRelease undoes a pending release: an upload arrived, or the process is going away.
func (s *Service) cancelIdleRelease() {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
}

// releaseEmbedder tells whoever runs the model server that the uploads are over for now.
//
// It runs on the timer's goroutine, deliberately: this is a request to another process, and a
// worker that blocks on it would be stopping uploads in order to ask for memory back on behalf of
// those same uploads. A failure is a Warn rather than a shrug because the visible symptom of one
// is a machine that runs out of memory later, with nothing in the interface to explain it.
// `waited` is how long the queue stayed empty on the way here — the window as it was when the
// clock was armed, which is the number worth printing if this is ever the line that explains a
// machine's memory.
func (s *Service) releaseEmbedder(waited time.Duration) {
	s.idleMu.Lock()
	s.idle = nil
	release := s.release
	s.idleMu.Unlock()

	if release == nil {
		return
	}
	// The ingest context, because a release should not outlive the work it was waiting for:
	// Close cancels it, and a request still in flight to a server being stopped is noise.
	if err := release(s.jobCtx); err != nil {
		s.log.Warn("could not hand the memory back after ingest went idle", "err", err)
		return
	}
	s.log.Info("ingest idle; the embedding model is off the device", "idle", waited.String())
}

// resumeMissing embeds the chunks a stopped run never got vectors for, and reports
// whether it handled the job. It declines only one case — a source whose store holds no
// chunks at all — because there is then nothing to continue from, and the caller's full
// pipeline is the right answer.
//
// It reads the store rather than any note the stopped run left behind, so a resume works
// after a restart just as well as right after the stop: the rows are the record of what
// was done, which is the same thing the search path trusts.
func (s *Service) resumeMissing(ctx context.Context, source domain.Source, settle func(domain.SourceStatus, string, []domain.Finding)) bool {
	missing, err := s.store.SegmentsWithoutEmbeddings(ctx, source.ID)
	if err != nil {
		settle(domain.StatusFailed, "could not read what was left to embed: "+err.Error(), nil)
		return true
	}

	if len(missing) == 0 {
		stored, err := s.store.ListSegments(ctx, source.ID)
		if err != nil {
			settle(domain.StatusFailed, "could not read this source's chunks: "+err.Error(), nil)
			return true
		}
		if len(stored) == 0 {
			return false
		}
		// Every chunk already has a vector: the run finished the work and died before
		// recording that it had. Nothing is recomputed; the row is simply closed.
		settle(domain.StatusReady, "", source.Findings)
		return true
	}

	start := time.Now()
	if err := s.embed(ctx, source.Name, missing); err != nil {
		s.log.Warn("resume embedding failed", "source", source.ID, "segments", len(missing), "err", err)
		settle(domain.StatusFailed, err.Error(), source.Findings)
		return true
	}
	settle(domain.StatusReady, "", source.Findings)
	s.log.Info("resume done", "source", source.ID, "embedded", len(missing),
		"seconds", time.Since(start).Round(time.Second))
	return true
}

// ingest runs the pipeline for one source and records how it ended. A failure
// here is data, never a crash: the batch continues and the source says why.
func (s *Service) ingest(ctx context.Context, sourceID string, r *run, resume bool) {
	source, err := s.store.GetSource(ctx, sourceID)
	if err != nil {
		s.log.Warn("ingest skipped", "source", sourceID, "err", err)
		return
	}
	if source.Status != domain.StatusProcessing {
		// Stopped between the queue and this moment. The stop marked the row when it
		// arrived; there is nothing here to build and nothing to record.
		s.log.Info("ingest skipped: the source is not waiting to be built",
			"source", sourceID, "status", source.Status)
		return
	}

	// settle writes an outcome, unless a person stopped this run on the way — in which
	// case it writes the stop instead. Every branch asks it rather than the store
	// because a cancelled context makes the writes along the way fail in exactly the
	// shape of a real failure, and a file someone stopped must not read as a file that
	// broke.
	settle := func(status domain.SourceStatus, detail string, findings []domain.Finding) {
		if r.stopped.Load() {
			s.recordStopped(sourceID)
			return
		}
		s.setStatus(ctx, sourceID, status, detail, findings)
	}

	// A resume takes the short path when there is something to resume from, and falls
	// through to the whole pipeline when there is not — which is the honest answer for
	// a run stopped before extraction committed its chunks.
	if resume && s.resumeMissing(ctx, source, settle) {
		return
	}

	cfg := s.settings.Get()
	processor, err := s.processor(cfg, ingest.DirSink{Root: s.assetsDir})
	if err != nil {
		settle(domain.StatusFailed, "could not start ingest: "+err.Error(), nil)
		return
	}

	start := time.Now()
	res, err := processor.Process(ctx, source.OriginalPath)
	switch {
	case errors.Is(err, context.Canceled):
		if r.stopped.Load() {
			settle(domain.StatusStopped, "stopped", nil)
			s.log.Info("ingest stopped", "source", sourceID,
				"seconds", time.Since(start).Round(time.Millisecond))
			return
		}
		// Only a killed process gets here. Leave the row processing so the next
		// start's sweep marks it failed and the user can retry.
		s.log.Info("ingest interrupted", "source", sourceID)
		return
	case err != nil:
		s.log.Warn("ingest failed", "source", sourceID, "err", err)
		settle(domain.StatusFailed, err.Error(), nil)
		return
	}

	// The notebook may have been deleted while this job ran, and with it the last
	// notebook using this file — in which case its rows are already gone and its
	// files were just re-made by the rasters below. Nothing may be written for a
	// source that no longer exists, so the job cleans up after itself instead.
	if _, err := s.store.GetSource(ctx, sourceID); errors.Is(err, domain.ErrNotFound) {
		s.removeTree(s.assetsDir, sourceID)
		s.log.Info("ingest discarded: the source was deleted while indexing", "source", sourceID)
		return
	}

	// Assets first: segments reference them by id and the schema enforces it.
	var assets []domain.Asset
	seen := map[string]bool{}
	for _, seg := range res.Segments {
		if seg.Asset != nil && !seen[seg.Asset.ID] {
			seen[seg.Asset.ID] = true
			assets = append(assets, *seg.Asset)
		}
	}
	if err := s.store.PutAssets(ctx, sourceID, assets); err != nil {
		settle(domain.StatusFailed, "could not store assets: "+err.Error(), nil)
		return
	}
	if err := s.store.PutSegments(ctx, sourceID, res.Segments); err != nil {
		settle(domain.StatusFailed, "could not store segments: "+err.Error(), nil)
		return
	}

	if res.Unsupported != "" {
		settle(domain.StatusUnsupported, res.Unsupported, res.Findings)
		s.log.Info("source unsupported", "source", sourceID, "reason", res.Unsupported,
			"findings", res.Findings, "seconds", time.Since(start).Round(time.Millisecond))
		return
	}

	// Embedding is part of ingest, not a stage a caller opts into. Until it
	// succeeds this source is not `ready`, because `ready` is the promise that
	// asking a question will search this source.
	if err := s.embed(ctx, source.Name, res.Segments); err != nil {
		s.log.Warn("embedding failed", "source", sourceID, "segments", len(res.Segments), "err", err)
		// The chunks stay, so the source is still readable and inspectable; what
		// is missing is retrievability, and the detail says which server to fix.
		settle(domain.StatusFailed, err.Error(), res.Findings)
		return
	}

	settle(domain.StatusReady, "", res.Findings)
	// Findings are logged even on a Ready row, because "ready" is a promise about retrieval and
	// says nothing about a PDF whose page images were skipped: the row that looks fine and
	// answers no visual question is explained by this line and by nothing else.
	s.log.Info("ingest done",
		"source", sourceID, "kind", source.Kind, "segments", len(res.Segments),
		"assets", len(assets), "embedded", "yes", "findings", res.Findings,
		"seconds", time.Since(start).Round(time.Millisecond))
}

// embed turns segments into vectors and stores them.
func (s *Service) embed(ctx context.Context, title string, segments []domain.Segment) error {
	if len(segments) == 0 {
		return nil
	}

	embedder, err := s.embedder(s.settings.Get())
	if err != nil {
		return fmt.Errorf("could not start the embedder: %w", err)
	}

	// Every vector is written the moment its batch is done, which is what lets the
	// source row count how far the wait has got: an hour of a recording is a quarter
	// of an hour of vectors arriving one batch at a time, and until now the row said
	// nothing about any of it until the last one.
	//
	// The sink is the write path when the embedder uses it. If one does not — a hosted
	// API that has never heard of the callback — the vectors are still the source's
	// promise, and the write after the call is where that promise is kept whole.
	var sunk atomic.Bool
	vectors, err := embedder.Embed(ctx, title, segments, func(batch []domain.Embedding) error {
		sunk.Store(true)
		return s.store.PutEmbeddings(ctx, batch)
	})
	if err != nil {
		return err
	}
	if len(vectors) != len(segments) {
		return fmt.Errorf("embedder returned %d vectors for %d segments",
			len(vectors), len(segments))
	}
	if sunk.Load() {
		return nil
	}
	return s.store.PutEmbeddings(ctx, vectors)
}

// setStatus records an outcome. Its own failure is logged, not propagated: the
// only thing left to do is keep serving.
func (s *Service) setStatus(ctx context.Context, id string, status domain.SourceStatus, detail string, findings []domain.Finding) {
	if err := s.store.SetSourceStatus(ctx, id, status, detail, findings); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Warn("could not record source status", "source", id, "status", status, "err", err)
	}
}
