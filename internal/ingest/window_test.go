package ingest

import (
	"encoding/binary"
	"testing"
)

// The window arithmetic is the part of the slicer that fails silently. A hop that drifts by
// one sample shifts every clip's citation, and a window that is never emitted throws away a
// sentence with no error to notice it. So these tests ask the two questions that matter about
// a stream the slicer has never seen: is every sample inside some window, and does every
// neighbouring pair share the overlap the setting asked for.

func drain(t *testing.T, w *windower, total int64) []window {
	t.Helper()
	var out []window
	for _, chunk := range []int64{7, 13, 64, 1000, 1 << 20} {
		if total <= 0 {
			break
		}
		n := min(chunk, total)
		total -= n
		out = append(out, w.accept(w.read+n, total == 0)...)
	}
	return out
}

func TestWindowsCoverTheSample(t *testing.T) {
	const (
		total = 10_000 // samples
		size  = 3200   // a 200 ms window at 16 kHz
		hop   = 1600   // 50% overlap
	)

	var got []window
	w := &windower{size: size, hop: hop, minTail: 16}
	got = append(got, w.accept(300, false)...) // shorter than a hop: nothing is complete
	got = append(got, drain(t, w, total-300)...)

	if len(got) == 0 {
		t.Fatal("no windows at all")
	}
	if got[0].start != 0 {
		t.Errorf("first window starts at %d, want 0", got[0].start)
	}
	if last := got[len(got)-1]; last.end != total {
		t.Errorf("last window ends at %d, want %d — the end of the recording was dropped",
			last.end, total)
	}

	// Coverage: no hole anywhere, in either direction.
	for i := 1; i < len(got); i++ {
		if got[i].start > got[i-1].end {
			t.Fatalf("hole between %v and %v", got[i-1], got[i])
		}
		if want := int64(size - hop); got[i-1].end-got[i].start != want {
			t.Errorf("windows %d and %d overlap %d samples, want %d",
				i-1, i, got[i-1].end-got[i].start, want)
		}
	}
	for _, win := range got {
		if win.end <= win.start {
			t.Errorf("window %v is empty or backwards", win)
		}
	}
	// Every full window is the size asked for; only the tail may be shorter, and only the
	// last one is the tail.
	for _, win := range got[:len(got)-1] {
		if win.end-win.start != size {
			t.Errorf("window %v is %d samples, want %d", win, win.end-win.start, size)
		}
	}

	// And the whole recording really is inside them, sample by sample, at the seams a
	// sentence can straddle.
	for at := 0; at < total; at += 97 {
		inside := false
		for _, win := range got {
			if at >= int(win.start) && at < int(win.end) {
				inside = true
				break
			}
		}
		if !inside {
			t.Fatalf("sample %d belongs to no window", at)
		}
	}
}

func TestWindowsStopAtTheFirstWindowThatCompletes(t *testing.T) {
	w := &windower{size: 100, hop: 50, minTail: 1}
	if got := w.accept(99, false); len(got) != 0 {
		t.Fatalf("99 samples of a 100-sample window returned %v", got)
	}
	got := w.accept(100, false)
	if len(got) != 1 || got[0] != (window{0, 100}) {
		t.Fatalf("at exactly one window: got %v", got)
	}
	// Two more hops' worth, and two more windows are complete.
	if got := w.accept(200, false); len(got) != 2 {
		t.Fatalf("got %v, want the two windows ending at 150 and 200", got)
	}
}

func TestWindowsDropATailTooShortToBeAClip(t *testing.T) {
	w := &windower{size: 100, hop: 100, minTail: 20}
	if got := w.accept(100, false); len(got) != 1 {
		t.Fatalf("got %v, want one window", got)
	}
	if got := w.accept(110, true); len(got) != 0 {
		t.Fatalf("a 10-sample tail returned %v; minTail is %d", got, w.minTail)
	}

	// The same length, one sample longer, is a clip. And it starts where the next full
	// window would have started, so it overlaps the last full one the way every other
	// pair does.
	w2 := &windower{size: 100, hop: 100, minTail: 10}
	w2.accept(100, false)
	got := w2.accept(110, true)
	if len(got) != 1 || got[0] != (window{100, 110}) {
		t.Fatalf("got %v, want the tail 100..110", got)
	}
}

func TestWindowsWithNoOverlap(t *testing.T) {
	w := &windower{size: 100, hop: 100, minTail: 1}
	got := append(w.accept(250, false), w.accept(250, true)...)
	if len(got) != 3 {
		t.Fatalf("got %v, want 100..200 and the 50-sample tail", got)
	}
	if got[1] != (window{100, 200}) || got[2] != (window{200, 250}) {
		t.Errorf("got %v", got)
	}
}

func TestPeakSeparatesSilenceFromVoice(t *testing.T) {
	samples := func(v ...int16) []byte {
		out := make([]byte, 2*len(v))
		for i, x := range v {
			binary.LittleEndian.PutUint16(out[i*2:], uint16(x))
		}
		return out
	}

	if got := peak(samples(0, 0, 0, 0)); got != 0 {
		t.Errorf("silence peaks at %d, want 0", got)
	}
	// The loudest sample in the window, whichever side of zero it is on: a waveform is
	// symmetric, and a filter that only looked at positives would call a loud inverted
	// passage silence.
	if got := peak(samples(100, -32000, 300)); got != 32000 {
		t.Errorf("peak %d, want 32000", got)
	}
	// A byte count that is not a whole sample cannot happen from this package's own read,
	// and must not read past the end if it ever did.
	if got := peak([]byte{0x01}); got != 0 {
		t.Errorf("peak of one stray byte: %d, want 0", got)
	}
	// What the filter actually compares against: the loudest thing in a quiet room has to
	// fall under it, and a speaker has to clear it.
	if peak(samples(audiblePeak-1)) >= audiblePeak {
		t.Error("audiblePeak does not reject its own predecessor")
	}
	if peak(samples(audiblePeak)) < audiblePeak {
		t.Error("audiblePeak rejects the sample it is defined as")
	}
}
