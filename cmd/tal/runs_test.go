package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/agent"
	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/provider"
	"talaria/internal/routine"
	"talaria/internal/runlog"
)

func webJob() cron.Job {
	return cron.Job{ID: "j1", Name: "report", Kind: cron.KindTask, Prompt: "do it", Gateway: "web", ChatID: 1}
}

func TestAWebJobLeavesARecordOfWhatItDid(t *testing.T) {
	k := newJobKit(t)
	k.runner.runs = &runlog.Log{}
	out, err := k.runner.run(context.Background(), webJob())
	if err != nil || out != "openai:in-local" {
		t.Fatalf("%q %v", out, err)
	}
	list := k.runner.runs.List("web", 1, 10)
	if len(list) != 1 || list[0].Status != runlog.Done || list[0].Steps != 1 {
		t.Fatalf("%+v", list)
	}
	run, _ := k.runner.runs.Get("web", 1, "j1")
	if run.Steps[0].Tool != "list_dir" || !strings.Contains(run.Steps[0].Detail, "/") || run.Result != "openai:in-local" || run.Prompt != "do it" {
		t.Fatalf("%+v", run)
	}
}

func TestOnlyTheWebChatsJobsAreRecorded(t *testing.T) {
	k := newJobKit(t)
	k.runner.runs = &runlog.Log{}
	j := webJob()
	j.Gateway = "telegram"
	if _, err := k.runner.run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if n := len(k.runner.runs.List("telegram", 1, 10)) + len(k.runner.runs.List("web", 1, 10)); n != 0 {
		t.Fatalf("%d recorded", n)
	}
}

// steerModel says something to the run through the log while it thinks about its first step, then reports every
// message it is shown at its second.
type steerModel struct {
	mu    sync.Mutex
	runs  *runlog.Log
	calls int
	seen  []string
	err   error
}

func (m *steerModel) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return provider.Message{}, m.err
	}
	if m.calls == 1 {
		m.runs.Steer("j1", "also look at the second site")
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "list_dir", Arguments: `{"path":"/"}`}}}, nil
	}
	for _, msg := range req.Messages {
		m.seen = append(m.seen, msg.Content)
	}
	return provider.Message{Role: provider.RoleAssistant, Content: "done"}, nil
}

// runnerOn is a runner with no choice of models, whose agent talks to p.
func runnerOn(t *testing.T, p provider.Provider, runs *runlog.Log) *jobRunner {
	t.Helper()
	rt := app.Open(app.Config{Home: t.TempDir(), Provider: p, Model: "m", Env: listEnv{name: "local"}})
	t.Cleanup(rt.Close)
	return &jobRunner{rt: rt, allowed: map[int64]bool{1: true}, runs: runs}
}

func TestWhatTheUserAddsIsReadAtTheNextStepAndOnlyOnce(t *testing.T) {
	runs := &runlog.Log{}
	m := &steerModel{runs: runs}
	runner := runnerOn(t, m, runs)
	if _, err := runner.run(context.Background(), webJob()); err != nil {
		t.Fatal(err)
	}
	var found int
	for _, s := range m.seen {
		if strings.Contains(s, "The user adds, while you are working: also look at the second site") {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("it should be told once, got %d: %q", found, m.seen)
	}
	run, _ := runs.Get("web", 1, "j1")
	if len(run.Steers) != 1 || !run.Steers[0].Applied {
		t.Fatalf("%+v", run.Steers)
	}
}

func TestAFailedRunIsRecordedAsFailed(t *testing.T) {
	runs := &runlog.Log{}
	runner := runnerOn(t, &steerModel{err: errors.New("the model is down")}, runs)
	if _, err := runner.run(context.Background(), webJob()); err == nil {
		t.Fatal("it should fail")
	}
	run, _ := runs.Get("web", 1, "j1")
	if run.Status != runlog.Failed || !strings.Contains(run.Result, "the model is down") {
		t.Fatalf("%+v", run)
	}
}

func TestFollowingUpAFinishedRunStartsATaskThatKnowsWhatHappened(t *testing.T) {
	k := newJobKit(t)
	runs := &runlog.Log{}
	jobs := &cron.Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	hooks := webRunHooks(k.runner.rt, runs, jobs, func() *cron.Scheduler { return nil })

	id := runs.Begin("j1", "Research", "task", "web", 1, "compare three laptops", "m", "")
	if _, err := hooks.FollowUp(id, "go on"); err == nil {
		t.Fatal("a running task is steered, not followed up")
	}
	runs.End(id, runlog.Done, "Laptop A is cheapest")
	name, err := hooks.FollowUp(id, "now check delivery times")
	if err != nil || !strings.HasPrefix(name, "Follow-up: Research") {
		t.Fatalf("%q %v", name, err)
	}
	all, _ := jobs.List()
	if len(all) != 1 || all[0].Parent != id || all[0].Kind != cron.KindTask || all[0].Gateway != "web" || all[0].AllowRisky {
		t.Fatalf("%+v", all)
	}
	for _, want := range []string{"compare three laptops", "Laptop A is cheapest", "now check delivery times"} {
		if !strings.Contains(all[0].Prompt, want) {
			t.Errorf("missing %q in %q", want, all[0].Prompt)
		}
	}
	if _, err := hooks.FollowUp("nope", "x"); err == nil {
		t.Fatal("an unknown run")
	}
}

func TestAStepSaysWhatWasAskedInPlainWordsAndNothingWhenNothingWas(t *testing.T) {
	a := runnerOn(t, &steerModel{}, nil).rt.NewAgent(app.AgentOptions{ChatID: 1, Gateway: "web"})
	for args, want := range map[string]string{
		`{}`:                                  "",
		`{"path":"/","depth":2}`:              "/",
		`{"url":"https://x.test","q":"cats"}`: "cats · https://x.test",
		`not json`:                            "not json",
	} {
		if got := briefArgs(a, "list_dir", args); got != want {
			t.Errorf("%s: got %q, want %q", args, got, want)
		}
	}
}

func TestARoutineJobIsAskedForItsSavedStepsAndMayOnlyUseTheBrowserWithoutAsking(t *testing.T) {
	runs := &runlog.Log{}
	runner := runnerOn(t, &steerModel{}, runs)
	runner.routines = &routine.Store{Path: filepath.Join(t.TempDir(), "routines.json")}
	saved, err := runner.routines.Add("Orders", []routine.Step{{Kind: routine.Open, URL: "https://shop.test/orders"}, {Kind: routine.Click, Target: `the link "Latest"`}})
	if err != nil {
		t.Fatal(err)
	}
	j := webJob()
	j.Routine, j.Prompt = saved.ID, "Repeat the saved routine Orders."
	prompt, _, _, err := runner.prepare(j)
	if err != nil || !strings.Contains(prompt, "1. Open https://shop.test/orders") || !strings.Contains(prompt, `2. Click the link "Latest"`) {
		t.Fatalf("%q %v", prompt, err)
	}
	// Edited, the next run does the new thing; deleted, it is dropped instead of run from memory.
	runner.routines.Remove(saved.ID)
	if _, _, _, err := runner.prepare(j); !errors.Is(err, cron.ErrDrop) {
		t.Fatalf("%v", err)
	}

	ok := routineApprove(false)
	for _, name := range []string{"browser_click", "browser_type", "browser_press", "browser_select"} {
		if !ok(name, "") {
			t.Errorf("%s should be allowed", name)
		}
	}
	for _, name := range []string{"shell", "write_file", "mcp_mail_send_email", "browser_handoff", "email_send", agent.Flagged("browser_click")} {
		if ok(name, "") {
			t.Errorf("%s must not be approved for a routine", name)
		}
	}
}

func TestSchedulingARoutineReplacesItsOldScheduleAndIsReadBack(t *testing.T) {
	k := newJobKit(t)
	store := &routine.Store{Path: filepath.Join(t.TempDir(), "routines.json")}
	jobs := &cron.Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	r, _ := store.Add("Orders", []routine.Step{{Kind: routine.Back}})
	zone, _ := time.LoadLocation("Europe/Paris")
	hooks := webRoutineHooks(k.runner.rt, store, jobs, zone, func() *cron.Scheduler { return nil })

	if _, err := hooks.Schedule(r.ID, "fortnightly", "07:30"); err == nil {
		t.Fatal("an unknown rhythm")
	}
	if _, err := hooks.Schedule(r.ID, "weekdays", "7:3"); err == nil {
		t.Fatal("a bad time")
	}
	if _, err := hooks.Schedule(r.ID, "weekdays", "07:30"); err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.Schedule(r.ID, "weekly:fri", "18:00"); err != nil {
		t.Fatal(err)
	}
	all, _ := jobs.List()
	if len(all) != 1 || all[0].Routine != r.ID || all[0].AllowRisky || all[0].Kind != "" {
		t.Fatalf("one schedule per routine, and it is not allowed risky tools: %+v", all)
	}
	got := hooks.Scheduled()[r.ID]
	if got.Days != "weekly:fri" || got.At != "18:00" {
		t.Fatalf("%+v", got)
	}
	if err := hooks.Unschedule(r.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := jobs.List(); len(all) != 0 {
		t.Fatalf("%+v", all)
	}
	// Running one now is a background task that carries the routine's id.
	if _, err := hooks.Run(r.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := jobs.List(); len(all) != 1 || all[0].Routine != r.ID || all[0].Kind != cron.KindTask {
		t.Fatalf("%+v", all)
	}
	if _, err := hooks.Run("nope"); err == nil {
		t.Fatal("no such routine")
	}
}
