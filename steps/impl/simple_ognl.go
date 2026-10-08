package impl

import (
	"fmt"
	"strings"
)

// OGNL: a method or property after a value, as in ${header.name.trim()},
// ${body.length}, ${variable.list.get(0)} or ${body[key]}. Camel calls the
// Java methods of the value; here the common methods of String, List and Map
// are known, and the others are an error.

type ognlStep struct {
	name  string
	call  bool
	args  []evalFn
	index evalFn
	safe  bool // ?. : nothing before it ends the chain
}

// ognl applies the methods and properties in suffix to the value of base.
func (c *compiler) ognl(b *block, base evalFn, suffix string) (evalFn, error) {
	if suffix == "" {
		return base, nil
	}
	steps, err := c.ognlSteps(b, suffix)
	if err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		v, err := base(e)
		if err != nil {
			return nil, err
		}
		for _, s := range steps {
			if v == nil {
				if s.safe {
					return nil, nil
				}
				return nil, fmt.Errorf("%s: cannot use %s: there is no value", b.src, s.describe())
			}
			if v, err = s.apply(e, v); err != nil {
				return nil, fmt.Errorf("%s: %w", b.src, err)
			}
		}
		return v, nil
	}, nil
}

func (s ognlStep) describe() string {
	switch {
	case s.index != nil:
		return "an index"
	case s.call:
		return s.name + "()"
	}
	return s.name
}

// ognlSteps reads .name, .name(args), ?.name and [index], one after the other.
func (c *compiler) ognlSteps(b *block, s string) ([]ognlStep, error) {
	var steps []ognlStep
	for s != "" {
		var st ognlStep
		switch {
		case strings.HasPrefix(s, "?."):
			st.safe = true
			s = s[2:]
		case s[0] == '.':
			s = s[1:]
		case s[0] == '[':
			inside, after, ok := matchBracket(s)
			if !ok {
				return nil, unsupported(b, "")
			}
			idx, err := c.keyName(b, inside)
			if err != nil {
				return nil, err
			}
			st.index = idx
			steps = append(steps, st)
			s = after
			continue
		default:
			return nil, unsupported(b, "")
		}
		i := 0
		for i < len(s) && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9' || s[i] == '_') {
			i++
		}
		if i == 0 {
			return nil, unsupported(b, "")
		}
		st.name, s = s[:i], s[i:]
		if strings.HasPrefix(s, "(") {
			inside, after, ok := matchParen(s)
			if !ok {
				return nil, unsupported(b, "")
			}
			st.call = true
			for _, tok := range splitArgs(inside, false, true) {
				fn, err := c.template(b, tok)
				if err != nil {
					return nil, err
				}
				st.args = append(st.args, fn)
			}
			if strings.TrimSpace(inside) == "" {
				st.args = nil
			}
			s = after
		}
		steps = append(steps, st)
	}
	return steps, nil
}

// splitArgs splits the arguments of a function at the commas outside quotes
// (as Camel's StringQuoteHelper.splitSafeQuote does). Quotes around an
// argument are taken off unless keepQuotes; with trim the spaces around an
// argument go.
func splitArgs(s string, keepQuotes, trim bool) []string {
	// The quotes that delimit an argument are written as marks first, so that the
	// spaces around an argument can go while the spaces in its quotes stay.
	const mark = '\x02'
	var parts []string
	var b strings.Builder
	var quote byte
	flush := func() {
		t := b.String()
		if trim {
			t = strings.TrimSpace(t)
		}
		var out strings.Builder
		for i := 0; i < len(t); i++ {
			if t[i] == mark {
				if keepQuotes {
					out.WriteByte(t[i+1])
				}
				i++
				continue
			}
			out.WriteByte(t[i])
		}
		parts = append(parts, out.String())
		b.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
			b.WriteByte(mark)
			b.WriteByte(c)
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
			b.WriteByte(mark)
			b.WriteByte(c)
		case quote == 0 && c == ',':
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return parts
}

// apply runs the step on v.
func (st ognlStep) apply(e *env, v any) (any, error) {
	if st.index != nil {
		k, err := st.index(e)
		if err != nil {
			return nil, err
		}
		return indexValue(v, render(k))
	}
	args := make([]string, len(st.args))
	for i, a := range st.args {
		x, err := a(e)
		if err != nil {
			return nil, err
		}
		args[i] = render(x)
	}

	switch x := v.(type) {
	case jmap:
		return mapMethod(x, st, args)
	case map[string]any:
		return mapMethod(x, st, args)
	case jlist:
		return listMethod([]any(x), st, args)
	case []any:
		return listMethod(x, st, args)
	}
	return stringMethod(render(v), st, args)
}

// indexValue is v[k]: an element of a list, a value of a map, a character of a text.
func indexValue(v any, k string) (any, error) {
	switch x := v.(type) {
	case jmap:
		return x[k], nil
	case map[string]any:
		return x[k], nil
	case jlist:
		return indexValue([]any(x), k)
	case []any:
		i, ok := asInt(k)
		if !ok || i < 0 || int(i) >= len(x) {
			return nil, fmt.Errorf("index %q is outside the list of %d elements", k, len(x))
		}
		return x[i], nil
	}
	return nil, fmt.Errorf("%T has no elements to index", v)
}

func mapMethod(m map[string]any, st ognlStep, args []string) (any, error) {
	if !st.call {
		if st.name == "size" || st.name == "length" {
			return int64(len(m)), nil
		}
		return m[st.name], nil
	}
	switch st.name {
	case "get":
		if len(args) == 1 {
			return m[args[0]], nil
		}
	case "containsKey":
		if len(args) == 1 {
			_, ok := m[args[0]]
			return ok, nil
		}
	case "size":
		return int64(len(m)), nil
	case "isEmpty":
		return len(m) == 0, nil
	case "toString":
		return javaString(m), nil
	}
	return nil, fmt.Errorf("the method %s(%d arguments) of a map is not supported", st.name, len(args))
}

func listMethod(l []any, st ognlStep, args []string) (any, error) {
	if !st.call {
		switch st.name {
		case "size", "length":
			return int64(len(l)), nil
		case "empty":
			return len(l) == 0, nil
		}
		return nil, fmt.Errorf("the property %s of a list is not supported", st.name)
	}
	switch st.name {
	case "size", "length":
		return int64(len(l)), nil
	case "isEmpty":
		return len(l) == 0, nil
	case "get":
		if len(args) == 1 {
			return indexValue(l, args[0])
		}
	case "contains":
		if len(args) == 1 {
			return listHas(l, args[0], false), nil
		}
	case "indexOf":
		if len(args) == 1 {
			for i, e := range l {
				if valuesEqual(e, args[0]) {
					return int64(i), nil
				}
			}
			return int64(-1), nil
		}
	case "toString":
		return javaList(l), nil
	}
	return nil, fmt.Errorf("the method %s(%d arguments) of a list is not supported", st.name, len(args))
}

func stringMethod(s string, st ognlStep, args []string) (any, error) {
	if !st.call {
		switch st.name {
		case "length":
			return int64(runeLen(s)), nil
		case "empty":
			return s == "", nil
		case "bytes":
			return []byte(s), nil
		}
		return nil, fmt.Errorf("the property %s of a text is not supported", st.name)
	}
	bad := func() (any, error) {
		return nil, fmt.Errorf("the method %s(%d arguments) of a text is not supported", st.name, len(args))
	}
	switch st.name {
	case "length":
		return int64(runeLen(s)), nil
	case "trim":
		// Camel cannot call trim with an argument, the platform ignores it.
		return javaTrim(s), nil
	case "strip":
		return strings.TrimSpace(s), nil
	case "isEmpty":
		return s == "", nil
	case "isBlank":
		return strings.TrimSpace(s) == "", nil
	case "toUpperCase":
		return strings.ToUpper(s), nil
	case "toLowerCase":
		return strings.ToLower(s), nil
	case "toString", "intern":
		return s, nil
	case "substring":
		rs := []rune(s)
		from, to := int64(0), int64(len(rs))
		var ok bool
		switch len(args) {
		case 1:
			if from, ok = asInt(args[0]); !ok {
				return bad()
			}
		case 2:
			var ok2 bool
			from, ok = asInt(args[0])
			to, ok2 = asInt(args[1])
			if !ok || !ok2 {
				return bad()
			}
		default:
			return bad()
		}
		if from < 0 || to > int64(len(rs)) || from > to {
			return nil, fmt.Errorf("substring(%d, %d) is outside the text of %d characters", from, to, len(rs))
		}
		return string(rs[from:to]), nil
	case "replaceAll", "replaceFirst":
		if len(args) != 2 {
			return bad()
		}
		re, err := javaRegexp(args[0])
		if err != nil {
			return nil, err
		}
		repl := javaReplacement(args[1])
		if st.name == "replaceAll" {
			return re.ReplaceAllString(s, repl), nil
		}
		loc := re.FindStringSubmatchIndex(s)
		if loc == nil {
			return s, nil
		}
		return s[:loc[0]] + string(re.ExpandString(nil, repl, s, loc)) + s[loc[1]:], nil
	case "replace":
		if len(args) != 2 {
			return bad()
		}
		return strings.ReplaceAll(s, args[0], args[1]), nil
	case "contains":
		if len(args) == 1 {
			return strings.Contains(s, args[0]), nil
		}
	case "startsWith":
		if len(args) == 1 {
			return strings.HasPrefix(s, args[0]), nil
		}
	case "endsWith":
		if len(args) == 1 {
			return strings.HasSuffix(s, args[0]), nil
		}
	case "equals":
		if len(args) == 1 {
			return s == args[0], nil
		}
	case "equalsIgnoreCase":
		if len(args) == 1 {
			return strings.EqualFold(s, args[0]), nil
		}
	case "indexOf":
		if len(args) == 1 {
			return int64(strings.Index(s, args[0])), nil
		}
	case "lastIndexOf":
		if len(args) == 1 {
			return int64(strings.LastIndex(s, args[0])), nil
		}
	case "charAt":
		if len(args) == 1 {
			rs := []rune(s)
			if i, ok := asInt(args[0]); ok && i >= 0 && int(i) < len(rs) {
				return string(rs[i]), nil
			}
			return nil, fmt.Errorf("charAt(%s) is outside the text of %d characters", args[0], len(rs))
		}
	case "concat":
		if len(args) == 1 {
			return s + args[0], nil
		}
	case "matches":
		if len(args) == 1 {
			re, err := javaRegexp("^(?:" + args[0] + ")$")
			if err != nil {
				return nil, err
			}
			return re.MatchString(s), nil
		}
	case "split":
		if len(args) == 1 {
			return splitRegexp(s, args[0])
		}
	}
	return bad()
}

// splitRegexp splits s at the matches of the regular expression sep and drops
// the empty strings at the end, as Java's String.split does.
func splitRegexp(s, sep string) (jlist, error) {
	re, err := javaRegexp(sep)
	if err != nil {
		return nil, err
	}
	parts := re.Split(s, -1)
	if len(parts) > 1 && parts[0] == "" && s != "" {
		if loc := re.FindStringIndex(s); loc != nil && loc[1] == 0 {
			parts = parts[1:] // Java drops an empty first string from a match of no width
		}
	}
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	l := make(jlist, len(parts))
	for i, p := range parts {
		l[i] = p
	}
	return l, nil
}
