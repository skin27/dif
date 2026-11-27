// Package program resolves local DIL documents without constructing processors.
package program

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	flowimpl "dif/flows/impl"
	"dif/internal/dilsyntax"
	"dif/internal/mapping"
)

type Source struct {
	Name       string // safe diagnostic label; remote URLs must not be used here
	Path       string // trusted local origin; empty disallows imports
	Data       []byte // nil reads Path; supplied bytes are copied
	Discovered bool   // directory entry, not an explicit root
}

type Limits struct {
	Mapping       mapping.Limits
	MaxBytes      int64
	MaxTotalBytes int64
	MaxFiles      int
	MaxDepth      int
}

type Dependency struct {
	Namespace string `json:"namespace"`
	Document  string `json:"document"`
}
type DocumentInfo struct {
	Name         string                     `json:"name"`
	Imports      []Dependency               `json:"imports"`
	Integrations []flowimpl.IntegrationInfo `json:"integrations"`
	Formats      []flowimpl.FormatInfo      `json:"formats,omitempty"`
	Functions    []mapping.FunctionInfo     `json:"functions,omitempty"`
	Processors   []flowimpl.ProcessorInfo   `json:"processors,omitempty"`
	Connections  []flowimpl.ConnectionInfo  `json:"connections,omitempty"`
	Stores       []flowimpl.StoreInfo       `json:"stores,omitempty"`
	Maps         []mapping.Info             `json:"maps,omitempty"`
	Services     []mapping.ServiceInfo      `json:"services,omitempty"`
}
type Entry struct {
	Origin string
	Plan   flowimpl.FlowPlan
}
type Result struct {
	Documents       []DocumentInfo
	Flows           []Entry
	ProcessorChecks []flowimpl.ProcessorCheck
}

type node struct {
	key      string
	name     string
	doc      *flowimpl.Document
	imports  map[string]*node
	info     DocumentInfo
	visiting bool
	height   int // longest import path below this document
}
type resolver struct {
	ctx    context.Context
	limits Limits
	total  int64
	nodes  map[string]*node
	seeds  map[string]Source
	order  []*node
	stack  []string
}

func Resolve(ctx context.Context, sources []Source, limits Limits) (*Result, error) {
	if _, err := limits.Mapping.Normalize(); err != nil {
		return nil, err
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = 4 << 20
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = 64 << 20
	}
	if limits.MaxFiles == 0 {
		limits.MaxFiles = 256
	}
	if limits.MaxDepth == 0 {
		limits.MaxDepth = 32
	}
	if limits.MaxBytes < 1 || limits.MaxTotalBytes < 1 || limits.MaxFiles < 1 || limits.MaxDepth < 1 {
		return nil, fmt.Errorf("program limits must be positive")
	}
	r := &resolver{ctx: ctx, limits: limits, nodes: map[string]*node{}, seeds: map[string]Source{}}
	keys := []string{}
	explicit := map[string]bool{}
	for i, s := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("anonymous:%d", i)
		if s.Path != "" {
			var err error
			original := s.Path
			key, s.Path, err = canonical(s.Path)
			if err != nil {
				var pathErr *os.PathError
				if errors.As(err, &pathErr) {
					return nil, &os.PathError{Op: "open", Path: original, Err: pathErr.Err}
				}
				return nil, fmt.Errorf("%s: %w", s.Name, err)
			}
		}
		if !s.Discovered {
			if explicit[key] {
				return nil, fmt.Errorf("duplicate entrypoint %q", s.Name)
			}
			explicit[key] = true
		}
		if _, ok := r.seeds[key]; ok {
			return nil, fmt.Errorf("duplicate entrypoint %q", s.Name)
		}
		if s.Name == "" {
			s.Name = s.Path
			if s.Name == "" {
				s.Name = fmt.Sprintf("document #%d", i+1)
			}
		}
		if s.Data != nil {
			s.Data = append([]byte{}, s.Data...)
		}
		r.seeds[key] = s
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no flow documents")
	}
	for _, key := range keys {
		if _, err := r.visit(key, r.seeds[key], 0); err != nil {
			return nil, err
		}
	}
	result := &Result{Documents: []DocumentInfo{}, Flows: []Entry{}}
	for _, n := range r.order {
		imports := map[string]*flowimpl.Document{}
		for alias, dep := range n.imports {
			imports[alias] = dep.doc
		}
		scopeKey := n.key
		if strings.HasPrefix(scopeKey, "anonymous:") {
			scopeKey = n.name
		}
		n.doc.BindScopeWithKey(n.name, scopeKey, imports)
	}
	functionLimits, _ := limits.Mapping.Normalize()
	count, sourceSize := 0, 0
	for _, n := range r.order {
		c, s, e := n.doc.FunctionSourceSize()
		if e != nil {
			return nil, e
		}
		count += c
		sourceSize += s
	}
	if count > functionLimits.Functions || sourceSize > functionLimits.TotalSourceBytes {
		return nil, fmt.Errorf("program function declaration/source limit exceeded")
	}
	for _, n := range r.order {
		if err := n.doc.PrepareFunctions(ctx, limits.Mapping); err != nil {
			return nil, fmt.Errorf("%s: %w", n.name, err)
		}
		n.info.Functions = n.doc.FunctionInfo()
		n.info.Processors = n.doc.ProcessorInfo()
		n.info.Connections = n.doc.ConnectionInfo()
		n.info.Stores = n.doc.StoreInfo()
	}
	for _, n := range r.order {
		if err := n.doc.PrepareMappings(ctx, limits.Mapping); err != nil {
			return nil, fmt.Errorf("%s: %w", n.name, err)
		}
		n.info.Formats = n.doc.FormatInfo()
		n.info.Maps = n.doc.MappingInfo()
	}
	for _, n := range r.order {
		if err := n.doc.PrepareServices(ctx, limits.Mapping); err != nil {
			return nil, fmt.Errorf("%s: %w", n.name, err)
		}
		n.info.Services = n.doc.ServiceInfo()
	}
	for _, n := range r.order {
		checks, e := n.doc.ProcessorChecks()
		if e != nil {
			return nil, e
		}
		result.ProcessorChecks = append(result.ProcessorChecks, checks...)
	}
	ids := map[string]string{}
	for _, n := range r.order {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		imports := map[string]*flowimpl.Document{}
		for alias, dep := range n.imports {
			imports[alias] = dep.doc
		}
		plans, err := n.doc.Plans(imports)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n.name, err)
		}
		for _, p := range plans {
			if p.ID == "" {
				return nil, fmt.Errorf("%s: flow has no id", n.name)
			}
			if prev, ok := ids[p.ID]; ok {
				return nil, fmt.Errorf("duplicate flow id %q in %s and %s", p.ID, prev, n.name)
			}
			ids[p.ID] = n.name
			result.Flows = append(result.Flows, Entry{n.name, p})
		}
		result.Documents = append(result.Documents, n.info)
	}
	return result, nil
}

func canonical(path string) (string, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", err
	}
	key := filepath.Clean(real)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	// Resolve imports relative to the canonical document, independent of aliases.
	return key, real, nil
}

func (r *resolver) visit(key string, source Source, depth int) (*node, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if depth > r.limits.MaxDepth {
		return nil, fmt.Errorf("import depth exceeds %d", r.limits.MaxDepth)
	}
	if n := r.nodes[key]; n != nil {
		if n.visiting {
			return nil, fmt.Errorf("import cycle: %s", strings.Join(append(append([]string(nil), r.stack...), n.name), " -> "))
		}
		if depth+n.height > r.limits.MaxDepth {
			return nil, fmt.Errorf("import depth exceeds %d", r.limits.MaxDepth)
		}
		return n, nil
	}
	if len(r.nodes) >= r.limits.MaxFiles {
		return nil, fmt.Errorf("program exceeds %d files", r.limits.MaxFiles)
	}
	if seed, ok := r.seeds[key]; ok {
		source = seed
	}
	data := source.Data
	if data == nil {
		if source.Path == "" {
			return nil, fmt.Errorf("%s: missing document bytes or local path", source.Name)
		}
		f, err := os.Open(source.Path)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("%s: document must be a regular file", source.Name)
		}
		data, err = io.ReadAll(io.LimitReader(f, r.limits.MaxBytes+1))
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: document read failed", source.Name)
		}
	}
	if int64(len(data)) > r.limits.MaxBytes {
		return nil, fmt.Errorf("%s: document exceeds max-bytes", source.Name)
	}
	r.total += int64(len(data))
	if r.total > r.limits.MaxTotalBytes {
		return nil, fmt.Errorf("program exceeds total byte limit")
	}
	// A .dil file, or bytes that are DIL text, are compiled to DIL JSON; errors
	// name the document.
	data, err := dilsyntax.Document(source.Name, source.Path, data)
	if err != nil {
		return nil, err
	}
	doc, err := flowimpl.DecodeDocument(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source.Name, err)
	}
	n := &node{key: key, name: source.Name, doc: doc, imports: map[string]*node{}, visiting: true}
	n.info = DocumentInfo{Name: n.name, Imports: []Dependency{}, Integrations: doc.Integrations()}
	r.nodes[key] = n
	r.order = append(r.order, n)
	r.stack = append(r.stack, n.name)
	defer func() { r.stack = r.stack[:len(r.stack)-1] }()
	for _, imp := range doc.Imports() {
		if source.Path == "" {
			return nil, fmt.Errorf("%s: local imports require an explicit local origin", n.name)
		}
		if strings.Contains(imp.Path, "://") || strings.HasPrefix(imp.Path, `\\`) || strings.HasPrefix(imp.Path, "//") {
			return nil, fmt.Errorf("%s: namespace %q requires a local file", n.name, imp.Namespace)
		}
		path := imp.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(source.Path), path)
		}
		depKey, path, err := canonical(path)
		if err != nil {
			return nil, fmt.Errorf("%s: import %q: %w", n.name, imp.Namespace, err)
		}
		dep, err := r.visit(depKey, Source{Name: path, Path: path}, depth+1)
		if err != nil {
			return nil, fmt.Errorf("%s: import %q: %w", n.name, imp.Namespace, err)
		}
		n.imports[imp.Namespace] = dep
		n.height = max(n.height, dep.height+1)
		n.info.Imports = append(n.info.Imports, Dependency{imp.Namespace, dep.name})
	}
	n.visiting = false
	return n, nil
}
