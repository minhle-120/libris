package config

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

// The generated schema is the only bridge between Go config and the UI. The
// frontend holds no defaults, labels, ranges or enum options of its own.
//
//go:generate go run gen.go

const GeneratedHeader = `// Code generated from internal/config. DO NOT EDIT.
// Edit internal/config/config.go, then run: make generate
`

type schemaField struct {
	Key      string      `json:"key"`
	Label    string      `json:"label"`
	Help     string      `json:"help"`
	Kind     string      `json:"kind"`
	Unit     string      `json:"unit,omitempty"`
	Min      *int        `json:"min,omitempty"`
	Max      *int        `json:"max,omitempty"`
	Options  []EnumValue `json:"options,omitempty"`
	Readonly bool        `json:"readonly,omitempty"`
	// Hidden comes from noview:"true": the setting exists, is saved, validated and generated
	// like every other one, and the generated form renders no control for it because a better
	// one lives beside the facts it needs. Hiding a field is not dropping it from the schema —
	// the value still round-trips through the API.
	Hidden bool `json:"hidden,omitempty"`
	// Mode is which engine mode a field means anything in, and it is empty for the fields
	// that mean the same thing either way. It is generated rather than written in the
	// frontend so "which fields does managed mode hide" cannot disagree with the struct
	// that defines the settings.
	Mode string `json:"mode,omitempty"`
}

type schemaGroup struct {
	ID     string        `json:"id"`
	Label  string        `json:"label"`
	Help   string        `json:"help"`
	Fields []schemaField `json:"fields"`
}

// Generate writes the TypeScript schema accepted by web/src/lib/config.gen.ts.
func Generate(w io.Writer) error {
	return generateSchema(w, Default())
}

// generateSchema takes the config value so tests can feed deliberately
// mistagged structs and exercise the validation of the generator itself.
func generateSchema(w io.Writer, cfg any) error {
	val := reflect.ValueOf(cfg)
	typ := val.Type()

	order := make([]string, 0, len(groups()))
	byID := map[string]*schemaGroup{}
	for _, g := range groups() {
		order = append(order, g.ID)
		byID[g.ID] = &schemaGroup{ID: g.ID, Label: g.Label, Help: g.Help}
	}

	type fieldInfo struct {
		key    string
		tsType string
		value  any
	}
	var fields []fieldInfo

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		key := f.Tag.Get("key")
		if key == "" {
			return fmt.Errorf("config: field %s has no key tag", f.Name)
		}
		groupID := f.Tag.Get("group")
		g, ok := byID[groupID]
		if !ok {
			return fmt.Errorf("config: field %s has unknown group %q", f.Name, groupID)
		}

		kind, tsType, err := kindOf(f, val.Field(i))
		if err != nil {
			return err
		}

		sf := schemaField{
			Key:      key,
			Label:    f.Tag.Get("label"),
			Help:     f.Tag.Get("help"),
			Kind:     kind,
			Unit:     f.Tag.Get("unit"),
			Readonly: f.Tag.Get("readonly") == "true",
			Hidden:   f.Tag.Get("noview") == "true",
			Mode:     f.Tag.Get("mode"),
		}
		switch sf.Mode {
		case "", "managed", "external":
		default:
			return fmt.Errorf("config: field %s: bad mode %q (want managed or external)", f.Name, sf.Mode)
		}
		for tag, dst := range map[string]**int{"min": &sf.Min, "max": &sf.Max} {
			if s := f.Tag.Get(tag); s != "" {
				n, err := strconv.Atoi(s)
				if err != nil {
					return fmt.Errorf("config: field %s: bad %s %q", f.Name, tag, s)
				}
				*dst = &n
			}
		}
		if enum := f.Tag.Get("enum"); enum != "" {
			sf.Options = Enums()[enum]
			if len(sf.Options) == 0 {
				return fmt.Errorf("config: field %s: unknown enum %q", f.Name, enum)
			}
			tsType = enumTS(sf.Options)
		}

		g.Fields = append(g.Fields, sf)
		fields = append(fields, fieldInfo{key: key, tsType: tsType, value: val.Field(i).Interface()})
	}

	var groupsJSON []schemaGroup
	for _, id := range order {
		if g := byID[id]; len(g.Fields) > 0 {
			groupsJSON = append(groupsJSON, *g)
		}
	}

	var b strings.Builder
	b.WriteString(GeneratedHeader)
	b.WriteString("\nexport type SettingKind = 'text' | 'url' | 'password' | 'number' | 'bool' | 'enum';\n")
	b.WriteString("export type SettingOption = { value: string; label: string };\n")
	b.WriteString("export type SettingField = {\n\tkey: keyof Settings;\n\tlabel: string;\n\thelp: string;\n\tkind: SettingKind;\n\tunit?: string;\n\tmin?: number;\n\tmax?: number;\n\treadonly?: boolean;\n	/** Saved, validated and generated like the rest; rendered by a hand-written control elsewhere. */\n	hidden?: boolean;\n\toptions?: SettingOption[];\n\t/** Which engine mode this field means anything in; absent means both. */\n\tmode?: 'managed' | 'external';\n};\n")
	b.WriteString("export type SettingGroup = { id: string; label: string; help: string; fields: SettingField[] };\n\n")

	b.WriteString("export type Settings = {\n")
	for _, f := range fields {
		fmt.Fprintf(&b, "\t%s: %s;\n", f.key, f.tsType)
	}
	b.WriteString("};\n\n")

	b.WriteString("export const settingGroups: SettingGroup[] = ")
	groupsJSONBytes, err := json.MarshalIndent(groupsJSON, "", "\t")
	if err != nil {
		return err
	}
	b.Write(groupsJSONBytes)
	b.WriteString(";\n\n")

	b.WriteString("export function defaultSettings(): Settings {\n\treturn {\n")
	for _, f := range fields {
		raw, err := json.Marshal(f.value)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "\t\t%s: %s,\n", f.key, raw)
	}
	b.WriteString("\t};\n}\n")

	_, err = io.WriteString(w, b.String())
	return err
}

func kindOf(f reflect.StructField, v reflect.Value) (kind, tsType string, err error) {
	switch v.Kind() {
	case reflect.String:
		tsType = "string"
	case reflect.Int, reflect.Int64:
		tsType = "number"
	case reflect.Bool:
		tsType = "boolean"
	default:
		return "", "", fmt.Errorf("config: field %s: unsupported kind %s", f.Name, v.Kind())
	}

	if k := f.Tag.Get("kind"); k != "" {
		return k, tsType, nil
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int64:
		return "number", tsType, nil
	case reflect.Bool:
		return "bool", tsType, nil
	}
	return "text", tsType, nil
}

func enumTS(values []EnumValue) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, "'"+v.Value+"'")
	}
	return strings.Join(parts, " | ")
}
