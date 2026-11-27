package dilsyntax

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// A value is what DIL JSON holds: string, json.Number, bool, nil, []any or
// object. Objects keep the order of their fields, so that a document printed
// as text reads in the order it was written.
type object []member

type member struct {
	key string
	val any
	pos Pos // where the field is written in DIL text; zero for JSON
}

// get returns the value of the field key.
func (o object) get(key string) (any, bool) {
	for _, m := range o {
		if m.key == key {
			return m.val, true
		}
	}
	return nil, false
}

// str returns the field key if it is a string.
func (o object) str(key string) (string, bool) {
	v, _ := o.get(key)
	s, ok := v.(string)
	return s, ok
}

// obj returns the field key if it is an object.
func (o object) obj(key string) object {
	v, _ := o.get(key)
	m, _ := v.(object)
	return m
}

// many returns v as a list: DIL JSON, converted from XML, writes a single
// element as an object and several as an array.
func many(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	}
	return []any{v}
}

// decodeJSON decodes JSON into a value, keeping the order of object fields.
// As encoding/json does, a field written twice keeps its last value.
func decodeJSON(data []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	d.UseNumber()
	v, err := decodeValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("invalid JSON: data after the document")
	}
	return v, nil
}

const maxDepth = 1000

func decodeValue(d *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("invalid JSON: nested too deep")
	}
	t, err := d.Token()
	if err != nil {
		return nil, errors.New("invalid JSON")
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			o := object{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return nil, errors.New("invalid JSON")
				}
				key, _ := k.(string)
				v, err := decodeValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				replaced := false
				for i := range o {
					if o[i].key == key {
						o[i].val, replaced = v, true
					}
				}
				if !replaced {
					o = append(o, member{key: key, val: v})
				}
			}
			_, err := d.Token()
			return o, err
		case '[':
			list := []any{}
			for d.More() {
				v, err := decodeValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := d.Token()
			return list, err
		}
		return nil, errors.New("invalid JSON")
	}
	return t, nil
}

// appendJSON appends v as compact JSON.
func appendJSON(b []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(b, "null"...)
	case bool:
		if x {
			return append(b, "true"...)
		}
		return append(b, "false"...)
	case json.Number:
		return append(b, x...)
	case string:
		return appendJSONString(b, x)
	case []any:
		b = append(b, '[')
		for i, e := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSON(b, e)
		}
		return append(b, ']')
	case object:
		b = append(b, '{')
		for i, m := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSONString(b, m.key)
			b = append(b, ':')
			b = appendJSON(b, m.val)
		}
		return append(b, '}')
	}
	panic("dilsyntax: not a value")
}

// appendJSONString appends s as a JSON string. Unlike encoding/json it keeps
// <, > and & as they are, so expressions stay readable.
func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\':
				b = append(b, '\\', c)
			case c == '\n':
				b = append(b, `\n`...)
			case c == '\r':
				b = append(b, `\r`...)
			case c == '\t':
				b = append(b, `\t`...)
			case c < ' ':
				b = append(b, `\u00`...)
				b = append(b, "0123456789abcdef"[c>>4], "0123456789abcdef"[c&15])
			default:
				b = append(b, c)
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			b = append(b, `�`...)
		} else {
			b = append(b, s[i:i+n]...)
		}
		i += n
	}
	return append(b, '"')
}
