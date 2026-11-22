package subagent

import (
	"fmt"
	"strings"
)

// Named is one of the user's saved agents (set up under Agents in the app), as a task can be handed to it: its own
// instructions, perhaps its own model, and whether it may only look. The agent can use them and nothing more; only the
// person makes or removes them, so the team never grows or changes behind anyone's back.
type Named struct {
	Name         string
	About        string // a line to choose by
	Instructions string
	Profile      string // its model profile; empty is the main agent's own
	ReadOnly     bool   // it may only look: nothing that needs approval is open to it
	Scopes       []string
}

func (p *Pool) namedAgents() []Named {
	if p.Named == nil {
		return nil
	}
	return p.Named()
}

// agentNamed finds the saved agent a task is handed to; nil and no error when none was asked for.
func (p *Pool) agentNamed(name string) (*Named, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	var names []string
	for _, n := range p.namedAgents() {
		if strings.EqualFold(n.Name, name) {
			return &n, nil
		}
		names = append(names, n.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("there is no saved agent called %q: none are set up, so leave agent out", name)
	}
	return nil, fmt.Errorf("there is no saved agent called %q: choose one of %s, or leave agent out", name, strings.Join(names, ", "))
}
