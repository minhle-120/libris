package llm

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

func TestEndpoint(t *testing.T) {
	cases := map[string]string{
		"http://h:1":        "http://h:1/v1/chat/completions",
		"http://h:1/":       "http://h:1/v1/chat/completions",
		"http://h:1/v1":     "http://h:1/v1/chat/completions",
		"http://h:1/v1/":    "http://h:1/v1/chat/completions",
		" http://h:1/v1/  ": "http://h:1/v1/chat/completions",
	}
	for base, want := range cases {
		if got := endpoint(base, "chat/completions"); got != want {
			t.Errorf("endpoint(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("a\nb"); got != "a" {
		t.Errorf("firstLine = %q", got)
	}
	got := firstLine(strings.Repeat("x", 300))
	if n := utf8.RuneCountInString(got); n != 201 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncation = %d runes", n)
	}
}

func TestMediaParts(t *testing.T) {
	raw := strings.Repeat("QUJD", 100)
	if p := AudioBase64(raw); p.InputAudio == nil || p.InputAudio.Data != raw || p.InputAudio.URL != "" {
		t.Errorf("AudioBase64 = %+v", p.InputAudio)
	}
	if p := VideoBase64(raw); p.InputVideo == nil || p.InputVideo.Data != raw {
		t.Errorf("VideoBase64 = %+v", p.InputVideo)
	}
	// A path made only of letters and slashes must never be mistaken for data.
	if p := AudioPart("path/to/file"); p.InputAudio == nil || p.InputAudio.URL != "path/to/file" || p.InputAudio.Data != "" {
		t.Errorf("AudioPart(path) = %+v", p.InputAudio)
	}
	if p := VideoPart("/tmp/v.mp4"); p.InputVideo == nil || p.InputVideo.URL != "/tmp/v.mp4" {
		t.Errorf("VideoPart = %+v", p.InputVideo)
	}
	if p := TextPart("hi"); p.Type != PartText || p.Text != "hi" {
		t.Errorf("TextPart = %+v", p)
	}
	if got := Parts(TextPart("a"), TextPart("b")); len(got) != 2 {
		t.Errorf("Parts = %+v", got)
	}
}

func TestNewToolMarshalError(t *testing.T) {
	if _, err := NewTool("x", "y", func() {}); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestSSESkipsNonDataAndPropagatesError(t *testing.T) {
	in := strings.NewReader(": comment\nretry: 100\nid: 1\nevent: x\ndata: a\n\ndata: b\n")
	s := newSSE(in)
	for _, want := range []string{"a", "b"} {
		got, ok, err := s.Next()
		if err != nil || !ok || string(got) != want {
			t.Fatalf("Next = %q ok=%v err=%v, want %q", got, ok, err, want)
		}
	}
	if _, ok, err := s.Next(); ok || err != nil {
		t.Fatalf("expected clean EOF, got ok=%v err=%v", ok, err)
	}

	s = newSSE(iotest.ErrReader(errors.New("boom")))
	if _, _, err := s.Next(); err == nil {
		t.Fatal("expected scanner error")
	}
}
