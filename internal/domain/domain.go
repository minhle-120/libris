// Package domain holds the vocabulary shared by every other package: plain
// types, no dependencies, and no behaviour worth mocking.
package domain

import "fmt"

// Kind is the modality of an embeddable segment.
type Kind string

const (
	KindText  Kind = "text"
	KindImage Kind = "image"
	KindAudio Kind = "audio"
)

// SourceKind is the original file's type, independent of how it is segmented.
type SourceKind string

const (
	SourcePDF         SourceKind = "pdf"         // text layer + page images
	SourceImage       SourceKind = "image"       // one image
	SourceAudio       SourceKind = "audio"       // needs transcription
	SourceText        SourceKind = "text"        // plain text, markdown, html, csv, code
	SourceUnsupported SourceKind = "unsupported" // stored, not searchable
)

// Source is the uploaded file. The original is always kept on disk so a
// citation can be opened later.
type Source struct {
	ID     string // sha256 of the contents: identity and dedupe key
	Name   string // original filename
	Kind   SourceKind
	SHA256 string
	Bytes  int64
}

// Provenance points back into the original file. Every segment carries one so
// the UI can open the PDF at a page, or the audio at a timestamp.
type Provenance struct {
	Page      int     `json:"page,omitempty"` // 1-based; 0 when not paginated
	CharStart int     `json:"char_start,omitempty"`
	CharEnd   int     `json:"char_end,omitempty"`
	T0        float64 `json:"t0,omitempty"` // seconds, for audio
	T1        float64 `json:"t1,omitempty"`
}

// Asset is a file written by the sink: an extracted page image, a copied
// original, or a media slice.
type Asset struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	MIME  string `json:"mime"`
	Bytes int64  `json:"bytes"`
}

// Segment is one embeddable unit, and the contract between ingest and the
// embedder:
//
//	KindText  -> embed Text
//	KindImage -> embed the bytes at Asset.Path (llama.cpp wants a file:// URL)
//	KindAudio -> embed the slice at Asset.Path (T0..T1 within it)
//
// Text always stays for citation and lexical search, even when the payload is
// media.
type Segment struct {
	ID       string     `json:"id"`
	SourceID string     `json:"source_id"`
	DocID    string     `json:"doc_id"` // derived document; a PDF yields two
	Kind     Kind       `json:"kind"`
	Ordinal  int        `json:"ordinal"`
	Text     string     `json:"text"`
	Asset    *Asset     `json:"asset,omitempty"`
	Span     Provenance `json:"span"`
}

func (s Segment) String() string {
	return fmt.Sprintf("%s[%d] page=%d chars=%d-%d asset=%v",
		s.DocID, s.Ordinal, s.Span.Page, s.Span.CharStart, s.Span.CharEnd, s.Asset != nil)
}
