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
		Span: Provenance{Page: 4, T0: 0},
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
// ?page=7, ?t=84.2-104.6 — depends on this shape: snake_case throughout, a zero
// timestamp that survives, and no absolute paths anywhere.
func TestCitationJSONIsWhatTheFrontendReads(t *testing.T) {
	msg := Message{
		ID: 12, ChatID: "c1", Role: RoleAssistant, Content: "4.2 K [[Chunk 1]].",
		Status: MessageAnswered, CreatedAt: 1794000000,
		Citations: []Citation{{
			Chunk: 1, Segment: "sha1:0002", DocID: "sha1#pages", SourceID: "sha1",
			SourceName: "paper.pdf", Kind: KindAudio, Ordinal: 2,
			Asset: &Asset{ID: "sha1/slices/slice-0003.wav", Path: "/data/hidden", MIME: "audio/wav", Bytes: 960},
			Span:  Provenance{T0: 0, T1: 30},
		}},
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`"chat_id":"c1"`, `"created_at":1794000000`, `"segment_id":"sha1:0002"`,
		`"doc_id":"sha1#pages"`, `"source_id":"sha1"`, `"source_name":"paper.pdf"`,
		`"kind":"audio"`, `"ordinal":2`, `"t1":30`, `"mime":"audio/wav"`, `"bytes":960`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}

	// t0 has no omitempty precisely because the first slice starts at zero seconds,
	// and a client that read it as "absent" could not open the audio at all.
	if !strings.Contains(body, `"t0":0`) {
		t.Errorf("the zero timestamp was dropped, so the first slice is unopenable: %s", body)
	}
	for _, forbidden := range []string{"/data/hidden", `"path"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("%q reached the wire: %s", forbidden, body)
		}
	}
}
