package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"libris/internal/llm"
)

type recorded struct {
	path   string
	query  string
	auth   string
	accept string
	body   []byte
}

// server starts a test server that records the last request and replies with
// the given status and body.
func server(t *testing.T, status int, body string, headers map[string]string) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path
		rec.query = r.URL.RawQuery
		rec.auth = r.Header.Get("Authorization")
		rec.accept = r.Header.Get("Accept")
		rec.body, _ = io.ReadAll(r.Body)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func newClient(t *testing.T, cfg llm.Config) *llm.Client {
	t.Helper()
	cfg.Timeout = 5 * time.Second
	c, err := llm.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode request: %v\nbody: %s", err, raw)
	}
	return v
}

// ── New / endpoint handling ──────────────────────────────────────

func TestNewRequiresChatURL(t *testing.T) {
	if _, err := llm.New(llm.Config{}); err == nil {
		t.Fatal("expected error for missing chat URL")
	}
}

func TestEndpointToleratesV1Suffix(t *testing.T) {
	for _, base := range []string{"http://x:8081", "http://x:8081/", "http://x:8081/v1", "http://x:8081/v1/"} {
		srv, rec := server(t, 200, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`, nil)
		_ = base
		c := newClient(t, llm.Config{ChatURL: srv.URL + strings.TrimPrefix(base, "http://x:8081")})
		if _, err := c.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}}); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if rec.path != "/v1/chat/completions" {
			t.Fatalf("base %q → path %q, want /v1/chat/completions", base, rec.path)
		}
	}
}

// ── chat ─────────────────────────────────────────────────────────

func TestChatRequestAndResponse(t *testing.T) {
	srv, rec := server(t, 200, `{
		"id":"c1","model":"m","created":1,
		"choices":[{"index":0,"message":{"role":"assistant","content":"hello","reasoning_content":"hm"},
		            "finish_reason":"stop"}],
		"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
	}`, nil)

	c := newClient(t, llm.Config{ChatURL: srv.URL})
	tool, err := llm.NewTool("search", "find things", map[string]any{"type": "object"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages:   []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Tools:      []llm.Tool{tool},
		ToolChoice: llm.ToolChoiceRequired,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if rec.path != "/v1/chat/completions" {
		t.Fatalf("path = %q", rec.path)
	}
	// The servers are loopback ones, and libris has no credential to send: an Authorization
	// header appearing here would mean something grew one without a setting behind it.
	if rec.auth != "" {
		t.Fatalf("sent an Authorization header: %q", rec.auth)
	}

	sent := decode[map[string]any](t, rec.body)
	if sent["stream"] != nil {
		t.Fatalf("non-streaming chat must not send stream, got %v", sent["stream"])
	}
	if sent["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %v", sent["tool_choice"])
	}

	if got := res.Choices[0].Message.Content; got != "hello" {
		t.Fatalf("content = %v", got)
	}
	if got := res.Choices[0].Message.ReasoningContent; got != "hm" {
		t.Fatalf("reasoning = %q", got)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", res.Usage)
	}
}

func TestChatToolCalls(t *testing.T) {
	srv, _ := server(t, 200, `{"choices":[{"message":{"role":"assistant","tool_calls":[
		{"id":"call_1","type":"function","function":{"name":"query_knowledge_base","arguments":"{\"query\":\"x\"}"}}]}}]}`, nil)

	c := newClient(t, llm.Config{ChatURL: srv.URL})
	res, err := c.Chat(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	calls := res.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].Function.Name != "query_knowledge_base" {
		t.Fatalf("tool calls = %+v", calls)
	}
	// Arguments must stay a raw string: parsing is the caller's problem.
	if calls[0].Function.Arguments != `{"query":"x"}` {
		t.Fatalf("arguments = %q", calls[0].Function.Arguments)
	}
}

func TestChatErrorBusy(t *testing.T) {
	srv, _ := server(t, 503, `{"error":{"message":"no slot available","type":"unavailable"}}`,
		map[string]string{"Retry-After": "3"})

	c := newClient(t, llm.Config{ChatURL: srv.URL})
	_, err := c.Chat(context.Background(), llm.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !llm.IsBusy(err) {
		t.Fatalf("IsBusy(%v) = false", err)
	}
	apiErr, ok := llm.AsError(err)
	if !ok {
		t.Fatalf("not an API error: %v", err)
	}
	if apiErr.StatusCode != 503 || apiErr.Message != "no slot available" {
		t.Fatalf("error = %+v", apiErr)
	}
	if apiErr.RetryAfter != 3*time.Second {
		t.Fatalf("retry after = %v", apiErr.RetryAfter)
	}
}

func TestChatErrorAuthAndTextFallback(t *testing.T) {
	srv, _ := server(t, 401, "unauthorized", nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	_, err := c.Chat(context.Background(), llm.ChatRequest{})
	if !llm.IsAuth(err) {
		t.Fatalf("IsAuth(%v) = false", err)
	}
	apiErr, _ := llm.AsError(err)
	if apiErr.Message != "unauthorized" {
		t.Fatalf("message = %q", apiErr.Message)
	}
}

func TestChatMalformedResponse(t *testing.T) {
	srv, _ := server(t, 200, `not json`, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	if _, err := c.Chat(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestChatContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	c := newClient(t, llm.Config{ChatURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := c.Chat(ctx, llm.ChatRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("error = %v", err)
	}
}

// ── streaming ────────────────────────────────────────────────────

func TestChatStream(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		``,
		`: keep-alive comment`,
		`event: ping`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
		`data: {"choices":[{"delta":{"content":"ignored"}}]}`,
	}, "\n")

	srv, rec := server(t, 200, body, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	var text, reasoning, finish strings.Builder
	err := c.ChatStream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, func(ch llm.Chunk) error {
		text.WriteString(ch.Delta.Content)
		reasoning.WriteString(ch.Delta.ReasoningContent)
		if ch.FinishReason != "" {
			finish.WriteString(ch.FinishReason)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	if rec.accept != "text/event-stream" {
		t.Fatalf("accept = %q", rec.accept)
	}
	sent := decode[map[string]any](t, rec.body)
	if sent["stream"] != true {
		t.Fatalf("stream = %v", sent["stream"])
	}
	if text.String() != "Hello" {
		t.Fatalf("text = %q (chunks after [DONE] must be ignored)", text.String())
	}
	if reasoning.String() != "think" {
		t.Fatalf("reasoning = %q", reasoning.String())
	}
	if finish.String() != "stop" {
		t.Fatalf("finish = %q", finish.String())
	}
}

func TestChatStreamYieldStops(t *testing.T) {
	srv, _ := server(t, 200, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n", nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	stop := errors.New("stop")
	var seen []string
	err := c.ChatStream(context.Background(), llm.ChatRequest{}, func(ch llm.Chunk) error {
		seen = append(seen, ch.Delta.Content)
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the yield error", err)
	}
	if len(seen) != 1 {
		t.Fatalf("seen = %v, want one chunk", seen)
	}
}

func TestChatStreamHTTPError(t *testing.T) {
	srv, _ := server(t, 503, `{"error":{"message":"busy"}}`, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	err := c.ChatStream(context.Background(), llm.ChatRequest{}, func(llm.Chunk) error { return nil })
	if !llm.IsBusy(err) {
		t.Fatalf("IsBusy(%v) = false", err)
	}
}

func TestChatStreamBadChunk(t *testing.T) {
	srv, _ := server(t, 200, "data: {broken\n", nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	err := c.ChatStream(context.Background(), llm.ChatRequest{}, func(llm.Chunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "decode stream chunk") {
		t.Fatalf("err = %v", err)
	}
}

// chat_template_kwargs is the one part of the request that is not a top-level key: llama.cpp
// merges the object into the chat template and throws on a kwarg of the wrong JSON type, so a
// bool has to arrive as a bool. Silence has to stay silence — an absent object is what leaves
// the server's own default in charge.
func TestChatTemplateKwargsOnTheWire(t *testing.T) {
	srv, rec := server(t, 200, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`, nil)
	c := newClient(t, llm.Config{ChatURL: srv.URL})

	_, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages:           []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		ChatTemplateKwargs: &llm.ChatTemplateKwargs{EnableThinking: false},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	kw, ok := decode[map[string]any](t, rec.body)["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatalf("no chat_template_kwargs object on the wire: %s", rec.body)
	}
	if got, ok := kw["enable_thinking"].(bool); !ok || got {
		t.Errorf("enable_thinking = %#v, want the JSON boolean false", kw["enable_thinking"])
	}

	if _, err := c.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, ok := decode[map[string]any](t, rec.body)["chat_template_kwargs"]; ok {
		t.Error("a request with no opinion about thinking still sent chat_template_kwargs")
	}
}

// ── embeddings ───────────────────────────────────────────────────

func TestEmbedTextInputs(t *testing.T) {
	srv, rec := server(t, 200, `{"model":"e","data":[
		{"index":0,"embedding":[0.1,0.2,0.3]},
		{"index":1,"embedding":[1,2,3]}]}`, nil)

	c := newClient(t, llm.Config{ChatURL: "http://unused", EmbedURL: srv.URL})
	res, err := c.Embed(context.Background(), llm.EmbedRequest{
		Input: []llm.EmbedInput{llm.TextInput("a"), llm.TextInput("b")},
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if rec.path != "/v1/embeddings" {
		t.Fatalf("path = %q", rec.path)
	}
	sent := decode[map[string]any](t, rec.body)
	if sent["encoding_format"] != "float" {
		t.Fatalf("encoding_format default = %v", sent["encoding_format"])
	}
	inputs, _ := sent["input"].([]any)
	if len(inputs) != 2 || inputs[0] != "a" {
		t.Fatalf("input = %v", sent["input"])
	}
	if len(res.Data) != 2 || res.Data[1].Embedding[2] != 3 {
		t.Fatalf("embeddings = %+v", res.Data)
	}
}

func TestEmbedMultimodalInputs(t *testing.T) {
	srv, rec := server(t, 200, `{"data":[{"index":0,"embedding":[0.5]}]}`, nil)
	c := newClient(t, llm.Config{ChatURL: "http://unused", EmbedURL: srv.URL})

	inlined, err := llm.MediaPart(llm.MediaImage, "image/png", "cG5n")
	if err != nil {
		t.Fatalf("MediaPart: %v", err)
	}

	_, err = c.Embed(context.Background(), llm.EmbedRequest{
		Input: []llm.EmbedInput{
			llm.PartsInput(llm.ImagePart("/tmp/page-1.png")),
			llm.PartsInput(inlined),
		},
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	sent := decode[map[string]any](t, rec.body)
	inputs, _ := sent["input"].([]any)
	if len(inputs) != 2 {
		t.Fatalf("input = %v", sent["input"])
	}
	first, _ := inputs[0].(map[string]any)
	parts, _ := first["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("parts = %v", first)
	}
	part, _ := parts[0].(map[string]any)
	if part["type"] != "image_url" {
		t.Fatalf("part = %v", part)
	}
	img, _ := part["image_url"].(map[string]any)
	if img["url"] != "/tmp/page-1.png" {
		t.Fatalf("image url = %v", img["url"])
	}

	second, _ := inputs[1].(map[string]any)
	parts2, _ := second["content"].([]any)
	inner, _ := parts2[0].(map[string]any)
	if url, _ := inner["image_url"].(map[string]any); url["url"] != "data:image/png;base64,cG5n" {
		t.Fatalf("inlined image = %v", url)
	}
}

func TestEmbedNoInput(t *testing.T) {
	c := newClient(t, llm.Config{ChatURL: "http://unused", EmbedURL: "http://unused"})
	if _, err := c.Embed(context.Background(), llm.EmbedRequest{}); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestEmbedNotConfigured(t *testing.T) {
	c := newClient(t, llm.Config{ChatURL: "http://unused"})
	_, err := c.Embed(context.Background(), llm.EmbedRequest{Input: []llm.EmbedInput{llm.TextInput("a")}})
	if !errors.Is(err, llm.ErrNoEmbedder) {
		t.Fatalf("err = %v", err)
	}
}
