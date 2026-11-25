// Package llm is the only place in Libris that speaks the OpenAI wire format.
//
// It targets any OpenAI-compatible server: a standalone llama.cpp server, one
// packaged inside the app, or vLLM. Process lifecycle and health probing are
// deliberately out of scope here.
package llm

import (
	"encoding/json"
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

// Only the part types libris actually sends. input_audio and input_video were
// declared and never built by anything: audio stopped being an input modality, and
// video never was, so a client carrying both was a menu of wire shapes rather than
// a description of this program's traffic.
const (
	PartText  = "text"
	PartImage = "image_url"
)

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

func Parts(p ...ContentPart) []ContentPart { return p }

func TextPart(s string) ContentPart { return ContentPart{Type: PartText, Text: s} }

// ImagePart references an image by URL. There is no auto-detection between the two
// constructors: a path such as "path/to/file" is indistinguishable from base64 by
// charset alone, so choosing to inline raw bytes is always explicit.
//
// llama.cpp requires a local reference to be prefixed with file:// and the server
// to be started with --media-path; anything else is treated as remote.
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

	// ChatTemplateKwargs is the object a server merges into the chat template before it
	// renders the prompt. Nil sends no such object and leaves the server's own default in
	// place — which is not the same as sending a value: llama.cpp starts some models with
	// thinking on and others with it off, and only one of the two says what the user chose.
	ChatTemplateKwargs *ChatTemplateKwargs `json:"chat_template_kwargs,omitempty"`
}

// ChatTemplateKwargs holds the one template flag libris sends. It is a struct rather than a
// map so the key cannot be mistyped into something a server ignores in silence, and the flag
// is a Go bool rather than an `any` because llama.cpp throws on a template kwarg of the wrong
// type — {"enable_thinking": "false"} is a 500, not a preference.
//
// Checked against the pinned clone: tools/server/server-common.cpp merges the request object
// over the --chat-template-kwargs defaults and reads enable_thinking out of it, overriding
// whatever the server was started with. The CLI flag for the same key is deprecated in favour
// of --reasoning; the request field is not.
type ChatTemplateKwargs struct {
	EnableThinking bool `json:"enable_thinking"`
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
