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
		// There is no engine *build* choice: one Vulkan asset runs on NVIDIA, AMD and Intel
		// alike, and on pure CPU when no Vulkan device will open. What is a choice
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
// it has servers somebody else started. Both at once is what the settings page
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
	// binds it (a setting is an ability the app can apply), and a loopback port
	// rather than a URL because "which address" is not a choice when the process is ours.
	// EnginePort is which loopback port libris binds when it runs the servers. Zero means
	// Auto: the kernel is asked for a free port every time the server starts, so two libris
	// installs, a dev server and anything else already holding 8080 cannot collide with it.
	// A number is a request for that exact port and is tried once — the reason to name a port
	// is usually that something else was told that address, so binding a different one
	// silently would break the thing it was pinned for.
	//
	// The floor is 1024 and 0 is legal anyway, because ports below 1024 need root on Linux and
	// setting one would produce a failure about permissions rather than about the choice.
	EnginePort int `key:"enginePort" group:"servers" label:"Server port" help:"0 lets libris take a free port each time the server starts. A number asks for that exact port, and the start fails if something else already holds it. Chat and embeddings share whatever port is chosen." min:"1024" max:"65535" autoZero:"true" mode:"managed"`

	// Which device llama.cpp offloads to, written into the preset as --device. A setting
	// because libris is the thing that writes it, and a string rather than an enum
	// because the legal values are not a list this program can know: they are the slot names
	// the installed build prints with --list-devices, which differ per machine and per build —
	// "Vulkan0" on the build libris downloads, "CUDA0" on one a person built themselves — and a
	// name that build does not know is an argument error, not a fallback. Blank means libris
	// picks the first device that build can use (deviceinfo.Report.DefaultDevice) — nothing on
	// the page offers that any more, but a setting written before the choice was taken, or one
	// edited by hand, still has to mean something.
	//
	// noview: the control is the picker beside the device report in the machine section, because
	// a text field would ask someone to type a name they can only learn by running a command.
	// The CPU is one of the choices rather than a fallback sentence: its name is llama.cpp's own
	// "none", so the value the row carries is the value the preset needs.
	EngineDevice string `key:"engineDevice" group:"servers" label:"Run models on" help:"Which device llama.cpp offloads to. The choices are the slots the installed engine reports, CPU among them." mode:"managed" noview:"true"`
	// EngineDeviceAck records that a person has looked at the device choice for this engine
	// build on this machine. It is not a preference and no form should offer it: it is the
	// difference between “libris picked a device” and “nobody was ever asked”, which only the
	// setup rule needs to know. The value is the engine-and-machine key the confirmation was
	// given against, so a new engine build — with a different device list — or a data directory
	// copied onto other hardware asks again instead of inheriting an answer for a machine that
	// was never shown the row.
	EngineDeviceAck string `key:"engineDeviceAck" group:"servers" label:"Device choice confirmed" help:"Written by the setup screen when the device row is confirmed. Blank means nobody has been asked yet." mode:"managed" noview:"true"`

	ChatURL        string `key:"chatURL" group:"servers" label:"Chat server" help:"Text and image input." kind:"url" required:"true" mode:"external"`
	EmbedURL       string `key:"embedURL" group:"servers" label:"Embedding server" help:"Multimodal embeddings for text and page images." kind:"url" required:"true" mode:"external"`
	TimeoutSeconds int    `key:"timeoutSeconds" group:"servers" label:"Request timeout" help:"Local CPU inference is slow. Lower it for GPU." min:"5" max:"3600" unit:"s"`

	// ── models ───────────────────────────────────────────────────
	// Model names are what the server answers to. They are settings in external mode only:
	// managed mode's names are the aliases in the preset libris writes, derived by
	// internal/serving, so these fields are never read there and are not typed anywhere; in
	// external mode they are
	// what the user's own llama-server was started with. Libris ships no guesses either
	// way: a default that names a model nobody downloaded is a field that looks correct and
	// fails anyway. Capacity is not here either — n_ctx and the GPU layer count belong to
	// the server's command line, and in managed mode to its preset file.
	ChatModel  string `key:"chatModel" group:"models" label:"Chat model" help:"The name your chat server answers to. Must support tool calls for the research loop." mode:"external"`
	EmbedModel string `key:"embedModel" group:"models" label:"Embedding model" help:"The name your embedding server answers to." mode:"external"`
	EmbedDim   string `key:"embedDimensions" group:"models" label:"Embedding dimensions" help:"Probed once at startup and recorded with every segment." readonly:"true"`

	// ── retrieval ────────────────────────────────────────────────
	// One budget, counted across every kind of chunk: a search's text, a PDF's page
	// rasters and a picture all come out of the same number, and the answer turn is
	// shown the same number of chunks. Two depths sat next to each other here and read
	// as two knobs for one thing; the deeper one is derived instead — see
	// retrieve.recallPool, which says why it cannot simply equal this.
	//
	// The key stayed "finalTopK" when the second k went away: a saved settings file
	// holds that name, and renaming it would reset whatever was saved.
	TopK          int `key:"finalTopK" group:"retrieval" label:"Chunks per search" help:"How many chunks one search returns. Every kind counts: text, page images and pictures share this budget." min:"1" max:"50" unit:"chunks"`
	MaxIterations int `key:"maxIterations" group:"retrieval" label:"Max iterations" help:"Deeper multi-hop research; diminishing returns past 3." min:"1" max:"10"`

	// The research turn's only output is a tool call, so what a model wrote to itself
	// before naming that search is thrown away the moment the call lands — paid for in
	// seconds on every iteration, read by nobody. The answer turn is not in this switch
	// because its deliberation is shown to the reader, which is why there is one setting
	// here and not two. Sent explicitly rather than left to the server's default: a switch
	// that works only when llama.cpp was started the same way is not a switch.
	ResearchThinking bool `key:"researchThinking" group:"retrieval" label:"Think before searching" help:"Off sends the search request straight away. On lets the model reason first, which costs time each iteration and sometimes picks a better search. The answer's own thinking is unaffected."`
	HistoryMessages  int  `key:"historyMessages" group:"retrieval" label:"History window" help:"Recent turns given to the synthesizer." min:"0" max:"64" unit:"messages"`
	ChunkWords       int  `key:"chunkWords" group:"retrieval" label:"Chunk size" help:"Target words per text chunk." min:"10" max:"1000" unit:"words"`
	ChunkOverlap     int  `key:"chunkOverlap" group:"retrieval" label:"Chunk overlap" help:"Words carried into the next chunk." min:"0" max:"999" unit:"words"`

	// A recording is cut into clips the way text is cut into chunks, and the same two
	// questions apply: how big, and how much the neighbours share. The units differ
	// because a clip has no words to count.
	AudioClipSeconds int `key:"audioClipSeconds" group:"retrieval" label:"Recording clip length" help:"Seconds of a recording per clip. Longer is worse: a clip's vector is the average of everything in it, so one sentence in a long clip is diluted by the rest of it. Measured on real recordings, 15 s is the shortest window that still holds a whole sentence, and it beat 30 s and 60 s on every query." min:"5" max:"60" unit:"seconds"`
	AudioClipOverlap int `key:"audioClipOverlap" group:"retrieval" label:"Recording clip overlap" help:"Percent of each clip repeated at the start of the next, so a sentence that straddles a boundary survives whole in at least one clip." min:"0" max:"95" unit:"%"`

	// ── ingest ───────────────────────────────────────────────────
	PDFMode   PDFMode `key:"pdfPageImages" group:"ingest" label:"PDF embeddings" help:"A PDF becomes two documents: page text and page rasters." kind:"enum" enum:"pdfModes"`
	PDFDPI    int     `key:"pdfDPI" group:"ingest" label:"PDF raster DPI" help:"Resolution for page images. Only affects how legible a cited page looks: the embedding cost per image is fixed." min:"50" max:"600" unit:"dpi"`
	MaxFileMB int     `key:"maxFileMB" group:"ingest" label:"Max file size" help:"Larger files are refused at the door, not mid-job." min:"1" max:"102400" unit:"MB"`
}

// Group titles and copy. Presentation only; the fields themselves are above.
func groups() []struct{ ID, Label, Help string } {
	return []struct{ ID, Label, Help string }{
		{"servers", "Model servers", "Either libris runs llama.cpp for you, or it talks to servers you started. The switch decides which of these fields exist — libris does not ask for an address it is about to compute."},
		{"models", "Models", "The model that answers, and the one that finds. Changing the embedder re-indexes every source."},
		{"retrieval", "Retrieval", "Research loop budget. Every extra iteration costs one search round trip."},
		{"ingest", "Ingest", "Extraction is a background job. An original is kept until no notebook uses it."},
	}
}

func Default() Config {
	return Config{
		// Managed is the default because it is the answer that needs nothing typed: libris
		// fetches the build, runs it on the port below, and the addresses are derived. The
		// two URLs and two names still have defaults, because external mode is a real
		// mode and a saved config that validates in one mode must validate in the other.
		EngineMode: EngineManaged,
		// Auto. A fixed default was the collision: 8080 is the port every local dev server
		// reaches for first, and libris's own settings page could not tell you which one won.
		EnginePort: 0,

		ChatURL:        "http://127.0.0.1:8081",
		EmbedURL:       "http://127.0.0.1:8082",
		TimeoutSeconds: 300,

		ChatModel:  "",
		EmbedModel: "",
		EmbedDim:   "",

		TopK:          10,
		MaxIterations: 5,
		// Off, and worth writing down rather than inheriting from the zero value: the
		// thinking a research turn does is the one thinking nobody sees.
		ResearchThinking: false,
		HistoryMessages:  8,
		ChunkWords:       100,
		ChunkOverlap:     50,
		AudioClipSeconds: 15,
		AudioClipOverlap: 50,

		PDFMode:   PDFModeAlways,
		PDFDPI:    110,
		MaxFileMB: 512,
	}
}

// intRange checks an int field against the min/max its tags declare, so a range is declared
// once and the generated form and the API cannot disagree about it.
//
// autoZero:"true" is the one exception the reflection understands: zero is a legal value that
// means "choose for me", which lets a field keep a floor that excludes the privileged ports
// while still allowing the value that turns choosing off.
func intRange(key string, v int64, f reflect.StructField) error {
	if s := f.Tag.Get("min"); s != "" && !(v == 0 && f.Tag.Get("autoZero") == "true") {
		if min, _ := strconv.ParseInt(s, 10, 64); v < min {
			return fmt.Errorf("config: %s = %d, below minimum %d", key, v, min)
		}
	}
	if s := f.Tag.Get("max"); s != "" {
		if max, _ := strconv.ParseInt(s, 10, 64); v > max {
			return fmt.Errorf("config: %s = %d, above maximum %d", key, v, max)
		}
	}
	return nil
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
			if err := intRange(key, fv.Int(), f); err != nil {
				return err
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
