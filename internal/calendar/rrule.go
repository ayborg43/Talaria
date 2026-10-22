package calendar

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rrule is the part of a repeat rule this reader understands: daily, weekly, monthly and yearly
// repeats, with an interval, a count or an end date, and the usual day and month lists. Anything
// else (BYSETPOS, BYWEEKNO, ...) is refused at parse time, so the event is shown once and the
// reader says so, rather than listing wrong dates.
type rrule struct {
	freq       string
	interval   int
	count      int // 0: no count
	until      time.Time
	hasUntil   bool
	byday      []dayRule
	bymonthday []int
	bymonth    []int
	wkst       time.Weekday
}

type dayRule struct {
	wd time.Weekday
	n  int // 0 is every such day; 2 is the second; -1 the last
}

var weekdays = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

func parseRRule(s string) (*rrule, error) {
	r := &rrule{interval: 1, wkst: time.Monday}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.ToUpper(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "FREQ":
			r.freq = strings.ToUpper(v)
			switch r.freq {
			case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
			default:
				return nil, fmt.Errorf("a %s repeat", strings.ToLower(v))
			}
		case "INTERVAL":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("an interval of %q", v)
			}
			r.interval = n
		case "COUNT":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("a count of %q", v)
			}
			r.count = n
		case "UNTIL":
			t, err := parseUntil(v)
			if err != nil {
				return nil, fmt.Errorf("an end of %q", v)
			}
			r.until, r.hasUntil = t, true
		case "BYDAY":
			for _, d := range strings.Split(v, ",") {
				d = strings.ToUpper(strings.TrimSpace(d))
				if len(d) < 2 {
					return nil, fmt.Errorf("a day of %q", d)
				}
				wd, ok := weekdays[d[len(d)-2:]]
				if !ok {
					return nil, fmt.Errorf("a day of %q", d)
				}
				n := 0
				if pre := d[:len(d)-2]; pre != "" {
					var err error
					if n, err = strconv.Atoi(pre); err != nil || n == 0 || n > 53 || n < -53 {
						return nil, fmt.Errorf("a day of %q", d)
					}
				}
				r.byday = append(r.byday, dayRule{wd, n})
			}
		case "BYMONTHDAY":
			for _, d := range strings.Split(v, ",") {
				n, err := strconv.Atoi(strings.TrimSpace(d))
				if err != nil || n == 0 || n > 31 || n < -31 {
					return nil, fmt.Errorf("a day of the month of %q", d)
				}
				r.bymonthday = append(r.bymonthday, n)
			}
		case "BYMONTH":
			for _, m := range strings.Split(v, ",") {
				n, err := strconv.Atoi(strings.TrimSpace(m))
				if err != nil || n < 1 || n > 12 {
					return nil, fmt.Errorf("a month of %q", m)
				}
				r.bymonth = append(r.bymonth, n)
			}
		case "WKST":
			wd, ok := weekdays[strings.ToUpper(v)]
			if !ok {
				return nil, fmt.Errorf("a week start of %q", v)
			}
			r.wkst = wd
		case "BYSETPOS", "BYYEARDAY", "BYWEEKNO", "BYHOUR", "BYMINUTE", "BYSECOND":
			return nil, fmt.Errorf("a %s rule", k)
		}
	}
	if r.freq == "" {
		return nil, fmt.Errorf("a rule with no FREQ")
	}
	return r, nil
}

func parseUntil(v string) (time.Time, error) {
	if strings.HasSuffix(v, "Z") {
		return time.ParseInLocation("20060102T150405", strings.TrimSuffix(v, "Z"), time.UTC)
	}
	if len(v) == 8 {
		t, err := time.ParseInLocation("20060102", v, time.UTC)
		return t.Add(24*time.Hour - time.Second), err // the whole of that day
	}
	return time.ParseInLocation("20060102T150405", v, time.UTC)
}

const maxPeriods = 120000 // a rule that has not reached the window by then is not expanded further

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// monthDays lists the days of one month an event falls on, from BYMONTHDAY, BYDAY or the
// start day, in order.
func (r *rrule) monthDays(year int, month time.Month, startDay int) []int {
	dim := daysIn(year, month)
	set := map[int]bool{}
	switch {
	case len(r.bymonthday) > 0:
		for _, d := range r.bymonthday {
			if d < 0 {
				d = dim + d + 1
			}
			if d >= 1 && d <= dim {
				set[d] = true
			}
		}
	case len(r.byday) > 0:
		for _, dr := range r.byday {
			var matches []int
			for d := 1; d <= dim; d++ {
				if time.Date(year, month, d, 0, 0, 0, 0, time.UTC).Weekday() == dr.wd {
					matches = append(matches, d)
				}
			}
			switch {
			case dr.n == 0:
				for _, d := range matches {
					set[d] = true
				}
			case dr.n > 0 && dr.n <= len(matches):
				set[matches[dr.n-1]] = true
			case dr.n < 0 && -dr.n <= len(matches):
				set[matches[len(matches)+dr.n]] = true
			}
		}
	default:
		if startDay <= dim {
			set[startDay] = true
		}
	}
	days := make([]int, 0, len(set))
	for d := range set {
		days = append(days, d)
	}
	sort.Ints(days)
	return days
}

func (r *rrule) allowsMonth(m time.Month) bool {
	if len(r.bymonth) == 0 {
		return true
	}
	for _, x := range r.bymonth {
		if time.Month(x) == m {
			return true
		}
	}
	return false
}

// candidates are the occurrences of one period of the rule, in order, at the event's clock time.
func (r *rrule) candidates(start time.Time, k int) []time.Time {
	loc := start.Location()
	sy, sm, sd := start.Date()
	hh, mi, ss := start.Clock()
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, hh, mi, ss, 0, loc) }
	var out []time.Time
	switch r.freq {
	case "DAILY":
		d := time.Date(sy, sm, sd+k*r.interval, 0, 0, 0, 0, time.UTC)
		if !r.allowsMonth(d.Month()) {
			return nil
		}
		if len(r.bymonthday) > 0 {
			ok := false
			for _, x := range r.bymonthday {
				if x < 0 {
					x = daysIn(d.Year(), d.Month()) + x + 1
				}
				ok = ok || x == d.Day()
			}
			if !ok {
				return nil
			}
		}
		if len(r.byday) > 0 {
			ok := false
			for _, dr := range r.byday {
				ok = ok || dr.wd == d.Weekday()
			}
			if !ok {
				return nil
			}
		}
		out = append(out, at(d.Year(), d.Month(), d.Day()))
	case "WEEKLY":
		startDate := time.Date(sy, sm, sd, 0, 0, 0, 0, time.UTC)
		back := (int(startDate.Weekday()) - int(r.wkst) + 7) % 7
		weekStart := startDate.AddDate(0, 0, -back+7*r.interval*k)
		wds := r.byday
		if len(wds) == 0 {
			wds = []dayRule{{wd: startDate.Weekday()}}
		}
		var offs []int
		for _, dr := range wds {
			offs = append(offs, (int(dr.wd)-int(r.wkst)+7)%7)
		}
		sort.Ints(offs)
		last := -1
		for _, off := range offs {
			if off == last {
				continue
			}
			last = off
			d := weekStart.AddDate(0, 0, off)
			if r.allowsMonth(d.Month()) {
				out = append(out, at(d.Year(), d.Month(), d.Day()))
			}
		}
	case "MONTHLY":
		m := time.Date(sy, sm+time.Month(k*r.interval), 1, 0, 0, 0, 0, time.UTC)
		if !r.allowsMonth(m.Month()) {
			return nil
		}
		for _, d := range r.monthDays(m.Year(), m.Month(), sd) {
			out = append(out, at(m.Year(), m.Month(), d))
		}
	case "YEARLY":
		months := r.bymonth
		if len(months) == 0 {
			months = []int{int(sm)}
		}
		sorted := append([]int(nil), months...)
		sort.Ints(sorted)
		for _, mo := range sorted {
			for _, d := range r.monthDays(sy+k*r.interval, time.Month(mo), sd) {
				out = append(out, at(sy+k*r.interval, time.Month(mo), d))
			}
		}
	}
	return out
}

// starts lists the start times of an event's occurrences that begin before to, in order, stopping at
// the end of the rule. The first is the event's own start. Occurrences wholly before from are still
// walked, because a COUNT includes them, but are not returned.
func (e rawEvent) starts(from, to time.Time) []time.Time {
	dur := e.length()
	keep := func(s time.Time) bool {
		return s.Before(to) && (s.Add(dur).After(from) || (dur == 0 && !s.Before(from)))
	}
	excluded := func(s time.Time) bool {
		for _, x := range e.exdates {
			if x.Equal(s) || (e.allDay && x.Format("20060102") == s.Format("20060102")) {
				return true
			}
		}
		return false
	}
	var out []time.Time
	if e.rrule == nil {
		if keep(e.start) && !excluded(e.start) {
			out = append(out, e.start)
		}
	} else {
		r := e.rrule
		seen := 0
	periods:
		for k := 0; k < maxPeriods; k++ {
			cs := r.candidates(e.start, k)
			for _, s := range cs {
				if s.Before(e.start) {
					continue
				}
				if r.hasUntil && s.After(r.until) {
					break periods
				}
				if !s.Before(to) {
					break periods
				}
				seen++
				if keep(s) && !excluded(s) {
					out = append(out, s)
				}
				if r.count > 0 && seen >= r.count {
					break periods
				}
			}
		}
	}
	for _, s := range e.rdates {
		if keep(s) && !excluded(s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}
