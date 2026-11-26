package engine

// The router's configuration file: what libris writes, and what llama.cpp reads.
//
// Write (Preset) and read (ReadPreset) belong together, and the pair is what the supervisor asks
// for at every start: write the file from what libris derives, read it back, serve what the file
// says. Whether a write could be skipped is no longer a question anyone gets to ask, and
// refreshPreset says why not.
//
// Why the configuration is a file at all, and where it lives, is PresetsDir's and Preset's own
// comments — said once, there.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cacheRAMMiB is the ceiling on how much of the KV cache a model may keep in host memory. A
// ceiling and not a target: what does not fit is recomputed rather than lost, so the number is
// a trade and not a requirement.
//
// The chat model gets one because its context is long and reloading that context is the slowest
// thing a question can pay for. It gets a modest one because host RAM is not libris's alone: it
// is what the browser, the ingest queue and a page raster at full resolution are also asking
// for. The two small models are told to hold none, because an embedding pass does not reuse a
// cache and any of it would be memory the chat model cannot use.
const (
	chatCacheRAMMiB  = 512
	smallCacheRAMMiB = 0
)

// RouterID is the name libris gives the single llama-server it runs. It is also
// the name of the preset file it reads, so the process, the file and the log say the same
// thing about the same thing.
const RouterID = "libris"

// PresetsDir is where the model server's configuration lives: the data directory, beside the
// database and the models — deliberately NOT beside the binary.
//
// The engine directory is what the "Remove" button deletes, and this file describes the models
// and the device rather than the program that reads them — a file naming
// <data>/models/gemma-4-E2B-it-qat-UD-Q4_K_XL.gguf does not belong under a tree a button can
// make go away, and it would have to sit there for removing the engine to also delete the record
// of what was last served.
// So it is a sibling of the engine, and "where is my model server's configuration" keeps one
// answer for every file libris keeps.
func PresetsDir(dataDir string) string { return filepath.Join(dataDir, "presets") }

// PresetPath is the file one router process reads.
func PresetPath(dataDir, id string) string {
	return filepath.Join(PresetsDir(dataDir), id+".ini")
}

// Preset writes the INI file the router reads. Its shape is the one upstream documents
// (tools/server/README.md, "Model presets"): a version line, a [*] section shared by every
// model, then one section per model whose keys are command-line arguments without the dashes.
//
// The globals are the ones a local notebook wants: flash attention on, jinja templates so a
// chat template is not guessed, no warmup (the first question is the user's, not a benchmark),
// sampling done in the backend rather than in Go, and -fit left on so llama.cpp chooses the
// arguments that fit the memory it finds rather than libris claiming numbers it cannot
// measure — which is also why the device report stops at reporting.
//
// The ubatch is written because the default is too small for media, and this is the one
// global in here that fixes something that was already broken: at llama.cpp's 512 a page
// raster is shrunk to 256 tokens of resolution with a log line saying so, and a clip over
// about twenty seconds is refused outright. See serve.go, where the number and both of those
// messages from the pinned build live together.
//
// Nothing is written for slots, and the absence is the choice. llama-server's default for a
// model instance is auto, and auto means 4 slots *and* one KV buffer shared across them
// ("n_parallel is set to auto, using n_parallel = 4 and kv_unified = true", tools/server/
// server.cpp). A count of 1 gives continuous batching — which is on by default — exactly one
// task to schedule, so the eight chunks one embeddings request queues ran one after another.
// Writing 4 outright would keep the slots and lose the shared buffer, because that is only
// implied by auto: naming the key hands each slot ctx-size/4 of context, and a page raster is
// not a 100-word chunk. So the key stays out, and serve_test.go asserts that it is.
//
// device is the one global that varies by machine, and it is not a preference: the value is a
// slot name from that build's --list-devices, and a name it does not know is a parse error
// that stops the server from starting at all (measured on the build libris downloads:
// `error while handling argument "--device": invalid device: CUDA0`, then exit). So the value
// arrives from app, which resolves it against the live report; an empty string here means libris
// says nothing and llama.cpp decides for itself.
//
// reasoning-preserve is written out although its default is already "enabled": the chat model
// libris downloads is a reasoning model, the synthesis call is given the real message history,
// and whether the thinking in that history survives to the next turn is a fact about answers a
// reader may one day ask about. Explicit beats inherited when the inheritance is a default
// somebody else can change.
//
// Per-kind choices are not style: an embedding model needs --embeddings, and an embedder is
// not a chat model whatever it is asked.
//
// Pooling is written out, and today it says what the model already says: the GGUF carries
// gemma-embedding2.pooling_type = 1, which is MEAN (include/llama.h, llama_pooling_type), and
// --pooling's help is "use model default if unspecified" (common/arg.cpp, LLAMA_ARG_POOLING).
// So this is inert — measured on this embedder with and without the key, the vector is the same
// 768 floats — and the reason to write it anyway is that a stored vector's pooling becomes
// readable in the one file that says what the server was told, instead of a fact buried in a
// GGUF nobody opens. That the OAI route answers at all is the proof the file's own value is what
// was in force: llama.cpp refuses /v1/embeddings outright for pooling "none".
//
// The obligation that comes with it, for whoever adds the next embedder: this is one key written
// for every model of kind embed, and libris does not read pooling out of a GGUF. For a model
// that pools another way, the line stops being a restatement and becomes an override — and an
// embedder pooled the wrong way still returns numbers of the right length, so nothing fails and
// retrieval simply gets worse. At that point pooling moves to per-model data or comes back out.
//
// The file is what the router is told, in full, every time it starts: libris writes it from what
// it derives — which models exist, where their files are, which device to offload to, and what
// each kind is given — and nothing is carried over from the previous file. So a number changed by
// hand here is what the server runs until the next start, and not after it. That is the trade,
// made on purpose: the file can never be behind the program, so an installed machine cannot be
// left serving a configuration no build writes any more. It is still a file rather than flags
// assembled at a call site because llama.cpp reads a file, and because this stays the one place
// on a machine where nothing else about the model server is configurable at which to read what
// was actually passed.
func Preset(path string, device string, models []Model) error {
	var b strings.Builder
	b.WriteString("; The model server's own configuration, in the libris data directory.\n")
	b.WriteString("; llama-server reads this file. libris rewrites it from its own defaults at every\n")
	b.WriteString("; start, so a number changed here is what the server runs until the next start, and\n")
	b.WriteString("; not after it.\n")
	b.WriteString("; A name llama.cpp does not recognise here stops the server from starting at all.\n")
	b.WriteString("; Capacity and sampling belong to the server, not to a request.\n")
	b.WriteString("; load-on-startup is off: the router comes up empty and loads a model when a request\n")
	b.WriteString("; asks for one, so opening libris never pays for a model a question may never need.\n")
	b.WriteString("version = 1\n\n[*]\n")
	b.WriteString("flash-attn = on\n")
	b.WriteString("jinja = true\n")
	b.WriteString("no-warmup = true\n")
	b.WriteString("backend-sampling = true\n")
	b.WriteString("fit = on\n")
	fmt.Fprintf(&b, "ubatch-size = %d\n", ubatchSize)
	fmt.Fprintf(&b, "cache-ram = %d\n", chatCacheRAMMiB)
	if device != "" {
		fmt.Fprintf(&b, "device = %s\n", device)
	}
	b.WriteString("reasoning-preserve = true\n\n")

	for _, m := range models {
		if m.Path == "" {
			continue
		}
		fmt.Fprintf(&b, "[%s]\n", m.Alias)
		fmt.Fprintf(&b, "model = %s\n", m.Path)
		if m.MMProj != "" {
			fmt.Fprintf(&b, "mmproj = %s\n", m.MMProj)
		}
		// Spelled out rather than left out, because the two mean different things: no device key
		// lets llama.cpp choose, and what it chooses is the fastest device it can find. A section
		// that exists precisely so that it does not touch the card has to say so.
		if m.Device != "" {
			fmt.Fprintf(&b, "device = %s\n", m.Device)
		}
		switch m.Kind {
		case KindEmbed:
			fmt.Fprintf(&b, "embeddings = on\npooling = mean\nctx-size = %d\ncache-ram = %d\n", embedCtx, smallCacheRAMMiB)
		default:
			fmt.Fprintf(&b, "ctx-size = %d\n", chatCtx)
			// A quantised KV cache, and only on the model that can pay it back. The chat
			// model's context is 32 768 tokens of history that gets re-read every turn, so
			// q8_0 there is memory the card can use for something else. The embedder is left
			// at llama.cpp's own f16: an embedding pass holds its context for the length of
			// one request and never reloads one, so there is nothing here to save — and the
			// one thing a lossy cache could change, for a model whose entire output is the
			// vector a search then compares, is the numbers that get stored. Like cache-ram,
			// this is a key that is written per model rather than shared, and for the same
			// reason: what one model wants the KV cache for is not what another wants it for.
			b.WriteString("cache-type-k = q8_0\n")
			b.WriteString("cache-type-v = q8_0\n")
			fmt.Fprintf(&b, "temp = %s\ntop-k = %s\ntop-p = %s\n", chatTemp, chatTopK, chatTopP)
			// Speculative decoding, and it is per-section because the draft is a file belonging
			// to one model: an MTP layer is trained against the network it drafts for, so a
			// section without a draft has nothing to guess with and asks for nothing.
			//
			// draft-mtp rather than the default lookup-and-draft is what the file is: a model
			// file named mtp-*.gguf, published beside the weights for exactly this purpose.
			// Each of these five keys resolves inside the server's argument set — three name
			// themselves as server arguments, and the two cache types inherit them from
			// llama.cpp's common set (common/arg.cpp, add_opt's example filter) — which matters
			// because a key the server does not know is not ignored: the process exits.
			if m.Draft != "" {
				b.WriteString("spec-type = draft-mtp\n")
				fmt.Fprintf(&b, "spec-draft-model = %s\n", m.Draft)
				fmt.Fprintf(&b, "spec-draft-n-max = %s\nspec-draft-type-k = %s\nspec-draft-type-v = %s\n",
					specDraftNMax, specDraftType, specDraftType)
			}
		}
		// Off, and written as `false` rather than left out, so the choice is visible to whoever
		// opens this file (the same reason `reasoning-preserve` is written out below its own
		// default). `load-on-startup` is a preset-only key read back with llama.cpp's truthiness,
		// which is a whitelist of on|enabled|true|1 (common/arg.cpp, is_truthy) — so `false` is
		// genuinely false, and so is any spelling upstream stops understanding.
		//
		// Empty at start is what makes opening libris cheap, and it is not a risk: the router
		// loads a model on the request that names it (`--models-autoload`, default enabled — not
		// passed, so not turned off), reports `GET /models` before anything has loaded, and
		// `--models-max` still caps how many may be resident at once. The chat model's weights
		// alone are 2.6 GB and most questions ask for one model; loading both at boot was a startup
		// cost — and on a 4 GB card, the two of them plus the host KV cache they were allowed were a
		// measurement neither model survives.
		b.WriteString("load-on-startup = false\n\n")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("engine: preset dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("engine: write preset: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("engine: replace preset: %w", err)
	}
	return nil
}

// PresetFile is what the file on disk says, read back after it has been written. Only the parts
// the caller reports are kept: the global device, and each section's name and files. Everything
// else in the file is passed to llama.cpp untouched, because this program is not the one running
// the model server.
type PresetFile struct {
	Device string
	Models []Model
}

// ReadPreset reads what the file says. A file that is not there reads as nil rather than an
// error, so asking what a file says never has to interpret a filesystem; a file that cannot be
// read at all is an error, because it is there and its contents are unknown.
func ReadPreset(path string) (*PresetFile, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("engine: read the model server's config: %w", err)
	}

	var (
		out     PresetFile
		section string
		cur     *Model
	)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section != "*" {
				out.Models = append(out.Models, Model{Alias: Alias(section)})
				cur = &out.Models[len(out.Models)-1]
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case section == "" || section == "*":
			if key == "device" {
				out.Device = value
			}
		case cur != nil:
			observeKey(cur, key, value)
		}
	}

	// A section that names a model and asks for no embeddings is a chat model: that is what the
	// absence of the flag means to llama.cpp, and a section a person added by hand is otherwise
	// kindless here even though the server is busy treating it as one.
	for i := range out.Models {
		if out.Models[i].Path != "" && out.Models[i].Kind == "" {
			out.Models[i].Kind = KindChat
		}
	}
	return &out, nil
}

// observeKey records the keys libris cares about inside a model section. Everything else
// in the file is a key for llama.cpp and is left alone — that is what "the file is the
// configuration" means in code: this package reads the parts it must agree about and passes the
// rest through untouched.
func observeKey(cur *Model, key, value string) {
	switch key {
	case "model":
		cur.Path = value
	case "mmproj":
		cur.MMProj = value
	case "spec-draft-model":
		cur.Draft = value
	case "embeddings":
		if truthy(value) {
			cur.Kind = KindEmbed
		}
	case "device":
		cur.Device = value
	}
}

// truthy is llama.cpp's own rule for a flag key: is_truthy in common/arg.cpp accepts a handful
// of spellings, and a preset that says `embeddings = yes` means embeddings.
func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

// refreshPreset writes the preset from what libris derives and answers with what the file now
// says — which is what the router is about to serve, so the caller reports the file rather than
// what it meant to write.
//
// It writes every start. The earlier rule read the file first and wrote only when one of three
// things had moved, and what that bought was a hand-edited number surviving. What it cost was
// invisible: the comparison that made skipping possible compared only what libris *reports* —
// which models, where their files are, which device — so a change to anything libris *writes*, a
// context size or the default number of slots, never reached a machine that had started once
// already. It could not, because the rule for noticing was that same narrow rule, and the numbers
// were not in it. Such a machine serves an old server configuration indefinitely, and the only
// sign is that its answer is slower than the one beside it.
//
// So the file is rewritten, and a hand edit lasts until the next start. Preset's comment holds
// the other half: why this is a file at all, and what reading it is still good for.
func refreshPreset(path, device string, models []Model) (*PresetFile, error) {
	if err := Preset(path, device, models); err != nil {
		return nil, err
	}
	// Read back rather than trusting what was just built in memory: what the router is about to
	// serve is what the file says, and this way the two cannot drift.
	have, err := ReadPreset(path)
	if err != nil {
		return nil, err
	}
	if have == nil {
		return nil, fmt.Errorf("engine: the preset %s was written and is not there to read back", path)
	}
	return have, nil
}
