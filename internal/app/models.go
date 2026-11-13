package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
