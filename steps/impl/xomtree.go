package impl

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// xomDoc is an XML document as the Java XOM library holds it, which json-lib's
// XMLSerializer reads: an element has its child nodes in document order (text,
// elements, and comments and processing instructions, which count as children
// but have no content), its attributes without the namespace declarations, and
// its namespace declarations apart.
type xomElem struct {
	name     string    // as written, prefix:local
	attrs    []xmlAttr // without namespace declarations; names as written
	decls    []xomNS   // the element's own prefix first, then those it declares
	children []xomNode
}

type xomNS struct{ prefix, uri string }

type xomKind int

const (
	xomText    xomKind = iota
	xomElement         // elem is set
	xomOther           // a comment or a processing instruction
)

type xomNode struct {
	kind xomKind
	text string
	elem *xomElem
}

// parseXOM parses data into its root element. Like XOM it keeps whitespace and
// merges adjacent text (CDATA sections too) into one text node, and it rejects
// a prefix that is not declared. The declared encoding is ignored: the text is
// already characters.
func parseXOM(data []byte) (*xomElem, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	var (
		root   *xomElem
		stack  []*xomElem
		scopes []map[string]string // prefix -> uri, one per open element
	)
	lookup := func(prefix string) (string, bool) {
		for i := len(scopes) - 1; i >= 0; i-- {
			if uri, ok := scopes[i][prefix]; ok {
				return uri, true
			}
		}
		return "", prefix == "" || prefix == "xml"
	}
	for {
		tok, err := d.RawToken() // keeps prefixes as written
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("body is not XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 && root != nil {
				return nil, fmt.Errorf("body is not XML: more than one root element")
			}
			e := &xomElem{name: qualifiedName(t.Name)}
			declared := map[string]string{}
			var order []string
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					declared[""] = a.Value
					order = append(order, "")
				case a.Name.Space == "xmlns":
					declared[a.Name.Local] = a.Value
					order = append(order, a.Name.Local)
				default:
					e.attrs = append(e.attrs, xmlAttr{qualifiedName(a.Name), a.Value})
				}
			}
			scopes = append(scopes, declared)
			own, ok := lookup(t.Name.Space)
			if !ok {
				return nil, fmt.Errorf("body is not XML: the prefix %q of element <%s> is not declared", t.Name.Space, e.name)
			}
			e.decls = append(e.decls, xomNS{t.Name.Space, own})
			for _, p := range order {
				if p != t.Name.Space {
					e.decls = append(e.decls, xomNS{p, declared[p]})
				}
			}
			for _, a := range e.attrs {
				if prefix, _, ok := strings.Cut(a.name, ":"); ok {
					if _, ok := lookup(prefix); !ok {
						return nil, fmt.Errorf("body is not XML: the prefix %q of attribute %s is not declared", prefix, a.name)
					}
				}
			}
			if len(stack) == 0 {
				root = e
			} else {
				stack[len(stack)-1].children = append(stack[len(stack)-1].children, xomNode{kind: xomElement, elem: e})
			}
			stack = append(stack, e)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].name != qualifiedName(t.Name) {
				return nil, fmt.Errorf("body is not XML: unexpected end element </%s>", qualifiedName(t.Name))
			}
			stack, scopes = stack[:len(stack)-1], scopes[:len(scopes)-1]
		case xml.CharData:
			if len(stack) > 0 {
				if len(t) > 0 {
					stack[len(stack)-1].addText(string(t))
				}
			} else if len(bytes.TrimSpace(t)) > 0 {
				return nil, fmt.Errorf("body is not XML: text outside the root element")
			}
		case xml.Comment, xml.ProcInst:
			if len(stack) > 0 {
				e := stack[len(stack)-1]
				e.children = append(e.children, xomNode{kind: xomOther})
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("body is not XML: no root element")
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("body is not XML: element <%s> is not closed", stack[len(stack)-1].name)
	}
	return root, nil
}

// addText appends text, to the last child if that is text too.
func (e *xomElem) addText(s string) {
	if n := len(e.children); n > 0 && e.children[n-1].kind == xomText {
		e.children[n-1].text += s
		return
	}
	e.children = append(e.children, xomNode{kind: xomText, text: s})
}

// childElements returns the child elements.
func (e *xomElem) childElements() []*xomElem {
	var es []*xomElem
	for _, c := range e.children {
		if c.kind == xomElement {
			es = append(es, c.elem)
		}
	}
	return es
}

// value returns all the text in the element and its descendants, in document order.
func (e *xomElem) value() string {
	var b strings.Builder
	e.appendValue(&b)
	return b.String()
}

func (e *xomElem) appendValue(b *strings.Builder) {
	for _, c := range e.children {
		switch c.kind {
		case xomText:
			b.WriteString(c.text)
		case xomElement:
			c.elem.appendValue(b)
		}
	}
}

// attr returns the value of the attribute with this name, which has no prefix.
func (e *xomElem) attr(name string) (string, bool) {
	for _, a := range e.attrs {
		if a.name == name {
			return a.value, true
		}
	}
	return "", false
}
