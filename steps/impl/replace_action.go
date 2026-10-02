package impl

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// replaceAction replaces every match of a regular expression in the body.
// With group > 0 only that capture group of each match is replaced.
type replaceAction struct {
	re      *regexp.Regexp
	replace string
	group   int
}

func newReplaceAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	var flags string
	for f := range strings.SplitSeq(p["flags"].(string), ",") {
		switch f = strings.TrimSpace(f); f {
		case "":
		case "i", "m", "s":
			flags += f
		default:
			return nil, fmt.Errorf("option flags: unknown flag %q; use i, m or s", f)
		}
	}
	expr := p["regex"].(string)
	if flags != "" {
		expr = "(?" + flags + ")" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("option regex: %w", err)
	}
	group := p["group"].(int)
	if group > re.NumSubexp() {
		return nil, fmt.Errorf("option group: %d, but regex has %d groups", group, re.NumSubexp())
	}
	return replaceAction{re, p["replaceWith"].(string), group}, nil
}

func (a replaceAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	body := text(m[message.Body])
	if a.group == 0 {
		m[message.Body] = a.re.ReplaceAllString(body, a.replace)
		return m, nil
	}

	var b strings.Builder
	last := 0
	for _, match := range a.re.FindAllStringSubmatchIndex(body, -1) {
		start, end := match[2*a.group], match[2*a.group+1]
		if start < 0 { // the group took no part in this match
			continue
		}
		b.WriteString(body[last:start])
		b.Write(a.re.ExpandString(nil, a.replace, body, match))
		last = end
	}
	b.WriteString(body[last:])
	m[message.Body] = b.String()
	return m, nil
}
