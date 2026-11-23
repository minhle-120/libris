// Package config is the single source of truth for every setting: values,
// labels, help text, ranges, units and enums.
//
// The frontend does not define any of this. internal/config/schema.go reflects
// over the tags below and emits web/src/lib/config.gen.ts, and a test fails if
// that file is stale. Add a field here and it appears in the UI.
package config

import (
	"fmt"
	"libris/internal/engine"
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
		// There is no engine *build* choice: one Vulkan asset runs on NVIDIA, AMD and Intel
		// alike, and on pure CPU when no Vulkan device will open (ADR 0020). What is a choice
		// is where the models come from — and it is exclusive, so it is one enum rather than
		// two toggles that can both be on.
		"engineModes": {
			{Value: string(EngineManaged), Label: "Download and run llama.cpp"},
			{Value: string(EngineExternal), Label: "Servers I run myself"},
		},
	}
}

// EngineMode is the one answer to "where do the model servers come from", and it is
// exclusive by construction: a machine either has libris fetching and running llama.cpp, or
// it has servers somebody else started (ADR 0020). Both at once is what the settings page
// used to offer, and every screen of it showed fields that belong to the other answer.
type EngineMode string

const (
	// EngineManaged: libris downloads the Vulkan build, starts one llama-server in router
	// mode on the loopback port below, and the endpoint and model names are derived from
	// that process rather than typed into a field.
	EngineManaged EngineMode = "managed"
	// EngineExternal: the three URLs below are the whole configuration, as they always were.
	EngineExternal EngineMode = "external"
)

// Config is flat on purpose: Go code reads cfg.PDFDPI, and the `group` tag
// only affects how the generated UI arranges the rows.
//
// Tag reference:
//
//	key      JSON/TS property name (required)
//	label    UI label
//	help     UI description
//	group    UI section: servers | models | retrieval | ingest
//	kind     text (default) | url | password | number | bool | enum
//	enum     name in Enums()
//	unit     suffix shown after the control
//	min,max  range, validated by reflection
//	required non-empty string
//	readonly generated as a disabled control
//	mode     "managed" — only means anything when libris runs the engine
//	         "external" — only means anything when somebody else does
type Config struct {
	// ── model servers ─────────────────────────────────────────────
	// One group, because it is one question. The engine and the endpoints are not two
	// topics a user has to reconcile: which mode is set decides which half of the group is
	// shown, and the fields that are hidden are the ones libris derives.
	EngineMode EngineMode `key:"engineMode" group:"servers" label:"Who runs them" help:"Libris can download llama.cpp, run it, and derive the addresses itself — or talk to servers you started. Not both." kind:"enum" enum:"engineModes" required:"true"`

	// The port libris binds its own server to. A setting because libris is the thing that
	// binds it (ADR 0011: a setting is an ability the app can apply), and a loopback port
	// rather than a URL because "which address" is not a choice when the process is ours.
	EnginePort  int    `key:"enginePort" group:"servers" label:"Server port" help:"The loopback port libris runs llama.cpp on. Chat, embeddings and reranking share it." min:"1024" max:"65535" mode:"managed"`
	EngineProxy string `key:"engineProxy" group:"servers" label:"Download mirror" help:"Optional prefix for the llama.cpp download, for networks that cannot reach github.com. The version check still goes to GitHub, because a file mirror rarely mirrors its API." kind:"url" mode:"managed"`

	ChatURL        string `key:"chatURL" group:"servers" label:"Chat server" help:"Text, image and audio input." kind:"url" required:"true" mode:"external"`
	EmbedURL       string `key:"embedURL" group:"servers" label:"Embedding server" help:"Multimodal embeddings for text, page images and audio." kind:"url" required:"true" mode:"external"`
	RerankURL      string `key:"rerankURL" group:"servers" label:"Rerank server" help:"Optional. Degrades to vector order when unset." kind:"url" mode:"external"`
	APIKey         string `key:"apiKey" group:"servers" label:"API key" help:"Sent as a bearer token to every server." kind:"password"`
	TimeoutSeconds int    `key:"timeoutSeconds" group:"servers" label:"Request timeout" help:"Local CPU inference is slow. Lower it for GPU." min:"5" max:"3600" unit:"s"`

	// ── models ───────────────────────────────────────────────────
	// Model names are what the server answers to. In managed mode they are the aliases in
	// the preset libris writes, so they are not typed anywhere; in external mode they are
	// what the user's own llama-server was started with. Libris ships no guesses either
	// way: a default that names a model nobody downloaded is a field that looks correct and
	// fails anyway. Capacity is not here either — n_ctx and the GPU layer count belong to
	// the server's command line (ADR 0011), and in managed mode to its preset file.
	ChatModel     string `key:"chatModel" group:"models" label:"Chat model" help:"The name your chat server answers to. Must support tool calls for the research loop." mode:"external"`
	EmbedModel    string `key:"embedModel" group:"models" label:"Embedding model" help:"The name your embedding server answers to." mode:"external"`
	EmbedDim      string `key:"embedDimensions" group:"models" label:"Embedding dimensions" help:"Probed once at startup and recorded with every segment." readonly:"true"`
	RerankModel   string `key:"rerankModel" group:"models" label:"Reranker" help:"The name your rerank server answers to; blank when reranking is off." mode:"external"`
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
	PDFDPI             int     `key:"pdfDPI" group:"ingest" label:"PDF raster DPI" help:"Resolution for page images. Only affects how legible a cited page looks: the embedding cost per image is fixed." min:"50" max:"600" unit:"dpi"`
	AudioWindowSeconds int     `key:"audioWindowSeconds" group:"ingest" label:"Audio window" help:"Clip length for audio segments; align with transcript boundaries." min:"5" max:"600" unit:"s"`
	MaxFileMB          int     `key:"maxFileMB" group:"ingest" label:"Max file size" help:"Larger files are refused at the door, not mid-job." min:"1" max:"102400" unit:"MB"`
	KeepOriginals      bool    `key:"keepOriginals" group:"ingest" label:"Keep originals" help:"Required for page and timestamp citations." readonly:"true"`
}

// Group titles and copy. Presentation only; the fields themselves are above.
func groups() []struct{ ID, Label, Help string } {
	return []struct{ ID, Label, Help string }{
		{"servers", "Model servers", "Either libris runs llama.cpp for you, or it talks to servers you started. The switch decides which of these fields exist — libris does not ask for an address it is about to compute."},
		{"models", "Models", "Names are what the server reports. Changing the embedder re-indexes every source."},
		{"retrieval", "Retrieval", "Research loop budget. Every extra iteration costs one search round trip."},
		{"ingest", "Ingest", "Extraction is a background job. An original is kept until no notebook uses it (ADR 0013)."},
	}
}

func Default() Config {
	return Config{
		// Managed is the default because it is the answer that needs nothing typed: libris
		// fetches the build, runs it on the port below, and the addresses are derived. The
		// three URLs and three names still have defaults, because external mode is a real
		// mode and a saved config that validates in one mode must validate in the other.
		EngineMode:  EngineManaged,
		EnginePort:  8080,
		EngineProxy: "",

		ChatURL:        "http://127.0.0.1:8081",
		EmbedURL:       "http://127.0.0.1:8082",
		RerankURL:      "http://127.0.0.1:8083",
		APIKey:         "",
		TimeoutSeconds: 300,

		ChatModel:     "",
		EmbedModel:    "",
		EmbedDim:      "",
		RerankModel:   "",
		RerankEnabled: true,

		RecallTopK:      10,
		FinalTopK:       5,
		MaxIterations:   5,
		HistoryMessages: 8,
		ChunkWords:      100,
		ChunkOverlap:    50,

		PDFMode:            PDFModeAlways,
		PDFDPI:             110,
		AudioWindowSeconds: 30,
		MaxFileMB:          512,
		KeepOriginals:      true,
	}
}

// Served returns the config as the answer path should read it. In managed mode the three
// addresses and the three model names are not settings: they are facts about the single
// router process libris runs, so they are derived here, at the one place where a stored
// value turns into a request (ADR 0020). External mode is the identity, and that asymmetry
// is the point — the fields a person typed are used exactly as typed, and the ones libris
// derives are never stored, so there is nothing for a page to disagree with.
func (c Config) Served() Config {
	if c.EngineMode != EngineManaged {
		return c
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", c.EnginePort)
	out := c
	out.ChatURL, out.EmbedURL, out.RerankURL = base, base, base
	out.ChatModel = string(engine.AliasChat)
	out.EmbedModel = string(engine.AliasEmbed)
	out.RerankModel = string(engine.AliasRerank)
	return out
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
