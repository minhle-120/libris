package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout applies to non-streaming calls only. Streams are bounded by
// the caller's context, because a whole-response timeout would cut them off.
const DefaultTimeout = 5 * time.Minute

type Config struct {
	ChatURL   string
	EmbedURL  string
	RerankURL string
	APIKey    string
	Timeout   time.Duration
	HTTP      *http.Client
	UserAgent string
}

type Client struct {
	cfg Config
	hc  *http.Client
}

func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.ChatURL) == "" {
		return nil, errors.New("llm: chat URL is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	// No Client.Timeout: it would also cap streamed responses.
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "libris"
	}
	return &Client{cfg: cfg, hc: cfg.HTTP}, nil
}

// Chat performs a non-streaming completion.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	req.Stream = false
	var out ChatResponse
	if err := c.doJSON(ctx, c.cfg.ChatURL, "chat/completions", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatStream streams deltas until the server sends [DONE] or the context ends.
// A non-nil error from yield stops the stream and is returned unchanged.
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, yield func(Chunk) error) error {
	req.Stream = true
	resp, err := c.post(ctx, c.cfg.ChatURL, "chat/completions", req, "text/event-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return readError(resp)
	}

	// llama.cpp sends `data: [DONE]` for chat completions, but not for its
	// /v1/responses or /v1/messages endpoints. Treat a clean EOF as success so
	// either shape works. The terminating chunk before the sentinel already
	// carries finish_reason (stop | tool_calls | length) with an empty delta.
	dec := newSSE(resp.Body)
	for {
		payload, ok, err := dec.Next()
		if err != nil {
			return fmt.Errorf("llm: stream: %w", err)
		}
		if !ok {
			return nil
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			return nil
		}

		var chunk chatChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return fmt.Errorf("llm: decode stream chunk: %w", err)
		}
		for _, ch := range chunk.Choices {
			if err := yield(Chunk{Delta: ch.Delta, FinishReason: ch.FinishReason, Usage: chunk.Usage}); err != nil {
				return err
			}
		}
	}
}

// Embed returns one vector per input, in input order.
func (c *Client) Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	if len(req.Input) == 0 {
		return nil, errors.New("llm: embed: no input")
	}
	if strings.TrimSpace(c.cfg.EmbedURL) == "" {
		return nil, ErrNoEmbedder
	}
	if req.EncodingFormat == "" {
		req.EncodingFormat = "float"
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	var out EmbedResponse
	if err := c.doJSON(ctx, c.cfg.EmbedURL, "embeddings", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Rerank scores each document against the query. Servers disagree on the
// response key, so both shapes are accepted.
func (c *Client) Rerank(ctx context.Context, req RerankRequest) (*RerankResponse, error) {
	if strings.TrimSpace(c.cfg.RerankURL) == "" {
		return nil, ErrNoReranker
	}
	if len(req.Documents) == 0 {
		return nil, errors.New("llm: rerank: no documents")
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	var out RerankResponse
	if err := c.doJSON(ctx, c.cfg.RerankURL, "rerank", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── transport ─────────────────────────────────────────────────────

func (c *Client) doJSON(ctx context.Context, base, suffix string, in, out any) error {
	resp, err := c.post(ctx, base, suffix, in, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return readError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("llm: decode %s: %w", suffix, err)
	}
	return nil
}

func (c *Client) post(ctx context.Context, base, suffix string, body any, accept string) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("llm: encode %s: %w", suffix, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(base, suffix), bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("llm: %s: %w", suffix, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: %s: %w", suffix, err)
	}
	return resp, nil
}

// endpoint appends the OpenAI path to a base URL, tolerating a base that
// already ends in /v1.
func endpoint(base, suffix string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/" + suffix
	}
	return base + "/v1/" + suffix
}

// Drain is a helper for callers that want the body consumed before reuse.
func Drain(r io.Reader) { io.Copy(io.Discard, io.LimitReader(r, 1<<20)) }
