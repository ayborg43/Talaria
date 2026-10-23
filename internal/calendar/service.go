package calendar

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Service reads the configured calendars and lists what is on them.
type Service struct {
	Sources []Source
	// Display is the zone times are shown in and days are counted in.
	Display *time.Location
	Now     func() time.Time
	// Client builds the HTTP client for a source; nil uses safeClient. Tests replace it.
	Client func(Source) *http.Client
	// TTL is how long a downloaded feed is reused (default five minutes).
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	text string
	at   time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) zone() *time.Location {
	if s.Display != nil {
		return s.Display
	}
	return time.UTC
}

func (s *Service) feed(ctx context.Context, src Source) (string, error) {
	ttl := s.TTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	s.mu.Lock()
	if c, ok := s.cache[src.Name]; ok && s.now().Sub(c.at) < ttl {
		s.mu.Unlock()
		return c.text, nil
	}
	s.mu.Unlock()
	hc := safeClient(src.AllowPrivate)
	if s.Client != nil {
		hc = s.Client(src)
	}
	text, err := fetch(ctx, hc, src)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	s.cache[src.Name] = cached{text, s.now()}
	s.mu.Unlock()
	return text, nil
}

// Names lists the calendars.
func (s *Service) Names() []string {
	var out []string
	for _, src := range s.Sources {
		out = append(out, src.Name)
	}
	return out
}

// toDisplay moves a date-only event onto the display zone's midnights, so that a whole-day event
// is on the same days for everyone whatever zone the file was written in.
func toDisplay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// Events lists what is on, within [from, to), of one calendar (name, if not empty) and matching
// query (a word in the title, place or notes, if not empty). notes says what could not be read.
func (s *Service) Events(ctx context.Context, from, to time.Time, name, query string) (events []Event, notes []string, err error) {
	if len(s.Sources) == 0 {
		return nil, nil, fmt.Errorf("no calendar is set up")
	}
	display := s.zone()
	chosen := 0
	failed := 0
	for _, src := range s.Sources {
		if name != "" && !strings.EqualFold(src.Name, name) {
			continue
		}
		chosen++
		text, ferr := s.feed(ctx, src)
		if ferr != nil {
			failed++
			notes = append(notes, fmt.Sprintf("the calendar %q could not be read: %v", src.Name, ferr))
			continue
		}
		p, perr := parse(text, display)
		if perr != nil {
			failed++
			notes = append(notes, fmt.Sprintf("the calendar %q could not be read: %v", src.Name, perr))
			continue
		}
		for _, n := range p.notes {
			notes = append(notes, fmt.Sprintf("%s: %s", src.Name, n))
		}
		events = append(events, expand(p, src.Name, from, to, display)...)
	}
	if name != "" && chosen == 0 {
		return nil, nil, fmt.Errorf("there is no calendar named %q (calendars: %s)", name, strings.Join(s.Names(), ", "))
	}
	if chosen > 0 && failed == chosen {
		return nil, notes, fmt.Errorf("%s", strings.Join(notes, "; "))
	}
	if q := strings.ToLower(strings.TrimSpace(query)); q != "" {
		keep := events[:0]
		for _, e := range events {
			if strings.Contains(strings.ToLower(e.Summary+"\n"+e.Location+"\n"+e.Description), q) {
				keep = append(keep, e)
			}
		}
		events = keep
	}
	slices.SortStableFunc(events, func(a, b Event) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return strings.Compare(a.Summary, b.Summary)
	})
	return events, notes, nil
}

// expand turns the events of one file into the occurrences within the window.
func expand(p *parsed, calendar string, from, to time.Time, display *time.Location) []Event {
	raws := make([]rawEvent, len(p.events))
	copy(raws, p.events)
	for i := range raws {
		e := &raws[i]
		if e.allDay {
			dur := e.length()
			e.start = toDisplay(e.start, display)
			e.end, e.hasEnd = e.start.Add(dur), true
			for j := range e.exdates {
				e.exdates[j] = toDisplay(e.exdates[j], display)
			}
			for j := range e.rdates {
				e.rdates[j] = toDisplay(e.rdates[j], display)
			}
			if e.hasRecurrenceID {
				e.recurrenceID = toDisplay(e.recurrenceID, display)
			}
		}
	}
	// A changed occurrence of a repeating event is its own entry with a RECURRENCE-ID, and replaces the
	// occurrence it names.
	replaced := map[string]bool{}
	for _, e := range raws {
		if e.hasRecurrenceID {
			replaced[e.uid+"|"+e.recurrenceID.UTC().Format(time.RFC3339)] = true
		}
	}
	var out []Event
	for _, e := range raws {
		if e.cancelled {
			continue
		}
		length := e.length()
		for _, st := range e.starts(from, to) {
			if !e.hasRecurrenceID && e.uid != "" && replaced[e.uid+"|"+st.UTC().Format(time.RFC3339)] {
				continue
			}
			out = append(out, Event{
				Calendar: calendar, UID: e.uid, Summary: e.summary, Location: e.location, Description: e.description,
				Start: st.In(display), End: st.Add(length).In(display), AllDay: e.allDay,
			})
		}
	}
	return out
}
