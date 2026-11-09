package app

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"talaria/internal/agent"
	"talaria/internal/browser"
	"talaria/internal/calendar"
	"talaria/internal/choice"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/files"
	"talaria/internal/finance"
	"talaria/internal/goal"
	"talaria/internal/idea"
	"talaria/internal/mailbox"
	"talaria/internal/memory"
	"talaria/internal/notify"
	"talaria/internal/provider"
	"talaria/internal/skill"
	"talaria/internal/tool"
	"talaria/internal/webfetch"
	"talaria/internal/x"
)

// baseTools builds the registry of tools every conversation gets: the shell
// and file tools for the runtime's environment, skills, memory, and, for a
// chat, jobs and goals, plus whatever the connected services, the calendars
// and the mailbox offer. The watch tools come later, once the agent exists,
// because they call back into it.
func (rt *Runtime) baseTools(o AgentOptions, sessions *memory.Sessions, sessionID string) *tool.Registry {
	tools := tool.NewRegistry()
	tool.RegisterBuiltinsIn(tools, rt.env)
	skill.RegisterTools(tools, rt.skills)
	finance.RegisterTools(tools)
	webfetch.RegisterTools(tools)
	if o.ChatID != 0 && rt.cfg.Files != nil {
		files.RegisterTools(tools, files.Binding{Store: rt.cfg.Files, Retention: rt.cfg.Files.RetentionHours,
			Send: rt.fileSender(o.gatewayOrDefault()), ChatID: o.ChatID})
	}
	if o.ChatID != 0 && rt.cfg.Choices != nil {
		choice.RegisterTools(tools, choice.Binding{
			Store: rt.cfg.Choices, Gateway: o.gatewayOrDefault(), ChatID: o.ChatID,
		})
	}
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
	if o.ChatID != 0 && rt.cfg.Ideas != nil {
		gw, chat := o.gatewayOrDefault(), o.ChatID
		idea.RegisterTools(tools, idea.Binding{
			Store: rt.cfg.Ideas, Gateway: gw, ChatID: chat,
			Sent: func() []string {
				if sessions == nil || sessionID == "" {
					return nil
				}
				recent, err := sessions.Recent(sessionID, string(provider.RoleAssistant), 10)
				if err != nil {
					return nil
				}
				return recent
			},
			Done: func() []string {
				if rt.cfg.Goals == nil {
					return nil
				}
				goals, err := rt.cfg.Goals.List(gw, chat)
				if err != nil {
					return nil
				}
				var done []string
				for _, g := range goals {
					if g.Status == goal.Done || g.Status == goal.Dropped {
						done = append(done, g.Title)
					}
				}
				return done
			},
			Create: !o.NoCron,
		})
	}
	rt.mcp.Register(tools)
	if rt.cfg.Calendars != nil {
		calendar.RegisterTools(tools, rt.cfg.Calendars)
		var bind *calendar.ReminderBinder
		if !o.NoCron && o.ChatID != 0 {
			bind = &calendar.ReminderBinder{
				Add: func(e calendar.LocalEvent) (string, error) {
					at := e.Start.Add(-time.Duration(e.RemindMins) * time.Minute)
					j, err := rt.Cron.Add(cron.Job{
						Name:     "Reminder: " + e.Title,
						Schedule: "@at " + at.UTC().Format(time.RFC3339),
						Prompt:   fmt.Sprintf("Remind the user about %q at %s. Keep it to one short line.", e.Title, e.Start.Format("15:04")),
						Gateway:  o.Gateway, ChatID: o.ChatID,
					})
					if err != nil {
						return "", err
					}
					return j.ID, nil
				},
				Remove: func(jobID string) error { return rt.Cron.Remove(jobID, "", 0) },
			}
		}
		calendar.RegisterWriteTools(tools, rt.cfg.Calendars, bind)
	}
	if rt.cfg.Mail != nil {
		mailbox.RegisterTools(tools, rt.cfg.Mail)
		mailbox.RegisterThreadTools(tools, rt.cfg.Mail)
		var resolve mailbox.FileResolver
		var save mailbox.FileSaver
		if rt.cfg.Files != nil {
			resolve = func(id string) (string, []byte, error) {
				f, data, err := rt.cfg.Files.Open(id)
				if err != nil {
					return "", nil, err
				}
				return f.Name, data, nil
			}
			save = func(name string, data []byte) (string, error) {
				f, err := rt.cfg.Files.Save(name, data, "email attachment")
				if err != nil {
					return "", err
				}
				return "file " + f.ID + " (" + name + ")", nil
			}
		}
		mailbox.RegisterSendTools(tools, rt.cfg.Mail, resolve)
		mailbox.RegisterDownloadTool(tools, rt.cfg.Mail, save)
	}
	if rt.cfg.X != nil {
		x.RegisterTools(tools, rt.cfg.X)
	}
	if send := rt.notifier(); send != nil {
		notify.RegisterTool(tools, send, &rt.notifyLimit)
	}
	return tools
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
	opts.NoSandbox = env.Remote() || inOwnContainer() // the container or the far machine is the sandbox
	if n := rt.cfg.Browser.NoSandbox; n != nil {
		opts.NoSandbox = *n
	}
	m := &browser.Manager{Env: starter, Opts: opts, Social: rt.cfg.SocialSites}
	if rt.browsers == nil {
		rt.browsers = map[any]*browser.Manager{}
	}
	rt.browsers[key] = m
	rt.browserOrder = append(rt.browserOrder, m)
	return m
}

// Browsers lists the browsers this process has made, in the order they were
// first used, for the app's console: the user looking at, and working, the
// tabs the agent has open. Most runs have one, or none.
func (rt *Runtime) Browsers() []*browser.Manager {
	rt.bmu.Lock()
	defer rt.bmu.Unlock()
	return append([]*browser.Manager(nil), rt.browserOrder...)
}

// Env is where this runtime's shell and file tools run by default.
func (rt *Runtime) Env() execenv.Env { return rt.env }

// inOwnContainer reports whether Talaria itself runs inside its container
// image, where Chromium's sandbox cannot work (no privileges to take): the
// Dockerfile sets TALARIA_CONTAINER, and the no_sandbox setting still wins.
func inOwnContainer() bool { return os.Getenv("TALARIA_CONTAINER") == "1" }

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
