package impl

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"sort"
	"strings"
	"unicode"
)

// The functions of Camel's simple language that flows use, with the argument
// order and defaults of camel-core-languages (functions/*FunctionFactory.java).
// An argument is a template of its own; a function whose argument is left out
// works on the body.

type (
	funcDef func(c *compiler, b *block, args string) (evalFn, error)
	refDef  func(c *compiler, b *block) (evalFn, error)
)

var (
	simpleFuncs map[string]funcDef // ${name(args)}
	simpleRefs  map[string]refDef  // ${name}
)

func init() {
	simpleFuncs = map[string]funcDef{
		"capitalize":          unary(func(s string) any { return capitalizeAll(s) }),
		"uppercase":           unary(func(s string) any { return strings.ToUpper(s) }),
		"lowercase":           unary(func(s string) any { return strings.ToLower(s) }),
		"trim":                unary(func(s string) any { return javaTrim(s) }),
		"normalizeWhitespace": unary(func(s string) any { return strings.Join(strings.Fields(s), " ") }),
		"quote":               unary(func(s string) any { return quoteText(s) }),
		"unquote":             unary(func(s string) any { return stripQuotes(s) }),
		"safeQuote":           safeQuote,
		"length":              sizeOf(true),
		"size":                sizeOf(false),
		"val":                 val,
		"pad":                 pad,
		"concat":              concat,
		"replace":             replace,
		"substring":           substring,
		"substringBefore":     substringAround("before"),
		"substringAfter":      substringAround("after"),
		"substringBetween":    substringBetween,
		"contains":            containsFunc,
		"sum":                 mathReduce("sum"),
		"min":                 mathReduce("min"),
		"max":                 mathReduce("max"),
		"average":             mathReduce("average"),
		"abs":                 mathUnary("abs"),
		"ceil":                mathUnary("ceil"),
		"floor":               mathUnary("floor"),
		"join":                join,
		"split":               split,
		"distinct":            collection("distinct"),
		"reverse":             collection("reverse"),
		"sort":                sortFunc,
		"range":               rangeFunc,
		"hash":                hashFunc,
		"empty":               emptyFunc,
		"newEmpty":            emptyFunc,
		"iif":                 iif,
		"not":                 notFunc,
		"isEmpty":             isEmptyFunc,
		"isNumeric":           isNumericFunc,
		"jsonpath":            jsonpathFunc,
		"xpath":               xpathFunc,
		"jq":                  jqFunc,
	}
	simpleRefs = map[string]refDef{
		"uuid": func(*compiler, *block) (evalFn, error) {
			return func(*env) (any, error) { return newUUID(), nil }, nil
		},
		"null": func(*compiler, *block) (evalFn, error) {
			return func(*env) (any, error) { return nil, nil }, nil
		},
	}
	registerStateRefs(simpleRefs)
}

// removeQuotes takes every quote out of s, as Camel's StringHelper.removeQuotes does.
func removeQuotes(s string) string {
	return strings.NewReplacer("'", "", `"`, "").Replace(s)
}

// stripQuotes takes one pair of quotes off the ends of s.
func stripQuotes(s string) string {
	t := strings.TrimSpace(s)
	if n := len(t); n >= 2 && (t[0] == '\'' || t[0] == '"') && t[n-1] == t[0] {
		return t[1 : n-1]
	}
	return s
}

func quoteText(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s
	}
	return `"` + stripQuotes(s) + `"`
}

// capitalizeAll makes the first letter of every word a capital.
func capitalizeAll(s string) string {
	var b strings.Builder
	next := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			next = true
		} else if next {
			r = unicode.ToUpper(r)
			next = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// withDefault is the argument as a template, or the body when it is left out.
func (c *compiler) withDefault(b *block, raw string) (evalFn, error) {
	if raw == "" {
		return bodyOf, nil
	}
	return c.template(b, raw)
}

// unary makes a function of one text: ${name(exp)}, ${name()} for the body.
func unary(f func(string) any) funcDef {
	return func(c *compiler, b *block, args string) (evalFn, error) {
		arg, err := c.withDefault(b, removeQuotes(args))
		if err != nil {
			return nil, err
		}
		return func(e *env) (any, error) {
			v, err := arg(e)
			if err != nil || v == nil {
				return nil, err
			}
			return f(render(v)), nil
		}, nil
	}
}

// safeQuote puts a text, list or map in double quotes, as JSON wants it, and
// leaves numbers and booleans as they are.
func safeQuote(c *compiler, b *block, args string) (evalFn, error) {
	arg, err := c.withDefault(b, removeQuotes(args))
	if err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		v, err := arg(e)
		if err != nil || v == nil {
			return nil, err
		}
		switch v.(type) {
		case bool, int, int64, float64:
			return v, nil
		}
		return `"` + stripQuotes(render(v)) + `"`, nil
	}, nil
}

// sizeOf is length (the bytes of a text) and size (the elements of a list).
func sizeOf(bytes bool) funcDef {
	return func(c *compiler, b *block, args string) (evalFn, error) {
		arg, err := c.withDefault(b, removeQuotes(args))
		if err != nil {
			return nil, err
		}
		return func(e *env) (any, error) {
			v, err := arg(e)
			if err != nil || v == nil {
				return int64(0), err
			}
			switch x := v.(type) {
			case []byte:
				return int64(len(x)), nil
			case jlist:
				return int64(len(x)), nil
			case []any:
				return int64(len(x)), nil
			case map[string]any:
				return int64(len(x)), nil
			}
			if bytes {
				return int64(len(render(v))), nil
			}
			return int64(1), nil
		}, nil
	}
}

// val is the value of an expression: ${val(exp)}.
func val(c *compiler, b *block, args string) (evalFn, error) {
	if strings.TrimSpace(args) == "" {
		return nil, fmt.Errorf("valid syntax: ${val(exp)}")
	}
	return c.template(b, stripQuotes(args))
}

// argList splits the arguments of a function and compiles each as a template.
func (c *compiler) argList(b *block, raw string, keepQuotes bool, min, max int, syntax string) ([]string, []evalFn, error) {
	toks := splitArgs(raw, keepQuotes, true)
	if strings.TrimSpace(raw) == "" {
		toks = nil
	}
	if len(toks) < min || len(toks) > max {
		return nil, nil, fmt.Errorf("valid syntax: %s", syntax)
	}
	fns := make([]evalFn, len(toks))
	for i, t := range toks {
		fn, err := c.template(b, t)
		if err != nil {
			return nil, nil, err
		}
		fns[i] = fn
	}
	return toks, fns, nil
}

// str evaluates f to a text; ok is false for nothing.
func str(e *env, f evalFn) (s string, ok bool, err error) {
	v, err := f(e)
	if err != nil || v == nil {
		return "", false, err
	}
	return render(v), true, nil
}

// num evaluates f to a whole number.
func num(e *env, f evalFn) (int64, error) {
	v, err := f(e)
	if err != nil {
		return 0, err
	}
	n, ok := asInt(v)
	if !ok {
		return 0, fmt.Errorf("%q is no number", render(v))
	}
	return n, nil
}

// pad is ${pad(exp,width[,separator])}: the text extended with the separator
// (a space) to width characters, or from the left with a negative width.
func pad(c *compiler, b *block, args string) (evalFn, error) {
	toks := splitArgs(args, true, true)
	if len(toks) < 2 || len(toks) > 3 {
		return nil, fmt.Errorf("valid syntax: ${pad(exp,len)} or ${pad(exp,len,separator)}")
	}
	exp, err := c.template(b, removeQuotes(toks[0]))
	if err != nil {
		return nil, err
	}
	width, err := c.template(b, removeQuotes(toks[1]))
	if err != nil {
		return nil, err
	}
	sep := " "
	if len(toks) == 3 {
		if sep = removeQuotes(toks[2]); sep == "" {
			sep = " "
		}
	}
	return func(e *env) (any, error) {
		s, ok, err := str(e, exp)
		if err != nil || !ok {
			return nil, err
		}
		w, err := num(e, width)
		if err != nil {
			return nil, fmt.Errorf("pad length: %w", err)
		}
		max := int(math.Abs(float64(w)))
		for runeLen(s) < max {
			if w > 0 {
				s += sep
			} else {
				s = sep + s
			}
		}
		return s, nil
	}, nil
}

// concat is ${concat(exp)}, ${concat(exp,exp)} and ${concat(exp,exp,separator)};
// with one the body comes first.
func concat(c *compiler, b *block, args string) (evalFn, error) {
	if strings.TrimSpace(args) == "" {
		return nil, fmt.Errorf("valid syntax: ${concat(exp)} or ${concat(exp,exp)} or ${concat(exp,exp,separator)}")
	}
	var first, second evalFn = bodyOf, nil
	sep := ""
	var err error
	if strings.Contains(args, ",") {
		toks := splitArgs(args, true, true)
		if len(toks) > 3 {
			return nil, fmt.Errorf("valid syntax: ${concat(exp)} or ${concat(exp,exp)} or ${concat(exp,exp,separator)}")
		}
		if first, err = c.template(b, removeQuotes(toks[0])); err != nil {
			return nil, err
		}
		if second, err = c.template(b, removeQuotes(toks[1])); err != nil {
			return nil, err
		}
		if len(toks) == 3 {
			sep = removeQuotes(toks[2])
		}
	} else if second, err = c.template(b, removeQuotes(strings.TrimSpace(args))); err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		l, lok, err := str(e, first)
		if err != nil {
			return nil, err
		}
		r, rok, err := str(e, second)
		if err != nil {
			return nil, err
		}
		switch {
		case lok && rok:
			return l + sep + r, nil
		case lok:
			return l, nil
		case rok:
			return r, nil
		}
		return nil, nil
	}, nil
}

var xmlEntities = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")

// replace is ${replace(from,to)} and ${replace(from,to,exp)}: a regular expression
// replaced in the body or in exp.
func replace(c *compiler, b *block, args string) (evalFn, error) {
	toks, fns, err := c.argList(b, args, false, 2, 3, "${replace(from,to)} or ${replace(from,to,expression)}")
	if err != nil {
		return nil, err
	}
	from, to := xmlEntities.Replace(toks[0]), xmlEntities.Replace(toks[1])
	if to == "&empty;" {
		to = ""
	}
	re, err := javaRegexp(from)
	if err != nil {
		return nil, err
	}
	to = javaReplacement(to)
	var exp evalFn = bodyOf
	if len(fns) == 3 {
		exp = fns[2]
	}
	return func(e *env) (any, error) {
		s, ok, err := str(e, exp)
		if err != nil || !ok {
			return nil, err
		}
		return re.ReplaceAllString(s, to), nil
	}, nil
}

// substring is ${substring(head)}, ${substring(head,tail)} and
// ${substring(head,tail,exp)}: head characters off the front and tail off the
// end; a lone negative number cuts from the end.
func substring(c *compiler, b *block, args string) (evalFn, error) {
	_, fns, err := c.argList(b, args, false, 1, 3, "${substring(num)}, ${substring(num,num)} or ${substring(num,num,expression)}")
	if err != nil {
		return nil, err
	}
	var exp evalFn = bodyOf
	if len(fns) == 3 {
		exp = fns[2]
	}
	return func(e *env) (any, error) {
		head, err := num(e, fns[0])
		if err != nil {
			return nil, err
		}
		var tail int64
		if len(fns) > 1 {
			if tail, err = num(e, fns[1]); err != nil {
				return nil, err
			}
		}
		if head < 0 && tail == 0 {
			head, tail = 0, head
		}
		if head < 0 {
			head = -head
		}
		if tail < 0 {
			tail = -tail
		}
		s, ok, err := str(e, exp)
		if err != nil || !ok {
			return nil, err
		}
		rs := []rune(s)
		if head != 0 || tail != 0 {
			from := min(int(head), len(rs))
			to := max(len(rs)-int(tail), from)
			rs = rs[from:to]
		}
		return string(rs), nil
	}, nil
}

// substringAround is substringBefore and substringAfter: ${f(text)} on the
// body or ${f(exp,text)}; nothing when the text is not in it.
func substringAround(which string) funcDef {
	return func(c *compiler, b *block, args string) (evalFn, error) {
		_, fns, err := c.argList(b, args, false, 1, 2, "${substring"+strings.ToUpper(which[:1])+which[1:]+"(exp)} or ${...(exp,exp)}")
		if err != nil {
			return nil, err
		}
		var exp evalFn = bodyOf
		mark := fns[0]
		if len(fns) == 2 {
			exp, mark = fns[0], fns[1]
		}
		return func(e *env) (any, error) {
			s, ok, err := str(e, exp)
			if err != nil || !ok {
				return nil, err
			}
			m, _, err := str(e, mark)
			if err != nil {
				return nil, err
			}
			i := strings.Index(s, m)
			switch {
			case i < 0:
				return nil, nil
			case which == "before":
				return s[:i], nil
			}
			return s[i+len(m):], nil
		}, nil
	}
}

// substringBetween is ${substringBetween(after,before)} or
// ${substringBetween(exp,after,before)}.
func substringBetween(c *compiler, b *block, args string) (evalFn, error) {
	_, fns, err := c.argList(b, args, false, 2, 3, "${substringBetween(after,before)} or ${substringBetween(exp,after,before)}")
	if err != nil {
		return nil, err
	}
	var exp evalFn = bodyOf
	after, before := fns[0], fns[1]
	if len(fns) == 3 {
		exp, after, before = fns[0], fns[1], fns[2]
	}
	return func(e *env) (any, error) {
		s, ok, err := str(e, exp)
		if err != nil || !ok {
			return nil, err
		}
		a, _, err := str(e, after)
		if err != nil {
			return nil, err
		}
		z, _, err := str(e, before)
		if err != nil {
			return nil, err
		}
		i := strings.Index(s, a)
		if i < 0 {
			return nil, nil
		}
		rest := s[i+len(a):]
		j := strings.Index(rest, z)
		if j < 0 {
			return nil, nil
		}
		return rest[:j], nil
	}, nil
}

// containsFunc is ${contains(text)} or ${contains(exp,text)}, ignoring case.
func containsFunc(c *compiler, b *block, args string) (evalFn, error) {
	_, fns, err := c.argList(b, args, false, 1, 2, "${contains(text)} or ${contains(exp,text)}")
	if err != nil {
		return nil, err
	}
	var exp evalFn = bodyOf
	pattern := fns[0]
	if len(fns) == 2 {
		exp, pattern = fns[0], fns[1]
	}
	return func(e *env) (any, error) {
		l, err := exp(e)
		if err != nil {
			return nil, err
		}
		r, err := pattern(e)
		if err != nil {
			return nil, err
		}
		return valueContains(l, r, true), nil
	}, nil
}

// mathReduce is sum, min, max and average of whole numbers: every argument
// is a number, or a list or comma separated text of them.
func mathReduce(kind string) funcDef {
	return func(c *compiler, b *block, args string) (evalFn, error) {
		_, fns, err := c.argList(b, args, false, 0, 1<<10, "${"+kind+"(exp,exp,...)}")
		if err != nil {
			return nil, err
		}
		return func(e *env) (any, error) {
			var total, count int64
			for _, f := range fns {
				v, err := f(e)
				if err != nil {
					return nil, err
				}
				for _, el := range elements(v) {
					n, isInt, ok := asNumber(el)
					if !ok || !isInt {
						continue
					}
					i := int64(n)
					switch {
					case count == 0:
						total = i
					case kind == "min":
						total = min(total, i)
					case kind == "max":
						total = max(total, i)
					default:
						total += i
					}
					count++
				}
			}
			switch {
			case count == 0:
				return nil, nil
			case kind == "average":
				return total / count, nil
			}
			return total, nil
		}, nil
	}
}

// mathUnary is abs, ceil and floor.
func mathUnary(kind string) funcDef {
	return func(c *compiler, b *block, args string) (evalFn, error) {
		arg, err := c.withDefault(b, removeQuotes(args))
		if err != nil {
			return nil, err
		}
		return func(e *env) (any, error) {
			v, err := arg(e)
			if err != nil || v == nil {
				return nil, err
			}
			n, _, ok := asNumber(v)
			if !ok {
				return nil, fmt.Errorf("%q is no number", render(v))
			}
			switch kind {
			case "abs":
				return int64(math.Abs(n)), nil
			case "ceil":
				return int64(math.Ceil(n)), nil
			}
			return int64(math.Floor(n)), nil
		}, nil
	}
}

// join is ${join()}, ${join(separator)}, ${join(separator,prefix)} and
// ${join(separator,prefix,exp)}: the elements of exp (the body) with the prefix
// in front of each, one separator between them.
func join(c *compiler, b *block, args string) (evalFn, error) {
	sep, prefix := ",", ""
	var exp evalFn = bodyOf
	if strings.TrimSpace(args) != "" {
		toks, fns, err := c.argList(b, args, false, 1, 3, "${join(separator,prefix,expression)}")
		if err != nil {
			return nil, err
		}
		sep = toks[0]
		if len(toks) >= 2 {
			prefix = toks[1]
		}
		if len(fns) == 3 {
			exp = fns[2]
		}
	}
	return func(e *env) (any, error) {
		v, err := exp(e)
		if err != nil {
			return nil, err
		}
		els := elements(v)
		parts := make([]string, len(els))
		for i, el := range els {
			parts[i] = prefix + render(el)
		}
		return strings.Join(parts, sep), nil
	}, nil
}

// split is ${split(separator)} and ${split(exp,separator)}, the separator a
// regular expression as in Java; the default separator is a comma.
func split(c *compiler, b *block, args string) (evalFn, error) {
	var exp evalFn = bodyOf
	sep := ","
	if strings.TrimSpace(args) != "" {
		toks, fns, err := c.argList(b, args, false, 1, 2, "${split(separator)} or ${split(exp,separator)}")
		if err != nil {
			return nil, err
		}
		if len(toks) == 2 {
			exp, sep = fns[0], toks[1]
		} else {
			sep = toks[0]
		}
	}
	if _, err := javaRegexp(sep); err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		s, ok, err := str(e, exp)
		if err != nil || !ok {
			return nil, err
		}
		return splitRegexp(s, sep)
	}, nil
}

// collection is distinct and reverse over the elements of the arguments (the body).
func collection(kind string) funcDef {
	return func(c *compiler, b *block, args string) (evalFn, error) {
		var fns []evalFn = []evalFn{bodyOf}
		if strings.TrimSpace(args) != "" {
			var err error
			if _, fns, err = c.argList(b, args, false, 1, 1<<10, "${"+kind+"(exp,exp,...)}"); err != nil {
				return nil, err
			}
		}
		return func(e *env) (any, error) {
			var all jlist
			seen := map[string]bool{}
			for _, f := range fns {
				v, err := f(e)
				if err != nil {
					return nil, err
				}
				for _, el := range elements(v) {
					if kind == "distinct" {
						if k := render(el); seen[k] {
							continue
						} else {
							seen[k] = true
						}
					}
					all = append(all, el)
				}
			}
			if kind == "reverse" {
				for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
					all[i], all[j] = all[j], all[i]
				}
			}
			return all, nil
		}, nil
	}
}

// sortFunc is ${sort()}, ${sort(reverse)} and ${sort(exp,reverse)}.
func sortFunc(c *compiler, b *block, args string) (evalFn, error) {
	var exp evalFn = bodyOf
	reverse := false
	if strings.TrimSpace(args) != "" {
		toks, fns, err := c.argList(b, args, false, 1, 2, "${sort(reverse)} or ${sort(exp,reverse)}")
		if err != nil {
			return nil, err
		}
		last := toks[len(toks)-1]
		reverse = strings.EqualFold(last, "true")
		if len(toks) == 2 {
			exp = fns[0]
		}
	}
	return func(e *env) (any, error) {
		v, err := exp(e)
		if err != nil {
			return nil, err
		}
		l := append(jlist(nil), elements(v)...)
		sort.SliceStable(l, func(i, j int) bool {
			less := valuesOrdered("<", l[i], l[j])
			if reverse {
				return valuesOrdered(">", l[i], l[j])
			}
			return less
		})
		return l, nil
	}, nil
}

// rangeFunc is ${range(max)} and ${range(min,max)}: the whole numbers up to max (excluded).
func rangeFunc(c *compiler, b *block, args string) (evalFn, error) {
	_, fns, err := c.argList(b, args, false, 1, 2, "${range(min,max)} or ${range(max)}")
	if err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		lo, hi := int64(1), int64(0)
		var err error
		if len(fns) == 2 {
			if lo, err = num(e, fns[0]); err != nil {
				return nil, err
			}
			hi, err = num(e, fns[1])
		} else {
			hi, err = num(e, fns[0])
		}
		if err != nil {
			return nil, err
		}
		var l jlist
		for i := lo; i < hi; i++ {
			l = append(l, i)
		}
		return l, nil
	}, nil
}

// digests are the algorithms of ${hash(exp,algorithm)}, with the names of Java's MessageDigest.
var digests = map[string]func() hash.Hash{
	"MD5":      md5.New,
	"SHA-1":    sha1.New,
	"SHA-224":  sha256.New224,
	"SHA-256":  sha256.New,
	"SHA-384":  sha512.New384,
	"SHA-512":  sha512.New,
	"SHA3-224": func() hash.Hash { return sha3.New224() },
	"SHA3-256": func() hash.Hash { return sha3.New256() },
	"SHA3-384": func() hash.Hash { return sha3.New384() },
	"SHA3-512": func() hash.Hash { return sha3.New512() },
}

// hashFunc is ${hash(exp)} and ${hash(exp,algorithm)} (SHA-256 by default): the
// digest of a text as lower case hexadecimal.
func hashFunc(c *compiler, b *block, args string) (evalFn, error) {
	if strings.TrimSpace(args) == "" {
		return nil, fmt.Errorf("valid syntax: ${hash(value,algorithm)} or ${hash(value)}")
	}
	expText, algText, _ := strings.Cut(args, ",")
	if !strings.Contains(args, ",") {
		algText = "SHA-256"
	}
	alg := strings.ToUpper(removeQuotes(strings.TrimSpace(algText)))
	newHash, ok := digests[alg]
	if !ok {
		return nil, fmt.Errorf("hash algorithm %q is not supported; use one of MD5, SHA-1, SHA-224, SHA-256, SHA-384, SHA-512, SHA3-224, SHA3-256, SHA3-384, SHA3-512", alg)
	}
	exp, err := c.template(b, strings.TrimSpace(expText))
	if err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		v, err := exp(e)
		if err != nil || v == nil {
			return nil, err
		}
		h := newHash()
		h.Write(bytesOf(v))
		return hex.EncodeToString(h.Sum(nil)), nil
	}, nil
}

// emptyFunc is ${empty(type)} and ${newEmpty(type)}: an empty string, list, set or map.
func emptyFunc(c *compiler, b *block, args string) (evalFn, error) {
	typ := strings.ToLower(strings.TrimSpace(args))
	switch typ {
	case "string":
		return func(*env) (any, error) { return "", nil }, nil
	case "list", "set":
		return func(*env) (any, error) { return jlist{}, nil }, nil
	case "map":
		return func(*env) (any, error) { return map[string]any{}, nil }, nil
	}
	return nil, fmt.Errorf("valid syntax: ${empty(<type>)} with the type string, list, set or map")
}

// iif is ${iif(condition,trueExpression,falseExpression)}.
func iif(c *compiler, b *block, args string) (evalFn, error) {
	toks := splitArgs(args, true, true)
	if len(toks) != 3 {
		return nil, fmt.Errorf("valid syntax: ${iif(predicate,trueExpression,falseExpression)}")
	}
	test, err := c.condition(b, toks[0])
	if err != nil {
		return nil, err
	}
	yes, err := c.template(b, stripQuotes(toks[1])) // text, as in ${iif(${header.n} > 1, OK, NOK)}
	if err != nil {
		return nil, err
	}
	no, err := c.template(b, stripQuotes(toks[2]))
	if err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		ok, err := test(e)
		if err != nil {
			return nil, err
		}
		if ok {
			return yes(e)
		}
		return no(e)
	}, nil
}

// notFunc is ${not(condition)}.
func notFunc(c *compiler, b *block, args string) (evalFn, error) {
	test, err := c.condition(b, stripQuotes(args))
	if err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		ok, err := test(e)
		return !ok, err
	}, nil
}

func isEmptyFunc(c *compiler, b *block, args string) (evalFn, error) {
	arg, err := c.withDefault(b, removeQuotes(args))
	if err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		v, err := arg(e)
		return err == nil && empty(v), err
	}, nil
}

func isNumericFunc(c *compiler, b *block, args string) (evalFn, error) {
	arg, err := c.withDefault(b, removeQuotes(args))
	if err != nil {
		return nil, err
	}
	return func(e *env) (any, error) {
		v, err := arg(e)
		if err != nil || v == nil {
			return false, err
		}
		s := strings.TrimSpace(render(v))
		return s != "" && strings.Trim(s, "0123456789") == "", nil
	}, nil
}

// jsonpathFunc is ${jsonpath(path)} and ${jsonpath(path,type)} on the body: the
// value a path with a name for every step selects, or a list for a path with
// a * in it.
func jsonpathFunc(c *compiler, b *block, args string) (evalFn, error) {
	toks := splitArgs(args, false, true)
	if len(toks) < 1 || len(toks) > 2 {
		return nil, fmt.Errorf("valid syntax: ${jsonpath(path)} or ${jsonpath(path,type)}")
	}
	path, err := c.template(b, toks[0])
	if err != nil {
		return nil, err
	}
	typ := ""
	if len(toks) == 2 {
		typ = strings.TrimPrefix(toks[1], "java.lang.")
	}
	var static *jsonPath
	if !strings.ContainsRune(toks[0], phOpen) {
		p, err := compileJSONPath(toks[0])
		if err != nil {
			return nil, err
		}
		static = &p
	}
	return func(e *env) (any, error) {
		p := static
		if p == nil {
			s, _, err := str(e, path)
			if err != nil {
				return nil, err
			}
			q, err := compileJSONPath(s)
			if err != nil {
				return nil, err
			}
			p = &q
		}
		doc, err := decodeJSON(e.bodyValue())
		if err != nil {
			return nil, err
		}
		found := p.eval(doc)
		var out any
		switch {
		case p.definite() && len(found) == 0:
			out = nil
		case p.definite():
			out = found[0]
		default:
			out = jlist(found)
		}
		switch typ {
		case "":
			return out, nil
		case "String":
			return toStringValue(out), nil
		case "Integer", "Long", "int", "long":
			return toIntValue(out), nil
		case "Boolean", "boolean":
			return toBoolValue(out), nil
		}
		return nil, fmt.Errorf("jsonpath result type %q is not supported", typ)
	}, nil
}

// xpathFunc is ${xpath(expression)} and ${xpath(expression,type)} on the body:
// the text of the first item the XPath 2.0 expression selects ("" for none).
func xpathFunc(c *compiler, b *block, args string) (evalFn, error) {
	toks := splitArgs(args, false, true)
	if len(toks) < 1 || len(toks) > 2 {
		return nil, fmt.Errorf("valid syntax: ${xpath(expression)} or ${xpath(expression,type)}")
	}
	expr, err := c.template(b, toks[0])
	if err != nil {
		return nil, err
	}
	typ := ""
	if len(toks) == 2 {
		typ = strings.TrimPrefix(toks[1], "java.lang.")
	}
	var static xpath
	if !strings.ContainsRune(toks[0], phOpen) {
		if static, err = compileXPath(toks[0]); err != nil {
			return nil, err
		}
	}
	return func(e *env) (any, error) {
		q := static
		if q == nil {
			s, _, err := str(e, expr)
			if err != nil {
				return nil, err
			}
			if q, err = compileXPath(s); err != nil {
				return nil, err
			}
		}
		v, err := q.value(bytesOf(e.bodyValue()))
		if err != nil {
			return nil, err
		}
		switch typ {
		case "", "String":
			return v, nil
		case "Integer", "Long", "int", "long":
			return toIntValue(v), nil
		case "Boolean", "boolean":
			return toBoolValue(v), nil
		}
		return nil, fmt.Errorf("xpath result type %q is not supported", typ)
	}, nil
}
