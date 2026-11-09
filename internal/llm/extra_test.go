package llm_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"libris/internal/llm"
)

// A stream that ends without [DONE] is a normal EOF, not an error.
func TestChatStreamEndsAtEOF(t *testing.T) {
	srv, _ := server(t, 200, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n", nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	var seen strings.Builder
	if err := c.ChatStream(context.Background(), llm.ChatRequest{}, func(ch llm.Chunk) error {
		seen.WriteString(ch.Delta.Content)
		return nil
	}); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if seen.String() != "done" {
		t.Fatalf("seen = %q", seen.String())
	}
}

func TestEmbedExplicitEncodingFormat(t *testing.T) {
	srv, rec := server(t, 200, `{"data":[{"index":0,"embedding":[1]}]}`, nil)
	c := newClient(t, llm.Config{ChatURL: "http://unused", EmbedURL: srv.URL})

	if _, err := c.Embed(context.Background(), llm.EmbedRequest{
		Input:          []llm.EmbedInput{llm.TextInput("a")},
		EncodingFormat: "base64",
	}); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	sent := decode[map[string]any](t, rec.body)
	if sent["encoding_format"] != "base64" {
		t.Fatalf("encoding_format = %v", sent["encoding_format"])
	}
}

// A request that cannot be encoded must fail before hitting the network.
func TestEncodeError(t *testing.T) {
	c := newClient(t, llm.Config{ChatURL: "http://127.0.0.1:1"})
	_, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: make(chan int)}},
	})
	if err == nil || !strings.Contains(err.Error(), "encode") {
		t.Fatalf("err = %v", err)
	}
}

// An error object without a message falls back to the raw body.
func TestErrorWithoutMessage(t *testing.T) {
	srv, _ := server(t, 400, `{"error":{"code":42}}`, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	_, err := c.Chat(context.Background(), llm.ChatRequest{})
	apiErr, ok := llm.AsError(err)
	if !ok || !strings.Contains(apiErr.Message, "code") {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

// A response body that fails mid-read must surface as a stream error.
type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errBroken }
func (brokenBody) Close() error             { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestChatStreamReadError(t *testing.T) {
	c, err := llm.New(llm.Config{
		ChatURL: "http://x",
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: brokenBody{}, Header: http.Header{}}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = c.ChatStream(context.Background(), llm.ChatRequest{}, func(llm.Chunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "llm: stream:") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmbedServerError(t *testing.T) {
	srv, _ := server(t, 500, `{"error":{"message":"embedder down"}}`, nil)
	c := newClient(t, llm.Config{ChatURL: "http://unused", EmbedURL: srv.URL})

	if _, err := c.Embed(context.Background(), llm.EmbedRequest{Input: []llm.EmbedInput{llm.TextInput("a")}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestRerankServerError(t *testing.T) {
	srv, _ := server(t, 500, `{"error":{"message":"reranker down"}}`, nil)
	c := newClient(t, llm.Config{ChatURL: "http://unused", RerankURL: srv.URL})

	if _, err := c.Rerank(context.Background(), llm.RerankRequest{Documents: []string{"a"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestErrorStringCode(t *testing.T) {
	srv, _ := server(t, 400, `{"error":{"message":"m","code":"invalid_request"}}`, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	_, err := c.Chat(context.Background(), llm.ChatRequest{})
	apiErr, ok := llm.AsError(err)
	if !ok || apiErr.Code != "invalid_request" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

// Zero timeout must fall back to the default without failing.
func TestNewDefaults(t *testing.T) {
	if _, err := llm.New(llm.Config{ChatURL: "http://x"}); err != nil {
		t.Fatal(err)
	}
}

var errBroken = errors.New("stream broken")

// The client must never send sampling or inference configuration: llama.cpp
// fixes those on the server command line. This guards the contract.
func TestNoSamplingOrInferenceConfig(t *testing.T) {
	srv, rec := server(t, 200, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	tool, err := llm.NewTool("search", "d", map[string]any{"type": "object"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:      "m",
		Messages:   []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Tools:      []llm.Tool{tool},
		ToolChoice: llm.ToolChoiceRequired,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	sent := decode[map[string]any](t, rec.body)
	forbidden := []string{
		"temperature", "top_p", "top_k", "min_p", "typ_p",
		"max_tokens", "n_predict", "seed", "stop", "repeat_penalty",
		"presence_penalty", "frequency_penalty", "mirostat", "grammar",
		"cache_prompt", "parallel_tool_calls", "n_keep", "ignore_eos",
	}
	for _, key := range forbidden {
		if _, ok := sent[key]; ok {
			t.Errorf("request must not carry %q; sampling and inference config belong to the server", key)
		}
	}

	for _, want := range []string{"messages", "tools", "tool_choice", "model"} {
		if _, ok := sent[want]; !ok {
			t.Errorf("request is missing structural field %q", want)
		}
	}
}
