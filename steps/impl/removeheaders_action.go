package impl

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// removeHeadersAction removes the headers whose name matches pattern and not
// excludePattern. The body and metadata are never removed.
type removeHeadersAction struct {
	pattern, exclude headerPattern
}

func newRemoveHeadersAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	pattern, err := compileHeaderPattern(p["pattern"].(string))
	if err != nil {
		return nil, fmt.Errorf("option pattern: %w", err)
	}
	exclude, err := compileHeaderPattern(p["excludePattern"].(string))
	if err != nil {
		return nil, fmt.Errorf("option excludePattern: %w", err)
	}
	return removeHeadersAction{pattern, exclude}, nil
}

func (a removeHeadersAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	for k := range m {
		if k != message.Body && !message.IsMetadata(k) && a.pattern.match(k) && !a.exclude.match(k) {
			delete(m, k)
		}
	}
	return m, nil
}

// headerPattern matches header names as Camel does, case-insensitively: an
// exact name, a prefix ending with * or a regular expression. An empty
// pattern matches nothing.
type headerPattern struct {
	text   string
	prefix bool           // text ends with *, which is cut off
	regex  *regexp.Regexp // nil when text is not a valid regular expression
}

func compileHeaderPattern(s string) (headerPattern, error) {
	if s == "" {
		return headerPattern{}, nil
	}
	p := headerPattern{text: s}
	if cut, ok := strings.CutSuffix(s, "*"); ok {
		p.text, p.prefix = cut, true
	}
	re, err := regexp.Compile("(?i)^(?:" + s + ")$")
	if err != nil && !p.prefix {
		return p, err
	}
	p.regex = re
	return p, nil
}

func (p headerPattern) match(name string) bool {
	switch {
	case p.text == "" && !p.prefix:
		return false
	case strings.EqualFold(name, p.text):
		return true
	case p.prefix && len(name) >= len(p.text) && strings.EqualFold(name[:len(p.text)], p.text):
		return true
	}
	return p.regex != nil && p.regex.MatchString(name)
}
