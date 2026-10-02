package impl

import (
	"context"
	"slices"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// logAction writes the message to the flow's logger and passes it on.
// The trace id is always logged; headers and body on request.
type logAction struct {
	stepID      string
	showHeaders bool
	showBody    bool
}

func newLogAction(stepID string, p stepdef.Params) (stepdef.Processor, error) {
	return logAction{stepID: stepID, showHeaders: p["showHeaders"].(bool), showBody: p["showBody"].(bool)}, nil
}

func (l logAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	var b strings.Builder
	b.WriteString("step ")
	b.WriteString(l.stepID)
	b.WriteString(": traceid=")
	b.WriteString(text(m[message.TraceID]))

	if l.showHeaders {
		keys := make([]string, 0, len(m))
		for k := range m {
			if k != message.Body && k != message.TraceID {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		b.WriteString(" headers={")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(text(m[k]))
		}
		b.WriteString("}")
	}
	if l.showBody {
		b.WriteString(" body=")
		b.WriteString(text(m[message.Body]))
	}

	stepdef.Logger(ctx).Print(b.String())
	return m, nil
}
