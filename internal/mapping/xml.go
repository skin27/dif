package mapping

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type xmlNode struct {
	name     string
	attrs    object
	children []any
}
type xmlText string

const xmlNamespace = "http://www.w3.org/XML/1998/namespace"

func ncname(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(unicode.IsLetter(r) || r == '_' || i > 0 && (unicode.IsDigit(r) || r == '-' || r == '.' || unicode.IsMark(r))) {
			return false
		}
	}
	return true
}
func expanded(s string) (uri, local string, err error) {
	local = s
	if strings.HasPrefix(s, "{") {
		at := strings.IndexByte(s, '}')
		if at < 2 {
			return "", "", fmt.Errorf("invalid expanded XML name")
		}
		uri, local = s[1:at], s[at+1:]
	}
	if !ncname(local) || uri == "http://www.w3.org/2000/xmlns/" || local == "xmlns" && uri == "" {
		return "", "", fmt.Errorf("invalid XML name")
	}
	return
}
func xmlChars(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !(r == 9 || r == 10 || r == 13 || r >= 0x20 && r <= 0xD7FF || r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF) {
			return false
		}
	}
	return true
}
func readXML(data []byte, b *budget) (any, error) {
	if len(data) > b.limits.Bytes || !utf8.Valid(data) {
		return nil, fmt.Errorf("XML byte limit or encoding error")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	stack := []*xmlNode{}
	rawNames := []xml.Name{}
	scopes := []map[string]string{{"xml": xmlNamespace}}
	var root *xmlNode
	decl := false
	resolve := func(n xml.Name, ns map[string]string, attribute bool) (string, error) {
		if !ncname(n.Local) {
			return "", fmt.Errorf("invalid XML name")
		}
		uri := ns[n.Space]
		if n.Space != "" && uri == "" {
			return "", fmt.Errorf("undeclared XML namespace")
		}
		if attribute && n.Space == "" {
			uri = ""
		}
		if uri != "" {
			return "{" + uri + "}" + n.Local, nil
		}
		return n.Local, nil
	}
	for {
		if e := b.tick(); e != nil {
			return nil, e
		}
		t, e := d.RawToken()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("invalid XML at byte %d", d.InputOffset())
		}
		switch t := t.(type) {
		case xml.StartElement:
			if e := b.node(len(stack)); e != nil {
				return nil, e
			}
			ns := map[string]string{}
			for k, v := range scopes[len(scopes)-1] {
				ns[k] = v
			}
			seenDecl := map[string]bool{}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Space == "" && a.Name.Local == "xmlns" {
					key := a.Name.Local
					if a.Name.Space == "" {
						key = ""
					}
					if seenDecl[key] || key == "xmlns" || key == "xml" && a.Value != xmlNamespace || key != "xml" && a.Value == xmlNamespace || a.Value == "http://www.w3.org/2000/xmlns/" || key != "" && a.Value == "" {
						return nil, fmt.Errorf("invalid namespace declaration")
					}
					seenDecl[key] = true
					ns[key] = a.Value
				}
			}
			name, e := resolve(t.Name, ns, false)
			if e != nil {
				return nil, e
			}
			n := &xmlNode{name: name, attrs: object{}, children: []any{}}
			seen := map[string]bool{}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Space == "" && a.Name.Local == "xmlns" {
					continue
				}
				if e := b.node(len(stack) + 1); e != nil {
					return nil, e
				}
				name, e := resolve(a.Name, ns, true)
				if e != nil {
					return nil, e
				}
				if seen[name] {
					return nil, fmt.Errorf("duplicate XML attribute")
				}
				seen[name] = true
				n.attrs = append(n.attrs, member{name, a.Value})
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("XML requires one root")
				}
				root = n
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, n)
			}
			stack = append(stack, n)
			rawNames = append(rawNames, t.Name)
			scopes = append(scopes, ns)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("unexpected XML end element")
			}
			if rawNames[len(rawNames)-1] != t.Name {
				return nil, fmt.Errorf("mismatched XML element")
			}
			name, e := resolve(t.Name, scopes[len(scopes)-1], false)
			if e != nil || name != stack[len(stack)-1].name {
				return nil, fmt.Errorf("mismatched XML element")
			}
			stack = stack[:len(stack)-1]
			rawNames = rawNames[:len(rawNames)-1]
			scopes = scopes[:len(scopes)-1]
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return nil, fmt.Errorf("text outside XML root")
				}
			} else {
				if e := b.node(len(stack)); e != nil {
					return nil, e
				}
				n := stack[len(stack)-1]
				n.children = append(n.children, xmlText(string(t)))
			}
		case xml.ProcInst:
			if t.Target != "xml" || root != nil || decl {
				return nil, fmt.Errorf("XML processing instructions are unsupported")
			}
			decl = true
			// Decoder enforces UTF-8; no CharsetReader is installed.
		default:
			return nil, fmt.Errorf("XML comments and directives are unsupported")
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, fmt.Errorf("incomplete XML document")
	}
	return root, nil
}
func xmlCall(name string, a []any, b *budget) (any, error) {
	bad := func() (any, error) { return nil, fmt.Errorf("%s: invalid XML argument", name) }
	if name == "textNode" {
		v, ok := a[0].(string)
		if !ok || !xmlChars(v) {
			return bad()
		}
		return xmlText(v), nil
	}
	if name == "element" {
		name, ok := a[0].(string)
		attrs, ok2 := a[1].(object)
		children, ok3 := a[2].([]any)
		if !ok || !ok2 || !ok3 {
			return bad()
		}
		if _, _, e := expanded(name); e != nil {
			return nil, e
		}
		if e := b.node(0); e != nil {
			return nil, e
		}
		for _, m := range attrs {
			if _, _, e := expanded(m.key); e != nil {
				return nil, e
			}
			s, ok := m.value.(string)
			if !ok || !xmlChars(s) {
				return bad()
			}
		}
		for _, c := range children {
			switch c.(type) {
			case *xmlNode, xmlText:
			default:
				return bad()
			}
		}
		return &xmlNode{name, attrs, children}, nil
	}
	n, ok := a[0].(*xmlNode)
	if !ok {
		return bad()
	}
	if name == "text" {
		var s strings.Builder
		for _, c := range n.children {
			if e := b.tick(); e != nil {
				return nil, e
			}
			t, ok := c.(xmlText)
			if !ok {
				return nil, fmt.Errorf("text requires a text-only element")
			}
			if s.Len()+len(t) > b.limits.Bytes {
				return nil, fmt.Errorf("text size limit exceeded")
			}
			s.WriteString(string(t))
		}
		return s.String(), nil
	}
	key, ok := a[1].(string)
	if !ok {
		return bad()
	}
	if _, _, e := expanded(key); e != nil {
		return nil, e
	}
	if name == "attr" {
		return b.lookup(n.attrs, key)
	}
	out := []any{}
	for _, c := range n.children {
		if e := b.tick(); e != nil {
			return nil, e
		}
		if child, ok := c.(*xmlNode); ok && child.name == key {
			out = append(out, child)
		}
	}
	if name == "child" {
		if len(out) != 1 {
			return nil, fmt.Errorf("child requires exactly one matching element")
		}
		return out[0], nil
	}
	return out, nil
}
func (w *output) xml(v any) error {
	root, ok := v.(*xmlNode)
	if !ok {
		return fmt.Errorf("XML output requires an element root")
	}
	namespaces := map[string]string{xmlNamespace: "xml"}
	var scan func(*xmlNode, int) error
	scan = func(n *xmlNode, depth int) error {
		if e := w.b.node(depth); e != nil {
			return e
		}
		names := []string{n.name}
		for _, a := range n.attrs {
			names = append(names, a.key)
		}
		for _, name := range names {
			uri, _, e := expanded(name)
			if e != nil {
				return e
			}
			if uri != "" && uri != xmlNamespace {
				namespaces[uri] = ""
			}
		}
		for _, c := range n.children {
			if x, ok := c.(*xmlNode); ok {
				if e := scan(x, depth+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := scan(root, 0); e != nil {
		return e
	}
	uris := []string{}
	for uri := range namespaces {
		if uri != xmlNamespace {
			uris = append(uris, uri)
		}
	}
	sort.Strings(uris)
	for i, uri := range uris {
		namespaces[uri] = "n" + strconv.Itoa(i)
	}
	qname := func(s string) string {
		uri, local, _ := expanded(s)
		if uri == "" {
			return local
		}
		return namespaces[uri] + ":" + local
	}
	esc := func(s string) (string, error) {
		if !xmlChars(s) {
			return "", fmt.Errorf("invalid XML characters")
		}
		var buf bytes.Buffer
		xml.EscapeText(&buf, []byte(s))
		return buf.String(), nil
	}
	var emit func(*xmlNode, int) error
	emit = func(n *xmlNode, depth int) error {
		if e := w.b.node(depth); e != nil {
			return e
		}
		name := qname(n.name)
		if e := w.put("<" + name); e != nil {
			return e
		}
		if depth == 0 {
			for _, uri := range uris {
				s, e := esc(uri)
				if e != nil {
					return e
				}
				if e = w.put(" xmlns:" + namespaces[uri] + "=\"" + s + "\""); e != nil {
					return e
				}
			}
		}
		for _, a := range n.attrs {
			s, ok := a.value.(string)
			if !ok {
				return fmt.Errorf("XML attributes must be strings")
			}
			s, e := esc(s)
			if e != nil {
				return e
			}
			if e = w.put(" " + qname(a.key) + "=\"" + s + "\""); e != nil {
				return e
			}
		}
		if e := w.put(">"); e != nil {
			return e
		}
		for _, c := range n.children {
			switch x := c.(type) {
			case *xmlNode:
				if e := emit(x, depth+1); e != nil {
					return e
				}
			case xmlText:
				s, e := esc(string(x))
				if e != nil {
					return e
				}
				if e = w.put(s); e != nil {
					return e
				}
			default:
				return fmt.Errorf("invalid XML child")
			}
		}
		return w.put("</" + name + ">")
	}
	return emit(root, 0)
}
