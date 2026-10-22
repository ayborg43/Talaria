// Package app wires Talaria's pieces together so the CLI and the gateways
// build agents the same way.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"talaria/internal/activity"
	"talaria/internal/agent"
	"talaria/internal/browser"
	"talaria/internal/calendar"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/goal"
	"talaria/internal/guard"
	"talaria/internal/mailbox"
	"talaria/internal/mcp"
	"talaria/internal/memory"
	"talaria/internal/provider"
	"talaria/internal/skill"
	"talaria/internal/subagent"
	"talaria/internal/tool"
	"talaria/internal/watch"
)

const SystemPrompt = `You are Talaria, a fast and lightweight personal agent running on the user's machine.
Use the available tools to get things done, keep answers short, and say plainly when something failed.

You learn over time:
- After solving a non-trivial task, or when a skill you followed was wrong, save or fix a skill with skill_save.
- Record durable facts about the user and the environment with the memory tool. Keep entries terse; do not store one-off details.
- When the user refers to earlier work you cannot see, use session_search before asking them.`

type Config struct {
	Home     string // state directory: skills, notes, history
	Provider provider.Provider
	Model    string
	MCP      *mcp.Manager // connected MCP servers; may be nil
	// Env is where the shell and file tools run; nil means this machine.
	Env execenv.Env
	// Subagents bounds the delegate tool; the zero value gives the defaults.
	Subagents subagent.Limits
	// Guard holds the permission rules, the user's grants, the activity log and
	// the secrets to scrub. Nil leaves tools as they were: those that need
	// approval ask, and nothing is logged or scrubbed.
	Guard *guard.Guard
	// Browser says how the browser tools are offered; the zero value is "when
	// the environment is the workspace".
	Browser BrowserConfig
	// Vault holds saved logins the browser can fill in without the model seeing
	// them. Nil means there is none.
	Vault browser.Credentials
	// ModelCallsPerHour limits requests to the model, across everything this process runs; 0 is no limit.
	ModelCallsPerHour int
	// TrustOutsideContent turns off the guard that makes anything able to send something
	// out ask first once the conversation has read outside content.
	TrustOutsideContent bool
	// Fence keeps the file tools out of protected places on this machine; see execenv.Fence.
	Fence *execenv.Fence
	// Goals keeps the objectives chats have given the agent. Nil turns goals off.
	Goals *goal.Store
	// Calendars lets the agent read the user's calendars. Nil turns the calendar tool off.
	Calendars *calendar.Service
	// Mail lets the agent read the user's mailbox, and only read it. Nil turns the mail tools off.
	Mail *mailbox.Service
	// Watches keeps what chats have asked to be told about in connected services.
	// Nil turns watches off.
	Watches *watch.Store
}

// BrowserConfig is how the browser tools are set up.
type BrowserConfig struct {
	// Enabled turns the tools on or off; nil means on in the workspace only.
	Enabled *bool
	// Options are the browser's settings. Empty ProfileDir and unset NoSandbox
	// are filled in from the environment.
	Options browser.Options
	// NoSandbox, if set, overrides Options.NoSandbox.
	NoSandbox *bool
}

// HomeDir is where Talaria keeps its state: $TALARIA_HOME or ~/.talaria.
func HomeDir() (string, error) {
	if h := os.Getenv("TALARIA_HOME"); h != "" {
		return h, nil
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(u, ".talaria"), nil
}

// Runtime holds what every agent shares. It is safe for concurrent NewAgent calls.
type Runtime struct {
	Cron *cron.Store // scheduled jobs

	cfg      Config
	provider provider.Provider
	skills   *skill.Store
	notes    *memory.Notes
	sessions *memory.Sessions // nil when history is unavailable
	mcp      *mcp.Manager
	env      execenv.Env
	pool     *subagent.Pool // nil when subagents are disabled
	warn     func(format string, args ...any)

	budget *budget

	bmu      sync.Mutex
	browsers map[any]*browser.Manager // one browser per environment, started when first used
}

// Open prepares shared state. History is optional: if the database cannot
// open, warn is called and agents run without it.
func Open(cfg Config, warn func(format string, args ...any)) *Runtime {
	rt := &Runtime{
		Cron:     &cron.Store{Path: filepath.Join(cfg.Home, "cron", "jobs.json")},
		cfg:      cfg,
		provider: cfg.Provider,
		skills:   &skill.Store{Dir: filepath.Join(cfg.Home, "skills")},
		notes:    &memory.Notes{Dir: filepath.Join(cfg.Home, "memories")},
		mcp:      cfg.MCP,
		env:      cfg.Env,
		warn:     warn,
		budget:   &budget{max: cfg.ModelCallsPerHour},
	}
	if rt.env == nil {
		rt.env = execenv.Local{Fence: cfg.Fence}
	}
	if !cfg.Subagents.Disabled {
		rt.pool = subagent.NewPool(cfg.Subagents)
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		warn("session history disabled: %v", err)
		return rt
	}
	os.Chmod(cfg.Home, 0o700) // history, notes and jobs are private; a home made by an older version may be open
	s, err := memory.OpenSessions(filepath.Join(cfg.Home, "sessions.db"))
	if err != nil {
		warn("session history disabled: %v", err)
		return rt
	}
	rt.sessions = s
	return rt
}

// HasVault reports whether saved logins are available.
func (rt *Runtime) HasVault() bool { return rt.cfg.Vault != nil }

// Goals returns the goal store, nil if goals are off.
func (rt *Runtime) Goals() *goal.Store { return rt.cfg.Goals }

// HasCalendars reports whether the agent can read calendars.
func (rt *Runtime) HasCalendars() bool { return rt.cfg.Calendars != nil }

// Calendars returns the calendar service, nil if no calendar is set up.
func (rt *Runtime) Calendars() *calendar.Service { return rt.cfg.Calendars }

// Mail returns the mailbox service, nil if no mailbox is set up.
func (rt *Runtime) Mail() *mailbox.Service { return rt.cfg.Mail }

// HasMail reports whether the agent can read a mailbox.
func (rt *Runtime) HasMail() bool { return rt.cfg.Mail != nil }

// Watches returns the watch store, nil if watches are off.
func (rt *Runtime) Watches() *watch.Store { return rt.cfg.Watches }

// WatchBinding is what the watch tools and the /watches command work on for a chat.
func (rt *Runtime) WatchBinding(gateway string, chatID int64, tools *tool.Registry, call func(context.Context, string, json.RawMessage) (string, error), create bool) watch.Binding {
	if gateway == "" {
		gateway = cron.DefaultGateway
	}
	return watch.Binding{Store: rt.cfg.Watches, Sched: rt.Cron, Gateway: gateway, ChatID: chatID, Tools: tools, Call: call, Create: create}
}

// Guard returns the permission layer, nil if the runtime has none.
func (rt *Runtime) Guard() *guard.Guard { return rt.cfg.Guard }

func (rt *Runtime) Close() {
	rt.bmu.Lock()
	for _, m := range rt.browsers {
		m.Close()
	}
	rt.browsers = nil
	rt.bmu.Unlock()
	if rt.sessions != nil {
		rt.sessions.Close()
	}
}

// AgentOptions customizes one conversation.
type AgentOptions struct {
	// Approve is asked before tools that need approval run. Nil denies them.
	Approve func(name, args string) bool
	// ExtraSystem is appended to the system prompt (e.g. notes about the channel).
	ExtraSystem string
	// ChatID, when non-zero, gives the agent cron tools whose jobs deliver to that
	// chat, on the gateway named by Gateway ("" is Telegram).
	ChatID  int64
	Gateway string
	// Jobs says what profiles and environments a job scheduled from this chat may name.
	Jobs cron.Targets
	// Origin says what the conversation is for the activity log: "cli", "chat" or
	// "job:<id>". Left empty it is "chat" when there is a ChatID and "cli" if not.
	Origin string
	// NoCron keeps the tools that start more work out (cron_add, task_start,
	// goal_create) even though there is a ChatID: a scheduled job has the chat it
	// reports to, but must not schedule more jobs.
	NoCron bool
	// NoHistory keeps the conversation out of the saved history. A watch polling
	// a service every few minutes would otherwise fill it with empty sessions.
	NoHistory bool
	// Tasks runs and reports on this chat's background tasks. Nil means the chat
	// has no task tools.
	Tasks cron.Control
	// Activity, if set, is told when this agent hands work to a subagent, and what each does.
	Activity activity.Sink
}

// owner names whose conversation this is, for the history: one chat on one service. The command line is
// the person who runs Talaria and has no owner, so it sees everything.
func (o AgentOptions) owner() string {
	if o.ChatID == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", o.gatewayOrDefault(), o.ChatID)
}

func (o AgentOptions) origin() string {
	switch {
	case o.Origin != "":
		return o.Origin
	case o.ChatID != 0:
		return "chat"
	}
	return "cli"
}

func (o AgentOptions) gatewayOrDefault() string {
	if o.Gateway != "" {
		return o.Gateway
	}
	return cron.DefaultGateway
}

// gateway names the chat service for the log and for grants; "" for none.
func (o AgentOptions) gateway() string {
	switch {
	case o.Gateway != "":
		return o.Gateway
	case o.ChatID != 0:
		return cron.DefaultGateway
	}
	return ""
}

// NewAgent starts a fresh conversation with its own history session. Notes and
// the skill index are read now, so the prompt stays stable for the conversation.
func (rt *Runtime) NewAgent(o AgentOptions) *agent.Agent {
	sessions, sessionID := rt.sessions, ""
	if o.NoHistory {
		sessions = nil
	}
	if sessions != nil {
		id, err := sessions.StartFor(o.owner())
		if err != nil {
			rt.warn("session history disabled for this conversation: %v", err)
			sessions = nil
		}
		sessionID = id
	}

	tools := tool.NewRegistry()
	tool.RegisterBuiltinsIn(tools, rt.env)
	skill.RegisterTools(tools, rt.skills)
	memory.RegisterTools(tools, rt.notes, sessions, sessionID, o.owner())
	if o.ChatID != 0 && !o.NoCron {
		cron.RegisterTools(tools, rt.Cron, o.Gateway, o.ChatID, o.Jobs)
		if o.Tasks != nil {
			cron.RegisterTaskTools(tools, rt.Cron, o.Tasks, o.Gateway, o.ChatID, o.Jobs)
		}
	}
	if o.ChatID != 0 && rt.cfg.Goals != nil {
		goal.RegisterTools(tools, goal.Binding{
			Store: rt.cfg.Goals, Sched: rt.Cron, Gateway: o.gatewayOrDefault(), ChatID: o.ChatID, Targets: o.Jobs,
			Create: !o.NoCron,
		})
	}
	rt.mcp.Register(tools)
	if rt.cfg.Calendars != nil {
		calendar.RegisterTools(tools, rt.cfg.Calendars)
	}
	if rt.cfg.Mail != nil {
		mailbox.RegisterTools(tools, rt.cfg.Mail)
	}

	system := SystemPrompt
	if o.ExtraSystem != "" {
		system += "\n\n" + o.ExtraSystem
	}
	system += fmt.Sprintf("\n\n%s%s", rt.notes.Prompt(), rt.skills.Index())

	ag := &agent.Agent{
		Provider:       rt.provider,
		Model:          rt.cfg.Model,
		Tools:          tools,
		System:         system,
		Approve:        o.Approve,
		GuardUntrusted: !rt.cfg.TrustOutsideContent,
		BeforeModel:    rt.budget.take,
		Activity:       o.Activity,
	}
	if o.ChatID != 0 && rt.cfg.Watches != nil && rt.mcp.HasTools() {
		watch.RegisterTools(tools, rt.WatchBinding(o.Gateway, o.ChatID, tools, ag.Call, !o.NoCron))
	}
	scrub := func(s string) string { return s }
	if g := rt.cfg.Guard; g != nil {
		scope := g.For(o.origin(), o.gateway(), o.ChatID)
		ag.Gate, scrub = scope, scope.Scrub
	}
	ag.OnMessage = func(m provider.Message) {
		if sessions != nil && (m.Role == provider.RoleUser || m.Role == provider.RoleAssistant) {
			sessions.Add(sessionID, string(m.Role), scrub(m.Content)) // history is searchable and long-lived: no secrets in it
		}
	}
	rt.follow(ag, rt.env) // after the agent exists: subagents follow its model, approval and environment
	rt.offerBrowser(ag, rt.env)
	return ag
}

// UseEnvironment moves a live agent's shell and file tools to env. The
// conversation carries on; what changes is where the tools act, what the tools
// and the system prompt say about that, and where subagents work. Call it
// between tasks, never while the agent is running.
func (rt *Runtime) UseEnvironment(ag *agent.Agent, env execenv.Env) {
	tool.RegisterBuiltinsIn(ag.Tools, env) // replaces the four tools in place, with descriptions for env
	rt.follow(ag, env)
	rt.offerBrowser(ag, env)
}

// offerBrowser gives the agent the browser tools for env, or takes them away
// if env has no browser to offer. Its tab is its own, named after the agent.
func (rt *Runtime) offerBrowser(ag *agent.Agent, env execenv.Env) {
	ag.Tools.RemovePrefix(browser.Prefix)
	ag.Tools.RemovePrefix("vault_")
	mgr := rt.browserFor(env)
	if mgr == nil {
		return
	}
	owner := fmt.Sprintf("agent-%p", ag)
	browser.Register(ag.Tools, mgr, owner)
	if rt.cfg.Vault != nil {
		browser.RegisterVault(ag.Tools, mgr, owner, rt.cfg.Vault)
	}
}

// browserFor returns the browser manager for env, creating it on first use, or
// nil when the browser is not offered there.
func (rt *Runtime) browserFor(env execenv.Env) *browser.Manager {
	starter, ok := env.(execenv.Starter)
	if !ok {
		return nil
	}
	inWorkspace := false
	if c, ok := env.(interface{ ContainerName() string }); ok {
		inWorkspace = c.ContainerName() == execenv.WorkspaceContainer
	}
	on := inWorkspace
	if e := rt.cfg.Browser.Enabled; e != nil {
		on = *e
	}
	if !on {
		return nil
	}

	rt.bmu.Lock()
	defer rt.bmu.Unlock()
	key := envKey(env)
	if m := rt.browsers[key]; m != nil {
		return m
	}
	opts := rt.cfg.Browser.Options
	switch {
	case opts.ProfileDir != "":
	case inWorkspace:
		opts.ProfileDir = execenv.WorkspaceDir + "/.browser-profile"
	case !env.Remote():
		opts.ProfileDir = filepath.Join(rt.cfg.Home, "browser")
	}
	opts.NoSandbox = env.Remote() // the container or the far machine is the sandbox
	if n := rt.cfg.Browser.NoSandbox; n != nil {
		opts.NoSandbox = *n
	}
	m := &browser.Manager{Env: starter, Opts: opts}
	if rt.browsers == nil {
		rt.browsers = map[any]*browser.Manager{}
	}
	rt.browsers[key] = m
	return m
}

// envKey identifies an environment: the value itself where it can be compared
// (a pointer to a running container, say), else its description.
func envKey(env execenv.Env) any {
	if reflect.TypeOf(env).Comparable() {
		return env
	}
	return fmt.Sprintf("%T %s", env, env.Describe())
}

// follow makes the parts of an agent that mention its environment say env. The
// note goes in through Context rather than the stored system prompt, so
// switching later cannot leave an out-of-date statement at the top of the
// conversation.
func (rt *Runtime) follow(ag *agent.Agent, env execenv.Env) {
	note := environmentNote(env)
	ag.Context = func() string { return note }
	if rt.pool != nil {
		rt.pool.Register(ag, func() string { return joinNonEmpty(note, rt.notes.Prompt()+rt.skills.Index()) })
	}
}

// environmentNote tells the model where its shell and file tools run, when that
// is not the machine the user is on, so it does not mistake one for the other.
func environmentNote(env execenv.Env) string {
	if !env.Remote() {
		return ""
	}
	return "Your shell and file tools run in " + env.Describe() + ".\n" +
		"That is not the user's computer: commands, paths and files there are separate from the machine the user is using, and a relative path starts in that environment's working directory."
}

func joinNonEmpty(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}
