package impl

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/knroy/go-xml/xdm"
	xp "github.com/knroy/go-xml/xpath"

	"dif/message"
	stepdef "dif/steps/definition"
)

// schematronAction validates an XML body against ISO Schematron rules, as
// Camel's schematron component does: the body goes on unchanged, and the
// headers say what came out:
//
//	CamelSchematronValidationStatus   SUCCESS, or FAILED when an assert did not hold
//	CamelSchematronValidationReport   the report, in SVRL
//
// A flow tells the two apart by the header (a router with a condition) or lets
// them go on. The rules are read directly, not compiled to XSLT as the ISO
// reference does; the tests are XPath 2.0 expressions, by github.com/knroy/go-xml.
//
// What is supported: ns, let (of the schema, a pattern and a rule), pattern,
// rule (a node fires only the first rule of a pattern whose context it matches),
// assert, report, and in messages value-of and name. Not supported, and
// refused when the schema is read: abstract patterns and rules (is-a, extends),
// include, and phases (all patterns are active). A report that holds is in the
// SVRL as successful-report but is no failure: only an assert is.
type schematronAction struct {
	title    string
	lets     []stLet
	patterns []stPattern
}

type stLet struct {
	name string
	expr *xp.Compiled
}

type stPattern struct {
	id    string
	lets  []stLet
	rules []stRule
}

type stRule struct {
	context string
	nodes   *xp.Compiled
	lets    []stLet
	checks  []stCheck
}

type stCheck struct {
	report   bool
	src      string
	test     *xp.Compiled
	id, role string
	message  []stPart
}

// stPart is a piece of a message: text, a value-of or a name.
type stPart struct {
	text string
	expr *xp.Compiled // value-of select, or name path
	name bool         // the name of the node (of path, if expr is set)
}

const schematronStatus, schematronReport = "CamelSchematronValidationStatus", "CamelSchematronValidationReport"

func newSchematronAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	inline, file := p["resource"].(string), p["path"].(string)
	var src string
	switch {
	case (inline == "") == (file == ""):
		return nil, fmt.Errorf("set one of the options resource (schematron:ref:<name>) and path")
	case inline != "":
		src = inline
	default:
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("option path: %w", err)
		}
		src = string(data)
	}
	tree, err := xdm.ParseString(src, xdm.ParseOptions{})
	if err != nil {
		return nil, fmt.Errorf("schema is not XML: %s", firstLine(err))
	}
	a, err := compileSchematron(tree.Root)
	if err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return a, nil
}

func elementChildren(n *xdm.Node) []*xdm.Node {
	var out []*xdm.Node
	for _, c := range n.Children {
		if c.Kind == xdm.KindElement {
			out = append(out, c)
		}
	}
	return out
}

func attrOf(n *xdm.Node, name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name && a.Name.URI == "" {
			return a.StringValue()
		}
	}
	return ""
}

func compileSchematron(doc *xdm.Node) (*schematronAction, error) {
	var root *xdm.Node
	for _, c := range doc.Children {
		if c.Kind == xdm.KindElement {
			root = c
		}
	}
	if root == nil || root.Name.Local != "schema" {
		return nil, fmt.Errorf("the root element is not schema")
	}
	ns := xpathNS{"xs": "http://www.w3.org/2001/XMLSchema"} // as in XSLT, where the tests run in the reference
	for _, c := range elementChildren(root) {
		if c.Name.Local == "ns" {
			ns[attrOf(c, "prefix")] = attrOf(c, "uri")
		}
	}
	compile := func(expr, what string) (*xp.Compiled, error) {
		c, err := xp.CompileWith(expr, xp.CompileOptions{Namespaces: ns, MaxBytes: maxXPathSize})
		if err != nil {
			return nil, fmt.Errorf("%s %q: %s", what, expr, firstLine(err))
		}
		return c, nil
	}
	lets := func(parent *xdm.Node) ([]stLet, error) {
		var out []stLet
		for _, c := range elementChildren(parent) {
			if c.Name.Local == "let" {
				x, err := compile(attrOf(c, "value"), "let "+attrOf(c, "name"))
				if err != nil {
					return nil, err
				}
				out = append(out, stLet{attrOf(c, "name"), x})
			}
		}
		return out, nil
	}

	a := &schematronAction{}
	var err error
	if a.lets, err = lets(root); err != nil {
		return nil, err
	}
	for _, c := range elementChildren(root) {
		switch c.Name.Local {
		case "title":
			a.title = strings.TrimSpace(c.StringValue())
		case "ns", "let", "p", "diagnostics", "phase", "properties":
		case "include":
			return nil, fmt.Errorf("include is not supported")
		case "pattern":
			pat, err := compileStPattern(c, compile, lets)
			if err != nil {
				return nil, err
			}
			a.patterns = append(a.patterns, pat)
		default:
			return nil, fmt.Errorf("the element %s is not supported", c.Name.Local)
		}
	}
	if len(a.patterns) == 0 {
		return nil, fmt.Errorf("the schema has no pattern")
	}
	return a, nil
}

func compileStPattern(n *xdm.Node, compile func(string, string) (*xp.Compiled, error), lets func(*xdm.Node) ([]stLet, error)) (stPattern, error) {
	pat := stPattern{id: attrOf(n, "id")}
	if attrOf(n, "abstract") == "true" || attrOf(n, "is-a") != "" {
		return pat, fmt.Errorf("abstract patterns (pattern %q) are not supported", pat.id)
	}
	var err error
	if pat.lets, err = lets(n); err != nil {
		return pat, err
	}
	for _, r := range elementChildren(n) {
		switch r.Name.Local {
		case "let", "p", "title":
			continue
		case "rule":
		default:
			return pat, fmt.Errorf("the element %s in pattern %q is not supported", r.Name.Local, pat.id)
		}
		if attrOf(r, "abstract") == "true" {
			return pat, fmt.Errorf("abstract rules are not supported")
		}
		rule := stRule{context: attrOf(r, "context")}
		if rule.context == "" {
			return pat, fmt.Errorf("a rule has no context")
		}
		if rule.nodes, err = compile(contextXPath(rule.context), "rule context"); err != nil {
			return pat, err
		}
		if rule.lets, err = lets(r); err != nil {
			return pat, err
		}
		for _, c := range elementChildren(r) {
			switch c.Name.Local {
			case "let", "p":
			case "extends":
				return pat, fmt.Errorf("extends is not supported")
			case "assert", "report":
				check := stCheck{report: c.Name.Local == "report", src: attrOf(c, "test"), id: attrOf(c, "id"), role: attrOf(c, "role")}
				if check.src == "" {
					return pat, fmt.Errorf("an %s has no test", c.Name.Local)
				}
				if check.test, err = compile(check.src, c.Name.Local+" test"); err != nil {
					return pat, err
				}
				if check.message, err = stMessage(c, compile); err != nil {
					return pat, err
				}
				rule.checks = append(rule.checks, check)
			default:
				return pat, fmt.Errorf("the element %s in a rule is not supported", c.Name.Local)
			}
		}
		pat.rules = append(pat.rules, rule)
	}
	return pat, nil
}

// stMessage reads the message of an assert or report.
func stMessage(n *xdm.Node, compile func(string, string) (*xp.Compiled, error)) ([]stPart, error) {
	var parts []stPart
	for _, c := range n.Children {
		switch {
		case c.Kind == xdm.KindText:
			parts = append(parts, stPart{text: c.StringValue()})
		case c.Kind == xdm.KindElement && c.Name.Local == "value-of":
			x, err := compile(attrOf(c, "select"), "value-of")
			if err != nil {
				return nil, err
			}
			parts = append(parts, stPart{expr: x})
		case c.Kind == xdm.KindElement && c.Name.Local == "name":
			part := stPart{name: true}
			if path := attrOf(c, "path"); path != "" {
				x, err := compile(path, "name path")
				if err != nil {
					return nil, err
				}
				part.expr = x
			}
			parts = append(parts, part)
		case c.Kind == xdm.KindElement:
			parts = append(parts, stPart{text: c.StringValue()}) // emph, dir, span
		}
	}
	return parts, nil
}

// contextXPath turns a Schematron context, which is an XSLT match pattern (a
// node anywhere that matches), into an XPath expression that selects those
// nodes: section becomes //section, /doc/a stays as it is, and a | b becomes
// //a | //b.
func contextXPath(pattern string) string {
	var branches []string
	depth, start := 0, 0
	quote := rune(0)
	for i, r := range pattern {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '(' || r == '[':
			depth++
		case r == ')' || r == ']':
			depth--
		case r == '|' && depth == 0:
			branches = append(branches, pattern[start:i])
			start = i + 1
		}
	}
	branches = append(branches, pattern[start:])
	for i, b := range branches {
		b = strings.TrimSpace(b)
		if !strings.HasPrefix(b, "/") {
			b = "//" + b
		}
		branches[i] = b
	}
	return strings.Join(branches, " | ")
}

func (a *schematronAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	tree, err := xdm.ParseString(string(bytesOf(m[message.Body])), xdm.ParseOptions{})
	if err != nil {
		return nil, fmt.Errorf("body is not XML: %s", firstLine(err))
	}
	report, failed, err := a.validate(ctx, tree.Root)
	if err != nil {
		return nil, fmt.Errorf("schematron: %w", err)
	}
	status := "SUCCESS"
	if failed > 0 {
		status = "FAILED"
	}
	m[schematronStatus], m[schematronReport] = status, report
	return m, nil
}

// validate checks the document and returns the SVRL report and the number of
// asserts that failed.
func (a *schematronAction) validate(ctx context.Context, doc *xdm.Node) (string, int, error) {
	base := xp.NewContext(doc, xp.Builtins())
	base.Ctx = ctx
	bind := func(c *xp.Context, lets []stLet) (*xp.Context, error) {
		for _, l := range lets {
			v, err := l.expr.Eval(c)
			if err != nil {
				return nil, fmt.Errorf("let %s: %s", l.name, firstLine(err))
			}
			c = c.WithVar(xdm.QName{Local: l.name}, v)
		}
		return c, nil
	}
	root, err := bind(base, a.lets)
	if err != nil {
		return "", 0, err
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<svrl:schematron-output xmlns:svrl="http://purl.oclc.org/dsdl/svrl" title="` + attrEscaper.Replace(a.title) + `" schemaVersion="">` + "\n")
	failed := 0
	for _, pat := range a.patterns {
		b.WriteString(`<svrl:active-pattern document="" id="` + attrEscaper.Replace(pat.id) + `" name=""/>` + "\n")
		patCtx, err := bind(root, pat.lets)
		if err != nil {
			return "", 0, err
		}
		done := map[*xdm.Node]bool{} // a node fires one rule of a pattern
		for _, rule := range pat.rules {
			seq, err := rule.nodes.Eval(patCtx)
			if err != nil {
				return "", 0, fmt.Errorf("context %q: %s", rule.context, firstLine(err))
			}
			for i, it := range seq {
				node, ok := it.(*xdm.Node)
				if !ok || done[node] {
					continue
				}
				done[node] = true
				b.WriteString(`<svrl:fired-rule context="` + attrEscaper.Replace(rule.context) + `"/>` + "\n")
				nodeCtx, err := bind(patCtx.WithFocus(node, i+1, len(seq)), rule.lets)
				if err != nil {
					return "", 0, err
				}
				for _, check := range rule.checks {
					holds, err := check.test.EvalBool(nodeCtx)
					if err != nil {
						return "", 0, fmt.Errorf("test %q: %s", check.src, firstLine(err))
					}
					if holds != check.report { // an assert that holds, or a report that does not, says nothing
						continue
					}
					tag := "failed-assert"
					if check.report {
						tag = "successful-report"
					} else {
						failed++
					}
					text, err := check.text(nodeCtx, node)
					if err != nil {
						return "", 0, err
					}
					b.WriteString(`<svrl:` + tag + ` test="` + attrEscaper.Replace(check.src) + `"`)
					if check.id != "" {
						b.WriteString(` id="` + attrEscaper.Replace(check.id) + `"`)
					}
					if check.role != "" {
						b.WriteString(` role="` + attrEscaper.Replace(check.role) + `"`)
					}
					b.WriteString(` location="` + attrEscaper.Replace(nodePath(node)) + `">` + "\n")
					b.WriteString("<svrl:text>" + xmlEscaper.Replace(xmlSafe(text)) + "</svrl:text>\n")
					b.WriteString("</svrl:" + tag + ">\n")
				}
			}
		}
	}
	b.WriteString("</svrl:schematron-output>\n")
	return b.String(), failed, nil
}

// text writes the message of a check for a node.
func (c stCheck) text(ctx *xp.Context, node *xdm.Node) (string, error) {
	var b strings.Builder
	for _, p := range c.message {
		switch {
		case p.name && p.expr == nil:
			b.WriteString(node.Name.Lexical())
		case p.name:
			seq, err := p.expr.Eval(ctx)
			if err != nil {
				return "", fmt.Errorf("name path: %s", firstLine(err))
			}
			if len(seq) > 0 {
				if n, ok := seq[0].(*xdm.Node); ok {
					b.WriteString(n.Name.Lexical())
				}
			}
		case p.expr != nil:
			s, err := p.expr.EvalString(ctx)
			if err != nil {
				return "", fmt.Errorf("value-of: %s", firstLine(err))
			}
			b.WriteString(s)
		default:
			b.WriteString(p.text)
		}
	}
	return strings.Join(strings.Fields(b.String()), " "), nil
}

// nodePath is the position of a node in its document, as /document[1]/section[2].
func nodePath(n *xdm.Node) string {
	var parts []string
	for ; n != nil && n.Kind != xdm.KindDocument; n = n.Parent {
		switch n.Kind {
		case xdm.KindElement:
			pos := 1
			if n.Parent != nil {
				for _, s := range n.Parent.Children {
					if s == n {
						break
					}
					if s.Kind == xdm.KindElement && s.Name == n.Name {
						pos++
					}
				}
			}
			parts = append(parts, n.Name.Lexical()+"["+strconv.Itoa(pos)+"]")
		case xdm.KindAttribute:
			parts = append(parts, "@"+n.Name.Lexical())
		default:
			parts = append(parts, "node()")
		}
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return "/" + strings.Join(parts, "/")
}
