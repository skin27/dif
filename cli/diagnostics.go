package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"dif/api"
)

// diagnosticsOf returns the diagnostics of the static checks of a program, by
// the input path of the document that defines the flow. Those of a flow from
// a document that is no input (an import) go with the first path.
func diagnosticsOf(info *api.ProgramInfo, paths []string) map[string][]api.Diagnostic {
	out := map[string][]api.Diagnostic{}
	if info == nil || len(paths) == 0 {
		return out
	}
	for _, f := range info.Flows {
		if f.Flow == nil || len(f.Flow.Diagnostics) == 0 {
			continue
		}
		path := paths[0]
		for _, p := range paths {
			if p == f.Origin {
				path = p
			}
		}
		out[path] = append(out[path], f.Flow.Diagnostics...)
	}
	return out
}

// verdict tells whether diagnostics fail validation: errors do, and with
// strict warnings too. summary counts them, such as "1 error, 2 warnings".
func verdict(ds []api.Diagnostic, strict bool) (failed bool, summary string) {
	var errors, warnings int
	for _, d := range ds {
		switch d.Severity {
		case api.SeverityError:
			errors++
		case api.SeverityWarning:
			warnings++
		}
	}
	var parts []string
	if errors > 0 {
		parts = append(parts, plural(errors, "error"))
	}
	if warnings > 0 {
		parts = append(parts, plural(warnings, "warning"))
	}
	if n := len(ds) - errors - warnings; n > 0 {
		parts = append(parts, plural(n, "note"))
	}
	return errors > 0 || strict && warnings > 0, strings.Join(parts, ", ")
}

// staticChecksFailed is the error line of a validation the static checks fail.
func staticChecksFailed(strict bool) string {
	if strict {
		return "Error: the static checks found errors or warnings (--strict)"
	}
	return "Error: the static checks found errors"
}

// printValidation writes the text result of validating path: valid or not,
// and its diagnostics, one per line.
func printValidation(w io.Writer, path string, ds []api.Diagnostic, strict bool) {
	failed, summary := verdict(ds, strict)
	switch {
	case failed:
		fmt.Fprintf(w, "%s: invalid: the static checks found %s\n", path, summary)
	case summary != "":
		fmt.Fprintf(w, "%s: valid (structure and option schemas); the static checks found %s\n", path, summary)
	default:
		fmt.Fprintf(w, "%s: valid (structure and option schemas)\n", path)
	}
	for _, d := range ds {
		fmt.Fprintf(w, "  %s\n", d)
	}
}

// validationResult is the JSON result of validating path.
func validationResult(path string, ds []api.Diagnostic, strict bool) validationInfo {
	r := validationInfo{Path: path, Valid: true, Diagnostics: ds}
	if failed, summary := verdict(ds, strict); failed {
		r.Valid, r.Error = false, "the static checks found "+summary
	}
	return r
}

// strictFlag removes --strict (or --strict=true|false) from args and returns
// its value; the other arguments are left as they are.
func strictFlag(args []string) (rest []string, strict bool, err error) {
	for i, a := range args {
		if a == "--" {
			return append(rest, args[i:]...), strict, nil
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "strict" {
			rest = append(rest, a)
			continue
		}
		strict = true
		if hasValue {
			if strict, err = strconv.ParseBool(value); err != nil {
				return nil, false, fmt.Errorf("--strict must be true or false")
			}
		}
	}
	return rest, strict, nil
}

// takesValue reports whether a flag takes the next argument as its value: all
// do but boolean flags.
func takesValue(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !b.IsBoolFlag()
}
