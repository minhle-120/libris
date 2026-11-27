package ingest

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/tools"
)

type fakeCall struct {
	prog tools.Program
	args []string
}

// hostPDF resolves the programs this machine really has, for the tests that run them.
func hostPDF(t *testing.T) tools.PDF {
	t.Helper()
	return tools.FromPath().PDF()
}

// hostAudio resolves the program this machine really has, for the tests that run it.
func hostAudio(t *testing.T) tools.Audio {
	t.Helper()
	return tools.FromPath().Audio()
}

func hostVideo(t *testing.T) tools.Video {
	t.Helper()
	return tools.FromPath().Video()
}

// missingProgram is what a resolver hands back for a program found in neither place: named,
// and not found. A PDF struct with no programs in it at all is not a thing that can happen,
// and writing one hid that a finding's detail is taken from the program's own name.
func missingProgram(name string) tools.Program {
	return tools.Program{Name: name, Origin: tools.OriginMissing}
}

// pathProgram is a program a test does not care about the provenance of.
func pathProgram(name string) tools.Program {
	return tools.Program{Name: name, Path: name, Origin: tools.OriginPath}
}

// fakeRunner is safe across the goroutines a fan-out runs, because the thing under
// test now calls it from several at once.
type fakeRunner struct {
	mu    sync.Mutex
	calls []fakeCall
	fn    func(prog tools.Program, args []string) ([]byte, error)

	// fnCtx, when set, is asked instead of fn: the rare test that needs the context
	// the runner was handed, which is what a cancelled fan-out is observed through.
	fnCtx func(ctx context.Context, prog tools.Program, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(ctx context.Context, prog tools.Program, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{prog, args})
	f.mu.Unlock()
	if f.fnCtx != nil {
		return f.fnCtx(ctx, prog, args)
	}
	if f.fn == nil {
		return nil, nil
	}
	return f.fn(prog, args)
}

// invoked returns the arguments of every call, newest fan-out included.
func (f *fakeRunner) invoked() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.args)
	}
	return out
}

// pdfFn answers both programs a PDF needs: the text pass gets the given page strings,
// the raster pass gets the PNGs that invocation asked for. Honouring -f/-l is what
// lets a test see a range — the rasteriser checks that each one wrote its own span and
// nothing else — and writing the absolute page number is what poppler really does.
func pdfFn(t *testing.T, text []string, pages int) func(tools.Program, []string) ([]byte, error) {
	t.Helper()
	return func(prog tools.Program, args []string) ([]byte, error) {
		if strings.Contains(prog.Name, "totext") {
			return []byte(strings.Join(text, "\f") + "\f"), nil
		}
		first, last := 1, pages
		for i, a := range args {
			switch a {
			case "-f":
				first, _ = strconv.Atoi(args[i+1])
			case "-l":
				last, _ = strconv.Atoi(args[i+1])
			}
		}
		stem := args[len(args)-1]
		for n := first; n <= last; n++ {
			writePNG(t, fmt.Sprintf("%s-%d.png", stem, n), 4, 4)
		}
		return nil, nil
	}
}

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
}

func testConfig() config.Config {
	c := config.Default()
	return c
}

// ── PDF ──────────────────────────────────────────────────────────

func TestPDFPages(t *testing.T) {
	runner := &fakeRunner{fn: func(tools.Program, []string) ([]byte, error) {
		return []byte("page one text\fpage two text\f"), nil
	}}
	pdf := &PDF{Text: pathProgram("pdftotext"), DPI: 150, Runner: runner}

	pages, err := pdf.Pages(context.Background(), "/tmp/x.pdf")
	if err != nil {
		t.Fatalf("Pages: %v", err)
	}
	if len(pages) != 2 || pages[1] != "page two text" {
		t.Fatalf("pages = %q", pages)
	}

	args := strings.Join(runner.calls[0].args, " ")
	if !strings.Contains(args, "-enc UTF-8") || !strings.HasSuffix(args, "-") {
		t.Fatalf("args = %q", args)
	}
}

func TestPDFPagesUnavailable(t *testing.T) {
	pdf := &PDF{}
	if _, err := pdf.Pages(context.Background(), "x.pdf"); !errors.Is(err, ErrNoPDFText) {
		t.Fatalf("err = %v", err)
	}
}

func TestPDFPagesRunnerError(t *testing.T) {
	boom := errors.New("boom")
	pdf := &PDF{Text: pathProgram("pdftotext"), Runner: &fakeRunner{fn: func(tools.Program, []string) ([]byte, error) {
		return nil, boom
	}}}
	if _, err := pdf.Pages(context.Background(), "x.pdf"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestPDFPageImages(t *testing.T) {
	runner := &fakeRunner{fn: func(prog tools.Program, args []string) ([]byte, error) {
		stem := args[len(args)-1]
		// pdftoppm numbers pages without zero padding, so 10 sorts before 2.
		for _, n := range []string{"1", "2", "10"} {
			writePNG(t, stem+"-"+n+".png", 4, 4)
		}
		return nil, nil
	}}
	pdf := &PDF{Raster: pathProgram("pdftoppm"), DPI: 96, Runner: runner}
	sink := DirSink{Root: t.TempDir()}

	// A page count of 0 is the caller not knowing it, which is the one case that must
	// still render the whole document in a single process.
	assets, err := pdf.PageImages(context.Background(), "/tmp/x.pdf", sink, "src/pages", 0)
	if err != nil {
		t.Fatalf("PageImages: %v", err)
	}
	if len(assets) != 3 {
		t.Fatalf("got %d assets", len(assets))
	}
	for i, want := range []string{"src/pages/page-0001.png", "src/pages/page-0002.png", "src/pages/page-0003.png"} {
		if assets[i].ID != want {
			t.Errorf("asset %d = %q, want %q", i, assets[i].ID, want)
		}
		if _, err := os.Stat(assets[i].Path); err != nil {
			t.Errorf("asset %d missing on disk: %v", i, err)
		}
	}

	calls := runner.invoked()
	if len(calls) != 1 {
		t.Fatalf("%d processes for a document of unknown length", len(calls))
	}
	args := strings.Join(calls[0], " ")
	if !strings.Contains(args, "-png") || !strings.Contains(args, "-r 96") {
		t.Fatalf("args = %q", args)
	}
	if strings.Contains(args, "-f") || strings.Contains(args, "-l") {
		t.Fatalf("an unknown page count must not ask for a range: %q", args)
	}
}

// A document whose length is known is cut into ranges and rendered by one process
// each, and the pages come out in document order however many processes wrote them.
func TestPDFPageImagesFanOut(t *testing.T) {
	runner := &fakeRunner{fn: pdfFn(t, nil, 7)}
	pdf := &PDF{Raster: pathProgram("pdftoppm"), DPI: 96, Shards: 3, Runner: runner}

	assets, err := pdf.PageImages(context.Background(), "/tmp/x.pdf", DirSink{Root: t.TempDir()}, "src/pages", 7)
	if err != nil {
		t.Fatalf("PageImages: %v", err)
	}
	if len(assets) != 7 {
		t.Fatalf("got %d assets, want 7", len(assets))
	}
	for i, a := range assets {
		if want := fmt.Sprintf("src/pages/page-%04d.png", i+1); a.ID != want {
			t.Errorf("asset %d = %q, want %q", i, a.ID, want)
		}
	}

	// Three processes, and between them every page exactly once: the spans are what
	// make that true, so they are asserted rather than inferred from the asset list.
	calls := runner.invoked()
	if len(calls) != 3 {
		t.Fatalf("%d processes, want 3", len(calls))
	}
	seen := map[int]int{}
	for _, args := range calls {
		first, last := rangeOf(t, args)
		for n := first; n <= last; n++ {
			seen[n]++
		}
		if !strings.Contains(strings.Join(args, " "), "-r 96") {
			t.Errorf("args lost the dpi: %q", args)
		}
	}
	for n := 1; n <= 7; n++ {
		if seen[n] != 1 {
			t.Errorf("page %d rendered %d times", n, seen[n])
		}
	}
}

// rangeOf reads the -f/-l a raster call was given.
func rangeOf(t *testing.T, args []string) (int, int) {
	t.Helper()
	first, last := 0, 0
	for i, a := range args {
		switch a {
		case "-f":
			first, _ = strconv.Atoi(args[i+1])
		case "-l":
			last, _ = strconv.Atoi(args[i+1])
		}
	}
	if first == 0 || last == 0 {
		t.Fatalf("no range in %q", args)
	}
	return first, last
}

func TestPDFPageRanges(t *testing.T) {
	cases := []struct {
		pages, shards int
		want          []pageRange
	}{
		{0, 4, []pageRange{{0, 0}}},
		{9, 1, []pageRange{{0, 0}}},
		{1, 4, []pageRange{{0, 0}}},
		{7, 3, []pageRange{{1, 3}, {4, 6}, {7, 7}}},
		{10, 4, []pageRange{{1, 3}, {4, 6}, {7, 9}, {10, 10}}},
		{6, 8, []pageRange{{1, 2}, {3, 4}, {5, 6}}},
		{100, 8, []pageRange{{1, 13}, {14, 26}, {27, 39}, {40, 52}, {53, 65}, {66, 78}, {79, 91}, {92, 100}}},
	}
	for _, c := range cases {
		got := rasterRanges(c.pages, c.shards)
		if !slices.Equal(got, c.want) {
			t.Errorf("rasterRanges(%d, %d) = %v, want %v", c.pages, c.shards, got, c.want)
		}
	}
}

// Every page in exactly one range, whatever the count and the shards: a page in none
// is a missing raster, a page in two is the same page rendered twice and embedded twice.
func TestPDFPageRangesCoverOnce(t *testing.T) {
	for pages := 1; pages <= 250; pages++ {
		for shards := 1; shards <= 12; shards++ {
			checkRangesCoverOnce(t, pages, shards)
		}
	}
}

// checkRangesCoverOnce asserts what a cut of one document always has to be: at most
// shards processes, and every page inside one of their spans and no other. A page in
// none is a missing raster; a page in two is the same page rendered and embedded twice.
func checkRangesCoverOnce(t *testing.T, pages, shards int) {
	t.Helper()

	ranges := rasterRanges(pages, shards)
	if len(ranges) > max(shards, 1) {
		t.Fatalf("pages %d shards %d: %d ranges", pages, shards, len(ranges))
	}

	seen := make([]int, pages+1)
	for _, r := range ranges {
		first, last := r.first, r.last
		if first == 0 {
			// A process asked for no range, which is the whole document — allowed only
			// when there is nothing to cut or nothing to cut it with.
			if pages > 1 && shards > 1 {
				t.Fatalf("pages %d shards %d: whole-document range", pages, shards)
			}
			first, last = 1, pages
		}
		if first > last || first < 1 || last > pages {
			t.Fatalf("pages %d shards %d: bad span %v", pages, shards, r)
		}
		for n := first; n <= last; n++ {
			seen[n]++
		}
	}
	for n := 1; n <= pages; n++ {
		if seen[n] != 1 {
			t.Fatalf("pages %d shards %d: page %d in %d ranges", pages, shards, n, seen[n])
		}
	}
}

func TestPDFPageImagesShortRun(t *testing.T) {
	runner := &fakeRunner{fn: func(prog tools.Program, args []string) ([]byte, error) {
		writePNG(t, args[len(args)-1]+"-1.png", 4, 4) // whatever the range asked for
		return nil, nil
	}}
	pdf := &PDF{Raster: pathProgram("pdftoppm"), DPI: 96, Shards: 2, Runner: runner}

	_, err := pdf.PageImages(context.Background(), "/tmp/x.pdf", DirSink{Root: t.TempDir()}, "src/pages", 6)
	if err == nil || !strings.Contains(err.Error(), "page files for pages") {
		t.Fatalf("err = %v", err)
	}
}

// One range failing fails the document — and stops the processes still rendering it.
// The surviving range here returns only once its own context is closed, so a fan-out
// that waited for it to finish on its own would time this test out rather than fail it.
func TestPDFPageImagesRangeErrorCancelsTheRest(t *testing.T) {
	var cancelled atomic.Int32
	runner := &fakeRunner{fnCtx: func(ctx context.Context, _ tools.Program, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "-f 4") {
			return nil, errors.New("boom")
		}
		select {
		case <-ctx.Done():
			cancelled.Add(1)
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, nil
		}
	}}
	pdf := &PDF{Raster: pathProgram("pdftoppm"), DPI: 96, Shards: 2, Runner: runner}

	if _, err := pdf.PageImages(context.Background(), "/tmp/x.pdf", DirSink{Root: t.TempDir()}, "src/pages", 6); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	if cancelled.Load() != 1 {
		t.Fatal("the surviving range ran on instead of being cancelled")
	}
}

func TestRasterShardsFollowsTheMachine(t *testing.T) {
	got := rasterShards()
	if got < 1 {
		t.Fatalf("shards = %d", got)
	}
	if cpu := runtime.NumCPU(); got > cpu {
		t.Fatalf("shards %d on %d cpus", got, cpu)
	}
	if p := runtime.GOMAXPROCS(0); got > p {
		t.Fatalf("shards %d with GOMAXPROCS %d", got, p)
	}
	if got > rasterShardCap {
		t.Fatalf("shards %d over the cap", got)
	}
}

func TestPDFPageImagesUnavailable(t *testing.T) {
	pdf := &PDF{}
	if _, err := pdf.PageImages(context.Background(), "x.pdf", DirSink{Root: t.TempDir()}, "p", 0); !errors.Is(err, ErrNoPDFRaster) {
		t.Fatalf("err = %v", err)
	}
}

func TestPDFPageImagesNoOutput(t *testing.T) {
	pdf := &PDF{Raster: pathProgram("pdftoppm"), DPI: 72, Runner: &fakeRunner{}}
	_, err := pdf.PageImages(context.Background(), "x.pdf", DirSink{Root: t.TempDir()}, "p", 0)
	if err == nil || !strings.Contains(err.Error(), "produced no pages") {
		t.Fatalf("err = %v", err)
	}
}

func TestPDFPageImagesSinkError(t *testing.T) {
	runner := &fakeRunner{fn: func(_ tools.Program, args []string) ([]byte, error) {
		writePNG(t, args[len(args)-1]+"-1.png", 2, 2)
		return nil, nil
	}}
	pdf := &PDF{Raster: pathProgram("pdftoppm"), DPI: 72, Runner: runner}
	boom := errors.New("disk full")

	if _, err := pdf.PageImages(context.Background(), "x.pdf", failSink{err: boom}, "p", 0); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestPageNumber(t *testing.T) {
	cases := map[string]int{"/a/page-1.png": 1, "/a/page-42.png": 42, "/a/page.png": 0}
	for in, want := range cases {
		if got := pageNumber(in); got != want {
			t.Errorf("pageNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

// ── images ───────────────────────────────────────────────────────

func TestInspectImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.png")
	writePNG(t, path, 7, 9)

	meta, err := inspectImage(path)
	if err != nil {
		t.Fatalf("inspectImage: %v", err)
	}
	if meta.Width != 7 || meta.Height != 9 || meta.Format != "png" {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.MIME() != "image/png" || !strings.Contains(meta.String(), "7x9") {
		t.Fatalf("formatters = %q %q", meta.MIME(), meta.String())
	}

	garbage := filepath.Join(dir, "b.png")
	if err := os.WriteFile(garbage, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectImage(garbage); err == nil {
		t.Fatal("expected decode error")
	}
	if _, err := inspectImage(filepath.Join(dir, "missing.png")); err == nil {
		t.Fatal("expected open error")
	}
}

// ── sink ─────────────────────────────────────────────────────────

type failSink struct{ err error }

func (f failSink) PutFile(context.Context, string, string) (domain.Asset, error) {
	return domain.Asset{}, f.err
}

func (f failSink) Put(context.Context, string, io.Reader) (domain.Asset, error) {
	return domain.Asset{}, f.err
}

func TestDirSinkPut(t *testing.T) {
	sink := DirSink{Root: t.TempDir()}

	asset, err := sink.Put(context.Background(), "src/a.txt", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if asset.Bytes != 5 || asset.ID != "src/a.txt" {
		t.Fatalf("asset = %+v", asset)
	}
	if got, err := os.ReadFile(asset.Path); err != nil || string(got) != "hello" {
		t.Fatalf("content = %q, %v", got, err)
	}
	if _, err := os.Stat(asset.Path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file left behind")
	}
}

func TestDirSinkPutFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.png")
	writePNG(t, src, 3, 3)

	asset, err := DirSink{Root: filepath.Join(dir, "out")}.PutFile(context.Background(), "x/in.png", src)
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if asset.MIME != "image/png" || asset.Bytes == 0 {
		t.Fatalf("asset = %+v", asset)
	}
}

func TestDirSinkRejectsUnsafeNames(t *testing.T) {
	sink := DirSink{Root: t.TempDir()}
	for _, name := range []string{"../escape", "a/../../escape", "/absolute", ".", ""} {
		if _, err := sink.Put(context.Background(), name, strings.NewReader("x")); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
	if _, err := (DirSink{}).Put(context.Background(), "a", strings.NewReader("x")); err == nil {
		t.Error("empty root was accepted")
	}
}

func TestDirSinkCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sink := DirSink{Root: t.TempDir()}
	if _, err := sink.Put(ctx, "a.txt", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestMimeByExt(t *testing.T) {
	cases := map[string]string{
		"a.png": "image/png",
		"a.JPG": "image/jpeg",
		"a.gif": "image/gif",
		"a.pdf": "application/pdf",
		"a.bin": "application/octet-stream",
	}
	for in, want := range cases {
		if got := mimeByExt(in); got != want {
			t.Errorf("mimeByExt(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── pipeline ─────────────────────────────────────────────────────

func TestNewValidation(t *testing.T) {
	if _, err := New(config.Config{}, DirSink{Root: t.TempDir()}, tools.PDF{}, tools.Audio{}, tools.Video{}); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := New(testConfig(), nil, tools.PDF{}, tools.Audio{}, tools.Video{}); err == nil {
		t.Fatal("expected sink error")
	}
	if _, err := New(testConfig(), DirSink{Root: t.TempDir()}, hostPDF(t), hostAudio(t), hostVideo(t)); err != nil {
		t.Fatalf("New: %v", err)
	}
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProcessText(t *testing.T) {
	path := writeTemp(t, "notes.md", strings.Repeat("word ", 250))
	p, err := New(testConfig(), DirSink{Root: t.TempDir()}, hostPDF(t), hostAudio(t), hostVideo(t))
	if err != nil {
		t.Fatal(err)
	}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Source.Kind != domain.SourceText || len(res.Source.ID) != 64 {
		t.Fatalf("source = %+v", res.Source)
	}
	if len(res.Docs) != 1 || res.Docs[0].Kind != domain.KindText {
		t.Fatalf("docs = %+v", res.Docs)
	}
	if len(res.Segments) < 2 {
		t.Fatalf("segments = %d", len(res.Segments))
	}
	for i, seg := range res.Segments {
		if seg.Span.CharEnd <= seg.Span.CharStart {
			t.Errorf("segment %d has empty span: %+v", i, seg.Span)
		}
		if seg.Span.Page != 0 {
			t.Errorf("segment %d should not be paginated", i)
		}
		if seg.SourceID != res.Source.ID || seg.DocID != res.Source.ID {
			t.Errorf("segment %d has wrong ids", i)
		}
	}
}

func TestProcessHTML(t *testing.T) {
	path := writeTemp(t, "page.html", "<p>Hello <b>world</b></p><script>x</script>")
	p, _ := New(testConfig(), DirSink{Root: t.TempDir()}, hostPDF(t), hostAudio(t), hostVideo(t))

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 1 || res.Segments[0].Text != "Hello world" {
		t.Fatalf("segments = %+v", res.Segments)
	}
}

func TestProcessEmptyText(t *testing.T) {
	path := writeTemp(t, "blank.txt", "   \n\n  ")
	p, _ := New(testConfig(), DirSink{Root: t.TempDir()}, hostPDF(t), hostAudio(t), hostVideo(t))

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unsupported == "" || len(res.Segments) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestProcessImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	writePNG(t, path, 5, 6)

	sinkRoot := t.TempDir()
	p, _ := New(testConfig(), DirSink{Root: sinkRoot}, hostPDF(t), hostAudio(t), hostVideo(t))

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source.Kind != domain.SourceImage || len(res.Segments) != 1 {
		t.Fatalf("result = %+v", res)
	}
	seg := res.Segments[0]
	if seg.Kind != domain.KindImage || seg.Asset == nil {
		t.Fatalf("segment = %+v", seg)
	}
	if _, err := os.Stat(seg.Asset.Path); err != nil {
		t.Fatalf("asset missing: %v", err)
	}
}

func TestProcessImageStoreFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	writePNG(t, path, 2, 2)

	p := &Processor{cfg: testConfig(), sink: failSink{err: errors.New("disk full")}}
	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// A storage failure is the one failure whose own words survive into the row: a full disk
	// and a rejected filename want different answers. And it is not also a finding.
	if !strings.Contains(res.Unsupported, "disk full") {
		t.Fatalf("unsupported = %q, want the storage error's own words", res.Unsupported)
	}
	if len(res.Findings) != 0 {
		t.Errorf("findings = %v, want none", res.Findings)
	}
}

func TestProcessUnsupported(t *testing.T) {
	p, _ := New(testConfig(), DirSink{Root: t.TempDir()}, hostPDF(t), hostAudio(t), hostVideo(t))

	// A .zip is sniffed by its magic bytes, not its extension.
	for name, want := range map[string]string{
		"a.zip":  "unsupported file type",
		"a.docx": "unsupported file type",
	} {
		res, err := p.Process(context.Background(), writeTemp(t, name, "PK\x03\x04 binary payload"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(res.Unsupported, want) {
			t.Errorf("%s: unsupported = %q, want %q", name, res.Unsupported, want)
		}
		if len(res.Segments) != 0 {
			t.Errorf("%s: expected no segments", name)
		}
	}
}

func TestProcessRejections(t *testing.T) {
	cfg := testConfig()
	cfg.MaxFileMB = 1
	p, _ := New(cfg, DirSink{Root: t.TempDir()}, hostPDF(t), hostAudio(t), hostVideo(t))

	big := filepath.Join(t.TempDir(), "big.txt")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(cfg.MaxFileBytes() + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := p.Process(context.Background(), big); err == nil {
		t.Error("expected size error")
	}
	if _, err := p.Process(context.Background(), t.TempDir()); err == nil {
		t.Error("expected directory error")
	}
	if _, err := p.Process(context.Background(), filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Error("expected missing file error")
	}
}

func TestProcessPDF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4 fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{fn: pdfFn(t, []string{"Alpha page", "Beta page"}, 2)}

	p := &Processor{
		cfg:  testConfig(),
		sink: DirSink{Root: t.TempDir()},
		pdf:  &PDF{Text: pathProgram("pdftotext"), Raster: pathProgram("pdftoppm"), DPI: 150, Runner: runner},
	}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Docs) != 2 {
		t.Fatalf("expected text and page documents, got %+v", res.Docs)
	}
	if res.Docs[1].ID != res.Source.ID+"#pages" {
		t.Fatalf("page doc id = %q", res.Docs[1].ID)
	}

	var text, images int
	pages := map[int]bool{}
	for _, seg := range res.Segments {
		switch seg.Kind {
		case domain.KindText:
			text++
			pages[seg.Span.Page] = true
			if seg.Span.PageEnd != 0 {
				pages[seg.Span.PageEnd] = true
			}
		case domain.KindImage:
			images++
			if seg.Asset == nil {
				t.Fatal("image segment without asset")
			}
		}
	}
	// Both pages hold a few words, so the document is one chunk that runs
	// across the page break — that is the point of chunking the whole text.
	if text != 1 || images != 2 {
		t.Fatalf("text=%d images=%d", text, images)
	}
	if !pages[1] || !pages[2] {
		t.Fatalf("pages = %v", pages)
	}
	if res.Segments[0].Span.PageEnd != 2 {
		t.Fatalf("span = %+v, want a chunk spanning pages 1 and 2", res.Segments[0].Span)
	}
}

func TestProcessPDFWithoutTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4 fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &Processor{cfg: testConfig(), sink: DirSink{Root: t.TempDir()},
		pdf: &PDF{Text: missingProgram("pdftotext"), Raster: missingProgram("pdftoppm"), DPI: 150}}
	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// Two programs missing, so both stages report the machine's fault...
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %v", res.Findings)
	}
	for _, want := range []string{"pdftotext", "pdftoppm"} {
		found := false
		for _, f := range res.Findings {
			if f.Detail == want {
				found = true
				if f.Code != domain.FindingToolMissing {
					t.Errorf("%s: code = %q, want tool_missing", want, f.Code)
				}
			}
		}
		if !found {
			t.Errorf("no finding names %q: %v", want, res.Findings)
		}
	}
	// ...and the row has to say so, rather than blaming a PDF that no machine without poppler
	// could have read. This is the sentence a person acts on: install the tools, run it again.
	if res.Unsupported != "no PDF tools on this machine" {
		t.Errorf("unsupported = %q", res.Unsupported)
	}
}
