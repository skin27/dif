package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// flvAction converts a flat file of fixed-length values into XML. Every
// non-empty line is a record; the first rule whose matchOn the line starts
// with (every line, if matchOn is empty) cuts it into its fields, which are
// subcollection's, in order and by their length in characters. Values are
// trimmed. A line shorter than its fields leaves the rest empty, characters
// beyond them are ignored, and a line no rule matches fails the message.
//
// A record is an element named after its rule's name, else its matchOn, else
// record. With group, consecutive records of the rule are collected in one
// group element:
//
//	rules: [{"matchOn": "HDR", "group": true, "subcollection": [{"field": "header", "length": 3}, {"field": "body", "length": 5}]}]
//	HDRthing  ->  <flv><group><HDR><header>HDR</header><body>thing</body></HDR></group></flv>
type flvAction struct{ rules []flvRule }

type flvRule struct {
	Name          string     `json:"name"`
	MatchOn       string     `json:"matchOn"`
	Group         looseBool  `json:"group"`
	Subcollection []flvField `json:"subcollection"`

	element string // the record element, from Name and MatchOn
}

type flvField struct {
	Field  string   `json:"field"`
	Length looseInt `json:"length"`
}

// looseInt and looseBool are numbers and booleans that DIL, converted from
// XML, may hold as strings: 5 or "5", true or "true".
type (
	looseInt  int
	looseBool bool
)

func (n *looseInt) UnmarshalJSON(data []byte) error {
	v, err := strconv.Atoi(strings.Trim(string(data), `"`))
	*n = looseInt(v)
	return err
}

func (b *looseBool) UnmarshalJSON(data []byte) error {
	v, err := strconv.ParseBool(strings.Trim(string(data), `"`))
	*b = looseBool(v)
	return err
}

func newFlvAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	var rules []flvRule
	if err := json.Unmarshal([]byte(p["rules"].(string)), &rules); err != nil {
		return nil, fmt.Errorf("option rules: want a JSON list of rules: %w", err)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("option rules: no rules")
	}
	for i := range rules {
		r := &rules[i]
		if len(r.Subcollection) == 0 {
			return nil, fmt.Errorf("option rules: rule %d has no subcollection of fields", i+1)
		}
		for j, f := range r.Subcollection {
			if f.Field == "" || f.Length < 1 {
				return nil, fmt.Errorf("option rules: rule %d, field %d: want a field name and a length of 1 or more", i+1, j+1)
			}
		}
		switch {
		case r.Name != "":
			r.element = sanitizeXMLName(r.Name)
		case isXMLName(r.MatchOn):
			r.element = r.MatchOn
		default:
			r.element = "record"
		}
	}
	return flvAction{rules}, nil
}

func (a flvAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	var b strings.Builder
	b.WriteString("<flv>")
	grouped := -1 // the rule whose group is open
	closeGroup := func() {
		if grouped >= 0 {
			b.WriteString("</group>")
			grouped = -1
		}
	}

	for n, line := range strings.Split(text(m[message.Body]), "\n") {
		if line = strings.TrimSuffix(line, "\r"); strings.TrimSpace(line) == "" {
			continue
		}
		which := -1
		for i, r := range a.rules {
			if strings.HasPrefix(line, r.MatchOn) {
				which = i
				break
			}
		}
		if which < 0 {
			return nil, fmt.Errorf("line %d: no rule matches", n+1)
		}
		r := a.rules[which]

		if grouped != which {
			closeGroup()
		}
		if r.Group && grouped != which {
			b.WriteString("<group>")
			grouped = which
		}
		b.WriteString("<" + r.element + ">")
		rest := []rune(line)
		for _, f := range r.Subcollection {
			take := min(int(f.Length), len(rest))
			value := strings.TrimSpace(string(rest[:take]))
			rest = rest[take:]
			tag := sanitizeXMLName(f.Field)
			if value == "" {
				b.WriteString("<" + tag + "/>")
			} else {
				b.WriteString("<" + tag + ">" + xmlTextEscaper.Replace(value) + "</" + tag + ">")
			}
		}
		b.WriteString("</" + r.element + ">")
	}
	closeGroup()
	b.WriteString("</flv>")
	m[message.Body] = b.String()
	m[message.ContentType] = "application/xml"
	return m, nil
}
