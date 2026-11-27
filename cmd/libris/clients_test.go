package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"libris/internal/config"
	"libris/internal/engine"
)

func settings(chat, embed string, timeout int) config.Config {
	c := config.Default()
	// These tests are about the addresses a person typed. In managed mode there are none to
	// type — clients.For derives them from the port — so the mode is pinned here
	// rather than left to a default that would test the other thing.
	c.EngineMode = config.EngineExternal
	c.ChatURL = chat
	c.EmbedURL = embed
	c.TimeoutSeconds = timeout
	return c
}

func TestClientCacheReusesUntilTheConnectionChanges(t *testing.T) {
	var c clientCache

	first, err := c.For(settings("http://a:1", "http://e:1", 30))
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.For(settings("http://a:1", "http://e:1", 30))
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Error("identical connection settings built a second client")
	}

	// Each of these decides how libris connects, so each must rebuild.
	for name, next := range map[string]config.Config{
		"chat URL":  settings("http://a:2", "http://e:1", 30),
		"embed URL": settings("http://a:1", "http://e:2", 30),
		"timeout":   settings("http://a:1", "http://e:1", 31),
		// llm.New insists on a chat URL and tolerates a blank embed one,
		// which stays a per-call ErrNoEmbedder rather than a startup failure.
		"blank chat": settings("", "http://e:1", 30),
	} {
		got, err := c.For(next)
		if name == "blank chat" {
			if err == nil {
				t.Errorf("%s: a blank chat URL should not produce a client", name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got == first {
			t.Errorf("changing the %s reused the old client", name)
		}
		first = got
	}
}

// A model name is not a connection setting: it travels per request, so switching
// models must not throw away the transport.
func TestClientCacheIgnoresModelNames(t *testing.T) {
	var c clientCache

	before := settings("http://a:1", "http://e:1", 30)
	first, err := c.For(before)
	if err != nil {
		t.Fatal(err)
	}
	after := before
	after.EmbedModel = "something-else"
	after.ChatModel = "another-model"

	got, err := c.For(after)
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Error("changing a model rebuilt the client; the models are not part of the connection")
	}
}

// A rejected settings document must not be remembered, or the next correct save
// would keep using the client that was never built.
func TestClientCacheDoesNotRememberAFailure(t *testing.T) {
	var c clientCache

	if _, err := c.For(settings("", "http://e:1", 30)); err == nil {
		t.Fatal("a missing chat URL should fail")
	}
	if _, err := c.For(settings("http://a:1", "http://e:1", 30)); err != nil {
		t.Fatalf("the good settings after a failure were refused: %v", err)
	}
}

func TestClientCacheIsSafeForConcurrentJobs(t *testing.T) {
	var c clientCache
	base := settings("http://a:1", "http://e:1", 30)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := base
			if i%2 == 0 {
				next.EmbedURL = "http://e:2"
			}
			if _, err := c.For(next); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

// fixedConfig is the settings page as this file sees it: the values in force, on demand.
type fixedConfig config.Config

func (f fixedConfig) Get() config.Config { return config.Config(f) }

// Both halves of the memory arrangement ask the same question first, because the wire refuses
// "stop the thing that is already stopped" with an error: a job that opens by making room would
// otherwise log a refusal about a model that was never loaded.
func TestRoomForIngestUnloadsTheChatModelAndOnlyIt(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[`+
				`{"id":"libris-chat","status":{"value":"loaded"}},`+
				`{"id":"libris-embed","status":{"value":"loading"}}]}`)
		case "/models/unload":
			io.WriteString(w, `{"success":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer router.Close()

	port := router.Listener.Addr().(*net.TCPAddr).Port
	clients := &clientCache{server: fakeRouter{engine.Serving{Running: true, Port: port}}}

	if err := ingestRoomFor(clients, fixedConfig(config.Default()))(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := `POST /models/unload {"model":"libris-chat"}`
	if len(seen) != 2 || seen[0] != "GET /models " || seen[1] != want {
		t.Errorf("the router saw %v, want a list and then %q", seen, want)
	}
}

// A chat model that is not resident is the normal state, not a problem: the router loads it on the
// request that names it, so most uploads ask a question and are told nothing to do.
func TestRoomForIngestSaysNothingAboutAModelThatIsNotUp(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"libris-chat","status":{"value":"unloaded"}}]}`)
	}))
	defer router.Close()

	port := router.Listener.Addr().(*net.TCPAddr).Port
	clients := &clientCache{server: fakeRouter{engine.Serving{Running: true, Port: port}}}

	if err := ingestRoomFor(clients, fixedConfig(config.Default()))(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "GET /models" {
		t.Errorf("the router saw %v, want the list alone", seen)
	}
}

// An address a person typed is somebody else's process. Making their answering model get out of
// the way is not a thing this app does to a server it does not run.
func TestRoomForIngestDoesNotTouchAServerLibrisDoesNotRun(t *testing.T) {
	var mu sync.Mutex
	var hits int
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer router.Close()

	cfg := settings(router.URL, router.URL, 5)
	clients := &clientCache{}

	if err := ingestRoomFor(clients, fixedConfig(cfg))(context.Background()); err != nil {
		t.Fatalf("external mode should answer that there is nobody to ask, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("%d requests to a server libris does not run", hits)
	}
}
