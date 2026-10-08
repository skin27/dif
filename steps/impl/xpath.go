package impl

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// xpath is a compiled XPath expression of the subset DIF supports: an
// absolute path of element names, such as /persons/person or /a/*/c.
// Namespace prefixes in the path are ignored; elements match on local name.
type xpath []string

func compileXPath(expr string) (xpath, error) {
	expr = strings.TrimSpace(expr)
	if !strings.HasPrefix(expr, "/") || strings.HasPrefix(expr, "//") {
		return nil, unsupportedXPath(expr)
	}
	var x xpath
	for s := range strings.SplitSeq(expr[1:], "/") {
		if _, local, ok := strings.Cut(s, ":"); ok {
			s = local
		}
		if s != "*" && !isXMLName(s) {
			return nil, unsupportedXPath(expr)
		}
		x = append(x, s)
	}
	return x, nil
}

func unsupportedXPath(expr string) error {
	return fmt.Errorf("unsupported xpath %q; DIF supports absolute paths of element names, such as /a/b or /a/*", expr)
}

func isXMLName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 0x7F) {
			return false
		}
	}
	return (s[0] < '0' || s[0] > '9') && s[0] != '-' && s[0] != '.'
}

// xmlNode is an element the path selected.
type xmlNode struct {
	raw  string // the element as it is in the document, from its start tag to its end tag
	text string // its string-value: all text it contains
}

// selectXML returns the elements of the XML document data that x selects,
// in document order.
func (x xpath) selectXML(data []byte) ([]xmlNode, error) {
	var (
		nodes  []xmlNode
		depth  int
		match  int // depth of the leading path steps the open elements match
		start  int64
		text   strings.Builder
		inNode bool
		root   bool // the document has an element
	)
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		offset := d.InputOffset()
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("body is not XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			root = true
			depth++
			if match == depth-1 && depth <= len(x) && (x[depth-1] == "*" || x[depth-1] == t.Name.Local) {
				match = depth
				if depth == len(x) {
					inNode, start = true, offset
					text.Reset()
				}
			}
		case xml.EndElement:
			if inNode && depth == len(x) {
				nodes = append(nodes, xmlNode{raw: string(data[start:d.InputOffset()]), text: text.String()})
				inNode = false
			}
			if match == depth {
				match--
			}
			depth--
		case xml.CharData:
			if inNode {
				text.Write(t)
			}
		}
	}
	if !root {
		return nil, fmt.Errorf("body is not XML: no root element")
	}
	return nodes, nil
}

// xpathPredicate is a condition on an XML body: a path, true when it selects
// an element, or a path compared with = or != to a literal, true when one of
// the selected elements' text is (not) equal to it.
type xpathPredicate struct {
	path    xpath
	op      string // "", "=" or "!="
	literal string
}

func compileXPathPredicate(expr string) (xpathPredicate, error) {
	var p xpathPredicate
	path := expr
	if i := strings.IndexByte(expr, '='); i >= 0 { // a path holds no =, so the first one is the operator
		path, p.op = expr[:i], "="
		if strings.HasSuffix(path, "!") {
			path, p.op = path[:len(path)-1], "!="
		}
		p.literal = unquote(strings.TrimSpace(expr[i+1:]))
	}
	var err error
	p.path, err = compileXPath(path)
	return p, err
}

// match reports whether the predicate holds for the XML document data. A
// body that is not XML matches nothing.
func (p xpathPredicate) match(data []byte) bool {
	nodes, err := p.path.selectXML(data)
	if err != nil {
		return false
	}
	if p.op == "" {
		return len(nodes) > 0
	}
	for _, n := range nodes {
		if (n.text == p.literal) == (p.op == "=") {
			return true
		}
	}
	return false
}

// unquote removes the single or double quotes around s, if it has them.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
