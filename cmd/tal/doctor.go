package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"talaria/internal/calendar"
	"talaria/internal/config"
	"talaria/internal/ops"
	"talaria/internal/version"
)

// runDoctor is `tal doctor`: it checks the setup for the mistakes that matter and says
// how to fix each. It never prints a secret, and changes nothing. The result is the number
// of problems found, so a script can act on it.
func runDoctor(cfg *config.Config, cfgPath, home string, getenv func(string) string, out io.Writer) int {
	problems := 0
	ok := func(f string, a ...any) { fmt.Fprintf(out, "  ok    "+f+"\n", a...) }
	note := func(f string, a ...any) { fmt.Fprintf(out, "  note  "+f+"\n", a...) }
	bad := func(f string, a ...any) { problems++; fmt.Fprintf(out, "  FIX   "+f+"\n", a...) }

	fmt.Fprintf(out, "Talaria %s\n\nFiles\n", version.String())
	if fi, err := os.Stat(home); err != nil {
		note("%s does not exist yet: it is made the first time Talaria runs", home)
	} else if fi.Mode().Perm()&0o077 != 0 {
		bad("%s is open to other users (%v): run  chmod 700 %s", home, fi.Mode().Perm(), home)
	} else {
		ok("%s is private", home)
	}
	for _, name := range []string{"config.json", "vault.enc", "vault.key", "sessions.db", "grants.json", "audit.log", "goals.json", "watches.json", "MEMORY.md", "USER.md"} {
		p := filepath.Join(home, name)
		if name == "config.json" {
			p = cfgPath
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.Mode().Perm()&0o077 != 0 {
			bad("%s is readable by other users (%v): run  chmod 600 %s", p, fi.Mode().Perm(), p)
		}
	}
	if cfg.Vault.KeyFile != "" {
		p := expandHome(cfg.Vault.KeyFile)
		if fi, err := os.Stat(p); err != nil {
			bad("vault.key_file %s cannot be read: %v", p, err)
		} else if fi.Mode().Perm()&0o077 != 0 {
			bad("the vault's key file %s is readable by other users: run  chmod 600 %s", p, p)
		}
		if strings.HasPrefix(filepath.Clean(p), filepath.Clean(home)+string(filepath.Separator)) {
			note("the vault's key file is inside %s, next to the vault it opens: a backup of that folder holds both. `tal backup` leaves it out; keep it elsewhere if you can", home)
		}
	}
	for _, w := range cfg.Warnings {
		bad("%s", w)
	}

	fmt.Fprintln(out, "\nModel")
	// The same settings a real run would use: the environment can pick the provider and model.
	r, err := cfg.Resolve(config.Overrides{Profile: getenv("TALARIA_PROFILE"), Model: getenv("TALARIA_MODEL"),
		BaseURL: getenv("TALARIA_BASE_URL"), APIKey: getenv("TALARIA_API_KEY"), Type: getenv("TALARIA_PROVIDER")}, getenv)
	switch {
	case err != nil:
		bad("%v", err)
	default:
		if err := r.CheckKey(); err != nil {
			bad("%v", err)
		} else {
			key := "no key needed"
			if r.APIKey != "" {
				key = "key from " + r.KeySource
			}
			ok("profile %s: %s %s, %s", r.Name, r.Type, r.Model, key)
		}
	}

	fmt.Fprintln(out, "\nChat services")
	services := 0
	if cfg.TelegramToken(getenv) != "" {
		services++
		checkAllowed(out, "Telegram", len(telegramAllowed(cfg, getenv)), ok, note, bad)
	}
	if cfg.DiscordToken(getenv) != "" {
		services++
		checkAllowed(out, "Discord", len(discordAllowed(cfg, getenv)), ok, note, bad)
	}
	if cfg.XToken(getenv) != "" {
		ok("X is signed in, so the agent can read mentions and post (asking first)")
	}
	if services == 0 {
		note("no bot token is set, so `tal gateway` has nothing to serve (the command line works without one)")
	}

	localEvents := 0
	if events, err := (&calendar.Store{Path: filepath.Join(home, "calendar.json")}).List(); err == nil {
		localEvents = len(events)
	}
	if sources, problems := calendarSources(cfg, getenv); len(sources) > 0 || len(problems) > 0 || localEvents > 0 {
		fmt.Fprintln(out, "\nCalendars")
		for _, pr := range problems {
			bad("%s", pr)
		}
		if localEvents > 0 {
			ok("%d event(s) on Talaria's own calendar", localEvents)
		}
		if len(sources) > 0 {
			names := make([]string, 0, len(sources))
			for _, src := range sources {
				names = append(names, src.Name)
			}
			ok("%d calendar(s), read-only: %s", len(sources), strings.Join(names, ", "))
			if cfg.Timezone == "" && getenv("TALARIA_TIMEZONE") == "" {
				note("no timezone is set, so calendar times are shown in UTC: set TALARIA_TIMEZONE (such as Africa/Lagos)")
			}
		}
		if _, zp := calendarZone(cfg, getenv); len(zp) > 0 {
			bad("%s", zp[0])
		}
	}

	if m, problems, configured := mailConfig(cfg.Email, getenv); configured {
		fmt.Fprintln(out, "\nEmail")
		for _, pr := range problems {
			bad("%s", pr)
		}
		if len(problems) == 0 {
			ok("mailbox %s, read-only, over an encrypted connection to %s", m.Address, m.Server)
			if m.microsoft() {
				note("Microsoft has mostly turned off app-password sign-in for IMAP: if sign-in is refused, this account may need OAuth, which Talaria does not do yet")
			}
		}
	}

	if w, problems, configured := webConfig(getenv); configured {
		fmt.Fprintln(out, "\nWeb app")
		for _, pr := range problems {
			bad("%s", pr)
		}
		if len(problems) == 0 {
			ok("served on %s, signing in with a password of %d characters", w.Listen, len([]rune(w.Password)))
			if w.PublicURL == "" {
				note("TALARIA_WEB_PUBLIC_URL is not set: phones need an https address to install the app and to get notifications")
			} else {
				ok("public address %s", w.PublicURL)
			}
		}
	}

	fmt.Fprintln(out, "\nSafety")
	if cfg.Policy.GuardUntrusted() {
		ok("after reading outside content, anything that could send something out asks first")
	} else {
		bad("policy.untrusted_content is off: an injected instruction in a web page or mail can get a command or a send through without a question")
	}
	if cfg.Limits.Messages() == 0 || cfg.Limits.ModelCalls() == 0 {
		note("a limit on messages or model requests is switched off")
	} else {
		ok("limits: %d messages a minute per chat, %d model requests an hour", cfg.Limits.Messages(), cfg.Limits.ModelCalls())
	}
	trusted := 0
	for _, s := range cfg.MCPServers {
		if s.Trusted {
			trusted++
		}
	}
	if trusted > 0 {
		bad("%d connected service(s) are marked trusted, so none of their tools ask, sends included. Prefer \"permissions\": \"reads\" or \"reads_and_drafts\"", trusted)
	}
	for name, p := range cfg.Policy.Rules {
		if strings.EqualFold(p, "allow") && (name == "shell" || name == "write_file" || name == "*") {
			bad("policy rule %s = allow: that runs without asking even after outside content was read", name)
		}
	}

	if listen, token, err := opsSettings(cfg, getenv); err != nil {
		fmt.Fprintln(out, "\nMonitoring")
		bad("%v", err)
	} else if listen != "" {
		fmt.Fprintln(out, "\nMonitoring")
		if err := (&ops.Server{Addr: listen, Token: token}).Validate(); err != nil {
			bad("%v", err)
		} else {
			ok("health and metrics on %s", listen)
		}
	}

	fmt.Fprintln(out, "\nTools")
	for _, name := range usedEnvironments(cfg, getenv) {
		if spec, ok := cfg.Environments[name]; ok && spec.Type == "docker" {
			if _, err := exec.LookPath("docker"); err != nil {
				bad("environment %s needs docker, which is not installed or not on the PATH", name)
			}
		}
	}
	if _, err := exec.LookPath("docker"); err == nil {
		ok("docker is available")
	} else {
		note("docker is not installed: the workspace and docker environments cannot run")
	}

	if problems == 0 {
		fmt.Fprintln(out, "\nNothing to fix.")
	} else {
		fmt.Fprintf(out, "\n%d thing(s) to fix.\n", problems)
	}
	return problems
}

func checkAllowed(out io.Writer, service string, n int, ok, note func(string, ...any), bad func(string, ...any)) {
	switch {
	case n == 0:
		note("%s: a token is set but nobody is allowed in, so the bot only answers /whoami", service)
	case n == 1:
		ok("%s: one person allowed", service)
	default:
		bad("%s: %d people are allowed in. %s", service, n, sharedNote)
	}
}

func telegramAllowed(cfg *config.Config, getenv func(string) string) map[int64]bool {
	if v := getenv("TALARIA_TELEGRAM_ALLOWED_USERS"); v != "" {
		ids, _ := parseIDs(v)
		return ids
	}
	m := map[int64]bool{}
	for _, id := range cfg.Telegram.AllowedUsers {
		m[id] = true
	}
	return m
}

func discordAllowed(cfg *config.Config, getenv func(string) string) map[int64]bool {
	if v := getenv("TALARIA_DISCORD_ALLOWED_USERS"); v != "" {
		ids, _ := parseIDs(v)
		return ids
	}
	m := map[int64]bool{}
	for _, id := range cfg.Discord.AllowedUsers {
		m[id] = true
	}
	return m
}

// usedEnvironments are the environments something is set up to use: the default, the one the
// environment variable names, and those chats may switch to. The workspace is always defined, and needs
// docker only if something actually uses it.
func usedEnvironments(cfg *config.Config, getenv func(string) string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	add(getenv("TALARIA_ENV"))
	add(cfg.DefaultEnvironment)
	for _, n := range cfg.Telegram.Environments {
		add(n)
	}
	for _, n := range cfg.Discord.Environments {
		add(n)
	}
	return out
}
