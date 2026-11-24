package domain

import (
	"strings"
	"testing"
)

func TestSegmentString(t *testing.T) {
	seg := Segment{DocID: "abc", Ordinal: 3, Kind: KindText, Span: Provenance{Page: 7, CharStart: 10, CharEnd: 42}}
	got := seg.String()
	for _, want := range []string{"abc", "3", "page=7", "10-42"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}

func TestKinds(t *testing.T) {
	// These strings are persisted, so treat them as an API.
	if KindText != "text" || KindImage != "image" {
		t.Fatal("segment kinds changed")
	}
	if SourcePDF != "pdf" || SourceUnsupported != "unsupported" {
		t.Fatal("source kinds changed")
	}
}
