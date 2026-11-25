// Package tools resolves the programs libris runs but did not write.
//
// Two places can hold them, and which one wins is a fact about the operating system rather
// than a preference:
//
//   - The download carries a folder of programs beside the libris executable. That folder is
//     what makes the download enough on its own: nobody is asked to install a program before
//     a PDF will open.
//   - PATH holds whatever the machine's owner installed. On a source checkout it is the only
//     place there is.
//
// Windows takes the bundle first, because a poppler found through PATH there usually came
// from MSYS or Git for Windows: it cannot read the Windows path libris hands it, and it needs
// a DLL folder of its own to start at all. So "the machine has pdftotext" is often false in
// practice, and checking it first would break a working download.
//
// Linux takes the machine's own first, because poppler's data folder — the CMaps a Chinese or
// Japanese PDF needs in order to extract to anything — is a path compiled into the library at
// build time on that platform (POPPLER_DATADIR; on Windows the same path is computed from the
// executable instead). A copied libpoppler therefore cannot be made to find a data folder
// beside it, wherever the download was unpacked, while the system library finds its own. So no
// unreachable data is shipped, and the bundle on Linux gives up the CMaps: page images and
// every PDF whose text does not need them are unaffected. Building poppler from source is the
// only way to close that, and it is deliberately not part of a release pipeline yet.
package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Origin is where a program came from, which is the only way a person reading "pdftotext is
// missing" can tell whether to install something or to go look for a folder.
type Origin string

const (
	OriginBundled Origin = "bundled" // in the folder that travels with libris
	OriginPath    Origin = "path"    // installed on this machine by somebody
	OriginMissing Origin = "missing" // neither
)

// Program is one thing libris can run: where it is, and what its child process needs in its
// environment to work. The environment is only part of a bundled program — one the machine
// installed already has its libraries — so it travels beside the answer rather than being
// guessed at by whoever runs it.
type Program struct {
	Name   string
	Path   string
	Origin Origin

	// LibDir is the folder of shared libraries copied beside a bundled program, empty
	// when there are none. Windows needs no such thing: it looks in the executable's own
	// directory before anywhere else.
	LibDir string

	// FontConfig is a font configuration written for this bundle, empty when the program
	// should use whatever the machine configures. A copied poppler renders a page's
	// non-embedded fonts through fontconfig, and on a machine that has no fontconfig
	// installed there is no configuration to find — so the folder of fonts that travels in
	// the download would sit there unread, and a page whose font is not embedded would
	// rasterise without the glyphs it names.
	FontConfig string
}

func (p Program) Found() bool { return p.Path != "" }

// Env returns base with this program's needs applied. A bundle's libraries go *before*
// whatever the machine already had, so the copied ones win — that is the point of copying
// them. An existing variable for the same key is replaced rather than duplicated, because an
// environment with the key twice resolves to whichever the child happens to read first, and
// that is not a thing to leave to chance.
// There is no platform branch here, and that is not an omission: LibDir and FontConfig are
// only ever filled in on the platforms that need them, because Windows resolves a DLL from the
// executable's own directory and finds fonts beside its library without being told.
func (p Program) Env(base []string) []string {
	add := map[string]string{}
	if p.LibDir != "" {
		// The copied libraries go *before* whatever the machine already had, so the copied
		// ones win — which is the point of copying them at all.
		value := p.LibDir
		if existing := envValue(base, "LD_LIBRARY_PATH"); existing != "" {
			value += string(os.PathListSeparator) + existing
		}
		add["LD_LIBRARY_PATH"] = value
	}
	if p.FontConfig != "" {
		add["FONTCONFIG_FILE"] = p.FontConfig
	}
	if len(add) == 0 {
		return base
	}

	// A key present twice resolves to whichever copy the child's libc happens to read first,
	// so an existing one is replaced rather than appended to.
	out := make([]string, 0, len(base)+len(add))
	for _, kv := range base {
		if _, dup := add[strings.SplitN(kv, "=", 2)[0]]; !dup {
			out = append(out, kv)
		}
	}
	for _, key := range []string{"LD_LIBRARY_PATH", "FONTCONFIG_FILE"} {
		if v, ok := add[key]; ok {
			out = append(out, key+"="+v)
		}
	}
	return out
}

func envValue(env []string, name string) string {
	prefix := name + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return env[i][len(prefix):]
		}
	}
	return ""
}

// Resolver looks programs up in the two places, in the order this operating system wants
// them. The folder may not exist — a source checkout has no bundle — and that is a normal
// answer rather than an error, so nothing here fails on it.
//
// A Resolver is worth using once per job rather than once per process: which folder is beside
// the executable can change (a download moved on disk), and the answer the settings page shows
// should be the answer for now.
type Resolver struct {
	dir   string
	first Origin
}

// Bundled resolves against the folder of programs that travels beside the libris executable.
// dir is that folder, normally <executable's directory>/tools. Which of the two places is
// checked first is not this package's caller's choice to make — it is what the operating
// system does to a copied program, and it is argued in the package comment.
func Bundled(dir string) Resolver {
	first := OriginPath
	if runtime.GOOS == "windows" {
		first = OriginBundled
	}
	return Resolver{dir: dir, first: first}
}

// FromPath resolves through PATH alone: the build that was `go run`, which has no folder of
// programs beside it because it has the machine's package manager instead.
func FromPath() Resolver {
	return Resolver{first: OriginPath}
}

// BesideExecutable is the resolver for a libris run from wherever it was downloaded: the
// folder of programs is named by the executable's own location, <exe>/tools, so one download
// works unpacked anywhere and two copies on one disk each carry their own.
//
// Symlinks are followed. A launcher installed as a link in a bin directory is the case where
// "beside the executable" has to mean beside the thing the link points at — a folder of
// programs cannot live next to a link somebody made in their PATH.
func BesideExecutable() (Resolver, error) {
	exe, err := os.Executable()
	if err != nil {
		return Resolver{}, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return Bundled(filepath.Join(filepath.Dir(exe), "tools")), nil
}

// Dir is the bundle's folder, empty when this build has none.
func (r Resolver) Dir() string { return r.dir }

// BundledHere says whether this install brought its own folder of programs. It is the fact a
// reader needs before "install it" is offered as advice: on a machine whose programs came from
// a package manager that sentence is true, and on a machine that downloaded everything it sends
// the person looking for something that was never theirs to install.
func (r Resolver) BundledHere() bool {
	if r.dir == "" {
		return false
	}
	info, err := os.Stat(r.dir)
	return err == nil && info.IsDir()
}

// Look finds one program by name. The name is asked for twice, once in each place, and the
// first place that has it answers.
func (r Resolver) Look(name string) Program {
	first := r.first
	if first == "" {
		// A Resolver nobody configured has no folder of programs, which is the PATH-only
		// answer rather than a hole in the lookup.
		first = OriginPath
	}
	second := OriginPath
	if first == OriginPath {
		second = OriginBundled
	}
	for _, origin := range []Origin{first, second} {
		switch origin {
		case OriginBundled:
			if p, ok := r.bundled(name); ok {
				return p
			}
		case OriginPath:
			if found, err := exec.LookPath(name); err == nil {
				return Program{Name: name, Path: found, Origin: OriginPath}
			}
		}
	}
	return Program{Name: name, Origin: OriginMissing}
}

func (r Resolver) bundled(name string) (Program, bool) {
	if r.dir == "" {
		return Program{}, false
	}
	path := filepath.Join(r.dir, name+exeExt())
	if !regularFile(path) {
		return Program{}, false
	}
	return Program{
		Name:       name,
		Path:       path,
		Origin:     OriginBundled,
		LibDir:     r.libDir(),
		FontConfig: r.fontConfig(),
	}, true
}

func (r Resolver) libDir() string {
	if runtime.GOOS == "windows" {
		// Windows resolves a DLL from the executable's own directory first, so the
		// libraries travel inside the bundle without being named to anybody.
		return ""
	}
	if dir := filepath.Join(r.dir, "lib"); hasFiles(dir) {
		return dir
	}
	return ""
}

// fontConfig is the configuration written for this bundle, if the bundle carries one.
func (r Resolver) fontConfig() string {
	if runtime.GOOS == "windows" {
		// Windows finds fonts through poppler's own folder beside the library, and the
		// machine's font registry besides; nothing here has to point at them.
		return ""
	}
	if path := filepath.Join(r.dir, "fonts-conf", "fonts.conf"); regularFile(path) {
		return path
	}
	return ""
}

// PDF names the two programs reading a PDF needs: one for the page text, one for the page
// rasters. They are resolved together because every PDF needs both, and because the second
// one's absence is worth reporting beside the first.
func (r Resolver) PDF() PDF {
	return PDF{Text: r.Look("pdftotext"), Raster: r.Look("pdftoppm")}
}

// PDF is the pair.
type PDF struct {
	Text   Program
	Raster Program
}

// All names every program libris runs that a person might have to care about: the two that
// read a PDF. The programs run for libris's own convenience — opening a browser, asking ps
// which processes are llama-server — are deliberately not here. They degrade on their own and
// their absence is not something anyone could go and fix.
func (r Resolver) All() []Program {
	pdf := r.PDF()
	return []Program{pdf.Text, pdf.Raster}
}
