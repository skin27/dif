package impl

import "dif/internal/jsonschema"

type jsonSchema struct{ *jsonschema.Schema }

func compileJSONSchema(data []byte) (*jsonSchema, error) {
	s, err := jsonschema.Compile(data)
	if err != nil {
		return nil, err
	}
	return &jsonSchema{s}, nil
}
func (s *jsonSchema) validate(v any, at string, problems *[]string) { s.Validate(v, at, problems) }
