package models_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"libris/internal/models"
)

// A catalogue entry the tests own: small bodies, digests computed here, so the download path
// is tested against real HTTP rather than against a stub of itself.
type body struct {
	name string
	data []byte
}

func (b body) digest() string { h := sha256.Sum256(b.data); return hex.EncodeToString(h[:]) }

// spy is what the fake server remembers. A finished file and a skipped file look identical on
// disk, so "it did not fetch it again" is only visible from the requests.
type spy struct {
	// asks counts every request per file.
	asks map[string]int
	// ranges counts the ones that carried a Range header, which is how "it resumed" and
	// "it restarted" are told apart.
	ranges map[string]int
}

func fixture(t *testing.T, bodies ...body) (*models.Fetcher, string, models.Entry, *spy) {
	t.Helper()

	want := map[string]body{}
	for _, b := range bodies {
		want[b.name] = b
	}
	spy := &spy{asks: map[string]int{}, ranges: map[string]int{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		b, ok := want[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		spy.asks[name]++
		var from int64
		if rng := r.Header.Get("Range"); rng != "" {
			if _, err := fmt.Sscanf(rng, "bytes=%d-", &from); err != nil {
				t.Errorf("bad Range %q", rng)
			}
			spy.ranges[name]++
		}
		if from > int64(len(b.data)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if from > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, int64(len(b.data))-1, len(b.data)))
			w.WriteHeader(http.StatusPartialContent)
		}
		w.Write(b.data[from:])
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	e := models.Entry{
		ID:     "two-parts",
		Role:   models.RoleChat,
		Label:  "Two parts",
		Quant:  "Q8",
		Repo:   "someone/two-parts",
		Rev:    strings.Repeat("0", 40),
		Model:  models.Part{Name: bodies[0].name, Bytes: int64(len(bodies[0].data)), SHA256: bodies[0].digest()},
		MMProj: models.Part{Name: bodies[1].name, Bytes: int64(len(bodies[1].data)), SHA256: bodies[1].digest()},
	}
	f := models.NewFetcher(dir, models.Seams{Base: srv.URL})
	return f, dir, e, spy
}

func TestDownloadVerifiesAndPlacesBothParts(t *testing.T) {
	a := body{"one.gguf", bytes.Repeat([]byte("abcdefgh"), 4096)}
	b := body{"mmproj-BF16.gguf", bytes.Repeat([]byte("wxyz"), 900)}
	f, dir, e, sp := fixture(t, a, b)

	if err := f.Start(e); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.Wait()

	if p := f.Progress(); p.State != models.StateIdle || p.Error != "" {
		t.Fatalf("progress after a good download: %+v", p)
	}
	if !models.StatusOf(dir, e).Complete {
		t.Fatal("a finished download is not complete")
	}
	for _, body := range []body{a, b} {
		got, err := os.ReadFile(e.Path(dir, models.Part{Name: body.name}))
		if err != nil {
			t.Fatalf("%s: %v", body.name, err)
		}
		if !bytes.Equal(got, body.data) {
			t.Errorf("%s: %d bytes on disk, want %d", body.name, len(got), len(body.data))
		}
		if _, err := os.Stat(models.PartPath(e.Path(dir, models.Part{Name: body.name}))); !os.IsNotExist(err) {
			t.Errorf("%s: the part file survived the rename", body.name)
		}
		rec, err := os.ReadFile(e.Path(dir, models.Part{Name: body.name}) + ".libris-sha256")
		if err != nil || strings.TrimSpace(string(rec)) != body.digest() {
			t.Errorf("%s: no record of the digest that was checked: %q %v", body.name, rec, err)
		}
	}
	for name, n := range sp.ranges {
		if n != 0 {
			t.Errorf("%s was fetched with a Range header %d times on a clean disk", name, n)
		}
	}
}

// Resume is the whole reason a part file exists: a gigabyte that restarts because a laptop
// closed its lid is a download nobody finishes.
func TestDownloadResumesFromAPart(t *testing.T) {
	a := body{"one.gguf", bytes.Repeat([]byte("0123456789"), 5000)}
	b := body{"mmproj-BF16.gguf", []byte("tail")}
	f, dir, e, sp := fixture(t, a, b)

	cut := len(a.data) / 3
	if err := os.MkdirAll(e.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(models.PartPath(e.Path(dir, e.Model)), a.data[:cut], 0o600); err != nil {
		t.Fatal(err)
	}

	if err := f.Start(e); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.Wait()

	if p := f.Progress(); p.State != models.StateIdle || p.Error != "" {
		t.Fatalf("resumed download: %+v", p)
	}
	if sp.ranges[a.name] != 1 {
		t.Errorf("the weights were asked for with Range %d times; the saved bytes were ignored", sp.ranges[a.name])
	}
	got, err := os.ReadFile(e.Path(dir, e.Model))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, a.data) {
		t.Errorf("resumed file is %d bytes, want %d", len(got), len(a.data))
	}
}

// A wrong digest means the bytes on disk are known to be wrong. Keeping them would make every
// later attempt fail the same way, forever.
func TestDownloadThrowsAwayADigestMismatch(t *testing.T) {
	a := body{"one.gguf", []byte("the bytes upstream sent")}
	b := body{"mmproj-BF16.gguf", []byte("tail")}
	f, dir, e, _ := fixture(t, a, b)

	// Lie about the digest the entry expects.
	e.Model.SHA256 = strings.Repeat("f", 64)
	if err := f.Start(e); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.Wait()

	p := f.Progress()
	if p.State != models.StateFailed || !strings.Contains(p.Error, "digest") {
		t.Fatalf("a mismatch must be reported as a failure: %+v", p)
	}
	if _, err := os.Stat(models.PartPath(e.Path(dir, e.Model))); !os.IsNotExist(err) {
		t.Error("the part file survived a digest mismatch, so the next attempt fails the same way")
	}
	if models.StatusOf(dir, e).Complete {
		t.Error("a model nobody verified reports itself complete")
	}
}

// Stop is "not now". The bytes stay, the record says nothing failed, and the two are told
// apart by the button that throws them away.
func TestStopKeepsThePartAndDiscardRemovesIt(t *testing.T) {
	a := body{"one.gguf", []byte("x")}
	b := body{"mmproj-BF16.gguf", []byte("y")}
	f, dir, e, _ := fixture(t, a, b)

	// Replace one body with a stream that does not end, so the stop happens mid-body without
	// the test sleeping to guess where the middle is.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		deadline := time.After(5 * time.Second)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-deadline:
				return
			default:
			}
			if _, err := w.Write(bytes.Repeat([]byte("z"), 1<<16)); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	f = models.NewFetcher(dir, models.Seams{Base: srv.URL})

	if err := f.Start(e); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Poll the progress it publishes rather than a clock: the condition is "bytes arrived".
	seen := false
	for i := 0; i < 2000 && !seen; i++ {
		if f.Progress().Done > 0 {
			seen = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !seen {
		t.Fatal("no bytes arrived, so this proves nothing about stopping")
	}
	f.Stop()

	if p := f.Progress(); p.State != models.StateIdle || p.Error != "" {
		t.Errorf("a stop is a bookmark, not a failure: %+v", p)
	}
	part := models.PartPath(e.Path(dir, e.Model))
	st, err := os.Stat(part)
	if err != nil {
		t.Fatalf("the part file was dropped by a stop: %v", err)
	}
	if st.Size() == 0 {
		t.Error("the part file is empty")
	}

	if err := f.Discard(e); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Error("Discard left the bytes behind")
	}
}

// Two at once would make one progress bar describe two downloads, and both slower.
func TestOneDownloadAtATime(t *testing.T) {
	f, _, e, _ := fixture(t,
		body{"one.gguf", bytes.Repeat([]byte("m"), 1<<20)},
		body{"mmproj-BF16.gguf", []byte("y")},
	)
	if err := f.Start(e); err != nil {
		t.Fatal(err)
	}
	defer f.Wait()

	if err := f.Start(e); !errors.Is(err, models.ErrBusy) {
		t.Errorf("second Start = %v, want ErrBusy", err)
	}
}

// A server that ignores Range answers 200 with the whole body. Trusting it would concatenate
// the body onto the bytes already saved.
func TestDownloadRestartsWhenRangeIsIgnored(t *testing.T) {
	a := body{"one.gguf", []byte("the whole body")}
	b := body{"mmproj-BF16.gguf", []byte("tail")}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		switch name {
		case a.name:
			w.Write(a.data) // 200, entire body, Range ignored
		case b.name:
			w.Write(b.data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	e := models.Entry{
		ID: "no-range", Role: models.RoleChat, Repo: "someone/no-range", Rev: strings.Repeat("0", 40),
		Model:  models.Part{Name: a.name, Bytes: int64(len(a.data)), SHA256: a.digest()},
		MMProj: models.Part{Name: b.name, Bytes: int64(len(b.data)), SHA256: b.digest()},
	}
	if err := os.MkdirAll(e.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(models.PartPath(e.Path(dir, e.Model)), a.data[:4], 0o600); err != nil {
		t.Fatal(err)
	}

	f := models.NewFetcher(dir, models.Seams{Base: srv.URL})
	if err := f.Start(e); err != nil {
		t.Fatal(err)
	}
	f.Wait()

	if p := f.Progress(); p.State != models.StateIdle || p.Error != "" {
		t.Fatalf("a server without Range must still produce a file: %+v", p)
	}
	got, err := os.ReadFile(e.Path(dir, e.Model))
	if err == nil && !bytes.Equal(got, a.data) {
		t.Errorf("file is %q, want the body exactly once", got)
	}
	if !models.StatusOf(dir, e).Complete {
		t.Error("the entry did not end up complete")
	}
}

// A model is gigabytes, so "resume" has to mean two different things depending on which file is
// where: a part file is continued, and a file already in its final place is left alone.
// Without the second half a stop in the middle re-fetched the gigabyte it had already placed,
// and overwrote a file whose digest it had checked with whatever upstream served that day.
func TestFetchLeavesAPlacedFileAlone(t *testing.T) {
	a := body{"one.gguf", bytes.Repeat([]byte("0123456789abcdef"), 4000)}
	b := body{"mmproj-BF16.gguf", bytes.Repeat([]byte("tail"), 500)}
	f, dir, e, sp := fixture(t, a, b)

	if err := f.Start(e); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.Wait()
	before := maps.Clone(sp.asks)
	if before[a.name] != 1 || before[b.name] != 1 {
		t.Fatalf("the first download asked for %v", before)
	}

	// Nothing on disk changed between these two lines. The second run must notice.
	if err := f.Start(e); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	f.Wait()

	if p := f.Progress(); p.State != models.StateIdle || p.Error != "" {
		t.Fatalf("a run with nothing to do: %+v", p)
	}
	for _, name := range []string{a.name, b.name} {
		if sp.asks[name] != before[name] {
			t.Errorf("%s was fetched again (%d asks, was %d) although it was already in place",
				name, sp.asks[name], before[name])
		}
	}
	for _, bd := range []body{a, b} {
		got, err := os.ReadFile(e.Path(dir, models.Part{Name: bd.name}))
		if err != nil || !bytes.Equal(got, bd.data) {
			t.Errorf("%s after a run with nothing to do: %d bytes, %v", bd.name, len(got), err)
		}
	}
}

// The marker is libris' own record, so its absence is not a verdict about the bytes: a GGUF
// copied into the models folder by hand is hashed and kept, and only a file that turns out to
// be something else is replaced.
func TestFetchKeepsAHandCopiedFileThatMatches(t *testing.T) {
	a := body{"one.gguf", bytes.Repeat([]byte("hand-copied"), 3000)}
	b := body{"mmproj-BF16.gguf", []byte("tail")}
	f, dir, e, sp := fixture(t, a, b)

	if err := os.MkdirAll(e.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.Path(dir, e.Model), a.data, 0o600); err != nil {
		t.Fatal(err)
	}
	// No marker, and the other part missing entirely: only one file is still to be fetched.
	if err := f.Start(e); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.Wait()

	if p := f.Progress(); p.State != models.StateIdle || p.Error != "" {
		t.Fatalf("a copied-in file should not have stopped the download: %+v", p)
	}
	if sp.asks[a.name] != 0 {
		t.Errorf("the hand-copied weights were fetched %d times anyway", sp.asks[a.name])
	}
	if _, err := os.ReadFile(e.Path(dir, e.Model) + ".libris-sha256"); err != nil {
		t.Errorf("a file that hashed correctly is now recorded as verified: %v", err)
	}
	if st := models.StatusOf(dir, e); !st.Complete || !st.Verified {
		t.Errorf("status after keeping a copied file: %+v", st)
	}
}

// The mirror case: right size, wrong bytes. Keeping it would mean answering questions with a
// model nobody checked, and the page would say "installed" about a lie.
func TestFetchReplacesAFileWithTheWrongDigest(t *testing.T) {
	a := body{"one.gguf", bytes.Repeat([]byte("0123456789abcdef"), 3000)}
	b := body{"mmproj-BF16.gguf", []byte("tail")}
	f, dir, e, sp := fixture(t, a, b)

	if err := os.MkdirAll(e.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	// Same length as the real body, different bytes.
	if err := os.WriteFile(e.Path(dir, e.Model), bytes.Repeat([]byte("x"), len(a.data)), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := f.Start(e); err != nil {
		t.Fatalf("Start: %v", err)
	}
	f.Wait()

	if p := f.Progress(); p.State != models.StateIdle || p.Error != "" {
		t.Fatalf("a wrong file of the right size should be replaced, not fatal: %+v", p)
	}
	if sp.asks[a.name] != 1 {
		t.Errorf("the wrong file was fetched %d times, want once", sp.asks[a.name])
	}
	got, err := os.ReadFile(e.Path(dir, e.Model))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, a.data) {
		t.Errorf("the wrong file survived: %d bytes of %q", len(got), got[:8])
	}
}
