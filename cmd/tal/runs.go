package main

import (
	"errors"
	"fmt"
	"strings"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/runlog"
	"talaria/internal/web"
)

// webRunHooks lets the Activity view stop a running task and write to a finished one. Writing to a finished task
// starts a new background task that is told what the first one was asked, what it reported and what the user now
// says: the same kind of task the model starts in chat (risky tools off, a few at a time), and held to the same
// rule about starting work.
func webRunHooks(rt *app.Runtime, runs *runlog.Log, jobs *cron.Store, sched func() *cron.Scheduler) *web.RunHooks {
	return &web.RunHooks{
		FollowUp: func(runID, text string) (string, error) {
			run, ok := runs.Get("web", web.ChatID, runID)
			if !ok {
				return "", errors.New("there is no record of that run")
			}
			if run.Status == runlog.Running {
				return "", errors.New("it is still running: what you write is passed to it instead")
			}
			if err := ruleRefuses(rt, "task_start"); err != nil {
				return "", err
			}
			var b strings.Builder
			b.WriteString("You did a task earlier and the user has written to you about it. Carry on from where it left off, and do what they ask.\n\n")
			fmt.Fprintf(&b, "The task was:\n%s\n\n", run.Prompt)
			if run.Result != "" {
				fmt.Fprintf(&b, "What you reported:\n%s\n\n", run.Result)
			}
			fmt.Fprintf(&b, "The user now says:\n%s", text)
			var ctl cron.Control
			if s := sched(); s != nil {
				ctl = s
			}
			j, err := cron.StartFollowUp(jobs, ctl, "web", web.ChatID, tidy("Follow-up: "+run.Name, 60, "Follow-up"), b.String(), run.ID)
			if err != nil {
				return "", err
			}
			return j.Name, nil
		},
		Stop: func(runID string) error {
			run, ok := runs.Get("web", web.ChatID, runID)
			if !ok || run.Status != runlog.Running {
				return errors.New("that is not running")
			}
			if s := sched(); s == nil || !s.Cancel("web", web.ChatID, run.JobID) {
				return errors.New("it could not be stopped: it may have just finished")
			}
			return nil
		},
	}
}
