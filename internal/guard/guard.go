package guard

import (
	"fmt"
	"strings"
	"time"

	"talaria/internal/agent"
)

// Guard ties the permission rules, the user's grants, the activity log and
// the redactor together. Any part may be nil: a nil Policy has no rules, nil
// Grants grants nothing, a nil Audit keeps no log, a nil Redact scrubs nothing.
type Guard struct {
	Policy *Policy
	Grants *Grants
	Audit  *Log
	Redact *Redactor
}

// Scope is a Guard as one agent sees it: it knows who the agent is working for,
// so it can find that chat's grants and label the log.
type Scope struct {
	g         *Guard
	origin    string
	gateway   string
	chat      int64
	useGrants bool
}

// For makes the agent.Gate for one conversation.
//
//	origin   "cli", "chat" or "job:<id>", for the log
//	gateway  the chat service, "" for the command line
//	chat     the chat on it; 0 for none
//
// A chat or the command line may have grants. A scheduled job never does: it
// runs unattended, so only the config's rules apply to it.
func (g *Guard) For(origin, gateway string, chat int64) *Scope {
	return &Scope{g: g, origin: origin, gateway: gateway, chat: chat, useGrants: origin == "cli" || origin == "chat"}
}

func grantKey(gateway string, chat int64) string {
	if gateway == "" {
		return "cli"
	}
	return fmt.Sprintf("%s:%d", gateway, chat)
}

// Verdict implements agent.Gate. A permission rule decides first. Failing that,
// a grant allows; failing that, the tool's own default applies.
func (s *Scope) Verdict(tool string, needsApproval bool) agent.Verdict {
	if r, ok := s.g.Policy.match(tool); ok {
		name := r.pattern
		if r.prefix {
			name += "*"
		}
		return agent.Verdict{Action: r.action, Via: "rule", Rule: name + " = " + actionName(r.action)}
	}
	// A grant is only honoured for a tool it could have been made for: a file edited by hand,
	// or a tool listed in no_remember after the grant was given, does not get round that list.
	if needsApproval && s.useGrants && s.g.Grants != nil && s.g.Policy.Rememberable(tool) && s.g.Grants.Allowed(grantKey(s.gateway, s.chat), tool) {
		return agent.Verdict{Action: agent.Allow, Via: "grant"}
	}
	if needsApproval {
		return agent.Verdict{Action: agent.Ask, Via: "auto"}
	}
	return agent.Verdict{Action: agent.Allow, Via: "auto"}
}

// Scrub implements agent.Gate.
func (s *Scope) Scrub(text string) string { return s.g.Redact.Scrub(text) }

// Record implements agent.Gate.
func (s *Scope) Record(e agent.Entry) {
	if s.g.Audit == nil {
		return
	}
	r := Record{
		Time: time.Now().UTC(), Origin: s.origin, Gateway: s.gateway, Chat: s.chat,
		Tool: e.Tool, Label: e.Label, Allowed: e.Allowed, Via: e.Via, Rule: e.Rule, Status: e.Status,
		Args:   clip(s.g.Redact.Scrub(e.Args), maxArgsShown),
		Result: clip(s.g.Redact.Scrub(e.Result), maxResultKept),
		Bytes:  len(e.Result), Millis: e.Took.Milliseconds(),
	}
	s.g.Audit.Append(r) // a log that cannot be written must not stop the agent
}

// Rememberable reports whether this scope may keep a choice to always allow tool.
func (s *Scope) Rememberable(tool string) bool {
	return s.useGrants && s.g.Grants != nil && s.g.Policy.Rememberable(tool)
}

// Remember records that this scope's chat always allows a tool. It is what the
// "always allow" button does.
func (s *Scope) Remember(tool string) error {
	if s.g.Grants == nil {
		return fmt.Errorf("remembering is not set up")
	}
	return s.g.Grants.Add(grantKey(s.gateway, s.chat), tool)
}

// ChatPermissions is the Guard as a chat service's engine uses it: one
// service, many chats.
type ChatPermissions struct {
	G       *Guard
	Gateway string
}

// Rememberable reports whether "always allow" is offered for a tool.
func (p ChatPermissions) Rememberable(tool string) bool {
	return p.G.Grants != nil && p.G.Policy.Rememberable(tool)
}

// Remember keeps a chat's choice to always allow a tool.
func (p ChatPermissions) Remember(chat int64, tool string) error {
	if p.G.Grants == nil {
		return fmt.Errorf("remembering is not set up")
	}
	return p.G.Grants.Add(grantKey(p.Gateway, chat), tool)
}

// Describe says what a chat may do without asking.
func (p ChatPermissions) Describe(chat int64) string {
	var b strings.Builder
	b.WriteString("Rules from the config file:\n" + p.G.Policy.Describe())
	b.WriteString("\n\nTools this chat always allows: ")
	var granted []string
	if p.G.Grants != nil {
		granted = p.G.Grants.List(grantKey(p.Gateway, chat))
	}
	if len(granted) == 0 {
		b.WriteString("none")
	} else {
		b.WriteString(strings.Join(granted, ", "))
	}
	return b.String()
}

// Reset forgets a chat's grants and says how many there were.
func (p ChatPermissions) Reset(chat int64) (int, error) {
	if p.G.Grants == nil {
		return 0, nil
	}
	return p.G.Grants.Reset(grantKey(p.Gateway, chat))
}

// Activity renders a chat's most recent tool calls for display in the chat.
func (p ChatPermissions) Activity(chat int64, n int) string {
	if p.G.Audit == nil {
		return "The activity log is turned off."
	}
	recs, err := p.G.Audit.Recent(Filter{Gateway: p.Gateway, Chat: chat}, n)
	if err != nil {
		return "Could not read the activity log: " + err.Error()
	}
	if len(recs) == 0 {
		return "Nothing has been run in this chat yet."
	}
	lines := make([]string, len(recs))
	for i, r := range recs {
		lines[i] = Format(r)
	}
	return strings.Join(lines, "\n")
}
