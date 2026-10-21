package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"talaria/internal/tool"
)

// RegisterTools adds the memory tool and, when sessions is non-nil, session_search.
// currentSession is excluded from search results since the model already has it.
func RegisterTools(r *tool.Registry, n *Notes, sessions *Sessions, currentSession, owner string) {
	r.Register(tool.Tool{
		Name: "memory",
		Description: "Edit your persistent notes. target=memory is for your own durable notes (environment facts, conventions, lessons); " +
			"target=user is for the user's preferences and background. Entries are tiny and size-capped, so keep them terse and " +
			"replace or remove stale ones. Changes appear in the prompt from the next session. Save only what will matter later.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"action":{"type":"string","enum":["add","replace","remove"]},
			"target":{"type":"string","enum":["memory","user"]},
			"content":{"type":"string","description":"new entry text (add, replace)"},
			"old":{"type":"string","description":"unique snippet of the existing entry (replace, remove)"}},
			"required":["action","target"]}`),
		// Notes go into every later prompt, unattended runs included, so a note planted by
		// something the conversation read would steer the agent for good.
		Egress: tool.Always,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Action  string
				Target  Target
				Content string
				Old     string
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			var err error
			switch a.Action {
			case "add":
				err = n.Add(a.Target, a.Content)
			case "replace":
				err = n.Replace(a.Target, a.Old, a.Content)
			case "remove":
				err = n.Remove(a.Target, a.Old)
			default:
				err = fmt.Errorf("unknown action %q", a.Action)
			}
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("ok (%s now %s)", a.Target, n.Usage(a.Target)), nil
		},
	})

	if sessions == nil {
		return
	}
	r.Register(tool.Tool{
		Name:        "session_search",
		Description: "Full-text search over past conversations. Use when the user refers to earlier work or you need context from before.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer","description":"max results, default 5"}},"required":["query"]}`),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Query string
				Limit int
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			hits, err := sessions.SearchFor(owner, a.Query, a.Limit, currentSession)
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				return "no matches in past sessions", nil
			}
			var b strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&b, "[%s] %s (%s): %s\n", h.When.Format("2006-01-02 15:04"), h.Role, h.SessionID, h.Snippet)
			}
			return b.String(), nil
		},
	})
}
