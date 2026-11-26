package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"libris/internal/domain"
	"libris/internal/tools"
)

// The programs may be absent in either place they are looked for, and that is reported rather
// than guessed around: a page with no text and a page with no image are different things from
// a page that failed.
var (
	ErrNoPDFText   = errors.New("ingest: no pdftotext on this machine")
	ErrNoPDFRaster = errors.New("ingest: no pdftoppm on this machine")
)

// Runner abstracts process execution so PDF handling is testable without poppler installed.
// It is handed a resolved program rather than a name because where the program came from is
// what decides whether it needs an environment of its own.
type Runner interface {
	Run(ctx context.Context, prog tools.Program, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, prog tools.Program, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, prog.Path, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if env := prog.Env(os.Environ()); len(env) != 0 {
		cmd.Env = env
	}

	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", prog.Name, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", prog.Name, err)
	}
	return out, nil
}

// PDF turns one PDF into its two derived documents: page text, and page
// rasters at the configured DPI. Both are produced because a text layer is not
// reliable — slides, scans and figure-heavy papers need the image.
//
// The programs arrive resolved rather than being looked up here: which folder a program came
// from belongs to the module that knows about folders, and looking it up here would spread
// that rule across the two places that would then have to agree.
type PDF struct {
	Text   tools.Program
	Raster tools.Program
	DPI    int
	Runner Runner

	// Shards is how many processes one document may be rasterised by. Zero — what
	// the wiring leaves it at — takes the machine's own count, because a number
	// anyone wrote down would be wrong on the next machine. It is settable for the
	// same reason as every other field here: the tests want to watch a fan-out on a
	// box of any size.
	Shards int
}

func (p *PDF) runner() Runner {
	if p.Runner != nil {
		return p.Runner
	}
	return execRunner{}
}

// Pages extracts text, one string per page, in document order.
func (p *PDF) Pages(ctx context.Context, path string) ([]string, error) {
	if !p.Text.Found() {
		return nil, ErrNoPDFText
	}

	out, err := p.runner().Run(ctx, p.Text, "-enc", "UTF-8", path, "-")
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
//
// The document is rendered by several processes over page ranges of the same file,
// because pdftoppm renders one document on one thread: poppler has no page-level
// parallelism and no flag that adds one, so a long scan pins one core of a
// twelve-core machine while the model server waits for rasters and the other eleven
// cores idle. Pages render independently of each other, so ranges across one file
// scale until memory bandwidth.
//
// The file is not split to get them. pdfseparate and friends rewrite each page's
// resources into its own PDF, which duplicates a scan's images once per output file,
// and a render that happens once never re-reads a page anyway. What -f/-l does pay
// is re-parsing the page tree per process, which poppler walks without rendering.
//
// pages is the page count where the caller knows it — it does when the text pass
// worked — and 0 where it does not. Cutting ranges needs the count, so an unknown
// one falls back to rendering the document in one process.
func (p *PDF) PageImages(ctx context.Context, path string, sink Sink, prefix string, pages int) ([]domain.Asset, error) {
	if !p.Raster.Found() {
		return nil, ErrNoPDFRaster
	}

	dir, err := os.MkdirTemp("", "libris-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	ranges := rasterRanges(pages, p.shards())
	if err := p.rasterAll(ctx, dir, path, ranges); err != nil {
		return nil, err
	}

	matches, err := collectPages(dir, ranges)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("ingest: %s produced no pages", p.Raster.Name)
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

// rasterAll runs the ranges, concurrently when there is more than one.
//
// One failed range fails the document, and the rest are cancelled rather than left
// to finish a render nobody will use: a range that died means the page count cannot
// come out right however long the others run.
func (p *PDF) rasterAll(ctx context.Context, dir, path string, ranges []pageRange) error {
	if len(ranges) == 1 {
		return p.rasterRange(ctx, dir, path, ranges[0])
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed error
	)
	for i, r := range ranges {
		sub := filepath.Join(dir, fmt.Sprintf("r%d", i))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.rasterRange(ctx, sub, path, r); err != nil {
				mu.Lock()
				if failed == nil {
					failed = err
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return failed
}

// rasterRange renders one span of a document into dir as "page-<n>.png".
//
// The poppler output name carries the page's own number whatever range asked for it,
// which is what lets pages from different processes be sorted into document order
// afterwards — and what makes the count below the only guard against a short
// document, since a missing page and a missing page number look the same from here.
func (p *PDF) rasterRange(ctx context.Context, dir, path string, r pageRange) error {
	stem := filepath.Join(dir, "page")
	args := []string{"-png", "-r", strconv.Itoa(p.DPI)}
	if r.first > 0 {
		args = append(args, "-f", strconv.Itoa(r.first), "-l", strconv.Itoa(r.last))
	}
	args = append(args, path, stem)

	if _, err := p.runner().Run(ctx, p.Raster, args...); err != nil {
		return err
	}
	if r.first == 0 {
		// The whole document, however many pages it turns out to have.
		return nil
	}

	// A range that wrote fewer files than it was asked for is a short document, and
	// nothing else downstream would notice: the page segments are made from whatever
	// rasters arrive, so thirteen pages of a hundred ingests as a happy thirteen.
	files, err := filepath.Glob(stem + "-*.png")
	if err != nil {
		return err
	}
	if want := r.last - r.first + 1; len(files) != want {
		return fmt.Errorf("ingest: %s wrote %d page files for pages %d-%d", p.Raster.Name, len(files), r.first, r.last)
	}
	return nil
}

// collectPages gathers the rasters of every range. Document order is the caller's
// sort by page number, not this order: the padding poppler pads to is its own.
func collectPages(dir string, ranges []pageRange) ([]string, error) {
	var out []string
	for i := range ranges {
		sub := dir
		if len(ranges) > 1 {
			sub = filepath.Join(dir, fmt.Sprintf("r%d", i))
		}
		files, err := filepath.Glob(filepath.Join(sub, "page-*.png"))
		if err != nil {
			return nil, err
		}
		out = append(out, files...)
	}
	return out, nil
}

// pageRange is the inclusive span of pages one pdftoppm process renders. A zero span
// is the whole document, run without the range flags at all.
type pageRange struct{ first, last int }

// rasterRanges cuts a document into at most shards spans, each page in exactly one.
//
// Ranges are near-equal rather than exactly equal: ceil-dividing the page count by
// the shard count keeps the range count at or under the shards, and a last range a
// page short costs less than every process waiting on a rebalanced one. The same
// count for every range also means the padding poppler chooses cannot differ between
// ranges of a hundred-page document, which it would if the sizes straddled 10, 100
// or 1000.
func rasterRanges(pages, shards int) []pageRange {
	if shards < 1 {
		shards = 1
	}
	if pages < 2 || shards == 1 {
		return []pageRange{{}}
	}

	// No range of one page: a process that renders a single page pays for the exec and
	// the page-tree parse with nothing to overlap them with, so the shard count is
	// floored at two pages before it is divided.
	size := max(2, (pages+shards-1)/shards)
	ranges := make([]pageRange, 0, shards)
	for first := 1; first <= pages; first += size {
		ranges = append(ranges, pageRange{first, min(first+size-1, pages)})
	}
	return ranges
}

// shards is how many processes this PDF may be rasterised by.
func (p *PDF) shards() int {
	if p.Shards > 0 {
		return p.Shards
	}
	return rasterShards()
}

// rasterShards is the same reading of the machine the vector scan uses, for the same
// reason: NumCPU is the affinity mask the OS hands this process — what a cpuset or a
// taskset changes — and GOMAXPROCS caps it again when the runtime is confined below
// what the machine has.
//
// The ceiling is shared with the scan's, for a different reason. A shard there is a
// SQLite connection with its own page cache; a shard here is a process holding a
// page's bitmap, some 11 MB at the default DPI and more while a scan's embedded
// image is decoded. Neither is the model server, which is sharing those cores and
// needs them first.
func rasterShards() int {
	n := runtime.NumCPU()
	if p := runtime.GOMAXPROCS(0); p < n {
		n = p
	}
	if n < 1 {
		n = 1
	}
	return min(n, rasterShardCap)
}

// rasterShardCap is the most fan-out one document is allowed, whatever the machine.
const rasterShardCap = 8

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
