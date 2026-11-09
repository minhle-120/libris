package ingest

import (
	"html"
	"path/filepath"
	"strings"
)

// textFrom decodes a text-bearing file into normalised text. It never fails:
// binary-ish bytes are converted to valid UTF-8 rather than rejected, because
// refusing a file leaves the user with nothing.
func textFrom(name string, data []byte) string {
	s := strings.TrimPrefix(string(data), "\uFEFF")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ToValidUTF8(s, " ")

	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return normaliseText(htmlToText(s))
	default:
		return normaliseText(s)
	}
}

// htmlToText drops markup and keeps reading order. <script> and <style> bodies
// are skipped entirely; block-level tags become line breaks so paragraphs
// survive chunking.
func htmlToText(s string) string {
	var b strings.Builder
	lower := strings.ToLower(s)

	for i := 0; i < len(s); {
		if s[i] != '<' {
			b.WriteByte(s[i])
			i++
			continue
		}

		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			break // unterminated tag: drop the remainder
		}
		tag := lower[i+1 : i+end]
		i += end + 1

		name := tag
		if k := strings.IndexAny(name, " \t\n/"); k >= 0 {
			name = name[:k]
		}

		switch {
		case name == "script" || name == "style":
			closing := "</" + name
			k := strings.Index(lower[i:], closing)
			if k < 0 {
				return normaliseText(b.String())
			}
			i += k
		case isBlockTag(name):
			b.WriteByte('\n')
		case name == "td" || name == "th":
			b.WriteByte('\t')
		}
	}
	return html.UnescapeString(b.String())
}

func isBlockTag(name string) bool {
	switch name {
	case "p", "div", "br", "li", "tr", "hr", "pre", "table", "ul", "ol",
		"section", "article", "header", "footer", "blockquote",
		"h1", "h2", "h3", "h4", "h5", "h6":
		return true
	}
	return false
}

// normaliseText trims trailing space and collapses runs of blank lines. The
// text itself is otherwise untouched: chunks must stay verbatim substrings so
// their character offsets keep pointing at the right passage.
func normaliseText(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := 0

	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
