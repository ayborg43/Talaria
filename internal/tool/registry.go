// Package tool holds the registry of actions the agent may take.
package tool

import (
	"context"
	"encoding/json"
	"strings"

	"talaria/internal/provider"
)

// Tool is one capability exposed to the model.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema for the arguments object
	// NeedsApproval marks tools with side effects; the agent asks before running them.
	NeedsApproval bool
	// Parallel marks a tool that is safe to run at the same time as other
	// Parallel tools called in the same model turn.
	Parallel bool
	// NeedsApprovalFor, if set, decides per call whether it needs approval, for a
	// tool that is harmless for some arguments and not for others (a watch that is
	// paused versus one that is deleted). NeedsApproval, if set, still applies.
	NeedsApprovalFor func(args json.RawMessage) bool
	// Untrusted marks a tool whose results carry text from outside: a web page, a
	// mail, a connected service's reply. Once one has run, the conversation has
	// read something an attacker could have written.
	Untrusted bool
	// Egress, if set, says whether a call could send something out, or leave
	// something behind that acts later. After the conversation has read outside
	// content such a call asks first, even if it would normally run without asking.
	Egress func(args json.RawMessage) bool
	// Risk says what any call of this tool does that the user may want to decide on: it deletes something, or
	// posts publicly. RiskFor adds to it for a call whose arguments make the difference (a shell command that
	// removes files, a click on a social site). A user who asks to be asked only about those reads them here.
	Risk    Risk
	RiskFor func(args json.RawMessage) Risk
	Run     func(ctx context.Context, args json.RawMessage) (string, error)
	// RunRich, if set, is used instead of Run, for a tool whose result has pictures.
	RunRich func(ctx context.Context, args json.RawMessage) (Result, error)
	// Describe, if set, says in plain words what a call will do, for the
	// approval prompt, which is clearer than raw arguments (a click on "element
	// 7" tells the user nothing). It must be quick and must not change anything.
	Describe func(args json.RawMessage) string
	// LogArgs, if set, says what to put in the activity log in place of a call's
	// raw arguments, for a call whose arguments hold something that should not
	// be written down (what is typed into a password field).
	LogArgs func(args json.RawMessage) string
}

// RiskOf is what one call could do, from the tool's own declaration and the call's arguments.
func (t Tool) RiskOf(args json.RawMessage) Risk {
	r := t.Risk
	if t.RiskFor != nil {
		r |= t.RiskFor(args)
	}
	return r
}

// Result is what a tool returns when it has more than text.
type Result struct {
	Text   string
	Images []provider.Image
}

// Execute runs the tool by whichever of Run and RunRich it has.
func (t Tool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if t.RunRich != nil {
		return t.RunRich(ctx, args)
	}
	text, err := t.Run(ctx, args)
	return Result{Text: text}, err
}

type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Name]; !exists {
		r.order = append(r.order, t.Name)
	}
	r.tools[t.Name] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Specs() []provider.ToolSpec {
	specs := make([]provider.ToolSpec, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		specs = append(specs, provider.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.Schema})
	}
	return specs
}

// RemovePrefix removes every tool whose name starts with prefix, in place.
func (r *Registry) RemovePrefix(prefix string) {
	kept := r.order[:0]
	for _, name := range r.order {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
		} else {
			kept = append(kept, name)
		}
	}
	r.order = kept
}

// WithoutPrefix returns a copy of the registry that leaves out every tool whose
// name starts with prefix.
func (r *Registry) WithoutPrefix(prefix string) *Registry {
	out := NewRegistry()
	for _, name := range r.order {
		if !strings.HasPrefix(name, prefix) {
			out.Register(r.tools[name])
		}
	}
	return out
}

// Without returns a copy of the registry that leaves out the named tools.
func (r *Registry) Without(names ...string) *Registry {
	skip := make(map[string]bool, len(names))
	for _, n := range names {
		skip[n] = true
	}
	out := NewRegistry()
	for _, name := range r.order {
		if !skip[name] {
			out.Register(r.tools[name])
		}
	}
	return out
}
