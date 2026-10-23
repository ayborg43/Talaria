package main

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"talaria/internal/agent"
	"talaria/internal/config"
	"talaria/internal/execenv"
	"talaria/internal/provider"
)

func TestCommandTable(t *testing.T) {
	if findCommand("bogus") != nil {
		t.Fatal("an unknown command finds nothing")
	}
	if c := findCommand(""); c == nil || !c.needsModel {
		t.Fatal("bare tal is the interactive chat, which needs the model")
	}
	if c := findCommand("gateway"); c == nil || !c.needsConfig || !c.needsModel {
		t.Fatal("gateway needs the config and the model")
	}
	if c := findCommand("restore"); c == nil || c.needsConfig || c.needsModel {
		t.Fatal("restore runs before the config is read")
	}
	seen := map[string]bool{}
	for _, c := range commands {
		if seen[c.name] {
			t.Fatalf("command %q is listed twice", c.name)
		}
		seen[c.name] = true
		if c.needsModel && !c.needsConfig {
			t.Fatalf("command %q needs the model without the config", c.name)
		}
		if c.run == nil {
			t.Fatalf("command %q has nothing to run", c.name)
		}
	}
	// The unknown-command error lists every command from the table itself.
	want := "tal gateway, tal cron list, tal mcp list, tal env, tal log, tal policy, " +
		"tal workspace, tal vault, tal connect, tal backup, tal restore, tal doctor, " +
		"tal calendar, tal email, tal version, tal config"
	if got := commandHints(); got != want {
		t.Fatalf("hints:\n got %q\nwant %q", got, want)
	}
}

func envFn(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestGatewayBackends(t *testing.T) {
	cfg := &config.Config{Profiles: map[string]config.Profile{
		"openai": {Type: "openai", APIKeyEnv: "OPENAI_API_KEY", Model: "gpt-x"},
		"claude": {Type: "anthropic", APIKeyEnv: "ANTHROPIC_API_KEY", Model: "claude-x"},
		"local":  {Type: "openai", BaseURL: "http://localhost:11434/v1", Model: "llama"},
	}}
	getenv := envFn("OPENAI_API_KEY", "sk-1")
	def, _ := cfg.Resolve(config.Overrides{Profile: "openai"}, getenv)
	defProvider := provider.NewOpenAI(def.BaseURL, def.APIKey)

	b := gatewayBackends(cfg, def, defProvider, getenv)
	if b.Default != "openai" || strings.Join(b.Names, ",") != "claude,local,openai" {
		t.Fatalf("default %q names %v", b.Default, b.Names)
	}

	// The profile in use keeps the provider that was already built for it.
	got, err := b.Open("openai")
	if err != nil || got.Provider != provider.Provider(defProvider) || got.Model != "gpt-x" || got.Desc != "openai, gpt-x" {
		t.Fatalf("%+v %v", got, err)
	}
	// Needs no key, so it opens.
	if got, err := b.Open("local"); err != nil || got.Model != "llama" {
		t.Fatalf("%+v %v", got, err)
	}
	// A profile whose key is missing explains what to set, and is not a gateway failure.
	if _, err := b.Open("claude"); err == nil || !strings.Contains(err.Error(), "$ANTHROPIC_API_KEY") {
		t.Fatalf("got %v", err)
	}
	if _, err := b.Open("nope"); err == nil {
		t.Fatal("an unknown profile cannot be opened")
	}

	// Once the key exists the profile opens as a native Anthropic provider, and is built once.
	b = gatewayBackends(cfg, def, defProvider, envFn("OPENAI_API_KEY", "sk-1", "ANTHROPIC_API_KEY", "ak-1"))
	first, err := b.Open("claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := first.Provider.(*provider.Anthropic); !ok || first.Desc != "anthropic, claude-x" {
		t.Fatalf("%+v", first)
	}
	if again, _ := b.Open("claude"); again.Provider != first.Provider {
		t.Fatal("a profile should be built once and reused")
	}
}

// Profiles added or changed while Talaria runs are visible at once: the list
// is live, the cache drops on demand, and effort reaches thinking models.
func TestGatewayBackendsStayLiveAndCarryEffort(t *testing.T) {
	yes := true
	cfg := &config.Config{Profiles: map[string]config.Profile{
		"main": {Type: "openai", APIKeyEnv: "OPENAI_API_KEY", Model: "gpt-x", BestFor: []string{"cheap"}},
		"deep": {Type: "deepseek", APIKey: "dk", Model: "deepseek-chat", BestFor: []string{"code"}, Vision: &yes},
	}}
	getenv := envFn("OPENAI_API_KEY", "sk-1")
	def, _ := cfg.Resolve(config.Overrides{Profile: "main"}, getenv)
	b := gatewayBackends(cfg, def, provider.NewOpenAI(def.BaseURL, def.APIKey), getenv)

	be, err := b.Open("deep")
	if err != nil || !be.Reasoning || !be.Vision || len(be.Best) != 1 || be.Best[0] != "code" {
		t.Fatalf("traits: %+v %v", be, err)
	}
	if be, err := b.OpenEffort("deep", "high"); err != nil {
		t.Fatalf("effort: %v", err)
	} else if p, ok := be.Provider.(*provider.OpenAI); !ok || p.ReasoningEffort != "high" {
		t.Fatalf("effort did not reach the model: %+v", be.Provider)
	}
	if be, err := b.OpenEffort("main", "high"); err != nil {
		t.Fatalf("effort on a plain model: %v", err)
	} else if p, ok := be.Provider.(*provider.OpenAI); !ok || p.ReasoningEffort != "" {
		t.Fatalf("effort should mean nothing without thinking: %+v", be.Provider)
	}

	cfg.Profiles["new"] = config.Profile{Type: "openai", APIKey: "nk", Model: "fresh"}
	if names := b.List(); !slices.Contains(names, "new") {
		t.Fatalf("a profile added while running is not listed: %v", names)
	}
	if _, err := b.Open("new"); err != nil {
		t.Fatalf("a profile added while running: %v", err)
	}
	deep := cfg.Profiles["deep"]
	deep.Model = "deepseek-reasoner"
	cfg.Profiles["deep"] = deep
	b.Forget()
	if be, err := b.Open("deep"); err != nil || be.Model != "deepseek-reasoner" {
		t.Fatalf("an edited profile: %+v %v", be, err)
	}
}

func TestGatewayBackendsWithoutAConfigFile(t *testing.T) {
	cfg := &config.Config{}
	def, _ := cfg.Resolve(config.Overrides{}, envFn("OPENAI_API_KEY", "sk-1"))
	b := gatewayBackends(cfg, def, provider.NewOpenAI(def.BaseURL, def.APIKey), envFn())
	if b.Default != "default" || len(b.Names) != 1 || b.Names[0] != "default" {
		t.Fatalf("the implicit profile should be offered under its own name: %q %v", b.Default, b.Names)
	}
	if _, err := b.Open("default"); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayEnvironments(t *testing.T) {
	cfg := &config.Config{
		Environments: map[string]execenv.Spec{
			"sandbox": {Type: "docker", Image: "python:3.12-slim"},
			"build":   {Type: "ssh", Host: "build.example.com", User: "ci"},
		},
		Telegram: config.Telegram{Environments: []string{"sandbox", "local"}},
	}
	var applied []string
	apply := func(_ *agent.Agent, env execenv.Env) { applied = append(applied, env.Describe()) }
	log := slog.New(slog.DiscardHandler)

	pool := gatewayPool(cfg, "sandbox", execenv.Local{}, nil, os.Getenv, log)
	defer pool.Close()
	envs := gatewayEnvironments(cfg, cfg.Telegram.Environments, "sandbox", pool, apply)
	// The list is what the config allows plus the default, sorted, with no duplicates, and
	// "build" is defined but not offered to chats.
	if envs.Default != "sandbox" || strings.Join(envs.Names, ",") != "local,sandbox" {
		t.Fatalf("default %q names %v", envs.Default, envs.Names)
	}
	if envs.Summary("sandbox") != "docker: container from python:3.12-slim" || envs.Summary("local") != "this machine (built in)" {
		t.Fatalf("%q / %q", envs.Summary("sandbox"), envs.Summary("local"))
	}

	// The default is already running (Talaria started it); "local" needs no start.
	if !envs.Running("sandbox") || envs.Running("local") {
		t.Fatalf("running: sandbox=%v local=%v", envs.Running("sandbox"), envs.Running("local"))
	}
	if env, err := envs.Get(context.Background(), "local"); err != nil || env.Remote() {
		t.Fatalf("%v %v", env, err)
	}
	if !envs.Running("local") {
		t.Fatal("local is running once asked for")
	}
	envs.Apply(nil, execenv.Local{})
	if len(applied) != 1 || applied[0] != "this machine" {
		t.Fatalf("Apply is the app's UseEnvironment: %v", applied)
	}

	// Only what the config names can be started: an environment defined for the
	// command line but not offered to chats is unreachable from a chat, and so is a name that
	// does not exist at all.
	if _, err := envs.Get(context.Background(), "ghost"); err == nil || !strings.Contains(err.Error(), `no environment named "ghost"`) {
		t.Fatalf("%v", err)
	}
	for _, n := range envs.Names {
		if n == "build" {
			t.Fatal("build is not offered to chats")
		}
	}

	// A start that fails says why and does not stick.
	t.Setenv("PATH", t.TempDir())
	cfg2 := &config.Config{Environments: map[string]execenv.Spec{"box": {Type: "ssh", Host: "h"}}, Telegram: config.Telegram{Environments: []string{"box"}}}
	pool2 := gatewayPool(cfg2, "local", nil, nil, os.Getenv, log)
	defer pool2.Close()
	envs2 := gatewayEnvironments(cfg2, cfg2.Telegram.Environments, "local", pool2, apply)
	if _, err := envs2.Get(context.Background(), "box"); err == nil || !strings.Contains(err.Error(), "ssh is not installed") {
		t.Fatalf("%v", err)
	}
	if envs2.Running("box") {
		t.Fatal("a failed start is not running")
	}
}

func TestGatewayEnvironmentsOffUnlessConfigured(t *testing.T) {
	cfg := &config.Config{Environments: map[string]execenv.Spec{"sandbox": {Type: "docker", Image: "x"}}}
	pool := gatewayPool(cfg, "local", nil, nil, os.Getenv, slog.New(slog.DiscardHandler))
	defer pool.Close()
	if envs := gatewayEnvironments(cfg, cfg.Telegram.Environments, "local", pool, nil); envs != nil {
		t.Fatal("with telegram.environments empty, chats cannot switch")
	}
	if envs := gatewayEnvironments(cfg, cfg.Discord.Environments, "local", pool, nil); envs != nil {
		t.Fatal("with discord.environments empty, chats cannot switch")
	}
}

// Each chat service offers its own list of environments, but they share the
// environments themselves, so a container one service started serves the other.
func TestServicesOfferTheirOwnEnvironmentsOverOneSharedPool(t *testing.T) {
	cfg := &config.Config{
		Environments: map[string]execenv.Spec{
			"sandbox": {Type: "docker", Image: "a"},
			"build":   {Type: "ssh", Host: "h"},
		},
		Telegram: config.Telegram{Environments: []string{"sandbox"}},
		Discord:  config.Discord{Environments: []string{"build", "local"}},
	}
	pool := gatewayPool(cfg, "local", nil, nil, os.Getenv, slog.New(slog.DiscardHandler))
	defer pool.Close()
	tg := gatewayEnvironments(cfg, cfg.Telegram.Environments, "local", pool, nil)
	dc := gatewayEnvironments(cfg, cfg.Discord.Environments, "local", pool, nil)

	if strings.Join(tg.Names, ",") != "local,sandbox" || strings.Join(dc.Names, ",") != "build,local" {
		t.Fatalf("telegram %v, discord %v", tg.Names, dc.Names)
	}
	a, _ := tg.Get(context.Background(), "local")
	b, _ := dc.Get(context.Background(), "local")
	if a == nil || a != b {
		t.Fatal("both services should be handed the same environment")
	}

	for name, want := range map[string]string{
		"sandbox": "  (Telegram chats may switch to it)",
		"build":   "  (Discord chats may switch to it)",
		"local":   "  (Discord chats may switch to it)",
		"other":   "",
	} {
		if got := switchableBy(cfg, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	cfg.Telegram.Environments = []string{"build"}
	if got := switchableBy(cfg, "build"); got != "  (Telegram and Discord chats may switch to it)" {
		t.Fatalf("%q", got)
	}
}

// Discord user IDs are snowflakes: 64-bit numbers far past what a float holds exactly.
func TestParseIDsAcceptsDiscordSnowflakes(t *testing.T) {
	ids, err := parseIDs("1234567890123456789, 42,,")
	if err != nil || !ids[1234567890123456789] || !ids[42] || len(ids) != 2 {
		t.Fatalf("%v %v", ids, err)
	}
	if _, err := parseIDs("alice"); err == nil {
		t.Fatal("a name is not an ID")
	}
}

func TestAllowlistIDsFromTheEnvironmentMustBePositive(t *testing.T) {
	if ids, err := parseIDs("12, 34 ,,56"); err != nil || len(ids) != 3 || !ids[34] {
		t.Fatalf("%v %v", ids, err)
	}
	for _, bad := range []string{"0", "-1", "12,0", "abc", "1.5"} {
		if _, err := parseIDs(bad); err == nil {
			t.Errorf("%q was accepted: user 0 is what a missing ID parses to", bad)
		}
	}
}
