package mapping

import (
	"fmt"
	"math/big"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"
	"unicode/utf8"
)

var extraBuiltins = map[string]int{"isEmpty": 1, "replace": 3, "substring": 3, "contains": 2, "startsWith": 2, "endsWith": 2, "split": 2, "join": 2, "min": 1, "max": 1, "abs": 1, "distinct": 1, "keys": 1, "values": 1, "formatDate": 4}

func init() {
	for n, a := range extraBuiltins {
		builtins[n] = a
	}
}
func (b *budget) work(n int) error {
	if n > b.limits.Operations-b.ops {
		return fmt.Errorf("mapping operation limit exceeded")
	}
	b.ops += n
	return b.ctx.Err()
}
func extraCall(name string, a []any, b *budget) (any, error) {
	bad := func() (any, error) { return nil, fmt.Errorf("%s: invalid argument type or value", name) }
	for i, v := range a {
		if h, ok := v.(headerValues); ok {
			n, e := h.materialize(b)
			if e != nil {
				return nil, e
			}
			a[i] = n
		}
		if s, ok := a[i].(string); ok {
			if e := b.work(len(s)); e != nil {
				return nil, e
			}
		}
	}
	switch name {
	case "isEmpty":
		switch v := a[0].(type) {
		case nil, missing:
			return true, nil
		case string:
			return len(v) == 0, nil
		case []any:
			return len(v) == 0, nil
		case object:
			return len(v) == 0, nil
		}
		return false, nil
	case "abs":
		if n, ok := a[0].(*big.Rat); ok {
			return boundedNumber(new(big.Rat).Abs(n), b.limits)
		}
	case "min", "max":
		ar, ok := a[0].([]any)
		if !ok || len(ar) == 0 {
			return bad()
		}
		var result *big.Rat
		for _, v := range ar {
			if e := b.tick(); e != nil {
				return nil, e
			}
			n, ok := v.(*big.Rat)
			if !ok {
				return bad()
			}
			if result == nil || name == "min" && n.Cmp(result) < 0 || name == "max" && n.Cmp(result) > 0 {
				result = n
			}
		}
		return new(big.Rat).Set(result), nil
	case "keys", "values":
		o, ok := a[0].(object)
		if !ok {
			return bad()
		}
		out := []any{}
		for _, m := range o {
			if e := b.node(0); e != nil {
				return nil, e
			}
			if name == "keys" {
				out = append(out, m.key)
			} else {
				out = append(out, m.value)
			}
		}
		return out, nil
	case "distinct":
		ar, ok := a[0].([]any)
		if !ok {
			return bad()
		}
		out := []any{}
		for _, v := range ar {
			found := false
			for _, prior := range out {
				eq, e := equal(v, prior, b)
				if e != nil {
					return nil, e
				}
				if eq {
					found = true
					break
				}
			}
			if !found {
				if e := b.node(0); e != nil {
					return nil, e
				}
				out = append(out, v)
			}
		}
		return out, nil
	case "join":
		ar, ok := a[0].([]any)
		sep, ok2 := a[1].(string)
		if !ok || !ok2 {
			return bad()
		}
		var out strings.Builder
		for i, v := range ar {
			s, ok := v.(string)
			if !ok {
				return bad()
			}
			if e := b.work(len(s) + 1); e != nil {
				return nil, e
			}
			extra := len(s)
			if i > 0 {
				extra += len(sep)
			}
			if extra > b.limits.Bytes-out.Len() {
				return nil, fmt.Errorf("string size limit exceeded")
			}
			if i > 0 {
				out.WriteString(sep)
			}
			out.WriteString(s)
		}
		return out.String(), nil
	case "substring":
		s, ok := a[0].(string)
		start, ok2 := integerIndex(a[1])
		end, ok3 := integerIndex(a[2])
		if !ok || !ok2 || !ok3 || start < 0 || end < start || end > int64(utf8.RuneCountInString(s)) {
			return bad()
		}
		return string([]rune(s)[start:end]), nil
	case "formatDate":
		ss := make([]string, 4)
		for i, v := range a {
			s, ok := v.(string)
			if !ok {
				return bad()
			}
			ss[i] = s
		}
		// Require numeric RFC3339-style or numeric Go-layout offsets, never local time.
		hasOffset := false
		for _, token := range []string{"Z07", "-07"} {
			if strings.Contains(ss[1], token) {
				hasOffset = true
			}
		}
		if !validDateLayout(ss[1]) || !validDateLayout(ss[2]) || !hasOffset || strings.Contains(ss[1], "MST") || ss[1] == "" || ss[2] == "" || ss[3] == "" || ss[3] == "Local" {
			return bad()
		}
		tm, e := time.Parse(ss[1], ss[0])
		if e != nil {
			return nil, fmt.Errorf("formatDate: invalid input or layout")
		}
		zone, e := time.LoadLocation(ss[3])
		if e != nil {
			return nil, fmt.Errorf("formatDate: invalid zone")
		}
		out := tm.In(zone).Format(ss[2])
		if len(out) > b.limits.Bytes {
			return nil, fmt.Errorf("string size limit exceeded")
		}
		return out, nil
	default:
		s, ok := a[0].(string)
		part, ok2 := a[1].(string)
		if !ok || !ok2 {
			return bad()
		}
		switch name {
		case "contains":
			return strings.Contains(s, part), nil
		case "startsWith":
			return strings.HasPrefix(s, part), nil
		case "endsWith":
			return strings.HasSuffix(s, part), nil
		case "replace":
			replacement, ok := a[2].(string)
			if !ok || part == "" {
				return bad()
			}
			count := strings.Count(s, part)
			if len(replacement) > len(part) && count > (b.limits.Bytes-len(s))/(len(replacement)-len(part)) {
				return nil, fmt.Errorf("string size limit exceeded")
			}
			return strings.ReplaceAll(s, part, replacement), nil
		case "split":
			if part == "" {
				return bad()
			}
			count := strings.Count(s, part) + 1
			if count > b.limits.Nodes-b.nodes {
				return nil, fmt.Errorf("mapping node limit exceeded")
			}
			parts := strings.Split(s, part)
			out := make([]any, 0, len(parts))
			for _, v := range parts {
				if e := b.node(0); e != nil {
					return nil, e
				}
				out = append(out, v)
			}
			return out, nil
		}
	}
	return bad()
}
func integerIndex(v any) (int64, bool) {
	n, ok := v.(*big.Rat)
	if !ok || !n.IsInt() || !n.Num().IsInt64() {
		return 0, false
	}
	return n.Num().Int64(), true
}

// Layouts use Go's documented numeric/text fields; other letters would silently
// become literals in time.Parse, often hiding accidental Java-style patterns.
func validDateLayout(s string) bool {
	fields := []string{"January", "Monday", "Z07:00:00", "-07:00:00", "Z070000", "-070000", "Z07:00", "-07:00", "Z0700", "-0700", "2006", "MST", "Jan", "Mon", "Z07", "-07", "__2", "002", "01", "02", "03", "04", "05", "06", "15", "PM", "pm", "_2", "1", "2", "3", "4", "5"}
	found := false
	for _, f := range fields {
		if strings.Contains(s, f) {
			found = true
			s = strings.ReplaceAll(s, f, "")
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) && r != 'T' && r != 'Z' {
			return false
		}
	}
	return found
}
