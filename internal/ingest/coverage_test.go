package ingest

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"libris/internal/domain"
	"libris/internal/tools"
)

// ── remaining split/detect/image branches ────────────────────────

func TestSplitTextUnicodeOnlySpace(t *testing.T) {
	// U+2003 EM SPACE passes strings.TrimSpace but is not an ASCII word byte.
	if got := SplitText("\u2003\u2003", 10, 2); got != nil {
		t.Fatalf("got %v", got)
	}
}

// A recording is claimed by its bytes rather than released to its extension: the
// extension-wins rule that makes a .md text whatever its first bytes look like would
// otherwise index a WAV as mojibake text. These are the containers net/http's sniffing
// does not answer for — the ones where a WAV named notes.md used to get through the door.
func TestDetectClaimsAudioByBytes(t *testing.T) {
	cases := map[string][]byte{
		"flac": []byte("fLaC\x00\x00\x00" + string(make([]byte, 64))),
		"m4a":  append([]byte{0, 0, 0, 32, 'f', 't', 'y', 'p', 'M', '4', 'A', ' '}, make([]byte, 64)...),
		"mp3":  append([]byte{0xFF, 0xFB, 0x90, 0x64}, make([]byte, 64)...),
		"aiff": append([]byte("FORM\x24\x00\x00\x00AIFF"), make([]byte, 64)...),
	}
	for format, head := range cases {
		if got := Detect("notes.md", head); got != domain.SourceAudio {
			t.Errorf("%s named notes.md: Detect = %q, want audio", format, got)
		}
	}
}

// Video is not an input. The box that opens an M4A opens an MP4 too, so a brand that names
// nothing in particular is refused even beside an extension that asks for audio: what the
// bytes say wins, and a file that says "video" gets no clips cut from it.
func TestDetectRefusesVideoBrand(t *testing.T) {
	head := append([]byte{0, 0, 0, 32, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, make([]byte, 64)...)
	if got := Detect("clip.m4a", head); got != domain.SourceAudio {
		t.Errorf("an .m4a whose container does not object: got %q, want audio", got)
	}
	if got := Detect("clip.mp4", head); got != domain.SourceUnsupported {
		t.Errorf("Detect(clip.mp4) = %q, want unsupported", got)
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
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	r := execRunner{}
	run := tools.Program{Name: "sh", Path: sh, Origin: tools.OriginPath}
	out, err := r.Run(context.Background(), run, "-c", "printf hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out) != "hello" {
		t.Fatalf("out = %q", out)
	}

	// A program copied into the bundle is run with the libraries that were copied with it.
	// This is the whole reason the answer carries where it came from: the same program run
	// from the machine's own PATH must not be told where to look.
	bundled := tools.Program{Name: "sh", Path: sh, Origin: tools.OriginBundled, LibDir: t.TempDir()}
	if out, err := r.Run(context.Background(), bundled, "-c", "printf %s \"$LD_LIBRARY_PATH\""); err != nil {
		t.Fatalf("Run with a library folder: %v", err)
	} else if string(out) == "" {
		t.Error("a bundled program was run without its libraries on the search path")
	}

	if _, err := r.Run(context.Background(), run, "-c", "echo boom >&2; exit 3"); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr not surfaced: %v", err)
	}

	if _, err := r.Run(context.Background(), run, "-c", "exit 2"); err == nil {
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
	pdf := &PDF{Raster: pathProgram("pdftoppm"), DPI: 72, Runner: &fakeRunner{fn: func(tools.Program, []string) ([]byte, error) {
		return nil, boom
	}}}
	if _, err := pdf.PageImages(context.Background(), "x.pdf", DirSink{Root: t.TempDir()}, "p", 0); !errors.Is(err, boom) {
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

	p, _ := New(testConfig(), DirSink{Root: t.TempDir()}, hostPDF(t), hostAudio(t))
	if _, err := p.Process(context.Background(), path); err == nil {
		t.Fatal("expected permission error")
	}
}

func TestProcessCorruptImage(t *testing.T) {
	path := writeTemp(t, "broken.png", "definitely not a png")
	p, _ := New(testConfig(), DirSink{Root: t.TempDir()}, hostPDF(t), hostAudio(t))

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// A file that failed outright gets its reason once: no finding repeats the status.
	if res.Unsupported == "" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Findings) != 0 {
		t.Errorf("findings = %v, want none beside a failure that is already the whole outcome", res.Findings)
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
	in := &fakeRunner{fn: func(tools.Program, []string) ([]byte, error) { return nil, boom }}
	p := &Processor{cfg: testConfig(), sink: DirSink{Root: t.TempDir()},
		pdf: &PDF{Text: pathProgram("pdftotext"), Raster: pathProgram("pdftoppm"), DPI: 72, Runner: in}}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %v", res.Findings)
	}
	for _, want := range []string{"page text", "page images"} {
		found := false
		for _, f := range res.Findings {
			if strings.Contains(f.Detail, want) {
				found = true
				if f.Code != domain.FindingStageFailed {
					t.Errorf("%s: code = %q, want stage_failed — the tools were found, they failed", want, f.Code)
				}
			}
		}
		if !found {
			t.Errorf("no finding names %q: %v", want, res.Findings)
		}
	}
	// The file is what broke here, so the row must not read as though the machine were short a
	// program — the distinction a typed finding exists to make.
	if len(res.Segments) != 0 || res.Unsupported != "PDF produced no segments" {
		t.Fatalf("result = %+v", res)
	}
}

func TestPDFSkipsEmptyPages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The middle page has no text but is a page, and the raster pass gets a raster for
	// it: the count the fan-out cuts ranges by is the document's, not the text's.
	runner := &fakeRunner{fn: pdfFn(t, []string{"first page", "", "third page"}, 3)}
	p := &Processor{cfg: testConfig(), sink: DirSink{Root: t.TempDir()},
		pdf: &PDF{Text: pathProgram("pdftotext"), Raster: pathProgram("pdftoppm"), DPI: 72, Runner: runner}}

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
