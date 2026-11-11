package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"talaria/internal/agent"
	"talaria/internal/routine"
	"talaria/internal/runlog"
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
	// routine says which profile scheduled prompts that name none run on (see config.RoutineProfileName); it is
	// asked at each run, so a change in Settings applies at once. Nil means the chat's own.
	routine func() string
	// runs keeps a short record of each run of a web chat's jobs, for the Activity view and for steering a running task.
	runs *runlog.Log
	// routines holds the routines the user recorded; a job that names one repeats it.
	routines *routine.Store
}

func (r *jobRunner) run(ctx context.Context, j cron.Job) (string, error) {
	if !r.allowed[j.ChatID] { // private chat ID == user ID; the owner was removed from the allowlist
		return "", cron.ErrDrop
	}
	if j.Kind == cron.KindWatch {
		return r.runWatch(ctx, j)
	}

	var backend *chat.Backend
	profile, routine := j.Profile, false
	if profile == "" && j.Kind == "" && r.routine != nil { // a plain scheduled prompt that names no model: routine work
		profile = r.routine()
		routine = profile != ""
	}
	if profile != "" {
		b, err := r.backend(profile)
		switch {
		case err == nil:
			backend = &b
		case !routine: // a model the job itself names must work, or it says so
			return "", fmt.Errorf("this job runs on the %q model profile, which cannot be used: %w", profile, err)
		}
		// a routine model that cannot be opened (no key, removed) is not the job's own choice: it runs on the chat's model
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
	approve := func(string, string) bool { return r.yolo || j.AllowRisky }
	if j.Routine != "" {
		approve = routineApprove(r.yolo || j.AllowRisky)
	}
	a := r.rt.NewAgent(app.AgentOptions{
		Approve:     approve,
		ExtraSystem: note,
		// Only for the activity log, which says whose job made each call. A job has no
		// cron tools: it must not schedule more jobs.
		Origin: "job:" + j.ID, Gateway: j.Gateway, ChatID: j.ChatID, NoCron: true, Activity: r.sink,
		// The morning brief may leave suggestions for the user to start or hide; no other job proposes anything.
		Suggest: j.Kind == "" && j.Name == briefName,
	})
	a.RefuseRisky = j.Routine != "" // a routine clicks and types as shown, but never deletes or posts
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
	reply, err := r.runRecorded(ctx, a, j, prompt)
	if err != nil && a.Acted() {
		// It had already done something. The scheduler tries a failed one-shot again; this one it must not.
		return reply, cron.Acted(err)
	}
	return reply, err
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
	if j.Routine != "" { // the steps are read now, so a routine the user has edited or deleted is run as it is
		if r.routines == nil {
			return "", "", "", cron.ErrDrop
		}
		rt, ok := r.routines.Get(j.Routine)
		if !ok {
			return "", "", "", cron.ErrDrop
		}
		note = r.taskNote
		if note == "" {
			note = taskNote
		}
		return rt.Prompt(), note, "", nil
	}
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

// runRecorded runs the agent and writes down what it did: each tool it used (what it was asked, scrubbed), the files
// it made, and what it reported. It also lets the user steer it: anything they add while it works is read at its next
// step. Only a web chat's jobs are recorded; the rest run as they always did.
func (r *jobRunner) runRecorded(ctx context.Context, a *agent.Agent, j cron.Job, prompt string) (string, error) {
	if r.runs == nil || j.Gateway != "web" {
		return a.Run(ctx, prompt)
	}
	rid := r.runs.Begin(j.ID, j.Name, j.Kind, j.Gateway, j.ChatID, prompt, a.Model, j.Parent)
	prevStart := a.OnToolStart
	a.OnToolStart = func(name, args string) {
		if prevStart != nil {
			prevStart(name, args)
		}
		r.runs.Step(rid, name, briefArgs(a, name, args))
	}
	prevArt := a.OnToolArtifact
	a.OnToolArtifact = func(name string, art json.RawMessage) {
		if prevArt != nil {
			prevArt(name, art)
		}
		var d struct{ Kind, ID, Name, MIME string }
		if json.Unmarshal(art, &d) == nil && d.Kind == "document" && d.ID != "" {
			r.runs.AddFile(rid, runlog.File{ID: d.ID, Name: d.Name, MIME: d.MIME})
		}
	}
	a.Interject = func() []string { return r.runs.Take(rid) }
	reply, err := a.Run(ctx, prompt)
	switch {
	case err == nil && strings.TrimSpace(reply) == cron.Silent:
		r.runs.End(rid, runlog.Done, "Nothing to report.")
	case err == nil:
		r.runs.End(rid, runlog.Done, a.Safe(reply))
	case ctx.Err() != nil:
		r.runs.End(rid, runlog.Cancelled, "Stopped before it finished.")
	default:
		r.runs.End(rid, runlog.Failed, a.Safe("error: "+err.Error()))
	}
	return reply, err
}

// briefArgs says in a short line what a tool was asked, for the record of a run: the tool's own account of its
// arguments where it has one (so a typed password is never written), else the values of a plain JSON object, with
// secrets removed. It is never what the tool read or returned.
func briefArgs(a *agent.Agent, name, args string) string {
	if t, ok := a.Tools.Get(name); ok && t.LogArgs != nil {
		args = t.LogArgs(json.RawMessage(args))
	}
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil && len(m) == 0 {
		return "" // a call that was asked nothing
	}
	if len(m) > 0 {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var parts []string
		for _, k := range keys {
			if s, ok := m[k].(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			args = strings.Join(parts, " · ")
		}
	}
	return a.Safe(args)
}

// routineTools are the browser actions a routine may take without asking: it is repeating what the user did by hand.
var routineTools = []string{"browser_click", "browser_type", "browser_press", "browser_select"}

// routineApprove is the approver of a run that repeats a routine. It says yes to those browser actions and to
// nothing else (no shell, no files, no mail), and never to a call flagged because it follows something read outside.
// A call that would delete or post is refused before it gets here (Agent.RefuseRisky). With wider is set the job
// already had leave to use risky tools, which a routine does not narrow.
func routineApprove(wider bool) func(name, args string) bool {
	return func(name, _ string) bool {
		if wider {
			return true
		}
		return !agent.IsFlagged(name) && slices.Contains(routineTools, name)
	}
}
