package ingest

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"libris/internal/domain"
	"libris/internal/tools"
)

// CorpusEnv names a folder of real recordings to slice. This is the only test in the suite
// that runs ffmpeg on samples nobody generated, and the only gate that keeps the window
// arithmetic honest about a real container: the synthetic stream in audio_test.go proves the
// arithmetic and says nothing about what a decoder hands it.
const CorpusEnv = "LIBRIS_AUDIO_CORPUS"

// wavHeaderBytes is the fixed header ingest writes before a clip's samples, which is where
// the samples begin and what every byte offset in this file is measured from.
const wavHeaderBytes = 44

// corpusMinutes is how much recording the corpus has to hold for a run to be worth its time.
// Below that the test says so rather than passing: one song exercises the header and the first
// window, and the failures this guards against are the late ones — a buffer trimmed a sample
// too far, a hop that drifts over an hour, a tail nobody wrote.
const corpusMinutes = 15

// TestClipsOverRealRecordings slices every recording in the corpus and checks what a listener
// would notice if the arithmetic were wrong: part of the recording unheard, a sentence at a
// seam in neither clip, or clips numbered with holes in them.
//
// It is skipped unless LIBRIS_AUDIO_CORPUS names a folder, because the recordings that would
// do are a person's rather than the repository's. Run it before touching Clips:
//
//	LIBRIS_AUDIO_CORPUS=~/Music go test ./internal/ingest/ -run RealRecordings -v
func TestClipsOverRealRecordings(t *testing.T) {
	dir := os.Getenv(CorpusEnv)
	if dir == "" {
		t.Skipf("set %s to a folder of recordings", CorpusEnv)
	}

	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // a folder this walk is not sure about is not its business
		}
		// The same question the pipeline asks, on the same bytes: a test that sliced a file
		// ingest would refuse is measuring something else.
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		head := make([]byte, 512)
		n, _ := io.ReadFull(f, head)
		if Detect(d.Name(), head[:n]) == domain.SourceAudio {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("%s=%s holds no recording libris would accept", CorpusEnv, dir)
	}
	sort.Strings(files)

	// The same numbers the settings ship: a 15-second clip, half of it repeated.
	const (
		clip        = 15.0
		hop         = clip * (1 - 0.5) // where the next clip starts
		overlapWant = clip - hop       // what neighbours must share
	)

	audio := &Audio{
		FFmpeg:  tools.FromPath().Audio().FFmpeg,
		Seconds: int(clip),
		Overlap: 50,
	}
	if !audio.FFmpeg.Found() {
		t.Skip("no ffmpeg on this machine")
	}

	var (
		heard  time.Duration
		nClips int
	)
	for _, path := range files {
		// Deliberately not parallel: the corpus total is what the parent reports, and a
		// parent cannot see what its parallel subtests did — it returns before they run.
		// ffmpeg fills the cores by itself.
		t.Run(filepath.Base(path), func(t *testing.T) {
			root := t.TempDir()
			got, err := audio.Clips(t.Context(), path, &DirSink{Root: root}, "corpus")
			if err != nil {
				t.Fatalf("Clips: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("a real recording sliced to nothing")
			}

			// In the order a listener meets them.
			slices.SortFunc(got, func(a, b Clip) int { return cmp.Compare(a.T0, b.T0) })

			furthest := 0.0
			for i, c := range got {
				// Numbered without holes: the names are what a reader sees beside the
				// recording, and a gap reads as a clip that got lost.
				if want := fmt.Sprintf("clip-%04d.wav", i+1); !strings.HasSuffix(c.Asset.Path, want) {
					t.Errorf("clip %d is %q, want it to end in %q", i, c.Asset.Path, want)
				}
				if c.T1 <= c.T0 {
					t.Errorf("clip %d spans %g–%g", i, c.T0, c.T1)
					continue
				}

				// What two clips must share, and what may be missing between them,
				// depends on how many windows were trimmed since the last one was kept.
				// Silence trimming is the only thing allowed to remove samples, so the
				// seam checks are the proof that it removes nothing but silence.
				if i == 0 {
					if c.T0 != 0 {
						t.Errorf("first clip starts at %gs, want 0", c.T0)
					}
				} else {
					prev := got[i-1]
					seam(t, prev, c, hop, overlapWant)
				}

				checkClipWAV(t, c)

				if c.T1 > furthest {
					furthest = c.T1
				}
			}

			heard += time.Duration(furthest * float64(time.Second))
			nClips += len(got)
			t.Logf("%d clips, %.0fs of recording", len(got), furthest)
		})
	}

	if minutes := heard.Minutes(); minutes < corpusMinutes {
		t.Errorf("the corpus gave %.1f minutes of recording; this test covers %d."+
			" Point %s at a bigger folder.", minutes, corpusMinutes, CorpusEnv)
	}
	t.Logf("%.0f minutes of recording, %d clips", heard.Minutes(), nClips)
}

// seam is one boundary between two stored clips.
//
// The window arithmetic is a hop, so two clips start a whole number of hops apart. Anything
// else is drift, and drift is the silent failure this whole test exists to catch: it shifts
// every later clip's citation by a fraction of a second while every other check stays green.
//
// Then, according to how many windows between them were dropped for silence:
//
//   - none: the pair are neighbours in the arithmetic too, and must share exactly the
//     overlap the setting asked for — that sharing is why a sentence straddling a
//     boundary survives whole in one of them;
//   - one: the two may meet exactly, and the window they would have shared is exactly the
//     samples at the end of one file plus the start of the other. Those are read back and
//     checked, so "it was silent" is a measurement here rather than a story about the code;
//   - two or more: those windows are the only thing that can account for a hole, so the
//     hole is checked against them. A hole larger than the silence that justifies it is
//     a hole in the recording.
func seam(t *testing.T, prev, next Clip, hop, overlapWant float64) {
	t.Helper()

	delta := next.T0 - prev.T0
	units := delta / hop
	dropped := int(math.Round(units)) - 1
	if math.Abs(units-float64(dropped+1)) > 1e-3 {
		t.Fatalf("clips %g–%gs and %g–%gs start %.3fs apart, which is not a whole"+
			" number of %gs hops", prev.T0, prev.T1, next.T0, next.T1, delta, hop)
	}
	switch {
	case dropped < 0:
		t.Fatalf("clip %g–%gs starts before the previous one's window even began", next.T0, next.T1)
	case dropped == 0:
		if shared := prev.T1 - next.T0; shared < overlapWant-0.01 {
			t.Errorf("clips %g–%gs and %g–%gs share %.2fs, want %.1fs",
				prev.T0, prev.T1, next.T0, next.T1, shared, overlapWant)
		}
	case dropped == 1:
		if hole := next.T0 - prev.T1; hole > 0.001 {
			t.Fatalf("clips %g–%gs and %g–%gs leave %.2fs unheard, and only one window was"+
				" dropped between them", prev.T0, prev.T1, next.T0, next.T1, hole)
		}
		seamWasSilent(t, prev, next, overlapWant)
	default:
		if hole, allow := next.T0-prev.T1, float64(dropped-1)*hop; hole > allow+0.001 {
			t.Fatalf("clips %g–%gs and %g–%gs leave %.2fs unheard; the %d silent windows between"+
				" them account for %.1fs", prev.T0, prev.T1, next.T0, next.T1, hole, dropped, allow)
		}
	}
}

// seamWasSilent reads the trimmed window back out of the two clips that replaced it and
// measures it. It is the same bytes the slicer measured, because the slicer writes the clips:
// a window dropped for silence has its first half at the tail of the clip before it and its
// second half at the head of the clip after.
func seamWasSilent(t *testing.T, prev, next Clip, seconds float64) {
	t.Helper()

	span := int64(seconds * sampleRate * bytesPerSample)
	tail, err := readRange(prev.Asset.Path, -span, span)
	if err != nil {
		t.Fatalf("tail of %s: %v", prev.Asset.ID, err)
	}
	head, err := readRange(next.Asset.Path, wavHeaderBytes, span)
	if err != nil {
		t.Fatalf("head of %s: %v", next.Asset.ID, err)
	}
	if p := peak(append(tail, head...)); p >= audiblePeak {
		t.Errorf("the window trimmed between clips %g–%gs and %g–%gs peaks at %d,"+
			" which is audible (the threshold is %d) — so something was dropped that should"+
			" have been kept", prev.T0, prev.T1, next.T0, next.T1, p, audiblePeak)
	}
}

// readRange reads n bytes of a file from offset, which may be negative to count back from
// the end.
func readRange(path string, offset, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if offset < 0 {
		size, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, err
		}
		offset = size + offset
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// checkClipWAV asserts the stored clip's own header agrees with its span and with what libris
// promises a clip is: 16 kHz, one channel, 16-bit PCM. A clip the browser will not open is a
// citation that plays silence, and nothing else in the pipeline would notice.
func checkClipWAV(t *testing.T, c Clip) {
	t.Helper()
	raw, err := os.ReadFile(c.Asset.Path)
	if err != nil {
		t.Fatalf("clip: %v", err)
	}
	if len(raw) < wavHeaderBytes {
		t.Fatalf("clip is %d bytes, shorter than a WAV header", len(raw))
	}
	if string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		t.Fatalf("clip is %q…%q, not a WAV", raw[0:4], raw[8:12])
	}
	if r := binary.LittleEndian.Uint32(raw[24:]); r != sampleRate {
		t.Errorf("clip plays at %d Hz, want %d", r, sampleRate)
	}
	if ch := binary.LittleEndian.Uint16(raw[22:]); ch != 1 {
		t.Errorf("clip has %d channels, want 1", ch)
	}
	if bps := binary.LittleEndian.Uint16(raw[34:]); bps != 16 {
		t.Errorf("clip is %d-bit, want 16", bps)
	}
	// The span in the database and the samples in the file are two claims about one window,
	// checked against each other rather than against a constant.
	samples := int64(len(raw)-wavHeaderBytes) / bytesPerSample
	if got, want := float64(samples)/sampleRate, c.T1-c.T0; got < want-0.01 || got > want+0.01 {
		t.Errorf("file holds %.3fs, the span says %.3fs", got, want)
	}
}
