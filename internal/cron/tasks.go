package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"talaria/internal/tool"
)

// Control is what the task tools need from the scheduler that runs a service's
// jobs: to start a task at once, to see what is running, and to stop it.
type Control interface {
	Wake()
	Runs(gateway string, chatID int64) []RunInfo
	Cancel(gateway string, chatID int64, id string) bool
}

const (
	maxTasksPerChat    = 3
	defaultTaskMinutes = 30
	maxTaskMinutes     = 120
)

// TaskCount is how many background tasks a chat has going: running, waiting
// for a slot, or stored and not yet picked up.
func TaskCount(s *Store, ctl Control, gateway string, chatID int64) int {
	n := 0
	seen := map[string]bool{}
	if ctl != nil {
		for _, r := range ctl.Runs(gateway, chatID) {
			if r.Kind == KindTask {
				n++
				seen[r.ID] = true
			}
		}
	}
	jobs, _ := s.List()
	for _, j := range jobs {
		if j.Kind == KindTask && j.ChatID == chatID && j.Gateway == gateway && !seen[j.ID] {
			n++
		}
	}
	return n
}

// RegisterTaskTools adds task_start, task_list and task_cancel for one chat. A
// task is a piece of work that runs on its own while the conversation goes on,
// and reports when it is done.
func RegisterTaskTools(r *tool.Registry, s *Store, ctl Control, gateway string, chatID int64, targets Targets) {
	if gateway == "" {
		gateway = DefaultGateway
	}
	schema := targets.taskSchema()
	r.Register(tool.Tool{
		Name: "task_start",
		Description: "Start a long piece of work in the background and carry on talking. It runs on its own, with its own copy of your tools, and its final reply is sent to this chat when it finishes. " +
			"Use it for work that would take more than about a minute (research, a long browser session, a big refactor), not for quick lookups. " +
			"It has no memory of this conversation, so the task must be fully self-contained. It cannot ask the user anything: tools that need approval are denied unless allow_risky is true, " +
			"so set that only when the task truly needs them; the user sees it when approving. At most " + fmt.Sprint(maxTasksPerChat) + " tasks run at once per chat.",
		Schema:        schema,
		NeedsApproval: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Task, Name           string
				AllowRisky           bool `json:"allow_risky"`
				TimeoutMinutes       int  `json:"timeout_minutes"`
				Profile, Environment string
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			if n := TaskCount(s, ctl, gateway, chatID); n >= maxTasksPerChat {
				return "", fmt.Errorf("this chat already has %d background tasks going (the most is %d): wait for one to finish or cancel one with task_cancel", n, maxTasksPerChat)
			}
			minutes := a.TimeoutMinutes
			if minutes <= 0 {
				minutes = defaultTaskMinutes
			}
			minutes = min(minutes, maxTaskMinutes)
			profile, environment, err := targets.resolve(strings.TrimSpace(a.Profile), strings.TrimSpace(a.Environment))
			if err != nil {
				return "", err
			}
			j, err := s.AddNow(Job{Name: a.Name, Kind: KindTask, Prompt: a.Task, Gateway: gateway, ChatID: chatID,
				AllowRisky: a.AllowRisky, Profile: profile, Environment: environment, TimeoutSec: minutes * 60})
			if err != nil {
				return "", err
			}
			if ctl != nil {
				ctl.Wake()
			}
			return fmt.Sprintf("started task %s (%s), which may run for up to %d minutes; its result will be sent to this chat%s", j.ID, j.Name, minutes, targets.describe(j)), nil
		},
	})

	r.Register(tool.Tool{
		Name:        "task_list",
		Description: "List this chat's background tasks that are running or waiting to run.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			var b strings.Builder
			seen := map[string]bool{}
			if ctl != nil {
				for _, r := range ctl.Runs(gateway, chatID) {
					if r.Kind != KindTask {
						continue
					}
					seen[r.ID] = true
					state := "waiting for a free slot"
					if r.Running {
						state = "running"
					}
					fmt.Fprintf(&b, "%s | %s | %s for %s\n", r.ID, r.Name, state, time.Since(r.Since).Round(time.Second))
				}
			}
			jobs, _ := s.List()
			for _, j := range jobs {
				if j.Kind == KindTask && j.ChatID == chatID && j.Gateway == gateway && !seen[j.ID] {
					fmt.Fprintf(&b, "%s | %s | about to start\n", j.ID, j.Name)
				}
			}
			if b.Len() == 0 {
				return "no background tasks are running", nil
			}
			return b.String(), nil
		},
	})

	r.Register(tool.Tool{
		Name:        "task_cancel",
		Description: "Stop one of this chat's background tasks by id (or \"all\").",
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ ID string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			n, err := CancelTasks(s, ctl, gateway, chatID, strings.TrimSpace(a.ID))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("cancelled %d task(s)", n), nil
		},
	})
}

// CancelTasks stops a chat's task by id, or every one of them for "all", and
// says how many it stopped. A task that has not started yet is removed before it can.
func CancelTasks(s *Store, ctl Control, gateway string, chatID int64, id string) (int, error) {
	if gateway == "" {
		gateway = DefaultGateway
	}
	var ids []string
	if id == "all" {
		if ctl != nil {
			for _, r := range ctl.Runs(gateway, chatID) {
				if r.Kind == KindTask {
					ids = append(ids, r.ID)
				}
			}
		}
		jobs, _ := s.List()
		for _, j := range jobs {
			if j.Kind == KindTask && j.ChatID == chatID && j.Gateway == gateway {
				ids = append(ids, j.ID)
			}
		}
	} else {
		ids = []string{id}
	}
	n := 0
	for _, one := range ids {
		switch {
		case ctl != nil && ctl.Cancel(gateway, chatID, one):
			n++
		case s.removeTask(one, gateway, chatID) == nil:
			n++
		}
	}
	if n == 0 {
		return 0, errors.New("no such task of this chat is running (see task_list)")
	}
	return n, nil
}

// removeTask deletes a stored task job that has not been picked up.
func (s *Store) removeTask(id, gateway string, chatID int64) error {
	jobs, err := s.List()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.ID == id && j.Kind == KindTask && j.ChatID == chatID && j.Gateway == gateway {
			return s.Remove(id, gateway, chatID)
		}
	}
	return errors.New("no such task")
}

// taskSchema is task_start's arguments, with profile and environment offered
// where there is a choice, as for cron_add.
func (t Targets) taskSchema() json.RawMessage {
	props := map[string]any{
		"name":            map[string]any{"type": "string", "description": "short label shown with the result"},
		"task":            map[string]any{"type": "string", "description": "complete, self-contained instructions"},
		"allow_risky":     map[string]any{"type": "boolean"},
		"timeout_minutes": map[string]any{"type": "integer", "description": "how long it may run; default 30, at most 120"},
	}
	if len(t.Profiles) > 0 {
		props["profile"] = map[string]any{"type": "string", "enum": append([]string{"default"}, t.Profiles...), "description": "the model profile it runs on; left out, the one this chat uses now"}
	}
	if len(t.Environments) > 0 {
		props["environment"] = map[string]any{"type": "string", "enum": append([]string{"default"}, t.Environments...), "description": "where its shell and file tools run; left out, where this chat's run now"}
	}
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": []string{"task"}})
	return raw
}
