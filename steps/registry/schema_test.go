package registry

import (
	"reflect"
	"strings"
	"testing"

	stepdef "dif/steps/definition"
)

const testSchema = `{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"title": "test",
	"type": "object",
	"properties": {
		"name":    {"type": "string", "description": "required"},
		"count":   {"type": "integer", "minimum": 1, "default": 10},
		"ratio":   {"type": "number"},
		"enabled": {"type": "boolean", "default": false},
		"mode":    {"type": "string", "enum": ["a", "b"], "default": "a"}
	},
	"required": ["name"],
	"additionalProperties": false
}`

func mustCompile(t *testing.T, s string) *schema {
	t.Helper()
	c, err := compile([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestValidateDefaultsAndCoercion(t *testing.T) {
	s := mustCompile(t, testSchema)
	tests := []struct {
		name string
		opts map[string]any
		want stepdef.Params
	}{
		{"defaults", map[string]any{"name": "x"},
			stepdef.Params{"name": "x", "count": 10, "enabled": false, "mode": "a"}},
		{"json types", map[string]any{"name": "x", "count": 3.0, "ratio": 0.5, "enabled": true, "mode": "b"},
			stepdef.Params{"name": "x", "count": 3, "ratio": 0.5, "enabled": true, "mode": "b"}},
		{"strings from xml", map[string]any{"name": "x", "count": " 3", "ratio": "0.5", "enabled": "true"},
			stepdef.Params{"name": "x", "count": 3, "ratio": 0.5, "enabled": true, "mode": "a"}},
		{"number as string option", map[string]any{"name": 1234.0},
			stepdef.Params{"name": "1234", "count": 10, "enabled": false, "mode": "a"}},
		{"null means unset", map[string]any{"name": "x", "count": nil},
			stepdef.Params{"name": "x", "count": 10, "enabled": false, "mode": "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.validate(tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("params = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateErrors(t *testing.T) {
	s := mustCompile(t, testSchema)
	tests := []struct {
		name string
		opts map[string]any
		want string
	}{
		{"missing required", map[string]any{}, "missing required option name"},
		{"not an integer", map[string]any{"name": "x", "count": "x"}, `option count: want integer, got "x"`},
		{"fraction", map[string]any{"name": "x", "count": 1.5}, "option count: want integer, got 1.5"},
		{"not a boolean", map[string]any{"name": "x", "enabled": "yes"}, `option enabled: want boolean, got "yes"`},
		{"minimum", map[string]any{"name": "x", "count": 0.0}, "option count: 0 is less than 1"},
		{"enum", map[string]any{"name": "x", "mode": "c"}, `option mode: "c" is not one of "a", "b"`},
		{"unknown", map[string]any{"name": "x", "numbers": 1.0}, "unknown option numbers"},
		{"object for string", map[string]any{"name": []any{"x"}}, "option name: want string, got [x]"},
		{"all problems at once", map[string]any{"count": "x", "bogus": 1.0},
			`option count: want integer, got "x"; unknown option bogus; missing required option name`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.validate(tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateAdditionalProperties(t *testing.T) {
	s := mustCompile(t, `{"type": "object", "properties": {"a": {"type": "string"}}}`)
	got, err := s.validate(map[string]any{"a": "x", "extra": 1.0})
	if err != nil {
		t.Fatal(err)
	}
	if want := (stepdef.Params{"a": "x", "extra": 1.0}); !reflect.DeepEqual(got, want) {
		t.Errorf("params = %v, want %v", got, want)
	}
}

func TestCompileErrors(t *testing.T) {
	tests := []struct {
		name, schema, want string
	}{
		{"invalid json", `{`, "schema:"},
		{"not an object", `{"type": "string"}`, `type must be "object"`},
		{"unsupported keyword", `{"type": "object", "oneOf": []}`, `unsupported keyword "oneOf"`},
		{"unsupported property keyword", `{"type": "object", "properties": {"a": {"type": "string", "pattern": "x"}}}`, `unsupported keyword "pattern"`},
		{"nested object", `{"type": "object", "properties": {"a": {"type": "object"}}}`, "type must be one of"},
		{"bad default", `{"type": "object", "properties": {"a": {"type": "integer", "default": "x"}}}`, "default"},
		{"bad enum", `{"type": "object", "properties": {"a": {"type": "integer", "enum": ["x"]}}}`, "enum value"},
		{"minimum on string", `{"type": "object", "properties": {"a": {"type": "string", "minimum": 1}}}`, "minimum"},
		{"required unknown", `{"type": "object", "required": ["a"]}`, "required"},
		{"additionalProperties schema", `{"type": "object", "additionalProperties": {}}`, "additionalProperties"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compile([]byte(tt.schema))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}
