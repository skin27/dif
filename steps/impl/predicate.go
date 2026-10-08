package impl

import (
	"fmt"
	"strings"

	"dif/message"
)

// predicate is a compiled condition on a message. It fails only when a
// simple expression in it does (${bodyAs(<type>)}).
type predicate func(message.Message) (bool, error)

// compilePredicate compiles a condition in one of these languages:
//
//   - simple: ${...} == 'literal', !=, or contains; the right side may also
//     be a simple expression. Without an operator, the expression must
//     evaluate to "true".
//   - xpath: a path, or a path = or != 'literal' (see xpathPredicate)
//   - jsonpath: a path, true when it selects a value other than null or false
func compilePredicate(language, expr string) (predicate, error) {
	switch language {
	case "simple":
		return compileSimplePredicate(expr)
	case "xpath":
		p, err := compileXPathPredicate(expr)
		if err != nil {
			return nil, err
		}
		return func(m message.Message) (bool, error) { return p.match(bytesOf(m[message.Body])), nil }, nil
	case "jsonpath":
		p, err := compileJSONPath(expr)
		if err != nil {
			return nil, err
		}
		return func(m message.Message) (bool, error) { return p.matchJSON(m[message.Body]), nil }, nil
	}
	return nil, fmt.Errorf("language %q is not supported; use simple, xpath or jsonpath", language)
}

// simpleOperators are the comparisons a simple predicate supports.
var simpleOperators = []string{" == ", " != ", " contains "}

func compileSimplePredicate(expr string) (predicate, error) {
	op, i := findOutsideRefs(expr, simpleOperators)
	if _, j := findOutsideRefs(expr, []string{" && ", " || ", " and ", " or "}); j >= 0 {
		return nil, fmt.Errorf("simple predicate %q: combining conditions is not supported", expr)
	}
	if i < 0 {
		e, err := compileExpression("simple", expr)
		if err != nil {
			return nil, err
		}
		return func(m message.Message) (bool, error) {
			v, err := e.eval(m)
			return strings.EqualFold(strings.TrimSpace(v), "true"), err
		}, nil
	}

	left, err := compileExpression("simple", strings.TrimSpace(expr[:i]))
	if err != nil {
		return nil, err
	}
	rightText := strings.TrimSpace(expr[i+len(op):])
	right := expression{{text: unquote(rightText)}}
	if unquote(rightText) == rightText {
		if right, err = compileExpression("simple", rightText); err != nil {
			return nil, err
		}
	}

	compare := map[string]func(a, b string) bool{
		" == ":       func(a, b string) bool { return a == b },
		" != ":       func(a, b string) bool { return a != b },
		" contains ": strings.Contains,
	}[op]
	return func(m message.Message) (bool, error) {
		l, err := left.eval(m)
		if err != nil {
			return false, err
		}
		r, err := right.eval(m)
		return compare(l, r), err
	}, nil
}

// findOutsideRefs returns the first of ops that occurs in s outside ${...}
// references and quoted literals, and its index; -1 if none does.
func findOutsideRefs(s string, ops []string) (string, int) {
	var quote byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
			continue
		case c == '\'' || c == '"':
			quote = c
			continue
		case strings.HasPrefix(s[i:], "${"):
			if j := strings.IndexByte(s[i:], '}'); j >= 0 {
				i += j
				continue
			}
		}
		for _, op := range ops {
			if strings.HasPrefix(s[i:], op) {
				return op, i
			}
		}
	}
	return "", -1
}
