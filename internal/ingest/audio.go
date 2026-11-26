package ingest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"libris/internal/domain"
	"libris/internal/tools"
)

// ErrNoAudioTools means ffmpeg is in neither place it is looked for. It is reported rather
// than guessed around because it is a missing dependency, not a bad recording: the row has to
// say which program to go and get, and a person who downloaded libris should be told the
// folder beside it is missing rather than told to install something.
var ErrNoAudioTools = errors.New("ingest: no ffmpeg on this machine")

// ErrAudioTooLong stops a stream that will not end. ffprobe's duration is not consulted, so
// the only account of how much has been read is the byte count; without a ceiling, a file
// whose samples refuse to stop — a pipe, a container with a lying header — becomes an
// endless loop of clips written into the assets folder. The ceiling is a day of 16 kHz mono
// audio, which is hours past any recording that fits the size cap.
var ErrAudioTooLong = errors.New("ingest: recording is longer than libris will slice")

// sampleRate is what the clips are written at, and what the embedding model's audio tower
// expects: 16 kHz mono PCM. Resampling is ffmpeg's job and happens once, at the mouth of the
// pipe, which is why the container a person uploaded stops mattering below this line — the
// server never sees anything but WAV, so its sniffing of mp3 and flac is never needed.
const sampleRate = 16000

// bytesPerSample is s16le, the only encoding the clips are written in. A compressed clip
// would be smaller on disk and would make every clip below depend on a decoder's idea of
// what a sample is, to save 32 KB a second.
const bytesPerSample = 2

// audiblePeak is the level below which a clip has nothing in it: one percent of full scale,
// about -40 dBFS. It is a peak rather than an average on purpose — a window of quiet room
// tone averages low and still says something, while a window whose loudest sample sits under
// a hundredth of full scale has no speech in it whatever it is.
//
// The filter is not decoration. Measured with the embedding model, one second of silence
// scored 0.780 against a content query and ranked above the clip that answered it.
const audiblePeak = int16(math.MaxInt16 / 100)

// minTailSeconds is the shortest tail worth a clip of its own. A recording rarely ends on a
// window boundary, and the leftover is either the last sentence or the last quarter of one —
// under a second there is no sentence to keep, and a clip that short is a retrieval hazard
// rather than a benefit.
const minTailSeconds = 1

// Audio turns one recording into clips: fixed-length windows of 16 kHz mono PCM, each one
// stored whole and embedded whole. There is no transcription here and none is planned; a
// clip carries no words, and the model that answers from it is the model that hears it
// (ADR 0010).
//
// The window and its overlap arrive as settings rather than constants for the reason the
// text chunker's do. Both arrive in the units a person reads them in: whole seconds, and a
// percentage of those seconds.
//
// ffmpeg arrives resolved, for the same reason the PDF programs do: which folder a program
// came from belongs to the module that knows about folders.
type Audio struct {
	FFmpeg  tools.Program
	Seconds int // window length
	Overlap int // percent of the window repeated at the start of the next one
	Runner  StreamRunner
}

// Clip is one window of a recording: the bytes, and where in the original they came from.
type Clip struct {
	Asset domain.Asset
	T0    float64 // seconds into the recording
	T1    float64
}

// StreamRunner runs a program whose output is the payload — a recording's decoded samples,
// which for an hour of audio is 115 MB and does not belong in a []byte. It exists as a seam
// for the same reason Runner does: the slicing below is arithmetic over samples, and a test
// can hand that arithmetic samples directly.
type StreamRunner interface {
	Stream(ctx context.Context, prog tools.Program, args ...string) (io.ReadCloser, error)
}

type execStreamRunner struct{}

func (execStreamRunner) Stream(ctx context.Context, prog tools.Program, args ...string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, prog.Path, args...)
	stderr := newTail(4096)
	cmd.Stderr = stderr

	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", prog.Name, err)
	}
	if env := prog.Env(os.Environ()); len(env) != 0 {
		cmd.Env = env
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", prog.Name, err)
	}

	// Nothing drains stderr here: os/exec copies whatever the program writes into the
	// writer above, so the pipe is emptied whether or not anyone reads the result. What is
	// left of it — the tail, which is where the reason is — goes into the error.
	return &pipe{ReadCloser: out, cmd: cmd, stderr: stderr}, nil
}

// pipe ties the stream's lifetime to the process: closing it, or reaching the end, reports
// the exit status, because a decoder that wrote half the samples and then died has to fail
// the clip set rather than shorten it.
type pipe struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *tail

	waited bool
	err    error
}

// Close waits for the process, once, and reports what it exited with. The caller closes
// explicitly to get that answer and the deferred close runs again on the way out, so the
// second one must repeat the first rather than ask os/exec a question it has already
// answered.
func (p *pipe) Close() error {
	if p.waited {
		return p.err
	}
	p.waited = true

	closeErr := p.ReadCloser.Close()
	waitErr := p.cmd.Wait()

	p.err = closeErr
	if waitErr != nil {
		if msg := strings.TrimSpace(p.stderr.String()); msg != "" {
			p.err = fmt.Errorf("%s: %w: %s", p.cmd.Path, waitErr, msg)
		} else {
			p.err = fmt.Errorf("%s: %w", p.cmd.Path, waitErr)
		}
	}
	return p.err
}

func (a *Audio) runner() StreamRunner {
	if a.Runner != nil {
		return a.Runner
	}
	return execStreamRunner{}
}

// Clips reads a recording and writes its clips under "<prefix>/clip-0001.wav" and onward,
// numbered in the order they start. The numbering counts only the clips that were written,
// so a dropped silent window leaves no hole in the names.
//
// One decode pass, streamed: the samples are read once, and each window is cut and written as
// soon as its last sample arrives. That is what keeps memory at one window's worth whatever
// the recording's length, and it is why the window arithmetic lives in windower rather than
// scattered through the read loop.
func (a *Audio) Clips(ctx context.Context, path string, sink Sink, prefix string) ([]Clip, error) {
	if !a.FFmpeg.Found() {
		return nil, ErrNoAudioTools
	}
	size, hop, err := a.sampleWindow()
	if err != nil {
		return nil, err
	}

	// -vn matters: a recording ripped from a video, or an M4A with cover art, carries a
	// video stream, and without this the decoder would be asked to turn that into samples.
	out, err := a.runner().Stream(ctx, a.FFmpeg,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", path, "-vn", "-ac", "1", "-ar", strconv.Itoa(sampleRate), "-f", "s16le", "-")
	if err != nil {
		return nil, err
	}
	defer out.Close()

	w := &windower{size: size, hop: hop, minTail: minTailSeconds * sampleRate}
	var (
		clips []Clip
		// The samples of the windows not yet written, oldest first. It never holds more
		// than one window plus one read, which is what the trim below enforces.
		buf []byte
		// The offset of buf[0] in the stream, in samples. Every window the windower returns
		// is in stream samples, so this is what turns one into a slice of buf.
		base int64
		seq  int
	)

	for {
		buf = append(buf, make([]byte, chunkBytes)...)
		n, rerr := io.ReadFull(out, buf[len(buf)-chunkBytes:])
		buf = buf[:len(buf)-chunkBytes+n]
		// A short read is the end of the stream; anything else is a decoder that died, and
		// its exit status is the only account of why.
		eof := rerr != nil
		if eof && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return nil, rerr
		}

		for _, win := range w.accept(base+int64(len(buf)/bytesPerSample), eof) {
			if win.end > w.read {
				return nil, fmt.Errorf("ingest: clip window %d..%d past the end at %d", win.start, win.end, w.read)
			}
			asset, aerr := a.writeClip(ctx, sink, prefix, seq, base, buf, win)
			switch {
			case aerr != nil:
				return nil, aerr
			case asset != nil:
				seq++
				clips = append(clips, Clip{
					Asset: *asset,
					T0:    float64(win.start) / sampleRate,
					T1:    float64(win.end) / sampleRate,
				})
			}
			// Everything before the next window's first sample is never read again.
			keep := int(w.emitted - base)
			copy(buf, buf[keep*bytesPerSample:])
			buf = buf[:len(buf)-keep*bytesPerSample]
			base = w.emitted
		}

		if w.read > maxSamples {
			return nil, fmt.Errorf("%w: past %s of audio", ErrAudioTooLong, time.Duration(maxSamples/sampleRate)*time.Second)
		}
		if eof {
			// The exit status decides whether what was written is the whole recording, so a
			// decoder that stopped early fails the set rather than shortening it.
			if cerr := out.Close(); cerr != nil {
				return nil, cerr
			}
			return clips, nil
		}
	}
}

// writeClip stores one window, and returns nil when the window holds nothing worth storing.
func (a *Audio) writeClip(ctx context.Context, sink Sink, prefix string, seq int, base int64, buf []byte, win window) (*domain.Asset, error) {
	from, to := (win.start-base)*bytesPerSample, (win.end-base)*bytesPerSample
	samples := buf[from:to]
	if peak(samples) < audiblePeak {
		return nil, nil
	}

	var head [44]byte
	writeWAVHeader(&head, sampleRate, int64(len(samples)/bytesPerSample))
	name := fmt.Sprintf("%s/clip-%04d.wav", prefix, seq+1)

	asset, err := sink.Put(ctx, name, io.MultiReader(bytes.NewReader(head[:]), bytes.NewReader(samples)))
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

// sampleWindow turns the two settings into sample counts, so the arithmetic below never
// divides by a percentage again.
func (a *Audio) sampleWindow() (size, hop int64, err error) {
	if a.Seconds <= 0 {
		return 0, 0, fmt.Errorf("ingest: audio clip length must be positive, got %d", a.Seconds)
	}
	size = int64(a.Seconds) * sampleRate
	if a.Overlap < 0 || a.Overlap >= 100 {
		return 0, 0, fmt.Errorf("ingest: audio clip overlap must be under 100%%, got %d", a.Overlap)
	}
	// The overlap is a percentage of the window, which is what makes "half" mean half at
	// every window size — the reason the setting is not written in seconds.
	hop = size - size*int64(a.Overlap)/100
	if hop <= 0 {
		hop = 1
	}
	return size, hop, nil
}

// window is a window of the stream, in samples from the start of the recording.
type window struct{ start, end int64 }

// windower is the whole of the window arithmetic: given how many samples have arrived, which
// windows are now complete, plus the short one that closes a recording out.
//
// It is a type rather than a function because the answer depends on where the previous window
// ended, and because this is the part worth testing on its own: an off-by-one hop shifts
// every clip's citation by a fraction of a second, silently, forever.
type windower struct {
	size    int64
	hop     int64
	minTail int64

	read    int64
	emitted int64 // start of the next window not yet returned
}

// accept records that the stream now holds total samples in all, and returns the windows
// they complete. At the end of the stream it also returns the tail, if the tail is long
// enough to be a clip.
//
// A window is complete when its last sample has arrived — that is the whole rule, and the
// overlap is only the hop being smaller than the window. Two windows can complete in one
// call, and none can, when the read is shorter than the hop.
func (w *windower) accept(total int64, eof bool) []window {
	if total < w.read {
		panic("ingest: windower went backwards")
	}
	w.read = total
	var out []window
	for w.emitted+w.size <= w.read {
		out = append(out, window{w.emitted, w.emitted + w.size})
		w.emitted += w.hop
	}
	if !eof {
		return out
	}
	// The tail: whatever the full windows did not consume. It starts where the next full
	// window would have, so it overlaps the last full one by exactly the amount the last
	// one overlapped its predecessor — a final sentence that straddled a boundary is in
	// both, as it is everywhere else.
	tail := w.read - w.emitted
	if tail >= w.minTail {
		out = append(out, window{w.emitted, w.read})
	}
	return out
}

// peak is the loudest sample in the window, as a signed magnitude.
func peak(samples []byte) int16 {
	var hi int16
	for i := 0; i+1 < len(samples); i += bytesPerSample {
		v := int16(binary.LittleEndian.Uint16(samples[i:]))
		if v < 0 {
			v = -v
		}
		if v > hi {
			hi = v
		}
	}
	return hi
}

// writeWAVHeader writes the canonical 44-byte RIFF header for s16le mono PCM. It is written
// by hand rather than by a dependency because the format is a header and a payload, and the
// payload is already in the buffer.
func writeWAVHeader(h *[44]byte, rate int, samples int64) {
	data := samples * bytesPerSample
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+data))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(h[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(h[22:], 1)  // mono
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*bytesPerSample))
	binary.LittleEndian.PutUint16(h[32:], bytesPerSample)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(data))
}

// chunkBytes is how much of the stream is pulled at a time. It is a multiple of the sample
// width, so the buffer always holds whole samples however the reads land.
const chunkBytes = 64 * 1024

// maxSamples is the ceiling ErrAudioTooLong enforces: a day of audio.
const maxSamples = int64(24 * 60 * 60 * sampleRate)

// tail keeps the last bytes written to it, which is where a program's reason for failing is.
// The whole of stderr is not wanted: ffmpeg is chatty, and the head of its complaint is a
// version banner.
type tail struct {
	keep int
	buf  []byte
}

func newTail(keep int) *tail { return &tail{keep: keep} }

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.keep; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.buf) }
