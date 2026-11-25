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
// Audio and Video are the engine's account of itself, not a modality libris sends:
// image is the only kind with a wire shape (see MediaPart), and a recording is still
// not an input.
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

// Props reads what the chat server is serving and what it can take.
//
// The endpoint is /props at the root, not /v1/props, so this does not go through
// endpoint(). It is a GET on a loopback port that answers in microseconds, which is
// what buys calling it per question rather than caching an answer a serving restart
// would quietly invalidate.
func (c *Client) Props(ctx context.Context) (*Caps, error) {
	resp, err := c.get(ctx, root(c.cfg.ChatURL), "props")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return nil, readError(resp)
	}

	// modalities is the object server-context.cpp writes from the model's own mtmd
	// capabilities, so it says what the loaded projector can do — not what the file
	// on disk suggests it might.
	var raw struct {
		ModelAlias string `json:"model_alias"`
		Model      string `json:"model"`
		Modalities struct {
			Vision bool `json:"vision"`
			Video  bool `json:"video"`
			Audio  bool `json:"audio"`
		} `json:"modalities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("llm: decode props: %w", err)
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

func (c *Client) get(ctx context.Context, base, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/"+path, nil)
	if err != nil {
		return nil, fmt.Errorf("llm: %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: %s: %w", path, err)
	}
	return resp, nil
}
