package impl

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// jsonPath is a compiled JSONPath expression of the subset DIF supports: $
// followed by .name, ['name'], [n] (negative counts from the end), .* and [*].
type jsonPath []jsonStep

type jsonStep struct {
	key   string // object member; used when !index and !all
	n     int    // array index; used when index
	index bool
	all   bool // * : every member or element
}

func compileJSONPath(expr string) (jsonPath, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(expr), "$")
	if !ok {
		return nil, unsupportedJSONPath(expr)
	}
	var p jsonPath
	for rest != "" {
		var s jsonStep
		switch {
		case strings.HasPrefix(rest, ".."):
			return nil, unsupportedJSONPath(expr)
		case strings.HasPrefix(rest, ".["): // $.a.[*] is $.a[*]
			rest = rest[1:]
			continue
		case rest[0] == '.':
			end := strings.IndexAny(rest[1:], ".[") + 1
			if end == 0 {
				end = len(rest)
			}
			s.key, rest = rest[1:end], rest[end:]
			s.all = s.key == "*"
			if s.key == "" {
				return nil, unsupportedJSONPath(expr)
			}
		case rest[0] == '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, unsupportedJSONPath(expr)
			}
			sel := strings.TrimSpace(rest[1:end])
			rest = rest[end+1:]
			if n, err := strconv.Atoi(sel); err == nil {
				s.n, s.index = n, true
			} else if sel == "*" {
				s.all = true
			} else if q := unquote(sel); q != sel && q != "" {
				s.key = q
			} else {
				return nil, unsupportedJSONPath(expr)
			}
		default:
			return nil, unsupportedJSONPath(expr)
		}
		p = append(p, s)
	}
	return p, nil
}

func unsupportedJSONPath(expr string) error {
	return fmt.Errorf("unsupported jsonpath %q; DIF supports $ followed by .name, ['name'], [n], .* and [*]", expr)
}

// definite reports whether the path selects at most one value: it has no *.
func (p jsonPath) definite() bool {
	for _, s := range p {
		if s.all {
			return false
		}
	}
	return true
}

// eval returns the values the path selects in v, a decoded JSON document.
// Members of an object are visited in key order.
func (p jsonPath) eval(v any) []any {
	cur := []any{v}
	for _, s := range p {
		var next []any
		for _, c := range cur {
			switch x := c.(type) {
			case map[string]any:
				if s.all {
					for _, k := range slices.Sorted(maps.Keys(x)) {
						next = append(next, x[k])
					}
				} else if v, ok := x[s.key]; ok && !s.index {
					next = append(next, v)
				}
			case []any:
				if s.all {
					next = append(next, x...)
				} else if i := s.n; s.index {
					if i < 0 {
						i += len(x)
					}
					if i >= 0 && i < len(x) {
						next = append(next, x[i])
					}
				}
			}
		}
		cur = next
	}
	return cur
}

// decodeJSON returns body as decoded JSON: as is when it already is, else
// parsed from its text.
func decodeJSON(body any) (any, error) {
	switch body.(type) {
	case map[string]any, []any:
		return body, nil
	}
	var v any
	if err := json.Unmarshal(bytesOf(body), &v); err != nil {
		return nil, fmt.Errorf("body is not JSON: %w", err)
	}
	return v, nil
}

// matchJSON reports whether p selects a value in body other than null or
// false. A body that is not JSON matches nothing.
func (p jsonPath) matchJSON(body any) bool {
	v, err := decodeJSON(body)
	if err != nil {
		return false
	}
	for _, x := range p.eval(v) {
		if x != nil && x != false {
			return true
		}
	}
	return false
}
