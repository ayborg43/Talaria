package main

import (
	"errors"
	"strings"

	"talaria/internal/app"
	"talaria/internal/cron"
	"talaria/internal/idea"
	"talaria/internal/web"
)

// webSuggestions connects the Tasks screen to the agent's suggestions: the list, Start and Hide. Start accepts the
// idea and begins a background task from its prompt; Hide dismisses it for good. The task is the same kind the
// model can start in chat, with risky tools denied, so a tap cannot make it do more than it could be asked to.
// ctl is looked up when needed, because the scheduler is made after the app's sources.
func webSuggestions(ideas *idea.Store, jobs *cron.Store, ctl func() cron.Control) (func() []web.SuggestionInfo, func(string) (string, error), func(string) error) {
	list := func() []web.SuggestionInfo {
		var out []web.SuggestionInfo
		for _, it := range ideas.Suggest("web", web.ChatID, nil, nil) {
			si := web.SuggestionInfo{ID: it.ID, Title: tidy(it.Title, 120, "(suggestion)"), Body: tidy(it.Body, 300, ""), At: it.Created, Startable: strings.TrimSpace(it.Prompt) != ""}
			for _, src := range it.Sources {
				si.Sources = append(si.Sources, web.SuggestionSource{Title: tidy(src.Title, 80, ""), URL: src.URL})
			}
			out = append(out, si)
		}
		if len(out) > 10 {
			out = out[:10]
		}
		return out
	}
	start := func(id string) (string, error) {
		it, err := ideas.Get("web", web.ChatID, id)
		if err != nil {
			return "", err
		}
		if it.Status != idea.Open && it.Status != idea.Edited {
			return "", errors.New("that suggestion has already been dealt with")
		}
		if strings.TrimSpace(it.Prompt) == "" {
			return "", errors.New("that one is a note, not something to run: hide it, or ask about it in chat")
		}
		j, err := cron.StartChatTask(jobs, ctl(), "web", web.ChatID, tidy(it.Title, 60, "Suggested task"), it.Prompt)
		if err != nil {
			return "", err // the idea stays open, so it can be started once a slot is free
		}
		if err := ideas.Accept("web", web.ChatID, id); err != nil {
			return j.Name, nil // it is running; being listed once more is the lesser harm
		}
		return j.Name, nil
	}
	hide := func(id string) error { return ideas.Dismiss("web", web.ChatID, id) }
	return list, start, hide
}

// applyRules is how the Rules page puts a change in force without a restart: the running policy is replaced, and the
// next tool call of every chat obeys it. It is nil when there is no policy to change.
func applyRules(rt *app.Runtime) func(map[string]string) error {
	gd := rt.Guard()
	if gd == nil || gd.Policy == nil {
		return nil
	}
	return gd.Policy.SetRules
}
