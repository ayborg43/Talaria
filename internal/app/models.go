package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"talaria/internal/tool"
)

// ModelInfo is one model profile as the agent should see it when it chooses a model for a task.
type ModelInfo struct {
	Name      string
	Type      string
	Model     string
	Notes     string   // what the owner says it is for
	Best      []string // strengths the owner claims: code, cheap, fast, long, writing
	Vision    bool
	Reasoning bool
	Default   bool     // what this chat uses unless told otherwise
	Routine   bool     // what scheduled reminders and briefs use
	Fallbacks []string // tried in this order when it is not working
	Problem   string   // why it cannot be used or is failing now; empty when it works
	Tested    string   // how it did on the person's own test questions, if they were run
}

// SetModels lets the agent list the models it can give a task to. The gateway sets it once the profiles are known.
func (rt *Runtime) SetModels(list func() []ModelInfo) { rt.models = list }

// registerModelTool adds model_list: the choices the agent has, and what each is for.
func registerModelTool(r *tool.Registry, list func() []ModelInfo) {
	r.Register(tool.Tool{
		Name: "model_list",
		Description: "List the AI models you can give a task to: what each is for in the owner's words, its strengths, whether it can see pictures or reasons step by step, " +
			"and whether it is working now. Look here before choosing a model for a background task, a scheduled job or a delegated piece of work (the model argument of " +
			"task_start, cron_add and delegate): a cheap, fast one for plain lookups and summaries, a stronger one for code and hard problems, a long-context one for big documents. " +
			"If a model stops working, the next one in its fallback list is used for you without your doing anything.",
		Schema:   json.RawMessage(`{"type":"object","properties":{}}`),
		Parallel: true,
		Run: func(context.Context, json.RawMessage) (string, error) {
			ms := list()
			if len(ms) < 2 {
				return "There is only one model, so there is nothing to choose between.", nil
			}
			var b strings.Builder
			for _, m := range ms {
				fmt.Fprintf(&b, "- %s: %s", m.Name, m.Model)
				var tags []string
				if m.Default {
					tags = append(tags, "used by default")
				}
				if m.Routine {
					tags = append(tags, "used for scheduled reminders")
				}
				tags = append(tags, m.Best...)
				if m.Vision {
					tags = append(tags, "sees pictures")
				}
				if m.Reasoning {
					tags = append(tags, "reasons step by step")
				}
				if len(tags) > 0 {
					fmt.Fprintf(&b, " [%s]", strings.Join(tags, ", "))
				}
				if m.Notes != "" {
					fmt.Fprintf(&b, ". %s", strings.TrimSuffix(m.Notes, "."))
				}
				if m.Problem != "" {
					fmt.Fprintf(&b, ". NOT WORKING NOW: %s", m.Problem)
				}
				if m.Tested != "" {
					fmt.Fprintf(&b, ". Tested: %s", m.Tested)
				}
				if len(m.Fallbacks) > 0 {
					fmt.Fprintf(&b, ". If it fails: %s", strings.Join(m.Fallbacks, ", then "))
				}
				b.WriteByte('\n')
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})
}

// RegisterModelToolForTest registers model_list for a test in another package.
func RegisterModelToolForTest(r *tool.Registry, rt *Runtime) { registerModelTool(r, rt.models) }

// SetUsage lets the agent say what the models have cost. summary describes the last days days in a few sentences.
func (rt *Runtime) SetUsage(summary func(days int) string) { rt.usageSummary = summary }

// registerUsageTool adds usage_summary: tokens and cost by model, agent and kind of work.
func registerUsageTool(r *tool.Registry, summary func(days int) string) {
	r.Register(tool.Tool{
		Name: "usage_summary",
		Description: "Say how much the AI models have been used and what that cost, over today (days=1), this week (days=7) or this month (days=30): " +
			"requests and tokens, the dollars where the owner has set a price for the model, and where most of it went (which model, which agent, what kind of work). " +
			"Use it when the owner asks what they have spent, or before choosing a model for a big job.",
		Schema:   json.RawMessage(`{"type":"object","properties":{"days":{"type":"integer","minimum":1,"maximum":90}}}`),
		Parallel: true,
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ Days int }
			json.Unmarshal(args, &a)
			if a.Days < 1 {
				a.Days = 7
			}
			if a.Days > 90 {
				a.Days = 90
			}
			return summary(a.Days), nil
		},
	})
}

// SetDigest lets the agent say what it did on its own lately. digest writes the account for the last span.
func (rt *Runtime) SetDigest(digest func(span time.Duration) string) { rt.whileAway = digest }

// registerWhileAwayTool adds while_away: the work done unasked, from the activity log, finished tasks, watches, goals and the board.
func registerWhileAwayTool(r *tool.Registry, digest func(span time.Duration) string) {
	r.Register(tool.Tool{
		Name: "while_away",
		Description: "Say what you did on your own over the last stretch of time: tasks and scheduled work that finished (and which failed), watches that saw a change, " +
			"goals and board cards that moved, how many tool calls were made and how many were refused, what it cost, and what is waiting for the owner. " +
			"Use it when the owner asks what happened while they were away, what you have been up to, or what is waiting. hours defaults to 24.",
		Schema:   json.RawMessage(`{"type":"object","properties":{"hours":{"type":"integer","minimum":1,"maximum":720}}}`),
		Parallel: true,
		// It repeats what finished tasks and watches reported, and some of that was read from outside (a webhook's payload, a
		// page): it must not wash it back into a conversation as if the user had written it.
		Untrusted: true,
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ Hours int }
			json.Unmarshal(args, &a)
			if a.Hours < 1 {
				a.Hours = 24
			}
			if a.Hours > 720 {
				a.Hours = 720
			}
			return digest(time.Duration(a.Hours) * time.Hour), nil
		},
	})
}
