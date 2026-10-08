package impl

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSchedule is a Quartz cron expression: seconds, minutes, hours, day of
// month, month, day of week (1-7 = SUN-SAT) and an optional year, which must
// be *. A field holds *, ? (the day fields only), a value, a range a-b, a
// step (*/n, a/n, a-b/n) or a comma-separated list of those; months and days
// of the week may be named (JAN, MON). L, W and # are not supported.
type cronSchedule struct {
	fields  [6]uint64 // per field: bit v is set if value v matches
	domAny  bool      // day of month is * or ?
	dowAny  bool      // day of week is * or ?
	hourAny bool      // every hour matches
	loc     *time.Location
}

// The fields of a cron expression, in order.
const (
	cronSecond = iota
	cronMinute
	cronHour
	cronDayOfMonth
	cronMonth
	cronDayOfWeek
)

var cronFields = [6]struct {
	name     string
	min, max int
	names    []string // names[i] is value min+i
}{
	{"seconds", 0, 59, nil},
	{"minutes", 0, 59, nil},
	{"hours", 0, 23, nil},
	{"day-of-month", 1, 31, nil},
	{"month", 1, 12, []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}},
	{"day-of-week", 1, 7, []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}},
}

// parseCron parses a Quartz cron expression whose times are in loc.
func parseCron(expr string, loc *time.Location) (*cronSchedule, error) {
	f := strings.Fields(expr)
	if len(f) != 6 && len(f) != 7 {
		return nil, fmt.Errorf("cron %q: want 6 or 7 fields (seconds minutes hours day-of-month month day-of-week [year]), got %d", expr, len(f))
	}
	if len(f) == 7 && f[6] != "*" {
		return nil, fmt.Errorf("cron %q: year %q is not supported; use * or leave it out", expr, f[6])
	}
	s := &cronSchedule{loc: loc}
	for i := range s.fields {
		mask, err := parseCronField(f[i], i)
		if err != nil {
			return nil, fmt.Errorf("cron %q: %s: %w", expr, cronFields[i].name, err)
		}
		s.fields[i] = mask
	}
	s.domAny = f[cronDayOfMonth] == "*" || f[cronDayOfMonth] == "?"
	s.dowAny = f[cronDayOfWeek] == "*" || f[cronDayOfWeek] == "?"
	s.hourAny = s.fields[cronHour] == 1<<24-1
	if !s.domAny && !s.dowAny {
		return nil, fmt.Errorf("cron %q: set day-of-month or day-of-week, not both; use ? for the other", expr)
	}
	return s, nil
}

// parseCronField returns the values field i of an expression matches, as bits.
func parseCronField(s string, i int) (uint64, error) {
	f := cronFields[i]
	if s == "?" {
		if i != cronDayOfMonth && i != cronDayOfWeek {
			return 0, fmt.Errorf("? is only allowed in day-of-month and day-of-week")
		}
		s = "*"
	}
	value := func(v string) (int, error) {
		for j, name := range f.names {
			if strings.EqualFold(v, name) {
				return f.min + j, nil
			}
		}
		n, err := strconv.Atoi(v)
		switch {
		case err != nil && strings.ContainsAny(strings.ToUpper(v), "LW#"):
			return 0, fmt.Errorf("%q: L, W and # are not supported", v)
		case err != nil:
			return 0, fmt.Errorf("%q is not a value", v)
		case n < f.min || n > f.max:
			return 0, fmt.Errorf("%d is not between %d and %d", n, f.min, f.max)
		}
		return n, nil
	}

	var mask uint64
	for _, part := range strings.Split(s, ",") {
		rng, stepText, hasStep := strings.Cut(part, "/")
		lo, hi := f.min, f.max
		var err error
		switch a, b, isRange := strings.Cut(rng, "-"); {
		case rng == "*":
		case isRange:
			if lo, err = value(a); err == nil {
				hi, err = value(b)
			}
		default:
			lo, err = value(rng)
			if !hasStep {
				hi = lo // a single value; with a step, a/n runs from a to the end
			}
		}
		if err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("range %s: %d is after %d", rng, lo, hi)
		}
		step := 1
		if hasStep {
			if step, err = strconv.Atoi(stepText); err != nil || step < 1 {
				return 0, fmt.Errorf("step %q is not a positive number", stepText)
			}
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << v
		}
	}
	return mask, nil
}

// matches reports whether value v matches field i.
func (s *cronSchedule) matches(i, v int) bool { return s.fields[i]&(1<<v) != 0 }

// next returns the first time after after that matches the schedule, or the
// zero time if there is none within five years.
//
// Daylight saving time: a time that does not exist on a day (clocks go
// forward) is skipped that day, as in Quartz. When clocks go back, a schedule
// with fixed hours fires once in the hour that repeats; one that runs every
// hour keeps running in real time.
func (s *cronSchedule) next(after time.Time) time.Time {
	after = after.In(s.loc)
	t := after.Truncate(time.Second).Add(time.Second)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		y, mo, d := t.Date()
		switch {
		case !s.matches(cronMonth, int(mo)):
			t = time.Date(y, mo+1, 1, 0, 0, 0, 0, s.loc)
		case !s.dayMatches(t):
			t = time.Date(y, mo, d+1, 0, 0, 0, 0, s.loc)
		// Within a day, step in real time to the next local hour, minute or
		// second, so a repeated hour never sends t back.
		case !s.matches(cronHour, t.Hour()):
			t = t.Add(time.Duration(60-t.Minute())*time.Minute - time.Duration(t.Second())*time.Second)
		case !s.matches(cronMinute, t.Minute()):
			t = t.Add(time.Duration(60-t.Second()) * time.Second)
		case !s.matches(cronSecond, t.Second()):
			t = t.Add(time.Second)
		case !s.hourAny && !wallAfter(t, after):
			t = t.Add(time.Second) // a fixed time again in a repeated hour
		default:
			return t
		}
	}
	return time.Time{}
}

func (s *cronSchedule) dayMatches(t time.Time) bool {
	return (s.domAny || s.matches(cronDayOfMonth, t.Day())) &&
		(s.dowAny || s.matches(cronDayOfWeek, int(t.Weekday())+1))
}

// wallAfter reports whether the clock on the wall shows a later time at t
// than at u (both in the same location).
func wallAfter(t, u time.Time) bool {
	wall := func(t time.Time) time.Time {
		y, mo, d := t.Date()
		return time.Date(y, mo, d, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
	}
	return wall(t).After(wall(u))
}
