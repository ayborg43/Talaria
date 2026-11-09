package calendar

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/tool"
)

func writeService(t *testing.T) (*Service, *Store) {
	t.Helper()
	loc, _ := time.LoadLocation("Africa/Lagos")
	store := &Store{Path: filepath.Join(t.TempDir(), "calendar.json")}
	s := &Service{Local: store, Display: loc, Now: func() time.Time {
		return time.Date(2026, 10, 22, 8, 0, 0, 0, loc)
	}}
	return s, store
}

func runTool(t *testing.T, r *tool.Registry, name, args string) string {
	t.Helper()
	tl, ok := r.Get(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	out, err := tl.Run(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

func TestCalendarWriteToolsAddUpdateAndDeleteLocalEvents(t *testing.T) {
	s, store := writeService(t)
	r := tool.NewRegistry()
	RegisterWriteTools(r, s, nil)

	for _, name := range []string{"calendar_add", "calendar_update", "calendar_delete"} {
		tl, ok := r.Get(name)
		if !ok || !tl.NeedsApproval {
			t.Fatalf("%s must be registered and ask first: %+v", name, tl)
		}
	}
	if tl, _ := r.Get("calendar_delete"); tl.RiskOf(nil) == 0 {
		t.Fatal("deleting an event must count as a delete")
	}

	out := runTool(t, r, "calendar_add", `{"title":"Dentist","start":"2026-10-23 15:00","end":"2026-10-23 16:00","location":"Clinic","rrule":"FREQ=WEEKLY"}`)
	if !strings.Contains(out, "Dentist") || !strings.Contains(out, "id ") {
		t.Fatalf("add did not confirm with an id:\n%s", out)
	}
	events, _ := store.List()
	if len(events) != 1 || events[0].Title != "Dentist" || events[0].RRule != "FREQ=WEEKLY" {
		t.Fatalf("stored: %+v", events)
	}
	id := events[0].ID
	if events[0].Start.Hour() != 15 || events[0].Start.UTC().Hour() != 14 {
		t.Fatalf("start not read in the display zone (WAT): %v", events[0].Start)
	}

	out = runTool(t, r, "calendar_update", `{"id":"`+id+`","title":"Dentist (check-up)","start":"2026-10-23 16:30"}`)
	if !strings.Contains(out, "Dentist (check-up)") {
		t.Fatalf("update:\n%s", out)
	}
	got, _ := store.Get(id)
	if got.Title != "Dentist (check-up)" || got.Start.Hour() != 16 || got.End.Hour() != 17 || got.RRule != "FREQ=WEEKLY" {
		t.Fatalf("update did not merge and keep the end's length: %+v", got)
	}

	// One occurrence of the weekly event can go without touching the rest.
	occ := got.Start.AddDate(0, 0, 7).Format("2006-01-02 15:04")
	runTool(t, r, "calendar_delete", `{"id":"`+id+`","occurrence":"`+occ+`"}`)
	got, _ = store.Get(id)
	if len(got.ExDates) != 1 {
		t.Fatalf("occurrence not removed: %+v", got)
	}

	runTool(t, r, "calendar_delete", `{"id":"`+id+`"}`)
	if all, _ := store.List(); len(all) != 0 {
		t.Fatalf("event not deleted: %+v", all)
	}
	if _, err := func() (string, error) {
		tl, _ := r.Get("calendar_add")
		return tl.Run(context.Background(), json.RawMessage(`{"title":"x"}`))
	}(); err == nil {
		t.Fatal("an add with no start was accepted")
	}
}

func TestCalendarAddSchedulesAndReplacesAReminder(t *testing.T) {
	s, store := writeService(t)
	var added []LocalEvent
	var removed []string
	bind := &ReminderBinder{
		Add: func(e LocalEvent) (string, error) {
			added = append(added, e)
			return "job-1", nil
		},
		Remove: func(jobID string) error { removed = append(removed, jobID); return nil },
	}
	r := tool.NewRegistry()
	RegisterWriteTools(r, s, bind)
	out := runTool(t, r, "calendar_add", `{"title":"Flight","start":"2026-10-24 09:00","remind_mins":30}`)
	if !strings.Contains(out, "reminder") {
		t.Fatalf("add did not mention the reminder:\n%s", out)
	}
	events, _ := store.List()
	if len(added) != 1 || events[0].RemindJob != "job-1" {
		t.Fatalf("reminder not bound: added=%+v stored=%+v", added, events)
	}
	runTool(t, r, "calendar_delete", `{"id":"`+events[0].ID+`"}`)
	if len(removed) != 1 || removed[0] != "job-1" {
		t.Fatalf("deleting did not cancel the reminder: %v", removed)
	}
}

func TestAgendaShowsLocalEventIDs(t *testing.T) {
	s, _ := writeService(t)
	r := tool.NewRegistry()
	RegisterWriteTools(r, s, nil)
	runTool(t, r, "calendar_add", `{"title":"Standup","start":"2026-10-22 09:00"}`)
	out, err := s.Agenda(context.Background(), "today", 1, "", "")
	if err != nil || !strings.Contains(out, "Standup") || !strings.Contains(out, "id ") {
		t.Fatalf("agenda gives the model no id to act on: %v\n%s", err, out)
	}
}
