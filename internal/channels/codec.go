package channels

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"

	"dif/message"
)

// A tagged encoding avoids JSON's conversion of integers to float64 and bytes
// to strings. Only the documented message value types cross a durable boundary.
type value struct {
	Type string           `json:"t"`
	Text string           `json:"v,omitempty"`
	Map  map[string]value `json:"m,omitempty"`
	List []value          `json:"l,omitempty"`
	Nil  bool             `json:"n,omitempty"`
}

func encodeValue(v any, depth int) (value, error) {
	if depth > 100 {
		return value{}, fmt.Errorf("message nesting exceeds 100 levels")
	}
	w := value{}
	switch x := v.(type) {
	case nil:
		w.Type = "nil"
	case string:
		w.Type, w.Text = "string", x
	case bool:
		w.Type, w.Text = "bool", strconv.FormatBool(x)
	case []byte:
		w.Type, w.Text, w.Nil = "bytes", base64.StdEncoding.EncodeToString(x), x == nil
	case map[string]any:
		w.Type, w.Nil, w.Map = "map", x == nil, map[string]value{}
		for k, item := range x {
			child, err := encodeValue(item, depth+1)
			if err != nil {
				return w, fmt.Errorf("header %q: %w", k, err)
			}
			w.Map[k] = child
		}
	case message.Message:
		var err error
		w, err = encodeValue(map[string]any(x), depth+1)
		w.Type = "message"
		return w, err
	case []any:
		w.Type, w.Nil = "list", x == nil
		for _, item := range x {
			child, err := encodeValue(item, depth+1)
			if err != nil {
				return w, err
			}
			w.List = append(w.List, child)
		}
	case []string:
		w.Type, w.Nil = "strings", x == nil
		for _, item := range x {
			w.List = append(w.List, value{Type: "string", Text: item})
		}
	case json.Number:
		if !json.Valid([]byte(x)) || len(x) == 0 || x[0] != '-' && (x[0] < '0' || x[0] > '9') {
			return w, fmt.Errorf("invalid JSON number %q", x)
		}
		w.Type, w.Text = "number", string(x)
	default:
		r := reflect.ValueOf(v)
		// Reject user-defined types rather than restoring them with a different type.
		if r.Type().PkgPath() != "" {
			return w, fmt.Errorf("unsupported durable value %T", v)
		}
		w.Type = r.Kind().String()
		switch r.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			w.Text = strconv.FormatInt(r.Int(), 10)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			w.Text = strconv.FormatUint(r.Uint(), 10)
		case reflect.Float32, reflect.Float64:
			if math.IsNaN(r.Float()) || math.IsInf(r.Float(), 0) {
				return w, fmt.Errorf("non-finite durable number")
			}
			w.Text = strconv.FormatFloat(r.Float(), 'g', -1, r.Type().Bits())
		default:
			return w, fmt.Errorf("unsupported durable value %T", v)
		}
	}
	return w, nil
}

func decodeValue(w value) (any, error) {
	switch w.Type {
	case "nil":
		return nil, nil
	case "string":
		return w.Text, nil
	case "bool":
		return strconv.ParseBool(w.Text)
	case "bytes":
		if w.Nil {
			return []byte(nil), nil
		}
		return base64.StdEncoding.DecodeString(w.Text)
	case "number":
		return json.Number(w.Text), nil
	case "map", "message":
		var m map[string]any
		if !w.Nil {
			m = map[string]any{}
			for k, child := range w.Map {
				v, err := decodeValue(child)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
		}
		if w.Type == "message" {
			return message.Message(m), nil
		}
		return m, nil
	case "list":
		var a []any
		if !w.Nil {
			a = make([]any, len(w.List))
			for i, child := range w.List {
				v, err := decodeValue(child)
				if err != nil {
					return nil, err
				}
				a[i] = v
			}
		}
		return a, nil
	case "strings":
		var a []string
		if !w.Nil {
			a = make([]string, len(w.List))
			for i, child := range w.List {
				a[i] = child.Text
			}
		}
		return a, nil
	}
	types := map[string]reflect.Type{"int": reflect.TypeOf(int(0)), "int8": reflect.TypeOf(int8(0)), "int16": reflect.TypeOf(int16(0)), "int32": reflect.TypeOf(int32(0)), "int64": reflect.TypeOf(int64(0)), "uint": reflect.TypeOf(uint(0)), "uint8": reflect.TypeOf(uint8(0)), "uint16": reflect.TypeOf(uint16(0)), "uint32": reflect.TypeOf(uint32(0)), "uint64": reflect.TypeOf(uint64(0)), "float32": reflect.TypeOf(float32(0)), "float64": reflect.TypeOf(float64(0))}
	t := types[w.Type]
	if t == nil {
		return nil, fmt.Errorf("unknown durable value type %q", w.Type)
	}
	r := reflect.New(t).Elem()
	switch r.Kind() {
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(w.Text, t.Bits())
		if err != nil {
			return nil, err
		}
		r.SetFloat(v)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(w.Text, 10, t.Bits())
		if err != nil {
			return nil, err
		}
		r.SetInt(v)
	default:
		v, err := strconv.ParseUint(w.Text, 10, t.Bits())
		if err != nil {
			return nil, err
		}
		r.SetUint(v)
	}
	return r.Interface(), nil
}

func encodeMessage(m message.Message) ([]byte, error) {
	w, err := encodeValue(m, 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

func decodeMessage(data []byte) (message.Message, error) {
	var w value
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, err
	}
	v, err := decodeValue(w)
	if err != nil {
		return nil, err
	}
	m, ok := v.(message.Message)
	if !ok {
		return nil, fmt.Errorf("invalid durable message")
	}
	return m, nil
}
