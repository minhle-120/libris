// Package llm is the only place in Libris that speaks the OpenAI wire format.
//
// It targets any OpenAI-compatible server: a standalone llama.cpp server, one
// packaged inside the app, or vLLM. Process lifecycle and health probing are
// deliberately out of scope here.
package llm

import (
	"encoding/json"
	"sort"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message.Content is either a string or a []ContentPart.
type Message struct {
	Role       Role       `json:"role"`
	Content    any        `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`

	// ReasoningContent is returned by servers that keep thinking out of Content.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// ── multimodal content ────────────────────────────────────────────

const (
	PartText  = "text"
	PartImage = "image_url"
	PartAudio = "input_audio"
	PartVideo = "input_video"
)

type ContentPart struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	ImageURL   *ImageURL   `json:"image_url,omitempty"`
	InputAudio *InputMedia `json:"input_audio,omitempty"`
	InputVideo *InputMedia `json:"input_video,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

// InputMedia carries either raw base64 (Data) or a local path / URL (URL).
type InputMedia struct {
	Data string `json:"data,omitempty"`
	URL  string `json:"url,omitempty"`
}

func Parts(p ...ContentPart) []ContentPart { return p }

func TextPart(s string) ContentPart { return ContentPart{Type: PartText, Text: s} }

func ImagePart(url string) ContentPart {
	return ContentPart{Type: PartImage, ImageURL: &ImageURL{URL: url}}
}

// ImageBase64 inlines an image as a data: URL.
//
// Verified against llama.cpp's server README: image_url.url accepts a remote
// URL, a base64 data URI, or a local file — and only the local file needs the
// server started with --media-path. So inlining is the only form that works
// against a plain `llama-server --embedding --mmproj …`, which is why the
// embedding stage sends page rasters this way instead of asking the user for a
// flag. There is still no auto-detection: choosing to inline is explicit.
func ImageBase64(mimeType, data string) ContentPart {
	return ContentPart{
		Type:     PartImage,
		ImageURL: &ImageURL{URL: "data:" + mimeType + ";base64," + data},
	}
}

// AudioPart and VideoPart reference media by URL or local file, keeping whole
// files out of the request body. There is no auto-detection: a path such as
// "path/to/file" is indistinguishable from base64 by charset alone, so
// embedding raw bytes is always an explicit choice.
//
// llama.cpp requires local references to be prefixed with file:// and the
// server to be started with --media-path; anything else is treated as remote.
func AudioPart(src string) ContentPart {
	return ContentPart{Type: PartAudio, InputAudio: &InputMedia{URL: src}}
}

func VideoPart(src string) ContentPart {
	return ContentPart{Type: PartVideo, InputVideo: &InputMedia{URL: src}}
}

// AudioBase64 and VideoBase64 inline raw base64 payloads.
func AudioBase64(data string) ContentPart {
	return ContentPart{Type: PartAudio, InputAudio: &InputMedia{Data: data}}
}

func VideoBase64(data string) ContentPart {
	return ContentPart{Type: PartVideo, InputVideo: &InputMedia{Data: data}}
}

// ── tools ─────────────────────────────────────────────────────────

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
	Index    *int         `json:"index,omitempty"` // streaming fragments
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// NewTool builds a function tool, marshalling params into a JSON schema.
func NewTool(name, description string, params any) (Tool, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return Tool{}, err
	}
	return Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        name,
			Description: description,
			Parameters:  raw,
		},
	}, nil
}

// Tool choice values understood by OpenAI-compatible servers.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
)

// ── chat ──────────────────────────────────────────────────────────

// ChatRequest carries structure only: who said what, which tools exist, and
// whether to stream.
//
// Sampling and inference configuration is deliberately absent. Temperature,
// top-k/p, min-p, mirostat, repeat penalties, n_predict, seed, stop strings,
// grammar and KV-cache behaviour are all fixed on the llama-server command
// line, so sending them per request would either be ignored or silently
// diverge from what the operator configured. The client says what to think
// about, the server decides how.
type ChatRequest struct {
	Model      string    `json:"model,omitempty"` // echoed by router-mode servers, ignored otherwise
	Messages   []Message `json:"messages"`
	Stream     bool      `json:"stream,omitempty"`
	Tools      []Tool    `json:"tools,omitempty"`
	ToolChoice string    `json:"tool_choice,omitempty"` // auto | none | required
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Created int64        `json:"created"`
	Choices []ChatChoice `json:"choices"`
	Usage   *Usage       `json:"usage,omitempty"`
}

type ChatChoice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Chunk is one streaming delta plus its terminator.
type Chunk struct {
	Delta        Delta
	FinishReason string
	Usage        *Usage
}

type chatChunk struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

type Delta struct {
	Role             Role       `json:"role,omitempty"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// ── embeddings ────────────────────────────────────────────────────

type EmbedRequest struct {
	Model          string       `json:"model,omitempty"`
	Input          []EmbedInput `json:"input"`
	EncodingFormat string       `json:"encoding_format,omitempty"` // "float"; the server normalizes vectors
}

// EmbedInput is either a plain string or a multimodal content object. The
// object form follows llama.cpp's extension, which mirrors the shape used by
// vision-embedding providers.
type EmbedInput struct {
	Text  string
	Parts []ContentPart
}

func TextInput(s string) EmbedInput { return EmbedInput{Text: s} }

func PartsInput(p ...ContentPart) EmbedInput { return EmbedInput{Parts: p} }

func (in EmbedInput) MarshalJSON() ([]byte, error) {
	if len(in.Parts) > 0 {
		return json.Marshal(struct {
			Content []ContentPart `json:"content"`
		}{in.Parts})
	}
	return json.Marshal(in.Text)
}

type EmbedResponse struct {
	Model string      `json:"model"`
	Data  []Embedding `json:"data"`
	Usage *Usage      `json:"usage,omitempty"`
}

type Embedding struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// ── reranking ─────────────────────────────────────────────────────

// RerankRequest always uses the "documents" key, which selects llama.cpp's
// Jina response format (the "texts" key would select TEI's bare array).
type RerankRequest struct {
	Model     string      `json:"model,omitempty"`
	Query     string      `json:"query"`
	Documents []RerankDoc `json:"documents"`
	TopN      *int        `json:"top_n,omitempty"`
}

// RerankDoc is one document for the reranker.
//
// A text-only document marshals as a bare string, which is the form every rerank
// server reads. A document with an image marshals as {"text":…,"image":…}, the
// form llama.cpp parses when the reranker was started with an mmproj and a rerank
// chat template. Image is a data: URI or an http(s) URL, never a path — a file://
// path would demand --media-path on that server too.
//
// Both shapes exist so a search result can hold text chunks and page rasters and
// be scored on one scale. Scoring the text and ranking the images by a different
// measure would produce a merged list whose order means nothing.
type RerankDoc struct {
	Text  string
	Image string
}

// TextDoc is a passage to score.
func TextDoc(text string) RerankDoc { return RerankDoc{Text: text} }

// ImageDoc is a raster to score, inlined as a data: URI.
func ImageDoc(dataURL string) RerankDoc { return RerankDoc{Image: dataURL} }

func (d RerankDoc) MarshalJSON() ([]byte, error) {
	switch {
	case d.Image == "":
		return json.Marshal(d.Text)
	case d.Text == "":
		return json.Marshal(map[string]string{"image": d.Image})
	default:
		return json.Marshal(map[string]string{"text": d.Text, "image": d.Image})
	}
}

type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type RerankResponse struct {
	Model   string         `json:"model"`
	Results []RerankResult `json:"results"`
	Usage   *Usage         `json:"usage,omitempty"`

	// Data is the alternative key used by some servers for the same payload.
	Data []RerankResult `json:"data,omitempty"`
}

// Ranked returns the results regardless of which key the server used, sorted
// descending by score.
func (r *RerankResponse) Ranked() []RerankResult {
	out := r.Results
	if len(out) == 0 {
		out = r.Data
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].RelevanceScore > out[j].RelevanceScore
	})
	return out
}
