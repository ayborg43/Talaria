package main

import (
	"context"
	"fmt"
	"time"

	"talaria/internal/activity"
	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/goal"
	"talaria/internal/metrics"
	"talaria/internal/watch"
)

// runWatch is one poll of a watch. The tool is called without the model, through
// the same permission rules and activity log as any call; only when its answer
// has changed is the model asked whether the change is worth a message.
//
// Nothing here asks anyone for approval: a tool that needs it cannot be watched,
// and what the model does about a change is limited to what runs unattended,
// whatever the gateway's yolo setting says, because the text it is reading came
// from outside.
// pollBudget is how long the poll itself may take: half the job's remaining
// time, so that when something changed there is still room for the look at
// what it means. A slow poll must not starve the analysis run.
func pollBudget(ctx context.Context) time.Duration {
	budget := cron.WatchPollTimeout / 2
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			budget = remaining / 2
		}
	}
	return min(max(budget, time.Minute), cron.WatchPollTimeout/2)
}

func (r *jobRunner) runWatch(ctx context.Context, j cron.Job) (string, error) {
	ws := r.rt.Watches()
	if ws == nil {
		return "", cron.ErrDrop
	}
	w, err := ws.GetAny(j.Watch)
	if err != nil || w.Status != watch.Active {
		return "", cron.ErrDrop // deleted or paused since this was scheduled
	}

	activity.Note(r.sink, activity.ID(ctx), w.Tool)

	note := r.note
	if note == "" {
		note = cronNote
	}
	a := r.rt.NewAgent(app.AgentOptions{
		ExtraSystem: note,
		Origin:      "watch:" + w.ID, Gateway: j.Gateway, ChatID: j.ChatID, NoCron: true, NoHistory: true,
	})

	pollCtx, cancel := context.WithTimeout(ctx, pollBudget(ctx))
	result, callErr := a.Call(pollCtx, w.Tool, w.Args)
	cancel()
	if ctx.Err() != nil {
		return "", ctx.Err() // shutting down or cancelled: not a poll that failed
	}

	var out watch.Outcome
	var previous string
	w, err = ws.Update(w.Gateway, w.ChatID, w.ID, func(x *watch.Watch) error {
		out, previous = x.Observe(time.Now(), result, callErr)
		return nil
	})
	if err != nil {
		if _, gone := ws.GetAny(j.Watch); gone != nil {
			return "", cron.ErrDrop // the user deleted it while it was being polled
		}
		return "", err
	}

	metrics.Inc("talaria_watch_polls_total", "outcome", map[watch.Outcome]string{
		watch.Quiet: "quiet", watch.Changed: "changed", watch.Failed: "failed", watch.PausedByErrors: "paused", watch.PausedByBudget: "paused",
		watch.ConditionMet: "met",
	}[out])
	if (out == watch.Changed || out == watch.ConditionMet) && w.WakeGoal != "" {
		if r.wakeGoal(w) {
			return cron.Silent, nil // the goal now has it; telling the user too would be a second voice on one event
		}
	}
	switch out {
	case watch.PausedByErrors:
		r.rt.Cron.UnscheduleWatch(w.ID)
		problem := w.LastError
		if callErr != nil {
			problem = callErr.Error()
		}
		return fmt.Sprintf("I could not look at %s %d times in a row, so I have paused this watch. The last problem: %s\n\nTell me when it is fixed and I will resume it (or: /watches resume %s).", w.Tool, w.Errors, problem, w.ID), nil
	case watch.PausedByBudget:
		r.rt.Cron.UnscheduleWatch(w.ID)
		return fmt.Sprintf("This has changed %d times since you last wrote to me, so I have paused the watch rather than carry on unasked. Write to me to carry on, or: /watches resume %s.", w.Triggers-1, w.ID), nil
	case watch.ConditionMet:
		return watch.ConditionMessage(w, w.LastFound), nil // settled here: no model run, and nothing from the page but the price read
	case watch.Changed:
		a.MarkUntrusted() // what the model is about to read came from a service, not from the user
		return a.Run(ctx, watch.Prompt(w, previous, result))
	}
	return cron.Silent, nil
}

// minWakeGap is the shortest time between two check-ins of a goal, however often what it waits on changes.
const minWakeGap = goal.MinNextCheckin

// wakeGoal tells the goal a watch has seen something and brings its next check-in forward, within the goal's own
// limits: it has to be open, it keeps its cap on check-ins, and it is never woken closer than minWakeGap to its last
// one. The note says what happened in words of this program's, never in the page's: a watch reads text other people
// wrote, and a goal that works with tools unattended must not be handed it as if it were an instruction. The goal
// reads the page itself, as untrusted, when it wakes. It says whether the goal took it; if not the watch speaks as
// it would have.
func (r *jobRunner) wakeGoal(w watch.Watch) bool {
	if r.goals == nil {
		return false
	}
	g, err := r.goals.Get(w.Gateway, w.ChatID, w.WakeGoal)
	if err != nil || g.Status != goal.Open || g.Checkins >= g.MaxCheckins {
		return false
	}
	what := "changed"
	if w.When != nil {
		what = "reached what it was waiting for (" + w.When.Describe() + ")"
	}
	line := fmt.Sprintf("Woken by the watch %q (%s, polled with %s): it %s.", tidy(w.Title, 60, "watch"), w.ID, w.Tool, what)
	if _, err := r.rt.Cron.WakeGoal(g.ID, minWakeGap); err != nil {
		return false
	}
	_, err = r.goals.Update(w.Gateway, w.ChatID, g.ID, func(x *goal.Goal) error {
		x.AddNote(time.Now(), line)
		return nil
	})
	return err == nil
}
