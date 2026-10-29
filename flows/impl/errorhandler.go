package impl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	flowdef "dif/flows/definition"
)

// errorHandler returns the error handler an error step defines, and the id of
// its outbound link (empty if it has no error route). Its options are Camel's
// redelivery settings: maximumRedeliveries (or redeliveryAttempts) and
// redeliveryDelay (or redeliveryInterval) in milliseconds, default 1000, and
// backoffMultiplier and maximumRedeliveryDelay.
//
// Without a match option the handler takes every error. With errorClass,
// errorStep, errorUri (the scheme of the failing step) or errorMessage (a
// regular expression), it takes only the failures that satisfy all of them.
// With handled false the message still fails after the error route.
func errorHandler(s dilStep) (*flowdef.ErrorHandler, string, error) {
	if s.URI != "failedexchange" {
		return nil, "", fmt.Errorf("error step uri %q is not supported; use failedexchange", s.URI)
	}
	var out []string
	for _, l := range s.Links.Link {
		if l.Bound != "out" {
			return nil, "", fmt.Errorf("error step needs 0 inbound links and at most 1 outbound link")
		}
		out = append(out, l.ID)
	}
	if len(out) > 1 {
		return nil, "", fmt.Errorf("error step needs 0 inbound links and at most 1 outbound link")
	}

	o, err := parseErrorOptions(s.Options)
	if err != nil {
		return nil, "", err
	}
	h := &flowdef.ErrorHandler{ID: s.ID, Matches: o.matches, Unhandled: o.unhandled, Backoff: o.backoff}
	pick := func(name, alias string, def int) int {
		if o.numbers[name] >= 0 {
			return o.numbers[name]
		}
		if o.numbers[alias] >= 0 {
			return o.numbers[alias]
		}
		return def
	}
	h.Redeliveries = pick("maximumRedeliveries", "redeliveryAttempts", 0)
	h.RedeliveryDelay = time.Duration(pick("redeliveryDelay", "redeliveryInterval", 1000)) * time.Millisecond
	if o.numbers["maximumRedeliveryDelay"] > 0 {
		h.MaxDelay = time.Duration(o.numbers["maximumRedeliveryDelay"]) * time.Millisecond
	}
	if len(o.matches) > 0 {
		h.Match = o.match.takes
	}
	if len(out) == 0 {
		return h, "", nil
	}
	return h, out[0], nil
}

// errorOptions are the options of an error step, parsed.
type errorOptions struct {
	numbers   map[string]int // the redelivery options; -1 for one that is not set
	match     errorMatch
	matches   []string // the match options that are set
	unhandled bool
	backoff   float64
}

// errorOptionError is an option of an error step that is not valid. want says
// what the option must be, without its value.
type errorOptionError struct {
	name, want string
	cause      error // nil: the option is unknown
}

func (e *errorOptionError) Error() string {
	if e.cause == nil {
		return "error step: unknown option " + e.name
	}
	return fmt.Sprintf("error step: option %s: %v", e.name, e.cause)
}

// parseErrorOptions checks and parses the options of an error step.
func parseErrorOptions(options map[string]any) (errorOptions, *errorOptionError) {
	o := errorOptions{numbers: map[string]int{"maximumRedeliveries": -1, "redeliveryAttempts": -1, "redeliveryDelay": -1, "redeliveryInterval": -1, "maximumRedeliveryDelay": -1}}
	for k, v := range options {
		var err error
		want := "must be a non-negative integer"
		switch k {
		case "maximumRedeliveries", "redeliveryAttempts", "redeliveryDelay", "redeliveryInterval", "maximumRedeliveryDelay":
			o.numbers[k], err = nonNegativeInt(v)
		case "errorClass":
			o.match.class, err = globOption(v)
			want = "must be a pattern"
			o.matches = append(o.matches, k)
		case "errorStep":
			o.match.step, err = globOption(v)
			want = "must be a pattern"
			o.matches = append(o.matches, k)
		case "errorUri":
			o.match.uri, err = globOption(v)
			want = "must be a pattern"
			o.matches = append(o.matches, k)
		case "errorMessage":
			o.match.message, err = regexpOption(v)
			want = "must be a regular expression"
			o.matches = append(o.matches, k)
		case "handled":
			var handled bool
			if handled, err = boolOption(v); err == nil {
				o.unhandled = !handled
			}
			want = "must be true or false"
		case "backoffMultiplier":
			if o.backoff, err = floatOption(v); err == nil && o.backoff < 1 {
				err = fmt.Errorf("want a number of 1 or more, got %v", v)
			}
			want = "must be a number of 1 or more"
		default:
			return o, &errorOptionError{name: k}
		}
		if err != nil {
			return o, &errorOptionError{name: k, want: want, cause: err}
		}
	}
	return o, nil
}

// errorMatch holds what an error step matches a failure on; a nil member
// matches everything.
type errorMatch struct {
	class, step, uri, message *regexp.Regexp
}

func (m errorMatch) takes(f flowdef.Failure) bool {
	scheme, _, _ := strings.Cut(f.URI, ":")
	return (m.class == nil || m.class.MatchString(f.Class)) &&
		(m.step == nil || m.step.MatchString(f.Step)) &&
		(m.uri == nil || m.uri.MatchString(scheme)) &&
		(m.message == nil || m.message.MatchString(f.Message))
}

// globOption compiles a pattern in which * stands for any text and ? for one
// character; the rest is literal.
func globOption(v any) (*regexp.Regexp, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil, fmt.Errorf("want a pattern, got %v", v)
	}
	return compileGlob(s)
}

// compileGlob compiles a pattern in which * stands for any text and ? for one
// character; the rest is literal.
func compileGlob(s string) (*regexp.Regexp, error) {
	if s == "" {
		return nil, fmt.Errorf("want a pattern, got an empty text")
	}
	q := regexp.QuoteMeta(s)
	q = strings.ReplaceAll(q, `\*`, `(?s:.*)`)
	q = strings.ReplaceAll(q, `\?`, `(?s:.)`)
	return regexp.Compile("^" + q + "$")
}

// regexpOption compiles a regular expression that matches anywhere in the text.
func regexpOption(v any) (*regexp.Regexp, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil, fmt.Errorf("want a regular expression, got %v", v)
	}
	return regexp.Compile(s)
}

// boolOption returns v, a JSON boolean or (as DIL converted from XML has it) a
// string, as a bool.
func boolOption(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		if b, err := strconv.ParseBool(x); err == nil {
			return b, nil
		}
	}
	return false, fmt.Errorf("want true or false, got %v", v)
}

// floatOption returns v, a JSON number or a string with one, as a float64.
func floatOption(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f, nil
		}
	}
	return 0, fmt.Errorf("want a number, got %v", v)
}
