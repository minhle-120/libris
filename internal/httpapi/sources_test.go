package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"libris/internal/app"
	"libris/internal/config"
	"libris/internal/domain"
)

// fakeAPI records what the handlers ask for and replays canned outcomes. The
// use cases are tested in app; what is tested here is the mapping to HTTP.
type fakeAPI struct {
	notebooks []domain.Notebook
	sources   []domain.Source
	segments  []domain.Segment
	asset     domain.Asset
	err       error

	created string // name passed to CreateNotebook
	upload  string // filename passed to AddUpload
	body    string // contents passed to AddUpload
}

func (f *fakeAPI) CreateNotebook(_ context.Context, name string) (domain.Notebook, error) {
	f.created = name
	if f.err != nil {
		return domain.Notebook{}, f.err
	}
	return domain.Notebook{ID: "nb1", Name: name}, nil
}

func (f *fakeAPI) RenameNotebook(context.Context, string, string) error { return f.err }
func (f *fakeAPI) DeleteNotebook(context.Context, string) error         { return f.err }

func (f *fakeAPI) GetNotebook(_ context.Context, id string) (domain.Notebook, error) {
	if f.err != nil {
		return domain.Notebook{}, f.err
	}
	for _, nb := range f.notebooks {
		if nb.ID == id {
			return nb, nil
		}
	}
	return domain.Notebook{}, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
}

func (f *fakeAPI) ListNotebooks(context.Context) ([]domain.Notebook, error) {
	return f.notebooks, f.err
}

func (f *fakeAPI) AddUpload(_ context.Context, _ string, r io.Reader, name string) (domain.Source, error) {
	f.upload = name
	body, _ := io.ReadAll(r)
	f.body = string(body)
	if f.err != nil {
		return domain.Source{}, f.err
	}
	return domain.Source{ID: "sha1", Name: name, Status: domain.StatusProcessing}, nil
}

func (f *fakeAPI) DetachSource(context.Context, string, string) error { return f.err }

func (f *fakeAPI) GetSource(_ context.Context, id string) (domain.Source, error) {
	if f.err != nil {
		return domain.Source{}, f.err
	}
	for _, s := range f.sources {
		if s.ID == id {
			return s, nil
		}
	}
	return domain.Source{}, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
}

func (f *fakeAPI) ListSources(context.Context, string) ([]domain.Source, error) {
	return f.sources, f.err
}

func (f *fakeAPI) ListSegments(context.Context, string) ([]domain.Segment, error) {
	return f.segments, f.err
}

func (f *fakeAPI) GetAsset(_ context.Context, id string) (domain.Asset, error) {
	if f.err != nil {
		return domain.Asset{}, f.err
	}
	if f.asset.ID != id {
		return domain.Asset{}, fmt.Errorf("%w: %s", domain.ErrNotFound, id)
	}
	return f.asset, nil
}

func newAppServer(t *testing.T, a API) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(Options{
		Settings: settingsStore(t),
		App:      a,
		Logger:   slog.New(slog.NewTextHandler(nopWriter{}, nil)),
	}))
	t.Cleanup(srv.Close)
	return srv
}

func settingsStore(t *testing.T) *config.FileStore {
	t.Helper()
	store, err := config.NewFileStore(filepath.Join(t.TempDir(), "settings.json"), config.Default())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func request(t *testing.T, srv *httptest.Server, method, path string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ── notebooks ────────────────────────────────────────────────────

func TestNotebookEndpoints(t *testing.T) {
	api := &fakeAPI{notebooks: []domain.Notebook{{ID: "nb1", Name: "Quantum"}}}
	srv := newAppServer(t, api)

	resp := request(t, srv, "POST", "/api/notebooks", strings.NewReader(`{"name":"Quantum"}`))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", resp.StatusCode, readAll(t, resp))
	}
	var created domain.Notebook
	if err := json.Unmarshal([]byte(readAll(t, resp)), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "Quantum" || api.created != "Quantum" {
		t.Errorf("created = %+v", created)
	}

	resp = request(t, srv, "GET", "/api/notebooks", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var list []domain.Notebook
	if err := json.Unmarshal([]byte(readAll(t, resp)), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "nb1" {
		t.Errorf("list = %+v", list)
	}

	resp = request(t, srv, "GET", "/api/notebooks/nb1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", resp.StatusCode)
	}

	resp = request(t, srv, "PATCH", "/api/notebooks/nb1", strings.NewReader(`{"name":"Renamed"}`))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("rename status = %d", resp.StatusCode)
	}

	resp = request(t, srv, "DELETE", "/api/notebooks/nb1", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
}

func TestNotebookErrorsMapToStatuses(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		method string
		path   string
		body   io.Reader
		want   int
	}{
		{"unknown id", fmt.Errorf("%w: nb9", domain.ErrNotFound), "GET", "/api/notebooks/nb9", nil, http.StatusNotFound},
		{"blank name", fmt.Errorf("%w: a notebook needs a name", app.ErrInvalid), "POST", "/api/notebooks", strings.NewReader(`{"name":" "}`), http.StatusBadRequest},
		{"not json", nil, "POST", "/api/notebooks", strings.NewReader(`{`), http.StatusBadRequest},
		{"empty body", nil, "POST", "/api/notebooks", nil, http.StatusBadRequest},
		{"store fault", fmt.Errorf("store: busy"), "GET", "/api/notebooks", nil, http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{err: tc.err}
			s := newAppServer(t, api)
			resp := request(t, s, tc.method, tc.path, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("%s %s = %d, want %d: %s",
					tc.method, tc.path, resp.StatusCode, tc.want, readAll(t, resp))
			}
			if tc.want >= 400 {
				var body map[string]string
				if err := json.Unmarshal([]byte(readAll(t, resp)), &body); err != nil || body["error"] == "" {
					t.Errorf("error body = %q, want an error message", readAll(t, resp))
				}
			}
		})
	}
}

// ── upload ───────────────────────────────────────────────────────

// uploadBody builds a multipart body the way a browser's FormData would.
func uploadBody(t *testing.T, field, filename, contents string) (io.Reader, string) {
	t.Helper()
	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	// The writer must be closed before the buffer is read, so build it through a
	// real form file rather than hand-writing boundaries.
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, contents); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(buf.String()), w.FormDataContentType()
}

func postFile(t *testing.T, srv *httptest.Server, path, field, filename, contents string) *http.Response {
	t.Helper()
	body, ctype := uploadBody(t, field, filename, contents)
	req, err := http.NewRequest("POST", srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", ctype)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestUploadAcceptedWhileProcessing(t *testing.T) {
	api := &fakeAPI{}
	srv := newAppServer(t, api)

	resp := postFile(t, srv, "/api/notebooks/nb1/sources", "file", "paper.pdf", "%PDF-1.4\n")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", resp.StatusCode, readAll(t, resp))
	}
	if api.upload != "paper.pdf" || api.body != "%PDF-1.4\n" {
		t.Errorf("app saw name=%q body=%q", api.upload, api.body)
	}

	var src domain.Source
	if err := json.Unmarshal([]byte(readAll(t, resp)), &src); err != nil {
		t.Fatal(err)
	}
	if src.Status != domain.StatusProcessing {
		t.Errorf("status = %q, want the same shape as GET /api/sources", src.Status)
	}
}

func TestUploadRejections(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		field    string
		filename string
		want     int
		extra    func(t *testing.T, resp *http.Response)
	}{
		{
			name:     "no file field",
			field:    "attachment",
			filename: "a.pdf",
			want:     http.StatusBadRequest,
		},
		{
			name:     "too large",
			err:      fmt.Errorf("%w: paper.pdf is limited to 100 bytes", app.ErrTooLarge),
			field:    "file",
			filename: "paper.pdf",
			want:     http.StatusRequestEntityTooLarge,
		},
		{
			name:     "queue full",
			err:      fmt.Errorf("%w: 64 uploads waiting", app.ErrBusy),
			field:    "file",
			filename: "paper.pdf",
			want:     http.StatusTooManyRequests,
			extra: func(t *testing.T, resp *http.Response) {
				if resp.Header.Get("Retry-After") == "" {
					t.Error("a full queue should say when to come back")
				}
			},
		},
		{
			name:     "empty file",
			err:      fmt.Errorf("%w: a.txt is empty", app.ErrInvalid),
			field:    "file",
			filename: "a.txt",
			want:     http.StatusBadRequest,
		},
		{
			name:     "unknown notebook",
			err:      fmt.Errorf("%w: nb9", domain.ErrNotFound),
			field:    "file",
			filename: "a.txt",
			want:     http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{err: tc.err}
			srv := newAppServer(t, api)
			resp := postFile(t, srv, "/api/notebooks/nb1/sources", tc.field, tc.filename, "body")
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.want, readAll(t, resp))
			}
			if tc.extra != nil {
				tc.extra(t, resp)
			}
		})
	}
}

func TestUploadWithoutMultipartIsRejected(t *testing.T) {
	srv := newAppServer(t, &fakeAPI{})
	resp := request(t, srv, "POST", "/api/notebooks/nb1/sources", strings.NewReader("just text"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
}

// ── sources ──────────────────────────────────────────────────────

func TestSourceReadEndpoints(t *testing.T) {
	api := &fakeAPI{
		sources: []domain.Source{{ID: "sha1", Name: "paper.pdf", Status: domain.StatusReady, SegmentCount: 3}},
		segments: []domain.Segment{
			{ID: "sha1:0000", DocID: "sha1", Kind: domain.KindText, Ordinal: 0, Text: "a passage"},
		},
	}
	srv := newAppServer(t, api)

	for _, tc := range []struct {
		method, path string
		want         int
		contains     string
	}{
		{"GET", "/api/sources/sha1", http.StatusOK, `"name":"paper.pdf"`},
		{"GET", "/api/notebooks/nb1/sources", http.StatusOK, `"segment_count":3`},
		{"GET", "/api/sources/sha1/segments", http.StatusOK, "a passage"},
		{"GET", "/api/sources/nope", http.StatusNotFound, "nothing with that id"},
	} {
		resp := request(t, srv, tc.method, tc.path, nil)
		got := readAll(t, resp)
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, resp.StatusCode, tc.want, got)
		}
		if !strings.Contains(got, tc.contains) {
			t.Errorf("%s %s body = %s, want %q", tc.method, tc.path, got, tc.contains)
		}
	}
}

func TestDetachSource(t *testing.T) {
	srv := newAppServer(t, &fakeAPI{})
	resp := request(t, srv, "DELETE", "/api/notebooks/nb1/sources/sha1", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// ── stored files ─────────────────────────────────────────────────

func TestOriginalIsStreamedWithRanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "paper.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4 hello world"), 0o600); err != nil {
		t.Fatal(err)
	}

	api := &fakeAPI{sources: []domain.Source{{
		ID: "sha1", Name: "paper.pdf", OriginalPath: path,
	}}}
	srv := newAppServer(t, api)

	resp := request(t, srv, "GET", "/api/sources/sha1/original", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/pdf" {
		t.Errorf("content type = %q", got)
	}
	if got := resp.Header.Get("Content-Disposition"); got != `inline; filename="paper.pdf"` {
		t.Errorf("disposition = %q", got)
	}
	if got := readAll(t, resp); got != "%PDF-1.4 hello world" {
		t.Errorf("body = %q", got)
	}

	// Range support is what lets a browser PDF viewer seek without downloading.
	req, err := http.NewRequest("GET", srv.URL+"/api/sources/sha1/original", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-7")
	resp2, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	if string(body) != "%PDF-1.4" {
		t.Errorf("range body = %q", body)
	}
}

func TestOriginalMissingFromDisk(t *testing.T) {
	api := &fakeAPI{sources: []domain.Source{{
		ID: "sha1", Name: "gone.pdf", OriginalPath: filepath.Join(t.TempDir(), "gone.pdf"),
	}}}
	srv := newAppServer(t, api)

	resp := request(t, srv, "GET", "/api/sources/sha1/original", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := readAll(t, resp); !strings.Contains(got, "missing from disk") {
		t.Errorf("body = %s, want it to say the stored file is gone", got)
	}
}

func TestAssetRouteSlicesThroughSlashes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page-0001.png")
	if err := os.WriteFile(path, []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}

	api := &fakeAPI{asset: domain.Asset{
		ID: "sha1/pages/page-0001.png", Path: path, MIME: "image/png", Bytes: 3,
	}}
	srv := newAppServer(t, api)

	resp := request(t, srv, "GET", "/api/assets/sha1/pages/page-0001.png", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, readAll(t, resp))
	}
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("content type = %q, want the one the sink recorded", got)
	}

	resp = request(t, srv, "GET", "/api/assets/elsewhere/page-0001.png", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown asset status = %d", resp.StatusCode)
	}
}

func TestServeFileRefusesADirectory(t *testing.T) {
	api := &fakeAPI{sources: []domain.Source{{ID: "sha1", Name: "dir", OriginalPath: t.TempDir()}}}
	srv := newAppServer(t, api)

	resp := request(t, srv, "GET", "/api/sources/sha1/original", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a path that is not a file", resp.StatusCode)
	}
}

// ── wiring ───────────────────────────────────────────────────────

func TestAppEndpointsAbsentWithoutApp(t *testing.T) {
	srv := httptest.NewServer(New(Options{Settings: settingsStore(t)}))
	t.Cleanup(srv.Close)

	for _, path := range []string{"/api/notebooks", "/api/sources/sha1", "/api/assets/a/b"} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 when no app is wired", path, resp.StatusCode)
		}
	}

	// Settings and health stay available: the UI is usable without a database.
	resp, err := srv.Client().Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health = %d", resp.StatusCode)
	}
}
