package calendar

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func localStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "calendar.json")}
}

func TestBuiltInEventsAreKeptAndExpanded(t *testing.T) {
	s := localStore(t)
	lagos := zone(t, "Africa/Lagos")
	start := time.Date(2026, 10, 26, 9, 0, 0, 0, lagos) // a Monday
	added, err := s.Add(LocalEvent{
		Title: "Standup", Location: "Zoom", Start: start, End: start.Add(time.Hour),
		RRule: "FREQ=WEEKLY;BYDAY=MO,WE", RemindMins: 15,
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added.ID == "" || added.UID == "" || added.Created.IsZero() {
		t.Fatalf("missing stamps: %+v", added)
	}
	got, err := s.Get(added.ID)
	if err != nil || got.Title != "Standup" || got.RemindMins != 15 {
		t.Fatalf("get: %+v %v", got, err)
	}
	// A new store over the same file sees it: it survives restarts.
	again, err := (&Store{Path: s.Path}).Get(added.ID)
	if err != nil || again.Title != "Standup" {
		t.Fatalf("after reopening: %+v %v", again, err)
	}
	// Six Mondays and Wednesdays in three weeks, each carrying the local id.
	from := time.Date(2026, 10, 26, 0, 0, 0, 0, lagos)
	occ, err := s.Occurrences(from, from.AddDate(0, 0, 21), lagos)
	if err != nil {
		t.Fatalf("occurrences: %v", err)
	}
	if len(occ) != 6 {
		t.Fatalf("%d occurrences, want 6", len(occ))
	}
	for _, e := range occ {
		if e.LocalID != added.ID || e.Calendar != LocalCalendar || e.Summary != "Standup" {
			t.Fatalf("%+v", e)
		}
	}
	if !occ[0].Start.Equal(start) {
		t.Fatalf("first occurrence at %s", occ[0].Start)
	}
}

func TestBuiltInEventsRefuseNonsense(t *testing.T) {
	s := localStore(t)
	lagos := zone(t, "Africa/Lagos")
	start := time.Date(2026, 10, 26, 9, 0, 0, 0, lagos)
	for name, e := range map[string]LocalEvent{
		"a repeat rule that is not one": {Title: "x", Start: start, RRule: "FREQ=NEVER"},
		"an end before the start":       {Title: "x", Start: start, End: start.Add(-time.Hour)},
		"a reminder past a day out":     {Title: "x", Start: start, RemindMins: 2000},
	} {
		if _, err := s.Add(e); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := s.Add(LocalEvent{Title: "x", Start: start, End: start}); err != nil {
		t.Errorf("an event ending when it starts: %v", err)
	}
	allday, err := s.Add(LocalEvent{Title: "holiday", Start: start, AllDay: true})
	if err != nil {
		t.Fatalf("all day: %v", err)
	}
	if allday.End.Sub(allday.Start) != 24*time.Hour {
		t.Fatalf("an all-day event lasts %s", allday.End.Sub(allday.Start))
	}
}

func TestOneOccurrenceCanGoWhileTheRestStay(t *testing.T) {
	s := localStore(t)
	lagos := zone(t, "Africa/Lagos")
	start := time.Date(2026, 10, 26, 9, 0, 0, 0, lagos)
	added, err := s.Add(LocalEvent{Title: "Standup", Start: start, RRule: "FREQ=DAILY;COUNT=3"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	second := start.AddDate(0, 0, 1)
	if err := s.DeleteOccurrence(added.ID, second); err != nil {
		t.Fatalf("delete one: %v", err)
	}
	if err := s.DeleteOccurrence(added.ID, second); err == nil {
		t.Fatal("deleting it twice worked")
	}
	if err := s.DeleteOccurrence(added.ID, start.AddDate(0, 0, 9)); err == nil {
		t.Fatal("deleting a day with nothing on worked")
	}
	from := time.Date(2026, 10, 26, 0, 0, 0, 0, lagos)
	occ, err := s.Occurrences(from, from.AddDate(0, 0, 7), lagos)
	if err != nil {
		t.Fatalf("occurrences: %v", err)
	}
	if len(occ) != 2 || occ[0].Start.Equal(second) || occ[1].Start.Equal(second) {
		t.Fatalf("%v", occ)
	}
	// Editing an event that stops repeating drops the exceptions with them.
	added.RRule = ""
	if _, err := s.Update(added); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := s.Get(added.ID)
	if len(got.ExDates) != 0 {
		t.Fatalf("exceptions stayed: %v", got.ExDates)
	}
	if err := s.Delete(added.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(added.ID); err == nil {
		t.Fatal("it is still there")
	}
}

func TestTheBuiltInCalendarSitsNextToTheOthers(t *testing.T) {
	srv, _ := feedServer(t, week)
	s := service(t, srv, "2026-10-22 08:00")
	s.Local = localStore(t)
	lagos := zone(t, "Africa/Lagos")
	start := time.Date(2026, 10, 22, 12, 0, 0, 0, lagos)
	if _, err := s.Local.Add(LocalEvent{Title: "Lunch with Ada", Start: start}); err != nil {
		t.Fatalf("add: %v", err)
	}
	ctx := context.Background()
	from := time.Date(2026, 10, 22, 0, 0, 0, 0, lagos)
	events, _, err := s.Events(ctx, from, from.AddDate(0, 0, 1), "", "")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var titles []string
	for _, e := range events {
		titles = append(titles, e.Summary+" ["+e.Calendar+"]")
	}
	joined := strings.Join(titles, "|")
	if !strings.Contains(joined, "Standup [personal]") || !strings.Contains(joined, "Lunch with Ada [Talaria]") {
		t.Fatalf("one side is missing: %s", joined)
	}
	only, _, err := s.Events(ctx, from, from.AddDate(0, 0, 1), "talaria", "")
	if err != nil || len(only) != 1 || only[0].LocalID == "" {
		t.Fatalf("filtering to the built-in calendar: %v %+v", err, only)
	}
	bare := &Service{}
	if _, _, err := bare.Events(ctx, from, from.AddDate(0, 0, 1), "", ""); err == nil {
		t.Fatal("no calendars at all should still be an error")
	}
}
