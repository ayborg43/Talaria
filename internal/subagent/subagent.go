// Package subagent lets an agent hand a task to an isolated sub-conversation.
//
// The sub-conversation starts empty, works with the parent's tools, and returns
// only its final answer, so the parent's context is not filled with everything
// the subagent read and tried on the way.
package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"talaria/internal/activity"
	"talaria/internal/agent"
	"talaria/internal/tool"
)

// Limits are the configurable bounds on subagents.
type Limits struct {
	Disabled    bool `json:"disabled,omitempty"`
	MaxParallel int  `json:"max_parallel,omitempty"` // subagents running at once, across all chats (default 4)
	MaxSteps    int  `json:"max_steps,omitempty"`    // model calls one subagent may make (default 15)
}

const (
	defaultMaxParallel = 4
	defaultMaxSteps    = 15
	maxParallelCeiling = 16
	maxStepsCeiling    = 100
	maxResult          = 16 << 10 // bytes of a subagent's answer passed back
)

// Validate rejects limits that make no sense.
func (l Limits) Validate() error {
	switch {
	case l.MaxParallel < 0 || l.MaxParallel > maxParallelCeiling:
		return fmt.Errorf("subagents.max_parallel must be between 1 and %d", maxParallelCeiling)
	case l.MaxSteps < 0 || l.MaxSteps > maxStepsCeiling:
		return fmt.Errorf("subagents.max_steps must be between 1 and %d", maxStepsCeiling)
	}
	return nil
}

// excluded are the tools a subagent never gets. Subagents read, act and report;
// only the main agent changes Talaria's own persistent state (notes, skills,
// schedules) and decides what is worth keeping, and a subagent cannot start
// more subagents, so delegation cannot run away.
var excluded = []string{"delegate", "memory", "skill_save", "skill_delete", "cron_add", "cron_remove",
	"task_start", "task_cancel", "goal_create", "goal_update", "watch_create", "watch_update"}

const framing = `You are a subagent working for Talaria, a personal agent. The main agent has delegated one task to you.
You work alone: you cannot talk to the user or ask questions, and you cannot see the main conversation, so rely only on the task and your tools.
Do the task, then reply with your findings. Your final message is the only thing the main agent receives, so make it complete and self-contained: state the results and anything the main agent needs in order to act, and leave out the story of how you got there. If you cannot finish, say what you found and what stopped you.
You cannot change Talaria's notes, skills or schedules, and you cannot start more subagents; the main agent decides what to keep.`

const description = `Hand a task to a subagent: a separate assistant that works on it in its own empty conversation, with the same tools, and returns only its final answer. ` +
	`Use it for self-contained work whose details you do not need in this conversation: researching something, searching through many files, comparing options, producing a long result. ` +
	`Several delegate calls in the same turn run at the same time, so split independent pieces of work into separate calls. ` +
	`The subagent cannot see this conversation or ask you anything, so the task must contain everything it needs (paths, names, constraints) and say what form the answer should take. ` +
	`It cannot save notes, skills or schedules, or delegate further. Do not use it for work that takes a step or two: do that yourself.`

// Pool bounds how many subagents run at once and how long each may work.
type Pool struct {
	MaxSteps int
	Timeout  time.Duration // per subagent; default 10 minutes

	sem chan struct{}
}

// NewPool builds a pool from limits, filling in defaults.
func NewPool(l Limits) *Pool {
	if l.MaxParallel == 0 {
		l.MaxParallel = defaultMaxParallel
	}
	if l.MaxSteps == 0 {
		l.MaxSteps = defaultMaxSteps
	}
	return &Pool{MaxSteps: l.MaxSteps, Timeout: 10 * time.Minute, sem: make(chan struct{}, l.MaxParallel)}
}

// Register gives parent a delegate tool. A subagent is built fresh for every
// call and takes the parent's model, tools and approval at that moment, so a
// model the user switched to mid-conversation is the one used. background gives
// extra text for the subagent's system prompt (the user's notes and skills, and
// where the tools run); it is asked for at each delegation, so it is never stale.
//
// If the parent has an Activity sink, it is told when each subagent starts waiting for a place, begins,
// moves on to another tool, and finishes, so a front end can show who is working.
func (p *Pool) Register(parent *agent.Agent, background func() string) {
	parent.Tools.Register(tool.Tool{
		Name:        "delegate",
		Description: description,
		Schema: json.RawMessage(`{"type":"object","properties":{
			"task":{"type":"string","description":"complete, self-contained instructions, including the form the answer should take"},
			"name":{"type":"string","description":"a short label for this task, shown with its progress and approval prompts"}},
			"required":["task"]}`),
		Parallel: true,
		// What a subagent brings back was read from wherever it looked, and it starts with a clean
		// slate: a conversation that has read outside content must not be able to wash it through one.
		Untrusted: true,
		Egress:    tool.Always,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Task, Name string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			return p.run(ctx, parent, background, a.Task, a.Name)
		},
	})
}

func (p *Pool) run(ctx context.Context, parent *agent.Agent, background func() string, task, name string) (string, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return "", errors.New("task is empty: say what the subagent should do")
	}
	label := cleanLabel(name, task)

	sink := parent.Activity
	id, end := activity.Begin(sink, activity.Sub, label)
	defer end()
	select { // wait for a free slot
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	default:
		activity.Note(sink, id, "waiting for a free place")
		select {
		case p.sem <- struct{}{}:
			defer func() { <-p.sem }()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	activity.Note(sink, id, "")

	system := framing
	if background != nil {
		if b := background(); b != "" {
			system += "\n\n" + b
		}
	}
	sub := &agent.Agent{
		Provider: parent.Provider,
		Model:    parent.Model,
		// The browser is left out: a conversation has one tab, and subagents run
		// at once, so they would be clicking in each other's pages.
		Tools:    parent.Tools.Without(excluded...).WithoutPrefix("browser_"),
		System:   system,
		MaxSteps: p.MaxSteps,
		// A subagent is held to the same permission rules as the agent that
		// started it, and its calls go in the same record, under its label.
		Gate:           parent.Gate,
		GuardUntrusted: parent.GuardUntrusted,
		BeforeModel:    parent.BeforeModel, // a subagent spends from the same budget
		Label:          label,
		// Calls that ask go to the user through the parent's approval, labelled so
		// they can tell which task is asking.
		Approve: func(tool, args string) bool {
			if parent.Approve == nil {
				return false
			}
			return parent.Approve(agent.Labelled(tool, label), args)
		},
		// Progress shows up wherever the parent's tool activity does.
		OnToolStart: func(tool, args string) {
			activity.Note(sink, id, tool)
			if sink != nil { // a front end that shows the helpers has no use for the progress line as well
				return
			}
			if parent.OnToolStart != nil {
				parent.OnToolStart("  ↳ ["+label+"] "+tool, args)
			}
		},
	}
	if parent.ReadOutside() {
		sub.MarkUntrusted()
	}

	runCtx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	answer, err := sub.Run(runCtx, task)
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return "", fmt.Errorf("subagent %q timed out after %s", label, p.Timeout)
		}
		return "", fmt.Errorf("subagent %q failed: %w", label, err)
	}

	answer = strings.TrimSpace(answer)
	if answer == "" {
		return "(the subagent finished without giving a result)", nil
	}
	if len(answer) > maxResult {
		answer = strings.ToValidUTF8(answer[:maxResult], "") + "\n[the subagent's answer was cut here: it was too long]"
	}
	return answer, nil
}

// cleanLabel makes a short, single-line label from name, or from the task.
func cleanLabel(name, task string) string {
	label := strings.Join(strings.Fields(name), " ")
	if label == "" {
		label = strings.Join(strings.Fields(task), " ")
	}
	label = strings.NewReplacer("[", "(", "]", ")").Replace(label) // brackets frame the label in progress lines
	if r := []rune(label); len(r) > 40 {
		label = string(r[:40]) + "…"
	}
	return label
}
