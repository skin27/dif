package impl

import (
	"fmt"
	"sort"
	"strings"

	"github.com/knroy/go-xml/xdm"
	xp "github.com/knroy/go-xml/xpath"
)

// XPath 2.0 is what the platform's flows are written in (max(), avg(),
// distinct-values(), year-from-dateTime(), number() as a step, *:name tests),
// so the expressions are evaluated by github.com/knroy/go-xml, a pure Go
// XPath 2.0/3.1 and XSLT 2.0/3.0 processor, which the xslt step of Phase 5 will
// use too. This file is the only one that imports it for XPath: the rest of DIF
// sees an xpath.
//
// The processor builds a tree of the document, which costs memory in proportion
// to the document (about 35 times its size). The expressions that are no more
// than an absolute path of element names, such as /orders/order, are therefore
// still evaluated by plainPath, which scans the document for them.

// xpath is a compiled XPath expression.
type xpath = *xpathQuery

type xpathQuery struct {
	src    string
	plain  plainPath    // set when the expression is a plain path
	engine *xp.Compiled // else the XPath 2.0 expression
}

// xpathNS are the namespaces the prefixes of an expression stand for.
type xpathNS map[string]string

func (n xpathNS) ResolvePrefix(prefix string) (string, bool) { u, ok := n[prefix]; return u, ok }
func (xpathNS) DefaultElementNamespace() string              { return "" }
func (xpathNS) DefaultFunctionNamespace() string {
	return "http://www.w3.org/2005/xpath-functions"
}

// maxXPathSize bounds an expression, as it may come from a header.
const maxXPathSize = 64 << 10

// compileXPath compiles an expression without namespace prefixes.
func compileXPath(expr string) (xpath, error) { return compileXPathNS(expr, nil) }

// compileXPathNS compiles an expression whose prefixes stand for the namespaces in ns.
func compileXPathNS(expr string, ns map[string]string) (xpath, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty xpath")
	}
	q := &xpathQuery{src: expr}
	if !strings.Contains(expr, ":") {
		if p, err := compilePlainPath(expr); err == nil {
			q.plain = p
			return q, nil
		}
	}
	c, err := xp.CompileWith(expr, xp.CompileOptions{Namespaces: xpathNS(ns), MaxBytes: maxXPathSize})
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %s", expr, firstLine(err))
	}
	// The processor looks a function up when the call is evaluated; a flow
	// should not build with a call to a function that does not exist.
	lib := xp.Builtins()
	for _, call := range c.StaticCalls() {
		if _, ok := lib.Lookup(call.Name, call.Arity); !ok {
			return nil, fmt.Errorf("xpath %q: XPST0017: there is no function %s with %d arguments", expr, call.Name.Lexical(), call.Arity)
		}
	}
	q.engine = c
	return q, nil
}

func firstLine(err error) string { return strings.SplitN(err.Error(), "\n", 2)[0] }

// sequence evaluates the expression on the XML document data.
func (q *xpathQuery) sequence(data []byte) (xdm.Sequence, error) {
	tree, err := xdm.ParseString(string(data), xdm.ParseOptions{})
	if err != nil {
		return nil, fmt.Errorf("body is not XML: %s", firstLine(err))
	}
	seq, err := q.engine.Eval(xp.NewContext(tree.Root, xp.Builtins()))
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %s", q.src, firstLine(err))
	}
	return seq, nil
}

// selectXML returns what the expression selects in the XML document data, in
// order: for an element its XML, for another node or a value its text.
func (q *xpathQuery) selectXML(data []byte) ([]xmlNode, error) {
	if q.engine == nil {
		return q.plain.selectXML(data)
	}
	seq, err := q.sequence(data)
	if err != nil {
		return nil, err
	}
	nodes := make([]xmlNode, len(seq))
	for i, it := range seq {
		switch x := it.(type) {
		case *xdm.Node:
			raw := x.StringValue()
			if x.Kind != xdm.KindText && x.Kind != xdm.KindAttribute && x.Kind != xdm.KindNamespace {
				raw = nodeXML(x)
			}
			nodes[i] = xmlNode{raw: raw, text: x.StringValue()}
		case *xdm.Atomic:
			nodes[i] = xmlNode{raw: x.String(), text: x.String()}
		}
	}
	return nodes, nil
}

// boolean is the truth of the expression for the document: true when it selects
// a node, or its value is true, a number other than 0, or a text that is not empty.
func (q *xpathQuery) boolean(data []byte) (bool, error) {
	if q.engine == nil {
		nodes, err := q.plain.selectXML(data)
		return len(nodes) > 0, err
	}
	seq, err := q.sequence(data)
	if err != nil {
		return false, err
	}
	ok, err := xp.EffectiveBooleanValue(seq)
	if err != nil {
		return false, fmt.Errorf("xpath %q: %s", q.src, firstLine(err))
	}
	return ok, nil
}

// value is the text of the first item the expression selects, "" if it selects none.
func (q *xpathQuery) value(data []byte) (string, error) {
	nodes, err := q.selectXML(data)
	if err != nil || len(nodes) == 0 {
		return "", err
	}
	return nodes[0].text, nil
}

// nodeXML writes a node as XML. An element is written with the namespace
// declarations its names need, so that it can be read on its own.
func nodeXML(n *xdm.Node) string {
	var b strings.Builder
	if n.Kind == xdm.KindElement {
		used := map[string]string{}
		collectNamespaces(n, used, true)
		writeNodeXML(&b, n, map[string]string{}, used)
	} else {
		writeNodeXML(&b, n, map[string]string{}, nil)
	}
	return b.String()
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
var attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", `"`, "&quot;", "\n", "&#10;", "\r", "&#13;", "\t", "&#9;")

// collectNamespaces adds to used the namespace of each prefix (and of the
// default namespace) that the names of e, and with deep of its descendants, use.
func collectNamespaces(e *xdm.Node, used map[string]string, deep bool) {
	if e.Name.Prefix != "" || e.Name.URI != "" {
		used[e.Name.Prefix] = e.Name.URI
	}
	for _, a := range e.Attrs {
		if a.Name.Prefix != "" && a.Name.Prefix != "xml" {
			used[a.Name.Prefix] = a.Name.URI
		}
	}
	if deep {
		for _, c := range e.Children {
			if c.Kind == xdm.KindElement {
				collectNamespaces(c, used, true)
			}
		}
	}
}

// writeNodeXML writes n. declared holds the namespace declarations of the
// elements written around it; hoist those that this element is to declare.
func writeNodeXML(b *strings.Builder, n *xdm.Node, declared, hoist map[string]string) {
	switch n.Kind {
	case xdm.KindDocument:
		for _, c := range n.Children {
			writeNodeXML(b, c, declared, nil)
		}
	case xdm.KindElement:
		name := n.Name.Lexical()
		b.WriteString("<" + name)
		need := map[string]string{}
		for p, u := range hoist {
			need[p] = u
		}
		collectNamespaces(n, need, false)
		if n.Name.Prefix == "" && n.Name.URI == "" && declared[""] != "" {
			need[""] = "" // an element in no namespace inside a default namespace
		}
		inner := declared
		prefixes := make([]string, 0, len(need))
		for p, u := range need {
			if declared[p] != u {
				prefixes = append(prefixes, p)
			}
		}
		sort.Strings(prefixes)
		if len(prefixes) > 0 {
			inner = make(map[string]string, len(declared)+len(prefixes))
			for p, u := range declared {
				inner[p] = u
			}
		}
		for _, p := range prefixes {
			u := need[p]
			inner[p] = u
			if p == "" {
				b.WriteString(` xmlns="` + attrEscaper.Replace(u) + `"`)
			} else {
				b.WriteString(` xmlns:` + p + `="` + attrEscaper.Replace(u) + `"`)
			}
		}
		for _, a := range n.Attrs {
			b.WriteString(" " + a.Name.Lexical() + `="` + attrEscaper.Replace(a.Value) + `"`)
		}
		if len(n.Children) == 0 {
			b.WriteString("/>")
			return
		}
		b.WriteString(">")
		for _, c := range n.Children {
			writeNodeXML(b, c, inner, nil)
		}
		b.WriteString("</" + name + ">")
	case xdm.KindText:
		b.WriteString(xmlEscaper.Replace(n.Value))
	case xdm.KindComment:
		b.WriteString("<!--" + n.Value + "-->")
	case xdm.KindPI:
		if n.Value == "" {
			b.WriteString("<?" + n.Name.Local + "?>")
		} else {
			b.WriteString("<?" + n.Name.Local + " " + n.Value + "?>")
		}
	default: // an attribute or namespace node: its value
		b.WriteString(n.Value)
	}
}
