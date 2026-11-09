package ingest

import "strings"

// Piece is a contiguous slice of the source text, with byte offsets into it.
type Piece struct {
	Text  string
	Start int
	End   int
}

// SplitText breaks s into pieces of at most size words, each starting overlap
// words before the previous piece ended. Offsets are byte offsets into s, so a
// citation can point at the exact passage.
//
// Chunks are cut on word boundaries and never normalised: the returned Text is
// a verbatim substring of s.
func SplitText(s string, size, overlap int) []Piece {
	if size <= 0 || strings.TrimSpace(s) == "" {
		return nil
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= size {
		overlap = size - 1
	}

	// Any byte that survives TrimSpace is non-space, so this is never empty.
	words := wordSpans(s)

	step := size - overlap
	var out []Piece
	for i := 0; i < len(words); i += step {
		end := i + size
		if end > len(words) {
			end = len(words)
		}
		start := words[i].start
		stop := words[end-1].end
		out = append(out, Piece{Text: s[start:stop], Start: start, End: stop})
		if end == len(words) {
			break
		}
	}
	return out
}

type span struct {
	start int
	end   int
}

// wordSpans returns byte spans of runs of non-space bytes. Scanning bytes is
// safe for UTF-8: every multi-byte continuation is >= 0x80 and therefore never
// mistaken for ASCII whitespace.
func wordSpans(s string) []span {
	var spans []span
	inWord := false
	start := 0

	for i := 0; i < len(s); i++ {
		space := isASCIISpace(s[i])
		switch {
		case !space && !inWord:
			inWord, start = true, i
		case space && inWord:
			inWord = false
			spans = append(spans, span{start, i})
		}
	}
	if inWord {
		spans = append(spans, span{start, len(s)})
	}
	return spans
}

func isASCIISpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}
