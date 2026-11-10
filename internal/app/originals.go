package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"libris/internal/domain"
	"libris/internal/ingest"
)

// Originals is where uploaded files are kept, named by content hash.
//
// The name is the hash for two reasons: two uploads of the same bytes are the
// same file, and a citation can always open a path that cannot have moved out
// from under it. Nothing here overwrites or deletes — keeping the original is a
// stated rule, and garbage collection that might delete a cited file is a
// separate decision.
type Originals struct {
	Dir string
}

// Saved describes a file that is now on disk and identified.
type Saved struct {
	SHA256 string
	Bytes  int64
	Path   string
	Kind   domain.SourceKind
}

// headBytes is what content sniffing needs; ingest.ReadHead reads the same
// amount from the finished file, so this only saves a second open during upload.
const headBytes = 512

// Save streams r into a temporary file, hashing as it goes, then renames it to
// its hash. A failure leaves no file behind, and a size over the cap is refused
// part-way rather than after filling the disk.
func (o Originals) Save(r io.Reader, filename string, maxBytes int64) (Saved, error) {
	if strings.TrimSpace(o.Dir) == "" {
		return Saved{}, errors.New("app: originals directory is not configured")
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return Saved{}, err
	}

	tmp, err := os.CreateTemp(o.Dir, ".upload-*")
	if err != nil {
		return Saved{}, err
	}
	name := tmp.Name()
	// Any failure below must remove the temp: a half-written original with a
	// plausible name is worse than no original.
	defer func() {
		if name != "" {
			os.Remove(name)
		}
	}()

	hash := sha256.New()
	bounded := io.LimitReader(r, maxBytes+1)

	// A capped writer, so sniffing material does not grow with the upload.
	var head headBuffer

	n, err := io.Copy(io.MultiWriter(tmp, hash, &head), bounded)
	if err != nil {
		tmp.Close()
		return Saved{}, fmt.Errorf("reading the upload: %w", err)
	}
	if n > maxBytes {
		tmp.Close()
		return Saved{}, fmt.Errorf("%w: %s is limited to %d bytes", ErrTooLarge, filename, maxBytes)
	}
	if err := tmp.Close(); err != nil {
		return Saved{}, err
	}
	if n == 0 {
		return Saved{}, fmt.Errorf("%w: %s is empty", ErrInvalid, filename)
	}

	sum := hex.EncodeToString(hash.Sum(nil))
	ext := safeExt(filename)
	final := filepath.Join(o.Dir, sum+ext)

	// The hash decides the name, so a collision is the same file. Renaming over
	// it replaces identical bytes with identical bytes; the original is still
	// the one that was here first in every way that matters.
	if err := os.Rename(name, final); err != nil {
		return Saved{}, err
	}
	name = "" // renamed: nothing left to clean up

	return Saved{
		SHA256: sum,
		Bytes:  n,
		Path:   final,
		Kind:   ingest.Detect(filename, head.bytes()),
	}, nil
}

// headBuffer keeps the first headBytes written to it and discards the rest.
type headBuffer struct{ buf []byte }

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := headBytes - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(len(p), room)]...)
	}
	return len(p), nil
}

func (h *headBuffer) bytes() []byte { return h.buf }

// safeExt keeps only a short alphanumeric extension. Upload names come from a
// client, and an extension is appended to a path — so it is treated as input to
// be rejected, not sanitised.
func safeExt(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if len(ext) < 2 || len(ext) > 9 {
		return ""
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}
