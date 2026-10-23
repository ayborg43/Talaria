// Package calendar reads iCalendar (.ics) feeds, such as the secret address Google Calendar gives
// each calendar, and lists what is on them. It only reads: there is nothing here that creates, changes
// or answers an event.
package calendar

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone names work on a machine that has no zone database
)

// Event is one entry on a calendar, or one occurrence of a repeating one.
type Event struct {
	Calendar    string
	UID         string
	Summary     string
	Location    string
	Description string
	Start, End  time.Time
	AllDay      bool
	LocalID     string // the built-in calendar's id; "" for events read from elsewhere
}

// rawEvent is an event as written in the file, before repeats are expanded.
type rawEvent struct {
	uid, summary, location, description string
	start, end                          time.Time
	hasEnd                              bool
	dur                                 time.Duration
	allDay                              bool
	cancelled                           bool
	rrule                               *rrule
	exdates, rdates                     []time.Time
	recurrenceID                        time.Time
	hasRecurrenceID                     bool
	approxZone                          bool   // a time zone name that could not be found: its times are read as UTC
	localID                             string // the built-in calendar's id; "" for events read from a file
}

// parsed is one calendar file.
type parsed struct {
	events []rawEvent
	tz     *time.Location // the calendar's own zone, for times written with none
	notes  []string       // things in the file this reader could not honour
}

type prop struct {
	name   string
	params map[string]string
	value  string
}

// unfold joins continuation lines (those that begin with a space or tab) to the line before.
func unfold(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(out) > 0 {
			out[len(out)-1] += l[1:]
			continue
		}
		out = append(out, l)
	}
	return out
}

// parseProp splits "NAME;PARAM=v;PARAM2=\"v2\":value", honouring quotes in parameters.
func parseProp(line string) (prop, bool) {
	inQuote := false
	colon := -1
	for i, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ':' && !inQuote:
			colon = i
		}
		if colon >= 0 {
			break
		}
	}
	if colon < 0 {
		return prop{}, false
	}
	head, value := line[:colon], line[colon+1:]
	parts := splitUnquoted(head, ';')
	p := prop{name: strings.ToUpper(parts[0]), params: map[string]string{}, value: value}
	for _, kv := range parts[1:] {
		if k, v, ok := strings.Cut(kv, "="); ok {
			p.params[strings.ToUpper(k)] = strings.Trim(v, `"`)
		}
	}
	return p, true
}

func splitUnquoted(s string, sep rune) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == sep && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(out, cur.String())
}

// unescapeText undoes the escapes of a TEXT value.
func unescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// zoneFor finds the location a TZID names, and says whether it was found.
func zoneFor(tzid string, fallback *time.Location) (*time.Location, bool) {
	if tzid == "" {
		return fallback, true
	}
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc, true
	}
	return time.UTC, false
}

// parseTime reads a DATE or DATE-TIME value. allDay is true for a bare date. A time written with no
// zone (floating) is read in floating; one ending in Z is UTC; one with a TZID is in that zone.
func parseTime(p prop, floating *time.Location) (t time.Time, allDay, zoneFound bool, err error) {
	v := strings.TrimSpace(p.value)
	zoneFound = true
	if p.params["VALUE"] == "DATE" || (len(v) == 8 && !strings.Contains(v, "T")) {
		t, err = time.ParseInLocation("20060102", v, floating)
		return t, true, true, err
	}
	if strings.HasSuffix(v, "Z") {
		t, err = time.ParseInLocation("20060102T150405", strings.TrimSuffix(v, "Z"), time.UTC)
		return t, false, true, err
	}
	loc, found := zoneFor(p.params["TZID"], floating)
	t, err = time.ParseInLocation("20060102T150405", v, loc)
	return t, false, found, err
}

// parseDuration reads an iCalendar duration such as P1DT2H30M, PT45M or P1W.
func parseDuration(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	s = strings.TrimPrefix(s, "+")
	if !strings.HasPrefix(s, "P") {
		return 0, false
	}
	s = s[1:]
	var total time.Duration
	inTime := false
	num := ""
	for _, r := range s {
		switch {
		case r == 'T':
			inTime = true
		case r >= '0' && r <= '9':
			num += string(r)
		default:
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, false
			}
			num = ""
			switch {
			case r == 'W':
				total += time.Duration(n) * 7 * 24 * time.Hour
			case r == 'D':
				total += time.Duration(n) * 24 * time.Hour
			case r == 'H' && inTime:
				total += time.Duration(n) * time.Hour
			case r == 'M' && inTime:
				total += time.Duration(n) * time.Minute
			case r == 'S' && inTime:
				total += time.Duration(n) * time.Second
			default:
				return 0, false
			}
		}
	}
	if neg {
		total = -total
	}
	return total, true
}

// parse reads a calendar file. Parts it cannot read are skipped and noted, not fatal: a feed with
// one odd event should still show the rest.
func parse(text string, display *time.Location) (*parsed, error) {
	lines := unfold(text)
	if len(lines) == 0 || !strings.EqualFold(strings.TrimSpace(lines[0]), "BEGIN:VCALENDAR") {
		return nil, fmt.Errorf("this is not an iCalendar file (it does not begin with BEGIN:VCALENDAR)")
	}
	out := &parsed{tz: display}
	// The calendar's own zone applies to times written with no zone.
	for _, l := range lines {
		if p, ok := parseProp(l); ok && p.name == "X-WR-TIMEZONE" {
			if loc, err := time.LoadLocation(strings.TrimSpace(p.value)); err == nil {
				out.tz = loc
			}
			break
		}
	}

	var cur *rawEvent
	depth := 0 // components nested inside the event, such as alarms
	noted := map[string]bool{}
	note := func(s string) {
		if !noted[s] {
			noted[s] = true
			out.notes = append(out.notes, s)
		}
	}
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		upper := strings.ToUpper(l)
		switch {
		case upper == "BEGIN:VEVENT":
			cur, depth = &rawEvent{}, 0
			continue
		case cur != nil && strings.HasPrefix(upper, "BEGIN:"):
			depth++
			continue
		case cur != nil && strings.HasPrefix(upper, "END:") && depth > 0:
			depth--
			continue
		case upper == "END:VEVENT" && cur != nil:
			if cur.start.IsZero() {
				note("an event with no start time was skipped")
			} else {
				out.events = append(out.events, *cur)
			}
			cur = nil
			continue
		}
		if cur == nil || depth > 0 {
			continue
		}
		p, ok := parseProp(l)
		if !ok {
			continue
		}
		switch p.name {
		case "UID":
			cur.uid = strings.TrimSpace(p.value)
		case "SUMMARY":
			cur.summary = unescapeText(p.value)
		case "LOCATION":
			cur.location = unescapeText(p.value)
		case "DESCRIPTION":
			cur.description = unescapeText(p.value)
		case "STATUS":
			cur.cancelled = strings.EqualFold(strings.TrimSpace(p.value), "CANCELLED")
		case "DTSTART":
			t, allDay, found, err := parseTime(p, out.tz)
			if err != nil {
				note("an event with a start time that could not be read was skipped")
				continue
			}
			cur.start, cur.allDay = t, allDay
			if !found {
				cur.approxZone = true
				note("some times use a time zone name this reader does not know (read as UTC): " + p.params["TZID"])
			}
		case "DTEND":
			if t, _, _, err := parseTime(p, out.tz); err == nil {
				cur.end, cur.hasEnd = t, true
			}
		case "DURATION":
			if d, ok := parseDuration(p.value); ok {
				cur.dur = d
			}
		case "RRULE":
			r, err := parseRRule(p.value)
			if err != nil {
				note("a repeating event with a rule that could not be read is shown once: " + err.Error())
			} else {
				cur.rrule = r
			}
		case "EXDATE", "RDATE":
			for _, v := range strings.Split(p.value, ",") {
				q := prop{name: p.name, params: p.params, value: v}
				if t, _, _, err := parseTime(q, out.tz); err == nil {
					if p.name == "EXDATE" {
						cur.exdates = append(cur.exdates, t)
					} else {
						cur.rdates = append(cur.rdates, t)
					}
				}
			}
		case "RECURRENCE-ID":
			if t, _, _, err := parseTime(p, out.tz); err == nil {
				cur.recurrenceID, cur.hasRecurrenceID = t, true
			}
		}
	}
	return out, nil
}

// length is how long the event runs.
func (e rawEvent) length() time.Duration {
	switch {
	case e.hasEnd && e.end.After(e.start):
		return e.end.Sub(e.start)
	case e.dur > 0:
		return e.dur
	case e.allDay:
		return 24 * time.Hour
	}
	return 0
}
