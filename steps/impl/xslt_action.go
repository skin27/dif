package impl

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/knroy/go-xml/xdm"
	"github.com/knroy/go-xml/xslt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// xsltAction transforms an XML body with a stylesheet, as Camel's xslt-saxon
// does with Saxon: XSLT 2.0 and 3.0, and 1.0 stylesheets in their backwards
// compatible mode, by github.com/knroy/go-xml (xslt.Compile, Transform). The
// stylesheet is compiled once when the flow is built, so an error in it fails
// the build; a body that is no XML, or a stylesheet that stops with
// xsl:message terminate="yes", fails the message.
//
// As in Camel, the headers of the message are the values of the stylesheet's
// top-level parameters. They are limited to what has an XML Schema type, text,
// numbers and booleans, with a name that is an XML name.
//
// The processor reads nothing the stylesheet names: xsl:include, xsl:import,
// document(), collection(), unparsed-text() and the process environment are
// closed, as go-xml leaves them with nil resolvers. A stylesheet that is
// somebody's input must not read the files of the host.
type xsltAction struct {
	sheet *xslt.Stylesheet
	out   xslt.OutputSettings
}

func newXSLTAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	inline, file := p["resource"].(string), p["path"].(string)
	var src string
	switch {
	case (inline == "") == (file == ""):
		return nil, fmt.Errorf("set one of the options resource (xslt:ref:<name>) and path")
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
		return nil, fmt.Errorf("stylesheet is not XML: %s", firstLine(err))
	}
	sheet, err := xslt.Compile(tree.Root, xslt.CompileOptions{})
	if err != nil {
		return nil, fmt.Errorf("stylesheet: %s", firstLine(err))
	}
	return xsltAction{sheet, sheet.Output()}, nil
}

func (a xsltAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	tree, err := xdm.ParseString(string(bytesOf(m[message.Body])), xdm.ParseOptions{})
	if err != nil {
		return nil, fmt.Errorf("body is not XML: %s", firstLine(err))
	}
	res, err := a.sheet.Transform(ctx, tree.Root, xslt.TransformOptions{Params: xsltParams(m)})
	if err != nil {
		return nil, fmt.Errorf("xslt: %s", firstLine(err))
	}
	for _, msg := range res.Messages {
		stepdef.Logger(ctx).Printf("xslt: %s", strings.TrimSpace(msg))
	}
	m[message.Body] = a.serialize(res)
	return m, nil
}

// serialize writes the result as go-xml does, except for the two things in
// which it follows the specification and Saxon, which the platform runs, does
// not:
//
//   - An html result with no html-version or version has the HTML 5 document
//     type, <!DOCTYPE HTML>, as XSLT 3.0 makes 5.0 the default.
//   - Indentation is three spaces a level, go-xml writes two.
//
// The platform's flows are compared with its output, so they show.
func (a xsltAction) serialize(res *xslt.Result) string {
	out := res.String()
	o := a.out
	html := strings.EqualFold(o.Method, "html") || o.Method == "" && rootIsHTML(res.Nodes)
	if html && o.HTMLVersion == "" && o.Version == "" && o.DocTypePublic == "" && o.DocTypeSystem == "" && rootIsHTML(res.Nodes) {
		out = "<!DOCTYPE HTML>\n" + out
	}
	if !strings.EqualFold(o.Method, "text") && (o.Indent || o.Method == "" && html) {
		out = indentThreeSpaces(out)
	}
	return out
}

// rootIsHTML tells if the result is a document whose element is html, which
// makes html the output method when the stylesheet names none.
func rootIsHTML(seq xdm.Sequence) bool {
	for _, it := range seq {
		if n, ok := it.(*xdm.Node); ok {
			switch n.Kind {
			case xdm.KindElement:
				return n.Name.URI == "" && strings.EqualFold(n.Name.Local, "html")
			case xdm.KindDocument:
				for _, c := range n.Children {
					if c.Kind == xdm.KindElement {
						return c.Name.URI == "" && strings.EqualFold(c.Name.Local, "html")
					}
				}
			}
		}
	}
	return false
}

// indentThreeSpaces turns the two spaces a level that go-xml indents with into
// the three of Saxon, in front of every tag that starts a line.
func indentThreeSpaces(s string) string {
	if !strings.Contains(s, "\n  ") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		t := strings.TrimLeft(l, " ")
		if n := len(l) - len(t); n > 0 && n%2 == 0 && strings.HasPrefix(t, "<") {
			lines[i] = strings.Repeat(" ", n/2*3) + t
		}
	}
	return strings.Join(lines, "\n")
}

// xsltParams are the headers of m a stylesheet can take as parameters.
func xsltParams(m message.Message) map[string]xdm.Sequence {
	var params map[string]xdm.Sequence
	for k, v := range m {
		if k == message.Body || strings.HasPrefix(k, message.MetadataPrefix) || !isXMLName(k) {
			continue
		}
		var item xdm.Item
		switch x := v.(type) {
		case string:
			item = xdm.NewString(x)
		case bool:
			item = xdm.NewBoolean(x)
		case int:
			item = xdm.NewInteger(int64(x))
		case int64:
			item = xdm.NewInteger(x)
		case float64:
			item = xdm.NewDouble(x)
		default:
			continue
		}
		if params == nil {
			params = map[string]xdm.Sequence{}
		}
		params[k] = xdm.Sequence{item}
	}
	return params
}
