package guard

import (
	"fmt"
	"slices"
	"strings"
)

// Modes a kind of action can be set to. "default" is no rule at all: the tool does what it always did.
const (
	ModeDefault = "default"
	ModeAllow   = "allow"   // just do it, without asking
	ModeAsk     = "ask"     // ask me first, every time
	ModeHandoff = "handoff" // do not do it: ask me to do it myself
	ModeDeny    = "deny"    // never
)

// Modes lists them in the order Settings shows them.
var Modes = []string{ModeDefault, ModeAllow, ModeAsk, ModeHandoff, ModeDeny}

// Category is a kind of action the user can set one rule for, instead of one rule per tool. A name ending in *
// covers a family (a connected service's tools).
type Category struct {
	ID, Title, Detail string
	Tools             []string
	// Risky says "just do it" here lets the agent act on the machine or the world with nobody checking, so the
	// page asks twice before it is chosen.
	Risky bool
}

// Categories are the kinds of action the Rules page offers. Together they hold every tool that asks by default,
// so a rule here covers what the agent can do that matters.
var Categories = []Category{
	{ID: "email", Title: "Send email", Detail: "Sending a message or a reply from your mailbox.", Tools: []string{"email_send", "email_reply"}, Risky: true},
	{ID: "calendar", Title: "Change your calendar", Detail: "Adding, changing or deleting events.", Tools: []string{"calendar_add", "calendar_update", "calendar_delete"}},
	{ID: "social", Title: "Post on X", Detail: "Publishing a post in your name.", Tools: []string{"x_post"}, Risky: true},
	{ID: "commands", Title: "Run commands", Detail: "Anything typed in a shell on the agent's computer.", Tools: []string{"shell"}, Risky: true},
	{ID: "files", Title: "Write and delete files", Detail: "Saving or changing files, filling in PDF forms, deleting kept files.", Tools: []string{"write_file", "pdf_form_fill", "files_delete"}, Risky: true},
	{ID: "browser", Title: "Click and type in web pages", Detail: "Pressing buttons and typing in the agent's browser. Using a saved login always asks, whatever you set here.", Tools: []string{"browser_click", "browser_type", "browser_press", "browser_select"}, Risky: true},
	{ID: "schedule", Title: "Schedule and start work", Detail: "Reminders, goals, watches, background tasks and saved skills, which keep acting when you are away.", Tools: []string{"cron_add", "cron_remove", "phone_trigger_add", "task_start", "goal_create", "goal_update", "watch_create", "watch_update", "skill_save", "skill_delete"}},
	{ID: "phone", Title: "Read your phone", Detail: "Its location and battery, your contacts, calendar and notifications. The phone has its own switch for each; nothing is shared until you turn it on there.", Tools: []string{"phone_info", "phone_read"}},
	{ID: "apps", Title: "Use connected apps", Detail: "Anything done through a connected service (an MCP server).", Tools: []string{"mcp_*"}},
}

// CategoryByID finds one.
func CategoryByID(id string) (Category, bool) {
	for _, c := range Categories {
		if c.ID == id {
			return c, true
		}
	}
	return Category{}, false
}

// ModeOf says what a category is set to in rules: the one mode all its tools share, ModeDefault when none has a
// rule, and mixed when they differ (a rule written by hand for one tool of the group).
func ModeOf(rules map[string]string, c Category) (mode string, mixed bool) {
	seen := map[string]bool{}
	for _, t := range c.Tools {
		m := strings.ToLower(strings.TrimSpace(rules[t]))
		if m == "" {
			m = ModeDefault
		}
		seen[m] = true
	}
	if len(seen) != 1 {
		return ModeDefault, true
	}
	for m := range seen {
		return m, false
	}
	return ModeDefault, false
}

// SetMode returns rules with the category set to mode: a rule for each of its tools, or none for ModeDefault.
// Rules for other tools are untouched, and rules the config holds for a longer family inside the category's own
// (mcp_mail_*) still win, as they always do.
func SetMode(rules map[string]string, c Category, mode string) (map[string]string, error) {
	if !slices.Contains(Modes, mode) {
		return nil, fmt.Errorf("the mode is one of: %s", strings.Join(Modes, ", "))
	}
	out := make(map[string]string, len(rules)+len(c.Tools))
	for k, v := range rules {
		out[k] = v
	}
	for _, t := range c.Tools {
		if mode == ModeDefault {
			delete(out, t)
		} else {
			out[t] = mode
		}
	}
	return out, nil
}

// Other lists the rules no category owns (a rule written by hand for one tool or a family), so the page can show
// everything that is in force and not only what it edits.
func Other(rules map[string]string) map[string]string {
	owned := map[string]bool{}
	for _, c := range Categories {
		for _, t := range c.Tools {
			owned[t] = true
		}
	}
	out := map[string]string{}
	for k, v := range rules {
		if !owned[k] {
			out[k] = v
		}
	}
	return out
}
