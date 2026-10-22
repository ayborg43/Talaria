package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"talaria/internal/activity"
	"talaria/internal/agent"
	"talaria/internal/app"
	"talaria/internal/config"
	"talaria/internal/cron"
	"talaria/internal/execenv"
	"talaria/internal/gateway/chat"
	"talaria/internal/gateway/discord"
	"talaria/internal/gateway/telegram"
	"talaria/internal/guard"
	"talaria/internal/metrics"
	"talaria/internal/ops"
	"talaria/internal/provider"
	"talaria/internal/version"
	"talaria/internal/web"
)

// gatewayPool is the set of environments chats may use, shared by every chat
// service so that one container serves them all. Environments start when first
// asked for; the default one, which Talaria has already started, is in it from
// the beginning. Closing the pool stops what it started, and the default too.
func gatewayPool(cfg *config.Config, defName string, def execenv.Env, fence *execenv.Fence, getenv func(string) string, logf func(string, ...any)) *execenv.Pool {
	if def == nil {
		def = execenv.Local{Fence: fence}
	}
	pool := execenv.NewPool(func(ctx context.Context, name string) (execenv.Env, error) {
		if name == execenv.LocalName {
			return execenv.Local{Fence: fence}, nil
		}
		spec, ok := cfg.Environments[name]
		if !ok {
			return nil, fmt.Errorf("no environment named %q", name)
		}
		logf("environment %s: starting (%s)", name, spec.Summary())
		env, err := execenv.Open(ctx, name, spec, getenv)
		if err != nil {
			return nil, err
		}
		logf("environment %s: ready: %s", name, env.Describe())
		return env, nil
	})
	pool.Put(defName, def)
	return pool
}

// gatewayEnvironments sets up /env for one chat service: the environments its
// chats may switch between are the ones the config offers it plus the one the
// gateway started in. It returns nil when none is offered, which leaves
// switching off.
func gatewayEnvironments(cfg *config.Config, offered []string, defName string, pool *execenv.Pool, apply func(*agent.Agent, execenv.Env)) *chat.Environments {
	if len(offered) == 0 {
		return nil
	}
	names := append([]string{defName}, offered...)
	sort.Strings(names)
	names = slices.Compact(names)

	return &chat.Environments{
		Default: defName,
		Names:   names,
		Summary: func(name string) string {
			if name == execenv.LocalName {
				return "this machine (built in)"
			}
			return cfg.Environments[name].Summary()
		},
		Running: pool.Running,
		Get:     pool.Get,
		Apply:   apply,
	}
}

// switchableBy says, for `tal env`, which chat services may switch to an environment.
func switchableBy(cfg *config.Config, name string) string {
	var who []string
	if slices.Contains(cfg.Telegram.Environments, name) {
		who = append(who, "Telegram")
	}
	if slices.Contains(cfg.Discord.Environments, name) {
		who = append(who, "Discord")
	}
	if len(who) == 0 {
		return ""
	}
	return "  (" + strings.Join(who, " and ") + " chats may switch to it)"
}

// service is one chat service the gateway runs: how to reach it, who may use it,
// and the scheduler that delivers its jobs.
type service struct {
	name      string // "telegram": what jobs record as their gateway
	allowed   map[int64]bool
	envs      *chat.Environments
	markdown  bool
	run       func(ctx context.Context) error
	send      func(ctx context.Context, chatID int64, text string)
	selection func(chatID int64) (profile, environment string)
	// sched runs this service's scheduled jobs, background tasks and goal check-ins.
	sched *cron.Scheduler
	// sink, if set, is told who is working for this service's chats, for a page that shows it.
	sink activity.Sink
}

// serviceAllowlist reads who may use a service: the environment variable if
// set, else the config file.
func serviceAllowlist(envVar string, fromConfig []int64) (map[int64]bool, error) {
	allowed := map[int64]bool{}
	if v := os.Getenv(envVar); v != "" {
		var err error
		if allowed, err = parseIDs(v); err != nil {
			return nil, fmt.Errorf("%s: %w", envVar, err)
		}
		return allowed, nil
	}
	for _, id := range fromConfig {
		allowed[id] = true
	}
	return allowed, nil
}

func orNotSet(s string) string {
	if s == "" {
		return "not set"
	}
	return s
}

func onOff(b bool) string { return map[bool]string{true: "on", false: "off"}[b] }

func runGateway(ctx context.Context, rt *app.Runtime, cfg *config.Config, res config.Resolved, defProvider provider.Provider, home string, env execenv.Env, envName string, yolo bool) error {
	tgToken, dcToken := cfg.TelegramToken(os.Getenv), cfg.DiscordToken(os.Getenv)
	webSet, webProblems, webOn := webConfig(os.Getenv)
	if tgToken == "" && dcToken == "" && !webOn {
		return errors.New("set TALARIA_TELEGRAM_TOKEN to the token from @BotFather, TALARIA_DISCORD_TOKEN to a Discord bot's token, " +
			"or TALARIA_WEB_PASSWORD to serve the web app (or telegram.token_env / discord.token_env in the config file)")
	}
	if len(webProblems) > 0 {
		return errors.New(strings.Join(webProblems, "; "))
	}
	pause := newPauseState(home)
	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

	envDesc := ""
	if env != nil {
		envDesc = env.Describe()
	}
	var pool *execenv.Pool
	if len(cfg.Telegram.Environments) > 0 || len(cfg.Discord.Environments) > 0 {
		pool = gatewayPool(cfg, envName, env, buildFence(cfg, config.Path(home), home), os.Getenv, logf)
		defer pool.Close() // stops every environment the chats started, and the default one too
	}
	backends := gatewayBackends(cfg, res, defProvider, os.Getenv)

	var webHub *web.Hub // the web app's hub, once it is made: it is also what shows who is working

	// newAgent makes the function a service uses to build a chat's agent.
	newAgent := func(s *service, note string, selection func(int64) (string, string)) func(int64, func(string, string) bool) *agent.Agent {
		return func(chatID int64, approve func(name, args string) bool) *agent.Agent {
			if yolo {
				approve = func(string, string) bool { return true }
			}
			return rt.NewAgent(app.AgentOptions{
				Approve: approve, ExtraSystem: note + agencyNote(rt), ChatID: chatID, Gateway: s.name, Tasks: s.sched, Activity: s.sink,
				// A job scheduled from this chat starts out with what the chat is using now.
				Jobs: gatewayJobTargets(backends, s.envs, func() (string, string) { return selection(chatID) }),
			})
		}
	}

	// attach gives a service its scheduler, and the commands and hooks that
	// connect its chats to background tasks and goals.
	attach := func(s *service, logf func(string, ...any)) (map[string]chat.Command, func(int64), func(int64) int) {
		note := cronNote
		taskText := taskNote
		if s.markdown {
			note, taskText = cronNoteMarkdown, taskNoteMarkdown
		}
		runner := &jobRunner{rt: rt, backends: backends, envs: s.envs, allowed: s.allowed, note: note, taskNote: taskText, goals: rt.Goals(), yolo: yolo, sink: s.sink}
		s.sched = &cron.Scheduler{
			Store:    rt.Cron,
			Gateway:  s.name,
			Exec:     runner.run,
			Paused:   pause.Paused,
			Activity: s.sink,
			Deliver: func(ctx context.Context, chatID int64, text string) {
				if s.allowed[chatID] {
					s.send(ctx, chatID, text)
				}
			},
			Logf: logf,
		}
		cmds := map[string]chat.Command{
			"tasks": {Help: "lists the background tasks running for this chat (cancel one with: tasks cancel ID, or all)", Run: func(chatID int64, args []string) string {
				return tasksCommand(rt.Cron, s.sched, s.name, chatID, args)
			}},
		}
		if gs := rt.Goals(); gs != nil {
			cmds["goals"] = chat.Command{Help: "lists your goals (also: goals show ID, pause ID, resume ID, done ID, drop ID)", Run: func(chatID int64, args []string) string {
				return goalsCommand(gs, rt.Cron, s.name, chatID, args)
			}}
		}
		if rt.Watches() != nil {
			cmds["watches"] = chat.Command{Help: "lists what I am watching for you (also: watches pause ID, resume ID, delete ID)", Run: func(chatID int64, args []string) string {
				return watchesCommand(rt.WatchBinding(s.name, chatID, nil, nil, false), args)
			}}
		}
		onUser := func(chatID int64) {
			if ws := rt.Watches(); ws != nil {
				ws.ResetTriggers(s.name, chatID) // the user is here: watches get a fresh allowance of changes
			}
			if gs := rt.Goals(); gs != nil {
				gs.ResetCheckins(s.name, chatID) // the user is here: goals get a fresh allowance of check-ins
			}
		}
		stop := func(chatID int64) int {
			n, _ := cron.CancelTasks(rt.Cron, s.sched, s.name, chatID, "all")
			return n
		}
		return cmds, onUser, stop
	}

	var services []*service

	if tgToken != "" {
		allowed, err := serviceAllowlist("TALARIA_TELEGRAM_ALLOWED_USERS", cfg.Telegram.AllowedUsers)
		if err != nil {
			return err
		}
		api := telegram.NewAPI(tgToken)
		if base := os.Getenv("TALARIA_TELEGRAM_API_BASE"); base != "" { // self-hosted Bot API server or a test double
			api.Base = strings.TrimRight(base, "/")
		}
		me, err := api.GetMe(ctx)
		if err != nil {
			return fmt.Errorf("telegram rejected the bot token: %w", err)
		}
		if len(allowed) == 0 {
			logf("telegram: setup mode: TALARIA_TELEGRAM_ALLOWED_USERS is empty, so the agent is disabled.")
			logf("telegram: message @%s with /whoami to get your user ID, then restart with it set.", me.Username)
		} else {
			logf("telegram: serving @%s for %d allowed user(s), default profile %s (%s), streaming %s",
				me.Username, len(allowed), res.Name, res.Model, onOff(cfg.Telegram.StreamReplies()))
			sharedWarning(len(allowed), func(m string) { logf("telegram: %s", m) })
		}

		s := &service{name: "telegram", allowed: allowed}
		s.envs = gatewayEnvironments(cfg, cfg.Telegram.Environments, envName, pool, rt.UseEnvironment)
		g := &telegram.Gateway{
			API: api, Allowed: allowed, Logf: logf,
			MessagesPerMinute: cfg.Limits.Messages(), Stream: cfg.Telegram.StreamReplies(), Environment: envDesc, Environments: s.envs, Backends: backends,
			Prefs: &chat.FilePrefs{Path: filepath.Join(home, "telegram_chats.json")},
		}
		if gd := rt.Guard(); gd != nil {
			g.Permissions = guard.ChatPermissions{G: gd, Gateway: s.name}
		}
		g.NewAgent = newAgent(s, telegramNote, g.Selection)
		s.run, s.send, s.selection = g.Run, g.Send, g.Selection
		g.Commands, g.OnUserMessage, g.StopBackground = attach(s, logf)
		services = append(services, s)
	}

	if dcToken != "" {
		allowed, err := serviceAllowlist("TALARIA_DISCORD_ALLOWED_USERS", cfg.Discord.AllowedUsers)
		if err != nil {
			return err
		}
		api := discord.NewAPI(dcToken)
		if base := os.Getenv("TALARIA_DISCORD_API_BASE"); base != "" { // a test double
			api.Base = strings.TrimRight(base, "/")
		}
		me, err := api.GetMe(ctx)
		if err != nil {
			return fmt.Errorf("discord rejected the bot token: %w", err)
		}
		if len(allowed) == 0 {
			logf("discord: setup mode: TALARIA_DISCORD_ALLOWED_USERS is empty, so the agent is disabled.")
			logf("discord: send %s a direct message saying !whoami to get your user ID, then restart with it set.", me.Username)
		} else {
			logf("discord: serving %s for %d allowed user(s), default profile %s (%s), streaming %s",
				me.Username, len(allowed), res.Name, res.Model, onOff(cfg.Discord.StreamReplies()))
			sharedWarning(len(allowed), func(m string) { logf("discord: %s", m) })
		}

		s := &service{name: "discord", allowed: allowed, markdown: true}
		s.envs = gatewayEnvironments(cfg, cfg.Discord.Environments, envName, pool, rt.UseEnvironment)
		g := &discord.Gateway{
			API: api, Allowed: allowed, Logf: logf,
			MessagesPerMinute: cfg.Limits.Messages(), Stream: cfg.Discord.StreamReplies(), Environment: envDesc, Environments: s.envs, Backends: backends,
			Prefs: &chat.FilePrefs{Path: filepath.Join(home, "discord_chats.json")},
		}
		if gd := rt.Guard(); gd != nil {
			g.Permissions = guard.ChatPermissions{G: gd, Gateway: s.name}
		}
		g.NewAgent = newAgent(s, discordNote, g.Selection)
		s.run, s.send, s.selection = g.Run, g.Send, g.Selection
		g.Commands, g.OnUserMessage, g.StopBackground = attach(s, logf)
		services = append(services, s)
	}

	if webOn {
		allowed := map[int64]bool{web.UserID: true}
		webHub = &web.Hub{Path: filepath.Join(home, "web_log.json")}
		subject := webSet.PublicURL
		if subject == "" {
			subject = "mailto:admin@localhost.invalid"
		}
		push := &web.Pusher{Path: filepath.Join(home, "web_push.json"), Subject: subject, Logf: logf}
		webHub.Notifier = push
		s := &service{name: "web", allowed: allowed, markdown: true, sink: webHub}
		eng := &chat.Engine{
			T: webHub, Allowed: allowed, Logf: logf, Stream: true, MessagesPerMinute: cfg.Limits.Messages(),
			Environment: envDesc, Backends: backends, Prefs: &chat.FilePrefs{Path: filepath.Join(home, "web_chats.json")},
		}
		if gd := rt.Guard(); gd != nil {
			eng.Permissions = guard.ChatPermissions{G: gd, Gateway: s.name}
		}
		eng.NewAgent = newAgent(s, webNote, eng.Selection)
		eng.Commands, eng.OnUserMessage, eng.StopBackground = attach(s, logf)
		zone, _ := calendarZone(cfg, os.Getenv)
		model := res.Model
		srv := &web.Server{
			Hub: webHub, Auth: &web.Auth{Password: webSet.Password, Path: filepath.Join(home, "web_sessions.json"), Secure: webSet.Secure},
			Engine: eng, Push: push, Pause: pause, PublicURL: webSet.PublicURL, Model: model, Version: version.String(), Started: time.Now(),
			Sources: webSources(rt, zone, webLinks(cfg, rt, model, os.Getenv, webSet.PublicURL)), Logf: logf, TrustProxy: webSet.Secure,
			ConfigPath: config.Path(home),
			Cfg:        cfg,
		}
		s.run = func(ctx context.Context) error {
			eng.Start(ctx)
			defer eng.Wait(5 * time.Second)
			return srv.Serve(ctx, webSet.Listen)
		}
		s.send = eng.Send
		s.selection = eng.Selection
		logf("web: the app is on %s (public address %s), signing in with TALARIA_WEB_PASSWORD", webSet.Listen, orNotSet(webSet.PublicURL))
		if webSet.PublicURL == "" {
			logf("web: TALARIA_WEB_PUBLIC_URL is not set: phones need an https address to install the app and get notifications")
		}
		services = append(services, s)
	}
	if webHub != nil {
		defer webHub.Close()
	}

	if yolo {
		logf("WARNING: -yolo is on, so allowed users can run shell commands without approval.")
	}
	for _, s := range services {
		if s.envs != nil {
			logf("%s: chats may switch environments among: %s (default %s)", s.name, strings.Join(s.envs.Names, ", "), envName)
			if yolo && slices.Contains(s.envs.Names, execenv.LocalName) && envName != execenv.LocalName {
				logf("WARNING: -yolo is on and %s chats may switch to %q, which runs commands on this machine with no approval", s.name, execenv.LocalName)
			}
		}
	}

	// Run every service, and the scheduler for each, until the first of them
	// fails for good (a rejected token) or the gateway is stopped.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	fail := func(name string, err error) {
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", name, err)
		}
		mu.Unlock()
		cancel()
	}
	metrics.Set("talaria_build_info", 1, "version", version.String())
	opsAddr, opsToken, err := opsSettings(cfg, os.Getenv)
	if err != nil {
		return err
	}
	if opsAddr != "" {
		var names []string
		for _, s := range services {
			names = append(names, s.name)
		}
		srv := &ops.Server{Addr: opsAddr, Token: opsToken,
			Status: func() map[string]any { return map[string]any{"gateways": names} }}
		if err := srv.Validate(); err != nil {
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			fail("ops", srv.Run(ctx, logf))
		}()
	}
	for _, s := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fail(s.name, s.run(ctx))
		}()

		// Scheduled jobs run only while the gateway does, and only with an allowlist.
		if len(s.allowed) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.sched.Run(ctx)
		}()
	}
	wg.Wait()
	return firstErr
}

// sharedWarning says what is shared when more than one person is allowed in. Talaria is one
// person's agent: chats are kept apart, but what the agent knows and holds is not.
func sharedWarning(allowed int, say func(string)) {
	if allowed > 1 {
		say(sharedNote)
	}
}

const sharedNote = "warning: more than one person is allowed in. Their conversations and history are kept apart, but everything else is shared: " +
	"the agent's notes and skills, the saved logins and connected accounts (anyone allowed in can have the agent use them), the files and the " +
	"workspace, and the model account's spending. Only allow people you would give all of that to."
