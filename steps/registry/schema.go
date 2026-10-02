package registry

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	stepdef "dif/steps/definition"
)

// schema is a compiled JSON Schema for a step's options. Only a small subset
// of JSON Schema is supported: an object of scalar properties with type,
// enum, default and minimum, plus required and additionalProperties.
// A schema using any other keyword is rejected when it is compiled, so it can
// never silently rely on something that is not checked.
type schema struct {
	description string
	props       map[string]*property
	names       []string // property names, sorted
	required    []string
	additional  bool // whether options not in props are allowed
}

type property struct {
	description string
	typ         string // string, integer, number or boolean
	enum        []any  // allowed values, coerced to typ; nil for any
	def         any    // default, coerced to typ; nil for none
	minimum     *float64
}

var (
	ignored     = []string{"$schema", "$id", "$comment", "title", "description"}
	objectWords = []string{"type", "properties", "required", "additionalProperties"}
	scalarWords = []string{"type", "enum", "default", "minimum"}
	scalarTypes = []string{"string", "integer", "number", "boolean"}
)

// compile parses a JSON Schema document describing an object of options.
func compile(data []byte) (*schema, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := checkKeywords(raw, objectWords, "schema"); err != nil {
		return nil, err
	}
	if raw["type"] != "object" {
		return nil, fmt.Errorf("schema: type must be \"object\"")
	}

	s := &schema{props: map[string]*property{}, additional: true}
	s.description, _ = raw["description"].(string)
	props, _ := raw["properties"].(map[string]any)
	if raw["properties"] != nil && props == nil {
		return nil, fmt.Errorf("schema: properties must be an object")
	}
	for name, v := range props {
		def, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema: property %s must be an object", name)
		}
		p, err := compileProperty(def)
		if err != nil {
			return nil, fmt.Errorf("schema: property %s: %w", name, err)
		}
		s.props[name] = p
		s.names = append(s.names, name)
	}
	slices.Sort(s.names)

	if req, ok := raw["required"]; ok {
		list, _ := req.([]any)
		for _, v := range list {
			name, ok := v.(string)
			if !ok || s.props[name] == nil {
				return nil, fmt.Errorf("schema: required %v is not a property", v)
			}
			s.required = append(s.required, name)
		}
	}
	if add, ok := raw["additionalProperties"]; ok {
		b, isBool := add.(bool)
		if !isBool {
			return nil, fmt.Errorf("schema: additionalProperties must be a boolean")
		}
		s.additional = b
	}
	return s, nil
}

func compileProperty(raw map[string]any) (*property, error) {
	if err := checkKeywords(raw, scalarWords, "property"); err != nil {
		return nil, err
	}
	typ, _ := raw["type"].(string)
	if !slices.Contains(scalarTypes, typ) {
		return nil, fmt.Errorf("type must be one of %s", strings.Join(scalarTypes, ", "))
	}
	p := &property{typ: typ}
	p.description, _ = raw["description"].(string)

	if e, ok := raw["enum"]; ok {
		list, _ := e.([]any)
		if len(list) == 0 {
			return nil, fmt.Errorf("enum must be a non-empty array")
		}
		for _, v := range list {
			c, ok := coerce(v, typ)
			if !ok {
				return nil, fmt.Errorf("enum value %s is not a %s", show(v), typ)
			}
			p.enum = append(p.enum, c)
		}
	}
	if d, ok := raw["default"]; ok {
		c, ok := coerce(d, typ)
		if !ok {
			return nil, fmt.Errorf("default %s is not a %s", show(d), typ)
		}
		p.def = c
	}
	if m, ok := raw["minimum"]; ok {
		f, isNum := m.(float64)
		if !isNum || (typ != "integer" && typ != "number") {
			return nil, fmt.Errorf("minimum must be a number on a numeric property")
		}
		p.minimum = &f
	}
	return p, nil
}

func checkKeywords(raw map[string]any, allowed []string, what string) error {
	for k := range raw {
		if !slices.Contains(allowed, k) && !slices.Contains(ignored, k) {
			return fmt.Errorf("%s: unsupported keyword %q", what, k)
		}
	}
	return nil
}

// validate checks opts against the schema and returns them with defaults
// applied and every value converted to its declared type. All problems are
// reported in one error.
func (s *schema) validate(opts map[string]any) (stepdef.Params, error) {
	params := make(stepdef.Params, len(s.props))
	var problems []string

	for _, name := range s.names {
		p := s.props[name]
		v, set := opts[name]
		if !set || v == nil {
			if p.def != nil {
				params[name] = p.def
			}
			continue
		}
		c, ok := coerce(v, p.typ)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("option %s: want %s, got %s", name, p.typ, show(v)))
		case p.enum != nil && !slices.Contains(p.enum, c):
			problems = append(problems, fmt.Sprintf("option %s: %s is not one of %s", name, show(c), showAll(p.enum)))
		case p.minimum != nil && toFloat(c) < *p.minimum:
			problems = append(problems, fmt.Sprintf("option %s: %s is less than %v", name, show(c), *p.minimum))
		default:
			params[name] = c
		}
	}

	var unknown []string
	for name := range opts {
		if s.props[name] == nil {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	for _, name := range unknown {
		if !s.additional {
			problems = append(problems, "unknown option "+name)
		} else {
			params[name] = opts[name]
		}
	}

	for _, name := range s.required {
		if _, ok := params[name]; !ok && !slices.ContainsFunc(problems, func(p string) bool {
			return strings.HasPrefix(p, "option "+name+":")
		}) {
			problems = append(problems, "missing required option "+name)
		}
	}

	if problems != nil {
		return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return params, nil
}

// coerce converts v to typ. DIL converted from XML often holds numbers and
// booleans as strings, and strings as numbers, so those are converted too.
func coerce(v any, typ string) (any, bool) {
	switch typ {
	case "string":
		switch x := v.(type) {
		case string:
			return x, true
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64), true
		case bool:
			return strconv.FormatBool(x), true
		}
	case "integer":
		switch x := v.(type) {
		case float64:
			if x == math.Trunc(x) && math.Abs(x) <= math.MaxInt32 {
				return int(x), true
			}
		case int:
			return x, true
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
				return n, true
			}
		}
	case "number":
		switch x := v.(type) {
		case float64:
			return x, true
		case int:
			return float64(x), true
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
				return f, true
			}
		}
	case "boolean":
		switch x := v.(type) {
		case bool:
			return x, true
		case string:
			if b, err := strconv.ParseBool(strings.TrimSpace(x)); err == nil {
				return b, true
			}
		}
	}
	return nil, false
}

func toFloat(v any) float64 {
	if n, ok := v.(int); ok {
		return float64(n)
	}
	f, _ := v.(float64)
	return f
}

func show(v any) string {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	return fmt.Sprint(v)
}

func showAll(vs []any) string {
	s := make([]string, len(vs))
	for i, v := range vs {
		s[i] = show(v)
	}
	return strings.Join(s, ", ")
}
