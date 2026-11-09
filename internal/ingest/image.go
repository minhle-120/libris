package ingest

import (
	"fmt"
	"image"
	"os"

	// Decoders are registered for image.DecodeConfig.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

type imageMeta struct {
	Format string // "png", "jpeg", "gif"
	Width  int
	Height int
}

// inspectImage reads just the header. Formats without a stdlib decoder (webp,
// tiff, heic) return an error and are handled as unsupported by the pipeline.
func inspectImage(path string) (imageMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return imageMeta{}, err
	}
	defer f.Close()

	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return imageMeta{}, fmt.Errorf("ingest: decode image header: %w", err)
	}
	return imageMeta{Format: format, Width: cfg.Width, Height: cfg.Height}, nil
}

func (m imageMeta) String() string {
	return fmt.Sprintf("%s %dx%d", m.Format, m.Width, m.Height)
}

func (m imageMeta) MIME() string {
	switch m.Format {
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	}
	return "application/octet-stream"
}
