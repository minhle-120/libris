package ingest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"libris/internal/config"
	"libris/internal/domain"
)

// A minimal single-page PDF. poppler reads it without an xref table.
const pdfFixture = `%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj
3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj
4 0 obj << /Length 40 >> stream
BT /F1 14 Tf 20 100 Td (Hello PDF page) Tj ET
endstream endobj
5 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj
trailer << /Root 1 0 R /Size 6 >>
%%EOF
`

// This is the only test that runs real poppler binaries. It skips when they are
// not installed, so the unit suite stays hermetic.
func TestPDFEndToEnd(t *testing.T) {
	cfg := config.Default()
	p, err := New(cfg, DirSink{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if p.pdf.TextBin == "" || p.pdf.RasterBin == "" {
		t.Skip("pdftotext/pdftoppm not installed")
	}

	path := filepath.Join(t.TempDir(), "hello.pdf")
	if err := os.WriteFile(path, []byte(pdfFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Unsupported != "" {
		t.Fatalf("unsupported: %s (warnings %v)", res.Unsupported, res.Warnings)
	}

	if len(res.Docs) != 2 {
		t.Fatalf("docs = %+v", res.Docs)
	}

	var text, image *domain.Segment
	for i := range res.Segments {
		switch res.Segments[i].Kind {
		case domain.KindText:
			text = &res.Segments[i]
		case domain.KindImage:
			image = &res.Segments[i]
		}
	}

	if text == nil {
		t.Fatal("no text segment")
	}
	if !strings.Contains(text.Text, "Hello PDF page") {
		t.Fatalf("text = %q", text.Text)
	}
	if text.Span.Page != 1 {
		t.Fatalf("page = %d", text.Span.Page)
	}
	if !strings.Contains(text.Text, "Hello") || text.Span.CharEnd <= text.Span.CharStart {
		t.Fatalf("span = %+v", text.Span)
	}

	if image == nil || image.Asset == nil {
		t.Fatalf("no page image segment: %+v", image)
	}
	meta, err := inspectImage(image.Asset.Path)
	if err != nil {
		t.Fatalf("page image unreadable: %v", err)
	}
	if meta.Format != "png" {
		t.Fatalf("page image format = %q", meta.Format)
	}
}

// The DPI setting is the whole reason PDFs get rasterised, so assert it
// actually reaches pdftoppm.
func TestPDFDPIControlsRasterSize(t *testing.T) {
	sizes := func(dpi int) int {
		cfg := config.Default()
		cfg.PDFDPI = dpi

		p, err := New(cfg, DirSink{Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if p.pdf.RasterBin == "" {
			t.Skip("pdftoppm not installed")
		}

		path := filepath.Join(t.TempDir(), "hello.pdf")
		if err := os.WriteFile(path, []byte(pdfFixture), 0o644); err != nil {
			t.Fatal(err)
		}

		res, err := p.Process(context.Background(), path)
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		for _, seg := range res.Segments {
			if seg.Kind != domain.KindImage || seg.Asset == nil {
				continue
			}
			meta, err := inspectImage(seg.Asset.Path)
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			return meta.Width
		}
		t.Fatal("no page image produced")
		return 0
	}

	// A 200pt-wide page at 72dpi is 200px, at 144dpi 400px.
	low, high := sizes(72), sizes(144)
	if low < 195 || low > 205 {
		t.Errorf("72dpi width = %d, want ~200", low)
	}
	if high < 395 || high > 405 {
		t.Errorf("144dpi width = %d, want ~400", high)
	}
	if high <= low {
		t.Errorf("higher dpi did not enlarge the raster: %d → %d", low, high)
	}
}

// Real ffmpeg, for the same reason: the fake runner proves the argument list,
// only the real binary proves the slices are playable and the right length.
func TestAudioEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "tone.wav")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=65",
		"-ar", "8000", "-ac", "1", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating test audio: %v: %s", err, out)
	}

	p, err := New(config.Default(), DirSink{Root: filepath.Join(dir, "assets")})
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unsupported != "" {
		t.Fatalf("unsupported = %q (warnings %v)", res.Unsupported, res.Warnings)
	}
	if len(res.Segments) != 3 {
		t.Fatalf("65 s of audio should yield 3 slices, got %d", len(res.Segments))
	}

	want := [][2]float64{{0, 30}, {30, 60}, {60, 65}}
	for i, seg := range res.Segments {
		if seg.Kind != domain.KindAudio || seg.Asset == nil {
			t.Fatalf("segment %d = %+v", i, seg)
		}
		if seg.Span.T0 != want[i][0] || seg.Span.T1 != want[i][1] {
			t.Errorf("segment %d span = %v-%v, want %v-%v",
				i, seg.Span.T0, seg.Span.T1, want[i][0], want[i][1])
		}

		data, err := os.ReadFile(seg.Asset.Path)
		if err != nil {
			t.Fatalf("segment %d: %v", i, err)
		}
		if string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
			t.Fatalf("segment %d is not a WAV file (%q)", i, data[:12])
		}
		// 16 kHz mono s16le: 32 KB per second. Only the last slice is short.
		wantBytes := int((seg.Span.T1 - seg.Span.T0) * 32000)
		if len(data) < wantBytes-4000 || len(data) > wantBytes+4000 {
			t.Errorf("segment %d is %d bytes, want ~%d", i, len(data), wantBytes)
		}
	}
}
