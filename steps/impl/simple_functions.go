package impl

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// simpleFunction compiles the simple-language functions whose value is
// computed on every evaluation: random and the current date. ok reports
// whether ref is one of them; err whether it is, but invalid.
func simpleFunction(ref string) (fn func() string, ok bool, err error) {
	if args, found := strings.CutPrefix(ref, "random("); found && strings.HasSuffix(args, ")") {
		fn, err := randomFunction(args[:len(args)-1])
		if err != nil {
			return nil, true, fmt.Errorf("${%s}: %w", ref, err)
		}
		return fn, true, nil
	}

	var zone, format string
	switch {
	case strings.HasPrefix(ref, "date:"):
		parts := strings.SplitN(ref, ":", 3) // the format may hold colons
		if len(parts) < 3 || parts[1] != "now" {
			return nil, true, fmt.Errorf("${%s}: want ${date:now:<format>}", ref)
		}
		format = parts[2]
	case strings.HasPrefix(ref, "date-with-timezone:"):
		parts := strings.SplitN(ref, ":", 4)
		if len(parts) < 4 || parts[1] != "now" {
			return nil, true, fmt.Errorf("${%s}: want ${date-with-timezone:now:<zone>:<format>}", ref)
		}
		zone, format = parts[2], parts[3]
	default:
		return nil, false, nil
	}

	layout, err := javaDateLayout(format)
	if err != nil {
		return nil, true, fmt.Errorf("${%s}: %w", ref, err)
	}
	loc := time.Local
	if zone != "" {
		if loc, err = time.LoadLocation(zone); err != nil {
			return nil, true, fmt.Errorf("${%s}: %w", ref, err)
		}
	}
	return func() string { return time.Now().In(loc).Format(layout) }, true, nil
}

// randomFunction returns a function that draws a random integer: random(max)
// from 0 up to max, random(min,max) from min up to max (max excluded).
func randomFunction(args string) (func() string, error) {
	a, b := "0", args
	if x, y, two := strings.Cut(args, ","); two {
		a, b = x, y
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(a))
	hi, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("want random(<max>) or random(<min>,<max>) with integers")
	}
	if hi <= lo {
		return nil, fmt.Errorf("max %d must be greater than min %d", hi, lo)
	}
	return func() string { return strconv.Itoa(lo + rand.IntN(hi-lo)) }, nil
}

// javaDateLayout converts a Java date format, such as yyyy-MM-dd HH:mm:ss.SSS,
// into a Go time layout. Text in single quotes is literal (” is a quote).
// Letters without a Go equivalent are rejected.
func javaDateLayout(format string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(format); {
		c := format[i]
		if c == '\'' {
			if i+1 < len(format) && format[i+1] == '\'' {
				b.WriteByte('\'')
				i += 2
				continue
			}
			for i++; ; i++ { // quoted text, in which '' is a quote too
				if i == len(format) {
					return "", fmt.Errorf("date format %q: unclosed quote", format)
				}
				if format[i] != '\'' {
					b.WriteByte(format[i])
				} else if i+1 < len(format) && format[i+1] == '\'' {
					b.WriteByte('\'')
					i++
				} else {
					i++
					break
				}
			}
			continue
		}
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
			b.WriteByte(c)
			i++
			continue
		}

		n := 1
		for i+n < len(format) && format[i+n] == c {
			n++
		}
		i += n
		layout := javaDateField(c, n)
		if c == 'S' {
			// Go writes fractions of a second only right after a dot or comma.
			if s := b.String(); s == "" || (s[len(s)-1] != '.' && s[len(s)-1] != ',') {
				return "", fmt.Errorf("date format %q: S (fraction of a second) must follow . or ,", format)
			}
		}
		if layout == "" {
			return "", fmt.Errorf("date format %q: letter %c is not supported", format, c)
		}
		b.WriteString(layout)
	}
	return b.String(), nil
}

// javaDateField returns the Go layout of a Java date field: letter c repeated
// n times. It returns "" for an unsupported field.
func javaDateField(c byte, n int) string {
	pick := func(short, long string) string {
		if n == 1 {
			return short
		}
		return long
	}
	switch c {
	case 'y', 'u':
		if n == 2 {
			return "06"
		}
		return "2006"
	case 'M', 'L':
		switch {
		case n <= 2:
			return pick("1", "01")
		case n == 3:
			return "Jan"
		}
		return "January"
	case 'd':
		return pick("2", "02")
	case 'D':
		return "002"
	case 'H', 'k':
		return "15"
	case 'h', 'K':
		return pick("3", "03")
	case 'm':
		return pick("4", "04")
	case 's':
		return pick("5", "05")
	case 'S':
		return strings.Repeat("0", n)
	case 'a':
		return "PM"
	case 'E':
		if n <= 3 {
			return "Mon"
		}
		return "Monday"
	case 'z':
		return "MST"
	case 'Z':
		return "-0700"
	case 'X':
		switch n {
		case 1:
			return "Z07"
		case 2:
			return "Z0700"
		}
		return "Z07:00"
	}
	return ""
}
