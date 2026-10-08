package impl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
)

// The converters keep the order of JSON object members, which a Go map
// loses. In their JSON values an object is a jsonObject, an array an []any,
// a number a json.Number (its text as written), and strings, booleans and
// null are string, bool and nil.

type jsonObject []jsonMember

type jsonMember struct {
	key   string
	value any
}

// add appends key: v, or, when o has key already, turns that member into an
// array and appends v to it (repeated XML elements).
func (o *jsonObject) add(key string, v any) {
	for i, m := range *o {
		if m.key == key {
			if a, ok := m.value.(jsonArray); ok {
				(*o)[i].value = append(a, v)
			} else {
				(*o)[i].value = jsonArray{m.value, v}
			}
			return
		}
	}
	*o = append(*o, jsonMember{key, v})
}

// jsonArray is an array made of repeated XML elements; it marks members that
// add may extend, unlike arrays that are values in their own right.
type jsonArray []any

// readJSON returns body as an ordered JSON value. A body that is already
// decoded JSON (maps and slices) is converted, with its keys sorted.
func readJSON(body any) (any, error) {
	switch body.(type) {
	case map[string]any, []any:
		return orderedJSON(body), nil
	}
	d := json.NewDecoder(bytes.NewReader(bytesOf(body)))
	d.UseNumber()
	v, err := readJSONValue(d)
	if err == nil {
		if _, err = d.Token(); err != io.EOF {
			err = fmt.Errorf("data after the JSON value")
		} else {
			err = nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("body is not JSON: %w", err)
	}
	return v, nil
}

func readJSONValue(d *json.Decoder) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		o := jsonObject{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			v, err := readJSONValue(d)
			if err != nil {
				return nil, err
			}
			o = append(o, jsonMember{key.(string), v})
		}
		_, err := d.Token() // }
		return o, err
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, err := readJSONValue(d)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := d.Token() // ]
		return a, err
	}
	return tok, nil
}

func orderedJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		o := make(jsonObject, 0, len(x))
		for _, k := range slices.Sorted(maps.Keys(x)) {
			o = append(o, jsonMember{k, orderedJSON(x[k])})
		}
		return o
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = orderedJSON(e)
		}
		return a
	case float64:
		return json.Number(strconv.FormatFloat(x, 'f', -1, 64))
	}
	return v
}

// writeJSON writes v as compact JSON, without escaping <, > and &.
func writeJSON(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case jsonObject:
		b.WriteByte('{')
		for i, m := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, m.key)
			b.WriteByte(':')
			writeJSON(b, m.value)
		}
		b.WriteByte('}')
	case jsonArray:
		writeJSON(b, []any(x))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSON(b, e)
		}
		b.WriteByte(']')
	case string:
		writeJSONString(b, x)
	case json.Number:
		b.WriteString(string(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case nil:
		b.WriteString("null")
	default:
		writeJSONString(b, fmt.Sprint(x))
	}
}

func writeJSONString(b *bytes.Buffer, s string) {
	e := json.NewEncoder(b)
	e.SetEscapeHTML(false)
	e.Encode(s)             // a string always encodes
	b.Truncate(b.Len() - 1) // Encode adds a newline
}

// isJSONNumber reports whether s is a number as JSON writes it, such as 12,
// -1.5 or 2e3 (not 012 or +1).
func isJSONNumber(s string) bool {
	return s != "" && json.Valid([]byte(s)) && (s[0] == '-' || s[0] >= '0' && s[0] <= '9') &&
		s[len(s)-1] >= '0' && s[len(s)-1] <= '9'
}
