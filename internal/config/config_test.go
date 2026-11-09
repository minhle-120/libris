package config

import (
	"strings"
	"testing"
)

func TestDefaultValidates(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}
	if c.PDFDPI != 150 || c.ChunkWords != 100 || c.ChunkOverlap != 50 {
		t.Fatalf("defaults changed: %+v", c)
	}
	if got := c.MaxFileBytes(); got != 512<<20 {
		t.Fatalf("MaxFileBytes = %d", got)
	}
	if !c.PDFAlwaysImages() {
		t.Fatal("default PDF mode should rasterise always")
	}
	c.PDFMode = PDFModeFallback
	if c.PDFAlwaysImages() {
		t.Fatal("fallback mode must not force page images")
	}
}

func TestLoadEnvNamesFollowKeys(t *testing.T) {
	// The env name is derived from the key tag, so these stay stable.
	t.Setenv("LIBRIS_CHAT_URL", "http://127.0.0.1:9999")
	t.Setenv("LIBRIS_PDF_DPI", "200")
	t.Setenv("LIBRIS_MAX_FILE_MB", "32")
	t.Setenv("LIBRIS_RERANK_ENABLED", "false")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ChatURL != "http://127.0.0.1:9999" {
		t.Errorf("ChatURL = %q", c.ChatURL)
	}
	if c.PDFDPI != 200 {
		t.Errorf("PDFDPI = %d", c.PDFDPI)
	}
	if c.MaxFileBytes() != 32<<20 {
		t.Errorf("MaxFileBytes = %d", c.MaxFileBytes())
	}
	if c.RerankEnabled {
		t.Error("RerankEnabled should be false")
	}
}

func TestLoadDefaultsWhenEnvEmpty(t *testing.T) {
	t.Setenv("LIBRIS_PDF_DPI", "")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c != Default() {
		t.Fatalf("empty env should keep defaults")
	}
}

func TestLoadParseErrors(t *testing.T) {
	for _, key := range []string{"LIBRIS_PDF_DPI", "LIBRIS_TIMEOUT_SECONDS", "LIBRIS_RERANK_ENABLED"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "not-valid")
			if _, err := Load(); err == nil {
				t.Fatalf("%s accepted an invalid value", key)
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
		{"dpi too low", func(c *Config) { c.PDFDPI = 49 }, "pdfDPI"},
		{"dpi too high", func(c *Config) { c.PDFDPI = 601 }, "pdfDPI"},
		{"zero chunk words", func(c *Config) { c.ChunkWords = 0 }, "chunkWords"},
		{"overlap equals size", func(c *Config) { c.ChunkOverlap = c.ChunkWords }, "chunk overlap"},
		{"missing chat url", func(c *Config) { c.ChatURL = "  " }, "chatURL is required"},
		{"bad url scheme", func(c *Config) { c.EmbedURL = "127.0.0.1:8082" }, "http(s) URL"},
		{"bad enum", func(c *Config) { c.PDFMode = "sometimes" }, "pdfModes"},
		{"timeout too low", func(c *Config) { c.TimeoutSeconds = 1 }, "timeoutSeconds"},
		{"context too small", func(c *Config) { c.ContextSize = 1 }, "contextSize"},
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

func TestSnake(t *testing.T) {
	cases := map[string]string{
		"chatURL":        "chat_url",
		"pdfDPI":         "pdf_dpi",
		"maxFileMB":      "max_file_mb",
		"apiKey":         "api_key",
		"timeoutSeconds": "timeout_seconds",
	}
	for in, want := range cases {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnums(t *testing.T) {
	modes := Enums()["pdfModes"]
	if len(modes) != 2 {
		t.Fatalf("pdfModes = %+v", modes)
	}
	for _, m := range modes {
		if m.Label == "" {
			t.Errorf("enum %q has no label", m.Value)
		}
	}
}
