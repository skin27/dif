package impl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dif/internal/mapping"
)

type dilFormat mapping.Format
type dilMap mapping.Definition

func strictDeclaration(data []byte, v any, kind string) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return fmt.Errorf("invalid %s declaration (unknown field or type)", kind)
	}
	return nil
}
func (f *dilFormat) UnmarshalJSON(data []byte) error {
	type plain dilFormat
	return strictDeclaration(data, (*plain)(f), "format")
}
func (m *dilMap) UnmarshalJSON(data []byte) error {
	type plain dilMap
	return strictDeclaration(data, (*plain)(m), "map")
}

type FormatInfo struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	SchemaRef string `json:"schemaRef,omitempty"`
	Origin    string `json:"origin,omitempty"`
}

// BindScope connects immutable documents before compiling their declarations.
// This is a preparation operation; prepared flow plans do not retain documents.
func (d *Document) BindScope(origin string, imports map[string]*Document) {
	d.origin = origin
	d.imports = imports
}
func (d *Document) localFormat(name string) (mapping.Format, bool) {
	for _, f := range d.doc.DIL.Core.Formats.Format {
		if f.Name == name {
			return mapping.Format(f), true
		}
	}
	return mapping.Format{}, false
}
func (d *Document) format(name string) (mapping.Format, error) {
	owner := d
	f, ok := d.localFormat(name)
	if !ok {
		alias, key, qualified := strings.Cut(name, ":")
		if qualified && d.imports[alias] != nil {
			owner = d.imports[alias]
			f, ok = owner.localFormat(key)
		}
	}
	if !ok {
		return f, fmt.Errorf("format %q not found", name)
	}
	f.Origin = owner.origin
	if f.SchemaRef != "" {
		src, e := owner.resource(f.SchemaRef)
		if e != nil {
			return f, e
		}
		f.Schema = src
	}
	return f, nil
}
func (d *Document) resource(name string) (string, error) {
	for _, r := range d.doc.DIL.Core.Resources.Resource {
		if r.Name == name {
			return r.Content, nil
		}
	}
	alias, key, ok := strings.Cut(name, ":")
	if ok && d.imports[alias] != nil {
		for _, r := range d.imports[alias].doc.DIL.Core.Resources.Resource {
			if r.Name == key {
				return r.Content, nil
			}
		}
	}
	return "", fmt.Errorf("resource %q not found", name)
}
func (d *Document) PrepareMappings(ctx context.Context, l mapping.Limits) error {
	if d.compiledMaps != nil {
		return nil
	}
	compiled := map[string]*mapping.Plan{}
	seen := map[string]bool{}
	for _, f := range d.doc.DIL.Core.Formats.Format {
		if e := ctx.Err(); e != nil {
			return e
		}
		if f.Name == "" || seen[f.Name] {
			return fmt.Errorf("empty or duplicate format name %q", f.Name)
		}
		seen[f.Name] = true
		resolved, e := d.format(f.Name)
		if e != nil {
			return fmt.Errorf("format %q: %w", f.Name, e)
		}
		if e = mapping.ValidateFormat(resolved, l); e != nil {
			return fmt.Errorf("format %q: %w", f.Name, e)
		}
	}
	for _, m := range d.doc.DIL.Core.Maps.Map {
		if e := ctx.Err(); e != nil {
			return e
		}
		if m.Name == "" || compiled[m.Name] != nil {
			return fmt.Errorf("empty or duplicate map name %q", m.Name)
		}
		if m.Input == "" || m.Output == "" || m.Language == "" {
			return fmt.Errorf("map %q requires input, output, and language", m.Name)
		}
		if m.Language == "identity" {
			if m.Expression != "" || m.SourceRef != "" {
				return fmt.Errorf("map %q: identity must not have source", m.Name)
			}
		} else if (m.Expression == "") == (m.SourceRef == "") {
			return fmt.Errorf("map %q requires exactly one of expression and sourceRef", m.Name)
		}
		in, e := d.format(m.Input)
		if e != nil {
			return fmt.Errorf("map %q: %w", m.Name, e)
		}
		out, e := d.format(m.Output)
		if e != nil {
			return fmt.Errorf("map %q: %w", m.Name, e)
		}
		definition := mapping.Definition(m)
		if m.SourceRef != "" {
			definition.Expression, e = d.resource(m.SourceRef)
			if e != nil {
				return fmt.Errorf("map %q: %w", m.Name, e)
			}
		}
		p, e := mapping.CompileWithFunctions(ctx, definition, in, out, d.origin, l, d.functionBindings().Resolve)
		if e != nil {
			return fmt.Errorf("map %q: %w", m.Name, e)
		}
		compiled[m.Name] = p
	}
	d.compiledMaps = compiled
	return nil
}
func (d *Document) mappingBindings() map[string]*mapping.Plan {
	out := map[string]*mapping.Plan{}
	for k, p := range d.compiledMaps {
		out[k] = p
	}
	for _, i := range d.Imports() {
		dep := d.imports[i.Namespace]
		if dep == nil {
			continue
		}
		for k, p := range dep.compiledMaps {
			name := i.Namespace + ":" + k
			if out[name] == nil {
				out[name] = p
			}
		}
	}
	return out
}
func (d *Document) MappingInfo() []mapping.Info {
	out := []mapping.Info{}
	for _, m := range d.doc.DIL.Core.Maps.Map {
		if p := d.compiledMaps[m.Name]; p != nil {
			out = append(out, p.Info())
		}
	}
	return out
}
func (d *Document) FormatInfo() []FormatInfo {
	out := []FormatInfo{}
	for _, f := range d.doc.DIL.Core.Formats.Format {
		out = append(out, FormatInfo{f.Name, f.MediaType, f.SchemaRef, d.origin})
	}
	return out
}
