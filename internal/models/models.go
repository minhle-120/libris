// Package models knows which GGUF files are on this disk and what each one is for.
//
// It is deliberately small: a catalogue of filename patterns, a directory scan, and the
// pairing of a multimodal model with its projector. It does not download, does not read
// metadata, and holds no settings — the catalogue is the same one ADR 0020 records, in code
// rather than in prose, because a list that exists twice is two lists.
package models

import (
	"os"
	"path/filepath"
	"strings"

	"libris/internal/engine"
)

// Dir is where a user's own GGUFs go, and where a future downloader would put them.
func Dir(dataDir string) string { return filepath.Join(dataDir, "models") }

// Role is what a model is for. The three are exactly the three roles the answer path has:
// the research loop's tool caller, the embedding probe, and the reranker.
type Role string

const (
	RoleChat   Role = "chat"
	RoleEmbed  Role = "embed"
	RoleRerank Role = "rerank"
)

// pattern is one catalogue entry: the role it fills, and a case-insensitive glob the file
// name has to match. Globs rather than names so a quantisation or a build date in the name
// does not make a downloaded model invisible.
type pattern struct {
	role  Role
	glob  string
	alias engine.Alias
}

var catalogue = []pattern{
	{RoleChat, "qwen3.5-9b-uncensored-holo2.5*-q8_0.gguf", engine.AliasChat},
	{RoleEmbed, "embeddinggemma-300m-*it*.gguf", engine.AliasEmbed},
	{RoleRerank, "rerank-gemma-*it*.gguf", engine.AliasRerank},
}

// Roles lists what libris knows how to use, in the order a settings page would show them.
// Paired with Pattern, it lets the page say "no chat model yet — a file named
// Qwen3.5-9B…-Q8_0.gguf in <data>/models" instead of a bare "nothing found".
func Roles() []Role {
	out := make([]Role, 0, len(catalogue))
	for _, p := range catalogue {
		out = append(out, p.role)
	}
	return out
}

// Pattern returns the filename pattern libris looks for a role, or "" for a role it does not
// know.
func Pattern(role Role) string {
	for _, p := range catalogue {
		if p.role == role {
			return p.glob
		}
	}
	return ""
}

// Model is one GGUF on disk that libris knows what to do with.
type Model struct {
	Role  Role
	Alias engine.Alias
	Path  string
	Name  string
	Size  int64
	// MMProj is the vision projector paired with this model, empty when there is none. The
	// pairing is by filename prefix because that is the convention upstream publishes under
	// (…-Q8_0.gguf next to …-mmproj-F32.gguf) and there is no metadata that says it.
	MMProj string
}

// Scan lists the models in dir that the catalogue recognises. A file nobody recognises is
// left out on purpose: an unrecognised GGUF is not a model libris can name to a router, and
// a list containing it would be a promise the answer path cannot keep.
func Scan(dir string) []Model {
	paths := listGGUF(dir)
	var out []Model
	for _, p := range catalogue {
		for _, path := range paths {
			name := filepath.Base(path)
			ok, err := filepath.Match(p.glob, strings.ToLower(name))
			if err != nil || !ok {
				continue
			}
			m := Model{Role: p.role, Alias: p.alias, Path: path, Name: name, Size: sizeOf(path)}
			if proj := pairMMProj(paths, path); proj != "" {
				m.MMProj = proj
			}
			out = append(out, m)
			break // one file per role: the first match wins, and two chat models is a choice
		}
	}
	return out
}

// pairMMProj finds the projector that belongs to model. It looks for a sibling whose name is
// the model's stem plus "mmproj", which is the shape Hugging Face uploads come in.
func pairMMProj(paths []string, model string) string {
	stem := strings.ToLower(strings.TrimSuffix(filepath.Base(model), ".gguf"))
	cut := stem
	if i := strings.LastIndex(stem, "-"); i > 0 {
		cut = stem[:i]
	}
	for _, p := range paths {
		n := strings.ToLower(filepath.Base(p))
		if !strings.Contains(n, "mmproj") {
			continue
		}
		if strings.HasPrefix(n, cut) {
			return p
		}
	}
	return ""
}

func listGGUF(dir string) []string {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.gguf"))
	return paths
}

func sizeOf(path string) int64 {
	if st, err := os.Stat(path); err == nil {
		return st.Size()
	}
	return 0
}
