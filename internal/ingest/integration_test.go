package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/tools"
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
	p, err := New(cfg, DirSink{Root: t.TempDir()}, tools.FromPath().PDF(), tools.FromPath().Audio())
	if err != nil {
		t.Fatal(err)
	}
	if !p.pdf.Text.Found() || !p.pdf.Raster.Found() {
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
		t.Fatalf("unsupported: %s (findings %v)", res.Unsupported, res.Findings)
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

		p, err := New(cfg, DirSink{Root: t.TempDir()}, tools.FromPath().PDF(), tools.FromPath().Audio())
		if err != nil {
			t.Fatal(err)
		}
		if !p.pdf.Raster.Found() {
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

// The fan-out is unit-tested against a fake. This is the one place it meets real
// poppler: the page number in poppler's own output name is what lets pages from
// different processes be put back in document order, and that is a promise about a
// program we do not own, so it is checked rather than assumed.
func TestPDFRasterisesInParallelAcrossRanges(t *testing.T) {
	cfg := config.Default()
	runs := &countingRunner{}
	p, err := New(cfg, DirSink{Root: t.TempDir()}, tools.FromPath().PDF(), tools.FromPath().Audio())
	if err != nil {
		t.Fatal(err)
	}
	if !p.pdf.Raster.Found() {
		t.Skip("pdftoppm not installed")
	}
	p.pdf.Shards = 3
	p.pdf.Runner = runs

	path := filepath.Join(t.TempDir(), "six.pdf")
	if err := os.WriteFile(path, []byte(multiPagePDF(6)), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	seen := map[int]int{}
	for _, seg := range res.Segments {
		if seg.Kind != domain.KindImage || seg.Asset == nil {
			continue
		}
		seen[seg.Span.Page]++
		if stat, err := os.Stat(seg.Asset.Path); err != nil || stat.Size() == 0 {
			t.Fatalf("page %d raster: %v", seg.Span.Page, err)
		}
	}
	for page := 1; page <= 6; page++ {
		if seen[page] != 1 {
			t.Fatalf("page %d rendered %d times of 6", page, seen[page])
		}
	}

	// Six pages, three shards, two pages each — and the text pass runs too, so the
	// count is only about the rasteriser if the rasters are the majority of the runs.
	if got := runs.pdfRuns(); got != 3 {
		t.Fatalf("%d pdftoppm invocations, want 3", got)
	}
}

// countingRunner passes everything through to the real runner, counting the
// rasterisations on the way.
type countingRunner struct {
	inner execRunner
	mu    sync.Mutex
	pdf   int
}

func (c *countingRunner) Run(ctx context.Context, prog tools.Program, args ...string) ([]byte, error) {
	if strings.Contains(prog.Name, "pdftoppm") {
		c.mu.Lock()
		c.pdf++
		c.mu.Unlock()
	}
	return c.inner.Run(ctx, prog, args...)
}

func (c *countingRunner) pdfRuns() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pdf
}

// multiPagePDF writes a document of n trivial pages. Poppler reads it whether or not
// the offsets are right, so it is built from strings rather than a writer.
func multiPagePDF(n int) string {
	font := 3 + 2*n
	var sb strings.Builder
	sb.WriteString("%PDF-1.4\n")
	sb.WriteString("1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n")

	sb.WriteString("2 0 obj << /Type /Pages /Kids [")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, " %d 0 R", 3+2*(i-1))
	}
	fmt.Fprintf(&sb, " ] /Count %d >> endobj\n", n)

	for i := 1; i <= n; i++ {
		page, content := 3+2*(i-1), 4+2*(i-1)
		body := fmt.Sprintf("BT /F1 14 Tf 20 100 Td (Page %d) Tj ET", i)
		fmt.Fprintf(&sb, "%d 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] "+
			"/Contents %d 0 R /Resources << /Font << /F1 %d 0 R >> >> >> endobj\n", page, content, font)
		fmt.Fprintf(&sb, "%d 0 obj << /Length %d >> stream\n%s\nendstream endobj\n",
			content, len(body), body)
	}

	fmt.Fprintf(&sb, "%d 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj\n", font)
	fmt.Fprintf(&sb, "trailer << /Root 1 0 R /Size %d >>\n%%%%EOF\n", font+1)
	return sb.String()
}
