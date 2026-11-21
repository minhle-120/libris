package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
)

// Config marshals to the same keys the frontend uses, so the API and the
// generated schema can never disagree about names.

func (c Config) MarshalJSON() ([]byte, error) {
	val := reflect.ValueOf(c)
	typ := val.Type()

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i := 0; i < typ.NumField(); i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(typ.Field(i).Tag.Get("key"))
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(val.Field(i).Interface())
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(raw)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON decodes over defaults: absent keys keep their default value.
func (c *Config) UnmarshalJSON(b []byte) error {
	*c = Default()
	return c.ApplyJSON(b)
}

// ApplyJSON overlays the provided keys onto c and validates the result. Unknown
// keys are rejected rather than ignored, so a frontend typo fails loudly.
func (c *Config) ApplyJSON(b []byte) error {
	_, err := c.applyJSON(b, true)
	return err
}

// ApplyStoredJSON is ApplyJSON for the settings file on disk, which may have been
// written by an older Libris. A key the current struct no longer has is dropped and
// reported instead of fatal: a request carries a live bug, a file only records an
// older vocabulary, and refusing to boot over a retired setting punishes the user
// for upgrading. Keys that are still known are validated exactly as strictly.
func (c *Config) ApplyStoredJSON(b []byte) (dropped []string, err error) {
	return c.applyJSON(b, false)
}

func (c *Config) applyJSON(b []byte, strict bool) ([]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	val := reflect.ValueOf(c).Elem()
	typ := val.Type()

	byKey := make(map[string]int, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		byKey[typ.Field(i).Tag.Get("key")] = i
	}

	var dropped []string
	for key, payload := range raw {
		i, ok := byKey[key]
		if !ok {
			if strict {
				return nil, fmt.Errorf("config: unknown setting %q", key)
			}
			dropped = append(dropped, key)
			continue
		}
		field := val.Field(i)
		target := reflect.New(field.Type())
		if err := json.Unmarshal(payload, target.Interface()); err != nil {
			return nil, fmt.Errorf("config: %s: %w", key, err)
		}
		field.Set(target.Elem())
	}
	slices.Sort(dropped)
	return dropped, c.Validate()
}
