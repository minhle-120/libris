package llm

// What a serving model can be *shown*, as opposed to what it says.
//
// The distinction is the reason this file exists. Media can be inlined into a
// request — a page raster, and later a clip of audio or video — and a model that was
// started without the projector for it does not ignore the part quietly: the server
// throws on it and the whole request dies. llama.cpp's own instruction is to check
// the capability first (tools/server/README.md, on multimodal_data: "Clients should
// check /models or /v1/models for the multimodal capability before a multimodal
// request"), so the check happens here, in the package that owns the wire, and the
// answer is a type the caller can switch on rather than a sentence to substring.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Caps is a serving model's own account of the media it takes, read from its
// /props.
//
// A nil *Caps means nobody asked, and that is a value rather than an oversight: the
// alternative is guessing, and a guess here either withholds a page the model could
// have read or sends one it throws on. So retrieve sends media when Caps is nil and
// lets a server that cannot take it answer with its own error — the honest outcome
// for a server libris does not manage, where /props may not exist at all.
//
// Audio is a modality libris sends and asks about: a clip reaches the answering model
// only when the server says its own projector decodes sound. Video is the engine's
// account of itself and nothing else — there is still no wire shape for it.
type Caps struct {
	// Model is the alias the server reports itself under, so an error about a
	// missing modality can name the thing to go and change.
	Model  string
	Vision bool
	Audio  bool
	Video  bool
}

// Allows reports whether this model can be shown one modality at all.
//
// Unknown kinds are refused rather than waved through: a kind with no case here is
// one this build has no wire shape for either, and llm.MediaPart says so.
func (c *Caps) Allows(kind MediaKind) bool {
	if c == nil {
		return true
	}
	switch kind {
	case MediaImage:
		return c.Vision
	case MediaAudio:
		return c.Audio
	}
	return false
}

// MediaUnsupported is evidence that holds media this model cannot be given. It is
// returned before the request goes out, because afterwards the only account of it is
// a sentence from the server about somebody's mmproj.
type MediaUnsupported struct {
	Kind  MediaKind
	Model string // empty when the server never said what it is
	Count int    // how many pieces of that media the evidence holds
}

func (e *MediaUnsupported) Error() string {
	subject := "the answering model"
	if e.Model != "" {
		subject = fmt.Sprintf("the answering model %q", e.Model)
	}
	noun := string(e.Kind)
	if e.Count != 1 {
		noun += "s"
	}
	return fmt.Sprintf("llm: %s has no %s projector, and this answer rests on %d %s it cannot take",
		subject, e.Kind.Modality(), e.Count, noun)
}

// Props reads what a serving model can be shown, naming it first.
//
// The name is the part that matters, and it is why a call that looks like a one-line
// GET is not one. Libris runs the engine in router mode: one process holding a preset,
// which starts whichever model a request names. A router answers GET /props about
// itself (server-models.cpp, get_router_props) — role "router", model_alias the
// placeholder "llama-server", model_path "none", and no modalities key at all. Read
// that as the serving model's account and every notebook holding a page fails with an
// error about a projector, on a machine where the projector is loaded and running; the
// placeholder is even commented upstream as "a dummy response to make sure the UI
// doesn't break". With ?model=<alias> the same URL is proxied to the instance, which
// answers from its own loaded mtmd context.
//
// autoload=false is the other half of the query. Models load on demand and the router
// obliges a probe: without it, asking a capability question costs loading the model,
// the caller's short budget is spent on a load that cannot fit inside it, and the
// answer is "unknown" either way.
//
// The endpoint is /props at the root, not /v1/props, so this does not go through
// endpoint(). It is a GET on a loopback port that answers in microseconds once the
// model is up, which is what buys asking per question rather than caching an answer a
// serving restart would quietly invalidate.
func (c *Client) Props(ctx context.Context, model string) (*Caps, error) {
	target := root(c.cfg.ChatURL) + "/props"
	if name := strings.TrimSpace(model); name != "" {
		q := url.Values{"model": {name}, "autoload": {"false"}}
		target += "?" + q.Encode()
	}

	resp, err := c.getProps(ctx, target)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return nil, readError(resp)
	}

	// modalities is the object server-context.cpp writes from the instance's own mtmd
	// capabilities — meta.has_inp_image — so it says what the loaded projector can do,
	// not what a file on disk suggests it might. It is a pointer because absence must
	// not be read as a denial: a struct here would decode a payload without the key into
	// vision:false, which is the one mistake this whole path exists to avoid.
	var raw struct {
		Role       string `json:"role"`
		ModelAlias string `json:"model_alias"`
		Model      string `json:"model"`
		Modalities *struct {
			Vision bool `json:"vision"`
			Video  bool `json:"video"`
			Audio  bool `json:"audio"`
		} `json:"modalities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("llm: decode props: %w", err)
	}

	// Spoken about the router, or about a model that never named its modalities: nobody
	// answered. Callers read that as "it did not say", which is the right fallback here
	// — which is why this is a returned error and not something louder.
	if raw.Modalities == nil || raw.Role == "router" {
		return nil, fmt.Errorf("llm: /props spoke about %q and named no model to show anything", raw.Role)
	}

	name := raw.ModelAlias
	if name == "" {
		name = raw.Model
	}
	return &Caps{
		Model:  name,
		Vision: raw.Modalities.Vision,
		Audio:  raw.Modalities.Audio,
		Video:  raw.Modalities.Video,
	}, nil
}

// root is where the engine's non-OpenAI endpoints live. A chat URL already ending in
// /v1 is the same server one level up.
func root(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	return strings.TrimSuffix(base, "/v1")
}

func (c *Client) getProps(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("llm: props: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: props: %w", err)
	}
	return resp, nil
}
