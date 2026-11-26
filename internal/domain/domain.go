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
	KindAudio Kind = "audio" // one clip window of a recording; Text is empty
)

// Media reports whether a segment carries a file to show a model rather than text to
// paste at one. It is the axis that decides how evidence reaches a prompt, so the two
// consumers of evidence share the answer instead of each asking about kinds.
//
// Kinds are named rather than inferred from "not text": a kind this switch does not
// know is not silently treated as media. A new input modality lands here, in
// retrieve's mapping from a kind to a wire kind, and in llm's MediaPart and Caps.Allows.
func (k Kind) Media() bool {
	switch k {
	case KindImage, KindAudio:
		return true
	}
	return false
}

// SourceKind is the original file's type, independent of how it is segmented.
type SourceKind string

const (
	SourcePDF         SourceKind = "pdf"         // text layer + page images
	SourceImage       SourceKind = "image"       // one image
	SourceAudio       SourceKind = "audio"       // sliced into clips, embedded whole
	SourceText        SourceKind = "text"        // plain text, markdown, html, csv, code
	SourceUnsupported SourceKind = "unsupported" // stored, not searchable
)

// FindingCode says which kind of thing went wrong on the way to a status. It is a small
// vocabulary, and it is small because it answers a question a sentence cannot: what can the
// person reading this actually do?
//
// Tool missing is the only kind whose fix lives outside libris — install the program, then run
// the file again, and the run is worth doing. Stage failed is everything the pipeline noticed
// about this one file: re-running it is a guess, so the UI says what was lost and lets the
// person decide. Anything that would need a third word to explain is not a finding, it is the
// failure itself, and belongs in Detail with a status.
type FindingCode string

const (
	// FindingToolMissing names a program libris shells out to that this machine does not
	// have. The file is fine; the run was not able to finish.
	FindingToolMissing FindingCode = "tool_missing"
	// FindingStageFailed names a stage that gave up on this file while the rest of the run
	// stood — a page raster that would not render, an extraction that errored.
	FindingStageFailed FindingCode = "stage_failed"
)

// Finding is one non-fatal thing a run noticed. The code is the vocabulary; Detail is the half
// the code cannot say — which program was missing, or what the stage actually reported — and it
// is free text because that half is for a human reading a row, not for a program to switch on.
type Finding struct {
	Code   FindingCode `json:"code"`
	Detail string      `json:"detail"`
}

// ToolMissing is a finding for a program that was not on PATH.
func ToolMissing(tool string) Finding {
	return Finding{Code: FindingToolMissing, Detail: tool}
}

// StageFailed is a finding for a stage that gave up, named and with what it said.
func StageFailed(stage string, err error) Finding {
	return Finding{Code: FindingStageFailed, Detail: stage + ": " + err.Error()}
}

// Source is the uploaded file. The original is kept on disk while any notebook
// lists this source, so a citation can be opened later; the last membership to let
// go is the one act that may remove it.
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

	Status SourceStatus `json:"status"`
	Detail string       `json:"detail,omitempty"` // why unsupported or failed
	// Findings is what a run noticed on its way to the status above: a stage that
	// did not run, a tool this machine does not have. It is typed rather than
	// prose because the three things a person can do about a finding are
	// different, and a sentence cannot be asked which one applies.
	Findings []Finding `json:"findings,omitempty"`
	AddedAt  int64     `json:"added_at"` // unix seconds

	// SegmentCount is read from the database rather than stored: it is how the
	// UI shows "8 chunks" next to a finished source.
	SegmentCount int `json:"segment_count"`

	// EmbeddedCount is how many of those chunks have a vector yet. It only differs
	// from SegmentCount while the source is processing, which is the one moment the
	// number is wanted: "1 234 / 3 000 chunks" is the difference between a wait with
	// a shape and a spinner that could be stuck.
	EmbeddedCount int `json:"embedded_count"`

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
	StatusStopped     SourceStatus = "stopped"     // the person who uploaded it asked it to stop
)

// Provenance points back into the original file. Every segment carries one so
// the UI can open a PDF at the page a chunk came from.
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

	// T0 and T1 are the seconds of a recording one clip covers, and they are floats
	// because a window boundary can land half-way between two. No omitempty on T0:
	// the first clip starts at zero seconds, and that is a real value a client must
	// not read as "absent". Both are 0 for every other kind, where the columns stay
	// unread.
	T0 float64 `json:"t0"`
	T1 float64 `json:"t1,omitempty"`
}

// Asset is a file written by the sink: an extracted page image, a copied
// original, or a clip cut from a recording.
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
//	KindAudio -> the same, with the clip's bytes and the audio wire kind; Text is
//	            empty and Span carries the window in T0/T1.
//
// Text stays for citation and lexical search wherever the source has any. A clip is
// the exception ADR 0010 accepts: there are no words to store, so the segment is
// found by its vector alone and quoted only by the model that hears it.
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
// a consequence this design accepts up front.
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
