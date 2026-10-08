package impl

import (
	"context"
	"encoding/json"
	"fmt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// setHeadersAction sets several headers, each to the value of an expression.
// In DIL the step refers to a core message (setheaders:message:<name>); the
// DIL parser passes that message's headers as the JSON option "headers".
type setHeadersAction struct {
	headers []headerExpr
}

type headerExpr struct {
	name  string
	value expression
}

func newSetHeadersAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	var defs []struct {
		Name, Value, Language string
	}
	if err := json.Unmarshal([]byte(p["headers"].(string)), &defs); err != nil {
		return nil, fmt.Errorf("option headers: want a JSON array of {name, value, language}: %w", err)
	}

	writeAsString, _ := p["writeAsString"].(bool)
	a := setHeadersAction{headers: make([]headerExpr, 0, len(defs))}
	for _, d := range defs {
		if err := checkHeaderName(d.Name); err != nil {
			return nil, err
		}
		lang := d.Language
		if lang == "" {
			lang = "simple"
		}
		value, err := compileValue(flowOf(p), lang, d.Value, writeAsString)
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", d.Name, err)
		}
		a.headers = append(a.headers, headerExpr{d.Name, value})
	}
	return a, nil
}

// Process sets the headers in order, so a header can refer to an earlier one.
func (a setHeadersAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	for _, h := range a.headers {
		v, err := h.value.eval(m)
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", h.name, err)
		}
		m[h.name] = v
	}
	return m, nil
}
