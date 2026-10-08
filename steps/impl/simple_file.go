package impl

import (
	"path"
	"strings"
)

// The file: functions of the simple language read the headers that describe a
// file: CamelFileName (else file.name, which DIF's file source sets), and
// CamelFileNameOnly, CamelFileParent, CamelFilePath, CamelFileAbsolute,
// CamelFileAbsolutePath, CamelFileLength and CamelFileLastModified. What is not
// there is nothing. The rules for the name and extension are Camel's FileUtil.

// fileRef compiles ${file:what}.
func (c *compiler) fileRef(b *block, what string) (evalFn, error) {
	header := func(e *env, names ...string) (any, bool) {
		for _, n := range names {
			if v := headerValue(e.m, n); v != nil {
				return v, true
			}
		}
		return nil, false
	}
	name := func(e *env) (string, bool) {
		v, ok := header(e, "CamelFileName", "file.name")
		if !ok {
			return "", false
		}
		return render(v), true
	}
	onlyName := func(e *env) (string, bool) {
		if v, ok := header(e, "CamelFileNameOnly"); ok {
			return render(v), true
		}
		n, ok := name(e)
		if !ok {
			return "", false
		}
		return path.Base(strings.ReplaceAll(n, "\\", "/")), true
	}
	text := func(f func(e *env) (string, bool), edit func(string) string) evalFn {
		return func(e *env) (any, error) {
			s, ok := f(e)
			if !ok {
				return nil, nil
			}
			return edit(s), nil
		}
	}
	hdr := func(names ...string) evalFn {
		return func(e *env) (any, error) {
			v, _ := header(e, names...)
			return v, nil
		}
	}
	id := func(s string) string { return s }

	switch what {
	case "name":
		return text(name, id), nil
	case "name.noext":
		return text(name, func(s string) string { return stripExt(s, false) }), nil
	case "name.noext.single":
		return text(name, func(s string) string { return stripExt(s, true) }), nil
	case "name.ext", "ext":
		return text(name, func(s string) string { return onlyExt(s, false) }), nil
	case "name.ext.single":
		return text(name, func(s string) string { return onlyExt(s, true) }), nil
	case "onlyname":
		return text(onlyName, id), nil
	case "onlyname.noext":
		return text(onlyName, func(s string) string { return stripExt(s, false) }), nil
	case "onlyname.noext.single":
		return text(onlyName, func(s string) string { return stripExt(s, true) }), nil
	case "parent":
		return hdr("CamelFileParent"), nil
	case "path":
		return hdr("CamelFilePath"), nil
	case "absolute":
		return hdr("CamelFileAbsolute"), nil
	case "absolute.path":
		return hdr("CamelFileAbsolutePath"), nil
	case "length", "size":
		return hdr("CamelFileLength"), nil
	case "modified":
		return hdr("CamelFileLastModified"), nil
	}
	return nil, unsupported(b, "unknown file language syntax: "+what)
}

// stripExt removes the extension of a file name: from the first dot of the name
// itself, or with single only the last one.
func stripExt(name string, single bool) string {
	last := strings.LastIndexByte(name, '.')
	if last <= 0 {
		return name
	}
	if single {
		return name[:last]
	}
	start := strings.LastIndexAny(name, "/\\") + 1
	if first := strings.IndexByte(name[start:], '.'); first > 0 {
		return name[:start+first]
	}
	return name
}

// onlyExt is the extension of a file name: after the first dot of the name
// itself, or with single the last one; nothing without one.
func onlyExt(name string, single bool) string {
	start := strings.LastIndexAny(name, "/\\") + 1
	dot := strings.IndexByte(name[start:], '.')
	if single {
		dot = strings.LastIndexByte(name[start:], '.')
	}
	if dot < 0 {
		return ""
	}
	return name[start+dot+1:]
}
