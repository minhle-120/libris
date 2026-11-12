package chat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// jsonSchema describes a Go struct in the JSON Schema dialect OpenAI-compatible
// servers read, so a tool's schema and the struct its arguments are decoded into
// are the same declaration.
//
// That identity is the point. The gap between the two is invisible and expensive:
// the model writes the property it was shown, the decoder looks for the name it
// has, and the field arrives as the empty string — which reads as "the model used
// the tool badly" and is our bug. A renamed field then moves the schema and the
// decoder in one edit, or the tests notice.
//
// Names come from the json tag, descriptions from desc. Every field is required
// unless tagged optional:"true": a model presented with an optional argument uses
// it less often than the schema appears to invite, and every argument here is one
// the tool needs.
//
// Anything this cannot describe truthfully is an error. A tool whose parameters are
// quietly wrong is worse than a tool that failed to build.
func jsonSchema(v any) (map[string]any, error) {
	t := reflect.TypeOf(v)
	if t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("chat: tool arguments must be a struct, got %v", reflect.TypeOf(v))
	}

	props := make(map[string]any, t.NumField())
	var required []string
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported: not the model's business
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" || name == "" {
			continue
		}

		s, err := typeSchema(f.Type)
		if err != nil {
			return nil, fmt.Errorf("chat: %s.%s: %w", t.Name(), f.Name, err)
		}
		if d := f.Tag.Get("desc"); d != "" {
			s["description"] = strings.TrimSpace(d)
		}
		props[name] = s
		if f.Tag.Get("optional") != "true" {
			required = append(required, name)
		}
	}

	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, nil
}

// schemaProvider lets a field describe itself, for an argument whose wire shape is
// looser than its Go type — the list of chunk indices a model emits, say, which
// arrives as integers, as numeric strings, or as both in one array.
type schemaProvider interface {
	JSONSchema() map[string]any
}

func typeSchema(t reflect.Type) (map[string]any, error) {
	if v := reflect.New(t).Elem(); v.CanInterface() {
		if p, ok := v.Interface().(schemaProvider); ok {
			return p.JSONSchema(), nil
		}
		if p, ok := reflect.New(t).Interface().(schemaProvider); ok {
			return p.JSONSchema(), nil
		}
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, nil
		}
		if isInt(t.Elem()) {
			return map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, nil
		}
	}
	return nil, fmt.Errorf("cannot describe a %s in JSON Schema", t)
}

func isInt(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	}
	return false
}

// toolArguments decodes what the model sent into a fresh value of args' type.
//
// A failure to parse is not an error to return: the reference treats malformed
// arguments as an empty object and tells the model through the tool result, because
// a turn lost to a stray brace is a turn the user waited for. The caller therefore
// gets the zero value and decides what the missing fields mean — no keywords, no
// indices — which is a thing a model can fix on the next iteration.
func toolArguments(raw string, args any) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	// Deliberately unchecked: every field has a usable zero value, and the caller
	// is asked to cope with them by construction.
	_ = json.Unmarshal([]byte(raw), args)
}
