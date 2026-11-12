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
	"strings"
	"time"

	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/ingest"
)

// Store is what the use cases need from persistence. Declared by the consumer
// and narrow: the sqlite package may grow methods freely without widening this.
type Store interface {
	CreateNotebook(context.Context, string) (domain.Notebook, error)
	RenameNotebook(context.Context, string, string) error
	DeleteNotebook(context.Context, string) error
	GetNotebook(context.Context, string) (domain.Notebook, error)
	ListNotebooks(context.Context) ([]domain.Notebook, error)

	AddSource(context.Context, string, domain.Source) (domain.Source, bool, error)
	AttachSource(context.Context, string, string) error
	DetachSource(context.Context, string, string) error
	GetSource(context.Context, string) (domain.Source, error)
	ListSources(context.Context, string) ([]domain.Source, error)
	SetSourceStatus(context.Context, string, domain.SourceStatus, string, []string) error

	PutAssets(context.Context, string, []domain.Asset) error
	PutSegments(context.Context, string, []domain.Segment) error
	PutEmbeddings(context.Context, []domain.Embedding) error
	ReplaceEmbeddings(context.Context, string, []domain.Embedding) error
	ListSegments(context.Context, string) ([]domain.Segment, error)
	GetAsset(context.Context, string) (domain.Asset, error)

	FailStaleSources(context.Context) (int64, error)
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
// slice of itself, which is the failure ADR 0004 exists to prevent.
type Embedder interface {
	// Embed vectors a batch of segments belonging to one document. title is that
	// document's display name, used by models that want their inputs steered; an
	// embedder that has no use for it ignores it.
	Embed(ctx context.Context, title string, segments []domain.Segment) ([]domain.Embedding, error)
}

// EmbedderFor builds an embedder for one configuration. It is a factory for the
// same reason Processor is: settings can change while the process runs, and a
// job must use the server the user has configured now, not the one that was set
// when libris started.
type EmbedderFor func(cfg config.Config) (Embedder, error)

// ProcessorFor builds an ingestor for one configuration.
type ProcessorFor func(config.Config, ingest.Sink) (Ingestor, error)

// queueDepth is the number of uploads that may wait. Ingest is deliberately
// serial — poppler, ffmpeg and the model server all want RAM — so a burst of
// uploads queues rather than thrashing the machine. The cap exists so a runaway
// client cannot grow memory without bound; it is not tuned.
const queueDepth = 64

// jobTimeout bounds one ingest end to end. Nothing else bounds it: poppler and
// ffmpeg are outside our process, and Close waits for the job in flight, so
// without a deadline a wedged helper could hold shutdown open forever. Generous
// rather than tuned — a large scanned PDF is the case it must not interrupt.
const jobTimeout = 30 * time.Minute

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
	originals Originals
	assetsDir string
	log       *slog.Logger
	jobs      chan job
	stop      chan struct{}
	done      chan struct{}
	jobCtx    context.Context
	jobCancel context.CancelFunc
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
		// No opt-out: "ready" means searchable (ADR 0004), so a Service that
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
		originals: Originals{Dir: opts.Originals},
		assetsDir: opts.Assets,
		log:       log,
		jobs:      make(chan job, queueDepth),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}, nil
}

// Start closes out work left by a previous run and launches the ingest worker.
// Jobs do not run on the caller's context: an upload's request ending, or the
// process shutting down, must not abandon a half-rasterised PDF.
func (s *Service) Start(ctx context.Context) error {
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
	return s.store.DeleteNotebook(ctx, id)
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

// RetrySource re-queues a source that gave up. A failed embedding usually means
// the embedding server was not running when the file arrived, and asking the user
// to find and re-upload the file to fix that is a bad trade.
//
// Only a failed source is retried. A ready source is complete, and re-ingesting
// one deletes its segments — and their vectors with them — so it would answer
// questions with nothing while the UI still said "ready": the exact window ADR
// 0004 exists to close. Re-doing a source on purpose (after changing chunk
// settings, say) deserves an action of its own rather than a side effect of
// "retry". Re-running ingest from the beginning rather than only the embedding
// half is deliberate: segments already stored may come from a different chunker,
// and a half-refreshed source is the state this exists to avoid.
func (s *Service) RetrySource(ctx context.Context, id string) (domain.Source, error) {
	source, err := s.store.GetSource(ctx, id)
	if err != nil {
		return domain.Source{}, err
	}
	if source.Status != domain.StatusFailed {
		return source, nil // ready, processing or unsupported: nothing to redo
	}
	return s.requeue(ctx, source, false)
}

// ReembedSource re-vectors a ready source from the chunks it already has. It does
// not re-read the file: no rasterising, no text extraction, and the kept original
// is not touched — only the vectors are replaced.
//
// This is what a corpus needs when the embedding side of the pipeline changes
// identity underneath it: a new model, a different --pooling, or a new prompt
// scheme (index.PromptScheme). The store records all three beside every vector,
// so a corpus can be read back and asked which sources predate the change, which
// is exactly the list this is for.
func (s *Service) ReembedSource(ctx context.Context, id string) (domain.Source, error) {
	source, err := s.store.GetSource(ctx, id)
	if err != nil {
		return domain.Source{}, err
	}
	if source.Status != domain.StatusReady {
		// A failed or unsupported source has no complete vector set to replace, and
		// a processing one is being written by a job already in the queue. Retry is
		// the door for the first; waiting is the answer to the second.
		return source, fmt.Errorf("%w: a %s source has no vectors to re-embed", ErrInvalid, source.Status)
	}
	return s.requeue(ctx, source, true)
}

// requeue marks a source as working again, puts it back in the queue, and answers
// with the source as it now is.
//
// The returned value matters: it is the body the UI writes into the row, and a row
// still saying "failed" after a retry that started is worse than no answer at all
// — the UI decides whether to keep polling from that status, so a stale one stops
// the poll and the row never updates by itself.
//
// The status is written before enqueueing on purpose. A source re-ingests by
// deleting its segments, which deletes their vectors with them, so a `ready`
// source sitting in the queue is a source answering questions with nothing while
// the UI says it is searchable.
func (s *Service) requeue(ctx context.Context, source domain.Source, reembed bool) (domain.Source, error) {
	if err := s.store.SetSourceStatus(ctx, source.ID, domain.StatusProcessing, "", nil); err != nil {
		return source, err
	}
	if err := s.enqueue(job{sourceID: source.ID, reembed: reembed}); err != nil {
		// Undo, or the row waits at "processing" for the next start's sweep.
		if restore := s.store.SetSourceStatus(ctx, source.ID, source.Status, source.Detail, source.Warnings); restore != nil {
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
	source.Warnings = nil
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
func (s *Service) DetachSource(ctx context.Context, notebookID, sourceID string) error {
	return s.store.DetachSource(ctx, notebookID, sourceID)
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

func (s *Service) ListSegments(ctx context.Context, sourceID string) ([]domain.Segment, error) {
	return s.store.ListSegments(ctx, sourceID)
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

func (s *Service) enqueue(j job) error {
	select {
	case s.jobs <- j:
		return nil
	default:
		return fmt.Errorf("%w: %d uploads waiting", ErrBusy, len(s.jobs))
	}
}

// ── worker ───────────────────────────────────────────────────────

// job is one unit of queued work. Both kinds are keyed by source, because the
// queue's job is to bound how much runs at once next to the model servers — the
// difference is only whether the pipeline reads the file again.
type job struct {
	sourceID string
	reembed  bool
}

func (s *Service) worker() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case j := <-s.jobs:
			ctx, cancel := context.WithTimeout(s.jobCtx, jobTimeout)
			if j.reembed {
				s.reembed(ctx, j.sourceID)
			} else {
				s.ingest(ctx, j.sourceID)
			}
			cancel()
		}
	}
}

// ingest runs the pipeline for one source and records how it ended. A failure
// here is data, never a crash: the batch continues and the source says why.
func (s *Service) ingest(ctx context.Context, sourceID string) {
	source, err := s.store.GetSource(ctx, sourceID)
	if err != nil {
		s.log.Warn("ingest skipped", "source", sourceID, "err", err)
		return
	}

	cfg := s.settings.Get()
	processor, err := s.processor(cfg, ingest.DirSink{Root: s.assetsDir})
	if err != nil {
		s.setStatus(ctx, sourceID, domain.StatusFailed, "could not start ingest: "+err.Error(), nil)
		return
	}

	start := time.Now()
	res, err := processor.Process(ctx, source.OriginalPath)
	switch {
	case errors.Is(err, context.Canceled):
		// Only a killed process gets here. Leave the row processing so the next
		// start's sweep marks it failed and the user can retry.
		s.log.Info("ingest interrupted", "source", sourceID)
		return
	case err != nil:
		s.log.Warn("ingest failed", "source", sourceID, "err", err)
		s.setStatus(ctx, sourceID, domain.StatusFailed, err.Error(), nil)
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
		s.setStatus(ctx, sourceID, domain.StatusFailed, "could not store assets: "+err.Error(), nil)
		return
	}
	if err := s.store.PutSegments(ctx, sourceID, res.Segments); err != nil {
		s.setStatus(ctx, sourceID, domain.StatusFailed, "could not store segments: "+err.Error(), nil)
		return
	}

	if res.Unsupported != "" {
		s.setStatus(ctx, sourceID, domain.StatusUnsupported, res.Unsupported, res.Warnings)
		s.log.Info("source unsupported", "source", sourceID, "reason", res.Unsupported, "seconds", time.Since(start).Round(time.Millisecond))
		return
	}

	// Embedding is part of ingest, not a stage a caller opts into. Until it
	// succeeds this source is not `ready`, because `ready` is the promise that
	// asking a question will search this source (ADR 0004).
	if err := s.embed(ctx, source.Name, res.Segments); err != nil {
		s.log.Warn("embedding failed", "source", sourceID, "segments", len(res.Segments), "err", err)
		// The chunks stay, so the source is still readable and inspectable; what
		// is missing is retrievability, and the detail says which server to fix.
		s.setStatus(ctx, sourceID, domain.StatusFailed, err.Error(), res.Warnings)
		return
	}

	s.setStatus(ctx, sourceID, domain.StatusReady, "", res.Warnings)
	s.log.Info("ingest done",
		"source", sourceID, "kind", source.Kind, "segments", len(res.Segments),
		"assets", len(assets), "embedded", "yes", "seconds", time.Since(start).Round(time.Millisecond))
}

// reembed replaces a source's vectors from the segments already in the store.
//
// On failure the source goes back to ready rather than to failed. Its previous
// vectors are untouched — nothing is written until the new set is in hand — so it
// is still searchable, and a failed source is excluded from retrieval, which would
// make a broken button cost the user a source they could still use. The detail
// carries why, so the row says both.
func (s *Service) reembed(ctx context.Context, sourceID string) {
	source, err := s.store.GetSource(ctx, sourceID)
	if err != nil {
		s.log.Warn("re-embed skipped", "source", sourceID, "err", err)
		return
	}

	segments, err := s.store.ListSegments(ctx, sourceID)
	if err != nil {
		s.setStatus(ctx, sourceID, domain.StatusReady, "could not read the chunks to re-embed: "+err.Error(), source.Warnings)
		return
	}

	start := time.Now()
	if err := s.storeVectors(ctx, sourceID, source.Name, segments); err != nil {
		s.log.Warn("re-embed failed", "source", sourceID, "segments", len(segments), "err", err)
		s.setStatus(ctx, sourceID, domain.StatusReady, err.Error(), source.Warnings)
		return
	}

	s.setStatus(ctx, sourceID, domain.StatusReady, "", source.Warnings)
	s.log.Info("re-embed done", "source", sourceID, "segments", len(segments),
		"seconds", time.Since(start).Round(time.Millisecond))
}

// storeVectors embeds segments and swaps them in as a source's whole vector set.
func (s *Service) storeVectors(ctx context.Context, sourceID, title string, segments []domain.Segment) error {
	if len(segments) == 0 {
		return nil
	}

	embedder, err := s.embedder(s.settings.Get())
	if err != nil {
		return fmt.Errorf("could not start the embedder: %w", err)
	}

	vectors, err := embedder.Embed(ctx, title, segments)
	if err != nil {
		return err
	}
	if len(vectors) != len(segments) {
		return fmt.Errorf("embedder returned %d vectors for %d segments",
			len(vectors), len(segments))
	}
	return s.store.ReplaceEmbeddings(ctx, sourceID, vectors)
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

	vectors, err := embedder.Embed(ctx, title, segments)
	if err != nil {
		return err
	}
	if len(vectors) != len(segments) {
		return fmt.Errorf("embedder returned %d vectors for %d segments",
			len(vectors), len(segments))
	}
	return s.store.PutEmbeddings(ctx, vectors)
}

// setStatus records an outcome. Its own failure is logged, not propagated: the
// only thing left to do is keep serving.
func (s *Service) setStatus(ctx context.Context, id string, status domain.SourceStatus, detail string, warnings []string) {
	if err := s.store.SetSourceStatus(ctx, id, status, detail, warnings); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Warn("could not record source status", "source", id, "status", status, "err", err)
	}
}
