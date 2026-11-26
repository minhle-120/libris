package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"libris/internal/domain"
)

// A recording's original is not a document, and a browser decides for itself whether to
// play one or download it. The player page is how libris takes that decision back, so
// what matters about it is that a tab gets an <audio> element pointed at the file, and
// that the file's name — which came from whoever uploaded it — cannot become markup.
func TestPlayerPagePlaysTheRecording(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "standup.m4a")
	if err := os.WriteFile(path, []byte("fakely audio bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	api := &fakeAPI{sources: []domain.Source{{
		ID: "sha1", Name: "standup.m4a", Kind: domain.SourceAudio,
		OriginalPath: path, Bytes: 40 << 20, SegmentCount: 83,
	}}}
	srv := newAppServer(t, api)

	resp := request(t, srv, "GET", "/api/sources/sha1/player", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content type = %q", got)
	}

	body := readAll(t, resp)
	for _, want := range []string{
		`<audio id="play" controls`,
		`src="/api/sources/sha1/original"`,
		"standup.m4a",
		"83 clips",
		"#t=start,end", // the citation's window, read out of the hash
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}

	// The page is generated from a row that can be renamed, so a tab must never be
	// shown a stale one.
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control = %q", got)
	}
}

// The name is upload text. It belongs inside the page as text and nowhere else.
func TestPlayerPageEscapesTheName(t *testing.T) {
	dir := t.TempDir()
	name := `<img src=x onerror=alert(1)>.m4a`
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	api := &fakeAPI{sources: []domain.Source{{
		ID: "sha1", Name: name, Kind: domain.SourceAudio, OriginalPath: path,
	}}}
	srv := newAppServer(t, api)

	body := readAll(t, request(t, srv, "GET", "/api/sources/sha1/player", nil))
	if strings.Contains(body, "<img src=x onerror") {
		t.Error("the file name was written into the page as markup")
	}
	if !strings.Contains(body, "&lt;img src=x onerror") {
		t.Error("the name is not in the page at all, escaped")
	}
}

// The page explains its own hash protocol in comments, and the shape of this file is
// the reason: rendering it through html/template rewrites the script body and those
// comments arrive at the browser as blank lines. Pinning the prose is pinning the
// renderer choice.
func TestPlayerPageKeepsItsExplanations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tape.wav")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	api := &fakeAPI{sources: []domain.Source{{
		ID: "sha1", Name: "tape.wav", Kind: domain.SourceAudio, OriginalPath: path,
	}}}
	srv := newAppServer(t, api)

	body := readAll(t, request(t, srv, "GET", "/api/sources/sha1/player", nil))
	if !strings.Contains(body, "// A citation names its window in the hash") {
		t.Error("the script's comments were stripped on the way to the browser")
	}
}

// Anything that is not a recording has nothing to play. The page's controls would sit
// there doing nothing, so the URL says so instead.
func TestPlayerRefusesANonRecording(t *testing.T) {
	api := &fakeAPI{sources: []domain.Source{{
		ID: "sha1", Name: "paper.pdf", Kind: domain.SourcePDF, OriginalPath: "/tmp/paper.pdf",
	}}}
	srv := newAppServer(t, api)

	resp := request(t, srv, "GET", "/api/sources/sha1/player", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := readAll(t, resp); !strings.Contains(got, "not a recording") {
		t.Errorf("body = %s", got)
	}
}

// The row outlives the file sometimes. Only a missing file is worth a 404 here: the
// page's own error handler covers a codec the browser does not know, and that one is
// worth a download link rather than an error.
func TestPlayerMissingFromDisk(t *testing.T) {
	api := &fakeAPI{sources: []domain.Source{{
		ID: "sha1", Name: "gone.m4a", Kind: domain.SourceAudio,
		OriginalPath: filepath.Join(t.TempDir(), "gone.m4a"),
	}}}
	srv := newAppServer(t, api)

	resp := request(t, srv, "GET", "/api/sources/sha1/player", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := readAll(t, resp); !strings.Contains(got, "missing from disk") {
		t.Errorf("body = %s", got)
	}
}
