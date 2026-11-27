package ingest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"libris/internal/domain"
	"libris/internal/tools"
)

// ErrNoVideoTools means one of the two programs a video needs is in neither place they
// are looked for. It is reported rather than guessed around for the reason the recording
// has the same distinction: a missing dependency is not a bad file, the row has to say
// which program to go and get, and a video asks for two of them where a recording asks
// for one.
var ErrNoVideoTools = errors.New("ingest: no ffmpeg and ffprobe on this machine")

// ErrVideoTooLong stops a video whose pictures would cost more than the machine should
// spend to answer questions about it. The recording's ceiling is a day because a day of
// samples is 115 MB and one decoder pass; a day of frames is 7,776 ffmpeg runs and a
// gigabyte of clips beside a file the size cap already refused.
var ErrVideoTooLong = errors.New("ingest: video is longer than libris will slice")

// The frame rule, which is the whole of what a clip of a video is.
//
// One frame a second is what the model was trained to be shown — Gemma's own guide says
// a video is "processed as sampled frames through the vision encoder at 1 frame per
// second", and llama.cpp agrees by sampling at that rate when nothing tells it
// otherwise. It is written as a constant rather than a setting because it is a property
// of the model server, not of the footage: the server is told the same number (see the
// engine's preset), and a settings page that could ask for more frames than the server
// holds in one batch is a settings page that lies.
//
// videoMaxWidth is the other half of the same arithmetic. A frame costs tokens by its
// area, and the server's single-batch limit is what decides how many seconds of video
// one clip may be: at 640 across, a frame is a hundred and seven tokens and the batch
// holds about thirty seconds of them. At 1920 the same thirty seconds is a clip the
// server refuses, which is a worse outcome than a smaller picture — the pixels a
// retrieval model needs to match "the red car in the driveway" are in the low
// frequencies anyway.
const (
	videoFramesPerSecond = 1
	videoMaxWidth        = 640

	// videoMaxSeconds is what ErrVideoTooLong enforces: two hours of pictures.
	videoMaxSeconds = 2 * 60 * 60
)

// Video turns one video into clips of its pictures. Its soundtrack is not this type's
// business: it is cut by Audio, unchanged, because a voice does not change length or
// pauses because it arrived inside an mp4 (ADR 0012).
//
// The window arrives as settings for the reason the recording's does, in the units a
// person reads them in: whole seconds, and a percentage of those seconds. They are not
// the recording's two settings, and the two pairs are allowed to disagree: a row says
// what it covers, and a window of pictures and a window of sound from the same moment
// are two rows.
type Video struct {
	FFmpeg  tools.Program
	FFprobe tools.Program
	Seconds int // window length
	Overlap int // percent of the window repeated at the start of the next one
	Runner  Runner
}

// FrameClip is one window of a video's pictures: the file, and where in the original the
// window sits.
type FrameClip struct {
	Asset domain.Asset
	T0    float64 // seconds into the video
	T1    float64
}

// Streams is what one look at a container says about what is inside it, and it answers
// the only three questions worth asking before starting either set of clips: how long,
// whether there are pictures to cut, and whether there is a soundtrack to cut.
//
// This is where ffprobe earns its place beside ffmpeg. A recording never needed it —
// that slicer learns its length from the samples as they arrive — and this one cannot:
// there is no stream of frames to read once and count, because the point of the windowing
// is to seek. One invocation per video, before any clip is written.
type Streams struct {
	Seconds float64
	Video   bool
	Audio   bool
}

func (v *Video) runner() Runner {
	if v.Runner != nil {
		return v.Runner
	}
	return execRunner{}
}

// Probe reads the container's own account of itself.
func (v *Video) Probe(ctx context.Context, path string) (Streams, error) {
	if !v.FFmpeg.Found() || !v.FFprobe.Found() {
		return Streams{}, ErrNoVideoTools
	}
	out, err := v.runner().Run(ctx, v.FFprobe,
		"-v", "error",
		"-show_entries", "format=duration:stream=codec_type",
		"-of", "default=nw=1:nk=1", path)
	if err != nil {
		return Streams{}, err
	}

	var (
		st     Streams
		seen   bool
		latest float64
	)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "video":
			st.Video = true
		case "audio":
			st.Audio = true
		case "", "N/A":
		default:
			// The duration lines, which are the only other thing this asks for. A
			// container can carry more than one (a format and a stream), and the
			// longest is the one the windows have to cover.
			if f, cerr := strconv.ParseFloat(line, 64); cerr == nil {
				seen = true
				if f > latest {
					latest = f
				}
			}
		}
	}
	if !seen || latest <= 0 {
		return Streams{}, fmt.Errorf("ingest: %s reports no length", filepath.Base(path))
	}
	st.Seconds = latest
	return st, nil
}

// Frames cuts a video's pictures into "<prefix>/clip-0001.mp4" and onward, numbered in
// the order their windows start, and each one carrying the window it was cut from in its
// name's span rather than in its bytes.
//
// One ffmpeg run per window, seeking rather than streaming. The recording decodes once
// because its windows are contiguous reads of one stream and a pipe can carry them all;
// windows of pictures overlap, and no muxer writes overlapping segments, so the choice
// is between re-decoding from a keyframe and decoding the whole file into a temp
// directory first. A seek costs a keyframe's worth of decoding — and every clip is
// re-encoded here anyway, because a clip at the file's native 30 frames a second is
// 3,210 tokens of frames a second against the batch's forty-odd thousand.
func (v *Video) Frames(ctx context.Context, path string, st Streams, sink Sink, prefix string) ([]FrameClip, error) {
	if !v.FFmpeg.Found() || !v.FFprobe.Found() {
		return nil, ErrNoVideoTools
	}
	size, hop, err := v.window()
	if err != nil {
		return nil, err
	}
	if st.Seconds > videoMaxSeconds {
		return nil, fmt.Errorf("%w: past %s", ErrVideoTooLong, time.Duration(videoMaxSeconds)*time.Second)
	}

	// The windower counts whatever unit it is given. Here it counts hundredths of a
	// second, which is fine for a window boundary and coarse enough that the arithmetic
	// stays in int64 for a video of any length.
	w := &windower{size: size, hop: hop, minTail: minTailSeconds * centis}
	windows := w.accept(int64(math.Round(st.Seconds*centis)), true)

	var clips []FrameClip
	for _, win := range windows {
		t0 := float64(win.start) / centis
		t1 := float64(win.end) / centis

		name := fmt.Sprintf("%s/clip-%04d.mp4", prefix, len(clips)+1)
		asset, err := v.cutClip(ctx, path, t0, t1-t0, sink, name)
		switch {
		case errors.Is(err, errClipEmpty):
			// Nothing was there to cut. The container's own length is the only
			// evidence this had for where the video ended, and containers have been
			// known to overstate it: stop here, and keep what was cut.
			return clips, nil
		case err != nil:
			return nil, err
		}
		clips = append(clips, FrameClip{Asset: asset, T0: t0, T1: t1})
	}
	return clips, nil
}

// errClipEmpty says a window held no frames, which is a fact about where the video
// ended rather than a failure of the encoder.
var errClipEmpty = errors.New("ingest: window holds no frames")

// cutClip writes one window as a clip and stores it.
func (v *Video) cutClip(ctx context.Context, path string, t0, dur float64, sink Sink, name string) (domain.Asset, error) {
	// A directory rather than a file, because ffmpeg will not write over a file that
	// already exists and an os.CreateTemp leaves one behind.
	dir, err := os.MkdirTemp("", "libris-clip-")
	if err != nil {
		return domain.Asset{}, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "clip.mp4")

	if _, err := v.runner().Run(ctx, v.FFmpeg,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		// -ss before -i, so the seek happens in the demuxer: it lands on the keyframe
		// before the window and decodes forward from there, which is frame-accurate
		// and does not read the hour of video in front of it.
		"-ss", strconv.FormatFloat(t0, 'f', 3, 64),
		"-t", strconv.FormatFloat(dur, 'f', 3, 64),
		"-i", path,
		"-map", "0:v:0", "-an",
		"-vf", fmt.Sprintf("fps=%d,scale='min(%d,iw)':-2", videoFramesPerSecond, videoMaxWidth),
		"-r", strconv.Itoa(videoFramesPerSecond),
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "30",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart",
		out); err != nil {
		return domain.Asset{}, err
	}

	info, err := os.Stat(out)
	if err != nil {
		return domain.Asset{}, err
	}
	// A window past the end leaves a file that is a header and nothing else — under two
	// kilobytes is no window of pictures at one frame a second by any encoder's arithmetic.
	if info.Size() < 2048 {
		return domain.Asset{}, errClipEmpty
	}
	return sink.PutFile(ctx, name, out)
}

// window turns the two settings into hundredths of a second, in the shape the windower
// wants. The rules are the recording's, because they are the same two questions: how big
// is a window, and how much do neighbours share.
func (v *Video) window() (size, hop int64, err error) {
	if v.Seconds <= 0 {
		return 0, 0, fmt.Errorf("ingest: video clip length must be positive, got %d", v.Seconds)
	}
	size = int64(v.Seconds) * centis
	if v.Overlap < 0 || v.Overlap >= 100 {
		return 0, 0, fmt.Errorf("ingest: video clip overlap must be under 100%%, got %d", v.Overlap)
	}
	hop = size - size*int64(v.Overlap)/100
	if hop <= 0 {
		hop = 1
	}
	return size, hop, nil
}

// centis is the unit the window counts in, so that a half-second boundary is a whole
// number here and not a rounding argument later.
const centis = 100
