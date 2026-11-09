package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"libris/internal/domain"
)

var (
	ErrNoPDFText   = errors.New("ingest: pdftotext not found on PATH")
	ErrNoPDFRaster = errors.New("ingest: pdftoppm not found on PATH")
)

// Runner abstracts process execution so PDF handling is testable without
// poppler installed.
type Runner interface {
	Run(ctx context.Context, bin string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", bin, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", bin, err)
	}
	return out, nil
}

// PDF turns one PDF into its two derived documents: page text, and page
// rasters at the configured DPI. Both are produced because a text layer is not
// reliable — slides, scans and figure-heavy papers need the image.
type PDF struct {
	TextBin   string // pdftotext
	RasterBin string // pdftoppm
	DPI       int
	Runner    Runner
}

func (p *PDF) runner() Runner {
	if p.Runner != nil {
		return p.Runner
	}
	return execRunner{}
}

// Pages extracts text, one string per page, in document order.
func (p *PDF) Pages(ctx context.Context, path string) ([]string, error) {
	if p.TextBin == "" {
		return nil, ErrNoPDFText
	}

	out, err := p.runner().Run(ctx, p.TextBin, "-enc", "UTF-8", path, "-")
	if err != nil {
		return nil, err
	}

	// pdftotext separates pages with form feeds and terminates the last one.
	pages := strings.Split(string(out), "\f")
	if n := len(pages); n > 0 && strings.TrimSpace(pages[n-1]) == "" {
		pages = pages[:n-1]
	}
	for i := range pages {
		pages[i] = normaliseText(pages[i])
	}
	return pages, nil
}

// PageImages rasterises every page and hands each PNG to the sink under
// "<prefix>/page-0001.png", preserving document order.
func (p *PDF) PageImages(ctx context.Context, path string, sink Sink, prefix string) ([]domain.Asset, error) {
	if p.RasterBin == "" {
		return nil, ErrNoPDFRaster
	}

	dir, err := os.MkdirTemp("", "libris-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	stem := filepath.Join(dir, "page")
	args := []string{"-png", "-r", strconv.Itoa(p.DPI), path, stem}
	if _, err := p.runner().Run(ctx, p.RasterBin, args...); err != nil {
		return nil, err
	}

	matches, err := filepath.Glob(stem + "-*.png")
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("ingest: %s produced no pages", p.RasterBin)
	}
	sort.Slice(matches, func(i, j int) bool {
		return pageNumber(matches[i]) < pageNumber(matches[j])
	})

	assets := make([]domain.Asset, 0, len(matches))
	for i, src := range matches {
		name := fmt.Sprintf("%s/page-%04d.png", prefix, i+1)
		asset, err := sink.PutFile(ctx, name, src)
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

// pageNumber reads the trailing integer of "…/page-12.png".
func pageNumber(path string) int {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if i := strings.LastIndexByte(base, '-'); i >= 0 {
		base = base[i+1:]
	}
	n, err := strconv.Atoi(base)
	if err != nil {
		return 0
	}
	return n
}

func lookPath(bin string) string {
	if bin == "" {
		return ""
	}
	p, err := exec.LookPath(bin)
	if err != nil {
		return ""
	}
	return p
}
