// Package models is the catalogue of what libris can run, and the record of which of it is
// on this disk.
//
// A catalogue entry is not a suggestion: it names one file, at one revision of one
// repository, with the digest upstream gives it. That is what makes a downloaded model the
// same model twice, which a glob over a directory never was — and it is why this package,
// not a settings field, decides which models exist at all. It does not read GGUF metadata,
// holds no settings, and does not start anything.
package models

import (
	"fmt"
	"os"
	"path/filepath"

	"libris/internal/engine"
)

// Dir is the folder under the data directory where the catalogue is kept. Each entry gets a
// subfolder of its own rather than sharing one flat directory, and the reason is
// mmproj-BF16.gguf: upstream names a projector after its *precision*, not its model, so two
// entries can want that exact filename and only one can have it. A shared directory would
// answer that collision by silently pairing a chat model with another model's vision tower —
// an answer that cites the wrong picture. One folder per entry makes the collision impossible
// and a removal one rmdir.
func Dir(dataDir string) string { return filepath.Join(dataDir, "models") }

// Role is what a model is for. The two are exactly the two roles the answer path has:
// the research loop's tool caller and the embedding probe.
type Role string

const (
	RoleChat  Role = "chat"
	RoleEmbed Role = "embed"
)

// Alias is the name this model is given in the router's preset, and therefore the name libris
// sends in the "model" field of a request. engine owns the type because the preset is
// engine's file format; the mapping lives here because which model fills a role is this
// package's decision.
func (r Role) Alias() engine.Alias {
	switch r {
	case RoleEmbed:
		return engine.AliasEmbed
	default:
		return engine.AliasChat
	}
}

// Part is one file: the name upstream gives it, how big it must be, and its digest.
//
// The digest is not paranoia about a corrupted byte. It is the only thing that says the file
// a person is running is the file the catalogue describes, and Hugging Face serves the same
// value itself (x-linked-etag) for files under LFS.
type Part struct {
	Name   string
	Bytes  int64
	SHA256 string
}

// Entry is one model in the catalogue: what it is called, what it is for, and the files that
// make it run.
type Entry struct {
	ID    string
	Role  Role
	Label string // what the settings page calls it
	Quant string // how the weights were squeezed, in upstream's own shorthand
	Note  string // why this one, in a sentence the page can show

	// Repo and Rev are the pinned source. Rev is a commit sha rather than "main" because
	// main moves, and a model that changes underneath a saved corpus silently invalidates
	// every vector in it (index/supplier.go compares dimensions, not meanings).
	Repo string
	Rev  string

	Model  Part
	MMProj Part // empty for a model with no vision tower

	// Draft is the extra model file that drives speculative decoding — the small pass whose
	// guess the real model verifies. It is a part rather than a setting because it is a
	// download: it needs the same pin, digest and resume as the weights, and an answer that
	// was guessed at by a file libris never verified is not the same answer.
	Draft Part // empty when nothing drafts for this model
}

// catalog is everything libris offers. Two models, one chat and one embedding, because that
// is what the answer path needs before it needs anything else, and a menu of twelve quants is
// a way of asking somebody to guess. Retrieval is ordered by the embedding model's own
// similarity and by nothing else, so a third model would have to earn its download.
var catalog = []Entry{
	{
		ID:    "gemma4-e2b",
		Role:  RoleChat,
		Label: "Gemma 4 E2B",
		Quant: "Q4_K_XL (UD, QAT)",
		Note:  "Chat and synthesis. Vision-language, so it reads a page raster as well as its text, and it ships its own draft layer, so answering costs fewer forward passes.",
		Repo:  "unsloth/gemma-4-E2B-it-qat-GGUF",
		Rev:   "66a399f68ddd113b06dff02fca9523e55465d11d",
		Model: Part{
			Name:   "gemma-4-E2B-it-qat-UD-Q4_K_XL.gguf",
			Bytes:  2620370976,
			SHA256: "e531007218dfab990486a5de7676a6932d6ea8dea233d1f698d7c21cf8a16889",
		},
		MMProj: Part{
			Name:   "mmproj-BF16.gguf",
			Bytes:  986833728,
			SHA256: "38b33846f56426cd650e0e574d78de125abdfcedf35c0d7f6929f6ffe26efe02",
		},
		Draft: Part{
			Name:   "mtp-gemma-4-E2B-it.gguf",
			Bytes:  59235648,
			SHA256: "586f2460b909008640981ec34060aa864e03c144fbabfb3173c4335087e4aae0",
		},
	},
	{
		ID:    "embeddinggemma-2",
		Role:  RoleEmbed,
		Label: "EmbeddingGemma 2",
		Quant: "Q4_K_XL (UD)",
		Note:  "Recall. Multimodal, so a page image is searchable by what it shows.",
		Repo:  "unsloth/embeddinggemma-2-GGUF",
		Rev:   "031f0d4b35536f69ab3509d4893c923264fcf253",
		Model: Part{
			Name:   "embeddinggemma-2-UD-Q4_K_XL.gguf",
			Bytes:  175673856,
			SHA256: "ea905fd08e8061db77a0031cbb5096d459d7cfd2bdd0ad2ba66e483f16719493",
		},
		MMProj: Part{
			Name:   "mmproj-BF16.gguf",
			Bytes:  982074880,
			SHA256: "995aaa56e88b9b631f651861d659b728a625ccc02a238be37cff56cf11dc0032",
		},
	},
}

// Catalog returns what libris offers, in the order a page should show them.
func Catalog() []Entry { return catalog }

// Look finds an entry by id. The id is what the API sends, so this is the one place that
// decides whether a request names a model that exists.
func Look(id string) (Entry, bool) {
	for _, e := range catalog {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Parts returns the files an entry needs, weights first, then whatever is only useful beside
// them — the order a download goes in, because the projector and the draft are useless without
// the model they belong to.
func (e Entry) Parts() []Part {
	parts := []Part{e.Model}
	if e.MMProj.Name != "" {
		parts = append(parts, e.MMProj)
	}
	if e.Draft.Name != "" {
		parts = append(parts, e.Draft)
	}
	return parts
}

// Bytes is everything the entry costs on disk.
func (e Entry) Bytes() int64 {
	var n int64
	for _, p := range e.Parts() {
		n += p.Bytes
	}
	return n
}

// URL is where one part is fetched from. resolve/<sha>/ rather than resolve/main/ because the
// revision is pinned; a file whose name is under the same commit is the file the digest was
// read from.
func (e Entry) URL(p Part) string {
	return "https://huggingface.co/" + e.Repo + "/resolve/" + e.Rev + "/" + p.Name
}

// Dir is the entry's folder inside a models directory.
func (e Entry) Dir(modelsDir string) string { return filepath.Join(modelsDir, e.ID) }

// Path is where one part belongs on disk.
func (e Entry) Path(modelsDir string, p Part) string {
	return filepath.Join(e.Dir(modelsDir), p.Name)
}

// Status is one catalogue entry as the machine knows it: which files are here, how much of
// anything is missing, and whether what is here is what libris verified.
type Status struct {
	Entry

	// Where this entry lives: its folder, and the two files inside it. Computed once, here,
	// so a page and a router reading the same list cannot disagree about a path.
	Folder     string `json:"folder"`
	Path       string `json:"path"`
	MMProjPath string `json:"mmprojPath,omitempty"`
	DraftPath  string `json:"draftPath,omitempty"`

	// Have and Missing are part names, so a page can say "the model is here, its projector is
	// not" rather than "incomplete".
	Have []string
	// Missing lists the parts that are absent or the wrong size.
	Missing []string
	// PartialBytes is what a stopped download is holding in this entry's folder.
	PartialBytes int64
	// Verified says every present file was written by libris and hashed on the way in. A
	// file a person copied here with the right name and size is present but unverified —
	// which is true and worth showing, because the alternative is claiming a digest libris
	// never computed. Hashing gigabytes to find that out is not a page load.
	Verified bool
	// Complete is true when every part is present at the right size.
	Complete bool
}

// StatusOf reports one entry for a given models directory. It stats; it never hashes.
func StatusOf(modelsDir string, e Entry) Status {
	st := Status{
		Entry:    e,
		Folder:   e.Dir(modelsDir),
		Path:     e.Path(modelsDir, e.Model),
		Verified: true,
	}
	// Only for parts that exist: a path built from an empty part name is the folder itself,
	// and a folder handed to the preset as a model file — or to a page as a file — is a lie
	// that only the router would notice, loudly, at start.
	if e.MMProj.Name != "" {
		st.MMProjPath = e.Path(modelsDir, e.MMProj)
	}
	if e.Draft.Name != "" {
		st.DraftPath = e.Path(modelsDir, e.Draft)
	}
	for _, p := range e.Parts() {
		path := e.Path(modelsDir, p)
		sz, part := statFile(path)
		switch {
		case sz == p.Bytes:
			st.Have = append(st.Have, p.Name)
			if _, err := os.Stat(path + verifiedSuffix); err != nil {
				st.Verified = false
			}
		case sz > 0 || part > 0:
			st.Missing = append(st.Missing, p.Name)
			st.PartialBytes += part
			st.Verified = false
		default:
			st.Missing = append(st.Missing, p.Name)
			st.Verified = false
		}
	}
	st.Complete = len(st.Missing) == 0
	return st
}

// Statuses reports the whole catalogue, in catalogue order. This is the one read of the disk
// a caller needs: everything below is a pure function over its result, so a page that shows
// the list and a router that starts from it cannot disagree about what is installed.
func Statuses(modelsDir string) []Status {
	out := make([]Status, 0, len(catalog))
	for _, e := range catalog {
		out = append(out, StatusOf(modelsDir, e))
	}
	return out
}

// verifiedSuffix marks a file libris wrote and hashed. It is a sibling rather than a field in
// a database because the data directory has to stay right when copied, moved, or emptied by
// hand — the same reason the engine keeps its own record beside the binaries.
const verifiedSuffix = ".libris-sha256"

// MarkVerified records that a file was written by libris and matched its digest.
func MarkVerified(path, sum string) error {
	return os.WriteFile(path+verifiedSuffix, []byte(sum+"\n"), 0o600)
}

// Models turns statuses into what the router needs: an alias, a path, and whatever belongs
// beside it. An incomplete entry is skipped rather than half-served, because a preset naming a
// missing file is a server that refuses to start, and the sentence llama.cpp prints is worse
// than the one this app can print.
func Models(statuses []Status) []engine.Model {
	var out []engine.Model
	for _, s := range statuses {
		if !s.Complete {
			continue
		}
		out = append(out, engine.Model{
			Alias:  s.Role.Alias(),
			Kind:   kindOf(s.Role),
			Path:   s.Path,
			MMProj: s.MMProjPath,
			Draft:  s.DraftPath,
		})
	}
	return out
}

func kindOf(r Role) engine.ModelKind {
	switch r {
	case RoleEmbed:
		return engine.KindEmbed
	default:
		return engine.KindChat
	}
}

// Delete removes an entry's whole folder, record files and partial bytes included. A folder
// that is not there is not an error: the button that asked has already got the state it wants.
func Delete(modelsDir string, e Entry) error {
	err := os.RemoveAll(e.Dir(modelsDir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// retiredDirs are folders an older build downloaded and this one cannot run: the model they
// hold is no longer in the catalogue, so nothing names those files any more, and they are
// gigabytes a person is paying rent for out of habit. Named by folder rather than by entry
// because an entry that is gone cannot be looked up.
//
// This is a migration, not a feature: it exists for machines that installed the model this
// build replaced, and the day it stops finding anything is the day it can come out. Until then
// it is cheaper than a release note asking somebody to hunt down a folder by hand, and much
// cheaper than the settings page showing a disk full of a model no screen can name.
var retiredDirs = []string{
	"lfm3-vl-2b",
}

// RemoveRetired deletes the folders of models this build no longer offers, and reports which
// names it actually removed so the caller can say so in a log line. A folder that is not there
// is the normal case, not an error.
func RemoveRetired(modelsDir string) ([]string, error) {
	var gone []string
	for _, name := range retiredDirs {
		dir := filepath.Join(modelsDir, name)
		if _, err := os.Stat(dir); err != nil {
			continue // nothing there, and nothing worth logging
		}
		if err := os.RemoveAll(dir); err != nil {
			return gone, fmt.Errorf("models: remove the retired %s folder: %w", name, err)
		}
		gone = append(gone, name)
	}
	return gone, nil
}

// statFile returns the size of a finished file and the size of its partial twin. A part file
// counts as nothing for "present" and everything for "how much room is this using".
func statFile(path string) (size, partial int64) {
	if st, err := os.Stat(path); err == nil {
		size = st.Size()
	}
	if st, err := os.Stat(PartPath(path)); err == nil {
		partial = st.Size()
	}
	return size, partial
}

// PartPath is where a download in progress lives, beside the file it will become. Keeping it
// beside its destination is what makes "how much room is this using" one stat.
func PartPath(final string) string { return final + ".part" }
