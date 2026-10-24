package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"talaria/internal/cron"
	"talaria/internal/tool"
)

// Scheduling is what keeps a goal's check-ins coming. *cron.Store is one.
type Scheduling interface {
	ScheduleGoal(goalID, gateway string, chatID int64, name string, every time.Duration, allowRisky bool, profile, environment string) error
	UnscheduleGoal(goalID string) error
	NextCheckin(goalID string) (time.Time, bool)
}

// Binding says whose goals the tools work on.
type Binding struct {
	Store   *Store
	Sched   Scheduling
	Gateway string
	ChatID  int64
	// Targets says which model and environment check-ins run on; a new goal
	// gets what the chat uses now.
	Targets cron.Targets
	// Create offers goal_create. A check-in is working on a goal already: it
	// updates goals but does not start new ones.
	Create bool
}

// RegisterTools adds the goal tools for one chat.
func RegisterTools(r *tool.Registry, b Binding) {
	if b.Create {
		r.Register(tool.Tool{
			Name: "goal_create",
			Description: "Take on a goal the user wants pursued over days or weeks, not just answered now. You will check in on it by yourself every so often (default every 6 hours), " +
				"do the next step with your tools, record what you did, and message the user when you need them or something is done. " +
				"Give the objective (what done looks like) and a short plan. Check-ins run unattended: tools that need approval are denied unless allow_risky is true, " +
				"so set that only when the goal needs them; the user sees it when approving.",
			Schema: json.RawMessage(`{"type":"object","properties":{
				"title":{"type":"string","description":"a short name"},
				"objective":{"type":"string","description":"what done looks like, complete enough to work from with no other context"},
				"plan":{"type":"array","items":{"type":"string"},"description":"the steps, in order"},
				"check_in_minutes":{"type":"integer","description":"how often to check in; default 360, from 15 to 10080"},
				"max_checkins":{"type":"integer","description":"how many check-ins to make before pausing to hear from the user; default 20"},
				"allow_risky":{"type":"boolean"}},
				"required":["title","objective"]}`),
			NeedsApproval: true,
			Describe:      describeCreate,
			Run:           b.create,
		})
	}
	r.Register(tool.Tool{
		Name:        "goal_list",
		Description: "List this chat's goals with their status and progress.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Run:         b.list,
	})
	r.Register(tool.Tool{
		Name:        "goal_get",
		Description: "Show one goal in full: objective, plan, progress notes and next step.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Run:         b.get,
	})
	r.Register(tool.Tool{
		Name: "goal_update",
		Description: "Record progress on a goal, or change it. Add a note about what you did or found, add steps, mark steps done by number, set what the next check-in should do first, " +
			"change how often to check in, or set the status: open, paused (stop checking in), done, or dropped.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"id":{"type":"string"},
			"note":{"type":"string","description":"what you did or found, in a sentence or two"},
			"add_steps":{"type":"array","items":{"type":"string"}},
			"done_steps":{"type":"array","items":{"type":"integer"},"description":"plan step numbers, counting from 1"},
			"next_step":{"type":"string","description":"what the next check-in should do first"},
			"check_in_minutes":{"type":"integer"},
			"status":{"type":"string","enum":["open","paused","done","dropped"]}},
			"required":["id"]}`),
		// Reopening a goal gives it a fresh allowance of check-ins, and dropping one ends it: the user
		// decides those. Notes, steps and pausing or finishing it are the agent's to do on its own.
		NeedsApprovalFor: func(raw json.RawMessage) bool {
			var a struct{ Status string }
			return json.Unmarshal(raw, &a) != nil || a.Status == "open" || a.Status == "dropped"
		},
		RiskFor: func(raw json.RawMessage) tool.Risk {
			var a struct{ Status string }
			if json.Unmarshal(raw, &a) != nil || a.Status == "dropped" {
				return tool.RiskDeletes // dropping a goal ends it for good
			}
			return 0
		},
		Run: b.update,
	})
}

func describeCreate(raw json.RawMessage) string {
	var a struct {
		Title      string `json:"title"`
		Every      int    `json:"check_in_minutes"`
		Max        int    `json:"max_checkins"`
		AllowRisky bool   `json:"allow_risky"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return ""
	}
	every := DefaultEvery
	if a.Every > 0 {
		every = time.Duration(a.Every) * time.Minute
	}
	max := DefaultCheckins
	if a.Max > 0 {
		max = a.Max
	}
	out := fmt.Sprintf("take on the goal %q, checking in every %s, up to %d times before it asks you", clip(a.Title, 80), every, max)
	if a.AllowRisky {
		out += ", and with permission to use tools that normally ask (shell, file writes) while you are not looking"
	}
	return out
}

func clip(s string, n int) string { return clean(s, n) }

func (b Binding) create(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		Title, Objective string
		Plan             []string
		Every            int  `json:"check_in_minutes"`
		Max              int  `json:"max_checkins"`
		AllowRisky       bool `json:"allow_risky"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	profile, env, err := b.Targets.Resolve("", "")
	if err != nil {
		return "", err
	}
	g := Goal{Gateway: b.Gateway, ChatID: b.ChatID, Title: a.Title, Objective: a.Objective, MaxCheckins: a.Max,
		AllowRisky: a.AllowRisky, Profile: profile, Environment: env}
	if a.Every > 0 {
		g.Every = time.Duration(a.Every) * time.Minute
	}
	for _, s := range a.Plan {
		g.Plan = append(g.Plan, Step{Text: s})
	}
	if len(g.Plan) > 0 {
		g.Next = g.Plan[0].Text
	}
	g, err = b.Store.Add(g)
	if err != nil {
		return "", err
	}
	if err := b.Sched.ScheduleGoal(g.ID, g.Gateway, g.ChatID, g.Title, g.Every, g.AllowRisky, g.Profile, g.Environment); err != nil {
		b.Store.Update(g.Gateway, g.ChatID, g.ID, func(x *Goal) error { x.Status = Dropped; return nil })
		return "", fmt.Errorf("the goal was not saved, because its check-ins could not be scheduled: %w", err)
	}
	next, _ := b.Sched.NextCheckin(g.ID)
	return fmt.Sprintf("took on goal %s (%s). I will check in every %s, first at %s, up to %d times before asking you.", g.ID, g.Title, g.Every, next.Local().Format("Mon 15:04"), g.MaxCheckins), nil
}

func (b Binding) list(ctx context.Context, _ json.RawMessage) (string, error) {
	goals, err := b.Store.List(b.Gateway, b.ChatID)
	if err != nil {
		return "", err
	}
	if len(goals) == 0 {
		return "no goals", nil
	}
	var sb strings.Builder
	for _, g := range goals {
		sb.WriteString(b.line(g) + "\n")
	}
	return sb.String(), nil
}

func (b Binding) line(g Goal) string {
	when := ""
	if g.Status == Open {
		if next, ok := b.Sched.NextCheckin(g.ID); ok {
			when = ", next check-in " + next.Local().Format("Mon 15:04")
		}
	}
	return fmt.Sprintf("%s | %s | %s | %s%s", g.ID, g.Status, g.Title, g.Progress(), when)
}

func (b Binding) get(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct{ ID string }
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	g, err := b.Store.Get(b.Gateway, b.ChatID, strings.TrimSpace(a.ID))
	if err != nil {
		return "", err
	}
	next, _ := b.Sched.NextCheckin(g.ID)
	return Describe(g, next), nil
}

func (b Binding) update(ctx context.Context, raw json.RawMessage) (string, error) {
	var a struct {
		ID        string
		Note      string
		AddSteps  []string `json:"add_steps"`
		DoneSteps []int    `json:"done_steps"`
		NextStep  *string  `json:"next_step"`
		Every     int      `json:"check_in_minutes"`
		Status    string
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	switch a.Status {
	case "", Open, Paused, Done, Dropped:
	default:
		return "", fmt.Errorf("status must be open, paused, done or dropped, not %q", a.Status)
	}
	now := b.Store.now()
	var reopened bool
	g, err := b.Store.Update(b.Gateway, b.ChatID, strings.TrimSpace(a.ID), func(g *Goal) error {
		for _, n := range a.DoneSteps {
			if n < 1 || n > len(g.Plan) {
				return fmt.Errorf("there is no step %d: the plan has %d steps", n, len(g.Plan))
			}
			g.Plan[n-1].Done = true
		}
		for _, s := range a.AddSteps {
			g.Plan = append(g.Plan, Step{Text: s})
		}
		g.AddNote(now, a.Note)
		if a.NextStep != nil {
			g.Next = *a.NextStep
		}
		if a.Every > 0 {
			g.Every = time.Duration(a.Every) * time.Minute
		}
		if a.Status != "" {
			reopened = a.Status == Open && g.Status != Open
			g.Status = a.Status
			if reopened {
				g.Checkins = 0 // resuming is the user having a say
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	var schedErr error
	switch {
	case g.Status != Open:
		schedErr = b.Sched.UnscheduleGoal(g.ID)
	case reopened || a.Every > 0:
		schedErr = b.Sched.ScheduleGoal(g.ID, g.Gateway, g.ChatID, g.Title, g.Every, g.AllowRisky, g.Profile, g.Environment)
	}
	if schedErr != nil {
		return "", fmt.Errorf("the goal was updated, but its check-ins could not be rescheduled: %w", schedErr)
	}
	next, _ := b.Sched.NextCheckin(g.ID)
	return "updated.\n" + Describe(g, next), nil
}

// Describe writes a goal out in full.
func Describe(g Goal, nextCheckin time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal %s: %s [%s]\nObjective: %s\n", g.ID, g.Title, g.Status, g.Objective)
	if len(g.Plan) > 0 {
		b.WriteString("Plan:\n")
		for i, s := range g.Plan {
			mark := " "
			if s.Done {
				mark = "x"
			}
			fmt.Fprintf(&b, "  %d. [%s] %s\n", i+1, mark, s.Text)
		}
	}
	if g.Next != "" {
		fmt.Fprintf(&b, "Next: %s\n", g.Next)
	}
	if len(g.Notes) > 0 {
		b.WriteString("Progress notes (oldest first):\n")
		for _, n := range g.Notes {
			fmt.Fprintf(&b, "  - %s: %s\n", n.At.Local().Format("Jan 2 15:04"), n.Text)
		}
	}
	if g.Status == Open {
		fmt.Fprintf(&b, "Checks in every %s; %d of %d check-ins used since you last heard from the user", g.Every, g.Checkins, g.MaxCheckins)
		if !nextCheckin.IsZero() {
			fmt.Fprintf(&b, "; next at %s", nextCheckin.Local().Format("Mon Jan 2 15:04"))
		}
		b.WriteString(".\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// CheckinPrompt is what a check-in is asked to do. It is written from the goal
// as it stands, so a check-in needs no memory of the last one: the goal is the memory.
func CheckinPrompt(g Goal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are working on a goal for the user, on your own: this is check-in %d of at most %d, and the user is not in this conversation.\n\n", g.Checkins, g.MaxCheckins)
	b.WriteString(Describe(g, time.Time{}))
	b.WriteString(`

What to do now:
- Do the next step, or as much of the plan as you can finish in this check-in, with your tools. Do real work; do not just restate the plan.
- Record what you did with goal_update: add a note (what you did or found, in a sentence or two), mark finished steps done, set what the next check-in should do first, and add steps you discover. If the goal is complete, set its status to done; if it cannot go on without the user, say so below and leave it open (or pause it).
- If the next step is best taken sooner or later than your usual interval, change check_in_minutes.
- Your final reply is sent to the user. Send one when you need them (a decision, information, a login or approval you do not have), when something finished or went wrong, or at a milestone, and say exactly what you need. If you only made routine progress, reply with exactly [SILENT].
- Tools that need approval are denied here. If one is, say which and why, so the user can decide.`)
	return b.String()
}

// Summary lists a chat's goals, for a command typed in the chat.
func (b Binding) Summary() string {
	out, err := b.list(context.Background(), nil)
	if err != nil {
		return "Could not read the goals: " + err.Error()
	}
	return out
}

// Show writes one goal in full.
func (b Binding) Show(id string) string {
	raw, _ := json.Marshal(map[string]string{"id": id})
	out, err := b.get(context.Background(), raw)
	if err != nil {
		return err.Error()
	}
	return out
}

// SetStatus pauses, resumes, finishes or drops a goal.
func (b Binding) SetStatus(id, status string) string {
	raw, _ := json.Marshal(map[string]string{"id": id, "status": status})
	out, err := b.update(context.Background(), raw)
	if err != nil {
		return err.Error()
	}
	return out
}
