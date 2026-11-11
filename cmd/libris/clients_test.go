package main

import (
	"sync"
	"testing"

	"libris/internal/config"
)

func settings(chat, embed, key string, timeout int) config.Config {
	c := config.Default()
	c.ChatURL = chat
	c.EmbedURL = embed
	c.APIKey = key
	c.TimeoutSeconds = timeout
	return c
}

func TestClientCacheReusesUntilTheConnectionChanges(t *testing.T) {
	var c clientCache

	first, err := c.For(settings("http://a:1", "http://e:1", "k", 30))
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.For(settings("http://a:1", "http://e:1", "k", 30))
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Error("identical connection settings built a second client")
	}

	// Each of these decides how libris connects, so each must rebuild.
	for name, next := range map[string]config.Config{
		"chat URL":  settings("http://a:2", "http://e:1", "k", 30),
		"embed URL": settings("http://a:1", "http://e:2", "k", 30),
		"API key":   settings("http://a:1", "http://e:1", "other", 30),
		"timeout":   settings("http://a:1", "http://e:1", "k", 31),
		// llm.New insists on a chat URL and tolerates a blank embed or rerank one,
		// which stays a per-call ErrNoEmbedder rather than a startup failure.
		"blank chat": settings("", "http://e:1", "k", 30),
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

	before := settings("http://a:1", "http://e:1", "k", 30)
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

	if _, err := c.For(settings("", "http://e:1", "", 30)); err == nil {
		t.Fatal("a missing chat URL should fail")
	}
	if _, err := c.For(settings("http://a:1", "http://e:1", "", 30)); err != nil {
		t.Fatalf("the good settings after a failure were refused: %v", err)
	}
}

func TestClientCacheIsSafeForConcurrentJobs(t *testing.T) {
	var c clientCache
	base := settings("http://a:1", "http://e:1", "k", 30)

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
