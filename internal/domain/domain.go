// Package domain holds the vocabulary shared by every other package: plain
// types, no dependencies, and no behaviour worth mocking.
package domain

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned when an id does not resolve. Every layer wraps it, so
// errors.Is works from the transport down through the store without any layer
// importing another layer's error values.
var ErrNotFound = errors.New("not found")

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

// Source is the uploaded file. The original is kept on disk while any notebook
// lists this source, so a citation can be opened later; the last membership to let
// go is the one act that may remove it (ADR 0013).
// Field names are snake_case on the wire throughout domain, so the frontend
// reads one convention.
type Source struct {
	ID     string     `json:"id"` // sha256 of the contents: identity and dedupe key
	Name   string     `json:"name"`
	Kind   SourceKind `json:"kind"`
	SHA256 string     `json:"sha256"`
	Bytes  int64      `json:"bytes"`

	// OriginalPath is where the kept copy lives, never the upload site's path.
	// It is absolute on the machine that owns the data directory, and it is not
	// sent to the client: the API offers /api/sources/{id}/original instead, so
	// the browser never needs to know where the data directory is.
	OriginalPath string `json:"-"`

	Status   SourceStatus `json:"status"`
	Detail   string       `json:"detail,omitempty"`   // why unsupported or failed
	Warnings []string     `json:"warnings,omitempty"` // non-fatal findings
	AddedAt  int64        `json:"added_at"`           // unix seconds

	// SegmentCount is read from the database rather than stored: it is how the
	// UI shows "8 chunks" next to a finished source, and is 0 while processing.
	SegmentCount int `json:"segment_count"`

	// NotebooksUsing counts the memberships this source has, which is how many
	// notebooks would have to lose it before the file is worth deleting. The UI
	// says "remove from this notebook" or "delete" with it, because the two are
	// the same click and only one of them costs anything.
	NotebooksUsing int `json:"notebooks_using"`
}

// An Orphan is a source that no notebook refers to any more, together with every
// file the app owns for it: the kept original, and the derived page rasters,
// slices and images.
//
// The files travel with the fact because the database cannot describe them
// afterwards — its rows are what just got deleted. Whoever collects an orphan
// unlinks these paths, and only after the transaction that dropped the rows has
// committed: a file deleted while its citation still resolves is a broken
// answer, and a file left behind is only disk.
type Orphan struct {
	ID       string
	Name     string
	Original string
	Assets   []string
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
	Page      int `json:"page,omitempty"` // 1-based; 0 when not paginated
	PageEnd   int `json:"page_end,omitempty"`
	CharStart int `json:"char_start,omitempty"`
	CharEnd   int `json:"char_end,omitempty"`
	// T0 keeps no omitempty: the first audio slice starts at zero seconds, and
	// that is a real value the client must not read as "absent".
	T0 float64 `json:"t0"` // seconds, for audio
	T1 float64 `json:"t1,omitempty"`
}

// Asset is a file written by the sink: an extracted page image, a copied
// original, or a media slice.
type Asset struct {
	ID string `json:"id"` // also its URL path under /api/assets/
	// Path is absolute on this machine, and internal: the file endpoints look it
	// up by ID rather than accepting a path, so it stays out of the JSON.
	Path  string `json:"-"`
	MIME  string `json:"mime"`
	Bytes int64  `json:"bytes"`
}

// Segment is one embeddable unit, and the contract between ingest and the
// embedder:
//
//	KindText  -> embed Text
//	KindImage -> embed the bytes at Asset.Path, as a base64 data URL: verified
//	            against llama.cpp's server README, image_url.url accepts a data
//	            URI, a remote URL or a file:// path, and only the last one needs
//	            the server started with --media-path. Libris owns the assets
//	            directory, so it reads the raster off disk instead of asking the
//	            user for a flag.
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

// Embedding is one segment's vector, in the shape the store keeps.
//
// The model is recorded because a vector is only comparable with vectors from
// the same model: changing embeddingModel invalidates every stored vector,
// which is the consequence ADR 0004 accepts.
type Embedding struct {
	SegmentID string
	Model     string
	Dim       int
	Vector    []float32
}

// Notebook is a workspace: its own sources, its own chats, and the scope every
// answer is retrieved within.
type Notebook struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"` // unix seconds

	// SourceCount is read from the database rather than stored, the way
	// Source.SegmentCount is: it is what the sidebar shows beside a notebook.
	// It is 0 on a notebook that was just created, before any query reads it.
	SourceCount int `json:"source_count"`
}

func (s Segment) String() string {
	return fmt.Sprintf("%s[%d] page=%d chars=%d-%d asset=%v",
		s.DocID, s.Ordinal, s.Span.Page, s.Span.CharStart, s.Span.CharEnd, s.Asset != nil)
}
