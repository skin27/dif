package impl

import (
	"context"
	"fmt"
	"os"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// velocityAction replaces the body with the result of a Velocity template (see
// velocity.go for the part of the language that is supported), as Camel's
// velocity component does. The template can use:
//
//	$body            the body, as text
//	$headers         the headers of the message: $headers.name, $headers.get("name")
//	$in, $request    both of them: $in.body and $in.headers
//
// The template is compiled when the flow is built, so a template that uses
// something unsupported fails the build.
type velocityAction struct {
	tpl *velocityTemplate
}

func newVelocityAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	inline, file := p["resource"].(string), p["path"].(string)
	var src string
	switch {
	case (inline == "") == (file == ""):
		return nil, fmt.Errorf("set one of the options resource (velocity:ref:<name>) and path")
	case inline != "":
		src = inline
	default:
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("option path: %w", err)
		}
		src = string(data)
	}
	t, err := compileVelocity(src)
	if err != nil {
		return nil, err
	}
	return velocityAction{t}, nil
}

func (a velocityAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	headers := make(vHeaders, len(m))
	for k, v := range m {
		if k != message.Body && !strings.HasPrefix(k, message.MetadataPrefix) {
			headers[k] = velocityValue(v)
		}
	}
	body := vString(velocityValue(m[message.Body]))
	in := map[string]any{"body": body, "headers": headers}
	out, err := a.tpl.run(map[string]any{"body": body, "headers": headers, "in": in, "request": in})
	if err != nil {
		return nil, err
	}
	m[message.Body] = out
	return m, nil
}

// velocityValue is a header value as a template sees it: a string, a number
// (int64 or float64), a boolean, a list or a map.
func velocityValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint:
		return int64(x)
	case uint32:
		return int64(x)
	case float32:
		return float64(x)
	case nil, string, bool, int64, float64, []any, map[string]any:
		return v
	}
	return fmt.Sprint(v)
}
