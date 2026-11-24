package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// A citation is stored by value, so a later rebuild of the corpus cannot reach into
// an answer written before it. The copy has to be real: sharing the asset pointer
// would mean a re-ingest that rewrites the segment also rewrites what the user was
// told they were citing.
func TestCitationFromCopiesTheAsset(t *testing.T) {
	seg := Segment{
		ID: "sha1:0003", SourceID: "sha1", DocID: "sha1#pages", Kind: KindImage, Ordinal: 3,
		Text: "Figure 2. Sea surface temperature.",
		Asset: &Asset{
			ID: "sha1/pages/page-0004.png", Path: "/data/libris/assets/sha1/pages/page-0004.png",
			MIME: "image/png", Bytes: 118_432,
		},
		Span: Provenance{Page: 4},
	}

	got := CitationFrom(2, "paper.pdf", seg)
	if got.Chunk != 2 || got.SourceName != "paper.pdf" || got.Segment != seg.ID ||
		got.DocID != seg.DocID || got.SourceID != seg.SourceID || got.Kind != KindImage ||
		got.Ordinal != 3 || got.Quote != seg.Text {
		t.Errorf("citation = %+v", got)
	}
	if got.Asset == seg.Asset {
		t.Fatal("the citation shares the segment's asset pointer, so a rebuild rewrites it")
	}
	if got.Asset.Path != "" {
		t.Errorf("asset path = %q, and the browser must never learn where the data directory is", got.Asset.Path)
	}

	seg.Asset.MIME = "image/jpeg"
	if got.Asset.MIME != "image/png" {
		t.Errorf("citation asset followed the segment: %q", got.Asset.MIME)
	}

	if got.Asset.ID != "sha1/pages/page-0004.png" || got.Asset.Bytes != 118_432 {
		t.Errorf("asset = %+v", got.Asset)
	}
}

func TestCitationFromWithoutAnAsset(t *testing.T) {
	got := CitationFrom(1, "notes.txt", Segment{ID: "sha2:0000", SourceID: "sha2", Kind: KindText, Text: "a passage"})
	if got.Asset != nil {
		t.Errorf("asset = %+v, want none for a chunk with no file of its own", got.Asset)
	}
	if got.Quote != "a passage" {
		t.Errorf("quote = %q", got.Quote)
	}
}

// These strings are written to the database and read by the UI to decide how an answer
// is drawn — an unchecked answer and a grounded one must not look identical. Treat
// them as an API.
func TestMessageStatusesAreTheAPI(t *testing.T) {
	for status, want := range map[MessageStatus]string{
		MessageAnswered:    "answered",
		MessageNoInfo:      "no-info",
		MessageUngrounded:  "ungrounded",
		MessageCanceled:    "canceled",
		MessageInterrupted: "interrupted",
	} {
		if string(status) != want {
			t.Errorf("status = %q, want %q", status, want)
		}
	}
	if RoleUser != "user" || RoleAssistant != "assistant" {
		t.Error("message roles changed")
	}
}

// The citation route the frontend builds — #/source/<id>?page=7&c=1200-1640,
// ?page=7 — depends on this shape: snake_case throughout, an ordinal of zero that
// survives, and no absolute paths anywhere.
func TestCitationJSONIsWhatTheFrontendReads(t *testing.T) {
	msg := Message{
		ID: 12, ChatID: "c1", Role: RoleAssistant, Content: "4.2 K [[Chunk 1]].",
		Status: MessageAnswered, CreatedAt: 1794000000,
		Citations: []Citation{{
			Chunk: 1, Segment: "sha1:0002", DocID: "sha1#pages", SourceID: "sha1",
			SourceName: "paper.pdf", Kind: KindImage, Ordinal: 0,
			Asset: &Asset{ID: "sha1/pages/page-0003.png", Path: "/data/hidden", MIME: "image/png", Bytes: 960},
			Span:  Provenance{Page: 3},
		}},
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`"chat_id":"c1"`, `"created_at":1794000000`, `"segment_id":"sha1:0002"`,
		`"doc_id":"sha1#pages"`, `"source_id":"sha1"`, `"source_name":"paper.pdf"`,
		`"kind":"image"`, `"mime":"image/png"`, `"bytes":960`, `"page":3`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}

	// Ordinal carries no omitempty, and the first segment of a document is ordinal
	// zero — a client that read a missing ordinal as "absent" would drop the one
	// citation that exists. This is the json-omitempty-shrinks-nothing guard.
	if !strings.Contains(body, `"ordinal":0`) {
		t.Errorf("the zero ordinal was dropped: %s", body)
	}
	for _, forbidden := range []string{"/data/hidden", `"path"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("%q reached the wire: %s", forbidden, body)
		}
	}
}
