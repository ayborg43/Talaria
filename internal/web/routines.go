package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"talaria/internal/browser"
	"talaria/internal/routine"
)

// RoutineHooks connect saved routines to the scheduler: running one now, putting it on a schedule, and taking it off.
type RoutineHooks struct {
	// Run starts the routine once now as a background task and says so.
	Run func(id string) (string, error)
	// Schedule makes the routine run on days ("daily", "weekdays", "weekly:fri") at a time of day ("07:30") in the
	// person's zone, replacing any schedule it had.
	Schedule func(id, days, at string) (string, error)
	// Unschedule takes the routine off its schedule.
	Unschedule func(id string) error
	// Scheduled says when each scheduled routine runs.
	Scheduled func() map[string]RoutineSchedule
}

// RoutineSchedule is when a routine runs on its own.
type RoutineSchedule struct {
	Days string    `json:"days"`
	At   string    `json:"at"`
	Next time.Time `json:"next,omitzero"`
	Last string    `json:"last,omitempty"`
}

// StepInfo is a step as the page shows it.
type StepInfo struct {
	Line   string `json:"line"`
	Secret bool   `json:"secret,omitempty"`
}

// RoutineInfo is a saved routine as the page shows it.
type RoutineInfo struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Created  time.Time        `json:"created"`
	Steps    []StepInfo       `json:"steps"`
	Schedule *RoutineSchedule `json:"schedule,omitempty"`
}

func stepInfos(steps []routine.Step) []StepInfo {
	out := make([]StepInfo, 0, len(steps))
	for _, s := range steps {
		out = append(out, StepInfo{Line: s.Line(), Secret: s.Secret || s.Kind == routine.Login})
	}
	return out
}

func (s *Server) routinesOn() bool { return s.Routines != nil && s.Rec != nil }

func (s *Server) routinesState() map[string]any {
	on, n := s.Rec.Status()
	var sched map[string]RoutineSchedule
	if s.Sources.Routines != nil && s.Sources.Routines.Scheduled != nil {
		sched = s.Sources.Routines.Scheduled()
	}
	list := []RoutineInfo{}
	for _, r := range s.Routines.List() {
		ri := RoutineInfo{ID: r.ID, Name: r.Name, Created: r.Created, Steps: stepInfos(r.Steps)}
		if sc, ok := sched[r.ID]; ok {
			ri.Schedule = &sc
		}
		list = append(list, ri)
	}
	return map[string]any{"available": true, "recording": on, "count": n, "steps": stepInfos(s.Rec.Steps()), "routines": list,
		"can_run": s.Sources.Routines != nil && s.Sources.Routines.Run != nil}
}

func (s *Server) routinesGet(w http.ResponseWriter, r *http.Request) {
	if !s.routinesOn() {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "routines": []RoutineInfo{}, "steps": []StepInfo{}})
		return
	}
	writeJSON(w, http.StatusOK, s.routinesState())
}

// routinesPost records, saves, runs and schedules routines.
func (s *Server) routinesPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"` // start, stop, discard, save, remove, run, schedule, unschedule
		ID     string `json:"id"`
		Name   string `json:"name"`
		Keep   []int  `json:"keep"` // save: which of the recorded steps to keep (their numbers from 0); all when absent
		Days   string `json:"days"`
		At     string `json:"at"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !s.routinesOn() {
		fail(w, http.StatusNotImplemented, "routines are not set up here")
		return
	}
	in.ID = strings.TrimSpace(in.ID)
	if len(in.ID) > 64 {
		fail(w, http.StatusBadRequest, "that is not a routine")
		return
	}
	out := map[string]any{"ok": true}
	hooks := s.Sources.Routines
	switch in.Action {
	case "start":
		s.Rec.Start()
	case "stop":
		s.Rec.Stop()
	case "discard":
		s.Rec.Discard()
	case "save":
		steps := s.Rec.Steps()
		if in.Keep != nil {
			var kept []routine.Step
			for _, i := range in.Keep {
				if i >= 0 && i < len(steps) {
					kept = append(kept, steps[i])
				}
			}
			steps = kept
		}
		saved, err := s.Routines.Add(in.Name, steps)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		s.Rec.Discard()
		out["saved"] = saved.Name
	case "remove":
		if hooks != nil && hooks.Unschedule != nil {
			_ = hooks.Unschedule(in.ID)
		}
		if err := s.Routines.Remove(in.ID); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	case "run", "schedule", "unschedule":
		if _, ok := s.Routines.Get(in.ID); !ok {
			fail(w, http.StatusNotFound, "there is no such routine")
			return
		}
		if hooks == nil {
			fail(w, http.StatusNotImplemented, "routines cannot be run here")
			return
		}
		var msg string
		var err error
		switch in.Action {
		case "run":
			if hooks.Run == nil {
				fail(w, http.StatusNotImplemented, "routines cannot be run here")
				return
			}
			msg, err = hooks.Run(in.ID)
		case "schedule":
			if hooks.Schedule == nil {
				fail(w, http.StatusNotImplemented, "routines cannot be scheduled here")
				return
			}
			msg, err = hooks.Schedule(in.ID, strings.TrimSpace(in.Days), strings.TrimSpace(in.At))
		default:
			if hooks.Unschedule != nil {
				err = hooks.Unschedule(in.ID)
			}
		}
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		out["message"] = msg
	default:
		fail(w, http.StatusBadRequest, "say what to do: start, stop, discard, save, remove, run, schedule or unschedule")
		return
	}
	for k, v := range s.routinesState() {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// probe is what a recording asks of a page: where it is, and what is under a point or in focus, in words.
type probe interface {
	Where(ctx context.Context) string
	DescribeAt(ctx context.Context, x, y float64) (browser.Described, bool)
	DescribeFocus(ctx context.Context) (browser.Described, bool)
	FocusKind(ctx context.Context) string
	SignInFields(ctx context.Context) []browser.SignInField
}

// recordStep works out what a takeover action is as a step, before it is done (a click is described by what is under
// it now, and typing by the field that has focus), or nil if it is not something a routine repeats. It never reads a
// value out of a field: only labels.
func (s *Server) recordStep(ctx context.Context, p probe, kind string, x, y, dy float64, text, key, url string) *routine.Step {
	at := p.Where(ctx)
	switch kind {
	case "open":
		u := strings.TrimSpace(url)
		if u != "" && !strings.Contains(u, "://") {
			u = "https://" + u
		}
		return &routine.Step{Kind: routine.Open, URL: u}
	case "click":
		e, ok := p.DescribeAt(ctx, x, y)
		if !ok {
			return nil
		}
		return &routine.Step{Kind: routine.Click, Target: e.Words(), URL: at}
	case "type", "replace":
		if text == "" {
			return nil
		}
		e, ok := p.DescribeFocus(ctx)
		if !ok {
			return nil
		}
		st := &routine.Step{Kind: routine.Type, Target: e.Words(), URL: at, Secret: e.Private() || p.FocusKind(ctx) == "password"}
		if !st.Secret {
			st.Text = text
			if s.Scrub != nil {
				st.Text = s.Scrub(text)
			}
		}
		return st
	case "key":
		if key == "Enter" {
			return &routine.Step{Kind: routine.Key, URL: at}
		}
	case "wheel":
		if dy >= 40 || dy <= -40 {
			return &routine.Step{Kind: routine.Scroll, Down: dy > 0, URL: at}
		}
	case "back":
		return &routine.Step{Kind: routine.Back, URL: at}
	case "login":
		return &routine.Step{Kind: routine.Login, URL: at}
	}
	return nil
}
