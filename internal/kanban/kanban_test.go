package kanban

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/cron"
	"talaria/internal/tool"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "sessions.db")}
}

func mustAdd(t *testing.T, s *Store, c Card) Card {
	t.Helper()
	got, err := s.Add("web", 1, c)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAddAndList(t *testing.T) {
	s := testStore(t)
	a := mustAdd(t, s, Card{Title: "  First   card ", Body: "do it"})
	b := mustAdd(t, s, Card{Title: "Second", Column: Ready})
	if a.Column != Backlog || a.Title != "First card" || a.ID == "" {
		t.Fatalf("bad card: %+v", a)
	}
	cards, err := s.List("web", 1)
	if err != nil || len(cards) != 2 || cards[0].ID != a.ID || cards[1].ID != b.ID {
		t.Fatalf("list = %+v, %v (columns should come in board order)", cards, err)
	}
	if other, _ := s.List("web", 2); len(other) != 0 {
		t.Fatal("another chat saw this board")
	}
	if other, _ := s.List("telegram", 1); len(other) != 0 {
		t.Fatal("another service saw this board")
	}
	// State is on disk: a second Store on the same file sees it.
	if again, _ := (&Store{Path: s.Path}).List("web", 1); len(again) != 2 {
		t.Fatalf("second store saw %d cards", len(again))
	}
}

func TestAddRefusesBadCards(t *testing.T) {
	s := testStore(t)
	for name, c := range map[string]Card{
		"no title":    {Title: "  "},
		"long title":  {Title: strings.Repeat("x", MaxTitleRunes+1)},
		"long body":   {Title: "x", Body: strings.Repeat("y", MaxBodyRunes+1)},
		"unknown col": {Title: "x", Column: "later"},
		"running":     {Title: "x", Column: Running},
		"long agent":  {Title: "x", Agent: strings.Repeat("a", MaxAgentRunes+1)},
	} {
		if _, err := s.Add("web", 1, c); err == nil {
			t.Errorf("%s: was kept", name)
		}
	}
}

func TestBoardIsBounded(t *testing.T) {
	s := testStore(t)
	for range MaxCards {
		mustAdd(t, s, Card{Title: "x"})
	}
	if _, err := s.Add("web", 1, Card{Title: "one more"}); err == nil {
		t.Fatal("the board grew past its limit")
	}
	if _, err := s.Add("web", 2, Card{Title: "another chat"}); err != nil {
		t.Fatalf("one chat's full board stopped another's: %v", err)
	}
}

func TestMoveRules(t *testing.T) {
	s := testStore(t)
	c := mustAdd(t, s, Card{Title: "x", Agent: Main})
	if _, err := s.Move("web", 1, c.ID, Running); err == nil {
		t.Fatal("a card was moved to Running by hand")
	}
	if _, err := s.Move("web", 1, c.ID, "later"); err == nil {
		t.Fatal("a made-up column was accepted")
	}
	got, err := s.Move("web", 1, c.ID, "Review")
	if err != nil || got.Column != Review {
		t.Fatalf("move to review: %+v, %v", got, err)
	}
	if _, err := s.Move("web", 2, c.ID, Done); err == nil {
		t.Fatal("another chat moved this card")
	}

	// A card that is running can neither be moved, edited nor removed.
	s.Move("web", 1, c.ID, Ready)
	claimed, ok, err := s.Claim(c.ID)
	if err != nil || !ok || claimed.Column != Running {
		t.Fatalf("claim: %+v %v %v", claimed, ok, err)
	}
	if _, err := s.Move("web", 1, c.ID, Done); err == nil {
		t.Fatal("a running card was moved")
	}
	if _, err := s.Edit("web", 1, c.ID, func(c *Card) { c.Title = "changed" }); err == nil {
		t.Fatal("a running card was edited")
	}
	if err := s.Delete("web", 1, c.ID); err == nil {
		t.Fatal("a running card was removed")
	}
}

func TestEditOnlyChangesItsFields(t *testing.T) {
	s := testStore(t)
	c := mustAdd(t, s, Card{Title: "x"})
	got, err := s.Edit("web", 1, c.ID, func(c *Card) {
		c.Title, c.Agent, c.AllowRisky = "y", "Writer", true
		c.Column, c.Job, c.Result = Done, "job", "forged" // not the caller's to change
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "y" || got.Agent != "Writer" || !got.AllowRisky || got.Column != Backlog || got.Job != "" || got.Result != "" {
		t.Fatalf("edit changed the wrong things: %+v", got)
	}
	if _, err := s.Edit("web", 1, c.ID, func(c *Card) { c.Title = "" }); err == nil {
		t.Fatal("a card was left without a title")
	}
}

func TestClaimIsOnce(t *testing.T) {
	s := testStore(t)
	c := mustAdd(t, s, Card{Title: "x", Column: Ready, Agent: Main})
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, _ := s.Claim(c.ID); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d claims won the same card", wins)
	}
}

func TestMoveToReadyClearsLastRun(t *testing.T) {
	s := testStore(t)
	c := mustAdd(t, s, Card{Title: "x", Column: Ready, Agent: Main})
	s.Claim(c.ID)
	s.Attach(c.ID, "job1")
	if _, ok := s.Finish(c.ID, "job1", StatusFailed, "boom"); !ok {
		t.Fatal("finish did not move the card")
	}
	got, _ := s.Get("web", 1, c.ID)
	if got.Column != Review || got.Status != StatusFailed || got.Result != "boom" || got.Job != "job1" {
		t.Fatalf("after finish: %+v", got)
	}
	got, err := s.Move("web", 1, c.ID, Ready)
	if err != nil || got.Status != "" || got.Result != "" || got.Job != "" {
		t.Fatalf("a second go kept the last run: %+v, %v", got, err)
	}
}

func TestFinishOnlyForTheTaskThatOwnsTheCard(t *testing.T) {
	s := testStore(t)
	c := mustAdd(t, s, Card{Title: "x", Column: Ready, Agent: Main})
	s.Claim(c.ID)
	s.Attach(c.ID, "job2")
	if _, ok := s.Finish(c.ID, "old-job", "", "late news"); ok {
		t.Fatal("an older task settled the card")
	}
	if _, ok := s.Finish("", "job2", "", "x"); ok {
		t.Fatal("finish without a card did something")
	}
	if got, _ := s.Get("web", 1, c.ID); got.Column != Running {
		t.Fatalf("card moved: %+v", got)
	}
	if _, ok := s.Finish(c.ID, "job2", "", "done it"); !ok {
		t.Fatal("the owning task could not settle it")
	}
	if _, ok := s.Finish(c.ID, "job2", "", "again"); ok {
		t.Fatal("a settled card was settled twice")
	}
	if got, _ := s.Get("web", 1, c.ID); got.Result != "done it" {
		t.Fatalf("result = %q", got.Result)
	}
}

func TestFinishBeforeAttach(t *testing.T) {
	// A task that is quicker than the sweep that started it.
	s := testStore(t)
	c := mustAdd(t, s, Card{Title: "x", Column: Ready, Agent: Main})
	s.Claim(c.ID)
	if _, ok := s.Finish(c.ID, "fast", "", "ok"); !ok {
		t.Fatal("a card not yet attached to its task was not settled")
	}
	s.Attach(c.ID, "fast")
	if got, _ := s.Get("web", 1, c.ID); got.Column != Review || got.Job != "fast" {
		t.Fatalf("a late attach disturbed the card: %+v", got)
	}
}

// ---- dispatcher ----

type fakeControl struct {
	mu        sync.Mutex
	woken     int
	running   []cron.RunInfo
	cancelled []string
}

func (f *fakeControl) Wake()                             { f.mu.Lock(); f.woken++; f.mu.Unlock() }
func (f *fakeControl) Runs(string, int64) []cron.RunInfo { return f.running }
func (f *fakeControl) Cancel(_ string, _ int64, id string) bool {
	f.cancelled = append(f.cancelled, id)
	return true
}

type rig struct {
	cards *Store
	jobs  *cron.Store
	d     *Dispatcher
	ctl   *fakeControl
}

func newRig(t *testing.T, agents ...string) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{
		cards: &Store{Path: filepath.Join(dir, "sessions.db")},
		jobs:  &cron.Store{Path: filepath.Join(dir, "cron", "jobs.json")},
		ctl:   &fakeControl{},
	}
	r.d = &Dispatcher{Cards: r.cards, Jobs: r.jobs, Known: func(n string) bool {
		for _, a := range agents {
			if strings.EqualFold(a, n) {
				return true
			}
		}
		return false
	}}
	return r
}

func (r *rig) taskJobs(t *testing.T) []cron.Job {
	t.Helper()
	all, err := r.jobs.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []cron.Job
	for _, j := range all {
		if j.Kind == cron.KindTask {
			out = append(out, j)
		}
	}
	return out
}

func TestSweepStartsAReadyCardAsATask(t *testing.T) {
	r := newRig(t, "Researcher")
	c := mustAdd(t, r.cards, Card{Title: "Find three flights", Body: "Lagos to Accra, Friday", Column: Ready, Agent: "Researcher", AllowRisky: true})
	r.d.Sweep("web", r.ctl)

	jobs := r.taskJobs(t)
	if len(jobs) != 1 {
		t.Fatalf("%d tasks started, want 1", len(jobs))
	}
	j := jobs[0]
	if j.Card != c.ID || j.Agent != "Researcher" || !j.AllowRisky || j.Gateway != "web" || j.ChatID != 1 {
		t.Fatalf("task = %+v", j)
	}
	if !strings.Contains(j.Prompt, "Find three flights") || !strings.Contains(j.Prompt, "Lagos to Accra") {
		t.Fatalf("the task was not told the card: %q", j.Prompt)
	}
	got, _ := r.cards.Get("web", 1, c.ID)
	if got.Column != Running || got.Job != j.ID {
		t.Fatalf("card = %+v, want running with job %s", got, j.ID)
	}
	if r.ctl.woken == 0 {
		t.Fatal("the scheduler was not woken")
	}
	// A second sweep must not start it again.
	r.d.Sweep("web", r.ctl)
	if n := len(r.taskJobs(t)); n != 1 {
		t.Fatalf("a second sweep made %d tasks", n)
	}
}

func TestSweepMainAgentRunsWithoutASavedOne(t *testing.T) {
	r := newRig(t)
	mustAdd(t, r.cards, Card{Title: "x", Column: Ready, Agent: Main})
	r.d.Sweep("web", r.ctl)
	jobs := r.taskJobs(t)
	if len(jobs) != 1 || jobs[0].Agent != "" {
		t.Fatalf("jobs = %+v (main should name no saved agent)", jobs)
	}
}

func TestSweepLeavesCardsWithNoAgent(t *testing.T) {
	r := newRig(t)
	c := mustAdd(t, r.cards, Card{Title: "x", Column: Ready})
	r.d.Sweep("web", r.ctl)
	if n := len(r.taskJobs(t)); n != 0 {
		t.Fatalf("a card with nobody to do it started %d tasks", n)
	}
	if got, _ := r.cards.Get("web", 1, c.ID); got.Column != Ready {
		t.Fatalf("card = %+v", got)
	}
}

func TestSweepStopsACardWhoseAgentIsGone(t *testing.T) {
	r := newRig(t) // no saved agents
	c := mustAdd(t, r.cards, Card{Title: "x", Column: Ready, Agent: "Deleted"})
	r.d.Sweep("web", r.ctl)
	if n := len(r.taskJobs(t)); n != 0 {
		t.Fatalf("a removed agent's card started %d tasks", n)
	}
	got, _ := r.cards.Get("web", 1, c.ID)
	if got.Column != Review || got.Status != StatusFailed || !strings.Contains(got.Result, "Deleted") {
		t.Fatalf("card = %+v", got)
	}
}

func TestSweepKeepsTheLimitOfThreeTasksPerChat(t *testing.T) {
	r := newRig(t)
	var ids []string
	for range 5 {
		ids = append(ids, mustAdd(t, r.cards, Card{Title: "x", Column: Ready, Agent: Main}).ID)
		time.Sleep(time.Millisecond) // placed in order
	}
	// Another chat's card is not held up by the first chat being full.
	other, _ := r.cards.Add("web", 2, Card{Title: "other chat", Column: Ready, Agent: Main})
	r.d.Sweep("web", r.ctl)

	running := 0
	for _, id := range ids {
		if c, _ := r.cards.Get("web", 1, id); c.Column == Running {
			running++
		}
	}
	if running != 3 {
		t.Fatalf("%d cards running in one chat, want 3", running)
	}
	if c, _ := r.cards.Get("web", 1, ids[3]); c.Column != Ready {
		t.Fatalf("the fourth card should wait, got %+v", c)
	}
	if c, _ := r.cards.Get("web", 2, other.ID); c.Column != Running {
		t.Fatalf("another chat's card was held up: %+v", c)
	}
	if n := len(r.taskJobs(t)); n != 4 {
		t.Fatalf("%d tasks, want 4 (3 + the other chat's)", n)
	}

	// The first three go in the order they were placed.
	for i, id := range ids[:3] {
		if c, _ := r.cards.Get("web", 1, id); c.Column != Running {
			t.Fatalf("card %d was passed over: %+v", i, c)
		}
	}

	// One finishes: the next waiting card starts at the next sweep.
	first, _ := r.cards.Get("web", 1, ids[0])
	r.d.Finished(cron.Job{ID: first.Job, Card: first.ID}, "ok", "done")
	// The job is still in the store in this test, as it is until the scheduler records it; remove it as the scheduler would.
	r.jobs.Remove(first.Job, "", 0)
	r.d.Sweep("web", r.ctl)
	if c, _ := r.cards.Get("web", 1, ids[3]); c.Column != Running {
		t.Fatalf("a freed place was not used: %+v", c)
	}
}

func TestFinishedMovesTheCardToReview(t *testing.T) {
	r := newRig(t)
	c := mustAdd(t, r.cards, Card{Title: "x", Column: Ready, Agent: Main})
	r.d.Sweep("web", r.ctl)
	j := r.taskJobs(t)[0]

	cases := []struct{ status, result, wantStatus, wantResult string }{
		{"ok", "Three flights found.", "", "Three flights found."},
		{"error", "the site was down", StatusFailed, "the site was down"},
		{"cancelled", "", StatusCancelled, "stopped"},
		{"dropped", "", StatusLost, "dropped"},
	}
	for _, tc := range cases {
		r.cards.Move("web", 1, c.ID, Ready)
		r.cards.Claim(c.ID)
		r.cards.Attach(c.ID, j.ID)
		r.d.Finished(cron.Job{ID: j.ID, Card: c.ID}, tc.status, tc.result)
		got, _ := r.cards.Get("web", 1, c.ID)
		if got.Column != Review || got.Status != tc.wantStatus || !strings.Contains(got.Result, tc.wantResult) {
			t.Errorf("%s: card = %+v", tc.status, got)
		}
	}
	// A job with no card does nothing and does not panic.
	r.d.Finished(cron.Job{ID: "x"}, "ok", "x")
	var none *Dispatcher
	none.Finished(cron.Job{ID: "x", Card: "y"}, "ok", "x")
	none.Sweep("web", nil)
}

func TestSweepSettlesACardWhoseTaskVanished(t *testing.T) {
	r := newRig(t)
	c := mustAdd(t, r.cards, Card{Title: "x", Column: Ready, Agent: Main})
	r.d.Sweep("web", r.ctl)
	j := r.taskJobs(t)[0]

	// While the task exists the card stays running.
	r.d.Sweep("web", r.ctl)
	if got, _ := r.cards.Get("web", 1, c.ID); got.Column != Running {
		t.Fatalf("a live task's card moved: %+v", got)
	}
	// The task is removed before it started (or a restart ended it): the card must not stay Running for ever.
	r.jobs.Remove(j.ID, "", 0)
	r.d.Sweep("web", r.ctl)
	got, _ := r.cards.Get("web", 1, c.ID)
	if got.Column != Review || got.Status != StatusLost {
		t.Fatalf("card = %+v", got)
	}
}

func TestSweepReleasesAClaimThatNeverGotItsTask(t *testing.T) {
	now := time.Now()
	r := newRig(t)
	r.cards.Now = func() time.Time { return now }
	c := mustAdd(t, r.cards, Card{Title: "x", Column: Ready, Agent: Main})
	r.cards.Claim(c.ID) // a crash between claiming and making the task
	r.d.Sweep("web", &fakeControl{})
	if got, _ := r.cards.Get("web", 1, c.ID); got.Column != Running {
		t.Fatalf("a fresh claim was undone too soon: %+v", got)
	}
	r.cards.Now = func() time.Time { return now.Add(3 * time.Minute) }
	r.d.Sweep("web", r.ctl)
	got, _ := r.cards.Get("web", 1, c.ID)
	if got.Column != Running || got.Job == "" {
		t.Fatalf("the card was not given its task after the stale claim: %+v", got)
	}
}

// ---- tools ----

type toolRig struct {
	*rig
	reg  *tool.Registry
	kick int
	stop []string
}

func newToolRig(t *testing.T, create bool, agents ...string) *toolRig {
	t.Helper()
	tr := &toolRig{rig: newRig(t, agents...), reg: tool.NewRegistry()}
	RegisterTools(tr.reg, Binding{
		Store: tr.cards, Gateway: "web", ChatID: 1, Create: create,
		Agents: func() []string { return agents },
		Kick:   func() { tr.kick++; tr.d.Sweep("web", tr.ctl) },
		Stop:   func(job string) bool { tr.stop = append(tr.stop, job); return true },
	})
	return tr
}

func (tr *toolRig) call(t *testing.T, name string, args string) (string, error) {
	t.Helper()
	tl, ok := tr.reg.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return tl.Run(context.Background(), json.RawMessage(args))
}

func (tr *toolRig) needsApproval(t *testing.T, name, args string) bool {
	t.Helper()
	tl, _ := tr.reg.Get(name)
	return tl.NeedsApproval || (tl.NeedsApprovalFor != nil && tl.NeedsApprovalFor(json.RawMessage(args)))
}

func TestOnlyListWhenNotCreating(t *testing.T) {
	tr := newToolRig(t, false)
	if _, ok := tr.reg.Get("kanban_list"); !ok {
		t.Fatal("a job cannot even read the board")
	}
	for _, n := range []string{"kanban_add", "kanban_move", "kanban_remove"} {
		if _, ok := tr.reg.Get(n); ok {
			t.Fatalf("%s is offered to work that may not start more work", n)
		}
	}
}

func TestToolsAddMoveList(t *testing.T) {
	tr := newToolRig(t, true, "Researcher")
	out, err := tr.call(t, "kanban_add", `{"title":"Compare phone plans","body":"three cheapest","agent":"researcher"}`)
	if err != nil || !strings.Contains(out, "backlog") {
		t.Fatalf("add: %q %v", out, err)
	}
	cards, _ := tr.cards.List("web", 1)
	if len(cards) != 1 || cards[0].Column != Backlog || cards[0].Agent != "Researcher" {
		t.Fatalf("cards = %+v (agent should be stored as saved)", cards)
	}
	if n := len(tr.taskJobs(t)); n != 0 {
		t.Fatal("a backlog card started a task")
	}

	list, err := tr.call(t, "kanban_list", `{}`)
	if err != nil || !strings.Contains(list, "Backlog") || !strings.Contains(list, "Compare phone plans") || !strings.Contains(list, cards[0].ID) {
		t.Fatalf("list = %q %v", list, err)
	}

	out, err = tr.call(t, "kanban_move", `{"id":"`+cards[0].ID+`","column":"ready"}`)
	if err != nil || !strings.Contains(out, "running") {
		t.Fatalf("move to ready: %q %v", out, err)
	}
	if n := len(tr.taskJobs(t)); n != 1 {
		t.Fatalf("%d tasks after moving to ready", n)
	}
	list, _ = tr.call(t, "kanban_list", `{"column":"running"}`)
	if !strings.Contains(list, "Running") || strings.Contains(list, "Backlog") {
		t.Fatalf("filtered list = %q", list)
	}
}

func TestToolsAskBeforeStartingOrStopping(t *testing.T) {
	tr := newToolRig(t, true, "Researcher")
	c := mustAdd(t, tr.cards, Card{Title: "x"})
	for _, tc := range []struct {
		tool, args string
		want       bool
	}{
		{"kanban_add", `{"title":"x"}`, false},
		{"kanban_add", `{"title":"x","column":"backlog"}`, false},
		{"kanban_add", `{"title":"x","column":"ready","agent":"Researcher"}`, true},
		{"kanban_add", `{"title":"x","allow_risky":true}`, true},
		{"kanban_move", `{"id":"` + c.ID + `","column":"review"}`, false},
		{"kanban_move", `{"id":"` + c.ID + `","column":"ready"}`, true},
		{"kanban_move", `{"id":"` + c.ID + `","column":"review","allow_risky":true}`, true},
		{"kanban_remove", `{"id":"` + c.ID + `"}`, true},
	} {
		if got := tr.needsApproval(t, tc.tool, tc.args); got != tc.want {
			t.Errorf("%s %s: needs approval = %v, want %v", tc.tool, tc.args, got, tc.want)
		}
	}
	// Stopping work asks, as task_cancel does.
	s := tr.cards
	s.Move("web", 1, c.ID, Ready)
	s.Edit("web", 1, c.ID, func(c *Card) { c.Agent = Main })
	s.Claim(c.ID)
	if !tr.needsApproval(t, "kanban_move", `{"id":"`+c.ID+`","column":"backlog"}`) {
		t.Error("stopping a running card did not ask")
	}
}

func TestToolsRefuseWhatTheBoardRefuses(t *testing.T) {
	tr := newToolRig(t, true, "Researcher")
	for _, args := range []string{
		`{"title":"x","agent":"Nobody"}`,
		`{"title":"x","column":"running"}`,
		`{"title":"x","column":"done"}`,
		`{"title":""}`,
	} {
		if _, err := tr.call(t, "kanban_add", args); err == nil {
			t.Errorf("kanban_add %s was accepted", args)
		}
	}
	c := mustAdd(t, tr.cards, Card{Title: "x"})
	for _, args := range []string{
		`{"id":"` + c.ID + `","column":"running"}`,
		`{"id":"nope","column":"done"}`,
		`{"id":"` + c.ID + `","column":"ready","agent":"Nobody"}`,
		`{"id":"","column":"done"}`,
	} {
		if _, err := tr.call(t, "kanban_move", args); err == nil {
			t.Errorf("kanban_move %s was accepted", args)
		}
	}
	if n := len(tr.taskJobs(t)); n != 0 {
		t.Fatalf("refused calls still started %d tasks", n)
	}
}

func TestMoveReadyWithNoAgentWaits(t *testing.T) {
	tr := newToolRig(t, true, "Researcher")
	c := mustAdd(t, tr.cards, Card{Title: "x"})
	out, err := tr.call(t, "kanban_move", `{"id":"`+c.ID+`","column":"ready"}`)
	if err != nil || !strings.Contains(out, "no agent") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if n := len(tr.taskJobs(t)); n != 0 {
		t.Fatal("a card nobody was picked for started")
	}
	// Picking an agent while moving starts it.
	out, err = tr.call(t, "kanban_move", `{"id":"`+c.ID+`","column":"ready","agent":"Researcher"}`)
	if err != nil || !strings.Contains(out, "running") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestMovingARunningCardStopsItsTask(t *testing.T) {
	tr := newToolRig(t, true)
	c := mustAdd(t, tr.cards, Card{Title: "x", Column: Ready, Agent: Main})
	tr.d.Sweep("web", tr.ctl)
	running, _ := tr.cards.Get("web", 1, c.ID)
	out, err := tr.call(t, "kanban_move", `{"id":"`+c.ID+`","column":"backlog"}`)
	if err != nil || !strings.Contains(out, "stop") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if len(tr.stop) != 1 || tr.stop[0] != running.Job {
		t.Fatalf("stopped %v, want its task %s", tr.stop, running.Job)
	}
	if got, _ := tr.cards.Get("web", 1, c.ID); got.Column != Running {
		t.Fatal("the card moved before its task had stopped")
	}
	if _, err := tr.call(t, "kanban_remove", `{"id":"`+c.ID+`"}`); err == nil {
		t.Fatal("a running card was removed")
	}
}

func TestRemoveTool(t *testing.T) {
	tr := newToolRig(t, true)
	c := mustAdd(t, tr.cards, Card{Title: "x"})
	if _, err := tr.call(t, "kanban_remove", `{"id":"`+c.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if cards, _ := tr.cards.List("web", 1); len(cards) != 0 {
		t.Fatalf("cards = %+v", cards)
	}
	if out, _ := tr.call(t, "kanban_list", `{}`); !strings.Contains(out, "empty") {
		t.Fatalf("list = %q", out)
	}
}
