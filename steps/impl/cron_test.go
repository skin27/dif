package impl

import (
	"context"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the tests use Europe/Amsterdam on any machine

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestCronNext(t *testing.T) {
	utc := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}
	sat := utc(2026, 10, 3, 10, 15, 30) // a Saturday
	for _, tt := range []struct {
		expr        string
		after, want time.Time
	}{
		{"0 * * * * ?", sat, utc(2026, 10, 3, 10, 16, 0)},
		{"*/15 * * * * ?", utc(2026, 10, 3, 10, 15, 31), utc(2026, 10, 3, 10, 15, 45)},
		{"5/20 * * * * ?", utc(2026, 10, 3, 10, 0, 6), utc(2026, 10, 3, 10, 0, 25)},
		{"0 0 3 * * ?", sat, utc(2026, 10, 4, 3, 0, 0)},
		{"0 0 12 ? * MON-FRI", sat, utc(2026, 10, 5, 12, 0, 0)},
		{"0 0 12 ? * 1", sat, utc(2026, 10, 4, 12, 0, 0)}, // Quartz: 1 is Sunday
		{"0 30 9 1,15 * ?", sat, utc(2026, 10, 15, 9, 30, 0)},
		{"0 0 0 29 2 ?", sat, utc(2028, 2, 29, 0, 0, 0)},
		{"0 0 0 1 jan ?", utc(2026, 12, 31, 23, 59, 59), utc(2027, 1, 1, 0, 0, 0)},
		{"0 0 22-23 * * ? *", utc(2026, 10, 3, 23, 30, 0), utc(2026, 10, 4, 22, 0, 0)},
		{"0 0 0 31 2 ?", sat, time.Time{}}, // never
	} {
		s, err := parseCron(tt.expr, time.UTC)
		if err != nil {
			t.Fatalf("%s: %v", tt.expr, err)
		}
		if got := s.next(tt.after); !got.Equal(tt.want) {
			t.Errorf("%s after %v = %v, want %v", tt.expr, tt.after, got, tt.want)
		}
	}
}

func TestCronDaylightSaving(t *testing.T) {
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	at := func(utcHour, utcMin int, mo time.Month, d int) time.Time {
		return time.Date(2026, mo, d, utcHour, utcMin, 0, 0, time.UTC)
	}
	for _, tt := range []struct {
		name, expr  string
		after, want time.Time
	}{
		// On 29 March 2026 the clocks go from 02:00 to 03:00: 02:30 does not exist.
		{"skipped when clocks go forward", "0 30 2 * * ?", at(2, 0, 3, 28), at(0, 30, 3, 30)},
		// On 25 October 2026 they go from 03:00 back to 02:00: 02:30 happens twice.
		{"fixed time fires once when clocks go back", "0 30 2 * * ?", at(0, 30, 10, 25), at(1, 30, 10, 26)},
		{"every hour keeps running when clocks go back", "0 30 * * * ?", at(0, 30, 10, 25), at(1, 30, 10, 25)},
	} {
		s, err := parseCron(tt.expr, ams)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.next(tt.after); !got.Equal(tt.want) {
			t.Errorf("%s: after %v = %v, want %v", tt.name, tt.after.In(ams), got, tt.want.In(ams))
		}
	}
}

func TestCronInvalid(t *testing.T) {
	for expr, want := range map[string]string{
		"* * * *":          "want 6 or 7 fields",
		"0 0 0 L * ?":      "L, W and # are not supported",
		"0 0 0 ? * 6#3":    "L, W and # are not supported",
		"0 0 0 1 * MON":    "set day-of-month or day-of-week, not both",
		"? * * * * *":      "seconds: ? is only allowed in day-of-month and day-of-week",
		"60 * * * * ?":     "seconds: 60 is not between 0 and 59",
		"0 0 0 ? * 0":      "day-of-week: 0 is not between 1 and 7",
		"0 0 5-2 * * ?":    "hours: range 5-2: 5 is after 2",
		"*/0 * * * * ?":    `step "0" is not a positive number`,
		"0 0 0 * * ? 2027": `year "2027" is not supported`,
		"0 0 0 ? FOO *":    `month: "FOO" is not a value`,
	} {
		if _, err := parseCron(expr, time.UTC); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want containing %q", expr, err, want)
		}
	}
}

func TestQuartzSource(t *testing.T) {
	src := mustProcessor(t, stepdef.Source, "quartz:every-second", map[string]any{"cron": "* * * * * ?", "timeZone": "Europe/Amsterdam"}).(stepdef.SourceProcessor)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	msgs := make(chan message.Message, 10)
	go src.Run(ctx, func(m message.Message, _ func(message.Message, error)) error { msgs <- m; return nil })

	select {
	case m := <-msgs:
		fired, err := time.Parse(time.RFC3339, m[QuartzFireTime].(string))
		if err != nil || m[message.Body] != nil || time.Since(fired) > time.Second {
			t.Errorf("message = %v (%v), want no body and a recent fire time", m, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message within 2s")
	}

	wantInvalid(t, stepdef.Source, "quartz:x", nil, "missing required option cron")
	wantInvalid(t, stepdef.Source, "quartz:x", map[string]any{"cron": "0 0 0 31 2 ?"}, "never fires")
	wantInvalid(t, stepdef.Source, "quartz:x", map[string]any{"cron": "* * * * * ?", "timeZone": "Mars/Olympus"}, "option timeZone")
}

func TestExchangePatternSteps(t *testing.T) {
	for name, want := range map[string]string{
		"setoneway": message.InOnly, "setfireandforget": message.InOnly,
		"setrequestreply": message.InOut, "settwoways": message.InOut, "setrequestandreply": message.InOut,
	} {
		if got := process(t, name, nil, message.New("x"))[message.ExchangePattern]; got != want {
			t.Errorf("%s: exchange pattern = %v, want %s", name, got, want)
		}
	}
}
