package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// unpack lays an archive out on disk.
//
// Both shapes arrive depending on the operating system: upstream ships .tar.gz for Linux
// and .zip for Windows, and the zip is the one that matters most because it is the one the
// person double-clicking a download gets.
//
// Three rules, each learned from the way archives go wrong:
//
//   - The archive's own root directory is stripped. Upstream puts everything under
//     llama-b12649/ — flat, one directory of binaries and shared objects, which is what a
//     real download showed and not what the first fixture assumed — and keeping it would
//     put the engine one directory deeper than libris looks.
//   - A member that would land outside the destination is refused, not cleaned. "Reject
//     unsafe paths rather than sanitise them" is the rule this repo already applies to
//     DirSink, and an archive is exactly as untrusted as a filename from a browser.
//   - The total expanded size is capped, so a crafted or mislabelled archive cannot fill
//     the Documents folder while the app is busy doing something else.
func (m *Manager) unpack(ctx context.Context, archive, dest, root string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("engine: %s: %w", dest, err)
	}
	ext := filepath.Ext(strings.TrimSuffix(archive, ".gz"))
	switch ext {
	case ".zip":
		return m.unpackZip(archive, dest, root)
	case ".tar":
		return m.unpackTar(archive, dest, root)
	default:
		return fmt.Errorf("engine: %s is not an archive libris knows how to open", filepath.Base(archive))
	}
}

// maxExpanded is generous: the full Vulkan tree is a few hundred MB, and nothing legitimate
// is within a factor of ten of this.
const maxExpanded = 4 << 30

// safeJoin resolves a member name against dest and refuses anything that escapes.
func safeJoin(dest, name string) (string, error) {
	name = filepath.ToSlash(name)
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return "", fmt.Errorf("engine: archive member %q is not a path libris will create", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || strings.Contains(part, ":") {
			return "", fmt.Errorf("engine: archive member %q is not a path libris will create", name)
		}
	}
	p := filepath.Join(dest, filepath.FromSlash(name))
	// The belt to the loop's braces: filepath.Join cleans, so a name that still escapes
	// after cleaning means the loop above has a hole in it.
	if p != dest && !strings.HasPrefix(p, dest+string(os.PathSeparator)) {
		return "", fmt.Errorf("engine: archive member %q would escape %s", name, dest)
	}
	return p, nil
}

func (m *Manager) unpackZip(archive, dest, root string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("engine: open %s: %w", filepath.Base(archive), err)
	}
	defer r.Close()

	var written int64
	for _, f := range r.File {
		name := stripRoot(f.Name, root)
		if name == "" {
			continue
		}
		p, err := safeJoin(dest, name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return fmt.Errorf("engine: %s: %w", p, err)
			}
			continue
		}
		if f.UncompressedSize64 > uint64(maxExpanded-written) {
			return fmt.Errorf("engine: %s expands past %s, which is more than libris will write",
				filepath.Base(archive), human(maxExpanded))
		}
		if err := writeZipFile(f, p); err != nil {
			return err
		}
		written += int64(f.UncompressedSize64)
	}
	return nil
}

func writeZipFile(f *zip.File, p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("engine: %s: %w", filepath.Dir(p), err)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("engine: read %s from the archive: %w", f.Name, err)
	}
	defer rc.Close()

	mode := f.Mode() & 0o777
	if mode == 0 {
		// A zip written on Windows carries no unix modes at all, which is why an
		// executable bit cannot come from the archive: it comes from where the file lives.
		mode = 0o644
		if executableMember(f.Name) {
			mode = 0o755
		}
	}
	out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("engine: write %s: %w", p, err)
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return fmt.Errorf("engine: write %s: %w", p, err)
	}
	return out.Close()
}

func (m *Manager) unpackTar(archive, dest, root string) error {
	f, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("engine: open %s: %w", filepath.Base(archive), err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("engine: %s is not a gzip file: %w", filepath.Base(archive), err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var written int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("engine: read %s: %w", filepath.Base(archive), err)
		}
		name := stripRoot(h.Name, root)
		if name == "" {
			continue
		}
		p, err := safeJoin(dest, name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return fmt.Errorf("engine: %s: %w", p, err)
			}
		case tar.TypeReg:
			if h.Size > maxExpanded-written {
				return fmt.Errorf("engine: %s expands past %s, which is more than libris will write",
					filepath.Base(archive), human(maxExpanded))
			}
			if err := writeTarFile(tr, p, h.FileInfo().Mode().Perm()); err != nil {
				return err
			}
			written += h.Size
		case tar.TypeSymlink:
			// Upstream really does ship these, and the engine does not work without them:
			// libllama.so -> libllama.so.1 -> libllama.so.1.0.0 is how the shared objects
			// find each other, so refusing a link is refusing to install the server. The
			// first version of this function claimed the tree contained none, and died on
			// the 37 MB download that this machine actually made.
			//
			// Containment is checked by resolving the target, not by trusting its shape: a
			// link is the one archive member whose effect happens later, when something
			// opens it, so "no .. in the name" is not enough here.
			if err := linkFile(dest, p, filepath.ToSlash(h.Linkname)); err != nil {
				return err
			}
		case tar.TypeLink:
			// A hard link names another member, and member names in a hard link are
			// archive-absolute, so it goes through stripRoot before it is safe to join.
			target, err := safeJoin(dest, stripRoot(h.Linkname, root))
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return fmt.Errorf("engine: %s: %w", filepath.Dir(p), err)
			}
			_ = os.Remove(p)
			if err := os.Link(target, p); err != nil {
				return fmt.Errorf("engine: link %s: %w", p, err)
			}
		default:
			// Devices, fifos, whiteouts: nothing an engine build needs, and creating a
			// device node in a documents folder is never the intent.
			return fmt.Errorf("engine: %s contains an unsupported archive member type for %q",
				filepath.Base(archive), h.Name)
		}
	}
}

// linkFile creates a symbolic link whose target must stay inside dest once resolved. The
// target is kept as the archive stored it, so a relative chain still reads as relative on
// disk; only the decision to create it is libris's.
func linkFile(dest, p, target string) error {
	if target == "" || strings.HasPrefix(target, "/") {
		return fmt.Errorf("engine: archive link %q points at %q, which libris will not create", p, target)
	}
	full := filepath.Join(filepath.Dir(p), filepath.FromSlash(target))
	destClean := filepath.Clean(dest)
	if full != destClean && !strings.HasPrefix(full, destClean+string(os.PathSeparator)) {
		return fmt.Errorf("engine: archive link %q resolves to %s, outside %s", p, full, dest)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("engine: %s: %w", filepath.Dir(p), err)
	}
	_ = os.Remove(p) // a re-run over a half-unpacked tree
	if err := os.Symlink(filepath.FromSlash(target), p); err != nil {
		return fmt.Errorf("engine: link %s: %w", p, err)
	}
	return nil
}

func writeTarFile(r io.Reader, p string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("engine: %s: %w", filepath.Dir(p), err)
	}
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("engine: write %s: %w", p, err)
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return fmt.Errorf("engine: write %s: %w", p, err)
	}
	return out.Close()
}

// stripRoot removes upstream's build directory from a member name — and only that
// directory, which is why the tag is passed in rather than inferred from the archive.
//
// The first version took "everything before the first slash", which is a heuristic and was
// wrong in a way worth keeping a comment about: applied to the member "../escaped" it
// returned "escaped", turning a path that safeJoin would have refused into one it accepts.
// Stripping a root that was never a root does not tidy an archive, it launders an escape.
//
// So: strip the exact prefix "llama-<tag>/", or nothing. An archive whose tree is named
// differently is unpacked with its own name kept one level deeper, which findServer walks
// for anyway — an inconvenience in a path, where the alternative is a rule that can be
// exploited.
func stripRoot(name, root string) string {
	name = filepath.ToSlash(strings.TrimSuffix(name, "/"))
	if name == "" {
		return ""
	}
	if root == "" {
		return name
	}
	if name == root {
		return "" // the root directory entry itself
	}
	return strings.TrimPrefix(name, root+"/")
}

// executableMember is the Windows-safe version of an executable bit: a member of bin/, or
// anything with the extension that means "run me" on the platform being written to.
func executableMember(name string) bool {
	n := filepath.ToSlash(name)
	if strings.HasPrefix(n, "bin/") || strings.Contains(n, "/bin/") {
		return true
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Ext(n), ".exe") || strings.EqualFold(filepath.Ext(n), ".dll")
	}
	return false
}

// findServer locates the engine's server binary inside an installed tree.
//
// It searches rather than trusting a path because the layout is upstream's to change and
// this package's failure mode is a settings page that says "installed" about a tree libris
// cannot start. The name is stable; the depth under it is not.
func findServer(dest string) (string, error) {
	want := "llama-server"
	if runtime.GOOS == "windows" {
		want += ".exe"
	}

	// The expected place first, so an ordinary install costs one stat.
	if p := filepath.Join(dest, "bin", want); exists(p) {
		return p, nil
	}
	var found string
	err := filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() {
			return nil
		}
		if d.Name() == want {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	if found != "" {
		return found, nil
	}
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return "", fmt.Errorf("engine: look for %s under %s: %w", want, dest, err)
	}
	return "", fmt.Errorf("engine: no %s under %s — that build is not one libris can run", want, dest)
}

func exists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
