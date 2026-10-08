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
//   - xpath: an XPath 2.0 expression, true when it selects a node or its value
//     is true (see xpath2.go)
//   - jsonpath: a path, true when it selects a value other than null or false
//
// An xpath or jsonpath with ${...} in it, such as ${header.expression}, is a
// simple template evaluated for every message.
func compilePredicate(language, expr string) (predicate, error) {
	return compilePredicateIn(nil, language, expr)
}

// compilePredicateIn compiles a condition for a flow with the given properties.
func compilePredicateIn(flow *flowProperties, language, expr string) (predicate, error) {
	return compilePredicateNS(flow, nil, language, expr)
}

// compilePredicateNS compiles a condition whose xpath may use the prefixes in ns.
func compilePredicateNS(flow *flowProperties, ns map[string]string, language, expr string) (predicate, error) {
	switch language {
	case "simple":
		return compileSimplePredicate(flow, expr)
	case "xpath":
		get, err := perMessage(flow, expr, func(s string) (xpath, error) { return compileXPathNS(s, ns) })
		if err != nil {
			return nil, err
		}
		return func(m message.Message) (bool, error) {
			q, err := get(m)
			if err != nil {
				return false, err
			}
			// A body that is not XML matches nothing.
			ok, err := q.boolean(bytesOf(m[message.Body]))
			return ok && err == nil, nil
		}, nil
	case "jsonpath":
		get, err := perMessage(flow, expr, compileJSONPath)
		if err != nil {
			return nil, err
		}
		return func(m message.Message) (bool, error) {
			p, err := get(m)
			if err != nil {
				return false, err
			}
			return p.matchJSON(m[message.Body]), nil
		}, nil
	}
	return nil, fmt.Errorf("language %q is not supported; use simple, xpath or jsonpath", language)
}
