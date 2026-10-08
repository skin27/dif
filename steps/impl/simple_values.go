package impl

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The values the simple language computes with are strings, bools, int64,
// float64, nil (no value), jlist and the decoded JSON of a message (map[string]any
// and []any). Only a template turns them into text, with render.

// jlist is a list a simple function produced (split, jsonpath, ...). A template
// writes it as Java does, [a, b, c], where a list from the message (decoded
// JSON) is written as JSON.
type jlist []any

// render is the text of a value in a template: nothing for nil, Java's form for
// a jlist, and text (JSON for decoded JSON) for the rest.
func render(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case jlist:
		return javaString(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return text(v)
}

// javaString writes a value as Java's toString does: [a, b] for a list and
// {k=v, k2=v2} for a map (its keys in order), the elements without quotes.
func javaString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case jlist:
		return javaList([]any(x))
	case []any:
		return javaList(x)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k + "=" + javaString(x[k]))
		}
		b.WriteByte('}')
		return b.String()
	}
	return render(v)
}

func javaList(l []any) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, e := range l {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(javaString(e))
	}
	b.WriteByte(']')
	return b.String()
}

// asString converts a value to a string; ok is false for nil.
func asString(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	return render(v), true
}

// asNumber converts a value to a number: ints stay ints. ok is false when v
// is no number and no text that reads as one.
func asNumber(v any) (n float64, isInt bool, ok bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true, true
	case int64:
		return float64(x), true, true
	case float64:
		return x, x == math.Trunc(x) && math.Abs(x) < 1e15, true
	case string:
		return parseNumber(x)
	case []byte:
		return parseNumber(string(x))
	}
	return 0, false, false
}

// parseNumber reads s, ignoring spaces around it, as an integer or a decimal.
func parseNumber(s string) (n float64, isInt bool, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, false
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return float64(i), true, true
	}
	// Java reads a comma as no decimal separator, so does Go.
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) && !strings.ContainsAny(s, "xXpP_") {
		return f, false, true
	}
	return 0, false, false
}

// asInt converts a value to an integer (a decimal is truncated).
func asInt(v any) (int64, bool) {
	n, _, ok := asNumber(v)
	if !ok {
		return 0, false
	}
	return int64(n), true
}

// asBool converts a value to a bool: a bool, or the text true or false in
// any case.
func asBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// number is an integer value if n is whole and isInt, else a decimal.
func number(n float64, isInt bool) any {
	if isInt {
		return int64(n)
	}
	return n
}

// elements iterates a value the way Camel does: the elements of a list, the
// comma separated parts of a text, and a single element otherwise. nil has no
// elements.
func elements(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case jlist:
		return []any(x)
	case []any:
		return x
	case []string:
		l := make([]any, len(x))
		for i, s := range x {
			l[i] = s
		}
		return l
	case string:
		return splitString(x, ",", true)
	case []byte:
		return splitString(string(x), ",", true)
	}
	return []any{v}
}

// splitString splits s at every sep (a plain text) and drops the empty
// strings at the end, as Java's String.split does for a plain separator.
func splitString(s, sep string, trim bool) []any {
	parts := strings.Split(s, sep)
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	l := make([]any, len(parts))
	for i, p := range parts {
		if trim {
			p = strings.TrimSpace(p)
		}
		l[i] = p
	}
	return l
}

// javaRegexp compiles a Java regular expression. Go's syntax is the same for
// what flows use (classes, groups, quantifiers); what it lacks, such as a
// lookahead, is an error.
func javaRegexp(expr string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("regular expression %q: %w", expr, err)
	}
	return re, nil
}

// javaReplacement turns a Java replacement text, in which $1 is a group and \$ a
// dollar, into Go's, in which ${1} is a group and $$ a dollar.
func javaReplacement(repl string) string {
	var b strings.Builder
	for i := 0; i < len(repl); i++ {
		switch c := repl[i]; {
		case c == '\\' && i+1 < len(repl):
			i++
			if repl[i] == '$' {
				b.WriteString("$$")
			} else {
				b.WriteByte(repl[i])
			}
		case c == '$':
			j := i + 1
			for j < len(repl) && repl[j] >= '0' && repl[j] <= '9' {
				j++
			}
			if j == i+1 {
				b.WriteString("$$")
			} else {
				b.WriteString("${" + repl[i+1:j] + "}")
				i = j - 1
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
