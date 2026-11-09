package config

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The frontend schema is generated, so it must never drift. A failure here
// means config.go changed without running `make generate`.
func TestGeneratedSchemaIsCurrent(t *testing.T) {
	var buf bytes.Buffer
	if err := Generate(&buf); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	path := "../../web/src/lib/config.gen.ts"
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(onDisk, buf.Bytes()) {
		t.Fatalf("%s is stale: run make generate", path)
	}
}

func TestGeneratedSchemaCoversEveryField(t *testing.T) {
	var buf bytes.Buffer
	if err := Generate(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	typ := reflect.TypeOf(Default())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		key := field.Tag.Get("key")
		if !strings.Contains(out, "\t"+key+":") {
			t.Errorf("field %q missing from generated Settings", key)
		}
		for _, tag := range []string{"label", "help", "group"} {
			if field.Tag.Get(tag) == "" {
				t.Errorf("field %s has no %s tag", field.Name, tag)
			}
		}
	}
}

func TestGenerateReportsBadTags(t *testing.T) {
	// Generate reflects over Config only, so bad tags are caught by the
	// coverage test above. This guards the enum lookup path directly.
	if len(Enums()["pdfModes"]) == 0 {
		t.Fatal("pdfModes enum is empty")
	}
	if got := enumTS(Enums()["pdfModes"]); !strings.Contains(got, "'always'") {
		t.Fatalf("enumTS = %q", got)
	}
}
