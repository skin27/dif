package impl

import (
	"encoding/json"
	"fmt"
	"strings"

	"dif/message"
)

// expression is a compiled setbody/setheader expression: literal text with
// references to message keys in between.
type expression []segment

type segment struct {
	text string // literal text; used when key is empty
	key  string // message key whose value is inserted
	fail error  // evaluating the segment fails with this error
}

// compileExpression compiles expr in language "constant" (literal text) or
// "simple", which supports ${body} (also written ${bodyAs(String)}),
// ${header.<name>} and ${headers.<name>}. ${bodyAs(<type>)} with any other
// type compiles, but fails when evaluated, as converting the body does in
// Camel; anything else is rejected.
func compileExpression(language, expr string) (expression, error) {
	if language == "constant" {
		return expression{{text: expr}}, nil
	}

	var e expression
	for {
		i := strings.Index(expr, "${")
		if i < 0 {
			if expr != "" {
				e = append(e, segment{text: expr})
			}
			return e, nil
		}
		j := strings.IndexByte(expr[i:], '}')
		if j < 0 {
			return nil, fmt.Errorf("unclosed ${ in expression")
		}
		seg, err := simpleRef(expr[i+2 : i+j])
		if err != nil {
			return nil, err
		}
		if i > 0 {
			e = append(e, segment{text: expr[:i]})
		}
		e = append(e, seg)
		expr = expr[i+j+1:]
	}
}

func simpleRef(ref string) (segment, error) {
	if ref == "body" || ref == "bodyAs(String)" { // values are rendered as text anyway
		return segment{key: message.Body}, nil
	}
	if typ, ok := strings.CutPrefix(ref, "bodyAs("); ok && strings.HasSuffix(typ, ")") && isTypeName(typ[:len(typ)-1]) {
		typ = typ[:len(typ)-1]
		return segment{fail: fmt.Errorf("${bodyAs(%s)}: the body cannot be converted to %s; only String is supported", typ, typ)}, nil
	}
	for _, prefix := range []string{"header.", "headers."} {
		if name, ok := strings.CutPrefix(ref, prefix); ok && name != "" {
			return segment{key: name}, nil
		}
	}
	return segment{}, fmt.Errorf("unsupported simple expression ${%s}; use ${body} or ${header.<name>}", ref)
}

// isTypeName reports whether s looks like a Java type name, such as String,
// java.lang.Integer or byte[].
func isTypeName(s string) bool {
	return s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.$[]") == ""
}

// eval returns the expression's value for m. A missing key gives "".
func (e expression) eval(m message.Message) (string, error) {
	if len(e) == 1 && e[0].key == "" && e[0].fail == nil {
		return e[0].text, nil
	}
	var b strings.Builder
	for _, s := range e {
		switch {
		case s.fail != nil:
			return "", s.fail
		case s.key == "":
			b.WriteString(s.text)
		default:
			b.WriteString(text(m[s.key]))
		}
	}
	return b.String(), nil
}

// text renders a message value as text; JSON values are rendered as JSON.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case map[string]any, []any:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
	}
	return fmt.Sprint(v)
}

// bytesOf renders a message value as bytes: []byte as is, anything else as text.
func bytesOf(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	return []byte(text(v))
}
