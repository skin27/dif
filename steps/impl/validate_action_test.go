package impl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

const orderSchema = `{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"title": "Order",
	"type": "object",
	"required": ["id", "lines"],
	"properties": {
		"id": {"type": "integer", "minimum": 1},
		"status": {"enum": ["new", "paid"]},
		"customer": {"type": "string", "minLength": 2, "pattern": "^[A-Z]"},
		"lines": {"type": "array", "minItems": 1, "items": {
			"type": "object",
			"required": ["sku"],
			"properties": {"sku": {"type": "string"}, "qty": {"type": "number", "exclusiveMinimum": 0}},
			"additionalProperties": false
		}}
	}
}`

func validateErr(t *testing.T, opts map[string]any, body string) error {
	t.Helper()
	p := mustProcessor(t, stepdef.Action, "validate", opts).(stepdef.ActionProcessor)
	m := message.New(body)
	out, err := p.Process(context.Background(), m)
	if err == nil && out[message.Body] != body {
		t.Errorf("body = %v, want it unchanged", out[message.Body])
	}
	return err
}

func TestValidate(t *testing.T) {
	opts := map[string]any{"schema": orderSchema}
	if err := validateErr(t, opts, `{"id": 7, "status": "paid", "customer": "Acme", "lines": [{"sku": "a", "qty": 2.5}], "extra": true}`); err != nil {
		t.Errorf("valid order: %v", err)
	}

	err := validateErr(t, opts, `{"id": 1.5, "status": "lost", "customer": "a", "lines": [{"qty": 0, "note": "x"}]}`)
	want := []string{
		"/id: want integer, got number",
		`/status: "lost" is not one of ["new","paid"]`,
		"/customer: length 1 is less than 2",
		`/customer: "a" does not match ^[A-Z]`,
		"/lines/0: missing required property sku",
		"/lines/0/qty: 0 is not greater than 0",
		"/lines/0/note: not allowed",
	}
	if err == nil {
		t.Fatal("invalid order passed")
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("err = %v\nwant containing %q", err, w)
		}
	}

	if err := validateErr(t, opts, `[]`); err == nil || err.Error() != "body is not valid: /: want object, got array" {
		t.Errorf("array body: err = %v", err)
	}
	if err := validateErr(t, opts, `{"id": 2}`); err == nil || !strings.Contains(err.Error(), "/: missing required property lines") {
		t.Errorf("missing lines: err = %v", err)
	}
	if err := validateErr(t, opts, `<order/>`); err == nil || !strings.Contains(err.Error(), "body is not JSON") {
		t.Errorf("XML body: err = %v", err)
	}
}

func TestValidateSchemaFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "order.json")
	if err := os.WriteFile(path, []byte(orderSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateErr(t, map[string]any{"schemaFile": path}, `{"id": 0, "lines": []}`); err == nil ||
		!strings.Contains(err.Error(), "/id: 0 is less than 1") || !strings.Contains(err.Error(), "/lines: 0 items, want at least 1") {
		t.Errorf("err = %v", err)
	}
}

func TestValidateInvalid(t *testing.T) {
	for _, tt := range []struct {
		opts map[string]any
		want string
	}{
		{nil, "set one of the options schema and schemaFile"},
		{map[string]any{"schema": "{}", "schemaFile": "x.json"}, "set one of the options schema and schemaFile"},
		{map[string]any{"schemaFile": "does-not-exist.json"}, "option schemaFile:"},
		{map[string]any{"schema": "{"}, "schema is not JSON"},
		{map[string]any{"schema": `{"$ref": "#/x"}`}, `schema /: unsupported keyword "$ref"`},
		{map[string]any{"schema": `{"properties": {"a": {"oneOf": []}}}`}, `schema /properties/a: unsupported keyword "oneOf"`},
		{map[string]any{"schema": `{"items": 3}`}, "schema /items: must be an object or a boolean"},
		{map[string]any{"schema": `{"type": "date"}`}, `schema /: type "date" is not one of`},
		{map[string]any{"schema": `{"minLength": -1}`}, "minLength must be a non-negative integer"},
		{map[string]any{"schema": `{"pattern": "("}`}, "schema /: pattern:"},
	} {
		wantInvalid(t, stepdef.Action, "validate", tt.opts, tt.want)
	}
}

func TestSetBodyRestoresOriginalBody(t *testing.T) {
	m := message.New("changed")
	m[message.OriginalBody] = "original"
	out := process(t, "setbody", map[string]any{"language": "simple", "expression": "${header.metadata.originalbody}"}, m)
	if out[message.Body] != "original" {
		t.Errorf("body = %v, want the original body", out[message.Body])
	}
}
