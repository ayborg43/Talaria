// Package cron stores scheduled agent jobs and runs them on time.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MinInterval is the shortest repeat period allowed, so a job cannot hammer the model.
const MinInterval = time.Minute

// Schedule says when a job fires next.
type Schedule interface {
	// Next returns the first fire time strictly after the given time, in its
	// location. ok is false when there are no more runs.
	Next(after time.Time) (next time.Time, ok bool)
}

var macros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dowNames   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

// ParseSchedule accepts:
//
//	"*/15 9-17 * * mon-fri"   five-field cron (minute hour day-of-month month day-of-week)
//	"@hourly" "@daily" "@weekly" "@monthly" "@yearly"
//	"@every 90m"              repeat at a fixed interval (at least 1m)
//	"@at 2026-10-21T09:00:00+01:00"   run once (RFC 3339)
func ParseSchedule(spec string) (Schedule, error) {
	spec = strings.Join(strings.Fields(spec), " ")
	switch {
	case spec == "":
		return nil, fmt.Errorf("schedule is empty")
	case strings.HasPrefix(spec, "@every "):
		d, err := time.ParseDuration(strings.TrimPrefix(spec, "@every "))
		if err != nil {
			return nil, fmt.Errorf("bad interval in %q: %v", spec, err)
		}
		if d < MinInterval {
			return nil, fmt.Errorf("interval must be at least %s", MinInterval)
		}
		return every(d), nil
	case strings.HasPrefix(spec, "@at "):
		t, err := time.Parse(time.RFC3339, strings.TrimPrefix(spec, "@at "))
		if err != nil {
			return nil, fmt.Errorf("bad time in %q: use RFC 3339, e.g. 2026-10-21T09:00:00+01:00", spec)
		}
		return at(t), nil
	}

	if m, ok := macros[spec]; ok {
		spec = m
	}
	f := strings.Fields(spec)
	if len(f) != 5 {
		return nil, fmt.Errorf("%q: want 5 fields (minute hour day-of-month month day-of-week), @every, or @at", spec)
	}
	var e expr
	var err error
	if e.min, err = parseField(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if e.hour, err = parseField(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if e.dom, err = parseField(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if e.month, err = parseField(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if e.dow, err = parseField(f[4], 0, 7, dowNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if e.dow&(1<<7) != 0 { // 7 is also Sunday
		e.dow = e.dow&^(1<<7) | 1
	}
	return &e, nil
}

type every time.Duration

func (d every) Next(after time.Time) (time.Time, bool) { return after.Add(time.Duration(d)), true }

type at time.Time

func (a at) Next(after time.Time) (time.Time, bool) {
	t := time.Time(a)
	return t, t.After(after)
}

type expr struct {
	min, hour, dom, month, dow uint64
}

func (e *expr) dayMatches(t time.Time) bool {
	dom := e.dom&(1<<uint(t.Day())) != 0
	dow := e.dow&(1<<uint(t.Weekday())) != 0
	// Standard cron: when both day fields are restricted, either may match.
	// A "*" field has every bit set, so && is right when one is unrestricted.
	if e.domRestricted() && e.dowRestricted() {
		return dom || dow
	}
	return dom && dow
}

func (e *expr) domRestricted() bool { return e.dom != fullMask(1, 31) }
func (e *expr) dowRestricted() bool { return e.dow != fullMask(0, 6) }

func fullMask(lo, hi int) uint64 {
	var m uint64
	for v := lo; v <= hi; v++ {
		m |= 1 << uint(v)
	}
	return m
}

func (e *expr) Next(after time.Time) (time.Time, bool) {
	loc := after.Location()
	t := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), after.Minute(), 0, 0, loc).Add(time.Minute)
	// Each iteration jumps a month, day, hour or minute; a leap-day schedule needs a few hundred.
	for i := 0; i < 20000; i++ {
		switch {
		case e.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
		case !e.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
		case e.hour&(1<<uint(t.Hour())) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
		case e.min&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false // e.g. February 31
}

// parseField reads one cron field into a bitset of allowed values.
func parseField(s string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	value := func(tok string) (int, error) {
		if v, ok := names[strings.ToLower(tok)]; ok {
			return v, nil
		}
		v, err := strconv.Atoi(tok)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", tok)
		}
		return v, nil
	}

	for _, part := range strings.Split(s, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("bad step %q", stepStr)
			}
			step = n
		}

		var a, b int
		var err error
		switch {
		case rng == "*":
			a, b = lo, hi
		case strings.Contains(rng, "-"):
			from, to, _ := strings.Cut(rng, "-")
			if a, err = value(from); err != nil {
				return 0, err
			}
			if b, err = value(to); err != nil {
				return 0, err
			}
		default:
			if a, err = value(rng); err != nil {
				return 0, err
			}
			b = a
			if hasStep {
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}
