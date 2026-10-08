package impl

import (
	"fmt"

	"dif/message"
)

// predicate is a compiled condition on a message. It fails only when a
// simple expression in it does (${bodyAs(<type>)}).
type predicate func(message.Message) (bool, error)

// compilePredicate compiles a condition in one of these languages:
//
//   - simple: the conditions of Camel's simple language, such as
//     ${header.n} > 10 && ${body} contains 'x' (see simple_ops.go). Without an
//     operator, the expression must evaluate to "true".
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
