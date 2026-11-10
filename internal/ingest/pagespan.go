package ingest

import (
	"sort"
	"strings"

	"libris/internal/domain"
)

// PageRun is one page's byte range inside a concatenation of page texts.
type PageRun struct {
	Page  int // 1-based, as in domain.Provenance
	Start int // inclusive byte offset in the joined text
	End   int // exclusive
}

// JoinPages concatenates page texts in document order, newline separated, and
// returns the byte range of every non-empty page.
//
// Chunking the joined text rather than each page in isolation is what lets a
// chunk straddle a page break; the runs are what let its citation still name
// the right pages. Empty pages are skipped so their newlines cannot become a
// chunk's whole content.
func JoinPages(pages []string) (string, []PageRun) {
	var (
		joined strings.Builder
		runs   []PageRun
	)
	for i, page := range pages {
		if page == "" {
			continue
		}
		if joined.Len() > 0 {
			joined.WriteByte('\n')
		}
		start := joined.Len()
		joined.WriteString(page)
		runs = append(runs, PageRun{Page: i + 1, Start: start, End: joined.Len()})
	}
	return joined.String(), runs
}

// MapSpan resolves a byte range of the joined text to page-local provenance.
// start must be the first byte of a word and end one past the last, which is
// what SplitText guarantees — so neither offset can land in the separator
// between two pages.
func MapSpan(runs []PageRun, start, end int) domain.Provenance {
	if len(runs) == 0 || end <= start {
		return domain.Provenance{}
	}

	first := locatePage(runs, start)
	last := locatePage(runs, end-1)

	span := domain.Provenance{
		Page:      first.Page,
		CharStart: start - first.Start,
		CharEnd:   end - last.Start,
	}
	if last.Page != first.Page {
		span.PageEnd = last.Page
	}
	return span
}

// locatePage returns the run containing off. Runs are ordered and disjoint, so
// the answer is the last one starting at or before off.
func locatePage(runs []PageRun, off int) PageRun {
	i := sort.Search(len(runs), func(i int) bool { return runs[i].Start > off }) - 1
	if i < 0 {
		i = 0
	}
	return runs[i]
}
