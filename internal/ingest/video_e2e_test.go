package ingest

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"libris/internal/config"
	"libris/internal/domain"
	"libris/internal/tools"
)

// The only test in the package that lets a decoder decide what a clip is. The fakes in
// video_test.go prove the window arithmetic and the argument list; they cannot say whether
// what comes out the other end is a video at all, which is the thing a person notices when
// it is not: a player that shows nothing, and a model that throws on the bytes.
//
// It skips when ffmpeg or ffprobe is missing, and it generates its own footage rather than
// carrying a video in the repository — the fixture is a testsrc2 pattern, so nothing here is
// a person's holiday.
func TestVideoEndToEnd(t *testing.T) {
	ffmpeg, ffprobe := tools.FromPath().Video().FFmpeg, tools.FromPath().Video().FFprobe
	if !ffmpeg.Found() || !ffprobe.Found() {
		t.Skip("no ffmpeg and ffprobe on this machine")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "trip.mp4")
	// 720p on purpose: one of the things being checked is that a clip is written small
	// enough for the server to hold a window of them in one batch.
	run(t, ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", path)

	cfg := config.Default()
	cfg.VideoClipSeconds, cfg.VideoClipOverlap = 5, 0
	cfg.AudioClipSeconds, cfg.AudioClipOverlap = 15, 50

	root := t.TempDir()
	p, err := New(cfg, DirSink{Root: root}, tools.FromPath().PDF(), tools.FromPath().Audio(), tools.FromPath().Video())
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unsupported != "" {
		t.Fatalf("unsupported: %s (findings %v)", res.Unsupported, res.Findings)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("findings = %v", res.Findings)
	}
	if res.Source.Kind != domain.SourceVideo {
		t.Fatalf("source kind = %q, want video", res.Source.Kind)
	}

	// Two documents, because the pictures and the soundtrack are two kinds of evidence
	// about the same file — and two ordinal spaces, which is why both can have a clip one.
	if len(res.Docs) != 2 {
		t.Fatalf("docs = %+v, want the video and its soundtrack", res.Docs)
	}
	docs := map[domain.Kind]Doc{}
	for _, d := range res.Docs {
		docs[d.Kind] = d
	}
	frames, sound := docs[domain.KindVideo], docs[domain.KindAudio]
	if frames.ID != res.Source.ID {
		t.Errorf("frames doc = %q, want the source's own id", frames.ID)
	}
	if sound.ID != res.Source.ID+"#sound" {
		t.Errorf("sound doc = %q, want the suffix that keeps the ordinal spaces apart", sound.ID)
	}
	if frames.Title != "trip.mp4" || sound.Title != "trip.mp4 [sound]" {
		t.Errorf("titles = %q, %q", frames.Title, sound.Title)
	}

	var video, audio []*domain.Segment
	for i := range res.Segments {
		seg := &res.Segments[i]
		switch seg.Kind {
		case domain.KindVideo:
			video = append(video, seg)
		case domain.KindAudio:
			audio = append(audio, seg)
		default:
			t.Fatalf("segment of kind %q in a video source", seg.Kind)
		}
		if seg.Asset == nil {
			t.Fatalf("clip %d has no asset", seg.Ordinal)
		}
	}
	// Windows of five seconds over six, with the tail the first one did not reach: 0–5 and
	// 5–6. The soundtrack is one clip because the recording's own window is fifteen seconds
	// and the whole file fits inside it.
	if len(video) != 2 {
		t.Fatalf("video clips = %d, want 2", len(video))
	}
	if len(audio) != 1 {
		t.Fatalf("sound clips = %d, want 1", len(audio))
	}
	for i, want := range [][2]float64{{0, 5}, {5, 6}} {
		seg := video[i]
		if seg.Span.T0 != want[0] || seg.Span.T1 != want[1] {
			t.Errorf("video clip %d span = %v–%v, want %v–%v", i+1, seg.Span.T0, seg.Span.T1, want[0], want[1])
		}
	}

	// And the clip is a video: 640 across however large the source was, and one frame for
	// each second it covers.
	for i, seg := range video {
		width, height, frames := probeClip(t, ffprobe, seg.Asset.Path)
		if width != 640 || height != 360 {
			t.Errorf("clip %d is %dx%d, want 640x360 from a 720p source", i+1, width, height)
		}
		if want := int(video[i].Span.T1 - video[i].Span.T0); frames != want {
			t.Errorf("clip %d holds %d frames, want the %d seconds it covers", i+1, frames, want)
		}
		if seg.Asset.MIME != "video/mp4" {
			t.Errorf("clip %d mime = %q, want video/mp4 so the browser plays it", i+1, seg.Asset.MIME)
		}
	}
}

// TestVideoSilent is the ordinary case that the audio plan never had to face: a screen
// recording with nothing said over it. The absence is not the person's mistake and not a
// thing they could go and fix, so it is a document that does not exist rather than a
// finding — and the pictures still become rows.
func TestVideoSilent(t *testing.T) {
	ffmpeg, ffprobe := tools.FromPath().Video().FFmpeg, tools.FromPath().Video().FFprobe
	if !ffmpeg.Found() || !ffprobe.Found() {
		t.Skip("no ffmpeg and ffprobe on this machine")
	}

	path := filepath.Join(t.TempDir(), "screen.mp4")
	run(t, ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10:duration=4",
		"-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", path)

	root := t.TempDir()
	p, err := New(config.Default(), DirSink{Root: root}, tools.FromPath().PDF(), tools.FromPath().Audio(), tools.FromPath().Video())
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Process(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unsupported != "" {
		t.Fatalf("unsupported: %s (findings %v)", res.Unsupported, res.Findings)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a silent file is not a machine's fault: findings = %v", res.Findings)
	}
	if len(res.Docs) != 1 || res.Docs[0].Kind != domain.KindVideo {
		t.Fatalf("docs = %+v, want the pictures alone", res.Docs)
	}
	for _, seg := range res.Segments {
		if seg.Kind != domain.KindVideo {
			t.Fatalf("segment of kind %q from a file with no soundtrack", seg.Kind)
		}
	}
}

// probeClip asks a decoder what it makes of a clip: its size in pixels and its frame count
// counted from the frames themselves rather than from the header that claims them.
func probeClip(t *testing.T, ffprobe tools.Program, path string) (width, height, frames int) {
	t.Helper()
	out := run(t, ffprobe,
		"-v", "error",
		"-select_streams", "v:0", "-count_frames",
		"-show_entries", "stream=width,height,nb_read_frames",
		"-of", "default=nw=1:nk=1", path)

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		switch {
		case width == 0:
			width = n
		case height == 0:
			height = n
		default:
			frames = n
		}
	}
	if width == 0 || height == 0 {
		t.Fatalf("%s: no size in %q", filepath.Base(path), out)
	}
	return width, height, frames
}

// run runs a program the resolver found, and hands back what it printed.
func run(t *testing.T, prog tools.Program, args ...string) string {
	t.Helper()
	out, err := exec.Command(prog.Path, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", prog.Name, strings.Join(args, " "), err, out)
	}
	return string(out)
}
