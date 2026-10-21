package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	r, err := cfg.Resolve(config.Overrides{APIKey: getenv("TALARIA_API_KEY")}, getenv)
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
	if services == 0 {
		note("no bot token is set, so `tal gateway` has nothing to serve (the command line works without one)")
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

	if cfg.Ops.Listen != "" {
		fmt.Fprintln(out, "\nMonitoring")
		srv := &ops.Server{Addr: cfg.Ops.Listen, Token: getenv(cfg.Ops.TokenEnv)}
		if err := srv.Validate(); err != nil {
			bad("%v", err)
		} else {
			ok("health and metrics on %s", cfg.Ops.Listen)
		}
	}

	fmt.Fprintln(out, "\nTools")
	for name, spec := range cfg.Environments {
		if spec.Type == "docker" {
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
