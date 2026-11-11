package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/recipe"
	"talaria/internal/tool"
	"talaria/internal/watch"
	"talaria/internal/web"
)

// ruleRefuses says whether the user's permission rules forbid the agent to do what a recipe is about to do for them.
// Tapping Add is the "ask", so a rule that asks or allows lets it through; a rule of never, or of "I do it myself",
// does not: recipes are held to the rules like anything else that schedules or starts work.
func ruleRefuses(rt *app.Runtime, toolName string) error {
	gd := rt.Guard()
	if gd == nil {
		return nil
	}
	switch gd.For("chat", "web", web.ChatID).Verdict(toolName, true).Action {
	case tool.Deny:
		return errors.New("your rules say the agent may never schedule or start work. You can change that under Settings, Permissions")
	case tool.Handoff:
		return errors.New("your rules say you set up schedules and start work yourself, so a recipe cannot do it for you. You can change that under Settings, Permissions")
	}
	return nil
}

// webRecipes connects the recipes screen to the job store and the watches. A recipe becomes an ordinary scheduled
// prompt (or a watch), run with no risky tools, delivered to the web chat, and shown under Up next like any other.
func webRecipes(rt *app.Runtime, jobs *cron.Store, zone *time.Location, ctl func() cron.Control) *web.RecipeHooks {
	find := func(id string) (cron.Job, bool) {
		all, err := jobs.List()
		if err != nil {
			return cron.Job{}, false
		}
		for _, j := range all {
			if j.Gateway == "web" && j.ChatID == web.ChatID && j.Recipe == id {
				return j, true
			}
		}
		return cron.Job{}, false
	}
	return &web.RecipeHooks{
		Caps: func() map[string]bool {
			cal := rt.Calendars()
			return map[string]bool{recipe.NeedsMail: rt.HasMail(), recipe.NeedsCalendar: cal != nil && len(cal.Sources) > 0, recipe.NeedsWeb: true}
		},
		State: func() map[string]web.RecipeAdded {
			out := map[string]web.RecipeAdded{}
			all, _ := jobs.List()
			for _, j := range all {
				if j.Gateway == "web" && j.ChatID == web.ChatID && j.Recipe != "" {
					out[j.Recipe] = web.RecipeAdded{At: briefTime(j.Schedule), Next: j.NextRun.In(zone), Last: j.LastStatus, Values: j.RecipeValues}
				}
			}
			return out
		},
		Add: func(id string, in map[string]string, at string) (string, error) {
			r, ok := recipe.ByID(id)
			if !ok {
				return "", errors.New("there is no such recipe")
			}
			vals, err := r.Values(in)
			if err != nil {
				return "", err
			}
			if r.Kind == recipe.Watch {
				return addWatchRecipe(rt, r, vals)
			}
			if err := ruleRefuses(rt, "cron_add"); err != nil {
				return "", err
			}
			if at == "" {
				at = r.When
			}
			sched, err := r.CronFor(zone.String(), at)
			if err != nil {
				return "", err
			}
			if old, ok := find(id); ok { // one of each: setting it again moves it
				if err := jobs.Remove(old.ID, old.Gateway, old.ChatID); err != nil {
					return "", err
				}
			}
			j, err := jobs.Add(cron.Job{Name: r.Title, Schedule: sched, Prompt: recipe.Fill(r.Prompt, vals), Gateway: "web", ChatID: web.ChatID, Recipe: r.ID, RecipeValues: vals})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%q is set: %s at %s (%s). Next run %s. It arrives in this chat. It can read and draft but cannot send, post or change anything, because nobody is there to approve it.",
				r.Title, r.DaysText(), at, zone, j.NextRun.In(zone).Format("Mon 2 Jan 15:04")), nil
		},
		Now: func(id string, in map[string]string) (string, error) {
			r, ok := recipe.ByID(id)
			if !ok || r.Kind != recipe.Schedule {
				return "", errors.New("there is nothing to run now for that")
			}
			vals, err := r.Values(in)
			if err != nil {
				return "", err
			}
			if err := ruleRefuses(rt, "task_start"); err != nil {
				return "", err
			}
			j, err := cron.StartChatTask(jobs, ctl(), "web", web.ChatID, r.Title, recipe.Fill(r.Prompt, vals))
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Started %q once now. The result comes to this chat.", j.Name), nil
		},
		Remove: func(id string) error {
			if j, ok := find(id); ok {
				return jobs.Remove(j.ID, j.Gateway, j.ChatID)
			}
			return errors.New("that recipe is not set up")
		},
	}
}

// watchArgs is the call a watch recipe makes to watch_create. It is plain data so it can be checked without a network.
func watchArgs(r recipe.Recipe, vals map[string]string) (json.RawMessage, error) {
	spec := r.Spec
	host := ""
	if u, err := url.Parse(vals["url"]); err == nil {
		host = strings.TrimPrefix(u.Hostname(), "www.")
	}
	args := map[string]any{
		"title":         strings.TrimSuffix(r.Title+": "+host, ": "),
		"tool":          "web_fetch",
		"every_minutes": spec.EveryMinutes,
		"arguments":     json.RawMessage(recipe.Fill(spec.ArgsTemplate, jsonValues(vals))),
		"instructions":  recipe.Fill(spec.Instructions, vals),
	}
	switch spec.Condition {
	case "price_below":
		var usd float64
		fmt.Sscanf(strings.TrimPrefix(vals[spec.USDParam], "$"), "%g", &usd)
		args["when"] = map[string]any{"kind": watch.PriceBelow, "usd": usd, "near": vals[spec.NearParam]}
	case "text_appears":
		args["when"] = map[string]any{"kind": watch.TextAppears, "text": vals[spec.TextParam]}
	}
	return json.Marshal(args)
}

// addWatchRecipe sets up a page watch the way the agent's own watch_create does, so it gets the same first look, the
// same limits and the same permission rules (a rule against setting up watches stops this too).
func addWatchRecipe(rt *app.Runtime, r recipe.Recipe, vals map[string]string) (string, error) {
	raw, err := watchArgs(r, vals)
	if err != nil {
		return "", err
	}
	// A throwaway agent for this one call: it has the same tools and the same rules as a chat, and no history.
	ag := rt.NewAgent(app.AgentOptions{ChatID: web.ChatID, Gateway: "web", NoHistory: true})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return ag.CallApproved(ctx, "watch_create", raw)
}

// jsonValues makes values safe to put inside a JSON string: an address with a quote in it cannot break out.
func jsonValues(vals map[string]string) map[string]string {
	out := make(map[string]string, len(vals))
	for k, v := range vals {
		b, _ := json.Marshal(v)
		out[k] = strings.Trim(string(b), `"`)
	}
	return out
}
