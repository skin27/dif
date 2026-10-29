package mapping

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"dif/internal/jsonschema"
)

// schema reuses the legacy subset's structural compiler while evaluating its
// numeric constraints against exact rationals. Diagnostics never contain values.
type schema struct {
	raw      any
	patterns map[string]*regexp.Regexp
}

func compileSchema(src string, l Limits) (*schema, error) {
	if src == "" {
		return nil, nil
	}
	b := &budget{ctx: context.Background(), limits: l}
	raw, e := readJSON([]byte(src), b)
	if e != nil {
		return nil, fmt.Errorf("invalid schema JSON: %w", e)
	}
	if _, e = jsonschema.Compile([]byte(src)); e != nil {
		return nil, fmt.Errorf("invalid or unsupported JSON Schema")
	}
	s := &schema{raw: raw, patterns: map[string]*regexp.Regexp{}}
	s.collect(raw)
	return s, nil
}
func (s *schema) collect(raw any) {
	o, ok := raw.(object)
	if !ok {
		return
	}
	if p, ok := o.get("pattern").(string); ok {
		s.patterns[p] = regexp.MustCompile(p)
	}
	if ps, ok := o.get("properties").(object); ok {
		for _, m := range ps {
			s.collect(m.value)
		}
	}
	s.collect(o.get("items"))
	s.collect(o.get("additionalProperties"))
}
func (s *schema) check(v any, b *budget) error {
	if s == nil {
		return nil
	}
	return s.at(s.raw, v, "", b)
}
func (s *schema) at(raw, v any, path string, b *budget) error {
	if h, ok := v.(headerValues); ok {
		value, e := h.materialize(b)
		if e != nil {
			return e
		}
		v = value
	}
	if e := b.tick(); e != nil {
		return e
	}
	fail := func(k string) error { return fmt.Errorf("schema %s: %s constraint failed", pointer(path), k) }
	if allowed, ok := raw.(bool); ok {
		if !allowed {
			return fail("false")
		}
		return nil
	}
	o := raw.(object)
	if t := o.get("type"); !isMissing(t) {
		valid := false
		matches := func(t any) bool {
			name, _ := t.(string)
			got := typeName(v)
			return name == got || name == "number" && got == "integer"
		}
		if ts, ok := t.([]any); ok {
			for _, t := range ts {
				valid = valid || matches(t)
			}
		} else {
			valid = matches(t)
		}
		if !valid {
			return fail("type")
		}
	}
	if es, ok := o.get("enum").([]any); ok {
		found := false
		for _, ev := range es {
			eq, e := equal(v, ev, b)
			if e != nil {
				return e
			}
			found = found || eq
		}
		if !found {
			return fail("enum")
		}
	}
	if c := o.get("const"); !isMissing(c) {
		eq, e := equal(v, c, b)
		if e != nil {
			return e
		}
		if !eq {
			return fail("const")
		}
	}
	switch x := v.(type) {
	case *big.Rat:
		for _, k := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
			if n, ok := o.get(k).(*big.Rat); ok {
				c := x.Cmp(n)
				if k == "minimum" && c < 0 || k == "maximum" && c > 0 || k == "exclusiveMinimum" && c <= 0 || k == "exclusiveMaximum" && c >= 0 {
					return fail(k)
				}
			}
		}
	case string:
		if e := schemaLength(o, "minLength", "maxLength", utf8.RuneCountInString(x), fail); e != nil {
			return e
		}
		if p, ok := o.get("pattern").(string); ok && !s.patterns[p].MatchString(x) {
			return fail("pattern")
		}
	case []any:
		if e := schemaLength(o, "minItems", "maxItems", len(x), fail); e != nil {
			return e
		}
		if item := o.get("items"); !isMissing(item) {
			for i, v := range x {
				if e := s.at(item, v, path+"/"+strconv.Itoa(i), b); e != nil {
					return e
				}
			}
		}
	case object:
		if req, ok := o.get("required").([]any); ok {
			for _, key := range req {
				v, e := b.lookup(x, key.(string))
				if e != nil {
					return e
				}
				if isMissing(v) {
					return fmt.Errorf("schema %s: required property missing", pointer(path+"/"+escape(key.(string))))
				}
			}
		}
		props, _ := o.get("properties").(object)
		additional := o.get("additionalProperties")
		for _, m := range x {
			rule, e := b.lookup(props, m.key)
			if e != nil {
				return e
			}
			if isMissing(rule) {
				rule = additional
			}
			if !isMissing(rule) {
				if e := s.at(rule, m.value, path+"/"+escape(m.key), b); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func schemaLength(o object, min, max string, n int, fail func(string) error) error {
	v := new(big.Rat).SetInt64(int64(n))
	if m, ok := o.get(min).(*big.Rat); ok && v.Cmp(m) < 0 {
		return fail(min)
	}
	if m, ok := o.get(max).(*big.Rat); ok && v.Cmp(m) > 0 {
		return fail(max)
	}
	return nil
}
func typeName(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case *big.Rat:
		if x.IsInt() {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case object:
		return "object"
	}
	return "unsupported"
}
func pointer(s string) string {
	if s == "" {
		return "/"
	}
	return s
}
func escape(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }
