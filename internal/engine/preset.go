package engine

// The router's configuration file: what libris writes, what llama.cpp reads, and the one
// question the supervisor is allowed to ask about it.
//
// Write (Preset), read (ReadPreset), and the comparison the two imply (refreshPreset) belong
// together, and were split across the supervisor: the supervisor held the rule about when a
// format needs rewriting, which is the one question it should not have been asking. Why the
// configuration is a file at all is PresetsDir's and Preset's own comments — said once, there.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cacheRAMMiB is how much of the KV cache a model may hold in host memory. The chat model gets
// a real allowance because its context is long and a reload of it is the slowest thing a
// question can pay for; the two small models are told to hold none, because an embedding pass
// does not reuse a cache and 2 GiB of it would be memory the chat model cannot use.
const (
	chatCacheRAMMiB  = 2048
	smallCacheRAMMiB = 0
)

// RouterID is the name libris gives the single llama-server it runs. It is also
// the name of the preset file it reads, so the process, the file and the log say the same
// thing about the same thing.
const RouterID = "libris"

// PresetsDir is where the model server's configuration lives: the data directory, beside the
// database and the models — deliberately NOT beside the binary.
//
// The file is the only place a model server's behaviour exists, and the binary that
// reads it can be removed and re-fetched by a button. A hand-edited cache-ram that vanished
// with the engine would be a setting the user did not change, which is exactly what this
// arrangement refuses. So the configuration outlives the engine by construction, and "where is my
// configuration" has one answer for every file libris keeps.
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
// Pooling is deliberately absent. --pooling's own help says "use model default if unspecified"
// (common/arg.cpp, LLAMA_ARG_POOLING), and the default is read out of the GGUF — so naming one
// here would override the answer that shipped inside the file, and an embedder pooled the wrong
// way still returns numbers of the right length. That is the quiet kind of retrieval bug:
// nothing fails, the vectors are just wrong. Measured on the catalogue's own embedder, with and
// without `pooling = mean`, the vectors are identical (one row of 768, the same twelve hex
// digits of a digest), which is the answer that let this line stay absent.
// The file is the configuration. libris writes it once, and afterwards only when what it
// derives has actually changed — which models exist, where their files are, and which device
// to offload to. A number edited by hand in the meantime (cache-ram, ctx-size, a quantisation
// someone wants to try against the pinned one) survives until one of those three moves, which
// is the whole reason it is a file in the data directory rather than flags assembled at a call
// site: it is the one place the server's own behaviour can be read and changed on a machine
// where nothing else about llama.cpp is configurable.
func Preset(path string, device string, models []Model) error {
	var b strings.Builder
	b.WriteString("; The model server's own configuration, in the libris data directory.\n")
	b.WriteString("; llama-server reads this file; libris rewrites it only when the models or the device\n")
	b.WriteString("; it derives have changed, so an edit to a number here survives until one of them moves.\n")
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
	b.WriteString("parallel = 1\n")
	b.WriteString("cache-type-k = q8_0\n")
	b.WriteString("cache-type-v = q8_0\n")
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
		switch m.Kind {
		case KindEmbed:
			fmt.Fprintf(&b, "embeddings = on\nctx-size = %d\ncache-ram = %d\n", embedCtx, smallCacheRAMMiB)
		default:
			fmt.Fprintf(&b, "ctx-size = %d\n", chatCtx)
			b.WriteString("temp = 0.1\ntop-k = 50\ntop-p = 1.0\nmin-p = 0.0\nrepeat-penalty = 1.1\n")
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
		// and `--models-max` still caps how many may be resident at once. The chat
		// model is 1.3 GB and most questions ask for one model; loading both at boot was a
		// startup cost — and on a 4 GB card, the two of them plus a 2 GiB KV cache were a
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

// PresetFile is what the file on disk says, read back before the router is started. Only the
// parts libris compares against its own derivation are kept: the global device, and each
// section's name and files. Everything else in the file — every key libris writes and every key
// a person writes — is passed to llama.cpp untouched, because this program is not the one
// running the model server.
type PresetFile struct {
	Device string
	Models []Model
}

// ReadPreset reads the file the router is about to read. A file that is not there is not an
// error: it is the state a data directory is in before the first start, and the caller writes
// one. A file that cannot be read at all is an error rather than an absence, because overwriting
// it would delete an edit nobody has been told about.
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

// observeKey records the four keys libris cares about inside a model section. Everything else
// in the file is a key for llama.cpp and is left alone — that is what "the file is the
// configuration" means in code: this package reads the parts it must agree about and passes the
// rest through untouched.
func observeKey(cur *Model, key, value string) {
	switch key {
	case "model":
		cur.Path = value
	case "mmproj":
		cur.MMProj = value
	case "embeddings":
		if truthy(value) {
			cur.Kind = KindEmbed
		}
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

// Matches is whether the file already says what libris would write about the three things it
// derives: which models exist, which file each one is, and which device to offload to. Section
// order is not compared — rearranging the file changes nothing libris can care about — and
// neither is any other key, which is the point of the comparison being this narrow.
func (p *PresetFile) Matches(device string, models []Model) bool {
	if p == nil || p.Device != device {
		return false
	}
	want := map[string]Model{}
	for _, m := range models {
		if m.Path != "" {
			want[string(m.Alias)] = m
		}
	}
	if len(want) != len(p.Models) {
		return false
	}
	for _, have := range p.Models {
		got, ok := want[string(have.Alias)]
		if !ok || got.Path != have.Path || got.MMProj != have.MMProj {
			return false
		}
	}
	return true
}

// refreshPreset brings the file up to date with what libris derives, and answers with what the
// file now says — which is what the router is about to serve, so the caller reports this rather
// than what it hoped for.
//
// The rule, in one place: read what is there; write only when one of the three things libris
// derives has moved (which models exist, where their files are, which device to offload to);
// after writing, read the file back. Absent is not an error — that is a data directory before
// its first start. Unreadable is an error rather than an absence, because overwriting it would
// delete an edit nobody has been told about.
//
// Why the configuration is a file at all, and why a hand edit outranks a regeneration, is
// Preset's comment; this is only the part that decides when to write. Matches is narrow so that
// the answer to that question is rarely "now".
func refreshPreset(path, device string, models []Model) (*PresetFile, error) {
	have, err := ReadPreset(path)
	if err != nil {
		return nil, err
	}
	if have.Matches(device, models) {
		return have, nil
	}
	if err := Preset(path, device, models); err != nil {
		return nil, err
	}
	// Read back rather than trusting what was just built in memory: what the router is about to
	// serve is what the file says, and this way the two cannot drift.
	return ReadPreset(path)
}
