package config

import (
	"strings"
	"testing"
)

func TestDefaultValidates(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if c.PDFDPI != DefaultPDFDPI {
		t.Fatalf("PDFDPI = %d", c.PDFDPI)
	}
}

func TestLoadDefaults(t *testing.T) {
	for _, key := range []string{"LIBRIS_PDF_DPI", "LIBRIS_CHUNK_WORDS", "LIBRIS_CHUNK_OVERLAP", "LIBRIS_MAX_FILE_MB"} {
		t.Setenv(key, "")
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c != Default() {
		t.Fatalf("Load = %+v, want defaults", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("LIBRIS_PDF_DPI", "200")
	t.Setenv("LIBRIS_CHUNK_WORDS", "300")
	t.Setenv("LIBRIS_CHUNK_OVERLAP", "10")
	t.Setenv("LIBRIS_MAX_FILE_MB", "32")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PDFDPI != 200 || c.ChunkWords != 300 || c.ChunkOverlap != 10 {
		t.Fatalf("config = %+v", c)
	}
	if c.MaxFileBytes != 32<<20 {
		t.Fatalf("MaxFileBytes = %d", c.MaxFileBytes)
	}
}

func TestLoadParseErrors(t *testing.T) {
	cases := map[string]string{
		"LIBRIS_PDF_DPI":       "not-a-number",
		"LIBRIS_CHUNK_WORDS":   "x",
		"LIBRIS_CHUNK_OVERLAP": "x",
		"LIBRIS_MAX_FILE_MB":   "x",
	}

	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q was accepted", key, value)
			} else if !strings.Contains(err.Error(), key) {
				t.Fatalf("error does not name %s: %v", key, err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"dpi too low", func(c *Config) { c.PDFDPI = MinPDFDPI - 1 }, "PDFDPI"},
		{"dpi too high", func(c *Config) { c.PDFDPI = MaxPDFDPI + 1 }, "PDFDPI"},
		{"zero words", func(c *Config) { c.ChunkWords = 0 }, "ChunkWords"},
		{"negative overlap", func(c *Config) { c.ChunkOverlap = -1 }, "ChunkOverlap"},
		{"overlap equals size", func(c *Config) { c.ChunkOverlap = c.ChunkWords }, "ChunkOverlap"},
		{"zero max bytes", func(c *Config) { c.MaxFileBytes = 0 }, "MaxFileBytes"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"LIBRIS_PDF_DPI", "LIBRIS_CHUNK_WORDS", "LIBRIS_CHUNK_OVERLAP", "LIBRIS_MAX_FILE_MB"} {
		t.Setenv(key, "")
	}
}
