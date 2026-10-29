package mapping

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"strings"
	"unicode/utf8"
)

const ServiceBindingKey = "dif.service.plan"

// HeaderSpec declares a typed message header contract on a service.
type HeaderSpec struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Required *bool           `json:"required,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
}

func (h *HeaderSpec) UnmarshalJSON(data []byte) error {
	type plain HeaderSpec
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode((*plain)(h)); err != nil {
		return fmt.Errorf("invalid header declaration")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid header declaration")
	}
	return nil
}

// ServiceDefinition is the neutral declaration of a single-capability service contract.
type ServiceDefinition struct {
	Name            string       `json:"name"`
	ExchangePattern string       `json:"exchangePattern,omitempty"`
	Input           string       `json:"input,omitempty"`
	Output          string       `json:"output,omitempty"`
	Error           string       `json:"error,omitempty"`
	InputHeaders    []HeaderSpec `json:"inputHeaders,omitempty"`
	OutputHeaders   []HeaderSpec `json:"outputHeaders,omitempty"`
}

// ServiceFormats holds resolved formats referenced by a ServiceDefinition.
type ServiceFormats struct {
	Input  *Format
	Output *Format
	Error  *Format
}

// HeaderInfo is the redaction-safe inspection summary of a header contract.
type HeaderInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Required   bool   `json:"required"`
	HasDefault bool   `json:"hasDefault,omitempty"`
}

// ServiceInfo is the redaction-safe inspection summary of a service contract.
type ServiceInfo struct {
	Name            string       `json:"name"`
	Origin          string       `json:"origin,omitempty"`
	ExchangePattern string       `json:"exchangePattern"`
	Input           string       `json:"input,omitempty"`
	Output          string       `json:"output,omitempty"`
	Error           string       `json:"error,omitempty"`
	InputOrigin     string       `json:"inputOrigin,omitempty"`
	OutputOrigin    string       `json:"outputOrigin,omitempty"`
	ErrorOrigin     string       `json:"errorOrigin,omitempty"`
	InputHeaders    []HeaderInfo `json:"inputHeaders,omitempty"`
	OutputHeaders   []HeaderInfo `json:"outputHeaders,omitempty"`
}

// ServiceBinding binds a compiled service contract and its canonical identity to a step.
type ServiceBinding struct {
	ID   string
	Name string
	Plan *ServicePlan
}

type compiledHeader struct {
	name       string
	typ        string
	required   bool
	hasDefault bool
	defaultRaw string
}

func (h compiledHeader) cloneDefault(b *budget) (any, error) {
	if !h.hasDefault {
		return nil, nil
	}
	if err := b.work(len(h.defaultRaw)); err != nil {
		return nil, err
	}
	d := json.NewDecoder(strings.NewReader(h.defaultRaw))
	d.UseNumber()
	var out any
	if err := d.Decode(&out); err != nil {
		return nil, fmt.Errorf("header %q: invalid default", h.name)
	}
	return out, nil
}

// ServicePlan is an immutable compiled service contract.
type ServicePlan struct {
	info      ServiceInfo
	in, out   *contract
	errFormat *contract
	inHdrs    []compiledHeader
	outHdrs   []compiledHeader
	limits    Limits
}

func (p *ServicePlan) Info() ServiceInfo       { return p.info }
func (p *ServicePlan) ExchangePattern() string { return p.info.ExchangePattern }
func (p *ServicePlan) HasErrorFormat() bool    { return p.errFormat != nil }

func validHeaderType(t string) bool {
	if t == "any" {
		return true
	}
	switch strings.TrimSuffix(t, "?") {
	case "string", "number", "boolean", "array", "object":
		return true
	}
	return false
}

func normalizeExchangePattern(s string) (string, error) {
	switch {
	case s == "" || strings.EqualFold(s, "InOut"):
		return "InOut", nil
	case strings.EqualFold(s, "InOnly"):
		return "InOnly", nil
	default:
		return "", fmt.Errorf("exchangePattern must be InOut or InOnly")
	}
}

func compileOptionalFormat(role, ref string, f *Format, l Limits) (*contract, string, error) {
	if ref == "" {
		if f != nil {
			return nil, "", fmt.Errorf("unexpected %s format without reference", role)
		}
		return nil, "", nil
	}
	if f == nil {
		return nil, "", fmt.Errorf("%s format %q not found", role, ref)
	}
	c, err := compileFormat(*f, l)
	if err != nil {
		return nil, "", fmt.Errorf("%s format %q: %w", role, ref, err)
	}
	if c.media == "text/csv" && !c.reader.header && len(c.reader.columns) == 0 {
		return nil, "", fmt.Errorf("%s format %q: CSV reader without header requires columns", role, ref)
	}
	return &c, f.Origin, nil
}

func compileHeaders(specs []HeaderSpec, l Limits) ([]compiledHeader, []HeaderInfo, error) {
	if len(specs) == 0 {
		return nil, nil, nil
	}
	compiled := make([]compiledHeader, 0, len(specs))
	infos := make([]HeaderInfo, 0, len(specs))
	seen := map[string]bool{}
	b := &budget{ctx: context.Background(), limits: l}
	for _, s := range specs {
		if s.Name == "" || !utf8.ValidString(s.Name) || strings.TrimSpace(s.Name) != s.Name {
			return nil, nil, fmt.Errorf("invalid header name")
		}
		if s.Name == "body" || strings.HasPrefix(s.Name, "metadata.") {
			return nil, nil, fmt.Errorf("header %q is reserved", s.Name)
		}
		if seen[s.Name] {
			return nil, nil, fmt.Errorf("duplicate header %q", s.Name)
		}
		seen[s.Name] = true
		if !validHeaderType(s.Type) {
			return nil, nil, fmt.Errorf("header %q: invalid type", s.Name)
		}
		ch := compiledHeader{name: s.Name, typ: s.Type}
		if len(s.Default) > 0 {
			if s.Required != nil && *s.Required {
				return nil, nil, fmt.Errorf("header %q cannot be both required and defaulted", s.Name)
			}
			val, err := readJSON(s.Default, b)
			if err != nil || !matches(s.Type, val) {
				return nil, nil, fmt.Errorf("header %q: invalid default", s.Name)
			}
			w := output{b: b}
			if err := w.json(val, 0); err != nil {
				return nil, nil, fmt.Errorf("header %q: invalid default", s.Name)
			}
			ch.hasDefault = true
			ch.defaultRaw = w.s.String()
			ch.required = false
		} else if s.Required == nil {
			ch.required = true
		} else {
			ch.required = *s.Required
		}
		compiled = append(compiled, ch)
		infos = append(infos, HeaderInfo{
			Name:       ch.name,
			Type:       ch.typ,
			Required:   ch.required,
			HasDefault: ch.hasDefault,
		})
	}
	return compiled, infos, nil
}

// CompileService compiles a ServiceDefinition into an immutable ServicePlan.
func CompileService(ctx context.Context, d ServiceDefinition, formats ServiceFormats, origin string, l Limits) (*ServicePlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := l.Normalize()
	if err != nil {
		return nil, err
	}
	if d.Name == "" || !utf8.ValidString(d.Name) || strings.TrimSpace(d.Name) != d.Name || strings.ContainsAny(d.Name, ":\x00") {
		return nil, fmt.Errorf("invalid service name")
	}
	mep, err := normalizeExchangePattern(d.ExchangePattern)
	if err != nil {
		return nil, err
	}
	if mep == "InOnly" {
		if d.Output != "" || formats.Output != nil {
			return nil, fmt.Errorf("InOnly service must not declare output")
		}
		if d.Error != "" || formats.Error != nil {
			return nil, fmt.Errorf("InOnly service must not declare error")
		}
		if len(d.OutputHeaders) > 0 {
			return nil, fmt.Errorf("InOnly service must not declare outputHeaders")
		}
	}
	inC, inOrigin, err := compileOptionalFormat("input", d.Input, formats.Input, l)
	if err != nil {
		return nil, err
	}
	outC, outOrigin, err := compileOptionalFormat("output", d.Output, formats.Output, l)
	if err != nil {
		return nil, err
	}
	errC, errOrigin, err := compileOptionalFormat("error", d.Error, formats.Error, l)
	if err != nil {
		return nil, err
	}
	inHdrs, inInfo, err := compileHeaders(d.InputHeaders, l)
	if err != nil {
		return nil, err
	}
	outHdrs, outInfo, err := compileHeaders(d.OutputHeaders, l)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &ServicePlan{
		info: ServiceInfo{
			Name:            d.Name,
			Origin:          origin,
			ExchangePattern: mep,
			Input:           d.Input,
			Output:          d.Output,
			Error:           d.Error,
			InputOrigin:     inOrigin,
			OutputOrigin:    outOrigin,
			ErrorOrigin:     errOrigin,
			InputHeaders:    inInfo,
			OutputHeaders:   outInfo,
		},
		in:        inC,
		out:       outC,
		errFormat: errC,
		inHdrs:    inHdrs,
		outHdrs:   outHdrs,
		limits:    l,
	}, nil
}

// ValidateInput checks msg against the service's input payload and inputHeaders contract.
// When wireHeaders is true, string header values for number/boolean declarations are parsed.
func (p *ServicePlan) ValidateInput(ctx context.Context, msg map[string]any, wireHeaders bool) error {
	return p.validateEnvelope(ctx, msg, p.in, p.in == nil, p.inHdrs, wireHeaders)
}

// ValidateOutput checks msg against the service's output payload and outputHeaders contract.
func (p *ServicePlan) ValidateOutput(ctx context.Context, msg map[string]any) error {
	if p.info.ExchangePattern != "InOut" {
		return nil
	}
	return p.validateEnvelope(ctx, msg, p.out, p.out == nil, p.outHdrs, false)
}

// ValidateError checks msg against the service's error payload contract when one is declared.
func (p *ServicePlan) ValidateError(ctx context.Context, msg map[string]any) error {
	if p.errFormat == nil {
		return ctx.Err()
	}
	return p.validateEnvelope(ctx, msg, p.errFormat, false, nil, false)
}

func isEmptyBody(body any) bool {
	switch x := body.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []byte:
		return len(x) == 0
	default:
		return false
	}
}

func (p *ServicePlan) validateEnvelope(ctx context.Context, msg map[string]any, bodyContract *contract, requireEmptyBody bool, headers []compiledHeader, wireHeaders bool) error {
	if msg == nil {
		return fmt.Errorf("message is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b := &budget{ctx: ctx, limits: p.limits}
	set := map[string]any{}
	del := map[string]bool{}

	if bodyContract == nil {
		if requireEmptyBody && !isEmptyBody(msg["body"]) {
			return fmt.Errorf("unexpected body: contract declares no payload")
		}
	} else {
		missingCT, err := bodyContract.checkBody(msg, b)
		if err != nil {
			return err
		}
		if missingCT {
			set["Content-Type"] = bodyContract.media
		}
	}

	if len(headers) > 0 {
		exact := make(map[string]bool, len(headers))
		for _, h := range headers {
			exact[h.name] = true
		}
		for _, h := range headers {
			v, ok := set[h.name]
			if !ok {
				v, ok = msg[h.name]
			}
			if !ok && wireHeaders {
				canon := textproto.CanonicalMIMEHeaderKey(h.name)
				if canon != h.name && canon != "Body" && !strings.HasPrefix(canon, "metadata.") && !exact[canon] {
					if cv, hasCanon := msg[canon]; hasCanon {
						v, ok = cv, true
						set[h.name] = cv
						del[canon] = true
					}
				}
			}
			if !ok {
				if h.hasDefault {
					dv, err := h.cloneDefault(b)
					if err != nil {
						return err
					}
					set[h.name] = dv
					continue
				}
				if h.required {
					return fmt.Errorf("missing required header %q", h.name)
				}
				continue
			}
			if wireHeaders {
				if s, isStr := v.(string); isStr {
					switch strings.TrimSuffix(h.typ, "?") {
					case "number":
						r, err := number(strings.TrimSpace(s), p.limits)
						if err != nil {
							return fmt.Errorf("header %q requires %s", h.name, h.typ)
						}
						dec, err := decimal(r, p.limits)
						if err != nil {
							return fmt.Errorf("header %q requires %s", h.name, h.typ)
						}
						v = json.Number(dec)
						set[h.name] = v
					case "boolean":
						switch strings.TrimSpace(s) {
						case "true":
							v = true
							set[h.name] = true
						case "false":
							v = false
							set[h.name] = false
						default:
							return fmt.Errorf("header %q requires %s", h.name, h.typ)
						}
					}
				}
			}
			norm, err := normalize(v, b, 0)
			if err != nil || !matches(h.typ, norm) {
				return fmt.Errorf("header %q requires %s", h.name, h.typ)
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	for k := range del {
		delete(msg, k)
	}
	for k, v := range set {
		msg[k] = v
	}
	return nil
}
