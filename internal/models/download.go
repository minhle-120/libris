package models

// Downloading a model is not downloading a program.
//
// The engine is 35 MB and gets replaced wholesale; these are 1.4 GB files whose whole purpose
// is to be *the same weights twice*. So the three properties that matter here are: a stopped
// download keeps its bytes (starting a gigabyte again is not what "not now" means), every byte
// is hashed on the way in rather than re-read afterwards (a second pass over 1.4 GB is a page
// that spins), and a file that fails its digest is thrown away rather than retried forever.
// Resume is honest only because Hugging Face answers Range requests — verified: 206 with
// accept-ranges: bytes on the pinned URLs.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrBusy means something is already downloading. One at a time is not politeness toward the
// CDN: two 1.4 GB writes to the same disk make both slower, and one progress bar cannot
// describe both without lying about one of them.
var ErrBusy = errors.New("models: a model download is already running")

// State is where a download stands, in the words the page uses.
type State string

const (
	StateIdle        State = "idle"
	StateDownloading State = "downloading"
	StateVerifying   State = "verifying"
	StateFailed      State = "failed"
)

// Progress is one download as the page sees it. Total and Done cover the whole entry —
// weights and projector together — because a bar that restarts when the projector begins reads
// as a download that failed and started over.
type Progress struct {
	ID      string    `json:"id"`
	State   State     `json:"state"`
	Part    string    `json:"part,omitempty"`
	Done    int64     `json:"done"`
	Total   int64     `json:"total"`
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"-"`
}

// Running reports whether a bar should be moving.
func (p Progress) Running() bool {
	return p.State == StateDownloading || p.State == StateVerifying
}

// Fetcher downloads catalogue entries into a models directory, one at a time.
type Fetcher struct {
	dir  string
	base string // host override, so the download path can be tested without Hugging Face
	http *http.Client

	mu     sync.Mutex
	prog   Progress
	cancel context.CancelFunc
	done   chan struct{}
}

// NewFetcher points a fetcher at the models directory it owns. seams exists for the same
// reason engine's does: a package that can only fetch from huggingface.co can only be tested
// by huggingface.co.
type Seams struct {
	Base string       // scheme://host to fetch from instead of huggingface.co
	HTTP *http.Client // nil means a client with no timeout: the caller's context is the bound
}

func NewFetcher(dir string, seams Seams) *Fetcher {
	hc := seams.HTTP
	if hc == nil {
		// No Client.Timeout on purpose: it would cap a multi-gigabyte stream at a fixed
		// number of seconds regardless of how slowly it is arriving (same reasoning as llm).
		hc = &http.Client{}
	}
	return &Fetcher{dir: dir, base: seams.Base, http: hc}
}

// Start begins fetching an entry and returns immediately. The caller's page polls State — a
// request that held a 1.4 GB download open would be a request a proxy or a browser tab is
// free to drop, and a dropped request must not be what cancels the download.
func (f *Fetcher) Start(e Entry) error {
	ctx, cancel := context.WithCancel(context.Background())

	f.mu.Lock()
	if f.cancel != nil {
		f.mu.Unlock()
		cancel()
		return ErrBusy
	}
	f.prog = Progress{ID: e.ID, State: StateDownloading, Total: e.Bytes(), Started: time.Now()}
	f.cancel = cancel
	f.done = make(chan struct{})
	f.mu.Unlock()

	go func() {
		defer close(f.done)
		err := f.fetch(ctx, e)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case err == nil:
			f.prog.State = StateIdle
			f.prog.Done = e.Bytes()
			f.prog.Error = ""
		case errors.Is(err, context.Canceled):
			// "Not now", not a failure: the bytes stay, and the next Start resumes them.
			f.prog = Progress{ID: e.ID, State: StateIdle}
		default:
			f.prog.State = StateFailed
			f.prog.Error = err.Error()
		}
		f.cancel = nil
		cancel()
	}()
	return nil
}

// Stop interrupts the running download and keeps its bytes. Stopping a download is not the
// same act as stopping the server or cancelling a job (AGENTS §11); it is a bookmark.
func (f *Fetcher) Stop() {
	f.mu.Lock()
	cancel := f.cancel
	done := f.done
	f.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done // the record, not the socket: the page must not read a half-written state
	}
}

// Discard throws a stopped download's bytes away — the button that means "I did mean cancel",
// as distinct from Stop.
func (f *Fetcher) Discard(e Entry) error {
	dir := e.Dir(f.dir)
	var first error
	for _, p := range e.Parts() {
		if err := os.Remove(PartPath(filepath.Join(dir, p.Name))); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	return first
}

// Progress reports the running download, or the last one's failure.
func (f *Fetcher) Progress() Progress {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prog
}

// Wait blocks until the running download finishes. It exists for a test and for a shutdown
// that must not leave a `.part` open behind a renamed directory.
func (f *Fetcher) Wait() {
	f.mu.Lock()
	done := f.done
	f.mu.Unlock()
	if done != nil {
		<-done
	}
}

// fetch runs one entry: every part in order, weights first.
func (f *Fetcher) fetch(ctx context.Context, e Entry) error {
	if err := os.MkdirAll(e.Dir(f.dir), 0o755); err != nil {
		return fmt.Errorf("models: create %s: %w", e.ID, err)
	}
	var already int64
	for _, p := range e.Parts() {
		if sz, err := os.Stat(filepath.Join(e.Dir(f.dir), p.Name)); err == nil && sz.Size() == p.Bytes {
			already += p.Bytes
		}
	}
	for _, p := range e.Parts() {
		if err := f.part(ctx, e, p, &already); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// part fetches one file into its final place, resuming and hashing as it goes.
func (f *Fetcher) part(ctx context.Context, e Entry, p Part, done *int64) error {
	final := filepath.Join(e.Dir(f.dir), p.Name)
	part := PartPath(final)

	// A file already in its final place is not fetched again — without this a resume
	// re-downloads the gigabytes a stop did not interrupt, and replaces a model it verified
	// once with whatever upstream serves now. The marker says libris hashed it on the way in;
	// where there is no marker the bytes are still checkable, so they are hashed now rather
	// than overwritten: a GGUF somebody copied into the folder is used, and only a file that
	// turns out to be the wrong one is replaced.
	if info, err := os.Stat(final); err == nil && info.Size() == p.Bytes {
		if sum, err := os.ReadFile(final + verifiedSuffix); err == nil &&
			strings.TrimSpace(string(sum)) == p.SHA256 {
			return nil
		}
		f.setProgress(e.ID, StateVerifying, p.Name, *done)
		h := sha256.New()
		if err := hashPrefix(final, h, p.Bytes); err == nil {
			if got := hex.EncodeToString(h.Sum(nil)); got == p.SHA256 {
				// The bytes are the ones the catalogue wants, whoever put them there. A
				// failed marker write only makes the page say "unverified" about a file it
				// just verified, so it is not worth failing the download over.
				MarkVerified(final, p.SHA256)
				return nil
			}
		}
		os.Remove(final)
	}

	exist, err := os.Stat(part)
	var off int64
	if err == nil {
		off = exist.Size()
		if off > p.Bytes {
			// A part bigger than the file it claims to be is not a download that was
			// interrupted; it is bytes from somewhere else. Start over rather than
			// concatenating nonsense onto it.
			os.Remove(part)
			off = 0
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("models: %s: %w", p.Name, err)
	}

	if off == p.Bytes {
		// The whole body is already on disk. Still verified below, because "the bytes are
		// here" and "the bytes are these bytes" are different claims.
	}

	h := sha256.New()
	if off > 0 {
		if err := hashPrefix(part, h, off); err != nil {
			// Unreadable halfway through: drop the part, since nothing can say what it holds.
			os.Remove(part)
			off = 0
			h = sha256.New()
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url(e, p), nil)
	if err != nil {
		return fmt.Errorf("models: %s: %w", p.Name, err)
	}
	if off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
	}
	f.setProgress(e.ID, StateDownloading, p.Name, *done)

	resp, err := f.http.Do(req)
	if err != nil {
		return fmt.Errorf("models: fetch %s: %w", p.Name, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusPartialContent && off > 0:
		// The byte range the part file claims, from the file that claims it.
	case resp.StatusCode == http.StatusOK:
		// No Range support, or none needed: start from nothing rather than trust a server
		// that answered 200 to a range request by sending the whole body.
		if off > 0 {
			off = 0
			h = sha256.New()
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		os.Remove(part)
		return fmt.Errorf("models: %s: the saved bytes are past the end of the file", p.Name)
	case resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent:
		return fmt.Errorf("models: fetch %s: HTTP %d", p.Name, resp.StatusCode)
	}

	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("models: open %s: %w", p.Name, err)
	}
	if _, err := out.Seek(off, io.SeekStart); err != nil {
		out.Close()
		return fmt.Errorf("models: seek %s: %w", p.Name, err)
	}

	w := &counter{w: io.MultiWriter(out, h), ctx: ctx, done: done, total: p.Bytes, onAdd: func(n int64) {
		f.setProgress(e.ID, StateDownloading, p.Name, *done)
	}}
	n, err := io.Copy(w, resp.Body)
	cerr := out.Close()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr // the part stays; this was a stop, not a failure
		}
		return fmt.Errorf("models: write %s: %w (%d of %d bytes)", p.Name, err, off+n, p.Bytes)
	}
	if cerr != nil {
		return fmt.Errorf("models: write %s: %w", p.Name, cerr)
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != p.SHA256 {
		// A wrong digest is not retried from the same part file. Whether the byte rotted in
		// transit or upstream moved the file, the bytes on disk are now known to be wrong,
		// and the only honest next attempt starts at zero.
		os.Remove(part)
		*done -= off + n
		return fmt.Errorf("models: %s has the wrong digest (got %s…, wanted %s…)",
			p.Name, got[:12], p.SHA256[:12])
	}

	if err := os.Rename(part, final); err != nil {
		return fmt.Errorf("models: place %s: %w", p.Name, err)
	}
	if err := MarkVerified(final, p.SHA256); err != nil {
		// The file is correct and placed. A missing record means the page says
		// "unverified" about a file it did verify — a cosmetic lie, not a broken model.
		f.progressNote(StateIdle)
	}
	return nil
}

// hashPrefix folds the bytes already on disk into the digest, so a resumed download is
// verified as a whole file rather than as its last chunk.
func hashPrefix(path string, h io.Writer, n int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.CopyN(h, f, n); err != nil {
		return fmt.Errorf("read the saved bytes: %w", err)
	}
	return nil
}

func (f *Fetcher) setProgress(id string, s State, part string, done int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prog.ID != id {
		return
	}
	f.prog.State, f.prog.Part, f.prog.Done = s, part, done
	if s != StateIdle {
		f.prog.Error = ""
	}
}

func (f *Fetcher) progressNote(s State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prog.State = s
}

func (f *Fetcher) url(e Entry, p Part) string {
	if f.base == "" {
		return e.URL(p)
	}
	return fmt.Sprintf("%s/%s/resolve/%s/%s", f.base, e.Repo, e.Rev, p.Name)
}

// counter is the progress source and the cancel point: a stopped download notices within a
// chunk rather than at the end of a 1.4 GB body.
type counter struct {
	w     io.Writer
	ctx   context.Context
	done  *int64
	total int64
	onAdd func(int64)
}

func (c *counter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.w.Write(p)
	if n > 0 {
		*c.done += int64(n)
		if c.onAdd != nil {
			c.onAdd(int64(n))
		}
	}
	return n, err
}
