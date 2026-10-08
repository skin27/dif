// Package impl converts DIL (Data Integration Language) JSON into the flow model.
package impl

import (
	"bytes"
	"encoding/json"
)

// The structs below cover only the DIL fields the MVP reads; everything else is ignored.

type dilDoc struct {
	DIL struct {
		Integrations struct {
			Integration oneOrMany[dilIntegration] `json:"integration"`
		} `json:"integrations"`
		Core struct {
			Messages struct {
				Message oneOrMany[dilMessage] `json:"message"`
			} `json:"messages"`
			Resources struct {
				Resource oneOrMany[dilResource] `json:"resource"`
			} `json:"resources"`
		} `json:"core"`
	} `json:"dil"`
}

type dilIntegration struct {
	Flows struct {
		Flow oneOrMany[dilFlow] `json:"flow"`
	} `json:"flows"`
}

type dilFlow struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Steps struct {
		Step oneOrMany[dilStep] `json:"step"`
	} `json:"steps"`
}

type dilStep struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	URI     string         `json:"uri"`
	Options map[string]any `json:"options"`
	Links   struct {
		Link oneOrMany[dilLink] `json:"link"`
	} `json:"links"`
}

type dilLink struct {
	ID         string `json:"id"`
	Bound      string `json:"bound"`
	Rule       string `json:"rule"`       // a router's outbound link: its role, such as "wiretap"
	Language   string `json:"language"`   // a router's outbound link: language of expression
	Expression string `json:"expression"` // a router's outbound link: its condition
}

type dilMessage struct {
	Name    string `json:"name"`
	Body    any    `json:"body"`
	Headers struct {
		Header oneOrMany[dilHeader] `json:"header"`
	} `json:"headers"`
}

// dilResource is a named text, such as a JSON Schema or a template, that steps
// refer to as <scheme>:ref:<name>.
type dilResource struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type dilHeader struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Language string `json:"language"`
}

// oneOrMany decodes either a single JSON object or an array of them.
// DIL JSON is converted from XML, where a single child element becomes an object.
type oneOrMany[T any] []T

func (s *oneOrMany[T]) UnmarshalJSON(data []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		return json.Unmarshal(data, (*[]T)(s))
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*s = []T{v}
	return nil
}
