package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// validateAction checks a JSON body against a JSON Schema, given inline or
// in a file. A valid message passes on unchanged; an invalid one fails with
// every problem found, each at its JSON Pointer.
type validateAction struct {
	schema *jsonSchema
}

func newValidateAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	inline, file := p["schema"].(string), p["schemaFile"].(string)
	var data []byte
	switch {
	case (inline == "") == (file == ""):
		return nil, fmt.Errorf("set one of the options schema and schemaFile")
	case inline != "":
		data = []byte(inline)
	default:
		var err error
		if data, err = os.ReadFile(file); err != nil {
			return nil, fmt.Errorf("option schemaFile: %w", err)
		}
	}
	s, err := compileJSONSchema(data)
	if err != nil {
		return nil, err
	}
	return validateAction{s}, nil
}

func (a validateAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	var v any
	if err := json.Unmarshal(bytesOf(m[message.Body]), &v); err != nil {
		return nil, fmt.Errorf("body is not JSON: %w", err)
	}
	var problems []string
	a.schema.validate(v, "", &problems)
	if problems != nil {
		return nil, fmt.Errorf("body is not valid: %s", strings.Join(problems, "; "))
	}
	return m, nil
}
