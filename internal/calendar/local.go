package calendar

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"talaria/internal/filelock"
)

// LocalCalendar is the name the built-in calendar shows under, next to any
// calendars read from elsewhere.
const LocalCalendar = "Talaria"

const (
	maxLocalEvents = 1000
	maxTitle       = 200
	maxPlace       = 200
	maxNotes       = 2000
	maxRemindMins  = 1440
)

// LocalEvent is one event on the built-in calendar. Times carry their zone;
// all-day events are stored as UTC midnights. RRule is "" or an RFC 5545
// recurrence rule such as "FREQ=WEEKLY;BYDAY=MO,WE". ExDates are the exact
// starts of deleted occurrences of a repeating event.
type LocalEvent struct {
	ID         string      `json:"id"`
	UID        string      `json:"uid"`
	Title      string      `json:"title"`
	Location   string      `json:"location,omitempty"`
	Notes      string      `json:"notes,omitempty"`
	Start      time.Time   `json:"start"`
	End        time.Time   `json:"end"`
	AllDay     bool        `json:"all_day,omitempty"`
	RRule      string      `json:"rrule,omitempty"`
	ExDates    []time.Time `json:"exdates,omitempty"`
	RemindMins int         `json:"remind_mins,omitempty"` // 0 sends no reminder
	NotifyGate string      `json:"notify_gateway,omitempty"`
	NotifyChat int64       `json:"notify_chat,omitempty"`
	Created    time.Time   `json:"created"`
	Updated    time.Time   `json:"updated"`
}

// Store keeps the built-in calendar's events in one JSON file. It is safe for
// use from many goroutines and processes: the CLI and the gateway cannot
// overwrite each other.
type Store struct {
	Path string
	Now  func() time.Time // defaults to time.Now; tests override

	mu sync.Mutex
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) lockFile() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return nil, err
	}
	return filelock.Lock(s.Path)
}

func (s *Store) load() ([]LocalEvent, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f struct {
		Events []LocalEvent `json:"events"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt (fix or delete it): %w", s.Path, err)
	}
	return f.Events, nil
}

func (s *Store) save(events []LocalEvent) error {
	raw, err := json.MarshalIndent(struct {
		Events []LocalEvent `json:"events"`
	}{events}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "calendar-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil { // a calendar is private
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

func newLocalID(existing []LocalEvent) string {
	for {
		var b [4]byte
		rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		taken := false
		for _, e := range existing {
			taken = taken || e.ID == id
		}
		if !taken {
			return id
		}
	}
}

// normalize fills in defaults and reports what is wrong with an event. It
// runs on both new and edited events, so an edit cannot smuggle in what an
// add would refuse.
func normalize(e *LocalEvent) error {
	e.Title = strings.TrimSpace(e.Title)
	if e.Title == "" {
		e.Title = "(no title)"
	}
	if r := []rune(e.Title); len(r) > maxTitle {
		return fmt.Errorf("the title is longer than %d characters", maxTitle)
	}
	e.Location = strings.TrimSpace(e.Location)
	if r := []rune(e.Location); len(r) > maxPlace {
		return fmt.Errorf("the place is longer than %d characters", maxPlace)
	}
	e.Notes = strings.TrimSpace(e.Notes)
	if r := []rune(e.Notes); len(r) > maxNotes {
		return fmt.Errorf("the notes are longer than %d characters", maxNotes)
	}
	e.RRule = strings.TrimSpace(e.RRule)
	if e.RRule != "" {
		if _, err := parseRRule(e.RRule); err != nil {
			return fmt.Errorf("the repeat rule is not valid: %v", err)
		}
	}
	if e.RemindMins < 0 || e.RemindMins > maxRemindMins {
		return fmt.Errorf("the reminder must be between 0 and %d minutes before", maxRemindMins)
	}
	if e.AllDay {
		y, m, d := e.Start.Date()
		e.Start = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		if e.End.IsZero() {
			e.End = e.Start.AddDate(0, 0, 1)
		} else {
			y, m, d := e.End.Date()
			e.End = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		}
		if !e.End.After(e.Start) {
			return fmt.Errorf("an all-day event must end after the day it starts")
		}
		return nil
	}
	if e.Start.IsZero() {
		return fmt.Errorf("a timed event needs a start")
	}
	if e.End.IsZero() {
		e.End = e.Start.Add(time.Hour)
	}
	if e.End.Before(e.Start) {
		return fmt.Errorf("an event cannot end before it starts")
	}
	return nil
}

// Add files a new event, returning it with its id and stamps.
func (s *Store) Add(e LocalEvent) (LocalEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockFile()
	if err != nil {
		return LocalEvent{}, err
	}
	defer unlock()
	events, err := s.load()
	if err != nil {
		return LocalEvent{}, err
	}
	if len(events) >= maxLocalEvents {
		return LocalEvent{}, fmt.Errorf("the calendar holds %d events already", maxLocalEvents)
	}
	if err := normalize(&e); err != nil {
		return LocalEvent{}, err
	}
	e.ID = newLocalID(events)
	e.UID = e.ID + "@talaria.local"
	e.Created, e.Updated = s.now(), s.now()
	events = append(events, e)
	if err := s.save(events); err != nil {
		return LocalEvent{}, err
	}
	return e, nil
}

// Update replaces the event with the same id, keeping its id, uid and stamps' creation.
func (s *Store) Update(e LocalEvent) (LocalEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockFile()
	if err != nil {
		return LocalEvent{}, err
	}
	defer unlock()
	events, err := s.load()
	if err != nil {
		return LocalEvent{}, err
	}
	at := -1
	for i := range events {
		if events[i].ID == e.ID {
			at = i
		}
	}
	if at < 0 {
		return LocalEvent{}, fmt.Errorf("there is no event %q", e.ID)
	}
	if err := normalize(&e); err != nil {
		return LocalEvent{}, err
	}
	e.UID = events[at].UID
	e.Created = events[at].Created
	e.Updated = s.now()
	// An edit that stops the repeats drops the exceptions with them.
	if e.RRule == "" {
		e.ExDates = nil
	}
	events[at] = e
	if err := s.save(events); err != nil {
		return LocalEvent{}, err
	}
	return e, nil
}

// Delete removes an event, repeats and all.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockFile()
	if err != nil {
		return err
	}
	defer unlock()
	events, err := s.load()
	if err != nil {
		return err
	}
	keep := events[:0]
	found := false
	for _, e := range events {
		if e.ID == id {
			found = true
			continue
		}
		keep = append(keep, e)
	}
	if !found {
		return fmt.Errorf("there is no event %q", id)
	}
	return s.save(keep)
}

// DeleteOccurrence removes one occurrence of a repeating event, keeping the
// rest. The start must be the exact start of an occurrence.
func (s *Store) DeleteOccurrence(id string, start time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockFile()
	if err != nil {
		return err
	}
	defer unlock()
	events, err := s.load()
	if err != nil {
		return err
	}
	at := -1
	for i := range events {
		if events[i].ID == id {
			at = i
		}
	}
	if at < 0 {
		return fmt.Errorf("there is no event %q", id)
	}
	e := events[at]
	if e.RRule == "" {
		return fmt.Errorf("%q does not repeat: delete it instead", e.Title)
	}
	raw, err := e.raw()
	if err != nil {
		return err
	}
	occurs := false
	for _, st := range raw.starts(start.Add(-time.Minute), start.Add(time.Minute)) {
		if st.Equal(start) {
			occurs = true
		}
	}
	if !occurs {
		return fmt.Errorf("nothing of %q starts at %s", e.Title, start.Format("Mon 2 Jan 2006 15:04"))
	}
	for _, x := range e.ExDates {
		if x.Equal(start) {
			return fmt.Errorf("that occurrence is already deleted")
		}
	}
	e.ExDates = append(e.ExDates, start.UTC())
	e.Updated = s.now()
	events[at] = e
	return s.save(events)
}

// Get returns one event by id.
func (s *Store) Get(id string) (LocalEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.load()
	if err != nil {
		return LocalEvent{}, err
	}
	for _, e := range events {
		if e.ID == id {
			return e, nil
		}
	}
	return LocalEvent{}, fmt.Errorf("there is no event %q", id)
}

// List returns every event, oldest first.
func (s *Store) List() ([]LocalEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// raw turns a stored event into the shape repeats are expanded from: the same
// machinery that reads calendar files.
func (e LocalEvent) raw() (rawEvent, error) {
	r := rawEvent{
		uid: e.UID, summary: e.Title, location: e.Location, description: e.Notes,
		start: e.Start, end: e.End, hasEnd: true, allDay: e.AllDay, localID: e.ID,
	}
	if e.RRule != "" {
		rr, err := parseRRule(e.RRule)
		if err != nil {
			return rawEvent{}, err
		}
		r.rrule = rr
	}
	r.exdates = append([]time.Time(nil), e.ExDates...)
	return r, nil
}

// Occurrences expands the calendar's events into what falls within [from, to),
// in the display zone.
func (s *Store) Occurrences(from, to time.Time, display *time.Location) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.load()
	if err != nil {
		return nil, err
	}
	p := &parsed{}
	for _, e := range events {
		r, err := e.raw()
		if err != nil {
			return nil, fmt.Errorf("event %q: %v", e.ID, err)
		}
		p.events = append(p.events, r)
	}
	return expand(p, LocalCalendar, from, to, display), nil
}
