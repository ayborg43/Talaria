package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"talaria/internal/tool"
)

const timeFormat = "Mon 2006-01-02 15:04 MST"

// Targets says where a job may run, and what one gets when its creator does not
// choose. The zero value offers no choice: every job runs on the gateway's defaults.
//
// A job may only name what a chat could switch to itself, so scheduling is never
// a way round the limits on /model and /env.
type Targets struct {
	Profiles     []string // model profiles a job may name; empty when there is no choice
	Environments []string // environments a job may name; empty when there is no choice
	// DefaultProfile and DefaultEnvironment are what "default" means, for display.
	DefaultProfile, DefaultEnvironment string
	// Current returns the creating chat's own profile and environment as names
	// ("" meaning the default). A job gets these unless told otherwise.
	Current func() (profile, environment string)
	// CheckProfile reports whether a profile can be used right now, for example
	// that its API key is set. It is how a job learns at creation, rather than at
	// 3 a.m., that the model it asked for is unusable.
	CheckProfile func(name string) error
}

// Resolve works out the profile and environment for something new started from
// this chat: what was asked for, else what the chat uses now.
func (t Targets) Resolve(profile, environment string) (string, string, error) {
	return t.resolve(profile, environment)
}

// resolve works out the profile and environment for a new job.
func (t Targets) resolve(profile, environment string) (string, string, error) {
	curProfile, curEnv := "", ""
	if t.Current != nil {
		curProfile, curEnv = t.Current()
	}
	p, err := pick("model profile", profile, curProfile, t.Profiles, t.DefaultProfile, t.CheckProfile)
	if err != nil {
		return "", "", err
	}
	e, err := pick("environment", environment, curEnv, t.Environments, t.DefaultEnvironment, nil)
	return p, e, err
}

// pick chooses one setting. asked is what the caller named, current is what
// the chat is using; either may mean "the default", which is stored as "".
func pick(kind, asked, current string, allowed []string, def string, check func(string) error) (string, error) {
	if asked == "" {
		if current == def || !slices.Contains(allowed, current) { // on the default, or on something no longer offered
			return "", nil
		}
		return current, nil
	}
	if len(allowed) == 0 {
		return "", fmt.Errorf("there is no choice of %s here: jobs use the default", kind)
	}
	if asked == "default" || asked == def {
		return "", nil
	}
	if !slices.Contains(allowed, asked) {
		return "", fmt.Errorf("no %s named %q (choose from: %s, or default)", kind, asked, strings.Join(allowed, ", "))
	}
	if check != nil {
		if err := check(asked); err != nil {
			return "", fmt.Errorf("the %s %q cannot be used right now: %w", kind, asked, err)
		}
	}
	return asked, nil
}

// schema builds cron_add's arguments, offering profile and environment only
// where there is something to choose between.
func (t Targets) schema() json.RawMessage {
	props := map[string]any{
		"name":        map[string]any{"type": "string", "description": "short label shown with results"},
		"schedule":    map[string]any{"type": "string"},
		"delay":       map[string]any{"type": "string", "description": "one-off, from now; at least 1m"},
		"prompt":      map[string]any{"type": "string"},
		"allow_risky": map[string]any{"type": "boolean"},
	}
	if len(t.Profiles) > 0 {
		props["profile"] = map[string]any{"type": "string", "enum": append([]string{"default"}, t.Profiles...),
			"description": "the model profile the job runs on; left out, the one this chat uses now"}
	}
	if len(t.Environments) > 0 {
		props["environment"] = map[string]any{"type": "string", "enum": append([]string{"default"}, t.Environments...),
			"description": "where the job's shell and file tools run; left out, where this chat's run now"}
	}
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": []string{"prompt"}})
	return raw
}

func (t Targets) hasChoices() bool { return len(t.Profiles) > 0 || len(t.Environments) > 0 }

// RegisterTools adds cron_add, cron_list and cron_remove for one chat on one
// gateway: jobs deliver to that chat, and the chat can only see and remove its
// own jobs.
func RegisterTools(r *tool.Registry, s *Store, gateway string, chatID int64, targets Targets) {
	if gateway == "" {
		gateway = DefaultGateway
	}
	addDescription := "Schedule a prompt to run later, once or repeatedly. Its final reply is sent to this chat. " +
		"Give exactly one of: schedule (five-field cron like '0 9 * * mon-fri', '@daily', '@every 90m', or '@at <RFC3339 time>' for once) " +
		"or delay (like '20m' or '2h30m') for a one-off reminder. Times use the machine's local timezone; call current_time first " +
		"if you must compute one. The job runs later with no memory of this conversation, so the prompt must be fully self-contained. " +
		"Unattended runs cannot use tools that need approval (shell, write_file, skill changes) unless allow_risky is true, " +
		"so set that only when the task truly needs them; the user sees it when approving."
	if targets.hasChoices() {
		addDescription += " The job runs on the model and in the environment this chat is using when it is created, which is usually what " +
			"is wanted; name a profile or environment only to run it somewhere else, or 'default' for the gateway's own."
	}

	r.Register(tool.Tool{
		Name:          "cron_add",
		Description:   addDescription,
		Schema:        targets.schema(),
		NeedsApproval: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Name, Schedule, Delay, Prompt string
				AllowRisky                    bool `json:"allow_risky"`
				Profile, Environment          string
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			spec := strings.TrimSpace(a.Schedule)
			switch {
			case (spec == "") == (strings.TrimSpace(a.Delay) == ""):
				return "", errors.New("give exactly one of schedule or delay")
			case spec == "":
				d, err := time.ParseDuration(strings.TrimSpace(a.Delay))
				if err != nil {
					return "", fmt.Errorf("bad delay: %v", err)
				}
				if d < MinInterval {
					return "", fmt.Errorf("delay must be at least %s", MinInterval)
				}
				spec = "@at " + s.now().Add(d).Format(time.RFC3339)
			}
			profile, environment, err := targets.resolve(strings.TrimSpace(a.Profile), strings.TrimSpace(a.Environment))
			if err != nil {
				return "", err
			}
			j, err := s.Add(Job{Name: a.Name, Schedule: spec, Prompt: a.Prompt, Gateway: gateway, ChatID: chatID, AllowRisky: a.AllowRisky, Profile: profile, Environment: environment})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("scheduled job %s (%s), first run %s%s", j.ID, j.Name, j.NextRun.Local().Format(timeFormat), targets.describe(j)), nil
		},
	})

	r.Register(tool.Tool{
		Name:        "cron_list",
		Description: "List this chat's scheduled jobs.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			jobs, err := s.List()
			if err != nil {
				return "", err
			}
			var b strings.Builder
			for _, j := range jobs {
				if j.ChatID == chatID && j.Gateway == gateway {
					b.WriteString(Describe(j) + "\n")
				}
			}
			if b.Len() == 0 {
				return "no scheduled jobs", nil
			}
			return b.String(), nil
		},
	})

	r.Register(tool.Tool{
		Name:          "cron_remove",
		NeedsApproval: true, // removing a schedule can silence something the user relies on
		Description:   "Delete one of this chat's scheduled jobs by id.",
		Schema:        json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ ID string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			if err := s.Remove(strings.TrimSpace(a.ID), gateway, chatID); err != nil {
				return "", err
			}
			return "removed job " + a.ID, nil
		},
	})
}

// describe says where a job will run, when there is a choice about that.
func (t Targets) describe(j Job) string {
	var parts []string
	if len(t.Profiles) > 0 {
		parts = append(parts, "the "+orDefault(j.Profile, t.DefaultProfile)+" model profile")
	}
	if len(t.Environments) > 0 {
		parts = append(parts, "the "+orDefault(j.Environment, t.DefaultEnvironment)+" environment")
	}
	if len(parts) == 0 {
		return ""
	}
	return "\nit runs with " + strings.Join(parts, " and ")
}

// orDefault names a setting, noting when it is the default and so will follow it.
func orDefault(name, def string) string {
	if name != "" {
		return name
	}
	if def == "" {
		return "default"
	}
	return def + " (default)"
}

// Describe renders a job on one line for listings.
func Describe(j Job) string {
	last := "never run"
	if !j.LastRun.IsZero() {
		last = fmt.Sprintf("last %s: %s", j.LastRun.Local().Format(timeFormat), j.LastStatus)
	}
	risky := ""
	if j.AllowRisky {
		risky = " [risky tools allowed]"
	}
	where := ""
	if j.Profile != "" {
		where += " [model: " + j.Profile + "]"
	}
	if j.Environment != "" {
		where += " [env: " + j.Environment + "]"
	}
	return fmt.Sprintf("%s | %s | %s | next %s | %s%s%s", j.ID, j.Name, j.Schedule, j.NextRun.Local().Format(timeFormat), last, risky, where)
}
