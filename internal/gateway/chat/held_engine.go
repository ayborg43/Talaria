package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"talaria/internal/agent"
	"talaria/internal/execenv"
)

// where says where a call runs, when that is not where Talaria runs. It matters when deciding whether to allow a
// call, and with per-chat environments the startup banner no longer says. Only the tools that go to the environment
// say so.
func (e *Engine) where(env, base string) string {
	if e.Environments != nil && env != "" && env != execenv.LocalName {
		switch base {
		case "shell", "read_file", "write_file", "list_dir":
			return " in " + env
		}
	}
	return ""
}

// freshToken makes the name of a request that waits: unlike one that is answered within minutes, it must not
// collide with any that is still waiting from before a restart.
func (e *Engine) freshToken() (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for range 8 {
		token, ok := newToken(e.pending)
		if !ok {
			return "", false
		}
		if !e.Held.Has(token) {
			return token, true
		}
	}
	return "", false
}

// deferRequest is the agent's Defer for a chat: it keeps a request for permission and lets the work go on, instead of
// holding the work up until the user answers.
func (e *Engine) deferRequest(c *chat) func(context.Context, agent.Request) (string, bool) {
	return func(ctx context.Context, r agent.Request) (string, bool) {
		if e.Held == nil {
			return "", false
		}
		if _, dup := e.Held.Same(c.id, r.Tool, r.Args); dup {
			return waitingNote(r.Why, true), true
		}
		c.mu.Lock()
		live, env, task := c.live, c.applied, c.task
		c.mu.Unlock()
		if live != nil {
			live.endSegment() // the text so far goes first; the question follows it
		}
		body, ok := e.approvalBody(c.id, r.Shown)
		if !ok {
			return "error: that request was too large to put to the user, so it was not run", true
		}
		token, ok := e.freshToken()
		if !ok {
			e.logf("approval for %s not kept: no randomness for its token", r.Name)
			return "", false
		}
		text := fmt.Sprintf("Allow %s%s?\n", r.Name, e.where(env, r.Tool))
		if r.Why != "" {
			text += r.Why + "\n"
		}
		text += body + "\n\nI will carry on with anything else, and do this once you say yes."
		always := e.Permissions != nil && !e.Destructive && e.Permissions.Rememberable(r.Tool) && !r.Flagged
		h := Held{Token: token, Chat: c.id, Name: r.Name, Tool: r.Tool, Args: r.Args, Shown: r.Shown, Why: r.Why, Flagged: r.Flagged, Task: clipRunes(task, 4000)}
		if err := e.Held.Add(h); err != nil {
			e.logf("approval for %s not kept, so it will be asked in the usual way: %v", r.Name, err)
			return "", false
		}
		if _, err := e.T.SendApproval(ctx, c.id, text, token, always); err != nil {
			e.Held.Take(token)
			e.logf("approval prompt failed: %v", err)
			return "", false
		}
		return waitingNote(r.Why, false), true
	}
}

// waitingNote is what the model is told when the user was asked and has not answered.
func waitingNote(why string, already bool) string {
	var b strings.Builder
	b.WriteString("This needs the user's permission")
	if why != "" {
		b.WriteString(" (" + strings.TrimSuffix(why, ".") + ")")
	}
	if already {
		b.WriteString(", and they have already been asked: it is still waiting. ")
	} else {
		b.WriteString(", so it was not run. They have been asked, and the request is saved: if they allow it, it is run and you are told, even if this conversation has been closed by then. ")
	}
	b.WriteString("Do not ask again or look for another way to do the same thing. Do whatever else you can that does not need it, then tell the user briefly what is waiting for their OK.")
	return b.String()
}

// decideHeld answers a request that was kept: the call is carried out by the chat's worker, so it runs where
// everything else for the chat runs, and the work is taken up again.
func (e *Engine) decideHeld(token string, user, chatID int64, choice Choice) (Decision, Choice) {
	if e.Held == nil {
		return DecisionExpired, choice
	}
	h, ok := e.Held.Get(token)
	if !ok {
		e.mu.Lock()
		was := e.answered[token]
		e.mu.Unlock()
		if was {
			return DecisionAnswered, choice
		}
		return DecisionExpired, choice
	}
	if !e.Allowed[user] || chatID != h.Chat {
		return DecisionForbidden, choice
	}
	if choice == ChoiceAlways && (e.Permissions == nil || e.Destructive || h.Flagged || !e.Permissions.Rememberable(h.Tool)) {
		choice = ChoiceAllow
	}
	if _, took := e.Held.Take(token); !took {
		return DecisionAnswered, choice
	}
	e.mu.Lock()
	e.answered[token] = true
	e.mu.Unlock()
	if choice == ChoiceAlways {
		if err := e.Permissions.Remember(h.Chat, h.Tool); err != nil {
			e.logf("keeping an always-allow choice failed: %v", err)
		}
	}
	c := e.chatFor(h.Chat)
	go func() {
		select {
		case c.resume <- resumeJob{h: h, choice: choice}:
		case <-e.root.Done():
		}
	}()
	return DecisionRecorded, choice
}

// carryOut runs an allowed request, and says what to do next to the agent: the prompt that takes the work up again.
func (e *Engine) carryOut(ctx context.Context, c *chat, a *agent.Agent, job resumeJob) string {
	h := job.h
	var b strings.Builder
	if job.choice == ChoiceDeny {
		fmt.Fprintf(&b, "The user was asked about the request you were waiting on, and they did not allow it.\nRequest: %s\n%s\n\nDo not do it, and do not look for another way to do the same thing. Carry on with anything else they asked for that does not need it, then tell them what was done and what was skipped.", h.Name, clipRunes(h.Shown, 1500))
	} else {
		// The agent reports the start of the call itself (OnToolStart); its end is only reported for calls in the
		// loop, so it is reported here.
		out, err := a.CallApproved(ctx, h.Tool, json.RawMessage(h.Args))
		result := out
		if err != nil {
			result = "error: " + err.Error()
		}
		if tw, ok := e.T.(ToolWatcher); ok {
			tw.ToolEnd(c.id, h.Tool, result)
		}
		fmt.Fprintf(&b, "The user allowed the request you were waiting on, so it has now been carried out.\nRequest: %s\n%s\n\nResult:\n%s\n\nCarry on from here and finish the job, then tell the user what was done.", h.Name, clipRunes(h.Shown, 1500), clipRunes(result, 6000))
	}
	if h.Task != "" {
		b.WriteString("\n\nWhat they originally asked for:\n" + h.Task)
	}
	return b.String()
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
