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

	// OriginalPath is where the kept copy lives, never the upload site's path.
	OriginalPath string `json:"original_path"`

	Status   SourceStatus `json:"status"`
	Detail   string       `json:"detail,omitempty"`   // why unsupported or failed
	Warnings []string     `json:"warnings,omitempty"` // non-fatal findings
	AddedAt  int64        `json:"added_at"`           // unix seconds

	// SegmentCount is read from the database rather than stored: it is how the
	// UI shows "8 chunks" next to a finished source, and is 0 while processing.
	SegmentCount int `json:"segment_count"`
}

// SourceStatus is where a source has got. Ingest is asynchronous, so an upload
// answers before any of it has happened.
type SourceStatus string

const (
	StatusProcessing  SourceStatus = "processing"  // queued or running
	StatusReady       SourceStatus = "ready"       // segments written
	StatusUnsupported SourceStatus = "unsupported" // read, but nothing to extract
	StatusFailed      SourceStatus = "failed"      // crashed, or died mid-flight
)

// Provenance points back into the original file. Every segment carries one so
// the UI can open the PDF at a page, or the audio at a timestamp.
//
// Char offsets are byte offsets into the extracted text of a page: CharStart
// counts from the start of Page, CharEnd from the start of PageEnd. PageEnd is
// 0 unless the chunk straddles a page break, in which case it is the page the
// chunk ends on. A chunk never spans more than two pages, because a page holds
// far more words than a chunk.
type Provenance struct {
	Page      int     `json:"page,omitempty"` // 1-based; 0 when not paginated
	PageEnd   int     `json:"page_end,omitempty"`
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

// Notebook is a workspace: its own sources, its own chats, and the scope every
// answer is retrieved within.
type Notebook struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"` // unix seconds
}

func (s Segment) String() string {
	return fmt.Sprintf("%s[%d] page=%d chars=%d-%d asset=%v",
		s.DocID, s.Ordinal, s.Span.Page, s.Span.CharStart, s.Span.CharEnd, s.Asset != nil)
}
