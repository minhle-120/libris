package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ErrNoEmbedder means the endpoint was not configured. A search treats it as a
// notebook that cannot be searched at all, which is a fact the user can fix in
// Settings — not as a question that failed.
var ErrNoEmbedder = errors.New("llm: embedder not configured")

// Error is a non-2xx response.
type Error struct {
	StatusCode int
	Type       string
	Code       string
	Message    string
	RetryAfter time.Duration
	Body       string
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("llm: %d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), msg)
}

func readError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	e := &Error{StatusCode: resp.StatusCode, Body: string(body)}

	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error.Message != "" {
		e.Message = payload.Error.Message
		e.Type = payload.Error.Type
		switch code := payload.Error.Code.(type) {
		case string:
			e.Code = code
		case float64:
			e.Code = strconv.FormatFloat(code, 'f', -1, 64)
		}
	} else {
		e.Message = firstLine(string(body))
	}

	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return e
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			s = s[:i]
			break
		}
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// AsError unwraps an API error, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// IsBusy reports that every inference slot is occupied. llama.cpp answers 503
// rather than queueing, so callers are expected to retry or queue themselves.
func IsBusy(err error) bool {
	e, ok := AsError(err)
	return ok && (e.StatusCode == http.StatusServiceUnavailable || e.StatusCode == http.StatusTooManyRequests)
}

func IsAuth(err error) bool {
	e, ok := AsError(err)
	return ok && (e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden)
}

func IsNotFound(err error) bool {
	e, ok := AsError(err)
	return ok && e.StatusCode == http.StatusNotFound
}
