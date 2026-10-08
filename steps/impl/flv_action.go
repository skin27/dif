package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// flvAction converts a flat file of fixed-length values into XML, as the Java
// platform's flv step does. Every non-empty line is matched against the rules:
// the first rule whose matchOn the line starts with cuts it into its fields,
// which follow each other, by their length in characters, from the start of
// the line (matchOn is part of the first field). Values are trimmed, a line
// shorter than its fields leaves the rest empty, characters beyond them are
// ignored, and a line no rule matches is left out.
//
// The result is <flv-message>: first a <rule matchOn="HDR" fields="header[3]body[5]" />
// for every rule, then a <segment> for every line, holding an element per field:
//
//	HDRthing  ->  <flv-message><rule matchOn="HDR" fields="header[3]body[5]" />
//	              <segment><header>HDR</header><body>thing</body></segment></flv-message>
//
// A line of a group rule starts a <group> (the one before is closed), which
// holds its segment and those of the lines of other rules that follow, up to
// the next line of a group rule. A segment with no group open is on its own.
//
// The rules are given in two ways, which may be combined. The option rules is
// a JSON list in the order it is written, as the DIL has it:
//
//	[{"matchOn": "HDR", "group": true, "subcollection": [{"field": "header", "length": 3}, {"field": "body", "length": 5}]}]
//
// Or every other option is a rule: its name is matchOn, or _group_ and matchOn
// for a group rule, and its value the fields as header[3]body[5]. A flow cannot
// tell the order of its options, so these are tried, and listed, with the
// longest matchOn first (the most specific), then in reverse alphabetical order.
type flvAction struct{ rules []flvRule }

type flvRule struct {
	MatchOn       string     `json:"matchOn"`
	Group         looseBool  `json:"group"`
	Subcollection []flvField `json:"subcollection"`

	fields string // the fields as written in the rule element, header[3]body[5]
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

const flvGroupPrefix = "_group_"

var flvFieldSpec = regexp.MustCompile(`^(?:[^\[\]]+\[[0-9]+\])+$`)
var flvFieldPart = regexp.MustCompile(`([^\[\]]+)\[([0-9]+)\]`)

func newFlvAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	var rules []flvRule
	if list, ok := p["rules"].(string); ok {
		if err := json.Unmarshal([]byte(list), &rules); err != nil {
			return nil, fmt.Errorf("option rules: want a JSON list of rules: %w", err)
		}
		for i := range rules {
			r := &rules[i]
			if len(r.Subcollection) == 0 {
				return nil, fmt.Errorf("option rules: rule %d has no subcollection of fields", i+1)
			}
			var spec strings.Builder
			for j, f := range r.Subcollection {
				if f.Field == "" || f.Length < 1 {
					return nil, fmt.Errorf("option rules: rule %d, field %d: want a field name and a length of 1 or more", i+1, j+1)
				}
				spec.WriteString(f.Field + "[" + strconv.Itoa(int(f.Length)) + "]")
			}
			r.fields = spec.String()
		}
	}

	var options []flvRule
	for name, v := range p {
		if name == "rules" || strings.HasPrefix(name, internalPrefix) {
			continue // not a rule: the rules, or what the loader binds to every step
		}
		spec, ok := v.(string)
		if !ok || !flvFieldSpec.MatchString(spec) {
			return nil, fmt.Errorf("option %s: want the fields of a rule, such as header[3]body[5], got %v", name, v)
		}
		r := flvRule{fields: spec}
		r.MatchOn, r.Group = name, false
		if m, ok := strings.CutPrefix(name, flvGroupPrefix); ok {
			r.MatchOn, r.Group = m, true
		}
		for _, f := range flvFieldPart.FindAllStringSubmatch(spec, -1) {
			n, _ := strconv.Atoi(f[2])
			if n < 1 {
				return nil, fmt.Errorf("option %s: field %s has a length of 0", name, f[1])
			}
			r.Subcollection = append(r.Subcollection, flvField{f[1], looseInt(n)})
		}
		options = append(options, r)
	}
	slices.SortFunc(options, func(a, b flvRule) int {
		if len(a.MatchOn) != len(b.MatchOn) {
			return len(b.MatchOn) - len(a.MatchOn)
		}
		return strings.Compare(b.MatchOn, a.MatchOn)
	})
	rules = append(rules, options...)

	if len(rules) == 0 {
		return nil, fmt.Errorf("no rules: set the option rules, or an option per rule, such as HDR: header[3]body[5]")
	}
	return flvAction{rules}, nil
}

func (a flvAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	var b strings.Builder
	b.WriteString("<flv-message>")
	for _, r := range a.rules {
		b.WriteString(`<rule matchOn="` + xmlAttrEscaper.Replace(r.MatchOn) + `" fields="` + xmlAttrEscaper.Replace(r.fields) + `" />`)
	}

	grouped := false // whether a group is open
	for _, line := range strings.Split(text(m[message.Body]), "\n") {
		if line = strings.TrimSuffix(line, "\r"); strings.TrimSpace(line) == "" {
			continue
		}
		var rule *flvRule
		for i := range a.rules {
			if strings.HasPrefix(line, a.rules[i].MatchOn) {
				rule = &a.rules[i]
				break
			}
		}
		if rule == nil {
			continue
		}

		if rule.Group {
			if grouped {
				b.WriteString("</group>")
			}
			b.WriteString("<group>")
			grouped = true
		}
		b.WriteString("<segment>")
		rest := []rune(line)
		for _, f := range rule.Subcollection {
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
		b.WriteString("</segment>")
	}
	if grouped {
		b.WriteString("</group>")
	}
	b.WriteString("</flv-message>")
	m[message.Body] = b.String()
	m[message.ContentType] = "application/xml"
	return m, nil
}
