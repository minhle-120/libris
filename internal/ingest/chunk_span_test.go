package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// ── audio ────────────────────────────────────────────────────────

// audioRunner reports a duration and echoes a distinguishable payload per slice.
func audioRunner(duration string) *fakeRunner {
	return &fakeRunner{fn: func(bin string, args []string) ([]byte, error) {
		if strings.HasSuffix(bin, "ffprobe") {
			return []byte(duration + "\n"), nil
		}
		// -ss <t0> -i file -t <len> …
		for i, a := range args {
			if a == "-ss" {
				return []byte("slice@" + args[i+1]), nil
			}
		}
		return []byte("slice"), nil
	}}
}

func TestAudioSlices(t *testing.T) {
	root := t.TempDir()
	a := &Audio{ProbeBin: "ffprobe", SliceBin: "ffmpeg", Runner: audioRunner("75")}

	slices, err := a.Slices(context.Background(), "talk.mp3", DirSink{Root: root}, "src/audio")
	if err != nil {
		t.Fatal(err)
	}
	if len(slices) != 3 {
		t.Fatalf("expected 3 slices of 30s in 75s, got %d", len(slices))
	}
	want := [][2]float64{{0, 30}, {30, 60}, {60, 75}}
	for i, s := range slices {
		if s.T0 != want[i][0] || s.T1 != want[i][1] {
			t.Errorf("slice %d span = %v-%v, want %v-%v", i, s.T0, s.T1, want[i][0], want[i][1])
		}
		if s.Asset.MIME != "audio/wav" || s.Asset.Bytes == 0 {
			t.Errorf("slice %d asset = %+v", i, s.Asset)
		}
		if _, err := os.Stat(s.Asset.Path); err != nil {
			t.Errorf("slice %d not written: %v", i, err)
		}
	}
}

func TestAudioSlicesArgs(t *testing.T) {
	runner := audioRunner("45")
	a := &Audio{ProbeBin: "/usr/bin/ffprobe", SliceBin: "/usr/bin/ffmpeg", Runner: runner}

	if _, err := a.Slices(context.Background(), "in.mp3", DirSink{Root: t.TempDir()}, "p"); err != nil {
		t.Fatal(err)
	}

	probe := runner.calls[0]
	if !strings.Contains(strings.Join(probe.args, " "), "format=duration") {
		t.Errorf("probe args = %v", probe.args)
	}
	// Every slice is decoded with an input-side seek and an output-side limit,
	// and written as 16 kHz mono PCM so llama.cpp needs no conversion.
	spans := [][2]string{{"0.000", "30.000"}, {"30.000", "15.000"}}
	for i, call := range runner.calls[1:] {
		args := strings.Join(call.args, " ")
		want := "-ss " + spans[i][0] + " -i in.mp3 -t " + spans[i][1]
		if !strings.Contains(args, want) {
			t.Errorf("slice %d args = %s, want %q", i, args, want)
		}
		for _, fixed := range []string{"-ar 16000 -ac 1", "pcm_s16le", "-f wav -"} {
			if !strings.Contains(args, fixed) {
				t.Errorf("slice %d args = %s, missing %q", i, args, fixed)
			}
		}
		if len(runner.calls) != 3 {
			t.Errorf("expected one probe and two slices, got %d calls", len(runner.calls))
		}
		if strings.Contains(args, "-y ") || strings.Contains(args, "--overwrite") {
			t.Errorf("args = %s, must not write to the original", args)
		}
	}
}

func TestAudioNoTools(t *testing.T) {
	a := &Audio{ProbeBin: "ffprobe"} // SliceBin missing
	if _, err := a.Slices(context.Background(), "x", DirSink{Root: t.TempDir()}, "p"); !errors.Is(err, ErrNoAudioTools) {
		t.Fatalf("err = %v", err)
	}
	if _, err := (&Audio{}).Duration(context.Background(), "x"); !errors.Is(err, ErrNoAudioTools) {
		t.Fatalf("err = %v", err)
	}
}

func TestAudioDuration(t *testing.T) {
	tests := map[string]float64{
		"12.5":    12.5,
		"N/A":     0,
		"-1":      0,
		"nan":     0,
		"garbage": 0,
	}
	for reported, want := range tests {
		a := &Audio{ProbeBin: "ffprobe", Runner: audioRunner(reported)}
		got, err := a.Duration(context.Background(), "x")
		if reported == "garbage" {
			if err == nil {
				t.Errorf("%q: expected an error", reported)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", reported, err)
			continue
		}
		if got != want {
			t.Errorf("%q = %v, want %v", reported, got, want)
		}
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

func TestAudioSinkFailure(t *testing.T) {
	a := &Audio{ProbeBin: "ffprobe", SliceBin: "ffmpeg", Runner: audioRunner("35")}
	_, err := a.Slices(context.Background(), "x", failSink{err: errors.New("disk full")}, "p")
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
}

func TestAudioNoPlayableDuration(t *testing.T) {
	a := &Audio{ProbeBin: "ffprobe", SliceBin: "ffmpeg", Runner: audioRunner("N/A")}
	slices, err := a.Slices(context.Background(), "x", DirSink{Root: t.TempDir()}, "p")
	if err != nil || slices != nil {
		t.Fatalf("slices = %v, err = %v", slices, err)
	}
}

func TestAudioProbeError(t *testing.T) {
	runner := &fakeRunner{fn: func(string, []string) ([]byte, error) {
		return nil, errors.New("boom")
	}}
	a := &Audio{ProbeBin: "ffprobe", SliceBin: "ffmpeg", Runner: runner}
	if _, err := a.Slices(context.Background(), "x", DirSink{Root: t.TempDir()}, "p"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAudioSliceError(t *testing.T) {
	runner := &fakeRunner{fn: func(bin string, _ []string) ([]byte, error) {
		if strings.HasSuffix(bin, "ffprobe") {
			return []byte("60\n"), nil
		}
		return nil, errors.New("cannot decode")
	}}
	a := &Audio{ProbeBin: "ffprobe", SliceBin: "ffmpeg", Runner: runner}
	if _, err := a.Slices(context.Background(), "x", DirSink{Root: t.TempDir()}, "p"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAudioRunawayDurationIsCapped(t *testing.T) {
	root := t.TempDir()
	a := &Audio{ProbeBin: "ffprobe", SliceBin: "ffmpeg", SampleRate: 8000,
		Runner: audioRunner("1e9")}

	slices, err := a.Slices(context.Background(), "x", DirSink{Root: root}, "p")
	if err == nil {
		t.Fatalf("expected the slice cap to trip after %d slices", maxAudioSlices)
	}
	if len(slices) != maxAudioSlices {
		t.Fatalf("slices = %d, want %d", len(slices), maxAudioSlices)
	}
}

// ── pipeline ─────────────────────────────────────────────────────

func TestProcessAudioSlices(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	p, err := New(cfg, DirSink{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	p.audio = &Audio{ProbeBin: "ffprobe", SliceBin: "ffmpeg", Runner: audioRunner("65")}

	path := filepath.Join(root, "talk.mp3")
	if err := os.WriteFile(path, []byte("fake mp3 payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unsupported != "" {
		t.Fatalf("unsupported = %q", res.Unsupported)
	}
	if len(res.Docs) != 1 || res.Docs[0].Kind != domain.KindAudio {
		t.Fatalf("docs = %+v", res.Docs)
	}
	if len(res.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(res.Segments))
	}
	for i, s := range res.Segments {
		if s.Kind != domain.KindAudio || s.Asset == nil {
			t.Fatalf("segment %d = %+v", i, s)
		}
		if s.Text != "" {
			t.Errorf("segment %d has text %q; transcription does not exist yet", i, s.Text)
		}
		if s.Span.T1 <= s.Span.T0 {
			t.Errorf("segment %d span = %+v", i, s.Span)
		}
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "transcript") {
		t.Errorf("warnings = %v, want a note about transcription", res.Warnings)
	}
}

func TestProcessAudioMissingTools(t *testing.T) {
	root := t.TempDir()
	p, _ := New(config.Default(), DirSink{Root: root})
	p.audio = &Audio{} // nothing on PATH

	path := filepath.Join(root, "a.mp3")
	if err := os.WriteFile(path, []byte("RIFF\x00\x00\x00\x00WAVEfmt "), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Unsupported, "ffmpeg") {
		t.Fatalf("unsupported = %q", res.Unsupported)
	}
}

func TestProcessAudioUndecodable(t *testing.T) {
	root := t.TempDir()
	p, _ := New(config.Default(), DirSink{Root: root})
	p.audio = &Audio{ProbeBin: "ffprobe", SliceBin: "ffmpeg", Runner: audioRunner("N/A")}

	path := filepath.Join(root, "bad.mp3")
	if err := os.WriteFile(path, []byte("not audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// ffprobe reports N/A for this file: no slices, and a reason that says so
	// rather than a warning nobody can act on.
	if !strings.Contains(res.Unsupported, "duration") {
		t.Fatalf("unsupported = %q", res.Unsupported)
	}
	if len(res.Segments) != 0 {
		t.Fatalf("segments = %d", len(res.Segments))
	}
}

// The provenance document and the citation link must agree on field names.
// A file that decodes as audio but whose probe fails must still report a
// reason and the tool's own complaint, and must not abort the batch.
func TestProcessAudioProbeFailure(t *testing.T) {
	root := t.TempDir()
	p, _ := New(config.Default(), DirSink{Root: root})
	p.audio = &Audio{ProbeBin: "ffprobe", SliceBin: "ffmpeg",
		Runner: &fakeRunner{fn: func(string, []string) ([]byte, error) {
			return nil, errors.New("Invalid data found when processing input")
		}}}

	path := filepath.Join(root, "broken.mp3")
	if err := os.WriteFile(path, []byte("not really mp3"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unsupported != "audio could not be decoded" {
		t.Fatalf("unsupported = %q", res.Unsupported)
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "Invalid data") {
		t.Errorf("warnings = %v, want ffprobe's message", res.Warnings)
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
