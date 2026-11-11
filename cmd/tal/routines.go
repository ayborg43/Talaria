package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/recipe"
	"talaria/internal/routine"
	"talaria/internal/web"
)

// routineDays are the schedules a routine may be given.
func routineDays(days string) bool {
	switch days {
	case "daily", "weekdays", "weekly:mon", "weekly:tue", "weekly:wed", "weekly:thu", "weekly:fri", "weekly:sat", "weekly:sun":
		return true
	}
	return false
}

// daysOf reads the day part of a schedule the recipes write back into the words the app uses.
func daysOf(schedule string) string {
	f := strings.Fields(schedule)
	if len(f) != 6 {
		return ""
	}
	switch dow := f[5]; dow {
	case "*":
		return "daily"
	case "1-5":
		return "weekdays"
	default:
		names := map[string]string{"0": "sun", "1": "mon", "2": "tue", "3": "wed", "4": "thu", "5": "fri", "6": "sat"}
		if n, ok := names[dow]; ok {
			return "weekly:" + n
		}
	}
	return ""
}

// webRoutineHooks lets the web app run a routine now, put it on a schedule or take it off. A routine runs as an
// ordinary background task or scheduled job that repeats the saved steps: it may click and type in the browser as
// they show, and cannot do anything else that asks for approval. It is refused when the user's rules say the agent
// may not start work or schedule it.
func webRoutineHooks(rt *app.Runtime, store *routine.Store, jobs *cron.Store, zone *time.Location, sched func() *cron.Scheduler) *web.RoutineHooks {
	jobsOf := func(id string) []cron.Job {
		all, _ := jobs.List()
		var out []cron.Job
		for _, j := range all {
			if j.Gateway == "web" && j.ChatID == web.ChatID && j.Routine == id && j.Kind == "" {
				out = append(out, j)
			}
		}
		return out
	}
	return &web.RoutineHooks{
		Run: func(id string) (string, error) {
			r, ok := store.Get(id)
			if !ok {
				return "", errors.New("there is no such routine")
			}
			if err := ruleRefuses(rt, "task_start"); err != nil {
				return "", err
			}
			var ctl cron.Control
			if s := sched(); s != nil {
				ctl = s
			}
			j, err := cron.StartRoutine(jobs, ctl, "web", web.ChatID, tidy(r.Name, 60, "Routine"), id)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Started %q. It follows your steps in the agent's browser, and its result comes to your chat.", j.Name), nil
		},
		Schedule: func(id, days, at string) (string, error) {
			r, ok := store.Get(id)
			if !ok {
				return "", errors.New("there is no such routine")
			}
			if !routineDays(days) {
				return "", errors.New("choose daily, weekdays or a day of the week")
			}
			if err := ruleRefuses(rt, "cron_add"); err != nil {
				return "", err
			}
			cr, err := (recipe.Recipe{Days: days}).CronFor(zone.String(), at)
			if err != nil {
				return "", err
			}
			for _, old := range jobsOf(id) {
				if err := jobs.Remove(old.ID, old.Gateway, old.ChatID); err != nil {
					return "", err
				}
			}
			j, err := jobs.Add(cron.Job{Name: tidy(r.Name, 60, "Routine"), Schedule: cr, Prompt: "Repeat the saved routine " + tidy(r.Name, 60, "") + ".",
				Gateway: "web", ChatID: web.ChatID, Routine: id, TimeoutSec: 30 * 60})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%q is set: %s at %s (%s). Next run %s.", r.Name, (recipe.Recipe{Days: days}).DaysText(), at, zone, j.NextRun.In(zone).Format("Mon 2 Jan 15:04")), nil
		},
		Unschedule: func(id string) error {
			for _, old := range jobsOf(id) {
				if err := jobs.Remove(old.ID, old.Gateway, old.ChatID); err != nil {
					return err
				}
			}
			return nil
		},
		Scheduled: func() map[string]web.RoutineSchedule {
			out := map[string]web.RoutineSchedule{}
			all, _ := jobs.List()
			for _, j := range all {
				if j.Gateway == "web" && j.ChatID == web.ChatID && j.Routine != "" && j.Kind == "" {
					out[j.Routine] = web.RoutineSchedule{Days: daysOf(j.Schedule), At: briefTime(j.Schedule), Next: j.NextRun.In(zone), Last: j.LastStatus}
				}
			}
			return out
		},
	}
}
