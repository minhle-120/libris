package llm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"libris/internal/llm"
)

func TestErrorFormatting(t *testing.T) {
	e := &llm.Error{StatusCode: 500, Message: "boom"}
	if got := e.Error(); !strings.Contains(got, "500") || !strings.Contains(got, "boom") {
		t.Fatalf("Error() = %q", got)
	}
	e = &llm.Error{StatusCode: 418}
	if got := e.Error(); !strings.Contains(got, "I'm a teapot") {
		t.Fatalf("Error() with no message = %q", got)
	}
}

func TestErrorNumericCodeAndBodyFallback(t *testing.T) {
	srv, _ := server(t, 422, `{"error":{"message":"bad","code":42}}`, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	_, err := c.Chat(context.Background(), llm.ChatRequest{})
	apiErr, ok := llm.AsError(err)
	if !ok || apiErr.Code != "42" || apiErr.Message != "bad" {
		t.Fatalf("apiErr = %+v", apiErr)
	}

	srv2, _ := server(t, 500, "plain text failure", map[string]string{"Retry-After": "soon"})
	c2 := newClient(t, llm.Config{ChatURL: srv2.URL})
	_, err = c2.Chat(context.Background(), llm.ChatRequest{})
	apiErr, _ = llm.AsError(err)
	if apiErr.Message != "plain text failure" {
		t.Fatalf("message = %q", apiErr.Message)
	}
	if apiErr.RetryAfter != 0 {
		t.Fatalf("retry after = %v, want 0 for unparsable header", apiErr.RetryAfter)
	}
}

func TestIsNotFound(t *testing.T) {
	srv, _ := server(t, 404, `{"error":{"message":"nope"}}`, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	_, err := c.Chat(context.Background(), llm.ChatRequest{})
	if !llm.IsNotFound(err) {
		t.Fatalf("IsNotFound(%v) = false", err)
	}
	if llm.IsBusy(err) || llm.IsAuth(err) {
		t.Fatal("404 must not read as busy or auth")
	}
	if _, ok := llm.AsError(nil); ok {
		t.Fatal("AsError(nil) must be false")
	}
}

func TestTransportErrors(t *testing.T) {
	c := newClient(t, llm.Config{ChatURL: "http://127.0.0.1:1"})

	if _, err := c.Chat(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("expected connection error")
	}
	err := c.ChatStream(context.Background(), llm.ChatRequest{}, func(llm.Chunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "chat/completions") {
		t.Fatalf("stream error = %v", err)
	}
}

func TestBadRequestURL(t *testing.T) {
	c := newClient(t, llm.Config{ChatURL: "://nope"})
	if _, err := c.Chat(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("expected URL error")
	}
}

func TestDrain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", 4096))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	llm.Drain(resp.Body)
}
