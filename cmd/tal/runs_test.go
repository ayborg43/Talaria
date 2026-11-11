package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/provider"
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
