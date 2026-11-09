// Package config is the single source of typed, validated settings.
package config

import (
	"fmt"
	"os"
	"strconv"
)

const (
	DefaultPDFDPI       = 150 // readable at 100–200; higher grows the page rasters fast
	DefaultChunkWords   = 100
	DefaultChunkOverlap = 50
	DefaultMaxFileBytes = 512 << 20

	MinPDFDPI = 50
	MaxPDFDPI = 600
)

type Config struct {
	// PDFDPI is the raster resolution used when a PDF page becomes an image
	// embedding. 150 dpi keeps a page legible at roughly 300–500 KB.
	PDFDPI int

	ChunkWords   int
	ChunkOverlap int

	MaxFileBytes int64
}

func Default() Config {
	return Config{
		PDFDPI:       DefaultPDFDPI,
		ChunkWords:   DefaultChunkWords,
		ChunkOverlap: DefaultChunkOverlap,
		MaxFileBytes: DefaultMaxFileBytes,
	}
}

// Load returns defaults with any LIBRIS_* environment overrides applied.
func Load() (Config, error) {
	c := Default()

	if v := os.Getenv("LIBRIS_PDF_DPI"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("config: LIBRIS_PDF_DPI: %w", err)
		}
		c.PDFDPI = n
	}
	if v := os.Getenv("LIBRIS_CHUNK_WORDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("config: LIBRIS_CHUNK_WORDS: %w", err)
		}
		c.ChunkWords = n
	}
	if v := os.Getenv("LIBRIS_CHUNK_OVERLAP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("config: LIBRIS_CHUNK_OVERLAP: %w", err)
		}
		c.ChunkOverlap = n
	}
	if v := os.Getenv("LIBRIS_MAX_FILE_MB"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("config: LIBRIS_MAX_FILE_MB: %w", err)
		}
		c.MaxFileBytes = n << 20
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.PDFDPI < MinPDFDPI || c.PDFDPI > MaxPDFDPI {
		return fmt.Errorf("config: PDFDPI %d outside %d–%d", c.PDFDPI, MinPDFDPI, MaxPDFDPI)
	}
	if c.ChunkWords <= 0 {
		return fmt.Errorf("config: ChunkWords must be > 0, got %d", c.ChunkWords)
	}
	if c.ChunkOverlap < 0 || c.ChunkOverlap >= c.ChunkWords {
		return fmt.Errorf("config: ChunkOverlap %d must be in [0, %d)", c.ChunkOverlap, c.ChunkWords)
	}
	if c.MaxFileBytes <= 0 {
		return fmt.Errorf("config: MaxFileBytes must be > 0")
	}
	return nil
}
