package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

// The generator exists so a tool's schema and its decoder cannot drift. These tests
// are about the generator's own honesty: what it describes, what it declines, and
// the tags that decide both.
func TestJSONSchemaFromTags(t *testing.T) {
	type inner struct {
		Word string `json:"word" desc:"A word"`
	}
	type doc struct {
		Text       string   `json:"text" desc:"The passage"`
		Count      int      `json:"count" desc:"How many"`
		Ratio      float64  `json:"ratio" desc:"Share"`
		Flag       bool     `json:"flag" desc:"Whether"`
		Words      []string `json:"words" desc:"Several"`
		Numbers    []int    `json:"numbers" desc:"Several integers"`
		Depth      int      `json:"depth" optional:"true" desc:"Ignored unless given"`
		Ignored    string   `json:"-"`
		unexported string
	}

	schema, err := jsonSchema(doc{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	if got["type"] != "object" {
		t.Fatalf("type = %v", got["type"])
	}
	props := got["properties"].(map[string]any)
	for _, drop := range []string{"Ignored", "unexported", ""} {
		if _, ok := props[drop]; ok {
			t.Errorf("%q is in the schema: a field nobody tagged is not the model's to fill", drop)
		}
	}

	wantType := map[string]string{
		"text": "string", "count": "integer", "ratio": "number", "flag": "boolean",
		"depth": "integer", "words": "array", "numbers": "array",
	}
	for name, want := range wantType {
		p, ok := props[name].(map[string]any)
		if !ok {
			t.Errorf("%s missing from %v", name, keys(props))
			continue
		}
		if p["type"] != want {
			t.Errorf("%s type = %v, want %s", name, p["type"], want)
		}
		if d, _ := p["description"].(string); d == "" {
			t.Errorf("%s lost its desc tag", name)
		}
	}
	if items := props["words"].(map[string]any)["items"].(map[string]any); items["type"] != "string" {
		t.Errorf("words items = %v", items["type"])
	}
	if items := props["numbers"].(map[string]any)["items"].(map[string]any); items["type"] != "integer" {
		t.Errorf("numbers items = %v", items["type"])
	}

	// Required is every field except the ones explicitly optional. The default is
	// deliberate: a model offered an optional argument uses it less than the schema
	// appears to invite, and an argument the tool needs is not optional.
	required := keys2(got["required"].([]any))
	if len(required) != len(wantType)-1 {
		t.Errorf("required = %v, want %d of them", required, len(wantType)-1)
	}
	for _, r := range required {
		if r == "depth" {
			t.Error("depth is tagged optional and is still required")
		}
	}

	// A struct with nothing to require omits the key rather than sending an empty
	// array, which some servers read as "no arguments are allowed".
	innerSchema, err := jsonSchema(inner{})
	if err != nil {
		t.Fatal(err)
	}
	bare, err := json.Marshal(innerSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bare), `"required":["word"]`) {
		t.Errorf("inner schema = %s", bare)
	}
}

// A type this cannot describe is an error, not a guess. A tool shipped with
// parameters that are quietly wrong is worse than one that failed to build, because
// the model will still be called upon to fill them.
func TestJSONSchemaDeclinesWhatItCannotDescribe(t *testing.T) {
	type bad struct {
		Mapping map[string]string `json:"mapping" desc:"A map"`
	}
	_, err := jsonSchema(bad{})
	if err == nil {
		t.Fatal("a map field was described anyway")
	}
	if !strings.Contains(err.Error(), "bad.Mapping") {
		t.Errorf("the error does not name the offending field: %v", err)
	}
}

func TestJSONSchemaRejectsNonStructs(t *testing.T) {
	for _, v := range []any{"text", 3, []string{"a"}, nil} {
		if _, err := jsonSchema(v); err == nil {
			t.Errorf("jsonSchema(%v) succeeded, want an error", v)
		}
	}
}

// A field may describe itself, which is how an argument looser on the wire than in
// Go still promises the strict thing to the model.
func TestSchemaProviderWins(t *testing.T) {
	type args struct {
		Indices intList `json:"indices" desc:"Indices"`
	}
	argSchema, err := jsonSchema(args{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(argSchema)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	props := got["properties"].(map[string]any)
	idx := props["indices"].(map[string]any)
	if idx["type"] != "array" {
		t.Fatalf("indices = %v, want the provider's array", idx)
	}
	if items := idx["items"].(map[string]any); items["type"] != "integer" {
		t.Errorf("items = %v", items)
	}
	if d, _ := idx["description"].(string); d != "Indices" {
		t.Errorf("the desc tag was lost to the provider: %q", d)
	}
}
