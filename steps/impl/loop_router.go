package impl

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// Headers a loop sets on the message of every round.
const (
	loopIndex = "loop.index" // the round, from 0
	loopSize  = "loop.size"  // the number of rounds (loop only)
)

// loopRouter sends the message along its loop link round after round: loop a
// number of times, dowhile as long as a condition holds. The loop link is the
// one with rule loop or dowhile (or with an expression); the other link, if
// any, gets the message after the last round. As an action (one link) the
// loop link is the rest of the flow, run once per round.
//
// Every round gets the message that came out of the round before; loop with
// copy gives every round a copy of the message as it entered instead.
type loopRouter struct {
	body, after int // after is -1 if there is none
	copy        bool

	count func(message.Message) (int, error) // loop: the number of rounds
	while predicate                          // dowhile: runs another round while it holds
	max   int                                // dowhile: at most this many rounds
}

func newLoopRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	r, expr, lang, err := loopLinks(p, "loop")
	if err != nil {
		return nil, err
	}
	if expr == "" {
		expr = "1"
	}
	if lang != "simple" && lang != "constant" {
		return nil, fmt.Errorf("language %q is not supported for the number of rounds; use simple or constant", lang)
	}
	e, err := compileExpressionIn(flowOf(p), lang, expr)
	if err != nil {
		return nil, err
	}
	r.copy = p["copy"].(bool)
	r.count = func(m message.Message) (int, error) {
		s, err := e.eval(m)
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return 0, fmt.Errorf("number of rounds %q is not an integer", s)
		}
		return n, nil
	}
	return r, nil
}

func newDoWhileRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	r, expr, lang, err := loopLinks(p, "dowhile")
	if err != nil {
		return nil, err
	}
	if expr == "" {
		return nil, fmt.Errorf("no condition: set it on the dowhile link or as option expression")
	}
	if r.while, err = compilePredicateIn(flowOf(p), lang, expr); err != nil {
		return nil, err
	}
	r.max = p["maxLoops"].(int)
	return r, nil
}

// loopLinks finds the loop link and the link after it, and the expression and
// language of the loop: the loop link's, else the options'.
func loopLinks(p stepdef.Params, rule string) (r loopRouter, expr, lang string, err error) {
	links := p[stepdef.Links].([]stepdef.Link)
	r.body, r.after = -1, -1
	for i, l := range links {
		if l.Rule == rule || (l.Rule == "" && l.Expression != "" && r.body < 0) {
			if r.body >= 0 {
				return r, "", "", fmt.Errorf("outbound links %d and %d are both loop links", r.body, i)
			}
			r.body = i
		}
	}
	switch {
	case len(links) == 1:
		r.body = 0
	case len(links) == 2 && r.body >= 0:
		r.after = 1 - r.body
	default:
		return r, "", "", fmt.Errorf("needs a link with rule %s and at most one other link, has %d links", rule, len(links))
	}

	expr, lang = links[r.body].Expression, links[r.body].Language
	if expr == "" {
		expr = p["expression"].(string)
	}
	if lang == "" {
		lang = p["language"].(string)
	}
	return r, expr, lang, nil
}

func (r loopRouter) Round(_ context.Context, in, prev message.Message, round int) ([]stepdef.Route, bool, error) {
	more, size := false, 0
	if r.count != nil {
		var err error
		if size, err = r.count(in); err != nil {
			return nil, false, err
		}
		more = round < size
	} else if round < r.max {
		var err error
		if more, err = r.while(prev); err != nil {
			return nil, false, err
		}
	}

	if !more {
		if r.after < 0 {
			return nil, true, nil
		}
		return []stepdef.Route{{Next: r.after, Message: prev}}, true, nil
	}
	m := prev
	if round == 0 || r.copy {
		m = in.Copy() // in stays as it entered
	}
	m[loopIndex] = round
	if r.count != nil {
		m[loopSize] = size
	}
	return []stepdef.Route{{Next: r.body, Message: m}}, false, nil
}
