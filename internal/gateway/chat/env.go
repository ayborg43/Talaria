package chat

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"talaria/internal/agent"
	"talaria/internal/execenv"
)

// Environments lets each chat choose where its shell and file tools run with /env.
//
// Names are the only environments a chat may pick. The operator decides that
// list, and a chat can never reach one that is not on it: if the gateway was
// started inside a sandbox, "local" is simply not an option unless it was
// listed, because that choice is the operator's, not the chat's.
type Environments struct {
	Default string   // where a chat's tools run until it picks another
	Names   []string // what a chat may pick, sorted; includes Default
	// Summary describes an environment in one line without starting it.
	Summary func(name string) string
	// Running reports whether an environment has already been started.
	Running func(name string) bool
	// Get starts an environment, or returns the one already running. It waits as
	// long as that takes, or until ctx ends.
	Get func(ctx context.Context, name string) (execenv.Env, error)
	// Apply points a live agent's tools at env.
	Apply func(a *agent.Agent, env execenv.Env)
}

// canSwitch reports whether there is anything for /env to choose between.
func (g *Engine) canSwitch() bool {
	return g.Environments != nil && len(g.Environments.Names) > 1
}

func (g *Engine) defaultEnv() string {
	if g.Environments == nil {
		return ""
	}
	return g.Environments.Default
}

// selectedEnv is the chat's chosen environment, or the default's name.
func (g *Engine) selectedEnv(c *chat) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.envName != "" {
		return c.envName
	}
	return g.Environments.Default
}

// describeEnv describes an environment in full if it is running, else briefly.
func (g *Engine) describeEnv(name string) string {
	e := g.Environments
	if e.Running(name) {
		if env, err := e.Get(g.root, name); err == nil {
			return env.Describe()
		}
	}
	return e.Summary(name)
}

func (g *Engine) describeEnvironments(c *chat) string {
	e := g.Environments
	current := g.selectedEnv(c)
	var out strings.Builder
	fmt.Fprintf(&out, "This chat's shell and file tools run in: %s\n\nEnvironments:\n", current)
	for _, name := range e.Names {
		line := "  " + name + " (" + e.Summary(name) + ")"
		if name == current {
			line += "  <- this chat"
		}
		if name == e.Default {
			line += "  [default]"
		}
		out.WriteString(line + "\n")
	}
	fmt.Fprintf(&out, "\nSwitch with %s <name>. %s default returns to %s.", g.cmd("env"), g.cmd("env"), e.Default)
	return out.String()
}

// handleEnv serves /env: show the environments, or switch this chat to one.
// It returns at once, because starting an environment can take minutes (an
// image to pull, a host to reach); the switch finishes in the background, and
// the chat's next message waits for it.
func (g *Engine) handleEnv(c *chat, args []string) {
	if !g.canSwitch() {
		where := g.Environment
		if where == "" {
			where = "this machine"
		}
		g.send(c.id, "Switching environments is not set up. This chat's shell and file tools run in "+where+".\n"+
			"To let chats switch, name the environments they may use under "+strings.ToLower(g.T.Service())+".environments in the config file.")
		return
	}
	e := g.Environments
	if len(args) == 0 {
		g.send(c.id, g.describeEnvironments(c))
		return
	}

	name := args[0]
	if name == "default" || name == "reset" {
		name = e.Default
	}
	if !slices.Contains(e.Names, name) {
		g.send(c.id, fmt.Sprintf("There is no environment named %q that chats may use.\n\n%s", args[0], g.describeEnvironments(c)))
		return
	}

	c.mu.Lock()
	idle := c.switching == nil
	c.mu.Unlock()
	if current := g.selectedEnv(c); name == current && idle {
		g.send(c.id, "This chat already uses "+name+".")
		return
	}

	// A newer request replaces one still starting.
	c.mu.Lock()
	if c.switchCancel != nil {
		c.switchCancel()
	}
	c.switchGen++
	gen := c.switchGen
	ctx, cancel := context.WithCancel(g.root)
	done := make(chan struct{})
	c.switching, c.switchCancel = done, cancel
	c.mu.Unlock()

	if !e.Running(name) {
		g.send(c.id, "Starting "+name+" ("+e.Summary(name)+"). This can take a while the first time.")
	}
	go g.finishSwitch(ctx, cancel, c, gen, name, done)
}

func (g *Engine) finishSwitch(ctx context.Context, cancel context.CancelFunc, c *chat, gen int, name string, done chan struct{}) {
	defer close(done)
	defer cancel()
	e := g.Environments
	env, err := e.Get(ctx, name)

	stored := name // the default is stored as "" so the chat follows it if the config changes
	if name == e.Default {
		stored = ""
	}
	c.mu.Lock()
	latest := gen == c.switchGen
	prev := c.envName
	if prev == "" {
		prev = e.Default
	}
	if latest {
		c.switching, c.switchCancel = nil, nil
		if err == nil {
			c.envName = stored // committed before done closes, so the next message sees it
		}
	}
	c.mu.Unlock()

	switch {
	case !latest:
		return // a newer /env replaced this one, and reports for itself
	case err != nil && ctx.Err() != nil:
		g.send(c.id, fmt.Sprintf("Stopped waiting for %s. This chat stays on %s. (It keeps starting in the background, so asking again will be quick.)", name, prev))
		return
	case err != nil:
		g.send(c.id, fmt.Sprintf("Cannot switch to %s: %v\nThis chat stays on %s.", name, err, prev))
		return
	}

	reply := fmt.Sprintf("Switched to %s: %s.\nThe conversation continues, but its shell and file tools now act there. Files and results from before belong to %s.", name, env.Describe(), prev)
	if g.Prefs != nil {
		if perr := g.Prefs.SetEnvironment(c.id, stored); perr != nil {
			g.logf("saving the environment choice failed: %v", perr)
			reply += "\n(I could not save this choice, so it will not survive a restart.)"
		}
	}
	g.send(c.id, reply)
}

// cancelSwitch stops waiting for an environment that is still starting.
func (c *chat) cancelSwitch() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.switchCancel == nil {
		return false
	}
	c.switchCancel()
	return true
}

// waitForSwitch holds a task back until any /env switch the chat asked for has
// finished, so a message sent after "/env sandbox" runs in the sandbox.
func (g *Engine) waitForSwitch(ctx context.Context, c *chat) error {
	for {
		c.mu.Lock()
		pending := c.switching
		c.mu.Unlock()
		if pending == nil {
			return nil
		}
		select {
		case <-pending:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// applyEnvironment makes the chat's agent use the environment the chat has
// chosen, before a task runs. It reports whether the task may go ahead, and any
// note the model should be given about a change.
//
// It never falls back to a different environment: if the chosen one cannot be
// started, the task does not run. Quietly running somewhere else, such as on
// this machine instead of the remote host the user picked, could do real harm.
// The one exception is a saved choice the config no longer permits, which goes
// back to the operator's default.
func (g *Engine) applyEnvironment(ctx context.Context, c *chat, a *agent.Agent) (ok bool, notice string) {
	e := g.Environments
	if e == nil {
		return true, ""
	}
	if g.waitForSwitch(ctx, c) != nil {
		return false, "" // /stop or shutdown
	}

	c.mu.Lock()
	chosen, applied, turns := c.envName, c.applied, c.turns
	c.mu.Unlock()

	name := chosen
	if name != "" && !slices.Contains(e.Names, name) {
		g.send(c.id, fmt.Sprintf("The %s environment is no longer available to chats, so this chat is back on %s.", name, e.Default))
		c.mu.Lock()
		c.envName = ""
		c.mu.Unlock()
		if g.Prefs != nil {
			g.Prefs.SetEnvironment(c.id, "")
		}
		name = ""
	}
	if name == "" {
		name = e.Default
	}
	if name == applied {
		return true, ""
	}

	env, err := e.Get(ctx, name)
	if err != nil {
		if ctx.Err() == nil {
			g.send(c.id, fmt.Sprintf("This chat is set to the %s environment, but it cannot be started now (%v), so I did not run your message.\n"+
				"Once that is fixed, send it again, or use %s default to go back to %s.", name, err, g.cmd("env"), e.Default))
		}
		return false, ""
	}

	before := g.describeEnv(applied)
	e.Apply(a, env)
	c.mu.Lock()
	c.applied = name
	c.mu.Unlock()

	if turns > 0 { // a conversation already under way needs telling; a fresh one starts out right
		notice = fmt.Sprintf("[Note for the assistant: the user moved your shell and file tools from %s to %s. "+
			"Files, directories and results from earlier in this conversation belong to the previous environment.]", before, env.Describe())
	}
	return true, notice
}
