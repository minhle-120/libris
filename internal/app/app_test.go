package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/ingest"
	"libris/internal/store/sqlite"
)

// fakeProcessor stands in for the real pipeline: the file-processing behaviour
// is tested in ingest, what is tested here is what the service does with the
// answer.
type fakeProcessor struct {
	mu    sync.Mutex
	calls int
	path  string

	res *ingest.Result
	err error
}

func (f *fakeProcessor) Process(_ context.Context, path string) (*ingest.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.path = path
	if f.err != nil {
		return nil, f.err
	}
	if f.res != nil {
		return f.res, nil
	}
	// A real doc id is derived from the source, so two sources never collide on
	// a segment id. The stored original is named <sha>.<ext>, which is that.
	doc := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return &ingest.Result{Segments: []domain.Segment{textSegment(doc, 0, "hello")}}, nil
}

func (f *fakeProcessor) called() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func textSegment(docID string, ordinal int, text string) domain.Segment {
	return domain.Segment{
		ID:      docID + ":" + pad(ordinal),
		DocID:   docID,
		Kind:    domain.KindText,
		Ordinal: ordinal,
		Text:    text,
	}
}

func pad(n int) string {
	s := []byte{'0', '0', '0', byte('0' + n%10)}
	return string(s)
}

func imageSegment(docID string, ordinal int, assetDir string) domain.Segment {
	return domain.Segment{
		ID:      docID + ":" + pad(ordinal),
		DocID:   docID,
		Kind:    domain.KindImage,
		Ordinal: ordinal,
		Asset: &domain.Asset{
			ID:    docID + "/page-1.png",
			Path:  filepath.Join(assetDir, "page-1.png"),
			MIME:  "image/png",
			Bytes: 4,
		},
	}
}

// fakeEmbedder stands in for internal/index. The wire behaviour is tested there;
// what is tested here is the promise ingest makes with it: a source does not
// become ready until every one of its segments has a vector.
type fakeEmbedder struct {
	mu       sync.Mutex
	calls    int
	embedded int
	segments []domain.Segment

	dim int
	err error
}

func (f *fakeEmbedder) Embed(_ context.Context, segs []domain.Segment) ([]domain.Embedding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	f.embedded += len(segs)
	f.segments = append(f.segments, segs...)
	if f.err != nil {
		return nil, f.err
	}

	dim := f.dim
	if dim == 0 {
		dim = 3
	}
	out := make([]domain.Embedding, 0, len(segs))
	for _, s := range segs {
		out = append(out, domain.Embedding{
			SegmentID: s.ID, Model: "fake-embed", Dim: dim, Vector: make([]float32, dim),
		})
	}
	return out, nil
}

func (f *fakeEmbedder) stats() (calls, embedded int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.embedded
}

// failWith turns the fake server off or on mid-test. Written as a method because
// Embed reads the field under the same lock: assigning to f.err from the test
// goroutine would be a data race that -race only sometimes catches.
func (f *fakeEmbedder) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// testService wires a real sqlite store to a fake pipeline. The store is real
// because the interesting failures are in how ingest's output lands in the
// schema — foreign keys, idempotency, the ready gate.
func testService(t *testing.T, proc Ingestor) (*Service, *sqlite.DB, string) {
	t.Helper()
	return testServiceWith(t, proc, &fakeEmbedder{})
}

func testServiceWith(t *testing.T, proc Ingestor, emb Embedder) (*Service, *sqlite.DB, string) {
	t.Helper()
	return testServiceWired(t, proc, func(config.Config) (Embedder, error) { return emb, nil })
}

// testServiceWired is the full knob: tests that care about how the embedder is
// built — a factory that itself fails, or one that sees the live settings — pass
// their own EmbedderFor.
func testServiceWired(t *testing.T, proc Ingestor, factory EmbedderFor) (*Service, *sqlite.DB, string) {
	t.Helper()

	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "libris.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := config.Default()
	svc, err := New(Options{
		Store:    db,
		Settings: constSettings(cfg),
		Processor: func(config.Config, ingest.Sink) (Ingestor, error) {
			return proc, nil
		},
		Embedder:  factory,
		Originals: filepath.Join(dir, "originals"),
		Assets:    filepath.Join(dir, "assets"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc, db, dir
}

// segmentCount and storedVectors read the schema directly, so a test cannot be
// satisfied by the same bookkeeping it is checking.
func segmentCount(t *testing.T, db *sqlite.DB, sourceID string) int {
	t.Helper()
	segs, err := db.ListSegments(context.Background(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	return len(segs)
}

func storedVectors(t *testing.T, db *sqlite.DB, sourceID string) int {
	t.Helper()
	var n int
	if err := db.Raw().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM segments WHERE source_id = ? AND embedding IS NOT NULL`, sourceID).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type constSettings config.Config

func (c constSettings) Get() config.Config { return config.Config(c) }

// waitUntil polls a condition without sleeping for synchronisation: it fails
// the test when the condition never holds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func sourceStatus(t *testing.T, db *sqlite.DB, id string) domain.Source {
	t.Helper()
	got, err := db.GetSource(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// ── embedding ─────────────────────────────────────────────────

func TestIngestEmbedsEverySegmentBeforeReady(t *testing.T) {
	proc := &fakeProcessor{res: &ingest.Result{Segments: []domain.Segment{
		textSegment("doc1", 0, "first"),
		textSegment("doc1", 1, "second"),
		imageSegment("doc1#pages", 0, t.TempDir()),
	}}}
	emb := &fakeEmbedder{dim: 8}
	svc, db, _ := testServiceWith(t, proc, emb)

	nb, _ := svc.CreateNotebook(context.Background(), "embed")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("x"), "p.pdf")
	if err != nil {
		t.Fatal(err)
	}

	got := sourceStatus(t, db, src.ID)
	waitUntil(t, "the source to become ready", func() bool {
		got = sourceStatus(t, db, src.ID)
		return got.Status == domain.StatusReady
	})

	calls, embedded := emb.stats()
	if calls != 1 {
		t.Errorf("embedder called %d times, want one batch", calls)
	}
	if embedded != 3 {
		t.Errorf("%d segments embedded, want all 3", embedded)
	}

	// The promise ADR 0004 makes: ready means the vectors are in the database,
	// not merely that ingest finished.
	if n := storedVectors(t, db, src.ID); n != 3 {
		t.Errorf("%d of 3 segments carry a vector while the source says ready", n)
	}
}

func TestEmbeddingFailureLeavesTheSourceFailed(t *testing.T) {
	proc := &fakeProcessor{}
	emb := &fakeEmbedder{err: errors.New("embed server at http://127.0.0.1:8082: connection refused")}
	svc, db, _ := testServiceWith(t, proc, emb)

	nb, _ := svc.CreateNotebook(context.Background(), "offline")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("x"), "p.pdf")
	if err != nil {
		t.Fatal(err)
	}

	var got domain.Source
	waitUntil(t, "the source to be marked failed", func() bool {
		got = sourceStatus(t, db, src.ID)
		return got.Status != domain.StatusProcessing
	})

	if got.Status != domain.StatusFailed {
		t.Fatalf("status = %s, want failed: a source nobody can search is not ready",
			got.Status)
	}
	// The reason has to name the server, because this string is what the user reads.
	if !strings.Contains(got.Detail, "8082") {
		t.Errorf("detail %q should say which server failed", got.Detail)
	}
	// The chunks stay: the source is still readable, and the inspector still works.
	if n := segmentCount(t, db, src.ID); n == 0 {
		t.Error("segments were discarded on an embedding failure")
	}
	if n := storedVectors(t, db, src.ID); n != 0 {
		t.Errorf("%d vectors stored after a failed embed", n)
	}
}

func TestEmbedderConstructionFailureIsRecorded(t *testing.T) {
	// Settings can be edited into something unusable while the process runs; the
	// source must report that rather than sit in processing forever.
	proc := &fakeProcessor{}
	svc, db, _ := testServiceWired(t, proc, func(config.Config) (Embedder, error) {
		return nil, errors.New("embedURL is not a URL")
	})

	nb, _ := svc.CreateNotebook(context.Background(), "bad settings")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("x"), "p.pdf")
	if err != nil {
		t.Fatal(err)
	}

	var got domain.Source
	waitUntil(t, "the source to be marked failed", func() bool {
		got = sourceStatus(t, db, src.ID)
		return got.Status != domain.StatusProcessing
	})
	if got.Status != domain.StatusFailed || !strings.Contains(got.Detail, "embedURL") {
		t.Errorf("status = %s detail = %q", got.Status, got.Detail)
	}
}

func TestRetryRequeuesAFailedSource(t *testing.T) {
	emb := &fakeEmbedder{err: errors.New("server down")}
	proc := &fakeProcessor{}
	svc, db, _ := testServiceWith(t, proc, emb)

	nb, _ := svc.CreateNotebook(context.Background(), "retry")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("x"), "p.pdf")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the first attempt to fail", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusFailed
	})

	// Retrying is a user action in the UI, so the returned source is what the row
	// is written from. If it still says "failed": the row looks unchanged, the UI
	// sees nothing in flight and stops polling, and the source only appears stuck
	// until the user reloads. Reading the database is not enough to catch that.
	back, err := svc.RetrySource(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != domain.StatusProcessing {
		t.Errorf("retry answered %s, want processing: the caller polls on this value", back.Status)
	}
	if back.Detail != "" {
		t.Errorf("retry kept the stale reason %q", back.Detail)
	}
	if got := sourceStatus(t, db, src.ID); got.Status == domain.StatusFailed {
		t.Errorf("a retried source still reports %s: %q", got.Status, got.Detail)
	}

	// Still broken: the second attempt has to fail and be *observed* as failed
	// before the next retry, or the retry would be refused as already processing.
	waitUntil(t, "the retry to fail as well", func() bool {
		calls, _ := emb.stats()
		return calls >= 2 && sourceStatus(t, db, src.ID).Status == domain.StatusFailed
	})

	emb.failWith(nil) // the server is fixed
	if _, err := svc.RetrySource(context.Background(), src.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the source to become ready after the fix", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusReady
	})
	if n := storedVectors(t, db, src.ID); n == 0 {
		t.Error("ready without a single stored vector")
	}
}

func TestRetryOfAWorkingSourceIsANoOp(t *testing.T) {
	svc, db, _ := testService(t, &fakeProcessor{})
	nb, _ := svc.CreateNotebook(context.Background(), "healthy")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("x"), "p.pdf")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the source to become ready", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusReady
	})

	before := sourceStatus(t, db, src.ID)
	if _, err := svc.RetrySource(context.Background(), src.ID); err != nil {
		t.Fatal(err)
	}
	// A retried ready source would drop to processing while it re-does work that
	// succeeded, which is a lie about a source that is still searchable.
	if got := sourceStatus(t, db, src.ID); got.Status != before.Status {
		t.Errorf("retrying a ready source moved it to %s", got.Status)
	}
}

func TestRetryOfAMissingSourceIsNotFound(t *testing.T) {
	svc, _, _ := testService(t, &fakeProcessor{})
	if _, err := svc.RetrySource(context.Background(), "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want domain.ErrNotFound", err)
	}
}

// ── uploads ──────────────────────────────────────────────────────

func TestAddUploadStoresThenProcesses(t *testing.T) {
	proc := &fakeProcessor{}
	svc, db, dir := testService(t, proc)

	nb, err := svc.CreateNotebook(context.Background(), "Quantum")
	if err != nil {
		t.Fatal(err)
	}

	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("%PDF-1.4\nscan"), "paper.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind != domain.SourcePDF {
		t.Errorf("kind = %q, want pdf from the %%PDF magic", src.Kind)
	}
	if src.ID != src.SHA256 || len(src.ID) != 64 {
		t.Errorf("id = %q, want the content hash", src.ID)
	}
	if src.OriginalPath != filepath.Join(dir, "originals", src.ID+".pdf") {
		t.Errorf("original path = %q", src.OriginalPath)
	}

	waitUntil(t, "the source to become ready", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusReady
	})

	got := sourceStatus(t, db, src.ID)
	if got.SegmentCount != 1 {
		t.Errorf("segment_count = %d, want 1", got.SegmentCount)
	}
	if proc.called() != 1 {
		t.Errorf("processor calls = %d, want 1", proc.called())
	}
	if proc.path != src.OriginalPath {
		t.Errorf("processor read %q, want the stored original", proc.path)
	}

	segments, err := svc.ListSegments(context.Background(), src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || segments[0].Text != "hello" {
		t.Errorf("segments = %+v", segments)
	}
}

func TestAddUploadReusesIdenticalBytes(t *testing.T) {
	proc := &fakeProcessor{}
	svc, db, _ := testService(t, proc)

	first, err := svc.CreateNotebook(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateNotebook(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}

	body := "%PDF-1.4\nidentical\n"
	a, err := svc.AddUpload(context.Background(), first.ID, strings.NewReader(body), "a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the first copy to finish", func() bool {
		return sourceStatus(t, db, a.ID).Status == domain.StatusReady
	})

	b, err := svc.AddUpload(context.Background(), second.ID, strings.NewReader(body), "copy-under-another-name.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != a.ID {
		t.Fatalf("ids differ: %q vs %q", a.ID, b.ID)
	}

	list, err := svc.ListSources(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("second notebook sees %d sources", len(list))
	}
	// Identity wins over filename: the row is shared, so it keeps the name it
	// was first offered under. Naming the same file differently per notebook is
	// a separate decision (an alias on the membership, not on the source).
	if list[0].Name != "a.pdf" {
		t.Errorf("shared source name = %q, want the first name it was offered under", list[0].Name)
	}

	// Ready sources are not re-rasterised.
	if got := proc.called(); got != 1 {
		t.Errorf("processor calls = %d, want the second upload to skip processing", got)
	}

	// Each notebook sees it, one row of segments serves both.
	for _, nb := range []string{first.ID, second.ID} {
		sources, err := svc.ListSources(context.Background(), nb)
		if err != nil {
			t.Fatal(err)
		}
		if len(sources) != 1 {
			t.Errorf("notebook %s has %d sources", nb, len(sources))
		}
	}
}

func TestAddUploadRequeuesAFailedSource(t *testing.T) {
	proc := &fakeProcessor{err: errors.New("pdftoppm went away")}
	svc, db, _ := testService(t, proc)

	nb, _ := svc.CreateNotebook(context.Background(), "retry")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("%PDF-1.4\n"), "x.pdf")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the failure to be recorded", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusFailed
	})
	if got := sourceStatus(t, db, src.ID).Detail; !strings.Contains(got, "pdftoppm") {
		t.Errorf("detail = %q, want the reason the user needs", got)
	}

	// Offering the same file again is the retry.
	proc.mu.Lock()
	proc.err = nil
	proc.mu.Unlock()

	again, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("%PDF-1.4\n"), "x.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != src.ID {
		t.Fatalf("re-upload changed the id: %q vs %q", again.ID, src.ID)
	}
	waitUntil(t, "the retry to finish", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusReady
	})
	if proc.called() != 2 {
		t.Errorf("processor calls = %d, want 2", proc.called())
	}
}

func TestAddUploadRecordsUnsupported(t *testing.T) {
	proc := &fakeProcessor{res: &ingest.Result{
		Unsupported: "no text layer; optical character recognition is not built yet",
		Warnings:    []string{"page 3 is a scan"},
	}}
	svc, db, _ := testService(t, proc)

	nb, _ := svc.CreateNotebook(context.Background(), "scans")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("binary"), "scan.djvu")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the unsupported verdict", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusUnsupported
	})

	got := sourceStatus(t, db, src.ID)
	if !strings.Contains(got.Detail, "text layer") {
		t.Errorf("detail = %q", got.Detail)
	}
	if len(got.Warnings) != 1 {
		t.Errorf("warnings = %v", got.Warnings)
	}
	if got.SegmentCount != 0 {
		t.Errorf("segment_count = %d, want 0", got.SegmentCount)
	}
}

func TestIngestWritesAssetsBeforeSegments(t *testing.T) {
	// The schema enforces the asset foreign key, so the order is load-bearing:
	// reversed, every image and audio source would fail to store.
	proc := &fakeProcessor{res: &ingest.Result{Segments: []domain.Segment{
		imageSegment("doc#pages", 0, "/tmp"),
		textSegment("doc", 1, "a page"),
	}}}
	svc, db, _ := testService(t, proc)

	nb, _ := svc.CreateNotebook(context.Background(), "images")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("PNGDATA"), "figure.png")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "an image source to store", func() bool {
		s := sourceStatus(t, db, src.ID)
		return s.Status == domain.StatusReady || s.Status == domain.StatusFailed
	})

	got := sourceStatus(t, db, src.ID)
	if got.Status != domain.StatusReady {
		t.Fatalf("status = %s detail = %s", got.Status, got.Detail)
	}
	if got.SegmentCount != 2 {
		t.Errorf("segment_count = %d, want 2", got.SegmentCount)
	}

	asset, err := svc.GetAsset(context.Background(), "doc#pages/page-1.png")
	if err != nil {
		t.Fatal(err)
	}
	if asset.MIME != "image/png" {
		t.Errorf("asset mime = %q", asset.MIME)
	}
}

func TestAddUploadRejectsOversizeAndEmpty(t *testing.T) {
	svc, _, _ := testService(t, &fakeProcessor{})
	nb, err := svc.CreateNotebook(context.Background(), "limits")
	if err != nil {
		t.Fatal(err)
	}

	// The cap is a setting, so the test moves the setting rather than the code.
	small := config.Default()
	small.MaxFileMB = 1
	svc.settings = constSettings(small)

	oversize := strings.NewReader(strings.Repeat("y", 2<<20))
	if _, err := svc.AddUpload(context.Background(), nb.ID, oversize, "big.txt"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize error = %v, want ErrTooLarge", err)
	}

	if _, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader(""), "empty.txt"); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty error = %v, want ErrInvalid", err)
	}
}

func TestAddUploadNeedsAnExistingNotebook(t *testing.T) {
	svc, _, _ := testService(t, &fakeProcessor{})

	_, err := svc.AddUpload(context.Background(), "nope", strings.NewReader("hi"), "a.txt")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestNotebookNameIsRequired(t *testing.T) {
	svc, _, _ := testService(t, &fakeProcessor{})

	if _, err := svc.CreateNotebook(context.Background(), "   "); !errors.Is(err, ErrInvalid) {
		t.Errorf("create error = %v, want ErrInvalid", err)
	}
	if err := svc.RenameNotebook(context.Background(), "x", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("rename error = %v, want ErrInvalid", err)
	}
}

// ── queue and lifetime ───────────────────────────────────────────

func TestIngestQueueIsBounded(t *testing.T) {
	release := make(chan struct{})
	block := &gateProcessor{gate: release}
	svc, db, _ := testService(t, block)
	// Release before Close: the worker is parked inside Process, and Close waits
	// for the job in flight. Once, because the drain below also releases it.
	releaseNow := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseNow)

	nb, _ := svc.CreateNotebook(context.Background(), "flood")

	// One upload occupies the worker, the rest fill the queue.
	ids := make([]string, 0, queueDepth+2)
	var busy error
	for i := 0; i < queueDepth+2; i++ {
		// Distinct bytes each time, or dedupe would skip the queue entirely.
		src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader(string(rune('A'+i))), "f"+string(rune('a'+i))+".txt")
		if err != nil {
			busy = err
			break
		}
		ids = append(ids, src.ID)
	}
	if busy == nil {
		t.Fatalf("expected ErrBusy after %d queued uploads", queueDepth)
	}
	if !errors.Is(busy, ErrBusy) {
		t.Fatalf("error = %v, want ErrBusy", busy)
	}

	releaseNow()
	waitUntil(t, "the queue to drain", func() bool {
		for _, id := range ids {
			if sourceStatus(t, db, id).Status == domain.StatusProcessing {
				return false
			}
		}
		return true
	})
}

// gateProcessor blocks in Process until the gate opens, which is how a bounded
// queue is observed without sleeping for synchronisation.
type gateProcessor struct {
	gate chan struct{}
}

func (g *gateProcessor) Process(ctx context.Context, _ string) (*ingest.Result, error) {
	select {
	case <-g.gate:
		return &ingest.Result{}, nil
	case <-ctx.Done():
		// A blocked ingest must still let the process exit.
		return nil, ctx.Err()
	}
}

func TestStartClosesOutInterruptedWork(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "libris.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	nb, err := db.CreateNotebook(context.Background(), "crash")
	if err != nil {
		t.Fatal(err)
	}
	// A row left processing by a kill -9.
	if _, _, err := db.AddSource(context.Background(), nb.ID, domain.Source{
		ID: "abc", Name: "half.pdf", Kind: domain.SourcePDF, SHA256: "abc", Status: domain.StatusProcessing,
	}); err != nil {
		t.Fatal(err)
	}

	svc, err := New(Options{
		Store:     db,
		Settings:  constSettings(config.Default()),
		Processor: func(config.Config, ingest.Sink) (Ingestor, error) { return &fakeProcessor{}, nil },
		Embedder:  func(config.Config) (Embedder, error) { return &fakeEmbedder{}, nil },
		Originals: filepath.Join(dir, "originals"),
		Assets:    filepath.Join(dir, "assets"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	got, err := db.GetSource(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusFailed {
		t.Errorf("status = %s, want failed so the UI offers a retry", got.Status)
	}
	if got.Detail == "" {
		t.Error("a swept source should say why")
	}
}

func TestCloseWaitsForTheJobInFlight(t *testing.T) {
	finished := make(chan struct{})
	proc := &finishProcessor{finished: finished}
	svc, db, _ := testService(t, proc)

	nb, _ := svc.CreateNotebook(context.Background(), "graceful")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("body"), "g.txt")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "ingest to start", func() bool { return proc.started() })

	svc.Close()

	select {
	case <-finished:
	default:
		t.Fatal("Close returned before the ingest in flight finished")
	}
	if got := sourceStatus(t, db, src.ID).Status; got != domain.StatusReady {
		t.Errorf("status after Close = %s, want the in-flight job to have completed", got)
	}
}

type finishProcessor struct {
	finished  chan struct{}
	startedMu sync.Mutex
	began     bool
}

func (f *finishProcessor) started() bool {
	f.startedMu.Lock()
	defer f.startedMu.Unlock()
	return f.began
}

func (f *finishProcessor) Process(context.Context, string) (*ingest.Result, error) {
	f.startedMu.Lock()
	f.began = true
	f.startedMu.Unlock()
	// Slow enough that Close would race past it if it did not wait.
	time.Sleep(30 * time.Millisecond)
	close(f.finished)
	return &ingest.Result{Segments: []domain.Segment{textSegment("doc", 0, "x")}}, nil
}

// ── the rest of the surface ──────────────────────────────────────

func TestNotebookAndSourceLifecycle(t *testing.T) {
	svc, db, _ := testService(t, &fakeProcessor{})
	ctx := context.Background()

	nb, err := svc.CreateNotebook(ctx, "Reading list")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := svc.GetNotebook(ctx, nb.ID); err != nil || got.Name != "Reading list" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	if err := svc.RenameNotebook(ctx, nb.ID, "Quantum reading"); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListNotebooks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "Quantum reading" {
		t.Errorf("list = %+v", list)
	}

	// Two files, one notebook.
	a, err := svc.AddUpload(ctx, nb.ID, strings.NewReader("first body"), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.AddUpload(ctx, nb.ID, strings.NewReader("second body"), "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "both sources to finish", func() bool {
		return sourceStatus(t, db, a.ID).Status == domain.StatusReady &&
			sourceStatus(t, db, b.ID).Status == domain.StatusReady
	})

	sources, err := svc.ListSources(ctx, nb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
	if got, err := svc.GetSource(ctx, b.ID); err != nil || got.Name != "b.txt" {
		t.Fatalf("get source = %+v, %v", got, err)
	}

	// Detaching keeps the file: another notebook may cite it.
	if err := svc.DetachSource(ctx, nb.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if sources, err = svc.ListSources(ctx, nb.ID); err != nil || len(sources) != 1 {
		t.Fatalf("after detach: %d sources, %v", len(sources), err)
	}
	if _, err := svc.GetSource(ctx, b.ID); err != nil {
		t.Errorf("a detached source must still exist on disk: %v", err)
	}

	// Re-attaching is how a shared file enters a second notebook.
	other, err := svc.CreateNotebook(ctx, "Shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AttachSource(ctx, other.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := svc.ListSources(ctx, other.ID); err != nil || len(list) != 1 {
		t.Fatalf("attached source missing from the second notebook: %d, %v", len(list), err)
	}
	if err := svc.AttachSource(ctx, other.ID, "no-such-source"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("attaching an unknown source = %v, want ErrNotFound", err)
	}
	if err := svc.AttachSource(ctx, "no-such-notebook", b.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("attaching into an unknown notebook = %v, want ErrNotFound", err)
	}

	// Deleting a notebook takes its chats and memberships with it, but not the
	// files, which the other notebook is still using.
	if err := svc.DeleteNotebook(ctx, nb.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetNotebook(ctx, nb.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get after delete = %v, want ErrNotFound", err)
	}
	if _, err := svc.GetSource(ctx, b.ID); err != nil {
		t.Errorf("deleting a notebook must not delete a shared source: %v", err)
	}
	if _, err := svc.ListSources(ctx, nb.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("listing a deleted notebook = %v, want ErrNotFound", err)
	}
}

func TestNewValidatesItsWiring(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "libris.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	processor := func(config.Config, ingest.Sink) (Ingestor, error) { return &fakeProcessor{}, nil }
	embedder := func(config.Config) (Embedder, error) { return &fakeEmbedder{}, nil }
	settings := constSettings(config.Default())

	good := Options{
		Store: db, Settings: settings, Processor: processor, Embedder: embedder,
		Originals: filepath.Join(t.TempDir(), "o"), Assets: filepath.Join(t.TempDir(), "a"),
	}

	for _, field := range []string{"Store", "Settings", "Processor", "Embedder", "Originals", "Assets"} {
		bad := good
		switch field {
		case "Store":
			bad.Store = nil
		case "Settings":
			bad.Settings = nil
		case "Processor":
			bad.Processor = nil
		case "Embedder":
			bad.Embedder = nil
		case "Originals":
			bad.Originals = ""
		case "Assets":
			bad.Assets = ""
		}
		if _, err := New(bad); err == nil {
			t.Errorf("New accepted a missing %s", field)
		}
	}

	if _, err := New(good); err != nil {
		t.Errorf("New rejected a complete wiring: %v", err)
	}
}

func TestProcessorFailureIsRecordedNotLost(t *testing.T) {
	// A processor that cannot even be built is a configuration fault, and the
	// source has to say so rather than sit in processing forever.
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "libris.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc, err := New(Options{
		Store:    db,
		Settings: constSettings(config.Default()),
		Processor: func(config.Config, ingest.Sink) (Ingestor, error) {
			return nil, errors.New("no writable assets directory")
		},
		Embedder:  func(config.Config) (Embedder, error) { return &fakeEmbedder{}, nil },
		Originals: filepath.Join(dir, "originals"),
		Assets:    filepath.Join(dir, "assets"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	nb, _ := svc.CreateNotebook(context.Background(), "broken")
	src, err := svc.AddUpload(context.Background(), nb.ID, strings.NewReader("hi"), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the configuration fault to be recorded", func() bool {
		return sourceStatus(t, db, src.ID).Status == domain.StatusFailed
	})
	if got := sourceStatus(t, db, src.ID).Detail; !strings.Contains(got, "assets directory") {
		t.Errorf("detail = %q, want the reason", got)
	}
}
