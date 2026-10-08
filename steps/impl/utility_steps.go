package impl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// wastebin drops the message. As an action it is a router with no routes, so
// the message stops there and the steps after it never get it.
type wastebin struct{}

func newWastebin(string, stepdef.Params) (stepdef.Processor, error) { return wastebin{}, nil }

func (wastebin) Route(context.Context, message.Message) ([]stepdef.Route, error) { return nil, nil }

// wastebinSink is the wastebin at the end of a path. It is a type of its own:
// the engine would treat a wastebin that is also a sink as a sink, which in an
// action position passes the message on.
type wastebinSink struct{}

func newWastebinSink(string, stepdef.Params) (stepdef.Processor, error) { return wastebinSink{}, nil }

func (wastebinSink) Consume(context.Context, message.Message) error { return nil }

// delayAction holds the message for a while before passing it on; a stopping
// flow does not wait for it.
type delayAction struct{ delay time.Duration }

func newDelayAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return delayAction{time.Duration(p["milliseconds"].(int)) * time.Millisecond}, nil
}

func (a delayAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	t := time.NewTimer(a.delay)
	defer t.Stop()
	select {
	case <-t.C:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// loggerAction writes an expression, such as ${body}, to the flow's log at a
// level (INFO, WARN, ERROR; OFF writes nothing) and passes the message on.
type loggerAction struct {
	prefix string // "step <id>: <level> "
	expr   expression
	off    bool
}

func newLoggerAction(stepID string, p stepdef.Params) (stepdef.Processor, error) {
	expr, err := compileExpression(p["language"].(string), p["expression"].(string))
	if err != nil {
		return nil, err
	}
	level := p["loggingLevel"].(string)
	return loggerAction{prefix: "step " + stepID + ": " + level + " ", expr: expr, off: level == "OFF"}, nil
}

func (a loggerAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	if a.off {
		return m, nil
	}
	s, err := a.expr.eval(m)
	if err != nil {
		return nil, err
	}
	stepdef.Logger(ctx).Print(a.prefix + s)
	return m, nil
}

// simpleValidator passes the message on when a simple condition holds for it
// and fails it otherwise, so the error route can take it.
type simpleValidator struct {
	expr string
	cond predicate
}

func newSimpleValidator(_ string, p stepdef.Params) (stepdef.Processor, error) {
	expr := strings.TrimSpace(p["expression"].(string))
	if expr == "" {
		return nil, fmt.Errorf("option expression: empty condition")
	}
	cond, err := compilePredicate("simple", expr)
	if err != nil {
		return nil, fmt.Errorf("option expression: %w", err)
	}
	return simpleValidator{expr, cond}, nil
}

func (v simpleValidator) Process(_ context.Context, m message.Message) (message.Message, error) {
	ok, err := v.cond(m)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return nil, fmt.Errorf("validation failed: %s", v.expr)
	}
	return m, nil
}
