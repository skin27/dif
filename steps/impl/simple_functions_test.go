package impl

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"dif/message"
)

func TestJavaDateLayout(t *testing.T) {
	at := time.Date(2026, 3, 7, 14, 5, 9, 123_000_000, time.UTC)
	for format, want := range map[string]string{
		"yyyy-MM-dd HH:mm:ss":      "2026-03-07 14:05:09",
		"yy/M/d h:m:s a":           "26/3/7 2:5:9 PM",
		"ss":                       "09",
		"HH:mm:ss.SSS":             "14:05:09.123",
		"EEE, dd MMM yyyy":         "Sat, 07 Mar 2026",
		"EEEE d MMMM":              "Saturday 7 March",
		"yyyy-MM-dd'T'HH:mm:ssXXX": "2026-03-07T14:05:09Z",
		"yyyyMMddHHmmssZ":          "20260307140509+0000",
		"'o''clock' H":             "o'clock 14",
		"D":                        "066",
	} {
		layout, err := javaDateLayout(format)
		if err != nil {
			t.Errorf("%q: %v", format, err)
			continue
		}
		if got := at.Format(layout); got != want {
			t.Errorf("%q: %q, want %q", format, got, want)
		}
	}

	for format, want := range map[string]string{
		"yyyy-MM-dd Q": "letter Q is not supported",
		"ss SSS":       "S (fraction of a second) must follow . or ,",
		"'open":        "unclosed quote",
	} {
		if _, err := javaDateLayout(format); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want containing %q", format, err, want)
		}
	}
}

func TestSimpleFunctions(t *testing.T) {
	eval := func(expr string) string {
		t.Helper()
		e, err := compileExpression("simple", expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		s, err := e.eval(message.New(nil))
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		return s
	}

	for range 50 {
		if n, err := strconv.Atoi(eval("${random(3)}")); err != nil || n < 0 || n >= 3 {
			t.Fatalf("random(3) = %d, %v", n, err)
		}
		if n, err := strconv.Atoi(eval("${random(5, 7)}")); err != nil || n < 5 || n >= 7 {
			t.Fatalf("random(5, 7) = %d, %v", n, err)
		}
	}

	before := time.Now().Year()
	if got := eval("${date:now:yyyy}"); got != strconv.Itoa(before) && got != strconv.Itoa(before+1) {
		t.Errorf("date:now:yyyy = %q", got)
	}
	got := eval("${date-with-timezone:now:UTC:yyyy-MM-dd HH:mm}")
	if _, err := time.Parse("2006-01-02 15:04", got); err != nil {
		t.Errorf("date-with-timezone = %q: %v", got, err)
	}

	for expr, want := range map[string]string{
		"${random(3,3)}":                        "max 3 must be greater than min 3",
		"${date:today:yyyy}":                    "want ${date:now:<format>}",
		"${date-with-timezone:now:yyyy}":        "want ${date-with-timezone:now:<zone>:<format>}",
		"${date-with-timezone:now:Mars/Base:y}": "Mars/Base",
		"${date:now:yyyy Q}":                    "letter Q is not supported",
	} {
		if _, err := compileExpression("simple", expr); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want containing %q", expr, err, want)
		}
	}
}
