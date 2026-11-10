package ingest

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"libris/internal/domain"
)

// ── remaining split/detect/image branches ────────────────────────

func TestSplitTextUnicodeOnlySpace(t *testing.T) {
	// U+2003 EM SPACE passes strings.TrimSpace but is not an ASCII word byte.
	if got := SplitText("\u2003\u2003", 10, 2); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestDetectSniffedAudio(t *testing.T) {
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
	if got := Detect("blob", wav); got != domain.SourceAudio {
		t.Fatalf("Detect = %q", got)
	}
}

func TestImageMetaMIME(t *testing.T) {
	cases := map[string]string{
		"png":  "image/png",
		"jpeg": "image/jpeg",
		"gif":  "image/gif",
		"webp": "application/octet-stream",
	}
	for format, want := range cases {
		if got := (imageMeta{Format: format}).MIME(); got != want {
			t.Errorf("MIME(%q) = %q, want %q", format, got, want)
		}
	}
}

// ── exec runner (real processes) ─────────────────────────────────

func TestExecRunner(t *testing.T) {
	if lookPath("sh") == "" {
		t.Skip("sh not available")
	}
	r := execRunner{}

	out, err := r.Run(context.Background(), "sh", "-c", "printf hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out) != "hello" {
		t.Fatalf("out = %q", out)
	}

	if _, err := r.Run(context.Background(), "sh", "-c", "echo boom >&2; exit 3"); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr not surfaced: %v", err)
	}

	if _, err := r.Run(context.Background(), "sh", "-c", "exit 2"); err == nil {
		t.Fatal("expected error")
	}
}

func TestPDFDefaultRunner(t *testing.T) {
	if _, ok := (&PDF{}).runner().(execRunner); !ok {
		t.Fatal("runner() should default to execRunner")
	}
}

func TestPDFPageImagesRunError(t *testing.T) {
	boom := errors.New("boom")
	pdf := &PDF{RasterBin: "pdftoppm", DPI: 72, Runner: &fakeRunner{fn: func(string, []string) ([]byte, error) {
		return nil, boom
	}}}
	if _, err := pdf.PageImages(context.Background(), "x.pdf", DirSink{Root: t.TempDir()}, "p"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// ── pipeline error paths ─────────────────────────────────────────

func TestProcessUnreadableFile(t *testing.T) {
	path := writeTemp(t, "secret.txt", "hello")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skip("cannot change permissions")
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}

	p, _ := New(testConfig(), DirSink{Root: t.TempDir()})
	if _, err := p.Process(context.Background(), path); err == nil {
		t.Fatal("expected permission error")
	}
}

func TestProcessCorruptImage(t *testing.T) {
	path := writeTemp(t, "broken.png", "definitely not a png")
	p, _ := New(testConfig(), DirSink{Root: t.TempDir()})

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unsupported == "" || len(res.Warnings) == 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestReadHeadAndHashErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")

	if _, err := readHead(missing, 16); err == nil {
		t.Error("readHead on missing file should fail")
	}
	if _, err := hashFile(missing); err == nil {
		t.Error("hashFile on missing file should fail")
	}
}

func TestPDFPartialFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	in := &fakeRunner{fn: func(string, []string) ([]byte, error) { return nil, boom }}
	p := &Processor{cfg: testConfig(), sink: DirSink{Root: t.TempDir()},
		pdf: &PDF{TextBin: "pdftotext", RasterBin: "pdftoppm", DPI: 72, Runner: in}}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 2 {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	if len(res.Segments) != 0 || res.Unsupported == "" {
		t.Fatalf("result = %+v", res)
	}
}

func TestPDFSkipsEmptyPages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{fn: func(bin string, args []string) ([]byte, error) {
		if strings.Contains(bin, "totext") {
			return []byte("first page\f\fthird page\f"), nil
		}
		writePNG(t, args[len(args)-1]+"-1.png", 2, 2)
		return nil, nil
	}}
	p := &Processor{cfg: testConfig(), sink: DirSink{Root: t.TempDir()},
		pdf: &PDF{TextBin: "pdftotext", RasterBin: "pdftoppm", DPI: 72, Runner: runner}}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[int]bool{}
	for _, seg := range res.Segments {
		if seg.Kind != domain.KindText {
			continue
		}
		seen[seg.Span.Page] = true
		if seg.Span.PageEnd != 0 {
			seen[seg.Span.PageEnd] = true
		}
	}
	// The blank page has no text, so no chunk may name it.
	if seen[2] {
		t.Fatalf("empty page 2 produced a segment: %v", seen)
	}
	if !seen[1] || !seen[3] {
		t.Fatalf("pages = %v", seen)
	}
}

// ── sink error paths ────────────────────────────────────────────

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestDirSinkWriteErrors(t *testing.T) {
	root := t.TempDir()
	sink := DirSink{Root: root}

	// Copy failure removes the temporary file.
	if _, err := sink.Put(context.Background(), "a.txt", errReader{}); err == nil {
		t.Fatal("expected copy error")
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt.tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind after copy error")
	}

	// A directory squatting on the temp name makes os.Create fail.
	if err := os.MkdirAll(filepath.Join(root, "b.txt.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Put(context.Background(), "b.txt", strings.NewReader("x")); err == nil {
		t.Fatal("expected create error")
	}

	// A directory at the destination makes the rename fail.
	if err := os.MkdirAll(filepath.Join(root, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Put(context.Background(), "c", strings.NewReader("x")); err == nil {
		t.Fatal("expected rename error")
	}
}

type cancelReader struct {
	cancel context.CancelFunc
	sent   bool
}

func (c *cancelReader) Read(p []byte) (int, error) {
	if c.sent {
		return 0, io.EOF
	}
	c.sent = true
	c.cancel()
	p[0] = 'x'
	return 1, nil
}

// Cancelling mid-copy must abort the write and clean up the temporary file.
func TestDirSinkCanceledMidCopy(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := (DirSink{Root: root}).Put(ctx, "a.txt", &cancelReader{cancel: cancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt.tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind after cancellation")
	}
}

func TestDirSinkRootIsFile(t *testing.T) {
	file := writeTemp(t, "not-a-dir", "x")
	if _, err := (DirSink{Root: file}).Put(context.Background(), "sub/a.txt", strings.NewReader("x")); err == nil {
		t.Fatal("expected mkdir error")
	}
}

func TestDirSinkPutFileErrors(t *testing.T) {
	sink := DirSink{Root: t.TempDir()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := writeTemp(t, "in.txt", "x")
	if _, err := sink.PutFile(ctx, "a/in.txt", src); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := sink.PutFile(context.Background(), "a/in.txt", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected open error")
	}
}
