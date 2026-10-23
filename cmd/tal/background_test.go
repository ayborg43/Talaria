package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/goal"
	"talaria/internal/provider"
)

// spy is a model that records what it was told and offered, and answers with a reply.
type spy struct {
	mu     sync.Mutex
	reply  string
	system []string
	user   []string
	tools  [][]string
}

func (s *spy) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.system = append(s.system, req.Messages[0].Content)
	s.user = append(s.user, req.Messages[len(req.Messages)-1].Content)
	var names []string
	for _, t := range req.Tools {
		names = append(names, t.Name)
	}
	s.tools = append(s.tools, names)
	return provider.Message{Role: provider.RoleAssistant, Content: s.reply}, nil
}

func (s *spy) calls() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.user) }

type goalRig struct {
	model  *spy
	goals  *goal.Store
	runner *jobRunner
	rt     *app.Runtime
}

func newGoalRig(t *testing.T) *goalRig {
	t.Helper()
	home := t.TempDir()
	model := &spy{reply: "did the work"}
	goals := &goal.Store{Path: filepath.Join(home, "goals.json")}
	rt := app.Open(app.Config{Home: home, Provider: model, Model: "m", Env: execenv.Local{}, Goals: goals})
	t.Cleanup(rt.Close)
	return &goalRig{model: model, goals: goals, rt: rt,
		runner: &jobRunner{rt: rt, allowed: map[int64]bool{5: true}, goals: goals}}
}

func (g *goalRig) newGoal(t *testing.T, mutate func(*goal.Goal)) goal.Goal {
	t.Helper()
	x := goal.Goal{Gateway: "discord", ChatID: 5, Title: "Ship the report", Objective: "the report is in the shared folder",
		Plan: []goal.Step{{Text: "gather"}, {Text: "draft"}}, MaxCheckins: 3}
	if mutate != nil {
		mutate(&x)
	}
	created, err := g.goals.Add(x)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.rt.Cron.ScheduleGoal(created.ID, "discord", 5, created.Title, time.Hour, false, "", ""); err != nil {
		t.Fatal(err)
	}
	return created
}

func (g *goalRig) job(goalID string) cron.Job {
	return cron.Job{ID: "job1", Name: "Ship the report", Kind: cron.KindGoal, Goal: goalID, Gateway: "discord", ChatID: 5}
}

func TestACheckinIsAskedAboutTheGoalAsItStands(t *testing.T) {
	g := newGoalRig(t)
	created := g.newGoal(t, nil)
	out, err := g.runner.run(context.Background(), g.job(created.ID))
	if err != nil || out != "did the work" {
		t.Fatalf("%q %v", out, err)
	}
	prompt := g.model.user[0]
	for _, want := range []string{"check-in 1 of at most 3", "the report is in the shared folder", "1. [ ] gather", "[SILENT]"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q in:\n%s", want, prompt)
		}
	}
	got, _ := g.goals.GetAny(created.ID)
	if got.Checkins != 1 || got.LastCheckin.IsZero() {
		t.Fatalf("a check-in is counted: %+v", got)
	}
	g.runner.run(context.Background(), g.job(created.ID))
	if again, _ := g.goals.GetAny(created.ID); again.Checkins != 2 {
		t.Fatalf("%+v", again)
	}
	if !strings.Contains(g.model.user[1], "check-in 2 of at most 3") {
		t.Fatalf("each check-in knows which it is: %s", g.model.user[1])
	}
}

func TestACheckinCanUpdateGoalsButNotStartMoreWork(t *testing.T) {
	g := newGoalRig(t)
	created := g.newGoal(t, nil)
	g.runner.run(context.Background(), g.job(created.ID))
	tools := strings.Join(g.model.tools[0], ",")
	for _, want := range []string{"goal_update", "goal_get", "goal_list"} {
		if !strings.Contains(tools, want) {
			t.Errorf("a check-in needs %s: %s", want, tools)
		}
	}
	for _, never := range []string{"goal_create", "task_start", "cron_add"} {
		if strings.Contains(tools, never) {
			t.Errorf("unattended work must not start more work (%s): %s", never, tools)
		}
	}
	// And the note it is given says nobody is there.
	if !strings.Contains(g.model.system[0], "nobody watching") {
		t.Fatalf("%s", g.model.system[0])
	}
}

func TestCheckinsForGoalsThatAreNoLongerOpenAreDropped(t *testing.T) {
	g := newGoalRig(t)
	created := g.newGoal(t, nil)
	for _, status := range []string{goal.Paused, goal.Done, goal.Dropped} {
		g.goals.Update("discord", 5, created.ID, func(x *goal.Goal) error { x.Status = status; return nil })
		if _, err := g.runner.run(context.Background(), g.job(created.ID)); err != cron.ErrDrop {
			t.Fatalf("%s: %v", status, err)
		}
	}
	if _, err := g.runner.run(context.Background(), g.job("gone")); err != cron.ErrDrop {
		t.Fatalf("a goal that no longer exists: %v", err)
	}
	if g.model.calls() != 0 {
		t.Fatal("the model must not be run for a goal nobody wants worked on")
	}
}

// After its allowance of check-ins without the user, a goal stops and says so,
// rather than spending on, unasked.
func TestAGoalPausesWhenItsCheckinsAreUsedUp(t *testing.T) {
	g := newGoalRig(t)
	created := g.newGoal(t, func(x *goal.Goal) { x.MaxCheckins = 2 })
	for i := 0; i < 2; i++ {
		if _, err := g.runner.run(context.Background(), g.job(created.ID)); err != nil {
			t.Fatal(err)
		}
	}
	calls := g.model.calls()
	out, err := g.runner.run(context.Background(), g.job(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if g.model.calls() != calls {
		t.Fatal("the model must not run once the allowance is used up")
	}
	for _, want := range []string{"checked in on this 2 times", "paused it", "goals resume " + created.ID, "Ship the report"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	got, _ := g.goals.GetAny(created.ID)
	if got.Status != goal.Paused || !strings.Contains(got.Notes[len(got.Notes)-1].Text, "Paused after 2 check-ins") {
		t.Fatalf("%+v", got)
	}
	if _, ok := g.rt.Cron.NextCheckin(created.ID); ok {
		t.Fatal("a paused goal is not scheduled")
	}
}

func TestSpeakingToTheAgentGivesBackTheAllowance(t *testing.T) {
	g := newGoalRig(t)
	created := g.newGoal(t, func(x *goal.Goal) { x.MaxCheckins = 2 })
	g.runner.run(context.Background(), g.job(created.ID))
	g.runner.run(context.Background(), g.job(created.ID))
	g.goals.ResetCheckins("discord", 5) // what the gateway does when the user writes
	if _, err := g.runner.run(context.Background(), g.job(created.ID)); err != nil || g.model.calls() != 3 {
		t.Fatalf("%v %d", err, g.model.calls())
	}
}

func TestCheckinsRunOnTheModelAndPermissionsTheGoalWasGiven(t *testing.T) {
	g := newGoalRig(t)
	created := g.newGoal(t, func(x *goal.Goal) { x.AllowRisky = true })
	j := g.job(created.ID)
	j.AllowRisky = created.AllowRisky
	if _, err := g.runner.run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	// Only the owner's chat is served: one removed from the allowlist is dropped.
	g.runner.allowed = map[int64]bool{}
	if _, err := g.runner.run(context.Background(), j); err != cron.ErrDrop {
		t.Fatalf("%v", err)
	}
}

func TestATaskIsToldItIsATask(t *testing.T) {
	g := newGoalRig(t)
	j := cron.Job{ID: "t1", Name: "research", Kind: cron.KindTask, Prompt: "find the three best options", Gateway: "discord", ChatID: 5}
	if _, err := g.runner.run(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if g.model.user[0] != "find the three best options" {
		t.Fatalf("the task's own words: %q", g.model.user[0])
	}
	sys := g.model.system[0]
	if !strings.Contains(sys, "background task that the user started") || strings.Contains(sys, "[SILENT]") {
		t.Fatalf("a task always reports, so it is not offered a way to stay silent:\n%s", sys)
	}
	tools := strings.Join(g.model.tools[0], ",")
	if strings.Contains(tools, "task_start") || strings.Contains(tools, "cron_add") || strings.Contains(tools, "goal_create") {
		t.Fatalf("a task does not start more work: %s", tools)
	}
	// An ordinary scheduled job keeps its own wording.
	g.runner.run(context.Background(), cron.Job{ID: "c1", Name: "daily", Prompt: "summarise", Gateway: "discord", ChatID: 5})
	if !strings.Contains(g.model.system[1], "scheduled job") || !strings.Contains(g.model.system[1], "[SILENT]") {
		t.Fatalf("%s", g.model.system[1])
	}
}

func TestTasksCommand(t *testing.T) {
	store := &cron.Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	sched := &cron.Scheduler{Store: store}
	if got := tasksCommand(store, sched, "discord", 5, nil); !strings.Contains(got, "Nothing is running") {
		t.Fatal(got)
	}
	queued, _ := store.AddNow(cron.Job{Name: "research", Kind: cron.KindTask, Prompt: "p", Gateway: "discord", ChatID: 5})
	store.AddNow(cron.Job{Name: "not mine", Kind: cron.KindTask, Prompt: "p", Gateway: "discord", ChatID: 6})
	got := tasksCommand(store, sched, "discord", 5, nil)
	if !strings.Contains(got, queued.ID+"  research (task): about to start") || strings.Contains(got, "not mine") {
		t.Fatalf("%s", got)
	}
	if got := tasksCommand(store, sched, "discord", 5, []string{"cancel"}); !strings.Contains(got, "Say which") {
		t.Fatal(got)
	}
	if got := tasksCommand(store, sched, "discord", 5, []string{"cancel", queued.ID}); got != "Cancelled 1 task(s)." {
		t.Fatal(got)
	}
	if got := tasksCommand(store, sched, "discord", 5, []string{"cancel", "nope"}); !strings.Contains(got, "no such task") {
		t.Fatal(got)
	}
	if got := tasksCommand(store, sched, "discord", 6, []string{"cancel", "all"}); got != "Cancelled 1 task(s)." {
		t.Fatalf("%s", got)
	}
}

func TestGoalsCommand(t *testing.T) {
	g := newGoalRig(t)
	cmd := func(args ...string) string { return goalsCommand(g.goals, g.rt.Cron, "discord", 5, args) }
	if got := cmd(); !strings.Contains(got, "No goals yet") {
		t.Fatal(got)
	}
	created := g.newGoal(t, nil)
	if got := cmd(); !strings.Contains(got, created.ID+" | open | Ship the report | 0 of 2 steps") || !strings.Contains(got, "goals show ID") {
		t.Fatal(got)
	}
	if got := cmd("show", created.ID); !strings.Contains(got, "Objective: the report is in the shared folder") {
		t.Fatal(got)
	}
	if got := cmd("pause", created.ID); !strings.Contains(got, "[paused]") {
		t.Fatal(got)
	}
	if _, ok := g.rt.Cron.NextCheckin(created.ID); ok {
		t.Fatal("paused goals are not checked in on")
	}
	if got := cmd("resume", created.ID); !strings.Contains(got, "[open]") {
		t.Fatal(got)
	}
	if _, ok := g.rt.Cron.NextCheckin(created.ID); !ok {
		t.Fatal("resumed goals are")
	}
	if got := cmd("done", created.ID); !strings.Contains(got, "[done]") {
		t.Fatal(got)
	}
	if got := cmd("drop", "nope"); !strings.Contains(got, "no goal") {
		t.Fatal(got)
	}
	if got := cmd("explode", created.ID); !strings.Contains(got, "Use:") {
		t.Fatal(got)
	}
	// Another chat can do none of it.
	if got := goalsCommand(g.goals, g.rt.Cron, "discord", 6, []string{"drop", created.ID}); !strings.Contains(got, "no goal") {
		t.Fatalf("%s", got)
	}
}

func TestAgencyNoteOnlyMentionsWhatIsThere(t *testing.T) {
	home := t.TempDir()
	bare := app.Open(app.Config{Home: home, Provider: &spy{}, Model: "m"})
	defer bare.Close()
	note := agencyNote(bare)
	if !strings.Contains(note, "task_start") || strings.Contains(note, "goal_create") || strings.Contains(note, "vault") {
		t.Fatalf("%s", note)
	}
	full := newGoalRig(t)
	if n := agencyNote(full.rt); !strings.Contains(n, "goal_create") || strings.Contains(n, "vault") {
		t.Fatalf("%s", n)
	}
}
