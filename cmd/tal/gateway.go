package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
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
	"talaria/internal/subagent"
	"talaria/internal/version"
	"talaria/internal/web"
)

// gatewayPool is the set of environments chats may use, shared by every chat
// service so that one container serves them all. Environments start when first
// asked for; the default one, which Talaria has already started, is in it from
// the beginning. Closing the pool stops what it started, and the default too.
func gatewayPool(cfg *config.Config, defName string, def execenv.Env, fence *execenv.Fence, getenv func(string) string, log *slog.Logger) *execenv.Pool {
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
		log.Info(fmt.Sprintf("environment %s: starting (%s)", name, spec.Summary()))
		env, err := execenv.Open(ctx, name, spec, getenv)
		if err != nil {
			return nil, err
		}
		log.Info(fmt.Sprintf("environment %s: ready: %s", name, env.Describe()))
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
	slices.Sort(names)
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
func serviceAllowlist(getenv func(string) string, envVar string, fromConfig []int64) (map[int64]bool, error) {
	allowed := map[int64]bool{}
	if v := getenv(envVar); v != "" {
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

func runGateway(c *cli) error {
	ctx := c.ctx
	tgToken, dcToken := c.cfg.TelegramToken(c.getenv), c.cfg.DiscordToken(c.getenv)
	webSet, webProblems, webOn := webConfig(c.getenv)
	if tgToken == "" && dcToken == "" && !webOn {
		return errors.New("set TALARIA_TELEGRAM_TOKEN to the token from @BotFather, TALARIA_DISCORD_TOKEN to a Discord bot's token, " +
			"or TALARIA_WEB_PASSWORD to serve the web app (or telegram.token_env / discord.token_env in the config file)")
	}
	if len(webProblems) > 0 {
		return errors.New(strings.Join(webProblems, "; "))
	}
	pause := newPauseState(c.home)
	// logf speaks the Logf dialect the gateways, the web server and the
	// schedulers take, reporting through the process logger.
	logf := func(format string, args ...any) { c.log.Info(fmt.Sprintf(format, args...)) }

	envDesc := ""
	if c.env != nil {
		envDesc = c.env.Describe()
	}
	var pool *execenv.Pool
	if len(c.cfg.Telegram.Environments) > 0 || len(c.cfg.Discord.Environments) > 0 {
		pool = gatewayPool(c.cfg, c.defEnv, c.env, buildFence(c.cfg, config.Path(c.home, c.getenv), c.home), c.getenv, c.log)
		defer pool.Close() // stops every environment the chats started, and the default one too
	}
	backends := gatewayBackends(c.cfg, c.res, c.p, c.getenv)
	c.rt.SetHelpers(helperModels(backends))

	var webHub *web.Hub // the web app's hub, once it is made: it is also what shows who is working

	// newAgent makes the function a service uses to build a chat's agent.
	newAgent := func(s *service, note string, selection func(int64) (string, string)) func(int64, func(string, string) bool) *agent.Agent {
		return func(chatID int64, approve func(name, args string) bool) *agent.Agent {
			if c.yolo {
				approve = func(string, string) bool { return true }
			}
			return c.rt.NewAgent(app.AgentOptions{
				Approve: approve, ExtraSystem: note + agencyNote(c.rt), ChatID: chatID, Gateway: s.name, Tasks: s.sched, Activity: s.sink,
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
		runner := &jobRunner{rt: c.rt, backends: backends, envs: s.envs, allowed: s.allowed, note: note, taskNote: taskText, goals: c.rt.Goals(), yolo: c.yolo, sink: s.sink}
		s.sched = &cron.Scheduler{
			Store:    c.rt.Cron,
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
				return tasksCommand(c.rt.Cron, s.sched, s.name, chatID, args)
			}},
		}
		if gs := c.rt.Goals(); gs != nil {
			cmds["goals"] = chat.Command{Help: "lists your goals (also: goals show ID, pause ID, resume ID, done ID, drop ID)", Run: func(chatID int64, args []string) string {
				return goalsCommand(gs, c.rt.Cron, s.name, chatID, args)
			}}
		}
		if c.rt.Watches() != nil {
			cmds["watches"] = chat.Command{Help: "lists what I am watching for you (also: watches pause ID, resume ID, delete ID)", Run: func(chatID int64, args []string) string {
				return watchesCommand(c.rt.WatchBinding(s.name, chatID, nil, nil, false), args)
			}}
		}
		onUser := func(chatID int64) {
			if ws := c.rt.Watches(); ws != nil {
				ws.ResetTriggers(s.name, chatID) // the user is here: watches get a fresh allowance of changes
			}
			if gs := c.rt.Goals(); gs != nil {
				gs.ResetCheckins(s.name, chatID) // the user is here: goals get a fresh allowance of check-ins
			}
		}
		stop := func(chatID int64) int {
			n, _ := cron.CancelTasks(c.rt.Cron, s.sched, s.name, chatID, "all")
			return n
		}
		return cmds, onUser, stop
	}

	var services []*service

	if tgToken != "" {
		allowed, err := serviceAllowlist(c.getenv, "TALARIA_TELEGRAM_ALLOWED_USERS", c.cfg.Telegram.AllowedUsers)
		if err != nil {
			return err
		}
		api := telegram.NewAPI(tgToken)
		if base := c.getenv("TALARIA_TELEGRAM_API_BASE"); base != "" { // self-hosted Bot API server or a test double
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
				me.Username, len(allowed), c.res.Name, c.res.Model, onOff(c.cfg.Telegram.StreamReplies()))
			sharedWarning(len(allowed), func(m string) { logf("telegram: %s", m) })
		}

		s := &service{name: "telegram", allowed: allowed}
		s.envs = gatewayEnvironments(c.cfg, c.cfg.Telegram.Environments, c.defEnv, pool, c.rt.UseEnvironment)
		g := &telegram.Gateway{
			API: api, Allowed: allowed, Logf: logf,
			MessagesPerMinute: c.cfg.Limits.Messages(), Stream: c.cfg.Telegram.StreamReplies(), Environment: envDesc, Environments: s.envs, Backends: backends,
			Prefs:      &chat.FilePrefs{Path: filepath.Join(c.home, "telegram_chats.json")},
			OffsetPath: filepath.Join(c.home, "telegram_offset.json"),
			OutboxPath: filepath.Join(c.home, "outbox_telegram.json"),
		}
		if !c.yolo {
			g.HeldPath, g.Destructive = filepath.Join(c.home, "held_telegram.json"), c.rt.AsksOnlyToDestroy()
		}
		if gd := c.rt.Guard(); gd != nil {
			g.Permissions = guard.ChatPermissions{G: gd, Gateway: s.name}
		}
		if len(allowed) > 0 {
			// Whatever the agent is working on, from the web app or a scheduled job too, it can message the user here.
			c.rt.SetNotifier(func(ctx context.Context, text string) error {
				for id := range allowed {
					g.Send(ctx, id, text)
				}
				return nil
			})
		}
		g.NewAgent = newAgent(s, telegramNote, g.Selection)
		s.run, s.send, s.selection = g.Run, g.Send, g.Selection
		g.Commands, g.OnUserMessage, g.StopBackground = attach(s, logf)
		services = append(services, s)
	}

	if dcToken != "" {
		allowed, err := serviceAllowlist(c.getenv, "TALARIA_DISCORD_ALLOWED_USERS", c.cfg.Discord.AllowedUsers)
		if err != nil {
			return err
		}
		api := discord.NewAPI(dcToken)
		if base := c.getenv("TALARIA_DISCORD_API_BASE"); base != "" { // a test double
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
				me.Username, len(allowed), c.res.Name, c.res.Model, onOff(c.cfg.Discord.StreamReplies()))
			sharedWarning(len(allowed), func(m string) { logf("discord: %s", m) })
		}

		s := &service{name: "discord", allowed: allowed, markdown: true}
		s.envs = gatewayEnvironments(c.cfg, c.cfg.Discord.Environments, c.defEnv, pool, c.rt.UseEnvironment)
		g := &discord.Gateway{
			API: api, Allowed: allowed, Logf: logf,
			MessagesPerMinute: c.cfg.Limits.Messages(), Stream: c.cfg.Discord.StreamReplies(), Environment: envDesc, Environments: s.envs, Backends: backends,
			Prefs:      &chat.FilePrefs{Path: filepath.Join(c.home, "discord_chats.json")},
			OutboxPath: filepath.Join(c.home, "outbox_discord.json"),
		}
		if !c.yolo {
			g.HeldPath, g.Destructive = filepath.Join(c.home, "held_discord.json"), c.rt.AsksOnlyToDestroy()
		}
		if gd := c.rt.Guard(); gd != nil {
			g.Permissions = guard.ChatPermissions{G: gd, Gateway: s.name}
		}
		g.NewAgent = newAgent(s, discordNote, g.Selection)
		s.run, s.send, s.selection = g.Run, g.Send, g.Selection
		g.Commands, g.OnUserMessage, g.StopBackground = attach(s, logf)
		services = append(services, s)
	}

	if webOn {
		allowed := map[int64]bool{web.UserID: true}
		webHub = &web.Hub{Path: filepath.Join(c.home, "web_log.json")}
		subject := webSet.PublicURL
		if subject == "" {
			subject = "mailto:admin@localhost.invalid"
		}
		push := &web.Pusher{Path: filepath.Join(c.home, "web_push.json"), Subject: subject, Logf: logf}
		webHub.Notifier = push
		s := &service{name: "web", allowed: allowed, markdown: true, sink: webHub}
		eng := &chat.Engine{
			T: webHub, Allowed: allowed, Logf: logf, Stream: true, MessagesPerMinute: c.cfg.Limits.Messages(),
			Environment: envDesc, Backends: backends, Prefs: &chat.FilePrefs{Path: filepath.Join(c.home, "web_chats.json")},
			Outbox: &chat.Outbox{Path: filepath.Join(c.home, "outbox_web.json")},
		}
		if gd := c.rt.Guard(); gd != nil {
			eng.Permissions = guard.ChatPermissions{G: gd, Gateway: s.name}
		}
		if !c.yolo {
			eng.Held, eng.Destructive = &chat.HeldStore{Path: filepath.Join(c.home, "held_web.json")}, c.rt.AsksOnlyToDestroy()
			webHub.Held = eng.Held.Has
		}
		eng.NewAgent = newAgent(s, webNote, eng.Selection)
		eng.Commands, eng.OnUserMessage, eng.StopBackground = attach(s, logf)
		zone, _ := calendarZone(c.cfg, c.getenv)
		model := c.res.Model
		srv := &web.Server{
			Hub: webHub, Auth: &web.Auth{Password: webSet.Password, Path: filepath.Join(c.home, "web_sessions.json"), Secure: webSet.Secure},
			Engine: eng, Push: push, Pause: pause, Telegram: c.rt.CanNotify(), Permissions: permissionsLabel(c.rt), PublicURL: webSet.PublicURL, Model: model, Version: version.String(), Started: time.Now(),
			Sources: webSources(c.rt, zone, webLinks(c.cfg, c.rt, model, c.getenv, webSet.PublicURL)), Logf: logf, TrustProxy: webSet.Secure,
			ConfigPath: config.Path(c.home, c.getenv),
			Cfg:        c.cfg,
			Mailbox:    c.rt.Mail(), Calendars: c.rt.Calendars(), X: c.rt.X(), Getenv: c.getenv,
		}
		if convos, err := web.OpenConvos(filepath.Join(c.home, "web_chats.json")); err != nil {
			logf("web: kept conversations did not open: %v", err)
		} else {
			srv.Convos = convos
			webHub.OnChange = srv.SaveActive
		}
		s.run = func(ctx context.Context) error {
			eng.Start(ctx)
			defer eng.Wait(5 * time.Second)
			srv.AdoptActiveConversation()
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

	if c.yolo {
		logf("WARNING: -yolo is on, so allowed users can run shell commands without approval.")
	}
	for _, s := range services {
		if s.envs != nil {
			logf("%s: chats may switch environments among: %s (default %s)", s.name, strings.Join(s.envs.Names, ", "), c.defEnv)
			if c.yolo && slices.Contains(s.envs.Names, execenv.LocalName) && c.defEnv != execenv.LocalName {
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
	opsAddr, opsToken, err := opsSettings(c.cfg, c.getenv)
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

// helperModels offers every profile that can be used right now to subagents, so the main agent can hand a plain
// lookup to a cheap model and a hard problem to a strong one. A profile that cannot be opened (no key) is left out.
func helperModels(b *chat.Backends) func() []subagent.Helper {
	return func() []subagent.Helper {
		var out []subagent.Helper
		names := b.Names
		if b.List != nil {
			names = b.List()
		}
		for _, name := range names {
			be, err := b.Open(name)
			if err != nil {
				continue
			}
			out = append(out, subagent.Helper{
				Name: name, Desc: be.Desc, Best: be.Best,
				Open: func() (provider.Provider, string, error) {
					be, err := b.Open(name)
					return be.Provider, be.Model, err
				},
			})
		}
		if len(out) < 2 {
			return nil // one model to choose from is no choice
		}
		return out
	}
}

// permissionsLabel names which calls ask first in this run, as the config spells it.
func permissionsLabel(rt *app.Runtime) string {
	if rt.AsksOnlyToDestroy() {
		return config.PermissionsDeletesAndPosts
	}
	return config.PermissionsStrict
}
