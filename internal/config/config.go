// Package config is the single source of truth for every setting: values,
// labels, help text, ranges, units and enums.
//
// The frontend does not define any of this. internal/config/schema.go reflects
// over the tags below and emits web/src/lib/config.gen.ts, and a test fails if
// that file is stale. Add a field here and it appears in the UI.
package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

// PDFMode selects how PDFs are turned into segments.
type PDFMode string

const (
	PDFModeAlways   PDFMode = "always"
	PDFModeFallback PDFMode = "fallback"
)

// EnumValue is one choice of an enum field.
type EnumValue struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Enums backs the `enum:"<name>"` tag. Options live here, not in the frontend.
func Enums() map[string][]EnumValue {
	return map[string][]EnumValue{
		"pdfModes": {
			{Value: string(PDFModeAlways), Label: "Text + page image"},
			{Value: string(PDFModeFallback), Label: "Page image only when text is missing"},
		},
	}
}

// Config is flat on purpose: Go code reads cfg.PDFDPI, and the `group` tag
// only affects how the generated UI arranges the rows.
//
// Tag reference:
//
//	key      JSON/TS property name (required)
//	label    UI label
//	help     UI description
//	group    UI section: connection | models | retrieval | ingest
//	kind     text (default) | url | password | number | bool | enum
//	enum     name in Enums()
//	unit     suffix shown after the control
//	min,max  range, validated by reflection
//	required non-empty string
//	readonly generated as a disabled control
type Config struct {
	// ── llama.cpp servers ────────────────────────────────────────
	ChatURL        string `key:"chatURL" group:"connection" label:"Chat server" help:"Text, image and audio input." kind:"url" required:"true"`
	EmbedURL       string `key:"embedURL" group:"connection" label:"Embedding server" help:"Multimodal embeddings for text, page images and audio." kind:"url" required:"true"`
	RerankURL      string `key:"rerankURL" group:"connection" label:"Rerank server" help:"Optional. Degrades to vector order when unset." kind:"url"`
	APIKey         string `key:"apiKey" group:"connection" label:"API key" help:"Sent as a bearer token to all three servers." kind:"password"`
	TimeoutSeconds int    `key:"timeoutSeconds" group:"connection" label:"Request timeout" help:"Local CPU inference is slow. Lower it for GPU." min:"5" max:"3600" unit:"s"`

	// ── models ───────────────────────────────────────────────────
	ChatModel     string `key:"chatModel" group:"models" label:"Chat model" help:"Must support tool calls for the research loop."`
	ContextSize   int    `key:"contextSize" group:"models" label:"Context window" help:"n_ctx per slot: the KV cache budget." min:"512" max:"1048576" unit:"tokens"`
	GPULayers     int    `key:"gpuLayers" group:"models" label:"GPU layers" help:"-1 for all, 0 for CPU only." min:"-1" max:"999"`
	EmbedModel    string `key:"embedModel" group:"models" label:"Embedding model" help:"EmbeddingGemma 2: text, image and audio in one space."`
	EmbedDim      string `key:"embedDimensions" group:"models" label:"Embedding dimensions" help:"Probed once at startup and recorded with every segment." readonly:"true"`
	RerankModel   string `key:"rerankModel" group:"models" label:"Reranker" help:"bge-reranker-v2-m3, or a VL reranker for images."`
	RerankEnabled bool   `key:"rerankEnabled" group:"models" label:"Enable reranking" help:"Off means pure vector order."`

	// ── retrieval ────────────────────────────────────────────────
	RecallTopK      int `key:"recallTopK" group:"retrieval" label:"Recall depth" help:"Candidates pulled from the vector scan, before reranking." min:"1" max:"100" unit:"candidates"`
	FinalTopK       int `key:"finalTopK" group:"retrieval" label:"Evidence per search" help:"What the research agent sees in a tool result." min:"1" max:"50" unit:"chunks"`
	MaxIterations   int `key:"maxIterations" group:"retrieval" label:"Max iterations" help:"Deeper multi-hop research; diminishing returns past 3." min:"1" max:"10"`
	HistoryMessages int `key:"historyMessages" group:"retrieval" label:"History window" help:"Recent turns given to the synthesizer." min:"0" max:"64" unit:"messages"`
	ChunkWords      int `key:"chunkWords" group:"retrieval" label:"Chunk size" help:"Target words per text chunk." min:"10" max:"1000" unit:"words"`
	ChunkOverlap    int `key:"chunkOverlap" group:"retrieval" label:"Chunk overlap" help:"Words carried into the next chunk." min:"0" max:"999" unit:"words"`

	// ── ingest ───────────────────────────────────────────────────
	PDFMode            PDFMode `key:"pdfPageImages" group:"ingest" label:"PDF embeddings" help:"A PDF becomes two documents: page text and page rasters." kind:"enum" enum:"pdfModes"`
	PDFDPI             int     `key:"pdfDPI" group:"ingest" label:"PDF raster DPI" help:"Resolution for page images. 150 keeps a page legible at roughly 300-500 KB." min:"50" max:"600" unit:"dpi"`
	AudioWindowSeconds int     `key:"audioWindowSeconds" group:"ingest" label:"Audio window" help:"Clip length for audio segments; align with transcript boundaries." min:"5" max:"600" unit:"s"`
	MaxFileMB          int     `key:"maxFileMB" group:"ingest" label:"Max file size" help:"Larger files are refused at the door, not mid-job." min:"1" max:"102400" unit:"MB"`
	KeepOriginals      bool    `key:"keepOriginals" group:"ingest" label:"Keep originals" help:"Required for page and timestamp citations." readonly:"true"`
}

// Group titles and copy. Presentation only; the fields themselves are above.
func groups() []struct{ ID, Label, Help string } {
	return []struct{ ID, Label, Help string }{
		{"connection", "llama.cpp servers", "One process per model. Separate ports keep embedding and chat from starving each other."},
		{"models", "Models", "Names are what the server reports. Changing the embedder re-indexes every source."},
		{"retrieval", "Retrieval", "Research loop budget. Every extra iteration costs one search round trip."},
		{"ingest", "Ingest", "Extraction is a background job. Originals are always kept."},
	}
}

func Default() Config {
	return Config{
		ChatURL:        "http://127.0.0.1:8081",
		EmbedURL:       "http://127.0.0.1:8082",
		RerankURL:      "http://127.0.0.1:8083",
		APIKey:         "",
		TimeoutSeconds: 300,

		ChatModel:     "qwen3-vl-8b-instruct-q4_k_m",
		ContextSize:   16384,
		GPULayers:     -1,
		EmbedModel:    "embeddinggemma-2-q8_0",
		EmbedDim:      "",
		RerankModel:   "bge-reranker-v2-m3-q8_0",
		RerankEnabled: true,

		RecallTopK:      10,
		FinalTopK:       5,
		MaxIterations:   5,
		HistoryMessages: 8,
		ChunkWords:      100,
		ChunkOverlap:    50,

		PDFMode:            PDFModeAlways,
		PDFDPI:             150,
		AudioWindowSeconds: 30,
		MaxFileMB:          512,
		KeepOriginals:      true,
	}
}

// MaxFileBytes is derived, never configured directly: one unit, one field.
func (c Config) MaxFileBytes() int64 { return int64(c.MaxFileMB) << 20 }

// Load returns defaults with LIBRIS_* environment overrides applied.
func Load() (Config, error) {
	c := Default()

	for i := 0; i < reflect.TypeOf(c).NumField(); i++ {
		f := reflect.TypeOf(c).Field(i)
		env := "LIBRIS_" + strings.ToUpper(snake(f.Tag.Get("key")))
		raw, ok := os.LookupEnv(env)
		if !ok || raw == "" {
			continue
		}

		fv := reflect.ValueOf(&c).Elem().Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(raw)
		case reflect.Int:
			n, err := strconv.Atoi(raw)
			if err != nil {
				return c, fmt.Errorf("config: %s: %w", env, err)
			}
			fv.SetInt(int64(n))
		case reflect.Bool:
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return c, fmt.Errorf("config: %s: %w", env, err)
			}
			fv.SetBool(b)
		default:
			return c, fmt.Errorf("config: %s: unsupported field kind %s", env, fv.Kind())
		}
	}
	return c, c.Validate()
}

// Validate enforces the tags by reflection, so ranges are declared once.
func (c Config) Validate() error {
	v := reflect.ValueOf(c)
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := v.Field(i)
		key := f.Tag.Get("key")

		switch fv.Kind() {
		case reflect.Int:
			if s := f.Tag.Get("min"); s != "" {
				if min, _ := strconv.ParseInt(s, 10, 64); fv.Int() < min {
					return fmt.Errorf("config: %s = %d, below minimum %d", key, fv.Int(), min)
				}
			}
			if s := f.Tag.Get("max"); s != "" {
				if max, _ := strconv.ParseInt(s, 10, 64); fv.Int() > max {
					return fmt.Errorf("config: %s = %d, above maximum %d", key, fv.Int(), max)
				}
			}
		case reflect.String:
			if f.Tag.Get("required") == "true" && strings.TrimSpace(fv.String()) == "" {
				return fmt.Errorf("config: %s is required", key)
			}
			if f.Tag.Get("kind") == "url" && fv.String() != "" && !isHTTP(fv.String()) {
				return fmt.Errorf("config: %s must be an http(s) URL, got %q", key, fv.String())
			}
			if enum := f.Tag.Get("enum"); enum != "" {
				if !validEnum(enum, fv.String()) {
					return fmt.Errorf("config: %s = %q is not a valid %s", key, fv.String(), enum)
				}
			}
		}
	}

	if c.ChunkOverlap >= c.ChunkWords {
		return fmt.Errorf("config: chunk overlap %d must be less than chunk size %d", c.ChunkOverlap, c.ChunkWords)
	}
	return nil
}

func validEnum(name, value string) bool {
	for _, v := range Enums()[name] {
		if v.Value == value {
			return true
		}
	}
	return false
}

func isHTTP(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// snake turns a camelCase key into ENV_STYLE, breaking only at camel
// boundaries: chatURL -> chat_url, maxFileMB -> max_file_mb.
func snake(key string) string {
	var b strings.Builder
	prevLower := false
	for _, r := range key {
		if r >= 'A' && r <= 'Z' {
			if prevLower {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			prevLower = false
			continue
		}
		prevLower = (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		b.WriteRune(r)
	}
	return b.String()
}

// PDFPageImages reports the segments a PDF should produce.
func (c Config) PDFAlwaysImages() bool { return c.PDFMode != PDFModeFallback }
