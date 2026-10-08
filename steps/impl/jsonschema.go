package impl

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// jsonSchema is a compiled JSON Schema that a JSON value, such as a body, is
// validated against. Only a subset of JSON Schema is supported; a schema
// using any other keyword is rejected when it is compiled, so it can never
// silently rely on something that is not checked.
type jsonSchema struct {
	never bool     // the schema false: no value is valid
	types []string // allowed types; nil for any

	properties map[string]*jsonSchema
	names      []string // property names, sorted
	required   []string
	additional *jsonSchema // schema of properties not in properties; nil for any
	items      *jsonSchema // schema of every array item; nil for any

	enum     []any
	constant any
	hasConst bool

	minimum, maximum, exclusiveMinimum, exclusiveMaximum *float64
	minLength, maxLength, minItems, maxItems             *int
	pattern                                              *regexp.Regexp
}

var (
	jsonSchemaIgnored = []string{"$schema", "$id", "$comment", "title", "description", "default", "examples"}
	jsonTypes         = []string{"object", "array", "string", "number", "integer", "boolean", "null"}
)

// compileJSONSchema parses a JSON Schema document.
func compileJSONSchema(data []byte) (*jsonSchema, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("schema is not JSON: %w", err)
	}
	return compileSchemaAt(raw, "")
}

// compileSchemaAt compiles raw, the schema at JSON Pointer at in the document.
func compileSchemaAt(raw any, at string) (*jsonSchema, error) {
	if b, ok := raw.(bool); ok {
		return &jsonSchema{never: !b}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, schemaError(fmt.Sprintf("schema %s: must be an object or a boolean", pointer(at)))
	}

	s := &jsonSchema{}
	keys := slices.Sorted(maps.Keys(obj))
	for _, k := range keys {
		if slices.Contains(jsonSchemaIgnored, k) {
			continue
		}
		if err := s.compileKeyword(k, obj[k], at); err != nil {
			if _, nested := err.(schemaError); nested {
				return nil, err
			}
			return nil, schemaError(fmt.Sprintf("schema %s: %v", pointer(at), err))
		}
	}
	return s, nil
}

// schemaError is an error that already says where in the schema it is.
type schemaError string

func (e schemaError) Error() string { return string(e) }

// compileKeyword sets keyword k with value v of the schema at at.
func (s *jsonSchema) compileKeyword(k string, v any, at string) error {
	var err error
	switch k {
	case "type":
		s.types, err = schemaTypes(v)
	case "properties":
		props, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("properties must be an object")
		}
		s.properties = map[string]*jsonSchema{}
		s.names = slices.Sorted(maps.Keys(props))
		for _, name := range s.names {
			if s.properties[name], err = compileSchemaAt(props[name], at+"/properties/"+escapePointer(name)); err != nil {
				return err
			}
		}
	case "required":
		list, ok := v.([]any)
		for _, r := range list {
			name, isString := r.(string)
			ok = ok && isString
			s.required = append(s.required, name)
		}
		if !ok {
			return fmt.Errorf("required must be an array of strings")
		}
	case "additionalProperties":
		s.additional, err = compileSchemaAt(v, at+"/additionalProperties")
	case "items":
		s.items, err = compileSchemaAt(v, at+"/items")
	case "enum":
		list, ok := v.([]any)
		if !ok || len(list) == 0 {
			return fmt.Errorf("enum must be a non-empty array")
		}
		s.enum = list
	case "const":
		s.constant, s.hasConst = v, true
	case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
		f, ok := v.(float64)
		if !ok {
			return fmt.Errorf("%s must be a number", k)
		}
		switch k {
		case "minimum":
			s.minimum = &f
		case "maximum":
			s.maximum = &f
		case "exclusiveMinimum":
			s.exclusiveMinimum = &f
		default:
			s.exclusiveMaximum = &f
		}
	case "minLength", "maxLength", "minItems", "maxItems":
		f, ok := v.(float64)
		if !ok || f < 0 || f != math.Trunc(f) || f > math.MaxInt32 {
			return fmt.Errorf("%s must be a non-negative integer", k)
		}
		n := int(f)
		switch k {
		case "minLength":
			s.minLength = &n
		case "maxLength":
			s.maxLength = &n
		case "minItems":
			s.minItems = &n
		default:
			s.maxItems = &n
		}
	case "pattern":
		p, ok := v.(string)
		if !ok {
			return fmt.Errorf("pattern must be a string")
		}
		if s.pattern, err = regexp.Compile(p); err != nil {
			return fmt.Errorf("pattern: %w", err)
		}
	default:
		return fmt.Errorf("unsupported keyword %q", k)
	}
	return err
}

func schemaTypes(v any) ([]string, error) {
	var list []any
	switch x := v.(type) {
	case string:
		list = []any{x}
	case []any:
		list = x
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("type must be a type name or a non-empty array of them")
	}
	types := make([]string, len(list))
	for i, t := range list {
		name, _ := t.(string)
		if !slices.Contains(jsonTypes, name) {
			return nil, fmt.Errorf("type %s is not one of %s", jsonText(t), strings.Join(jsonTypes, ", "))
		}
		types[i] = name
	}
	return types, nil
}

// validate checks v, a decoded JSON value at JSON Pointer at, and adds what
// is wrong with it to problems.
func (s *jsonSchema) validate(v any, at string, problems *[]string) {
	add := func(format string, args ...any) {
		*problems = append(*problems, pointer(at)+": "+fmt.Sprintf(format, args...))
	}
	if s.never {
		add("not allowed")
		return
	}
	got := schemaTypeOf(v)
	if s.types != nil && !slices.ContainsFunc(s.types, func(t string) bool { return t == got || t == "number" && got == "integer" }) {
		add("want %s, got %s", strings.Join(s.types, " or "), got)
		return
	}
	if s.enum != nil && !slices.ContainsFunc(s.enum, func(e any) bool { return reflect.DeepEqual(e, v) }) {
		add("%s is not one of %s", jsonText(v), jsonText(s.enum))
	}
	if s.hasConst && !reflect.DeepEqual(s.constant, v) {
		add("want %s, got %s", jsonText(s.constant), jsonText(v))
	}

	switch x := v.(type) {
	case float64:
		if s.minimum != nil && x < *s.minimum {
			add("%v is less than %v", x, *s.minimum)
		}
		if s.maximum != nil && x > *s.maximum {
			add("%v is greater than %v", x, *s.maximum)
		}
		if s.exclusiveMinimum != nil && x <= *s.exclusiveMinimum {
			add("%v is not greater than %v", x, *s.exclusiveMinimum)
		}
		if s.exclusiveMaximum != nil && x >= *s.exclusiveMaximum {
			add("%v is not less than %v", x, *s.exclusiveMaximum)
		}
	case string:
		n := utf8.RuneCountInString(x)
		if s.minLength != nil && n < *s.minLength {
			add("length %d is less than %d", n, *s.minLength)
		}
		if s.maxLength != nil && n > *s.maxLength {
			add("length %d is greater than %d", n, *s.maxLength)
		}
		if s.pattern != nil && !s.pattern.MatchString(x) {
			add("%s does not match %s", strconv.Quote(x), s.pattern)
		}
	case []any:
		if s.minItems != nil && len(x) < *s.minItems {
			add("%d items, want at least %d", len(x), *s.minItems)
		}
		if s.maxItems != nil && len(x) > *s.maxItems {
			add("%d items, want at most %d", len(x), *s.maxItems)
		}
		if s.items != nil {
			for i, item := range x {
				s.items.validate(item, at+"/"+strconv.Itoa(i), problems)
			}
		}
	case map[string]any:
		for _, name := range s.required {
			if _, ok := x[name]; !ok {
				add("missing required property %s", name)
			}
		}
		for _, name := range s.names {
			if pv, ok := x[name]; ok {
				s.properties[name].validate(pv, at+"/"+escapePointer(name), problems)
			}
		}
		if s.additional != nil {
			var extra []string
			for name := range x {
				if s.properties[name] == nil {
					extra = append(extra, name)
				}
			}
			slices.Sort(extra)
			for _, name := range extra {
				s.additional.validate(x[name], at+"/"+escapePointer(name), problems)
			}
		}
	}
}

// schemaTypeOf returns the JSON Schema type of a decoded JSON value; a whole
// number is an integer.
func schemaTypeOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	}
	return "object"
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// pointer shows the JSON Pointer at; the root, "", as "/".
func pointer(at string) string {
	if at == "" {
		return "/"
	}
	return at
}

func escapePointer(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}
