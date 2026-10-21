package guard

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"talaria/internal/agent"
)

// DefaultNoRemember lists the tools the "always allow" button is not offered
// for unless the config says otherwise. A shell command can do anything, so
// approving one is not approving the next. The rest are the ones whose effects
// outlast the conversation or run with nobody watching: a file written (a
// startup script, a setting) acts later, and a saved skill, scheduled job, task,
// goal or watch steers the agent when no one is there to approve what it does.
var DefaultNoRemember = []string{"shell", "write_file", "skill_save", "cron_add", "task_start", "goal_create", "watch_create", "watch_update", "goal_update"}

type rule struct {
	pattern string // as written: a tool name, or a name ending in *
	prefix  bool
	action  agent.Action
}

// Policy is the configured permission rules.
type Policy struct {
	rules []rule

	mu         sync.RWMutex
	noRemember map[string]bool
}

// ParsePolicy reads rules from the config: each key is a tool name, or a name
// ending in * for a family of tools (such as an MCP server's), and each value
// is "allow", "ask" or "deny". noRemember names tools that "always allow" is
// not offered for; nil means DefaultNoRemember.
func ParsePolicy(rules map[string]string, noRemember []string) (*Policy, error) {
	p := &Policy{noRemember: map[string]bool{}}
	if noRemember == nil {
		noRemember = DefaultNoRemember
	}
	for _, t := range noRemember {
		p.noRemember[t] = true
	}
	names := make([]string, 0, len(rules))
	for name := range rules {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		verdict := rules[name]
		r := rule{pattern: name}
		switch strings.ToLower(strings.TrimSpace(verdict)) {
		case "allow":
			r.action = agent.Allow
		case "ask":
			r.action = agent.Ask
		case "deny":
			r.action = agent.Deny
		default:
			return nil, fmt.Errorf("rule %q: %q is not allow, ask or deny", name, verdict)
		}
		switch stars := strings.Count(name, "*"); {
		case name == "" || strings.TrimSpace(name) != name:
			return nil, fmt.Errorf("rule %q: not a tool name", name)
		case stars == 1 && strings.HasSuffix(name, "*"):
			r.prefix, r.pattern = true, strings.TrimSuffix(name, "*")
		case stars > 0:
			return nil, fmt.Errorf("rule %q: * is only allowed at the end of a name", name)
		}
		p.rules = append(p.rules, r)
	}
	return p, nil
}

// match finds the rule for a tool: an exact name wins, then the longest prefix.
func (p *Policy) match(tool string) (rule, bool) {
	if p == nil {
		return rule{}, false
	}
	best, found := rule{}, false
	for _, r := range p.rules {
		switch {
		case !r.prefix && r.pattern == tool:
			return r, true
		case r.prefix && strings.HasPrefix(tool, r.pattern) && (!found || len(r.pattern) > len(best.pattern)):
			best, found = r, true
		}
	}
	return best, found
}

// Rememberable reports whether a choice to always allow tool may be offered
// and kept: not for tools a rule already decides, and not for the ones the
// config keeps out of it, or that were added with NeverRemember.
func (p *Policy) Rememberable(tool string) bool {
	if p == nil {
		return true
	}
	_, decided := p.match(tool)
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !decided && !p.noRemember[tool]
}

// NeverRemember keeps tools out of "always allow", as the config's
// no_remember does, for tools only known once Talaria is running: a
// connected service's tools that send or delete.
func (p *Policy) NeverRemember(tools ...string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range tools {
		p.noRemember[t] = true
	}
}

// Describe lists the rules, one per line.
func (p *Policy) Describe() string {
	if p == nil || len(p.rules) == 0 {
		return "no rules"
	}
	var b strings.Builder
	for _, r := range p.rules {
		name := r.pattern
		if r.prefix {
			name += "*"
		}
		fmt.Fprintf(&b, "%s: %s\n", name, actionName(r.action))
	}
	return strings.TrimRight(b.String(), "\n")
}

func actionName(a agent.Action) string {
	return [...]string{"allow", "ask", "deny"}[a]
}
