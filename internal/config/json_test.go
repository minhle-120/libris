package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestJSONRoundTrip(t *testing.T) {
	want := Default()
	want.PDFDPI = 300
	want.RerankEnabled = false
	want.PDFMode = PDFModeFallback
	want.APIKey = "secret"

	raw, err := want.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	got := Default()
	if err := got.ApplyJSON(raw); err != nil {
		t.Fatalf("ApplyJSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestMarshalCoversEveryField(t *testing.T) {
	raw, err := Default().MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}

	typ := reflect.TypeOf(Default())
	for i := 0; i < typ.NumField(); i++ {
		key := typ.Field(i).Tag.Get("key")
		if !strings.Contains(string(raw), `"`+key+`":`) {
			t.Errorf("marshalled config is missing %q", key)
		}
	}
}

func TestApplyJSONPartial(t *testing.T) {
	c := Default()
	base := c.ChatURL

	if err := c.ApplyJSON([]byte(`{"pdfDPI": 72}`)); err != nil {
		t.Fatalf("ApplyJSON: %v", err)
	}
	if c.PDFDPI != 72 {
		t.Fatalf("PDFDPI = %d", c.PDFDPI)
	}
	if c.ChatURL != base {
		t.Fatalf("absent key was clobbered: %q", c.ChatURL)
	}
}

func TestApplyJSONRejectsUnknownKey(t *testing.T) {
	c := Default()
	err := c.ApplyJSON([]byte(`{"nope": 1}`))
	if err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyJSONValidates(t *testing.T) {
	cases := map[string]string{
		"range":      `{"pdfDPI": 9999}`,
		"url":        `{"chatURL": "not-a-url"}`,
		"enum":       `{"pdfPageImages": "sometimes"}`,
		"type":       `{"pdfDPI": "high"}`,
		"crossfield": `{"chunkWords": 10, "chunkOverlap": 99}`,
		"json":       `{`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			c := Default()
			if err := c.ApplyJSON([]byte(payload)); err == nil {
				t.Fatalf("%s was accepted", payload)
			}
		})
	}
}

func TestUnmarshalStartsFromDefaults(t *testing.T) {
	var c Config
	if err := c.UnmarshalJSON([]byte(`{"pdfDPI": 200}`)); err != nil {
		t.Fatal(err)
	}
	if c.ChatURL != Default().ChatURL {
		t.Fatalf("defaults not applied: %q", c.ChatURL)
	}
	if c.PDFDPI != 200 {
		t.Fatalf("PDFDPI = %d", c.PDFDPI)
	}
}

// The context-window and GPU-layer settings were retired (ADR 0011), but a
// settings.json written by that build still names them. Starting up is not allowed
// to depend on what an older version happened to persist.
func TestApplyStoredJSONDropsRetiredKeys(t *testing.T) {
	c := Default()
	dropped, err := c.ApplyStoredJSON([]byte(`{"contextSize": 16384, "gpuLayers": -1, "pdfDPI": 96}`))
	if err != nil {
		t.Fatalf("ApplyStoredJSON: %v", err)
	}
	if want := []string{"contextSize", "gpuLayers"}; !reflect.DeepEqual(dropped, want) {
		t.Fatalf("dropped = %v, want %v", dropped, want)
	}
	if c.PDFDPI != 96 {
		t.Fatalf("the known key next to the retired ones was not applied: PDFDPI = %d", c.PDFDPI)
	}
}

// Lenient about names, not about values: a retired key must not become a licence to
// ignore that the rest of the file is unusable.
func TestApplyStoredJSONStillValidatesKnownKeys(t *testing.T) {
	c := Default()
	_, err := c.ApplyStoredJSON([]byte(`{"contextSize": 16384, "pdfDPI": 9999}`))
	if err == nil || !strings.Contains(err.Error(), "pdfDPI") {
		t.Fatalf("err = %v", err)
	}
}
