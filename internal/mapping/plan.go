package mapping

import (
	"context"
	"fmt"
	"strings"

	"dif/internal/xsltutil"
	"github.com/knroy/go-xml/xdm"
	"github.com/knroy/go-xml/xslt"
)

type Definition struct {
	Name       string `json:"name"`
	Input      string `json:"input"`
	Output     string `json:"output"`
	Language   string `json:"language"`
	Expression string `json:"expression,omitempty"`
	SourceRef  string `json:"sourceRef,omitempty"`
}
type Info struct {
	Name         string `json:"name"`
	Origin       string `json:"origin,omitempty"`
	Language     string `json:"language"`
	Input        string `json:"input"`
	Output       string `json:"output"`
	InputOrigin  string `json:"inputOrigin,omitempty"`
	OutputOrigin string `json:"outputOrigin,omitempty"`
}
type Plan struct {
	info    Info
	in, out contract
	code    *expr
	sheet   *xslt.Stylesheet
	limits  Limits
}

func (p *Plan) Info() Info { return p.info }

// ValidateFormat validates even unused named formats without constructing steps.
func ValidateFormat(f Format, l Limits) error {
	l, e := l.Normalize()
	if e != nil {
		return e
	}
	_, e = compileFormat(f, l)
	return e
}
func Compile(ctx context.Context, d Definition, in, out Format, origin string, l Limits) (*Plan, error) {
	return CompileWithFunctions(ctx, d, in, out, origin, l, nil)
}
func CompileWithFunctions(ctx context.Context, d Definition, in, out Format, origin string, l Limits, resolve Resolver) (*Plan, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	l, e := l.Normalize()
	if e != nil {
		return nil, e
	}
	p := &Plan{info: Info{d.Name, origin, d.Language, d.Input, d.Output, in.Origin, out.Origin}, limits: l}
	p.in, e = compileFormat(in, l)
	if e != nil {
		return nil, fmt.Errorf("input format %q: %w", d.Input, e)
	}
	p.out, e = compileFormat(out, l)
	if e != nil {
		return nil, fmt.Errorf("output format %q: %w", d.Output, e)
	}
	if p.in.media == "text/csv" && !p.in.reader.header && len(p.in.reader.columns) == 0 {
		return nil, fmt.Errorf("CSV reader without header requires columns")
	}
	if p.out.media == "text/csv" && len(p.out.writer.columns) == 0 {
		return nil, fmt.Errorf("CSV writer requires columns")
	}
	if len(d.Expression) > l.SourceBytes {
		return nil, fmt.Errorf("mapping source size limit exceeded")
	}
	switch d.Language {
	case "identity":
		if d.Expression != "" || d.SourceRef != "" {
			return nil, fmt.Errorf("identity must not have source")
		}
	case "difmap/1":
		if d.Expression == "" {
			return nil, fmt.Errorf("mapping expression is required")
		}
		p.code, e = compileScoped(d.Expression, l, map[string]bool{"payload": true, "headers": true}, resolve)
	case "xslt":
		if p.in.media != "application/xml" || p.out.media != "application/xml" || d.SourceRef == "" || d.Expression == "" {
			return nil, fmt.Errorf("XSLT requires XML contracts and sourceRef")
		}
		if e := checkStylesheet(d.Expression, &budget{ctx: ctx, limits: l}); e != nil {
			return nil, e
		}
		p.sheet, e = xsltutil.Compile(d.Expression)
		if e != nil {
			return nil, fmt.Errorf("invalid XSLT stylesheet")
		}
		o := p.sheet.Output()
		if o.Method != "" && !strings.EqualFold(o.Method, "xml") || o.Encoding != "" && !strings.EqualFold(o.Encoding, "UTF-8") || o.DocTypePublic != "" || o.DocTypeSystem != "" {
			return nil, fmt.Errorf("XSLT output must be UTF-8 XML without a doctype")
		}
	default:
		return nil, fmt.Errorf("unsupported mapping language")
	}
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	return p, nil
}

// Apply returns a complete serialized result, never mutating body or headers.
func (p *Plan) Apply(ctx context.Context, body any, contentType string, headers map[string]any) (result string, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("map %q (%s): %w", p.info.Name, p.info.Origin, err)
		}
	}()
	b := &budget{ctx: ctx, limits: p.limits}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if contentType != "" {
		m, e := media(contentType)
		if e != nil || m != p.in.media {
			return "", fmt.Errorf("input Content-Type conflicts with format %q", p.in.name)
		}
	}
	input, e := p.in.decode(body, b)
	if e != nil {
		return "", fmt.Errorf("input format %q: %w", p.in.name, e)
	}
	if e = p.in.schema.check(input, b); e != nil {
		return "", fmt.Errorf("input format %q: %w", p.in.name, e)
	}
	value := input
	if p.code != nil {
		value, e = p.code.eval(b, &environment{values: map[string]any{"payload": input, "headers": headerValues(headers)}})
	} else if p.sheet != nil {
		// Retain the stylesheet's serialization, including QName-valued text
		// and namespace prefixes. Validate the complete result before publishing.
		result, e = p.transform(body, headers, b)
		if e != nil {
			return "", e
		}
		if _, e = readXML([]byte(result), b); e != nil {
			return "", fmt.Errorf("XSLT output format: %w", e)
		}
		return result, ctx.Err()
	}
	if e != nil {
		return "", e
	}
	if e = p.out.schema.check(value, b); e != nil {
		return "", fmt.Errorf("output format %q: %w", p.out.name, e)
	}
	result, e = p.out.encode(value, b)
	if e != nil {
		return "", fmt.Errorf("output format %q: %w", p.out.name, e)
	}
	if e = ctx.Err(); e != nil {
		return "", e
	}
	return result, nil
}
func (p *Plan) ContentType() string { return p.out.media }

// InputMedia is the media type of the input format, which Apply checks the
// Content-Type against.
func (p *Plan) InputMedia() string { return p.in.media }
func (p *Plan) transform(input any, headers map[string]any, b *budget) (string, error) {
	var source string
	switch v := input.(type) {
	case string:
		source = v
	case []byte:
		source = string(v)
	}
	tree, e := xdm.ParseString(source, xdm.ParseOptions{})
	if e != nil {
		return "", fmt.Errorf("invalid XSLT input")
	}
	params := map[string]xdm.Sequence{}
	for k, v := range headers {
		if k == "body" || strings.HasPrefix(k, "metadata.") || !ncname(k) {
			continue
		}
		var item xdm.Item
		switch x := v.(type) {
		case string:
			item = xdm.NewString(x)
		case bool:
			item = xdm.NewBoolean(x)
		case int:
			item = xdm.NewInteger(int64(x))
		case int64:
			item = xdm.NewInteger(x)
		case float64:
			item = xdm.NewDouble(x)
		default:
			continue
		}
		params[k] = xdm.Sequence{item}
	}
	res, e := p.sheet.Transform(b.ctx, tree.Root, xslt.TransformOptions{Params: params})
	if e != nil {
		if b.ctx.Err() != nil {
			return "", b.ctx.Err()
		}
		return "", fmt.Errorf("XSLT transformation failed")
	}
	out := &output{b: b}
	if e = res.Serialize(out); e != nil {
		return "", fmt.Errorf("XSLT serialization failed")
	}
	return out.s.String(), nil
}
