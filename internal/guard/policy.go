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

// knownTools names every tool Talaria itself registers, so a misspelt rule is
// an error at load rather than a rule that silently does nothing. A connected
// service's tools are mcp_<server>_<tool> and cannot be listed here: their
// servers are only known once Talaria runs. TestKnownToolsMatchesTheRegisteredTools
// keeps this list in step with the registrations.
var knownTools = map[string]bool{
	"browser_back": true, "browser_click": true, "browser_close": true,
	"browser_fill": true, "browser_open": true, "browser_press": true,
	"browser_screenshot": true, "browser_scroll": true, "browser_select": true,
	"browser_snapshot": true, "browser_type": true, "browser_wait": true,
	"calendar_events": true,
	"cron_add":        true, "cron_list": true, "cron_remove": true,
	"current_time": true, "delegate": true,
	"email_folders": true, "email_list": true, "email_read": true,
	"goal_create": true, "goal_get": true, "goal_list": true, "goal_update": true,
	"list_dir": true, "memory": true, "read_file": true, "session_search": true,
	"shell":        true,
	"skill_delete": true, "skill_list": true, "skill_save": true, "skill_view": true,
	"task_cancel": true, "task_list": true, "task_start": true,
	"vault_list":   true,
	"watch_create": true, "watch_list": true, "watch_update": true,
	"write_file": true,
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
		if err := checkRuleName(name, r.prefix); err != nil {
			return nil, err
		}
		p.rules = append(p.rules, r)
	}
	return p, nil
}

// checkRuleName rejects a rule for a tool that does not exist, so a typo can
// never silently protect nothing. A name starting with mcp_ is a connected
// service's tool, whose servers are only known once Talaria runs, and a lone *
// covers everything on purpose; both are accepted. A family is accepted when
// it matches at least one tool Talaria has.
func checkRuleName(name string, prefix bool) error {
	if !prefix {
		if knownTools[name] || strings.HasPrefix(name, "mcp_") {
			return nil
		}
		return fmt.Errorf("rule %q: no such tool", name)
	}
	bare := strings.TrimSuffix(name, "*")
	if bare == "" || bare == "mcp" || strings.HasPrefix(bare, "mcp_") {
		return nil
	}
	for tool := range knownTools {
		if strings.HasPrefix(tool, bare) {
			return nil
		}
	}
	return fmt.Errorf("rule %q: no tool starts with %q", name, bare)
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
