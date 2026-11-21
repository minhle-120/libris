package httpapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"libris/internal/config"
)

func newServer(t *testing.T, assets fs.FS) (*Server, *config.FileStore) {
	t.Helper()
	path := t.TempDir() + "/settings.json"
	store, err := config.NewFileStore(path, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Settings: store,
		Assets:   assets,
		Logger:   slog.New(slog.NewTextHandler(nopWriter{}, nil)),
	}), store
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func do(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func decodeSettings(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return body
}

func TestHealth(t *testing.T) {
	srv, _ := newServer(t, nil)
	w := do(t, srv, http.MethodGet, "/api/health", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content type = %q", ct)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff header = %q", got)
	}
}

func TestGetSettings(t *testing.T) {
	srv, store := newServer(t, nil)

	w := do(t, srv, http.MethodGet, "/api/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	body := decodeSettings(t, w)
	// Counted off the config rather than written out, so retiring a setting cannot
	// leave this test asserting a number that no longer exists anywhere.
	want := countSettings(t)
	if len(body) != want {
		t.Fatalf("got %d settings, want the whole config (%d)", len(body), want)
	}
	if body["pdfDPI"] != float64(store.Get().PDFDPI) {
		t.Fatalf("pdfDPI = %v", body["pdfDPI"])
	}
}

func countSettings(t *testing.T) int {
	t.Helper()
	raw, err := json.Marshal(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	return len(all)
}

func TestPutSettingsPartial(t *testing.T) {
	srv, store := newServer(t, nil)

	w := do(t, srv, http.MethodPut, "/api/settings", `{"pdfDPI": 220}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}

	body := decodeSettings(t, w)
	if body["pdfDPI"] != float64(220) {
		t.Fatalf("response pdfDPI = %v", body["pdfDPI"])
	}
	if store.Get().PDFDPI != 220 {
		t.Fatalf("store = %d", store.Get().PDFDPI)
	}
	// Partial update must not reset unrelated values.
	if body["chunkWords"] != float64(store.Get().ChunkWords) {
		t.Fatalf("chunkWords lost: %v", body["chunkWords"])
	}
}

func TestPutSettingsRejects(t *testing.T) {
	cases := map[string]struct {
		body   string
		status int
		hint   string
	}{
		"unknown key":  {`{"nope": 1}`, http.StatusBadRequest, "unknown setting"},
		"out of range": {`{"pdfDPI": 9000}`, http.StatusBadRequest, "pdfDPI"},
		"bad type":     {`{"pdfDPI": "big"}`, http.StatusBadRequest, "pdfDPI"},
		"bad json":     {`{`, http.StatusBadRequest, "JSON"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, store := newServer(t, nil)
			w := do(t, srv, http.MethodPut, "/api/settings", tc.body)

			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.status, w.Body)
			}
			if !strings.Contains(w.Body.String(), tc.hint) {
				t.Fatalf("body %q does not mention %q", w.Body.String(), tc.hint)
			}
			if store.Get() != config.Default() {
				t.Fatal("store changed after a rejected update")
			}
		})
	}
}

func TestSettingsNotConfigured(t *testing.T) {
	srv := New(Options{})

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		w := do(t, srv, method, "/api/settings", "")
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d", method, w.Code)
		}
	}
}

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":     {Data: []byte("<!doctype html><title>app</title>")},
		"assets/app.js":  {Data: []byte("console.log(1)")},
		"assets/app.css": {Data: []byte("body{}")},
	}
}

func TestStaticServesIndex(t *testing.T) {
	srv, _ := newServer(t, testAssets())

	w := do(t, srv, http.MethodGet, "/", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<title>app</title>") {
		t.Fatalf("status = %d body = %q", w.Code, w.Body.String())
	}
}

func TestStaticServesAssets(t *testing.T) {
	srv, _ := newServer(t, testAssets())

	w := do(t, srv, http.MethodGet, "/assets/app.js", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "console.log") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestStaticFallsBackToIndexForUnknownPath(t *testing.T) {
	srv, _ := newServer(t, testAssets())

	for _, path := range []string{"/notebooks/abc", "/settings", "/deep/link/here"} {
		w := do(t, srv, http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Errorf("%s status = %d", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "<title>app</title>") {
			t.Errorf("%s did not fall back to index", path)
		}
	}
}

func TestStaticWithoutAssets(t *testing.T) {
	srv, _ := newServer(t, nil)

	w := do(t, srv, http.MethodGet, "/", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no frontend") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv, _ := newServer(t, nil)

	w := do(t, srv, http.MethodPost, "/api/settings", `{}`)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestPutSettingsUnreadableBody(t *testing.T) {
	srv, _ := newServer(t, nil)

	r := httptest.NewRequest(http.MethodPut, "/api/settings", failingBody{})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "could not read body") {
		t.Fatalf("body = %q", w.Body.String())
	}
}
