package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"talaria/internal/tool"
)

// What the user answered when asked to take over the browser.
const (
	HandoffDone    = tool.HandoffDone    // they finished what only they could do
	HandoffSkip    = tool.HandoffSkip    // they chose not to
	HandoffReplied = tool.HandoffReplied // they wrote in the chat instead
	HandoffTimeout = tool.HandoffTimeout // nobody answered in time
)

// Handoff asks the user to take over the agent's tab for something only a person can do, and waits until they
// answer. reason says what for and url is the page it is on. A front end that cannot ask (a scheduled job, a chat
// service with no page to show the tab) gives none, and the tool is then not offered at all.
type Handoff func(ctx context.Context, reason, url string) (outcome string, err error)

// RegisterHandoff adds browser_handoff for one conversation: the agent's way to say "I need you here", instead of
// describing it in chat and waiting for a message. It returns the page as it is when the user is done.
func RegisterHandoff(r *tool.Registry, m *Manager, owner string, ask Handoff) {
	t := &toolset{m: m, owner: owner, handoff: true}
	r.Register(tool.Tool{
		Name: "browser_handoff",
		Description: "Ask the user to take over the browser for something only they can do: sign in (a password, a code sent to their phone, a captcha), approve a prompt, or pass a human check. " +
			"Say in reason what for, such as \"Sign in to x.com\". The user is notified and gets a button that opens this tab; the call waits (up to 20 minutes) until they say they are done, then returns the page as it is now. " +
			"Use it whenever a page needs a login you have no saved credential for. Never ask for a password, code or key in the chat, and never try to guess one." + usage,
		Schema:    json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","description":"what the user is needed for, in a short sentence, such as: Sign in to x.com"}},"required":["reason"]}`),
		Untrusted: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct{ Reason string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			reason := clip(strings.Join(strings.Fields(a.Reason), " "), 200)
			if reason == "" {
				return "", errors.New("say what the user is needed for")
			}
			p, note, err := t.page(ctx)
			if err != nil {
				return "", err
			}
			if note != "" {
				return "", errors.New("there is no page open to take over: open the site first")
			}
			outcome, err := ask(ctx, reason, p.Where(ctx))
			if err != nil {
				return "", err
			}
			switch outcome {
			case HandoffDone:
				return t.finish(ctx, p, "The user says they are done. Read the result below before going on: if it still shows the sign-in, say so and ask again.")
			case HandoffSkip:
				return "The user chose not to do this. Do not ask again for the same thing: carry on without it, or tell them what you could not do.", nil
			case HandoffReplied:
				return "The user wrote in the chat instead of finishing in the browser: their message comes next. The page is still open.", nil
			default:
				return "Nobody answered in time. The page is still open; tell the user what is waiting, and ask again only if they come back.", nil
			}
		},
	})
}

// signInHint is added to a page listing that has a password field, when the agent can ask for the user.
const signInHint = "This page asks for a password. If you have no saved login for this site, call browser_handoff so the user can sign in themselves; never ask for a password in the chat."

func hasPasswordField(s *Snapshot) bool {
	for _, e := range s.Elements {
		if e.Type == "password" {
			return true
		}
	}
	return false
}
