package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"libris/internal/domain"
)

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestSaveNamesTheFileByItsHash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "originals")
	o := Originals{Dir: dir}

	body := "%PDF-1.4\nsomething\n"
	saved, err := o.Save(strings.NewReader(body), "My Paper.PDF", 1<<20)
	if err != nil {
		t.Fatal(err)
	}

	if saved.SHA256 != hashOf(body) {
		t.Errorf("sha256 = %q", saved.SHA256)
	}
	if saved.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", saved.Bytes, len(body))
	}
	if saved.Kind != domain.SourcePDF {
		t.Errorf("kind = %q, want pdf", saved.Kind)
	}

	want := filepath.Join(dir, hashOf(body)+".pdf")
	if saved.Path != want {
		t.Errorf("path = %q, want %q", saved.Path, want)
	}

	// The directory is the file's only home: no temp left behind.
	if got := leftovers(t, dir); len(got) != 1 {
		t.Errorf("directory = %v, want only the stored original", got)
	}

	raw, err := os.ReadFile(saved.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != body {
		t.Errorf("stored bytes = %q", raw)
	}
}

func TestSaveIsIdempotentPerContent(t *testing.T) {
	o := Originals{Dir: filepath.Join(t.TempDir(), "originals")}

	first, err := o.Save(strings.NewReader("same bytes"), "one.txt", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	second, err := o.Save(strings.NewReader("same bytes"), "two.txt", 1<<20)
	if err != nil {
		t.Fatal(err)
	}

	if first.SHA256 != second.SHA256 || first.Path != second.Path {
		t.Errorf("identical bytes stored apart: %q vs %q", first.Path, second.Path)
	}
	if got := leftovers(t, filepath.Dir(first.Path)); len(got) != 1 {
		t.Errorf("directory = %v, want one file for identical content", got)
	}
}

func TestSaveRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	o := Originals{Dir: dir}

	if _, err := o.Save(strings.NewReader(""), "empty.txt", 1<<20); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty error = %v, want ErrInvalid", err)
	}

	body := strings.Repeat("x", 4096)
	if _, err := o.Save(strings.NewReader(body), "big.bin", 1024); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize error = %v, want ErrTooLarge", err)
	}

	// A refused upload must not leave a file, and the cap must be enforced while
	// streaming rather than after the whole body arrives.
	if got := leftovers(t, dir); len(got) != 0 {
		t.Errorf("directory = %v, want nothing behind from refused uploads", got)
	}
}

func TestSaveWithoutADirectory(t *testing.T) {
	if _, err := (Originals{}).Save(strings.NewReader("x"), "a.txt", 1<<20); err == nil {
		t.Fatal("expected a missing directory to be refused")
	}
}

// TestSafeExt covers the one place a client-supplied name reaches a path.
func TestSafeExt(t *testing.T) {
	cases := map[string]string{
		"paper.pdf":          ".pdf",
		"IMAGE.PNG":          ".png",
		"song.M4A":           ".m4a",
		"notes":              "",
		"weird.":             "",
		"a.abcdefghijklmnop": "", // extension length is capped
		"a.xls ss":           "",
		"a.txt\nEvil: b":     "", // newline must not reach a filename
		"a.xls\"ss":          "",
		"dir.name.tar.gz":    ".gz",
		".hidden":            ".hidden", // a 6 char extension is alphanumeric and short
		"123456789":          "",
	}

	for name, want := range cases {
		if got := safeExt(name); got != want {
			t.Errorf("safeExt(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSaveDetectsKindFromContentNotJustName(t *testing.T) {
	o := Originals{Dir: filepath.Join(t.TempDir(), "originals")}

	// A PNG renamed .txt is still a PNG: detection reads the head as well as
	// the extension, which is why Save keeps the first bytes.
	saved, err := o.Save(strings.NewReader("\x89PNG\r\n\x1a\n rest"), "mislabelled.txt", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Kind != domain.SourceImage {
		t.Errorf("kind = %q, want image from the magic bytes", saved.Kind)
	}
}

func TestHeadBufferStopsAtTheCap(t *testing.T) {
	var h headBuffer
	n, err := h.Write([]byte(strings.Repeat("a", headBytes+100)))
	if err != nil {
		t.Fatal(err)
	}
	if n != headBytes+100 {
		t.Errorf("reported %d written, want the whole slice so io.Copy continues", n)
	}
	if len(h.bytes()) != headBytes {
		t.Errorf("kept %d bytes, want the cap of %d", len(h.bytes()), headBytes)
	}

	// A second write past the cap keeps the size stable.
	if _, err := h.Write([]byte("more")); err != nil {
		t.Fatal(err)
	}
	if len(h.bytes()) != headBytes {
		t.Errorf("kept %d bytes after a second write", len(h.bytes()))
	}
}
