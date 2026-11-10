package ingest

import (
	"strings"
	"testing"
)

func TestSplitText(t *testing.T) {
	text := "one two three four five six seven eight nine ten"

	tests := []struct {
		name    string
		size    int
		overlap int
		want    []string
	}{
		{"no overlap", 4, 0, []string{
			"one two three four", "five six seven eight", "nine ten",
		}},
		{"overlap one", 4, 1, []string{
			"one two three four", "four five six seven", "seven eight nine ten",
		}},
		{"whole text fits", 100, 10, []string{text}},
		{"single word chunks", 1, 0, strings.Fields(text)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitText(text, tc.size, tc.overlap)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d pieces, want %d: %v", len(got), len(tc.want), got)
			}
			for i, piece := range got {
				if piece.Text != tc.want[i] {
					t.Errorf("piece %d = %q, want %q", i, piece.Text, tc.want[i])
				}
				// Offsets must index the original text exactly.
				if text[piece.Start:piece.End] != piece.Text {
					t.Errorf("piece %d offsets %d-%d do not slice back to %q",
						i, piece.Start, piece.End, piece.Text)
				}
			}
		})
	}
}

func TestSplitTextClampsOverlap(t *testing.T) {
	// overlap >= size would make the loop stand still; it must be clamped.
	pieces := SplitText("a b c d", 2, 5)
	if len(pieces) == 0 {
		t.Fatal("expected pieces")
	}
	for _, p := range pieces {
		if p.Text == "" {
			t.Fatal("empty piece")
		}
	}
}

func TestSplitTextDegenerate(t *testing.T) {
	if got := SplitText("", 10, 2); got != nil {
		t.Errorf("empty text = %v", got)
	}
	if got := SplitText("   \n\t ", 10, 2); got != nil {
		t.Errorf("whitespace text = %v", got)
	}
	if got := SplitText("a b", 0, 0); got != nil {
		t.Errorf("size 0 = %v", got)
	}
}

func TestSplitTextNegativeOverlap(t *testing.T) {
	pieces := SplitText("a b c d e", 2, -3)
	if len(pieces) != 3 {
		t.Fatalf("got %d pieces: %v", len(pieces), pieces)
	}
}

func TestSplitTextUnicodeOffsets(t *testing.T) {
	text := "héllo wörld æther ñ"
	pieces := SplitText(text, 2, 0)
	if len(pieces) != 2 {
		t.Fatalf("got %d pieces", len(pieces))
	}
	for i, p := range pieces {
		if text[p.Start:p.End] != p.Text {
			t.Errorf("piece %d = %q", i, p.Text)
		}
	}
	if pieces[0].Text != "héllo wörld" {
		t.Errorf("first = %q", pieces[0].Text)
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		head string
		want string
	}{
		{"doc.pdf", "", "pdf"},
		{"page.PNG", "", "image"},
		{"photo.jpeg", "", "image"},
		{"talk.mp3", "", "audio"},
		{"notes.md", "", "text"},
		{"data.json", "", "text"},
		{"index.html", "", "text"},
		{"main.go", "", "text"},
		{"noext", "%PDF-1.7", "pdf"},
		{"noext", "\x89PNG\r\n\x1a\n", "image"},
		{"noext", "just some plain text here", "text"},
		{"archive.zip", "PK\x03\x04", "unsupported"},

		// Magic beats a misleading extension: a PNG named .txt must not be
		// read as text. The reverse does not apply — a .md is text whatever
		// the sniffer makes of its first bytes.
		{"mislabelled.txt", "\x89PNG\r\n\x1a\n", "image"},
		{"speech.txt", "RIFF\x00\x00\x00\x00WAVEfmt ", "audio"},
		{"renamed.ai", "%PDF-1.7", "pdf"},
		{"notes.md", "# heading\n", "text"},
	}

	for _, tc := range tests {
		got := string(Detect(tc.name, []byte(tc.head)))
		if got != tc.want {
			t.Errorf("Detect(%q, %q) = %q, want %q", tc.name, tc.head, got, tc.want)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<html><head><style>.a{color:red}</style><script>var x = 1 < 2;</script></head>
<body><h1>Title</h1><p>First &amp; second</p><div>third<br>fourth</div>
<table><tr><td>cell</td><td>two</td></tr></table></body></html>`

	got := htmlToText(in)
	for _, want := range []string{"Title", "First & second", "third", "fourth", "cell", "two"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"color", "var x", "<", ">"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("leaked %q in:\n%s", unwanted, got)
		}
	}
}

func TestHTMLToTextUnclosed(t *testing.T) {
	if got := htmlToText("before<script>var x = 1"); !strings.Contains(got, "before") {
		t.Errorf("unclosed script = %q", got)
	}
	if got := htmlToText("before <p unterminated"); !strings.Contains(got, "before") {
		t.Errorf("unterminated tag = %q", got)
	}
}

func TestTextFrom(t *testing.T) {
	if got := textFrom("a.txt", []byte("\uFEFFhello\r\nworld\r")); got != "hello\nworld" {
		t.Errorf("got %q", got)
	}
	if got := textFrom("a.txt", []byte("bad \xff byte")); got != "bad   byte" {
		t.Errorf("invalid UTF-8 = %q", got)
	}
	if got := textFrom("a.md", []byte("keep   spaces")); got != "keep   spaces" {
		t.Errorf("spaces inside a line must survive: %q", got)
	}
}

func TestNormaliseText(t *testing.T) {
	in := "a   \n\n\n\nb\n\n\nc   \n"
	want := "a\n\nb\n\nc"
	if got := normaliseText(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
