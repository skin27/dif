package flowcheck

import (
	"maps"
	"mime"
	"strings"

	stepdef "dif/steps/definition"
)

// Presence is what is known about a header: it is set, or it may be set.
type Presence uint8

const (
	Maybe Presence = iota + 1 // the header may be set
	Set                       // the header is set
)

// header is a header the type knows, by its name as a step wrote it.
type header struct {
	name     string
	presence Presence
}

// Type is what is known about the message on a link of a flow. Typing is
// gradual: the zero Type knows nothing, and a check reports only what the Type
// knows for certain.
type Type struct {
	// Body is what is known about the body. Media is the media type of the
	// Content-Type header, known only while that header is set to it.
	Body stepdef.BodyType

	// Headers are the headers that are or may be set, by lower-case name, as
	// the simple language does not tell names apart by case.
	Headers map[string]header

	// Closed: a header that is not in Headers is not set. Otherwise nothing is
	// known about it.
	Closed bool

	// familyFrom and mediaFrom are the steps that made the body's family and
	// its Content-Type known, for the messages of the diagnostics.
	familyFrom, mediaFrom string
}

const contentType = "content-type"

// presence returns what is known about the header name: Set, Maybe, or 0 if
// it is not set; ok is false when nothing is known.
func (t Type) presence(name string) (p Presence, ok bool) {
	if h, found := t.Headers[strings.ToLower(name)]; found {
		return h.presence, true
	}
	if t.Closed {
		return 0, true
	}
	return Maybe, false
}

func (t *Type) set(name string, p Presence) {
	if t.Headers == nil {
		t.Headers = map[string]header{}
	}
	t.Headers[strings.ToLower(name)] = header{name, p}
}

// apply returns the type of the message after a step with effect e, which got
// a message of type in; for a router e is the effect on one of its links.
func apply(e *stepdef.Effect, in Type, step string) Type {
	if e == nil {
		return Type{}
	}
	var out Type
	if e.KeepBody {
		out.Body.Family, out.Body.Format, out.familyFrom = in.Body.Family, in.Body.Format, in.familyFrom
		// A step that fails on any other family has, when the message passes it,
		// a body of the family it needs (the JSON and XML parsers are strict).
		if out.Body.Family == "" && !e.Tolerant && (e.Requires.Family == stepdef.FamilyJSON || e.Requires.Family == stepdef.FamilyXML) {
			out.Body.Family, out.familyFrom = e.Requires.Family, step
		}
	} else {
		out.Body.Family, out.Body.Format = e.Body.Family, e.Body.Format
		if out.Body.Family != "" {
			out.familyFrom = step
		}
	}

	switch e.Headers {
	case stepdef.HeadersKeep, stepdef.HeadersAdd, stepdef.HeadersRemove:
		out.Headers = maps.Clone(in.Headers)
		out.Closed = in.Closed && e.Headers != stepdef.HeadersAdd
		out.Body.Media, out.mediaFrom = in.Body.Media, in.mediaFrom
		if e.Headers == stepdef.HeadersRemove {
			for k, h := range out.Headers {
				out.Headers[k] = header{h.name, Maybe}
			}
			out.Body.Media, out.mediaFrom = "", ""
		}
	}
	for _, name := range e.MaySet {
		out.set(name, Maybe)
		if strings.EqualFold(name, contentType) {
			out.Body.Media, out.mediaFrom = "", ""
		}
	}
	for _, name := range e.Sets {
		out.set(name, Set)
		if strings.EqualFold(name, contentType) {
			out.Body.Media, out.mediaFrom = "", ""
		}
	}
	if !e.KeepBody && e.Body.Media != "" {
		out.set("Content-Type", Set)
		out.Body.Media, out.mediaFrom = normalMedia(e.Body.Media), step
	}
	return out
}

// join returns what is known of a message that has type a or type b.
func join(a, b Type) Type {
	var out Type
	if a.Body.Family == b.Body.Family {
		out.Body.Family = a.Body.Family
		if a.familyFrom == b.familyFrom {
			out.familyFrom = a.familyFrom
		}
	}
	if a.Body.Format == b.Body.Format {
		out.Body.Format = a.Body.Format
	}
	if a.Body.Media == b.Body.Media {
		out.Body.Media = a.Body.Media
		if a.mediaFrom == b.mediaFrom {
			out.mediaFrom = a.mediaFrom
		}
	}
	out.Closed = a.Closed && b.Closed
	for _, t := range []Type{a, b} {
		for k, h := range t.Headers {
			p := Maybe
			if pa, _ := a.presence(k); pa == Set {
				if pb, _ := b.presence(k); pb == Set {
					p = Set
				}
			}
			if out.Headers == nil {
				out.Headers = map[string]header{}
			}
			out.Headers[k] = header{h.name, p}
		}
	}
	return out
}

// equal reports whether a and b know the same.
func equal(a, b Type) bool {
	return a.Body == b.Body && a.Closed == b.Closed && a.familyFrom == b.familyFrom && a.mediaFrom == b.mediaFrom &&
		maps.Equal(a.Headers, b.Headers)
}

// normalMedia returns the media type of a Content-Type value, in lower case
// and without parameters; the value itself if it cannot be parsed.
func normalMedia(s string) string {
	if m, _, err := mime.ParseMediaType(s); err == nil {
		return m
	}
	return s
}
