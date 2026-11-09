package ingest

import (
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"libris/internal/config"
	"libris/internal/domain"
)

type fakeCall struct {
	bin  string
	args []string
}

type fakeRunner struct {
	calls []fakeCall
	fn    func(bin string, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{bin, args})
	if f.fn == nil {
		return nil, nil
	}
	return f.fn(bin, args)
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
	runner := &fakeRunner{fn: func(string, []string) ([]byte, error) {
		return []byte("page one text\fpage two text\f"), nil
	}}
	pdf := &PDF{TextBin: "pdftotext", DPI: 150, Runner: runner}

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
	pdf := &PDF{TextBin: "pdftotext", Runner: &fakeRunner{fn: func(string, []string) ([]byte, error) {
		return nil, boom
	}}}
	if _, err := pdf.Pages(context.Background(), "x.pdf"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestPDFPageImages(t *testing.T) {
	runner := &fakeRunner{fn: func(bin string, args []string) ([]byte, error) {
		stem := args[len(args)-1]
		// pdftoppm numbers pages without zero padding, so 10 sorts before 2.
		for _, n := range []string{"1", "2", "10"} {
			writePNG(t, stem+"-"+n+".png", 4, 4)
		}
		return nil, nil
	}}
	pdf := &PDF{RasterBin: "pdftoppm", DPI: 96, Runner: runner}
	sink := DirSink{Root: t.TempDir()}

	assets, err := pdf.PageImages(context.Background(), "/tmp/x.pdf", sink, "src/pages")
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

	args := strings.Join(runner.calls[0].args, " ")
	if !strings.Contains(args, "-png") || !strings.Contains(args, "-r 96") {
		t.Fatalf("args = %q", args)
	}
}

func TestPDFPageImagesUnavailable(t *testing.T) {
	pdf := &PDF{}
	if _, err := pdf.PageImages(context.Background(), "x.pdf", DirSink{Root: t.TempDir()}, "p"); !errors.Is(err, ErrNoPDFRaster) {
		t.Fatalf("err = %v", err)
	}
}

func TestPDFPageImagesNoOutput(t *testing.T) {
	pdf := &PDF{RasterBin: "pdftoppm", DPI: 72, Runner: &fakeRunner{}}
	_, err := pdf.PageImages(context.Background(), "x.pdf", DirSink{Root: t.TempDir()}, "p")
	if err == nil || !strings.Contains(err.Error(), "produced no pages") {
		t.Fatalf("err = %v", err)
	}
}

func TestPDFPageImagesSinkError(t *testing.T) {
	runner := &fakeRunner{fn: func(_ string, args []string) ([]byte, error) {
		writePNG(t, args[len(args)-1]+"-1.png", 2, 2)
		return nil, nil
	}}
	pdf := &PDF{RasterBin: "pdftoppm", DPI: 72, Runner: runner}
	boom := errors.New("disk full")

	if _, err := pdf.PageImages(context.Background(), "x.pdf", failSink{err: boom}, "p"); !errors.Is(err, boom) {
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

func TestLookPath(t *testing.T) {
	if got := lookPath(""); got != "" {
		t.Errorf("empty = %q", got)
	}
	if got := lookPath("definitely-not-a-real-binary-xyz"); got != "" {
		t.Errorf("missing binary = %q", got)
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
		"a.png":  "image/png",
		"a.JPG":  "image/jpeg",
		"a.gif":  "image/gif",
		"a.wav":  "audio/wav",
		"a.mp3":  "audio/mpeg",
		"a.flac": "audio/flac",
		"a.pdf":  "application/pdf",
		"a.bin":  "application/octet-stream",
	}
	for in, want := range cases {
		if got := mimeByExt(in); got != want {
			t.Errorf("mimeByExt(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── pipeline ─────────────────────────────────────────────────────

func TestNewValidation(t *testing.T) {
	if _, err := New(config.Config{}, DirSink{Root: t.TempDir()}); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := New(testConfig(), nil); err == nil {
		t.Fatal("expected sink error")
	}
	if _, err := New(testConfig(), DirSink{Root: t.TempDir()}); err != nil {
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
	p, err := New(testConfig(), DirSink{Root: t.TempDir()})
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
	p, _ := New(testConfig(), DirSink{Root: t.TempDir()})

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
	p, _ := New(testConfig(), DirSink{Root: t.TempDir()})

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
	p, _ := New(testConfig(), DirSink{Root: sinkRoot})

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
	if res.Unsupported == "" || len(res.Warnings) == 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestProcessUnsupportedAndAudio(t *testing.T) {
	p, _ := New(testConfig(), DirSink{Root: t.TempDir()})

	// A .zip is sniffed by its magic bytes, not its extension.
	for name, want := range map[string]string{
		"a.zip": "unsupported file type",
		"a.mp3": "transcription",
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
	p, _ := New(cfg, DirSink{Root: t.TempDir()})

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

	runner := &fakeRunner{fn: func(bin string, args []string) ([]byte, error) {
		if strings.Contains(bin, "totext") {
			return []byte("Alpha page\fBeta page\f"), nil
		}
		stem := args[len(args)-1]
		writePNG(t, stem+"-1.png", 4, 4)
		writePNG(t, stem+"-2.png", 4, 4)
		return nil, nil
	}}

	p := &Processor{
		cfg:  testConfig(),
		sink: DirSink{Root: t.TempDir()},
		pdf:  &PDF{TextBin: "pdftotext", RasterBin: "pdftoppm", DPI: 150, Runner: runner},
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
		case domain.KindImage:
			images++
			if seg.Asset == nil {
				t.Fatal("image segment without asset")
			}
		}
	}
	if text != 2 || images != 2 {
		t.Fatalf("text=%d images=%d", text, images)
	}
	if !pages[1] || !pages[2] {
		t.Fatalf("pages = %v", pages)
	}
}

func TestProcessPDFWithoutTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4 fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &Processor{cfg: testConfig(), sink: DirSink{Root: t.TempDir()}, pdf: &PDF{DPI: 150}}
	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 2 {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if res.Unsupported == "" {
		t.Fatal("expected unsupported reason when nothing could be extracted")
	}
}
