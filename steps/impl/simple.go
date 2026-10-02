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
}

// compileExpression compiles expr in language "constant" (literal text) or
// "simple", which supports ${body} (also written ${bodyAs(String)}),
// ${header.<name>} and ${headers.<name>}.
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
		key, err := simpleRef(expr[i+2 : i+j])
		if err != nil {
			return nil, err
		}
		if i > 0 {
			e = append(e, segment{text: expr[:i]})
		}
		e = append(e, segment{key: key})
		expr = expr[i+j+1:]
	}
}

func simpleRef(ref string) (string, error) {
	if ref == "body" || ref == "bodyAs(String)" { // values are rendered as text anyway
		return message.Body, nil
	}
	for _, prefix := range []string{"header.", "headers."} {
		if name, ok := strings.CutPrefix(ref, prefix); ok && name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("unsupported simple expression ${%s}; use ${body} or ${header.<name>}", ref)
}

// eval returns the expression's value for m. A missing key gives "".
func (e expression) eval(m message.Message) string {
	if len(e) == 1 && e[0].key == "" {
		return e[0].text
	}
	var b strings.Builder
	for _, s := range e {
		if s.key == "" {
			b.WriteString(s.text)
		} else {
			b.WriteString(text(m[s.key]))
		}
	}
	return b.String()
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
