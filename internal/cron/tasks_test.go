package cron

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/tool"
)

func TestAddNowStoresAJobThatIsDueAtOnce(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	j, err := s.AddNow(Job{Name: "research", Kind: KindTask, Prompt: "look into it", ChatID: 7, TimeoutSec: 600})
	if err != nil {
		t.Fatal(err)
	}
	if j.Gateway != "telegram" || j.Kind != KindTask || !j.NextRun.Equal(utc("2026-10-20 10:00")) || j.TimeoutSec != 600 {
		t.Fatalf("%+v", j)
	}
	due, err := s.ClaimDue(utc("2026-10-20 10:00"), "telegram")
	if err != nil || len(due) != 1 || due[0].ID != j.ID {
		t.Fatalf("%+v %v", due, err)
	}
	if jobs, _ := s.List(); len(jobs) != 0 {
		t.Fatalf("a task runs once, and is gone once claimed: %+v", jobs)
	}
	for name, bad := range map[string]Job{
		"empty": {Prompt: " ", ChatID: 1}, "no chat": {Prompt: "p"}, "negative timeout": {Prompt: "p", ChatID: 1, TimeoutSec: -1},
		"long": {Prompt: strings.Repeat("x", maxPrompt+1), ChatID: 1},
	} {
		if _, err := s.AddNow(bad); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
}

func TestJobKindsAreValidated(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	if _, err := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1, Kind: "weird"}); err == nil {
		t.Fatal("unknown kind")
	}
	if _, err := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1, Kind: KindGoal}); err == nil {
		t.Fatal("a goal job needs its goal")
	}
	if _, err := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1, Kind: KindGoal, Goal: "g1"}); err != nil {
		t.Fatal(err)
	}
}

func TestGoalSchedulesAreReplacedNotDuplicated(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	if err := s.ScheduleGoal("g1", "discord", 5, "ship it", 6*time.Hour, true, "claude", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleGoal("g1", "discord", 5, "ship it", 2*time.Hour, true, "claude", ""); err != nil {
		t.Fatal(err)
	}
	s.ScheduleGoal("g2", "discord", 5, "other", time.Hour, false, "", "")
	jobs, _ := s.List()
	if len(jobs) != 2 {
		t.Fatalf("one schedule per goal: %+v", jobs)
	}
	var g1 Job
	for _, j := range jobs {
		if j.Goal == "g1" {
			g1 = j
		}
	}
	if g1.Schedule != "@every 2h0m0s" || g1.Kind != KindGoal || g1.Gateway != "discord" || g1.ChatID != 5 || !g1.AllowRisky || g1.Profile != "claude" {
		t.Fatalf("%+v", g1)
	}
	if next, ok := s.NextCheckin("g1"); !ok || !next.Equal(utc("2026-10-20 12:00")) {
		t.Fatalf("%v %v", next, ok)
	}
	if err := s.ScheduleGoal("g3", "discord", 5, "x", time.Second, false, "", ""); err == nil {
		t.Fatal("too frequent")
	}
	if err := s.UnscheduleGoal("g1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.NextCheckin("g1"); ok {
		t.Fatal("unscheduled")
	}
	if jobs, _ := s.List(); len(jobs) != 1 || jobs[0].Goal != "g2" {
		t.Fatalf("only that goal's schedule is removed: %+v", jobs)
	}
	if err := s.UnscheduleGoal("nope"); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryWordingFollowsTheKind(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		ok      bool
		want    string
		wantErr string
	}{
		{"", true, "⏰ n\n\nbody", ""},
		{"", false, "⏰ n failed: body", ""},
		{KindTask, true, "✅ Task n\n\nbody", ""},
		{KindTask, false, "⚠️ Task n failed: body", ""},
		{KindGoal, true, "🎯 n\n\nbody", ""},
		{KindGoal, false, "🎯 n: the check-in failed: body", ""},
	} {
		if got := say(Job{Name: "n", Kind: tc.kind}, tc.ok, "body"); got != tc.want {
			t.Errorf("%q ok=%v: %q, want %q", tc.kind, tc.ok, got, tc.want)
		}
	}
}

func TestAJobMayAskForALongerTimeButNotWithoutLimit(t *testing.T) {
	s := &Scheduler{Timeout: time.Minute, MaxTimeout: time.Hour}
	for _, tc := range []struct {
		sec  int
		want time.Duration
	}{{0, time.Minute}, {300, 5 * time.Minute}, {99999, time.Hour}, {-5, time.Minute}} {
		if got := s.timeoutFor(Job{TimeoutSec: tc.sec}); got != tc.want {
			t.Errorf("%ds: %s, want %s", tc.sec, got, tc.want)
		}
	}
	if (&Scheduler{}).timeoutFor(Job{TimeoutSec: 99999}) != 2*time.Hour || (&Scheduler{}).timeoutFor(Job{}) != 10*time.Minute {
		t.Fatal("defaults are 10 minutes, and at most 2 hours")
	}
}

func TestPerJobTimeoutApplies(t *testing.T) {
	h := newHarness(t, func(ctx context.Context, _ Job) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	h.sched.Timeout = time.Hour
	h.sched.MaxTimeout = 30 * time.Millisecond // the job asks for a second; this caps it
	h.store.AddNow(Job{Name: "short", Kind: KindTask, Prompt: "p", ChatID: 2, TimeoutSec: 1})
	start := time.Now()
	h.fireAt("2026-10-20 10:00")
	if time.Since(start) > 5*time.Second {
		t.Fatal("the job's own limit was not applied")
	}
	if len(h.got) != 1 || !strings.HasPrefix(h.got[0].text, "⚠️ Task short failed: it ran out of time") {
		t.Fatalf("%+v", h.got)
	}
}

// taskRig is a scheduler with a model that blocks until told to finish.
type taskRig struct {
	*harness
	release chan string
	started chan string
}

func newTaskRig(t *testing.T) *taskRig {
	r := &taskRig{release: make(chan string, 8), started: make(chan string, 8)}
	r.harness = newHarness(t, func(ctx context.Context, j Job) (string, error) {
		r.started <- j.ID
		select {
		case out := <-r.release:
			return out, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	r.sched.Gateway = "telegram"
	r.sched.now = func() time.Time { return utc("2026-10-20 10:00") }
	return r
}

func TestRunsListsWhatIsRunningAndWaiting(t *testing.T) {
	r := newTaskRig(t)
	r.sched.MaxParallel = 1
	a, _ := r.store.AddNow(Job{Name: "first", Kind: KindTask, Prompt: "p", ChatID: 1})
	b, _ := r.store.AddNow(Job{Name: "second", Kind: KindTask, Prompt: "p", ChatID: 1})
	r.sched.tick(context.Background())
	running := <-r.started // whichever won the one slot
	time.Sleep(20 * time.Millisecond)

	runs := r.sched.Runs("telegram", 1)
	if len(runs) != 2 {
		t.Fatalf("%+v", runs)
	}
	for _, x := range runs {
		if x.Kind != KindTask || x.Running != (x.ID == running) {
			t.Fatalf("one runs and the other waits its turn: %+v (running: %s)", runs, running)
		}
	}
	if (running != a.ID && running != b.ID) || runs[0].ID == runs[1].ID {
		t.Fatalf("%+v", runs)
	}
	r.release <- "a"
	r.release <- "b"
	r.sched.wg.Wait()
	if len(r.sched.Runs("telegram", 1)) != 0 {
		t.Fatal("finished jobs are not listed")
	}
}

func TestRunsAreKeptApartByChatAndService(t *testing.T) {
	r := newTaskRig(t)
	r.sched.MaxParallel = 4
	r.store.AddNow(Job{Name: "mine", Kind: KindTask, Prompt: "p", ChatID: 1})
	r.store.AddNow(Job{Name: "someone else's", Kind: KindTask, Prompt: "p", ChatID: 2})
	r.sched.tick(context.Background())
	<-r.started
	<-r.started
	time.Sleep(20 * time.Millisecond)
	if got := r.sched.Runs("telegram", 2); len(got) != 1 || got[0].Name != "someone else's" {
		t.Fatalf("a chat sees only its own: %+v", got)
	}
	if got := r.sched.Runs("telegram", 1); len(got) != 1 || got[0].Name != "mine" {
		t.Fatalf("%+v", got)
	}
	if len(r.sched.Runs("discord", 1)) != 0 {
		t.Fatal("and only its own service's")
	}
	r.release <- "a"
	r.release <- "b"
	r.sched.wg.Wait()
}

func TestCancelStopsARunningTaskAndSaysSo(t *testing.T) {
	r := newTaskRig(t)
	j, _ := r.store.AddNow(Job{Name: "long one", Kind: KindTask, Prompt: "p", ChatID: 1})
	r.sched.tick(context.Background())
	<-r.started

	if r.sched.Cancel("telegram", 2, j.ID) || r.sched.Cancel("discord", 1, j.ID) {
		t.Fatal("another chat, or service, cannot cancel it")
	}
	if !r.sched.Cancel("telegram", 1, j.ID) {
		t.Fatal("its own chat can")
	}
	r.sched.wg.Wait()
	if len(r.got) != 1 || r.got[0].text != "🛑 long one was cancelled." {
		t.Fatalf("%+v", r.got)
	}
	if r.sched.Cancel("telegram", 1, j.ID) {
		t.Fatal("it is no longer running")
	}
}

func TestCancelOfATaskWaitingForASlot(t *testing.T) {
	r := newTaskRig(t)
	r.sched.MaxParallel = 1
	a, _ := r.store.AddNow(Job{Name: "first", Kind: KindTask, Prompt: "p", ChatID: 1})
	b, _ := r.store.AddNow(Job{Name: "second", Kind: KindTask, Prompt: "p", ChatID: 1})
	names := map[string]string{a.ID: "first", b.ID: "second"}
	r.sched.tick(context.Background())
	running := <-r.started
	waiting := b.ID
	if running == b.ID {
		waiting = a.ID
	}
	time.Sleep(20 * time.Millisecond)
	if !r.sched.Cancel("telegram", 1, waiting) {
		t.Fatal("a waiting task can be cancelled")
	}
	time.Sleep(50 * time.Millisecond)
	r.release <- "the running one is done"
	r.sched.wg.Wait()
	var texts []string
	for _, d := range r.got {
		texts = append(texts, d.text)
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, names[waiting]+" was cancelled before it started") || !strings.Contains(joined, "the running one is done") {
		t.Fatalf("%v", texts)
	}
	select {
	case id := <-r.started:
		t.Fatalf("the cancelled task ran: %s", id)
	default:
	}
}

func TestWakeMakesTheSchedulerLookAtOnce(t *testing.T) {
	r := newTaskRig(t)
	r.sched.Tick = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.sched.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond) // the first look has happened, and found nothing
	r.store.AddNow(Job{Name: "now", Kind: KindTask, Prompt: "p", ChatID: 1})
	r.sched.Wake()
	r.sched.Wake() // coalesces, never blocks
	select {
	case <-r.started:
	case <-time.After(2 * time.Second):
		t.Fatal("a woken scheduler should start the task without waiting for its hour-long tick")
	}
	r.release <- "ok"
	cancel()
	<-done
}

func taskTools(t *testing.T, s *Store, ctl Control, targets Targets) *tool.Registry {
	reg := tool.NewRegistry()
	RegisterTaskTools(reg, s, ctl, "telegram", 7, targets)
	return reg
}

func runTool(t *testing.T, reg *tool.Registry, name, args string) (string, error) {
	t.Helper()
	tl, ok := reg.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	return tl.Run(context.Background(), json.RawMessage(args))
}

type fakeCtl struct {
	mu     sync.Mutex
	woken  int
	runs   []RunInfo
	cancel map[string]bool
}

func (f *fakeCtl) Wake()                                     { f.mu.Lock(); f.woken++; f.mu.Unlock() }
func (f *fakeCtl) Runs(gateway string, chat int64) []RunInfo { return f.runs }
func (f *fakeCtl) Cancel(gateway string, chat int64, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel == nil {
		f.cancel = map[string]bool{}
	}
	for _, r := range f.runs {
		if r.ID == id {
			f.cancel[id] = true
			return true
		}
	}
	return false
}

func TestTaskStart(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	ctl := &fakeCtl{}
	reg := taskTools(t, s, ctl, Targets{})
	tl, _ := reg.Get("task_start")
	if !tl.NeedsApproval {
		t.Fatal("a task acts on its own, so starting one is asked about")
	}
	out, err := runTool(t, reg, "task_start", `{"name":"report","task":"write the report","timeout_minutes":500,"allow_risky":true}`)
	if err != nil || !strings.Contains(out, "started task") || !strings.Contains(out, "up to 120 minutes") {
		t.Fatalf("%q %v", out, err)
	}
	if ctl.woken != 1 {
		t.Fatal("the scheduler is woken so the task starts now")
	}
	jobs, _ := s.List()
	if len(jobs) != 1 || jobs[0].Kind != KindTask || jobs[0].ChatID != 7 || jobs[0].TimeoutSec != 7200 || !jobs[0].AllowRisky || jobs[0].Prompt != "write the report" {
		t.Fatalf("%+v", jobs)
	}
	if out, _ := runTool(t, reg, "task_start", `{"task":"quick"}`); !strings.Contains(out, "up to 30 minutes") {
		t.Fatalf("the default is 30 minutes: %q", out)
	}
	if _, err := runTool(t, reg, "task_start", `{}`); err == nil {
		t.Fatal("a task needs instructions")
	}
}

func TestAChatHasAtMostThreeTasks(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	ctl := &fakeCtl{runs: []RunInfo{{ID: "r1", Name: "a", Kind: KindTask, Running: true}, {ID: "r2", Name: "b", Kind: KindTask}}}
	reg := taskTools(t, s, ctl, Targets{})
	if _, err := runTool(t, reg, "task_start", `{"task":"third"}`); err != nil {
		t.Fatal(err)
	}
	_, err := runTool(t, reg, "task_start", `{"task":"fourth"}`)
	if err == nil || !strings.Contains(err.Error(), "already has 3") {
		t.Fatalf("running, waiting and not-yet-picked-up tasks all count: %v", err)
	}
	// Other kinds of jobs do not count against it.
	s2 := newStore(t, utc("2026-10-20 10:00"))
	s2.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 7})
	s2.ScheduleGoal("g", "telegram", 7, "g", time.Hour, false, "", "")
	if _, err := runTool(t, taskTools(t, s2, &fakeCtl{}, Targets{}), "task_start", `{"task":"x"}`); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestTaskListAndCancel(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	ctl := &fakeCtl{runs: []RunInfo{{ID: "r1", Name: "research", Kind: KindTask, Running: true, Since: time.Now().Add(-90 * time.Second)},
		{ID: "g1", Name: "a goal", Kind: KindGoal, Running: true}}}
	pending, _ := s.AddNow(Job{Name: "queued", Kind: KindTask, Prompt: "p", ChatID: 7})
	s.AddNow(Job{Name: "not mine", Kind: KindTask, Prompt: "p", ChatID: 8})
	reg := taskTools(t, s, ctl, Targets{})

	out, _ := runTool(t, reg, "task_list", `{}`)
	if !strings.Contains(out, "r1 | research | running") || !strings.Contains(out, pending.ID+" | queued | about to start") || strings.Contains(out, "not mine") || strings.Contains(out, "a goal") {
		t.Fatalf("%s", out)
	}

	out, err := runTool(t, reg, "task_cancel", `{"id":"r1"}`)
	if err != nil || out != "cancelled 1 task(s)" || !ctl.cancel["r1"] {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := runTool(t, reg, "task_cancel", `{"id":"`+pending.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.List()
	for _, j := range jobs {
		if j.ID == pending.ID {
			t.Fatal("a task not yet picked up is removed")
		}
	}
	if _, err := runTool(t, reg, "task_cancel", `{"id":"nope"}`); err == nil {
		t.Fatal("no such task")
	}

	ctl.runs = []RunInfo{{ID: "r2", Kind: KindTask, Running: true}, {ID: "r3", Kind: KindTask}}
	s.AddNow(Job{Name: "again", Kind: KindTask, Prompt: "p", ChatID: 7})
	out, err = runTool(t, reg, "task_cancel", `{"id":"all"}`)
	if err != nil || out != "cancelled 3 task(s)" {
		t.Fatalf("%q %v", out, err)
	}
	if empty, _ := runTool(t, taskTools(t, newStore(t, utc("2026-10-20 10:00")), &fakeCtl{}, Targets{}), "task_list", `{}`); !strings.Contains(empty, "no background tasks") {
		t.Fatal(empty)
	}
}

func TestTaskStartUsesTheChatsModelAndEnvironment(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	profile, env := "claude", "sandbox"
	reg := taskTools(t, s, &fakeCtl{}, chatTargets(&profile, &env))
	if _, err := runTool(t, reg, "task_start", `{"task":"x"}`); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.List()
	if jobs[0].Profile != "claude" || jobs[0].Environment != "sandbox" {
		t.Fatalf("%+v", jobs[0])
	}
	if _, err := runTool(t, reg, "task_start", `{"task":"x","profile":"nonexistent"}`); err == nil {
		t.Fatal("only what the chat could switch to")
	}
	spec, _ := reg.Get("task_start")
	if !strings.Contains(string(spec.Schema), `"profile"`) || !strings.Contains(string(spec.Schema), `"environment"`) {
		t.Fatalf("%s", spec.Schema)
	}
}

func TestWatchJobsAreValidatedScheduledAndUnscheduled(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	if _, err := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1, Kind: KindWatch}); err == nil {
		t.Fatal("a watch job needs its watch")
	}
	if err := s.ScheduleWatch("w1", "discord", 5, "mail", 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.ScheduleWatch("w1", "discord", 5, "mail", 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	s.ScheduleWatch("w2", "discord", 5, "other", time.Hour)
	jobs, _ := s.List()
	if len(jobs) != 2 {
		t.Fatalf("one schedule per watch: %+v", jobs)
	}
	var w1 Job
	for _, j := range jobs {
		if j.Watch == "w1" {
			w1 = j
		}
	}
	if w1.Schedule != "@every 30m0s" || w1.Kind != KindWatch || w1.Gateway != "discord" || w1.ChatID != 5 || w1.AllowRisky || w1.TimeoutSec != int(WatchPollTimeout/time.Second) {
		t.Fatalf("%+v", w1)
	}
	if next, ok := s.NextPoll("w1"); !ok || !next.Equal(utc("2026-10-20 10:30")) {
		t.Fatalf("%v %v", next, ok)
	}
	if err := s.ScheduleWatch("w3", "discord", 5, "x", time.Minute); err == nil {
		t.Fatal("too frequent")
	}
	if err := s.UnscheduleWatch("w1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.NextPoll("w1"); ok {
		t.Fatal("unscheduled")
	}
	if jobs, _ := s.List(); len(jobs) != 1 || jobs[0].Watch != "w2" {
		t.Fatalf("only that watch's schedule is removed: %+v", jobs)
	}
	if err := s.UnscheduleWatch("nope"); err != nil {
		t.Fatal(err)
	}
	// A watch with the same id as a goal does not take the goal's schedule away.
	s.ScheduleGoal("w2", "discord", 5, "g", time.Hour, false, "", "")
	s.UnscheduleWatch("w2")
	if _, ok := s.NextCheckin("w2"); !ok {
		t.Fatal("goals and watches are separate")
	}
}

func TestWatchDeliveryWording(t *testing.T) {
	if got := say(Job{Name: "n", Kind: KindWatch}, true, "body"); got != "👀 n\n\nbody" {
		t.Fatalf("%q", got)
	}
	if got := say(Job{Name: "n", Kind: KindWatch}, false, "body"); got != "👀 n: looking at the change failed: body" {
		t.Fatalf("%q", got)
	}
}
