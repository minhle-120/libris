package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// The router's own control surface.
//
// Everything else in this package asks a model for an answer. These two ask about the process:
// which instances exist and are resident, and stop one of them. They are not OpenAI-shaped, they
// are not under /v1 (server.cpp registers GET /models and POST /models/unload at the root), and
// only one caller uses them — the program that started the router in the first place. Pointed at
// somebody else's server, libris does not call them at all: stopping a model a person started by
// hand is not a thing an app should do to a server it does not run.

// RouterModel is one instance the router knows about. Status is llama.cpp's own word for what it
// is doing — "loaded", "unloaded", "loading", "sleeping", "failed", "downloading" — carried
// through untranslated, because an instance in a state this build has never seen is a state
// worth showing rather than one worth guessing at.
type RouterModel struct {
	Name   string
	Status string
}

// Running means the instance has bytes held somewhere: loaded, coming up, or asleep between
// requests. Those are the ones worth stopping. A name with no instance behind it is not running,
// and an unload for it is a call the router answers with an error — so the caller asks first and
// never has to read that error to find out it had nothing to do.
func (m RouterModel) Running() bool {
	switch m.Status {
	case "loaded", "loading", "sleeping":
		return true
	}
	return false
}

// RouterModels lists what the router serves. The address is the ChatURL's root because that is
// the field libris treats as the router: in managed mode both models are held by one process, so
// either name reaches the same control.
func (c *Client) RouterModels(ctx context.Context) ([]RouterModel, error) {
	base := root(c.cfg.ChatURL)
	if base == "" {
		return nil, errors.New("llm: no server address to ask about models")
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("llm: models: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return nil, readError(resp)
	}

	// The router's own key is "data", and the OAI-compatible shape uses the same one, so this
	// reads the payload the server actually writes rather than the one the compatibility route
	// would. Unknown keys are ignored on purpose: this call exists to ask one question about one
	// name, and a router that adds fields is not a reason to stop asking it.
	var raw struct {
		Data []struct {
			ID     string `json:"id"`
			Status struct {
				Value string `json:"value"`
			} `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("llm: decode models: %w", err)
	}

	out := make([]RouterModel, 0, len(raw.Data))
	for _, m := range raw.Data {
		out = append(out, RouterModel{Name: m.ID, Status: m.Status.Value})
	}
	return out, nil
}

// UnloadModel stops one model instance and gives back whatever it was holding. The router drains
// it rather than killing it: a request in flight finishes first (server-models.cpp, request_stop
// with graceful set), so this cannot truncate somebody's answer.
//
// It is not idempotent at the wire — asking for a model that is not loaded is an error there —
// which is why callers look first with RouterModels instead of reading this error as a shrug.
func (c *Client) UnloadModel(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("llm: unload: no model named")
	}
	base := root(c.cfg.ChatURL)
	if base == "" {
		return errors.New("llm: no server address to unload from")
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	body, err := json.Marshal(map[string]string{"model": name})
	if err != nil {
		return fmt.Errorf("llm: encode unload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/models/unload", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("llm: unload %s: %w", name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("llm: unload %s: %w", name, err)
	}
	defer resp.Body.Close()

	// The body is read before it is drained, both ways: the sentence explaining a refusal is the
	// whole value of this call's error, and it is the first thing to go when a body is discarded.
	if resp.StatusCode/100 != 2 {
		return readError(resp)
	}
	Drain(resp.Body)
	return nil
}
