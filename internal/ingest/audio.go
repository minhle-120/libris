package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"libris/internal/domain"
)

// AudioSliceSeconds is the window each audio chunk covers.
//
// The limit is the embedding model's budget, not taste: EmbeddingGemma 2 costs
// roughly 25 tokens per second of audio, and llama.cpp must fit one embedding
// input in a single ubatch — about 1022 usable tokens at the recommended
// "-ub 1024". 30 s lands near 750 tokens. Longer slices need a larger -ub.
const AudioSliceSeconds = 30

// maxAudioSlices stops a bogus duration from turning into an endless loop of
// ffmpeg runs. At 30 s each this allows about 16 hours, far past any file that
// fits the size cap.
const maxAudioSlices = 2000

// ErrNoAudioTools means the ffmpeg family is not installed, which is a missing
// dependency rather than a bad file.
var ErrNoAudioTools = errors.New("ingest: ffmpeg and ffprobe not found on PATH")

// Audio turns one audio file into fixed-length PCM clips, one segment each.
//
// There is no transcription yet, so a slice carries no text: it is searchable
// by embedding alone, and a citation can only offer to play it.
type Audio struct {
	ProbeBin   string // ffprobe
	SliceBin   string // ffmpeg
	SampleRate int    // PCM rate for the slices; llama.cpp wants 16 kHz mono
	Runner     Runner
}

// AudioSlice is one window of the original file, with its span in seconds.
type AudioSlice struct {
	Asset domain.Asset
	T0    float64
	T1    float64
}

func (a *Audio) runner() Runner {
	if a.Runner != nil {
		return a.Runner
	}
	return execRunner{}
}

func (a *Audio) sampleRate() int {
	if a.SampleRate > 0 {
		return a.SampleRate
	}
	return 16000
}

// Duration reads the playable length in seconds.
//
// ffprobe answers "N/A" for streams with no measurable duration (a pipe, some
// streaming-only containers). That is not a corrupt file, so it reports 0 with
// no error and the caller ends up with no slices rather than a failed batch.
func (a *Audio) Duration(ctx context.Context, path string) (float64, error) {
	if a.ProbeBin == "" {
		return 0, ErrNoAudioTools
	}

	out, err := a.runner().Run(ctx, a.ProbeBin,
		"-v", "error", "-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1", path)
	if err != nil {
		return 0, err
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "N/A" {
		return 0, nil
	}

	d, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("ingest: unreadable audio duration %q: %w", trimmed, err)
	}
	if math.IsNaN(d) || d < 0 {
		return 0, nil
	}
	return d, nil
}

// Slices decodes the file into PCM clips of AudioSliceSeconds each, the last
// one short. -ss before -i seeks the input, so decoding cost stays proportional
// to the slice rather than the whole file.
func (a *Audio) Slices(ctx context.Context, path string, sink Sink, prefix string) ([]AudioSlice, error) {
	if a.ProbeBin == "" || a.SliceBin == "" {
		return nil, ErrNoAudioTools
	}

	dur, err := a.Duration(ctx, path)
	if err != nil {
		return nil, err
	}
	if dur <= 0 {
		return nil, nil
	}

	var out []AudioSlice
	for i := 0; ; i++ {
		t0 := float64(i) * float64(AudioSliceSeconds)
		if t0 >= dur {
			break
		}
		if i >= maxAudioSlices {
			return out, fmt.Errorf("ingest: audio longer than %d slices, stopping", maxAudioSlices)
		}

		t1 := math.Min(t0+float64(AudioSliceSeconds), dur)
		name := fmt.Sprintf("%s/slice-%04d.wav", prefix, i+1)
		data, err := a.runner().Run(ctx, a.SliceBin,
			"-hide_banner", "-loglevel", "error",
			"-ss", strconv.FormatFloat(t0, 'f', 3, 64), "-i", path,
			"-t", strconv.FormatFloat(t1-t0, 'f', 3, 64),
			"-ar", strconv.Itoa(a.sampleRate()), "-ac", "1",
			"-c:a", "pcm_s16le", "-f", "wav", "-")
		if err != nil {
			return nil, err
		}

		asset, err := sink.Put(ctx, name, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		out = append(out, AudioSlice{Asset: asset, T0: t0, T1: t1})
	}
	return out, nil
}
