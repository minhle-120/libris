package ingest

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"libris/internal/config"
	"libris/internal/domain"
)

// ── page spans ───────────────────────────────────────────────────

func TestJoinPages(t *testing.T) {
	joined, runs := JoinPages([]string{"alpha", "", "bravo charlie", "delta"})

	if joined != "alpha\nbravo charlie\ndelta" {
		t.Fatalf("joined = %q", joined)
	}
	want := []PageRun{{Page: 1, Start: 0, End: 5}, {Page: 3, Start: 6, End: 19}, {Page: 4, Start: 20, End: 25}}
	if len(runs) != len(want) {
		t.Fatalf("runs = %+v", runs)
	}
	for i, r := range runs {
		if r != want[i] {
			t.Errorf("run %d = %+v, want %+v", i, r, want[i])
		}
	}
}

func TestMapSpan(t *testing.T) {
	joined, runs := JoinPages([]string{"aaaa bbbb", "cccc dddd"})

	tests := []struct {
		name       string
		start, end int
		want       domain.Provenance
	}{
		{"inside page 1", 5, 9, domain.Provenance{Page: 1, CharStart: 5, CharEnd: 9}},
		// Page 2 starts at byte 10 of the join, one past the separator.
		{"inside page 2", 12, 16, domain.Provenance{Page: 2, CharStart: 2, CharEnd: 6}},
		{"straddles", 7, 14, domain.Provenance{Page: 1, PageEnd: 2, CharStart: 7, CharEnd: 4}},
		{"empty", 3, 3, domain.Provenance{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MapSpan(runs, tc.start, tc.end); got != tc.want {
				t.Fatalf("MapSpan(%d,%d) = %+v, want %+v", tc.start, tc.end, got, tc.want)
			}
		})
	}

	// A straddling span's offsets must index their own page, not the join.
	span := MapSpan(runs, 7, 14)
	if got := joined[:span.CharStart]; got != "aaaa bb" {
		t.Errorf("page 1 prefix = %q", got)
	}
	if got := joined[runs[1].Start : runs[1].Start+span.CharEnd]; got != "cccc" {
		t.Errorf("page 2 prefix = %q", got)
	}
}

func TestMapSpanNoRuns(t *testing.T) {
	if got := MapSpan(nil, 0, 5); got != (domain.Provenance{}) {
		t.Fatalf("got %+v", got)
	}
}

// words builds n words of three letters each, so word and byte counts are easy
// to reason about.
func words(n int) string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, "w"+strconv.Itoa(i%1000))
	}
	return strings.Join(out, " ")
}

// A chunk that crosses a page boundary must report both pages and stay
// page-local in its offsets. This is the behaviour the citation link needs.
func TestPagedTextSegmentsStraddle(t *testing.T) {
	cfg := config.Default() // chunkWords 100, chunkOverlap 50
	pages := []string{words(120), words(120), words(30)}

	segs := pagedTextSegments(Doc{ID: "doc", Title: "t", Kind: domain.KindText}, "src", pages, cfg)
	if len(segs) < 3 {
		t.Fatalf("expected several chunks, got %d", len(segs))
	}

	var straddle []domain.Segment
	for _, s := range segs {
		if s.Span.PageEnd == 0 {
			continue
		}
		straddle = append(straddle, s)
		if s.Span.PageEnd != s.Span.Page+1 {
			t.Errorf("span %d→%d is not adjacent", s.Span.Page, s.Span.PageEnd)
		}
		if s.Span.CharStart < 0 || s.Span.CharEnd <= 0 {
			t.Errorf("span offsets out of range: %+v", s.Span)
		}
	}
	if len(straddle) == 0 {
		t.Fatal("no chunk straddled a page break; overlap across pages is not working")
	}

	// Ordinals stay dense and reading order holds.
	for i, s := range segs {
		if s.Ordinal != i {
			t.Fatalf("ordinal %d at index %d", s.Ordinal, i)
		}
		if !strings.Contains(s.Text, "w") {
			t.Fatalf("segment %d has no words: %q", i, s.Text)
		}
	}
}

// Offsets must resolve inside the page's own text, never past its end.
func TestPagedTextSegmentsOffsetsArePageLocal(t *testing.T) {
	pages := []string{words(120), words(40)}
	joined, runs := JoinPages(pages)
	segs := pagedTextSegments(Doc{ID: "d"}, "s", pages, config.Default())

	for _, s := range segs {
		first := runFor(runs, s.Span.Page)
		last := first
		if s.Span.PageEnd != 0 {
			last = runFor(runs, s.Span.PageEnd)
		}
		// The invariant a citation link depends on: CharStart counts from the
		// start page and CharEnd from the end page, so the two of them
		// reconstruct the chunk exactly.
		want := joined[first.Start+s.Span.CharStart : last.Start+s.Span.CharEnd]
		if want != s.Text {
			t.Errorf("span %+v does not reconstruct the text:\n got %q\nwant %q", s.Span, want, s.Text)
		}
		if s.Span.CharEnd > last.End-last.Start {
			t.Errorf("CharEnd %d exceeds page %d length %d",
				s.Span.CharEnd, last.Page, last.End-last.Start)
		}
	}
}

func runFor(runs []PageRun, page int) PageRun {
	for _, r := range runs {
		if r.Page == page {
			return r
		}
	}
	panic("no run for page " + strconv.Itoa(page))
}

func TestPagedTextSegmentsSkipsEmptyPages(t *testing.T) {
	segs := pagedTextSegments(Doc{ID: "d"}, "s", []string{"", "", words(10)}, config.Default())
	if len(segs) != 1 || segs[0].Span.Page != 3 {
		t.Fatalf("segments = %+v", segs)
	}
}

func TestPagedTextSegmentsAllEmpty(t *testing.T) {
	if got := pagedTextSegments(Doc{ID: "d"}, "s", []string{"", "  "}, config.Default()); len(got) != 0 {
		t.Fatalf("got %d segments", len(got))
	}
}

// locatePage's clamp only matters for runs that do not start at 0, which
// JoinPages never builds — but the function is reachable with hand-built runs.
func TestLocatePageBeforeFirstRun(t *testing.T) {
	runs := []PageRun{{Page: 4, Start: 5, End: 9}}
	if got := locatePage(runs, 1); got.Page != 4 {
		t.Fatalf("got page %d", got.Page)
	}
}

func TestProvenanceJSONSpan(t *testing.T) {
	b, err := json.Marshal(domain.Provenance{Page: 7, PageEnd: 8, CharStart: 20, CharEnd: 5})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"page":7`, `"page_end":8`, `"char_start":20`, `"char_end":5`} {
		if !strings.Contains(got, want) {
			t.Errorf("json = %s, missing %s", got, want)
		}
	}

	// A single-page span must not emit page_end at all.
	b, _ = json.Marshal(domain.Provenance{Page: 7})
	if strings.Contains(string(b), "page_end") {
		t.Errorf("json = %s, page_end should be omitted", b)
	}
}
