package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"libris/internal/domain"
)

// Sink stores derived files: extracted page images, copied originals, clips cut
// from a recording. Implementations decide where; the pipeline only needs an Asset
// back — including its MIME, which is what tells the browser to put a player on it.
type Sink interface {
	PutFile(ctx context.Context, name, srcPath string) (domain.Asset, error)
	Put(ctx context.Context, name string, r io.Reader) (domain.Asset, error)
}

// DirSink writes assets under Root, using slash-separated relative names such
// as "<source-id>/pages/page-0001.png".
//
// Absolute names and any ".." component are rejected rather than sanitised: a
// silently rewritten path is worse than a refusal, and these names originate
// from filenames and tool output.
type DirSink struct {
	Root string
}

func (d DirSink) PutFile(ctx context.Context, name, srcPath string) (domain.Asset, error) {
	if err := ctx.Err(); err != nil {
		return domain.Asset{}, err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return domain.Asset{}, err
	}
	defer src.Close()

	return d.put(ctx, name, src)
}

func (d DirSink) Put(ctx context.Context, name string, r io.Reader) (domain.Asset, error) {
	if err := ctx.Err(); err != nil {
		return domain.Asset{}, err
	}
	return d.put(ctx, name, r)
}

func (d DirSink) put(ctx context.Context, name string, r io.Reader) (domain.Asset, error) {
	dst, err := d.resolve(name)
	if err != nil {
		return domain.Asset{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return domain.Asset{}, err
	}

	// Write then rename, so a crashed ingest never leaves a half page behind.
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return domain.Asset{}, err
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return domain.Asset{}, copyErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return domain.Asset{}, closeErr
	}
	if err := ctx.Err(); err != nil {
		os.Remove(tmp)
		return domain.Asset{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return domain.Asset{}, err
	}

	return domain.Asset{ID: name, Path: dst, MIME: mimeByExt(dst), Bytes: n}, nil
}

func (d DirSink) resolve(name string) (string, error) {
	if strings.TrimSpace(d.Root) == "" {
		return "", errors.New("ingest: sink root is empty")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("ingest: unsafe asset name %q", name)
	}
	return filepath.Join(d.Root, clean), nil
}

func mimeByExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".pdf":
		return "application/pdf"
	case ".wav":
		return "audio/wav"
	}
	return "application/octet-stream"
}
