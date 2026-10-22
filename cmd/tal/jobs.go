package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"talaria/internal/activity"
	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/gateway/chat"
	"talaria/internal/goal"
)

// gatewayJobTargets says which profiles and environments a job scheduled from a
// chat may name, and what the chat itself is using. They are exactly what the
// chat could switch to with /model and /env, so scheduling is no way round them.
// A setting with only one possibility offers no choice.
func gatewayJobTargets(b *chat.Backends, e *chat.Environments, current func() (string, string)) cron.Targets {
	t := cron.Targets{Current: current}
	if b != nil && len(b.Names) > 1 {
		t.Profiles, t.DefaultProfile = b.Names, b.Default
		t.CheckProfile = func(name string) error { _, err := b.Open(name); return err }
	}
	if e != nil && len(e.Names) > 1 {
		t.Environments, t.DefaultEnvironment = e.Names, e.Default
	}
	return t
}

// jobRunner runs scheduled jobs on the model profile and in the environment each
// one asks for.
//
// It resolves both before building the agent, and a job that asks for something
// it cannot have fails, with the reason delivered to its chat. It is never run on
// the default instead: nobody is watching, and a job meant for the build server
// that quietly ran on this machine, or one meant for a small local model that
// quietly ran on an expensive one, is a surprise worth a failure message.
type jobRunner struct {
	rt       *app.Runtime
	backends *chat.Backends     // nil when there is one model
	envs     *chat.Environments // nil when there is one environment
	allowed  map[int64]bool
	note     string // tells the model how its reply will be shown; cronNote if empty
	taskNote string // the same for a background task; taskNote if empty
	goals    *goal.Store
	yolo     bool
	sink     activity.Sink // tells a page who is working, if there is one; may be nil
}

func (r *jobRunner) run(ctx context.Context, j cron.Job) (string, error) {
	if !r.allowed[j.ChatID] { // private chat ID == user ID; the owner was removed from the allowlist
		return "", cron.ErrDrop
	}
	if j.Kind == cron.KindWatch {
		return r.runWatch(ctx, j)
	}

	var backend *chat.Backend
	if j.Profile != "" {
		b, err := r.backend(j.Profile)
		if err != nil {
			return "", fmt.Errorf("this job runs on the %q model profile, which cannot be used: %w", j.Profile, err)
		}
		backend = &b
	}
	var env execenv.Env
	if j.Environment != "" {
		e, err := r.environment(ctx, j.Environment)
		if err != nil {
			return "", fmt.Errorf("this job runs in the %q environment, which cannot be used: %w", j.Environment, err)
		}
		env = e
	}

	prompt, note, final, err := r.prepare(j)
	if err != nil {
		return "", err
	}
	if final != "" { // there is nothing to run: this is the whole result
		return final, nil
	}

	// Nobody is watching: risky tools run only if the job was created with allow_risky.
	a := r.rt.NewAgent(app.AgentOptions{
		Approve:     func(string, string) bool { return r.yolo || j.AllowRisky },
		ExtraSystem: note,
		// Only for the activity log, which says whose job made each call. A job has no
		// cron tools: it must not schedule more jobs.
		Origin: "job:" + j.ID, Gateway: j.Gateway, ChatID: j.ChatID, NoCron: true, Activity: r.sink,
	})
	if r.sink != nil {
		id := activity.ID(ctx)
		a.OnToolStart = func(name, _ string) { activity.Note(r.sink, id, name) }
	}
	if backend != nil {
		a.Provider, a.Model = backend.Provider, backend.Model
	}
	if env != nil {
		r.rt.UseEnvironment(a, env)
	}
	return a.Run(ctx, prompt)
}

func (r *jobRunner) backend(name string) (chat.Backend, error) {
	if r.backends == nil || !slices.Contains(r.backends.Names, name) {
		return chat.Backend{}, errors.New("the gateway does not offer it any more")
	}
	return r.backends.Open(name)
}

func (r *jobRunner) environment(ctx context.Context, name string) (execenv.Env, error) {
	if r.envs == nil || !slices.Contains(r.envs.Names, name) {
		return nil, errors.New("the gateway does not offer it any more")
	}
	return r.envs.Get(ctx, name)
}

// prepare says what a job is to be asked and told. An ordinary job and a task
// carry their prompt; a goal check-in is written fresh from the goal as it
// stands, so it needs no memory of the last one. A check-in for a goal that is
// no longer open is dropped, and one that has used its allowance of check-ins
// pauses the goal and says so instead of running.
func (r *jobRunner) prepare(j cron.Job) (prompt, note, final string, err error) {
	switch j.Kind {
	case cron.KindTask:
		note = r.taskNote
		if note == "" {
			note = taskNote
		}
		return j.Prompt, note, "", nil
	case cron.KindGoal:
		if r.goals == nil {
			return "", "", "", cron.ErrDrop
		}
		g, err := r.goals.GetAny(j.Goal)
		if err != nil || g.Status != goal.Open {
			return "", "", "", cron.ErrDrop // finished, paused, dropped or deleted since this was scheduled
		}
		if g.Checkins >= g.MaxCheckins {
			paused, err := r.goals.Update(g.Gateway, g.ChatID, g.ID, func(x *goal.Goal) error {
				x.Status = goal.Paused
				x.AddNote(time.Now(), fmt.Sprintf("Paused after %d check-ins without hearing from the user.", x.Checkins))
				return nil
			})
			if err != nil {
				return "", "", "", err
			}
			r.rt.Cron.UnscheduleGoal(paused.ID)
			return "", "", fmt.Sprintf("I have checked in on this %d times since you last wrote, so I have paused it rather than carry on unasked. "+
				"Tell me how it should go on, and I will resume it (or: goals resume %s).\n\n%s", paused.Checkins, paused.ID, goal.Describe(paused, time.Time{})), nil
		}
		g, err = r.goals.Update(g.Gateway, g.ChatID, g.ID, func(x *goal.Goal) error {
			x.Checkins++
			x.LastCheckin = time.Now()
			return nil
		})
		if err != nil {
			return "", "", "", err
		}
		note = r.note
		if note == "" {
			note = cronNote
		}
		return goal.CheckinPrompt(g), note, "", nil
	}
	note = r.note
	if note == "" {
		note = cronNote
	}
	return j.Prompt, note, "", nil
}
