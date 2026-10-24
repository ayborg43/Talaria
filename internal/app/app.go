// Package app wires Talaria's pieces together so the CLI and the gateways
// build agents the same way.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"talaria/internal/activity"
	"talaria/internal/agent"
	"talaria/internal/browser"
	"talaria/internal/calendar"
	"talaria/internal/choice"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/goal"
	"talaria/internal/guard"
	"talaria/internal/idea"
	"talaria/internal/mailbox"
	"talaria/internal/mcp"
	"talaria/internal/memory"
	"talaria/internal/notify"
	"talaria/internal/provider"
	"talaria/internal/skill"
	"talaria/internal/subagent"
	"talaria/internal/tool"
	"talaria/internal/watch"
	"talaria/internal/x"
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
	// Ask is which calls ask the user first; the zero value is the strict way. SocialSites are extra sites the
	// browser treats as social media, where acting on a page counts as posting.
	Ask         agent.AskMode
	SocialSites []string
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
	// X lets the agent read the user's mentions and post as them. Nil turns the X tools off.
	X *x.Service
	// Watches keeps what chats have asked to be told about in connected services.
	// Nil turns watches off.
	Watches *watch.Store
	// Choices keeps structured questions the user answers by picking an
	// option. Nil turns the choice tools off.
	Choices *choice.Store
	// Ideas keeps suggestions with source evidence. Nil turns the idea tools off.
	Ideas *idea.Store
	// Log is where warnings go. Nil discards them.
	Log *slog.Logger
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
func HomeDir(getenv func(string) string) (string, error) {
	if h := getenv("TALARIA_HOME"); h != "" {
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
	log      *slog.Logger

	notifyMu    sync.Mutex
	notifySend  notify.Sender
	notifyLimit notify.Limit

	budget *budget

	bmu      sync.Mutex
	browsers map[any]*browser.Manager // one browser per environment, started when first used
}

// Open prepares shared state. History is optional: if the database cannot
// open, a warning is logged and agents run without it.
func Open(cfg Config) *Runtime {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	rt := &Runtime{
		Cron:     &cron.Store{Path: filepath.Join(cfg.Home, "cron", "jobs.json")},
		cfg:      cfg,
		provider: cfg.Provider,
		skills:   &skill.Store{Dir: filepath.Join(cfg.Home, "skills")},
		notes:    &memory.Notes{Dir: filepath.Join(cfg.Home, "memories")},
		mcp:      cfg.MCP,
		env:      cfg.Env,
		log:      log,
		budget:   &budget{max: cfg.ModelCallsPerHour},
	}
	if rt.env == nil {
		rt.env = execenv.Local{Fence: cfg.Fence}
	}
	if !cfg.Subagents.Disabled {
		rt.pool = subagent.NewPool(cfg.Subagents)
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		log.Warn("session history disabled", "err", err)
		return rt
	}
	os.Chmod(cfg.Home, 0o700) // history, notes and jobs are private; a home made by an older version may be open
	s, err := memory.OpenSessions(filepath.Join(cfg.Home, "sessions.db"))
	if err != nil {
		log.Warn("session history disabled", "err", err)
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

// X returns the X service, nil if no X account is set up.
func (rt *Runtime) X() *x.Service { return rt.cfg.X }

// HasX reports whether the agent can read and post to X.
func (rt *Runtime) HasX() bool { return rt.cfg.X != nil }

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

// SetNotifier lets agents message the user on Telegram, from wherever they were asked: the web app, a scheduled job.
// Conversations started before this do not have the tool until they are restarted.
func (rt *Runtime) SetNotifier(send notify.Sender) {
	rt.notifyMu.Lock()
	rt.notifySend = send
	rt.notifyMu.Unlock()
}

func (rt *Runtime) notifier() notify.Sender {
	rt.notifyMu.Lock()
	defer rt.notifyMu.Unlock()
	return rt.notifySend
}

// SetHelpers lets the main agent give subagents other models than its own, for tasks one suits better.
func (rt *Runtime) SetHelpers(list func() []subagent.Helper) {
	if rt.pool != nil {
		rt.pool.Helpers = list
	}
}

// CanNotify reports whether agents can message the user on Telegram.
func (rt *Runtime) CanNotify() bool { return rt.notifier() != nil }

// AsksOnlyToDestroy reports whether agents ask permission only for what deletes or posts, as opposed to the strict way.
func (rt *Runtime) AsksOnlyToDestroy() bool { return rt.cfg.Ask == agent.AskDestructive }

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
			rt.log.Warn("session history disabled for this conversation", "err", err)
			sessions = nil
		}
		sessionID = id
	}

	tools := rt.baseTools(o, sessions, sessionID)

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
		GuardUntrusted: !rt.cfg.TrustOutsideContent && rt.cfg.Ask != agent.AskDestructive,
		Ask:            rt.cfg.Ask,
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
