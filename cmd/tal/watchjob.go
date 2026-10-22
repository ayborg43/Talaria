package main

import (
	"context"
	"fmt"
	"time"

	"talaria/internal/activity"
	"talaria/internal/app"
	"talaria/internal/cron"
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

	pollCtx, cancel := context.WithTimeout(ctx, cron.WatchPollTimeout)
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
	}[out])
	switch out {
	case watch.PausedByErrors:
		r.rt.Cron.UnscheduleWatch(w.ID)
		return fmt.Sprintf("I could not look at %s %d times in a row, so I have paused this watch. The last problem: %v\n\nTell me when it is fixed and I will resume it (or: /watches resume %s).", w.Tool, w.Errors, callErr, w.ID), nil
	case watch.PausedByBudget:
		r.rt.Cron.UnscheduleWatch(w.ID)
		return fmt.Sprintf("This has changed %d times since you last wrote to me, so I have paused the watch rather than carry on unasked. Write to me to carry on, or: /watches resume %s.", w.Triggers-1, w.ID), nil
	case watch.Changed:
		a.MarkUntrusted() // what the model is about to read came from a service, not from the user
		return a.Run(ctx, watch.Prompt(w, previous, result))
	}
	return cron.Silent, nil
}
