package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"libris/internal/llm"
)

func routerClient(t *testing.T, url string) *llm.Client {
	t.Helper()
	c, err := llm.New(llm.Config{ChatURL: url, EmbedURL: url})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The list is what makes the unload call worth making: an instance that is not resident is not
// stopped, and the router answers that as an error rather than as a job already done.
func TestRouterModelsReportsWhatIsResident(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("asked for %s, want the router's own route at the root", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{
				map[string]any{"id": "libris-embed", "status": map[string]any{"value": "loaded"}},
				map[string]any{"id": "libris-embed-cpu", "status": map[string]any{"value": "unloaded"}},
				map[string]any{"id": "libris-chat", "status": map[string]any{"value": "sleeping"}},
				map[string]any{"id": "something-new", "status": map[string]any{"value": "wobbling"}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	got, err := routerClient(t, srv.URL).RouterModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("%d models: %+v", len(got), got)
	}

	byName := map[string]llm.RouterModel{}
	for _, m := range got {
		byName[m.Name] = m
	}
	if !byName["libris-embed"].Running() {
		t.Error("a loaded instance reads as not running")
	}
	if byName["libris-embed-cpu"].Running() {
		t.Error("an unloaded instance reads as running, so libris would ask for it to be stopped")
	}
	// Asleep is resident: the process is up and the memory is held. Stopping it is the point.
	if !byName["libris-chat"].Running() {
		t.Error("a sleeping instance reads as not running")
	}
	// A state this build has never seen stays unknown rather than becoming a guess, in either
	// direction: claiming it is running would stop something that is not, and claiming it is not
	// would leave bytes held.
	if byName["something-new"].Status != "wobbling" || byName["something-new"].Running() {
		t.Errorf("an unseen status became %+v", byName["something-new"])
	}
}

// The request names the instance in the body the router reads ("model", not "name"), and a
// refusal arrives with the server's own sentence — which is the only explanation libris will ever
// get for a model that stayed resident.
func TestUnloadModelNamesTheInstanceAndKeepsTheRefusal(t *testing.T) {
	var path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"model is not running","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(srv.Close)

	err := routerClient(t, srv.URL).UnloadModel(context.Background(), "libris-embed")
	if path != "/models/unload" {
		t.Errorf("asked %s, want the router's own route at the root", path)
	}
	if !strings.Contains(body, `"model":"libris-embed"`) {
		t.Errorf("the request was %s, want it to name the instance under the key the router reads", body)
	}
	if err == nil {
		t.Fatal("a refusal came back as success")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("the server's reason is missing from %q", err)
	}

	// And the call that succeeds says nothing, which is what the caller wanted.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Write([]byte(`{"success":true}`))
	}))
	t.Cleanup(ok.Close)
	if err := routerClient(t, ok.URL).UnloadModel(context.Background(), "libris-embed"); err != nil {
		t.Errorf("a stopped model reported %v", err)
	}
	if err := routerClient(t, ok.URL).UnloadModel(context.Background(), "  "); err == nil {
		t.Error("unloading nothing was accepted")
	}
}
