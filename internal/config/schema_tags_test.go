package config

import (
	"bytes"
	"strings"
	"testing"
)

// The generator rejects malformed configuration structs. These are unreachable
// from the real Config, but they are the guard rails that keep a bad tag from
// producing a broken TypeScript file.
func TestGenerateRejectsBadTags(t *testing.T) {
	cases := map[string]struct {
		value any
		hint  string
	}{
		"no key": {
			value: struct {
				A string `group:"connection"`
			}{},
			hint: "no key tag",
		},
		"unknown group": {
			value: struct {
				A string `key:"a" group:"nope"`
			}{},
			hint: "unknown group",
		},
		"bad min": {
			value: struct {
				A int `key:"a" group:"connection" min:"lots"`
			}{},
			hint: "bad min",
		},
		"unknown enum": {
			value: struct {
				A string `key:"a" group:"connection" enum:"nope"`
			}{},
			hint: "unknown enum",
		},
		"unsupported kind": {
			value: struct {
				A float64 `key:"a" group:"connection"`
			}{},
			hint: "unsupported kind",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			err := generateSchema(&buf, tc.value)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.hint) {
				t.Fatalf("error %q does not mention %q", err, tc.hint)
			}
		})
	}
}

func TestGenerateSkipsEmptyGroups(t *testing.T) {
	type onlyModels struct {
		ChatModel string `key:"chatModel" group:"models" label:"Chat model" help:"x"`
	}

	var buf bytes.Buffer
	if err := generateSchema(&buf, onlyModels{}); err != nil {
		t.Fatalf("generateSchema: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `"models"`) {
		t.Error("models group missing")
	}
	if strings.Contains(out, `"connection"`) {
		t.Error("empty group was emitted")
	}
}
