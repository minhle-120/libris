// Package llm is the only place in Libris that speaks the OpenAI wire format.
//
// It targets any OpenAI-compatible server: a standalone llama.cpp server, one
// packaged inside the app, or vLLM. Process lifecycle and health probing are
// deliberately out of scope here.
package llm

import (
	"encoding/json"
	"fmt"
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

// MediaKind is an input modality libris can show a model. Video stays undeclared:
// a client carrying a wire shape it never builds is a menu rather than a description
// of this program's traffic, which is why input_video was deleted and has not come
// back.
//
// What the seam buys is that adding one is mechanical rather than a search: a constant
// here, its Part constant, one case in MediaPart, one case in Caps.Allows, and one arm
// of retrieve's mapping from a segment kind.
//
// The bar for arriving here used to be "with citable words beside it", and ADR 0010
// lowered it for audio: a clip carries no words of its own, and the words arrive inside
// the answer, because the model that writes the answer is the one that heard the clip.
type MediaKind string

const (
	MediaImage MediaKind = "image"
	MediaAudio MediaKind = "audio"
)

// Modality names the thing a serving model must have loaded to take this kind, in
// the words an error about it is written in.
func (k MediaKind) Modality() string {
	switch k {
	case MediaImage:
		return "vision"
	}
	return string(k)
}

// Only the part types libris actually sends. See MediaKind for why the others are
// not here yet.
const (
	PartText       = "text"
	PartImage      = "image_url"
	PartInputAudio = "input_audio"
)

type ContentPart struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	ImageURL   *ImageURL   `json:"image_url,omitempty"`
	InputAudio *InputAudio `json:"input_audio,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

// InputAudio is the audio half of OpenAI's audio content part. Only Data is sent,
// as a data URI: a URL form would ask the server for --media-path, which libris never
// passes (see MediaPart). "format" is deliberately absent — llama.cpp ignores the key
// and sniffs the bytes it decoded, so sending one would be a claim about the container
// that nothing reads.
type InputAudio struct {
	Data string `json:"data"`
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

// MediaPart inlines one media file as the content part a server expects for that
// modality. It is the only constructor for media parts, so every media byte libris
// sends goes through the one switch that knows the wire shapes.
//
// Checked against the pinned clone: tools/server/server-common.cpp's
// oaicompat_content_load_media reads image_url.url — a remote URL, a base64 data
// URI, or a file:// path that needs the server started with --media-path — queues
// the decoded bytes, and replaces the part with a media marker. Bytes and marker are
// paired by position in the request, not by any id, so the parts a caller builds
// must stay in the order it wants them read. Inlining is the only form that asks
// nothing of the server, which is why every media part libris sends is one. That is
// true of the embedding stage too, which is started with nobody holding a flag.
//
// The data URI is the whole transport for both kinds, which is what the server's own
// reader accepts for audio: oaicompat_content_load_media decodes the base64 and hands
// the bytes to the projector, which sniffs the container from the data rather than from
// a format key beside it.
//
// A kind with no case is refused rather than guessed at: shipping a part of a type
// the server throws on would fail the turn for a mistake made here.
func MediaPart(kind MediaKind, mimeType, data string) (ContentPart, error) {
	uri := "data:" + mimeType + ";base64," + data
	switch kind {
	case MediaImage:
		return ContentPart{Type: PartImage, ImageURL: &ImageURL{URL: uri}}, nil
	case MediaAudio:
		return ContentPart{Type: PartInputAudio, InputAudio: &InputAudio{Data: uri}}, nil
	}
	return ContentPart{}, fmt.Errorf("llm: no wire shape for media kind %q", kind)
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
