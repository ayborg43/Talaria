package goal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/cron"
	"talaria/internal/tool"
)

func utc(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

type env struct {
	goals *Store
	jobs  *cron.Store
	reg   *tool.Registry
	now   time.Time
}

func newEnv(t *testing.T, create bool) *env {
	t.Helper()
	e := &env{now: utc("2026-10-20 10:00")}
	dir := t.TempDir()
	clock := func() time.Time { return e.now }
	e.goals = &Store{Path: filepath.Join(dir, "goals.json"), Now: clock}
	e.jobs = &cron.Store{Path: filepath.Join(dir, "jobs.json"), Now: clock}
	e.reg = tool.NewRegistry()
	RegisterTools(e.reg, Binding{Store: e.goals, Sched: e.jobs, Gateway: "discord", ChatID: 5, Create: create})
	return e
}

func (e *env) run(t *testing.T, name, args string) (string, error) {
	t.Helper()
	tl, ok := e.reg.Get(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return tl.Run(context.Background(), json.RawMessage(args))
}

func (e *env) ok(t *testing.T, name, args string) string {
	t.Helper()
	out, err := e.run(t, name, args)
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

func (e *env) onlyGoal(t *testing.T) Goal {
	t.Helper()
	gs, err := e.goals.List("discord", 5)
	if err != nil || len(gs) != 1 {
		t.Fatalf("%+v %v", gs, err)
	}
	return gs[0]
}

func TestCreateSchedulesCheckins(t *testing.T) {
	e := newEnv(t, true)
	out := e.ok(t, "goal_create", `{"title":"Ship the report","objective":"The Q3 report is in the shared folder","plan":["gather numbers","draft","send"],"check_in_minutes":120,"max_checkins":8,"allow_risky":true}`)
	g := e.onlyGoal(t)
	if !strings.Contains(out, g.ID) || !strings.Contains(out, "every 2h0m0s") || !strings.Contains(out, "up to 8 times") {
		t.Fatalf("%s", out)
	}
	if g.Status != Open || g.Every != 2*time.Hour || g.MaxCheckins != 8 || !g.AllowRisky || g.Next != "gather numbers" || len(g.Plan) != 3 || g.Gateway != "discord" || g.ChatID != 5 {
		t.Fatalf("%+v", g)
	}
	jobs, _ := e.jobs.List()
	if len(jobs) != 1 || jobs[0].Kind != cron.KindGoal || jobs[0].Goal != g.ID || jobs[0].Schedule != "@every 2h0m0s" || !jobs[0].AllowRisky || jobs[0].ChatID != 5 || jobs[0].Gateway != "discord" {
		t.Fatalf("%+v", jobs)
	}
	if !jobs[0].NextRun.Equal(utc("2026-10-20 12:00")) {
		t.Fatalf("the first check-in is one interval from now: %v", jobs[0].NextRun)
	}
	tl, _ := e.reg.Get("goal_create")
	if !tl.NeedsApproval {
		t.Fatal("starting autonomous work is asked about")
	}
	if d := tl.Describe([]byte(`{"title":"Ship","check_in_minutes":120,"max_checkins":8,"allow_risky":true}`)); !strings.Contains(d, "every 2h0m0s") || !strings.Contains(d, "up to 8 times") || !strings.Contains(d, "tools that normally ask") {
		t.Fatalf("%q", d)
	}
}

func TestCreateDefaultsAndLimits(t *testing.T) {
	e := newEnv(t, true)
	e.ok(t, "goal_create", `{"title":"x","objective":"y"}`)
	g := e.onlyGoal(t)
	if g.Every != 6*time.Hour || g.MaxCheckins != 20 || g.AllowRisky {
		t.Fatalf("%+v", g)
	}
	for name, args := range map[string]string{
		"no title":       `{"objective":"y"}`,
		"no objective":   `{"title":"x"}`,
		"too frequent":   `{"title":"x","objective":"y","check_in_minutes":5}`,
		"too rare":       `{"title":"x","objective":"y","check_in_minutes":99999}`,
		"huge budget":    `{"title":"x","objective":"y","max_checkins":1000}`,
		"empty step":     `{"title":"x","objective":"y","plan":["ok",""]}`,
		"long title":     `{"title":"` + strings.Repeat("t", 101) + `","objective":"y"}`,
		"too many steps": `{"title":"x","objective":"y","plan":[` + strings.TrimSuffix(strings.Repeat(`"s",`, 21), ",") + `]}`,
	} {
		if _, err := e.run(t, "goal_create", args); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	if gs, _ := e.goals.List("discord", 5); len(gs) != 1 {
		t.Fatalf("rejected goals are not kept: %d", len(gs))
	}
	if jobs, _ := e.jobs.List(); len(jobs) != 1 {
		t.Fatalf("and have no schedule: %d", len(jobs))
	}
}

func TestAChatHasAtMostFiveOpenGoals(t *testing.T) {
	e := newEnv(t, true)
	for i := 0; i < 5; i++ {
		e.ok(t, "goal_create", `{"title":"g","objective":"o"}`)
	}
	_, err := e.run(t, "goal_create", `{"title":"sixth","objective":"o"}`)
	if err == nil || !strings.Contains(err.Error(), "already has 5 open goals") {
		t.Fatalf("%v", err)
	}
	gs, _ := e.goals.List("discord", 5)
	e.ok(t, "goal_update", `{"id":"`+gs[0].ID+`","status":"done"}`)
	e.ok(t, "goal_create", `{"title":"sixth","objective":"o"}`) // finished goals do not count
	if _, err := e.run(t, "goal_update", `{"id":"`+gs[0].ID+`","status":"open"}`); err == nil {
		t.Fatal("reopening past the limit is refused too")
	}
}

func TestUpdateRecordsProgress(t *testing.T) {
	e := newEnv(t, true)
	e.ok(t, "goal_create", `{"title":"t","objective":"o","plan":["a","b","c"]}`)
	g := e.onlyGoal(t)
	e.now = utc("2026-10-20 16:00")
	out := e.ok(t, "goal_update", `{"id":"`+g.ID+`","note":"found the numbers in the shared sheet","done_steps":[1],"add_steps":["double-check totals"],"next_step":"draft the summary"}`)
	got := e.onlyGoal(t)
	if len(got.Plan) != 4 || !got.Plan[0].Done || got.Plan[1].Done || got.Plan[3].Text != "double-check totals" || got.Next != "draft the summary" {
		t.Fatalf("%+v", got)
	}
	if len(got.Notes) != 1 || got.Notes[0].Text != "found the numbers in the shared sheet" || !got.Notes[0].At.Equal(utc("2026-10-20 16:00")) {
		t.Fatalf("%+v", got.Notes)
	}
	if !strings.Contains(out, "1. [x] a") || !strings.Contains(out, "found the numbers") || !strings.Contains(out, "Next: draft the summary") || !got.Updated.Equal(utc("2026-10-20 16:00")) {
		t.Fatalf("%s", out)
	}
	if _, err := e.run(t, "goal_update", `{"id":"`+g.ID+`","done_steps":[9]}`); err == nil || !strings.Contains(err.Error(), "no step 9") {
		t.Fatalf("%v", err)
	}
	// A failed update changes nothing.
	if again := e.onlyGoal(t); len(again.Notes) != 1 || len(again.Plan) != 4 {
		t.Fatalf("%+v", again)
	}
	if _, err := e.run(t, "goal_update", `{"id":"`+g.ID+`","status":"finished"}`); err == nil {
		t.Fatal("unknown status")
	}
	if _, err := e.run(t, "goal_update", `{"id":"nope","note":"x"}`); err == nil {
		t.Fatal("unknown goal")
	}
}

func TestOnlyTheLastFortyNotesAreKept(t *testing.T) {
	e := newEnv(t, true)
	e.ok(t, "goal_create", `{"title":"t","objective":"o"}`)
	id := e.onlyGoal(t).ID
	for i := 0; i < 55; i++ {
		e.ok(t, "goal_update", `{"id":"`+id+`","note":"note `+string(rune('A'+i%26))+strings.Repeat("x", 600)+`"}`)
	}
	g := e.onlyGoal(t)
	if len(g.Notes) != MaxNotes {
		t.Fatalf("%d notes", len(g.Notes))
	}
	if n := len([]rune(g.Notes[0].Text)); n > 501 {
		t.Fatalf("a note is cut to 500 characters: %d", n)
	}
}

func TestStatusChangesStartAndStopCheckins(t *testing.T) {
	e := newEnv(t, true)
	e.ok(t, "goal_create", `{"title":"t","objective":"o"}`)
	id := e.onlyGoal(t).ID
	scheduled := func() bool { _, ok := e.jobs.NextCheckin(id); return ok }
	if !scheduled() {
		t.Fatal("created goals are checked in on")
	}
	for _, s := range []string{Paused, Done, Dropped} {
		e.ok(t, "goal_update", `{"id":"`+id+`","status":"`+s+`"}`)
		if scheduled() {
			t.Fatalf("a %s goal is not checked in on", s)
		}
		e.ok(t, "goal_update", `{"id":"`+id+`","status":"open"}`)
		if !scheduled() {
			t.Fatalf("reopened after %s: checked in on again", s)
		}
	}
	e.ok(t, "goal_update", `{"id":"`+id+`","check_in_minutes":30}`)
	jobs, _ := e.jobs.List()
	if len(jobs) != 1 || jobs[0].Schedule != "@every 30m0s" {
		t.Fatalf("changing the interval replaces the schedule: %+v", jobs)
	}
	if _, err := e.run(t, "goal_update", `{"id":"`+id+`","check_in_minutes":2}`); err == nil {
		t.Fatal("too frequent")
	}
	if g := e.onlyGoal(t); g.Every != 30*time.Minute {
		t.Fatalf("a refused change leaves the goal as it was: %+v", g)
	}
}

func TestResumingGivesAFreshAllowanceOfCheckins(t *testing.T) {
	e := newEnv(t, true)
	e.ok(t, "goal_create", `{"title":"t","objective":"o","max_checkins":3}`)
	id := e.onlyGoal(t).ID
	e.goals.Update("discord", 5, id, func(g *Goal) error { g.Checkins, g.Status = 3, Paused; return nil })
	e.ok(t, "goal_update", `{"id":"`+id+`","status":"open"}`)
	if g := e.onlyGoal(t); g.Checkins != 0 || g.Status != Open {
		t.Fatalf("%+v", g)
	}
}

func TestTheUserHavingASayResetsTheCount(t *testing.T) {
	e := newEnv(t, true)
	e.ok(t, "goal_create", `{"title":"t","objective":"o"}`)
	id := e.onlyGoal(t).ID
	e.ok(t, "goal_create", `{"title":"other chat's","objective":"o"}`)
	e.goals.Update("discord", 5, id, func(g *Goal) error { g.Checkins = 7; return nil })
	if err := e.goals.ResetCheckins("discord", 5); err != nil {
		t.Fatal(err)
	}
	gs, _ := e.goals.List("discord", 5)
	for _, g := range gs {
		if g.Checkins != 0 {
			t.Fatalf("%+v", g)
		}
	}
	// A paused goal is not resumed by the user speaking about something else.
	e.goals.Update("discord", 5, id, func(g *Goal) error { g.Checkins, g.Status = 9, Paused; return nil })
	e.goals.ResetCheckins("discord", 5)
	if g, _ := e.goals.GetAny(id); g.Status != Paused || g.Checkins != 9 {
		t.Fatalf("%+v", g)
	}
}

func TestChatsSeeOnlyTheirOwnGoals(t *testing.T) {
	e := newEnv(t, true)
	e.ok(t, "goal_create", `{"title":"mine","objective":"o"}`)
	mine := e.onlyGoal(t)
	other := tool.NewRegistry()
	RegisterTools(other, Binding{Store: e.goals, Sched: e.jobs, Gateway: "discord", ChatID: 6, Create: true})
	tl, _ := other.Get("goal_list")
	if out, _ := tl.Run(context.Background(), nil); out != "no goals" {
		t.Fatalf("%q", out)
	}
	up, _ := other.Get("goal_update")
	if _, err := up.Run(context.Background(), json.RawMessage(`{"id":"`+mine.ID+`","status":"dropped"}`)); err == nil {
		t.Fatal("another chat changed it")
	}
	get, _ := other.Get("goal_get")
	if _, err := get.Run(context.Background(), json.RawMessage(`{"id":"`+mine.ID+`"}`)); err == nil {
		t.Fatal("another chat read it")
	}
	// The same chat number on another service is another chat.
	tg := tool.NewRegistry()
	RegisterTools(tg, Binding{Store: e.goals, Sched: e.jobs, Gateway: "telegram", ChatID: 5, Create: true})
	list, _ := tg.Get("goal_list")
	if out, _ := list.Run(context.Background(), nil); out != "no goals" {
		t.Fatalf("%q", out)
	}
}

func TestCheckinsDoNotStartGoals(t *testing.T) {
	e := newEnv(t, false)
	if _, ok := e.reg.Get("goal_create"); ok {
		t.Fatal("a check-in works on a goal; it does not take on more")
	}
	for _, name := range []string{"goal_list", "goal_get", "goal_update"} {
		if _, ok := e.reg.Get(name); !ok {
			t.Fatalf("%s should be there", name)
		}
	}
}

func TestListAndGet(t *testing.T) {
	e := newEnv(t, true)
	e.ok(t, "goal_create", `{"title":"Ship the report","objective":"report is out","plan":["a","b"]}`)
	id := e.onlyGoal(t).ID
	e.ok(t, "goal_update", `{"id":"`+id+`","done_steps":[1]}`)
	list := e.ok(t, "goal_list", `{}`)
	if !strings.Contains(list, id+" | open | Ship the report | 1 of 2 steps, next check-in") {
		t.Fatalf("%s", list)
	}
	get := e.ok(t, "goal_get", `{"id":"`+id+`"}`)
	for _, want := range []string{"Objective: report is out", "1. [x] a", "2. [ ] b", "Checks in every 6h0m0s; 0 of 20 check-ins used", "next at "} {
		if !strings.Contains(get, want) {
			t.Errorf("missing %q in:\n%s", want, get)
		}
	}
}

func TestCheckinPromptCarriesTheGoal(t *testing.T) {
	g := Goal{ID: "ab12", Title: "Ship it", Objective: "the report is sent", Status: Open, Checkins: 3, MaxCheckins: 20, Every: time.Hour,
		Plan: []Step{{Text: "draft", Done: true}, {Text: "send"}}, Next: "send", Notes: []Note{{At: utc("2026-10-20 10:00"), Text: "drafted it"}}}
	p := CheckinPrompt(g)
	for _, want := range []string{"check-in 3 of at most 20", "the user is not in this conversation", "the report is sent", "1. [x] draft", "2. [ ] send", "Next: send", "drafted it",
		"goal_update", "[SILENT]", "Tools that need approval are denied"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q in:\n%s", want, p)
		}
	}
}

func TestGoalFileIsPrivateAndAtomic(t *testing.T) {
	e := newEnv(t, true)
	e.ok(t, "goal_create", `{"title":"t","objective":"o"}`)
	if fi, err := statMode(e.goals.Path); err != nil || fi != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
}

func statMode(path string) (uint32, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return uint32(fi.Mode().Perm()), nil
}
