package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"talaria/internal/tool"
)

// ReminderBinder schedules and cancels the one-shot reminder job for an
// event, in whatever gateway and chat the agent writing it belongs to. The
// calendar itself knows nothing of jobs: nil means no reminder is scheduled,
// and the tool says so rather than pretending.
type ReminderBinder struct {
	Add    func(e LocalEvent) (string, error)
	Remove func(jobID string) error
}

// RegisterWriteTools gives the agent calendar_add, calendar_update and
// calendar_delete over the built-in calendar. Every write asks first. Events
// on calendars read from elsewhere cannot be changed here; the CalDAV writer
// (caldav.go) serves those by name when one is configured for writing.
func RegisterWriteTools(r *tool.Registry, s *Service, remind *ReminderBinder) {
	if s == nil || (s.Local == nil && len(s.DAV) == 0) {
		return
	}
	w := &writer{s: s, remind: remind}
	r.Register(tool.Tool{
		Name:          "calendar_add",
		Description:   "Add an event to the user's built-in calendar (named Talaria). Asks first. Give title, start (such as 2026-10-24 15:00, a full RFC 3339 time, or a date 2026-10-24 for all day), optional end, location, notes, rrule (such as FREQ=WEEKLY) and remind_mins (minutes before to remind, needs a reminder binder). Returns the event's id, which calendar_update and calendar_delete take.",
		Schema:        json.RawMessage(`{"type":"object","properties":{"calendar":{"type":"string","description":"calendar name; empty is the built-in Talaria calendar"},"title":{"type":"string"},"start":{"type":"string"},"end":{"type":"string"},"location":{"type":"string"},"notes":{"type":"string"},"rrule":{"type":"string"},"remind_mins":{"type":"integer"}},"required":["title","start"]}`),
		NeedsApproval: true,
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a eventArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			return w.add(context.Background(), a)
		},
	})
	r.Register(tool.Tool{
		Name:          "calendar_update",
		Description:   "Change an event on the built-in calendar by its id (from calendar_events or calendar_add). Asks first. Only the fields given change; moving the start keeps the event's length unless end is given too.",
		Schema:        json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"calendar":{"type":"string","description":"calendar name; empty is the built-in Talaria calendar"},"title":{"type":"string"},"start":{"type":"string"},"end":{"type":"string"},"location":{"type":"string"},"notes":{"type":"string"},"rrule":{"type":"string"},"remind_mins":{"type":"integer"}},"required":["id"]}`),
		NeedsApproval: true,
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a eventArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			return w.update(context.Background(), a)
		},
	})
	r.Register(tool.Tool{
		Name:          "calendar_delete",
		Description:   "Delete an event from the built-in calendar by its id. Asks first, and counts as a delete. With occurrence (a start time) only that one occurrence of a repeating event goes.",
		Schema:        json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"uid":{"type":"string"},"calendar":{"type":"string"},"occurrence":{"type":"string"}},"required":["id"]}`),
		NeedsApproval: true,
		Risk:          tool.RiskDeletes,
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				ID         string `json:"id"`
				UID        string `json:"uid"`
				Calendar   string `json:"calendar"`
				Occurrence string `json:"occurrence"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			return w.delete(context.Background(), a.ID, a.UID, a.Calendar, a.Occurrence)
		},
	})
}

type eventArgs struct {
	ID         *string `json:"id"`
	UID        *string `json:"uid"`
	Calendar   *string `json:"calendar"`
	Title      *string `json:"title"`
	Start      *string `json:"start"`
	End        *string `json:"end"`
	Location   *string `json:"location"`
	Notes      *string `json:"notes"`
	RRule      *string `json:"rrule"`
	RemindMins *int    `json:"remind_mins"`
}

// davFor resolves the calendar a write names: the built-in one (nil DAV
// client), a configured CalDAV writer, or an error saying why it cannot be
// written.
func (w *writer) davFor(name string) (*DAVClient, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, LocalCalendar) {
		return nil, LocalCalendar, nil
	}
	for n, c := range w.s.DAV {
		if strings.EqualFold(n, name) {
			return c, n, nil
		}
	}
	return nil, "", fmt.Errorf("calendar %q cannot be written: only the built-in calendar (%s) and calendars configured with caldav_url can be changed", name, LocalCalendar)
}

type writer struct {
	s      *Service
	remind *ReminderBinder
}

func (w *writer) parseTime(v string) (time.Time, bool, error) {
	v = strings.TrimSpace(v)
	loc := w.s.zone()
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, false, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", v, loc); err == nil {
		return t, true, nil
	}
	return time.Time{}, false, fmt.Errorf("%q is not a time: write 2026-10-24 15:00, a full RFC 3339 time, or a date", v)
}

func (w *writer) confirm(verb string, e LocalEvent, extra string) string {
	when := e.Start.In(w.s.zone()).Format("Mon 2 Jan 2006 15:04")
	if e.AllDay {
		when = e.Start.Format("Mon 2 Jan 2006") + " (all day)"
	}
	out := fmt.Sprintf("%s %q on %s (id %s)", verb, e.Title, when, e.ID)
	if e.Location != "" {
		out += " at " + e.Location
	}
	if e.RRule != "" {
		out += ", repeating " + e.RRule
	}
	if e.RemindMins > 0 {
		out += fmt.Sprintf(", reminder %d minutes before", e.RemindMins)
	}
	return out + extra + "\n"
}

// bindReminder schedules (or reschedules) an event's reminder, keeping the
// stored job id on the event so an update or delete can cancel it.
func (w *writer) bindReminder(e LocalEvent, old LocalEvent) (LocalEvent, string) {
	if w.remind == nil || w.remind.Add == nil {
		if e.RemindMins > 0 {
			return e, " No reminder was scheduled: reminders are not set up for this chat."
		}
		return e, ""
	}
	unchanged := e.RemindMins == old.RemindMins && e.Start.Equal(old.Start) && old.RemindJob != ""
	if unchanged {
		e.RemindJob = old.RemindJob
		return e, ""
	}
	if old.RemindJob != "" && w.remind.Remove != nil {
		_ = w.remind.Remove(old.RemindJob)
		e.RemindJob = ""
	}
	if e.RemindMins <= 0 {
		return e, ""
	}
	jobID, err := w.remind.Add(e)
	if err != nil {
		return e, " The reminder could not be scheduled: " + err.Error()
	}
	e.RemindJob = jobID
	if saved, err := w.s.Local.Update(e); err == nil {
		e = saved
	}
	return e, ""
}

func (w *writer) add(ctx context.Context, a eventArgs) (string, error) {
	if a.Calendar != nil {
		if dav, name, err := w.davFor(*a.Calendar); err != nil {
			return "", err
		} else if dav != nil {
			return w.addDAV(ctx, dav, name, a)
		}
	}
	if w.s.Local == nil {
		return "", fmt.Errorf("the built-in calendar is not set up")
	}
	if a.Title == nil || strings.TrimSpace(*a.Title) == "" {
		return "", fmt.Errorf("give the event a title")
	}
	if a.Start == nil || strings.TrimSpace(*a.Start) == "" {
		return "", fmt.Errorf("give the event a start")
	}
	start, allDay, err := w.parseTime(*a.Start)
	if err != nil {
		return "", err
	}
	e := LocalEvent{Title: *a.Title, Start: start, AllDay: allDay}
	if a.End != nil && strings.TrimSpace(*a.End) != "" {
		end, _, err := w.parseTime(*a.End)
		if err != nil {
			return "", err
		}
		e.End = end
	}
	if a.Location != nil {
		e.Location = *a.Location
	}
	if a.Notes != nil {
		e.Notes = *a.Notes
	}
	if a.RRule != nil {
		e.RRule = *a.RRule
	}
	if a.RemindMins != nil {
		e.RemindMins = *a.RemindMins
	}
	saved, err := w.s.Local.Add(e)
	if err != nil {
		return "", err
	}
	saved, note := w.bindReminder(saved, LocalEvent{})
	return w.confirm("Added", saved, note), nil
}

func (w *writer) updateLocal(a eventArgs) (string, error) {
	if a.ID == nil || strings.TrimSpace(*a.ID) == "" {
		return "", fmt.Errorf("give the event's id")
	}
	if w.s.Local == nil {
		return "", fmt.Errorf("the built-in calendar is not set up")
	}
	old, err := w.s.Local.Get(strings.TrimSpace(*a.ID))
	if err != nil {
		return "", err
	}
	e := old
	length := e.End.Sub(e.Start)
	moved := false
	if a.Title != nil {
		e.Title = *a.Title
	}
	if a.Start != nil && strings.TrimSpace(*a.Start) != "" {
		start, allDay, err := w.parseTime(*a.Start)
		if err != nil {
			return "", err
		}
		e.Start, e.AllDay, moved = start, allDay, true
	}
	if a.End != nil && strings.TrimSpace(*a.End) != "" {
		end, _, err := w.parseTime(*a.End)
		if err != nil {
			return "", err
		}
		e.End = end
	} else if moved && !e.AllDay {
		e.End = e.Start.Add(length)
	}
	if a.Location != nil {
		e.Location = *a.Location
	}
	if a.Notes != nil {
		e.Notes = *a.Notes
	}
	if a.RRule != nil {
		e.RRule = *a.RRule
	}
	if a.RemindMins != nil {
		e.RemindMins = *a.RemindMins
	}
	saved, err := w.s.Local.Update(e)
	if err != nil {
		return "", err
	}
	saved, note := w.bindReminder(saved, old)
	return w.confirm("Updated", saved, note), nil
}

func (w *writer) delete(ctx context.Context, id, uid, calendarName, occurrence string) (string, error) {
	id, occurrence = strings.TrimSpace(id), strings.TrimSpace(occurrence)
	if calendarName != "" || uid != "" {
		if dav, name, err := w.davFor(calendarName); err != nil {
			return "", err
		} else if dav != nil {
			return w.deleteDAV(ctx, dav, name, uid, occurrence)
		}
	}
	if id == "" {
		return "", fmt.Errorf("give the event's id")
	}
	if w.s.Local == nil {
		return "", fmt.Errorf("the built-in calendar is not set up")
	}
	e, err := w.s.Local.Get(id)
	if err != nil {
		return "", err
	}
	if occurrence != "" {
		at, _, err := w.parseTime(occurrence)
		if err != nil {
			return "", err
		}
		if err := w.s.Local.DeleteOccurrence(id, at); err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted the %s occurrence of %q; the rest stays.\n", at.In(w.s.zone()).Format("Mon 2 Jan 2006 15:04"), e.Title), nil
	}
	if e.RemindJob != "" && w.remind != nil && w.remind.Remove != nil {
		_ = w.remind.Remove(e.RemindJob)
	}
	if err := w.s.Local.Delete(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted %q (id %s).\n", e.Title, e.ID), nil
}

// --- CalDAV writes ---

func (w *writer) rawFromArgs(a eventArgs, base rawEvent) (rawEvent, error) {
	e := base
	if a.Title != nil {
		e.summary = *a.Title
	}
	if a.Start != nil && strings.TrimSpace(*a.Start) != "" {
		start, allDay, err := w.parseTime(*a.Start)
		if err != nil {
			return e, err
		}
		e.start, e.allDay = start, allDay
	}
	if a.End != nil && strings.TrimSpace(*a.End) != "" {
		end, _, err := w.parseTime(*a.End)
		if err != nil {
			return e, err
		}
		e.end, e.hasEnd = end, true
	}
	if a.Location != nil {
		e.location = *a.Location
	}
	if a.Notes != nil {
		e.description = *a.Notes
	}
	if a.RRule != nil {
		e.rruleText = strings.TrimSpace(*a.RRule)
	}
	return e, nil
}

func (w *writer) addDAV(ctx context.Context, dav *DAVClient, name string, a eventArgs) (string, error) {
	if a.Title == nil || strings.TrimSpace(*a.Title) == "" {
		return "", fmt.Errorf("give the event a title")
	}
	if a.Start == nil || strings.TrimSpace(*a.Start) == "" {
		return "", fmt.Errorf("give the event a start")
	}
	raw, err := w.rawFromArgs(a, rawEvent{})
	if err != nil {
		return "", err
	}
	raw.uid = newUID()
	if _, err := dav.Put(ctx, raw.uid, []byte(formatEvent(raw, w.s.now())), ""); err != nil {
		return "", err
	}
	when := raw.start.In(w.s.zone()).Format("Mon 2 Jan 2006 15:04")
	return fmt.Sprintf("Added %q on %s to calendar %q (uid %s)\n", raw.summary, when, name, raw.uid), nil
}

func (w *writer) update(ctx context.Context, a eventArgs) (string, error) {
	if a.Calendar != nil || a.UID != nil {
		calName := ""
		if a.Calendar != nil {
			calName = *a.Calendar
		}
		if dav, name, err := w.davFor(calName); err != nil {
			return "", err
		} else if dav != nil {
			return w.updateDAV(ctx, dav, name, a)
		}
	}
	return w.updateLocal(a)
}

func (w *writer) updateDAV(ctx context.Context, dav *DAVClient, name string, a eventArgs) (string, error) {
	if a.UID == nil || strings.TrimSpace(*a.UID) == "" {
		return "", fmt.Errorf("give the event's uid and calendar")
	}
	uid := strings.TrimSpace(*a.UID)
	body, etag, err := dav.Get(ctx, uid)
	if err != nil {
		return "", err
	}
	p, err := parse(string(body), w.s.zone())
	if err != nil || len(p.events) == 0 {
		return "", fmt.Errorf("the event on calendar %q could not be read back", name)
	}
	raw := p.events[0]
	length := raw.length()
	movedTo := time.Time{}
	if a.Start != nil && strings.TrimSpace(*a.Start) != "" {
		start, allDay, err := w.parseTime(*a.Start)
		if err != nil {
			return "", err
		}
		raw.start, raw.allDay, movedTo = start, allDay, start
	}
	raw, err = w.rawFromArgs(a, raw)
	if err != nil {
		return "", err
	}
	if !movedTo.IsZero() && a.End == nil && !raw.allDay {
		raw.end, raw.hasEnd = raw.start.Add(length), true
	}
	if _, err := dav.Put(ctx, uid, []byte(formatEvent(raw, w.s.now())), etag); err != nil {
		return "", err
	}
	return fmt.Sprintf("Updated %q on calendar %q (uid %s)\n", raw.summary, name, uid), nil
}

func (w *writer) deleteDAV(ctx context.Context, dav *DAVClient, name, uid, occurrence string) (string, error) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return "", fmt.Errorf("give the event's uid and calendar")
	}
	_, etag, err := dav.Get(ctx, uid)
	if err != nil {
		return "", err
	}
	if occurrence != "" {
		body, _, err := dav.Get(ctx, uid)
		if err != nil {
			return "", err
		}
		p, err := parse(string(body), w.s.zone())
		if err != nil || len(p.events) == 0 {
			return "", fmt.Errorf("the event on calendar %q could not be read back", name)
		}
		raw := p.events[0]
		at, _, err := w.parseTime(occurrence)
		if err != nil {
			return "", err
		}
		if raw.rrule == nil {
			return "", fmt.Errorf("that event does not repeat: delete it instead")
		}
		raw.exdates = append(raw.exdates, at)
		if _, err := dav.Put(ctx, uid, []byte(formatEvent(raw, w.s.now())), etag); err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted one occurrence of the event (uid %s) on calendar %q; the rest stays.\n", uid, name), nil
	}
	if err := dav.Delete(ctx, uid, etag); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted the event (uid %s) from calendar %q.\n", uid, name), nil
}
