package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"libris/internal/tools"
)

// The frame slicer's own arithmetic, tested without a decoder: a window that starts a
// hop late shifts every citation in the notebook, and a window past the end of the file
// is a clip the browser shows as a black box. Neither is loud enough to notice in
// normal use, which is the usual reason to write them down as tests.

// videoRunner answers the two programs a video asks for. ffprobe gets one call and
// returns probe; ffmpeg gets one per window, and for each the fake writes whatever
// write says to write at the output path — including nothing, which is what a window
// past the end of a lying container looks like.
type videoRunner struct {
	probe string
	// cut returns the bytes to leave at the window's output path. Fewer than two
	// kilobytes is what a window past the end of the file looks like.
	cut func(window int, args []string) int

	calls [][]string
	cuts  int
}

func (r *videoRunner) Run(_ context.Context, prog tools.Program, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)

	if prog.Name == "ffprobe" {
		return []byte(r.probe), nil
	}
	r.cuts++
	out := args[len(args)-1]
	body := 4096
	if r.cut != nil {
		body = r.cut(r.cuts, args)
		if body < 0 {
			return nil, errors.New("ffmpeg: simulated failure")
		}
	}
	if err := os.WriteFile(out, make([]byte, body), 0o644); err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *videoRunner) cutArgs() [][]string {
	var out [][]string
	for _, c := range r.calls {
		if len(c) > 0 && !slices.Contains(c, "-show_entries") {
			out = append(out, c)
		}
	}
	return out
}

func videoPrograms() (tools.Program, tools.Program) {
	return pathProgram("ffmpeg"), pathProgram("ffprobe")
}

const probeBoth = "6.024000\nvideo\naudio\n"

func TestVideoProbeAsksTheContainerOnce(t *testing.T) {
	runner := &videoRunner{probe: "N/A\n40.000000\nvideo\naudio\n"}
	ffmpeg, ffprobe := videoPrograms()
	v := &Video{FFmpeg: ffmpeg, FFprobe: ffprobe, Runner: runner}

	st, err := v.Probe(context.Background(), "anything.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if st.Seconds != 40 || !st.Video || !st.Audio {
		t.Fatalf("streams = %+v, want 40 seconds of both kinds", st)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("probe calls = %d, want one per file", len(runner.calls))
	}
}

func TestVideoProbeNeedsBothPrograms(t *testing.T) {
	ffmpeg, ffprobe := videoPrograms()
	ffprobe = missingProgram("ffprobe")
	v := &Video{FFmpeg: ffmpeg, FFprobe: ffprobe, Runner: &videoRunner{probe: probeBoth}}

	if _, err := v.Probe(context.Background(), "a.mp4"); !errors.Is(err, ErrNoVideoTools) {
		t.Fatalf("probe = %v, want ErrNoVideoTools", err)
	}
	if _, err := v.Frames(context.Background(), "a.mp4", Streams{Seconds: 6, Video: true}, DirSink{Root: t.TempDir()}, "c"); !errors.Is(err, ErrNoVideoTools) {
		t.Fatalf("frames = %v, want ErrNoVideoTools", err)
	}
}

func TestVideoWindowsAreTheSettings(t *testing.T) {
	const (
		seconds = 40.0
		clip    = 15
		overlap = 50
	)
	runner := &videoRunner{probe: probeBoth}
	ffmpeg, ffprobe := videoPrograms()
	v := &Video{FFmpeg: ffmpeg, FFprobe: ffprobe, Seconds: clip, Overlap: overlap, Runner: runner}
	sink := DirSink{Root: t.TempDir()}

	clips, err := v.Frames(context.Background(), "a.mp4", Streams{Seconds: seconds, Video: true}, sink, "src/clips")
	if err != nil {
		t.Fatal(err)
	}

	// A hop of half a window over forty seconds: 0, 7.5, 15, 22.5 hold whole windows, and
	// the ten seconds the last one did not reach are the tail — a final scene that
	// straddled a boundary is in both, as it is everywhere else.
	want := [][2]float64{{0, 15}, {7.5, 22.5}, {15, 30}, {22.5, 37.5}, {30, 40}}
	if len(clips) != len(want) {
		t.Fatalf("clips = %d, want %d", len(clips), len(want))
	}
	for i, c := range clips {
		if c.T0 != want[i][0] || c.T1 != want[i][1] {
			t.Errorf("clip %d span = %v–%v, want %v–%v", i+1, c.T0, c.T1, want[i][0], want[i][1])
		}
		if c.Asset.MIME != "video/mp4" {
			t.Errorf("clip %d mime = %q, want video/mp4", i+1, c.Asset.MIME)
		}
		if !strings.Contains(c.Asset.Path, fmt.Sprintf("clip-%04d", i+1)) {
			t.Errorf("clip %d path = %q, want it numbered in order", i+1, c.Asset.Path)
		}
	}

	cuts := runner.cutArgs()
	if len(cuts) != len(want) {
		t.Fatalf("ffmpeg ran %d times, want %d", len(cuts), len(want))
	}
	for i, args := range cuts {
		joined := strings.Join(args, " ")
		for _, frag := range []string{
			"-ss " + trimFloat(want[i][0]),
			"-t " + trimFloat(want[i][1]-want[i][0]),
			"-map 0:v:0", "-an", // pictures only: the soundtrack is Audio's job
			"fps=1", "-r 1", "scale='min(640,iw)':-2",
		} {
			if !strings.Contains(joined, frag) {
				t.Errorf("clip %d: %q missing %q", i+1, joined, frag)
			}
		}
		if !strings.HasPrefix(args[0], "-hide_banner") {
			t.Errorf("clip %d: ffmpeg is not silenced: %q", i+1, joined)
		}
	}
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.3f", f)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}

func TestVideoStopsWhereTheDecoderStops(t *testing.T) {
	// A container that overstates its own length: the third window has nothing in it, and
	// a slicer that believed the header would ship a black clip and a citation pointing
	// nowhere.
	runner := &videoRunner{probe: probeBoth, cut: func(window int, _ []string) int {
		if window >= 3 {
			return 512 // the third window is past where the frames are
		}
		return 4096
	}}
	ffmpeg, ffprobe := videoPrograms()
	v := &Video{FFmpeg: ffmpeg, FFprobe: ffprobe, Seconds: 2, Overlap: 0, Runner: runner}

	clips, err := v.Frames(context.Background(), "a.mp4", Streams{Seconds: 9, Video: true}, DirSink{Root: t.TempDir()}, "c")
	if err != nil {
		t.Fatalf("a short file should end the set, not fail it: %v", err)
	}
	if len(clips) != 2 {
		t.Fatalf("clips = %d, want the two that held frames", len(clips))
	}
	if runner.cuts != 3 {
		t.Errorf("ffmpeg ran %d times, want it to stop after the empty window", runner.cuts)
	}
}

func TestVideoRefusesALengthItCannotCut(t *testing.T) {
	ffmpeg, ffprobe := videoPrograms()
	v := &Video{FFmpeg: ffmpeg, FFprobe: ffprobe, Seconds: 15, Overlap: 50, Runner: &videoRunner{probe: probeBoth}}

	_, err := v.Frames(context.Background(), "a.mp4",
		Streams{Seconds: videoMaxSeconds + 60, Video: true}, DirSink{Root: t.TempDir()}, "c")
	if !errors.Is(err, ErrVideoTooLong) {
		t.Fatalf("frames = %v, want ErrVideoTooLong", err)
	}
}

func TestVideoWindowIsValidated(t *testing.T) {
	ffmpeg, ffprobe := videoPrograms()
	for _, tc := range []struct{ seconds, overlap int }{{0, 50}, {15, 100}, {15, -1}} {
		v := &Video{FFmpeg: ffmpeg, FFprobe: ffprobe, Seconds: tc.seconds, Overlap: tc.overlap, Runner: &videoRunner{probe: probeBoth}}
		if _, _, err := v.window(); err == nil {
			t.Errorf("seconds=%d overlap=%d: want an error", tc.seconds, tc.overlap)
		}
	}
}
