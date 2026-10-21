package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"talaria/internal/config"
	"talaria/internal/guard"
)

// buildGuard sets up the permission layer: the config's rules, the file of
// choices users have made, the activity log, and the secrets to scrub.
func buildGuard(cfg *config.Config, home string, getenv func(string) string, environ []string) (*guard.Guard, error) {
	policy, err := cfg.Policy.Parse()
	if err != nil {
		return nil, err
	}
	g := &guard.Guard{
		Policy: policy,
		Grants: &guard.Grants{Path: filepath.Join(home, "grants.json")},
		Redact: guard.NewRedactor(collectSecrets(cfg, getenv, environ)),
	}
	if cfg.Policy.AuditEnabled() {
		g.Audit = &guard.Log{Path: filepath.Join(home, "audit.log")}
	}
	return g, nil
}

// collectSecrets gathers every secret Talaria knows about: model keys, bot
// tokens, what MCP servers are given, and any environment variable whose name
// says it holds one. The shapes of common tokens are caught separately.
func collectSecrets(cfg *config.Config, getenv func(string) string, environ []string) []string {
	var secrets []string
	for _, name := range append(cfg.ProfileNames(), "") {
		if r, err := cfg.Resolve(config.Overrides{Profile: name}, getenv); err == nil && r.APIKey != "" {
			secrets = append(secrets, r.APIKey)
		}
	}
	secrets = append(secrets, cfg.TelegramToken(getenv), cfg.DiscordToken(getenv), getenv("TALARIA_API_KEY"))
	for _, srv := range cfg.MCPServers {
		for _, group := range []map[string]string{srv.Env, srv.Headers} {
			for _, v := range group {
				if strings.Contains(v, "${") { // a reference; the variable it names is picked up below
					continue
				}
				secrets = append(secrets, v, strings.TrimSpace(strings.TrimPrefix(v, "Bearer ")))
			}
		}
	}
	return append(secrets, guard.EnvSecrets(environ)...)
}

// runLog handles `tal log [count] [denied]`: what the agent has run, newest last.
func runLog(home string, args []string) error {
	n, filter := 20, guard.Filter{}
	for _, a := range args {
		switch v, err := strconv.Atoi(a); {
		case a == "denied":
			filter.Denied = true
		case err == nil && v > 0:
			n = v
		default:
			return fmt.Errorf("unknown argument %q (try: tal log [count] [denied])", a)
		}
	}
	recs, err := (&guard.Log{Path: filepath.Join(home, "audit.log")}).Recent(filter, n)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		fmt.Println("nothing recorded yet")
	}
	for _, r := range recs {
		fmt.Println(guard.Format(r))
	}
	return nil
}

// runPolicy handles `tal policy` and `tal policy forget <chat|all>`.
func runPolicy(cfg *config.Config, cfgPath, home string, args []string) error {
	grants := &guard.Grants{Path: filepath.Join(home, "grants.json")}
	if len(args) > 0 {
		if args[0] != "forget" || len(args) != 2 {
			return fmt.Errorf("unknown policy command (try: tal policy, tal policy forget <service:chat | all>)")
		}
		keys := []string{args[1]}
		if args[1] == "all" {
			keys = keys[:0]
			for k := range grants.Chats() {
				keys = append(keys, k)
			}
		}
		total := 0
		for _, k := range keys {
			n, err := grants.Reset(k)
			if err != nil {
				return err
			}
			total += n
		}
		fmt.Printf("forgot %d always-allowed tool(s)\n", total)
		return nil
	}

	policy, err := cfg.Policy.Parse()
	if err != nil {
		return err
	}
	fmt.Printf("rules (from %s):\n", cfgPath)
	for _, line := range strings.Split(policy.Describe(), "\n") {
		fmt.Println("  " + line)
	}
	fmt.Println("\nwithout a rule: write_file, shell, skill changes and other risky tools ask; the rest run")
	if cfg.Policy.AuditEnabled() {
		fmt.Printf("activity log: %s (tal log)\n", filepath.Join(home, "audit.log"))
	} else {
		fmt.Println("activity log: off")
	}

	chats := grants.Chats()
	keys := make([]string, 0, len(chats))
	for k := range chats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("\nalways allowed by choice:")
	if len(keys) == 0 {
		fmt.Println("  nothing")
	}
	for _, k := range keys {
		fmt.Printf("  %-28s %s\n", k, strings.Join(chats[k], ", "))
	}
	return nil
}
