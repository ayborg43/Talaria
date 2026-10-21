package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/provider"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func write(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	if c, err := Load(filepath.Join(t.TempDir(), "absent.json")); err != nil || len(c.Profiles) != 0 {
		t.Fatalf("a missing file is fine: %+v %v", c, err)
	}

	p := write(t, `{"default_profile":"a","profiles":{"a":{"model":"m"}},"telegram":{"allowed_users":[1,2]}}`, 0o600)
	c, err := Load(p)
	if err != nil || c.Profiles["a"].Model != "m" || len(c.Telegram.AllowedUsers) != 2 || len(c.Warnings) != 0 {
		t.Fatalf("%+v %v", c, err)
	}

	// A typo must be an error, not a silently ignored setting.
	if _, err := Load(write(t, `{"profiles":{"a":{"modle":"m"}}}`, 0o600)); err == nil || !strings.Contains(err.Error(), "modle") {
		t.Fatalf("unknown field should be named in the error: %v", err)
	}
	if _, err := Load(write(t, `{broken`, 0o600)); err == nil {
		t.Fatal("malformed JSON should fail")
	}
}

func TestLoadWarnsAboutReadableSecrets(t *testing.T) {
	secret := `{"profiles":{"a":{"api_key":"sk-123"}}}`
	if c, _ := Load(write(t, secret, 0o644)); len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "chmod 600") {
		t.Fatalf("world-readable file with a key should warn: %+v", c.Warnings)
	}
	if c, _ := Load(write(t, secret, 0o600)); len(c.Warnings) != 0 {
		t.Fatalf("private file should not warn: %+v", c.Warnings)
	}
	if c, _ := Load(write(t, `{"profiles":{"a":{"api_key_env":"X"}}}`, 0o644)); len(c.Warnings) != 0 {
		t.Fatalf("no inline secret, nothing to warn about: %+v", c.Warnings)
	}
	if c, _ := Load(write(t, `{"telegram":{"token":"123:abc"}}`, 0o644)); len(c.Warnings) != 1 {
		t.Fatalf("an inline bot token is a secret too: %+v", c.Warnings)
	}
}

func TestResolveWithoutAConfigFileKeepsEnvBehaviour(t *testing.T) {
	r, err := (&Config{}).Resolve(Overrides{}, env("OPENAI_API_KEY", "sk-env"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "default" || r.Type != provider.TypeOpenAI || r.BaseURL != "https://api.openai.com/v1" || r.Model != "gpt-4o-mini" || r.APIKey != "sk-env" {
		t.Fatalf("%+v", r)
	}
	if err := r.CheckKey(); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePrecedence(t *testing.T) {
	c := &Config{Profiles: map[string]Profile{
		"claude": {Type: "anthropic", Model: "file-model", BaseURL: "https://file.example", APIKeyEnv: "MY_KEY"},
	}}

	// File only.
	r, _ := c.Resolve(Overrides{}, env("MY_KEY", "from-env-var"))
	if r.Model != "file-model" || r.BaseURL != "https://file.example" || r.APIKey != "from-env-var" || r.KeySource != "$MY_KEY" {
		t.Fatalf("file values should apply: %+v", r)
	}
	// Flags and environment override the file.
	r, _ = c.Resolve(Overrides{Model: "flag-model", BaseURL: "https://flag.example", APIKey: "override-key"}, env("MY_KEY", "from-env-var"))
	if r.Model != "flag-model" || r.BaseURL != "https://flag.example" || r.APIKey != "override-key" || r.KeySource != "TALARIA_API_KEY" {
		t.Fatalf("overrides should win: %+v", r)
	}
}

func TestResolveKeySources(t *testing.T) {
	for name, tc := range map[string]struct {
		p       Profile
		env     func(string) string
		wantKey string
		wantSrc string
	}{
		"api_key_env set":        {Profile{APIKeyEnv: "K"}, env("K", "v"), "v", "$K"},
		"inline key":             {Profile{APIKey: "inline"}, env(), "inline", "api_key in the config file"},
		"env var beats inline":   {Profile{APIKey: "inline", APIKeyEnv: "K"}, env("K", "v"), "v", "$K"},
		"inline when env unset":  {Profile{APIKey: "inline", APIKeyEnv: "K"}, env(), "inline", "api_key in the config file"},
		"openai default env":     {Profile{}, env("OPENAI_API_KEY", "o"), "o", "$OPENAI_API_KEY"},
		"anthropic default env":  {Profile{Type: "anthropic"}, env("ANTHROPIC_API_KEY", "a"), "a", "$ANTHROPIC_API_KEY"},
		"no fallback when named": {Profile{APIKeyEnv: "K"}, env("OPENAI_API_KEY", "o"), "", ""},
		"nothing at all":         {Profile{}, env(), "", ""},
	} {
		r, err := (&Config{Profiles: map[string]Profile{"p": tc.p}}).Resolve(Overrides{}, tc.env)
		if err != nil || r.APIKey != tc.wantKey || r.KeySource != tc.wantSrc {
			t.Errorf("%s: key=%q src=%q err=%v, want %q/%q", name, r.APIKey, r.KeySource, err, tc.wantKey, tc.wantSrc)
		}
	}
}

func TestProfileSelection(t *testing.T) {
	c := &Config{DefaultProfile: "b", Profiles: map[string]Profile{"a": {Model: "ma"}, "b": {Model: "mb"}}}

	for _, tc := range []struct{ flag, want string }{{"", "mb"}, {"a", "ma"}, {"b", "mb"}} {
		if r, err := c.Resolve(Overrides{Profile: tc.flag}, env()); err != nil || r.Model != tc.want {
			t.Errorf("profile %q: %+v %v", tc.flag, r, err)
		}
	}

	// A single profile is used without naming it.
	if r, _ := (&Config{Profiles: map[string]Profile{"only": {Model: "m"}}}).Resolve(Overrides{}, env()); r.Name != "only" {
		t.Fatalf("lone profile should be the default: %+v", r)
	}
	// A profile called "default" is used when nothing else is chosen.
	if r, _ := (&Config{Profiles: map[string]Profile{"default": {Model: "d"}, "x": {}}}).Resolve(Overrides{}, env()); r.Model != "d" {
		t.Fatalf("%+v", r)
	}

	// Unknown names, from the flag or from default_profile, list what exists.
	for _, ov := range []Overrides{{Profile: "nope"}} {
		if _, err := c.Resolve(ov, env()); err == nil || !strings.Contains(err.Error(), "a, b") {
			t.Fatalf("got %v", err)
		}
	}
	if _, err := (&Config{DefaultProfile: "gone"}).Resolve(Overrides{}, env()); err == nil || !strings.Contains(err.Error(), "none defined") {
		t.Fatalf("got %v", err)
	}
	if _, err := (&Config{Profiles: map[string]Profile{"x": {Type: "gemini"}}}).Resolve(Overrides{}, env()); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("got %v", err)
	}
}

func TestCheckKey(t *testing.T) {
	for name, tc := range []struct {
		r    Resolved
		fail bool
	}{
		{Resolved{Type: "openai", BaseURL: "https://api.openai.com/v1"}, true},
		{Resolved{Type: "anthropic", BaseURL: "https://api.anthropic.com"}, true},
		{Resolved{Type: "anthropic", BaseURL: "http://proxy.local"}, true}, // Anthropic always sends a key
		{Resolved{Type: "openai", BaseURL: "http://localhost:11434/v1"}, false},
		{Resolved{Type: "openai", BaseURL: "https://openrouter.ai/api/v1"}, false}, // key optional as far as we can tell
		{Resolved{Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "k"}, false},
	} {
		if err := tc.r.CheckKey(); (err != nil) != tc.fail {
			t.Errorf("case %d: err=%v, want failure=%v", name, err, tc.fail)
		}
	}

	r, _ := (&Config{Profiles: map[string]Profile{"claude": {Type: "anthropic", APIKeyEnv: "WORK_KEY"}}}).Resolve(Overrides{}, env())
	if err := r.CheckKey(); err == nil || !strings.Contains(err.Error(), "$WORK_KEY") || !strings.Contains(err.Error(), `"claude"`) {
		t.Fatalf("the error should name the profile and the variable to set: %v", err)
	}
}

func TestSpecAndProviderConstruction(t *testing.T) {
	r, _ := (&Config{Profiles: map[string]Profile{"c": {Type: "anthropic", MaxTokens: 4096}}}).Resolve(Overrides{APIKey: "k"}, env())
	p, err := provider.New(r.Spec())
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := p.(*provider.Anthropic); !ok || a.MaxTokens != 4096 || a.APIKey != "k" || a.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("%#v", p)
	}
}

func TestTelegramToken(t *testing.T) {
	c := &Config{Telegram: Telegram{Token: "inline", TokenEnv: "BOT"}}
	for name, tc := range map[string]struct {
		env  func(string) string
		want string
	}{
		"talaria env wins":      {env("TALARIA_TELEGRAM_TOKEN", "t", "BOT", "b"), "t"},
		"token_env":             {env("BOT", "b"), "b"},
		"inline as last resort": {env(), "inline"},
	} {
		if got := c.TelegramToken(tc.env); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if err := Init(path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("must not overwrite: %v", err)
	}

	// The example must itself be valid and resolve for every profile.
	c, err := Load(path)
	if err != nil {
		t.Fatalf("example config does not load: %v", err)
	}
	for _, name := range c.ProfileNames() {
		if r, err := c.Resolve(Overrides{Profile: name}, env()); err != nil || r.Model == "" {
			t.Errorf("example profile %q: %+v %v", name, r, err)
		}
	}
}

func TestPath(t *testing.T) {
	t.Setenv("TALARIA_CONFIG", "")
	if got := Path("/h"); got != "/h/config.json" {
		t.Fatal(got)
	}
	t.Setenv("TALARIA_CONFIG", "/etc/talaria.json")
	if got := Path("/h"); got != "/etc/talaria.json" {
		t.Fatal(got)
	}
}

func TestMCPServersInConfig(t *testing.T) {
	good := `{"mcp_servers":{
		"fs":{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/data"],"trusted":true},
		"remote":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer ${TOKEN}"},"auto_approve":["search"],"timeout_seconds":30}}}`
	c, err := Load(write(t, good, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.MCPServerNames(), ","); got != "fs,remote" {
		t.Fatalf("names %q", got)
	}
	if !c.MCPServers["fs"].Trusted || c.MCPServers["remote"].TimeoutSeconds != 30 || len(c.Warnings) != 0 {
		t.Fatalf("%+v warnings=%v", c.MCPServers, c.Warnings)
	}

	for name, bad := range map[string]string{
		"typo in a field": `{"mcp_servers":{"a":{"comand":"x"}}}`,
		"neither":         `{"mcp_servers":{"a":{}}}`,
		"both":            `{"mcp_servers":{"a":{"command":"x","url":"http://h"}}}`,
		"bad name":        `{"mcp_servers":{"has space":{"command":"x"}}}`,
		"bad url":         `{"mcp_servers":{"a":{"url":"ftp://h"}}}`,
		"wrong type":      `{"mcp_servers":{"a":{"command":"x","args":"not-a-list"}}}`,
	} {
		if _, err := Load(write(t, bad, 0o600)); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	if _, err := Load(write(t, `{"mcp_servers":{"bad name":{"command":"x"}}}`, 0o600)); err == nil || !strings.Contains(err.Error(), "bad name") {
		t.Fatalf("the error should name the server: %v", err)
	}
}

func TestMCPSecretsAndTransportWarnings(t *testing.T) {
	leaky := `{"mcp_servers":{"r":{"url":"https://h/mcp","headers":{"Authorization":"Bearer abc"}}}}`
	if c, _ := Load(write(t, leaky, 0o644)); len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "chmod 600") {
		t.Fatalf("a readable file with a literal bearer token should warn: %+v", c.Warnings)
	}
	if c, _ := Load(write(t, leaky, 0o600)); len(c.Warnings) != 0 {
		t.Fatalf("a private file is fine: %+v", c.Warnings)
	}
	safe := `{"mcp_servers":{"r":{"url":"https://h/mcp","headers":{"Authorization":"Bearer ${TOKEN}"}}}}`
	if c, _ := Load(write(t, safe, 0o644)); len(c.Warnings) != 0 {
		t.Fatalf("a ${VAR} reference is not a secret: %+v", c.Warnings)
	}
	plain := `{"mcp_servers":{"r":{"url":"http://example.com/mcp","headers":{"Authorization":"Bearer ${T}"}}}}`
	if c, _ := Load(write(t, plain, 0o600)); len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "plain http") {
		t.Fatalf("sending credentials over plain http should warn: %+v", c.Warnings)
	}
}

func TestTelegramStreamSetting(t *testing.T) {
	for in, want := range map[string]bool{
		`{}`:                            true, // on unless turned off
		`{"telegram":{}}`:               true,
		`{"telegram":{"stream":true}}`:  true,
		`{"telegram":{"stream":false}}`: false,
	} {
		c, err := Load(write(t, in, 0o600))
		if err != nil || c.Telegram.StreamReplies() != want {
			t.Errorf("%s: StreamReplies = %v (err %v), want %v", in, c.Telegram.StreamReplies(), err, want)
		}
	}
	if _, err := Load(write(t, `{"telegram":{"stream":"yes"}}`, 0o600)); err == nil {
		t.Fatal("a non-boolean stream setting should be rejected")
	}
}

func TestSubagentLimitsInConfig(t *testing.T) {
	c, err := Load(write(t, `{"subagents":{"max_parallel":2,"max_steps":30}}`, 0o600))
	if err != nil || c.Subagents.MaxParallel != 2 || c.Subagents.MaxSteps != 30 || c.Subagents.Disabled {
		t.Fatalf("%+v %v", c.Subagents, err)
	}
	if c, err := Load(write(t, `{"subagents":{"disabled":true}}`, 0o600)); err != nil || !c.Subagents.Disabled {
		t.Fatalf("%+v %v", c.Subagents, err)
	}
	for _, bad := range []string{
		`{"subagents":{"max_parallel":99}}`,
		`{"subagents":{"max_parallel":-1}}`,
		`{"subagents":{"max_steps":1000}}`,
		`{"subagents":{"max_step":5}}`, // a typo is not silently ignored
	} {
		if _, err := Load(write(t, bad, 0o600)); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}

func TestEnvironmentsInConfig(t *testing.T) {
	cfg := `{"default_environment":"sandbox","environments":{
		"sandbox":{"type":"docker","image":"python:3.12-slim","workdir":"/work","network":"none","mounts":["/home/me/proj:/work"]},
		"box":{"type":"ssh","host":"build.example.com","user":"ci"}}}`
	c, err := Load(write(t, cfg, 0o600))
	if err != nil || strings.Join(c.EnvironmentNames(), ",") != "box,sandbox,workspace" || len(c.Warnings) != 0 {
		t.Fatalf("%+v %v", c, err)
	}

	// Selection: the -env flag, then default_environment, then this machine.
	for _, tc := range []struct{ selected, want string }{{"", "sandbox"}, {"box", "box"}, {"sandbox", "sandbox"}, {"local", "local"}} {
		name, spec, err := c.Environment(tc.selected)
		if err != nil || name != tc.want || (tc.want == "local") != (spec == nil) {
			t.Errorf("Environment(%q) = %q, %v, %v", tc.selected, name, spec, err)
		}
	}
	if _, _, err := c.Environment("nope"); err == nil || !strings.Contains(err.Error(), "box, sandbox, workspace") {
		t.Fatalf("an unknown environment should list the real ones: %v", err)
	}

	// With nothing configured, everything runs on this machine.
	name, spec, err := (&Config{}).Environment("")
	if err != nil || name != "local" || spec != nil {
		t.Fatalf("%q %v %v", name, spec, err)
	}
	if _, _, err := (&Config{}).Environment("anything"); err == nil || !strings.Contains(err.Error(), "none defined") {
		t.Fatalf("%v", err)
	}
}

func TestEnvironmentsAreValidatedWhenLoaded(t *testing.T) {
	for name, bad := range map[string]string{
		"socket mount":     `{"environments":{"e":{"type":"docker","image":"a","mounts":["/var/run/docker.sock:/var/run/docker.sock"]}}}`,
		"missing image":    `{"environments":{"e":{"type":"docker"}}}`,
		"typo in a field":  `{"environments":{"e":{"type":"ssh","hots":"h"}}}`,
		"redefine local":   `{"environments":{"local":{"type":"ssh","host":"h"}}}`,
		"unknown default":  `{"default_environment":"ghost","environments":{"e":{"type":"ssh","host":"h"}}}`,
		"default, no envs": `{"default_environment":"ghost"}`,
	} {
		if _, err := Load(write(t, bad, 0o600)); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	if _, err := Load(write(t, `{"environments":{"bad name":{"type":"ssh","host":"h"}}}`, 0o600)); err == nil || !strings.Contains(err.Error(), "bad name") {
		t.Fatalf("the error should name the environment: %v", err)
	}
	if c, err := Load(write(t, `{"default_environment":"local"}`, 0o600)); err != nil || c.DefaultEnvironment != "local" {
		t.Fatalf("local is always a valid default: %v", err)
	}
}

func TestEnvironmentWarnings(t *testing.T) {
	c, _ := Load(write(t, `{"environments":{"e":{"type":"docker","image":"a","network":"host"}}}`, 0o600))
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "network isolation") {
		t.Fatalf("%v", c.Warnings)
	}
	leaky := `{"environments":{"e":{"type":"docker","image":"a","env":{"API_TOKEN":"abc123"}}}}`
	if c, _ := Load(write(t, leaky, 0o644)); len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "chmod 600") {
		t.Fatalf("a literal token in a readable file should warn: %v", c.Warnings)
	}
	if c, _ := Load(write(t, `{"environments":{"e":{"type":"docker","image":"a","env":{"LANG":"C"},"pass_env":["API_TOKEN"]}}}`, 0o644)); len(c.Warnings) != 0 {
		t.Fatalf("a harmless value, and a variable passed by name, are not secrets in the file: %v", c.Warnings)
	}
}

func TestTelegramEnvironmentsList(t *testing.T) {
	cfg := `{"environments":{"sandbox":{"type":"docker","image":"a"},"box":{"type":"ssh","host":"h"}},
		"telegram":{"environments":["sandbox","local"]}}`
	c, err := Load(write(t, cfg, 0o600))
	if err != nil || strings.Join(c.Telegram.Environments, ",") != "sandbox,local" {
		t.Fatalf("%+v %v", c.Telegram, err)
	}
	// Left unset, chats cannot switch: nothing changes for existing setups.
	if c, _ := Load(write(t, `{"environments":{"box":{"type":"ssh","host":"h"}}}`, 0o600)); len(c.Telegram.Environments) != 0 {
		t.Fatal("switching must be opt-in")
	}
	for name, bad := range map[string]string{
		"undefined":       `{"telegram":{"environments":["ghost"]}}`,
		"typo in a field": `{"telegram":{"enviroments":["local"]}}`,
		"not a list":      `{"telegram":{"environments":"local"}}`,
	} {
		if _, err := Load(write(t, bad, 0o600)); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	if _, err := Load(write(t, `{"telegram":{"environments":["ghost"]}}`, 0o600)); err == nil || !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("the error should name it: %v", err)
	}
}

func TestDiscordToken(t *testing.T) {
	c := &Config{Discord: Discord{Token: "inline", TokenEnv: "BOT"}}
	for name, tc := range map[string]struct {
		env  func(string) string
		want string
	}{
		"talaria env wins":       {env("TALARIA_DISCORD_TOKEN", "t", "BOT", "b"), "t"},
		"token_env":              {env("BOT", "b"), "b"},
		"inline as last resort":  {env(), "inline"},
		"telegram's is not used": {env("TALARIA_TELEGRAM_TOKEN", "tg"), "inline"},
	} {
		if got := c.DiscordToken(tc.env); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

// Discord shows user IDs as strings, and they are too long to trust to a JSON
// number everywhere, so both forms are accepted.
func TestDiscordAllowedUsersAcceptStringsAndNumbers(t *testing.T) {
	cfg := `{"discord":{"allowed_users":["1234567890123456789", 42],"stream":false,"environments":["local"]}}`
	c, err := Load(write(t, cfg, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Discord.AllowedUsers) != 2 || c.Discord.AllowedUsers[0] != 1234567890123456789 || c.Discord.AllowedUsers[1] != 42 {
		t.Fatalf("got %v", c.Discord.AllowedUsers)
	}
	if c.Discord.StreamReplies() {
		t.Fatal("stream:false must turn streaming off")
	}
	if !(Discord{}).StreamReplies() {
		t.Fatal("streaming is on by default")
	}
	// Written back as strings, which no tool rounds.
	raw, _ := json.Marshal(c.Discord.AllowedUsers)
	if string(raw) != `["1234567890123456789","42"]` {
		t.Fatalf("got %s", raw)
	}
	for name, bad := range map[string]string{
		"not a number":  `{"discord":{"allowed_users":["alice"]}}`,
		"negative":      `{"discord":{"allowed_users":["-5"]}}`,
		"zero":          `{"discord":{"allowed_users":[0]}}`,
		"fractional":    `{"discord":{"allowed_users":[1.5]}}`,
		"not a list":    `{"discord":{"allowed_users":"1"}}`,
		"undefined env": `{"discord":{"environments":["ghost"]}}`,
		"typo":          `{"discord":{"alowed_users":["1"]}}`,
	} {
		if _, err := Load(write(t, bad, 0o600)); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
}

func TestDiscordTokenInTheFileIsASecret(t *testing.T) {
	c, err := Load(write(t, `{"discord":{"token":"abc"}}`, 0o644))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(c.Warnings, "\n"), "holds secrets") {
		t.Fatalf("a readable file with a bot token should warn: %v", c.Warnings)
	}
}

func TestPolicyInConfig(t *testing.T) {
	cfg := `{"policy":{"rules":{"shell":"ask","write_file":"allow","mcp_mail_*":"deny"},"no_remember":["shell","cron_add"],"audit":false}}`
	c, err := Load(write(t, cfg, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if c.Policy.AuditEnabled() {
		t.Fatal("audit:false turns the log off")
	}
	if !(Policy{}).AuditEnabled() {
		t.Fatal("the log is on by default")
	}
	p, err := c.Policy.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Describe(); !strings.Contains(d, "mcp_mail_*: deny") || !strings.Contains(d, "write_file: allow") {
		t.Fatalf("%q", d)
	}
	if p.Rememberable("cron_add") || !p.Rememberable("skill_save") {
		t.Fatal("no_remember is the list given")
	}

	// Unset, shell is kept out of "always allow"; an explicit empty list is the way to allow it.
	c, _ = Load(write(t, `{"policy":{"rules":{}}}`, 0o600))
	p, _ = c.Policy.Parse()
	if p.Rememberable("shell") {
		t.Fatal("shell is not rememberable by default")
	}
	c, _ = Load(write(t, `{"policy":{"no_remember":[]}}`, 0o600))
	p, _ = c.Policy.Parse()
	if !p.Rememberable("shell") {
		t.Fatal(`"no_remember": [] allows it`)
	}
}

func TestBadPolicyIsRejectedWhenLoaded(t *testing.T) {
	for name, bad := range map[string]string{
		"unknown verdict": `{"policy":{"rules":{"shell":"sometimes"}}}`,
		"star in middle":  `{"policy":{"rules":{"a*b":"deny"}}}`,
		"typo":            `{"policy":{"rulez":{}}}`,
		"rules not a map": `{"policy":{"rules":["shell"]}}`,
	} {
		if _, err := Load(write(t, bad, 0o600)); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	if _, err := Load(write(t, `{"policy":{"rules":{"shell":"sometimes"}}}`, 0o600)); err == nil || !strings.Contains(err.Error(), `"sometimes"`) {
		t.Fatalf("the error should say what is wrong: %v", err)
	}
}

// The workspace needs no definition: it is there, attached to the container
// `tal workspace up` makes. A file may define its own under that name instead.
func TestWorkspaceEnvironmentIsBuiltIn(t *testing.T) {
	c, err := Load(write(t, `{}`, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	name, spec, err := c.Environment("workspace")
	if err != nil || name != "workspace" || spec == nil || spec.Type != "docker" || spec.Container != "talaria-workspace" || spec.Workdir != "/workspace" {
		t.Fatalf("%q %+v %v", name, spec, err)
	}
	if err := spec.Validate("workspace"); err != nil {
		t.Fatal(err)
	}
	// Even with no file at all.
	c, _ = Load(filepath.Join(t.TempDir(), "absent.json"))
	if _, spec, err := c.Environment("workspace"); err != nil || spec == nil {
		t.Fatalf("%+v %v", spec, err)
	}
	// Chats may be offered it like any environment.
	if _, err := Load(write(t, `{"telegram":{"environments":["workspace"]},"discord":{"environments":["workspace"]}}`, 0o600)); err != nil {
		t.Fatal(err)
	}
	// A definition in the file wins.
	c, _ = Load(write(t, `{"environments":{"workspace":{"type":"ssh","host":"myhost"}}}`, 0o600))
	if _, spec, _ := c.Environment("workspace"); spec.Type != "ssh" || spec.Host != "myhost" {
		t.Fatalf("%+v", spec)
	}
}

func TestBrowserAndWorkspaceSettings(t *testing.T) {
	c, err := Load(write(t, `{"browser":{"enabled":true,"allow_private":true,"idle_minutes":3,"binary":"/usr/bin/chromium","profile_dir":"/p","no_sandbox":false},
		"workspace":{"network":"none","memory":"4g","cpus":1.5}}`, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	b := c.Browser
	if b.Enabled == nil || !*b.Enabled || !b.AllowPrivate || b.IdleMinutes != 3 || b.Binary != "/usr/bin/chromium" || b.ProfileDir != "/p" || b.NoSandbox == nil || *b.NoSandbox {
		t.Fatalf("%+v", b)
	}
	if w := c.Workspace; w.Network != "none" || w.Memory != "4g" || w.CPUs != 1.5 {
		t.Fatalf("%+v", w)
	}
	if _, err := Load(write(t, `{"browser":{"alow_private":true}}`, 0o600)); err == nil {
		t.Fatal("a typo is an error")
	}
}

func TestVisionSetting(t *testing.T) {
	c, _ := Load(write(t, `{"profiles":{"big":{"type":"anthropic","api_key_env":"K"},"small":{"base_url":"http://localhost:1/v1","vision":false},"explicit":{"vision":true}}}`, 0o600))
	for name, noVision := range map[string]bool{"big": false, "small": true, "explicit": false} {
		r, err := c.Resolve(Overrides{Profile: name}, env())
		if err != nil || r.NoVision != noVision || r.Spec().NoVision != noVision {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
}

func TestVaultSettingInConfig(t *testing.T) {
	c, err := Load(write(t, `{"vault":{"key_file":"/home/me/.talaria/vault.key"}}`, 0o600))
	if err != nil || c.Vault.KeyFile != "/home/me/.talaria/vault.key" {
		t.Fatalf("%+v %v", c.Vault, err)
	}
	if _, err := Load(write(t, `{"vault":{"passphrase":"oops"}}`, 0o600)); err == nil {
		t.Fatal("there is deliberately no place in the config file for the passphrase itself")
	}
}

func TestDeepSeekProfile(t *testing.T) {
	c, err := Load(write(t, `{"profiles":{"ds":{"type":"deepseek"},"slow":{"type":"deepseek","thinking":false,"model":"deepseek-v4-pro"},"deep":{"type":"deepseek","thinking":true,"reasoning_effort":"max","vision":true}}}`, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve(Overrides{Profile: "ds"}, env("DEEPSEEK_API_KEY", "dk"))
	if err != nil || r.Type != "deepseek" || r.BaseURL != "https://api.deepseek.com" || r.Model != "deepseek-flash" ||
		r.APIKey != "dk" || r.KeySource != "$DEEPSEEK_API_KEY" || r.Thinking != "" || r.ReasoningEffort != "" || !r.NoVision {
		t.Fatalf("defaults: %+v %v", r, err)
	}
	r, _ = c.Resolve(Overrides{Profile: "slow"}, env())
	if r.Thinking != "disabled" || r.Model != "deepseek-v4-pro" || r.Spec().Thinking != "disabled" {
		t.Fatalf("%+v", r)
	}
	r, _ = c.Resolve(Overrides{Profile: "deep"}, env())
	if r.Thinking != "enabled" || r.ReasoningEffort != "max" || r.NoVision || r.Spec().ReasoningEffort != "max" || r.Spec().NoVision {
		t.Fatalf("%+v", r)
	}
	p, err := provider.New(r.Spec())
	if o, ok := p.(*provider.OpenAI); err != nil || !ok || !o.PassReasoning || o.Thinking != "enabled" {
		t.Fatalf("%#v %v", p, err)
	}
}

func TestDeepSeekSettingsAreValidated(t *testing.T) {
	for name, tc := range map[string]struct {
		p    Profile
		want string
	}{
		"bad effort":            {Profile{Type: "deepseek", ReasoningEffort: "extreme"}, "low, high or max"},
		"thinking on openai":    {Profile{Type: "openai", Thinking: new(bool)}, "only apply to type"},
		"effort on anthropic":   {Profile{Type: "anthropic", ReasoningEffort: "high"}, "only apply to type"},
		"effort on the default": {Profile{ReasoningEffort: "high"}, "only apply to type"},
	} {
		_, err := (&Config{Profiles: map[string]Profile{"p": tc.p}}).Resolve(Overrides{}, env())
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `"p"`) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := (&Config{Profiles: map[string]Profile{"p": {Type: "gemini"}}}).Resolve(Overrides{}, env()); err == nil || !strings.Contains(err.Error(), "deepseek") {
		t.Fatalf("the list of types names deepseek: %v", err)
	}
}

func TestDeepSeekKeyIsRequiredOnlyForTheHostedService(t *testing.T) {
	for _, tc := range []struct {
		base string
		fail bool
	}{{"", true}, {"https://api.deepseek.com", true}, {"https://api.deepseek.com/v1", true}, {"http://localhost:8000/v1", false}} {
		r, _ := (&Config{Profiles: map[string]Profile{"d": {Type: "deepseek", BaseURL: tc.base}}}).Resolve(Overrides{}, env())
		err := r.CheckKey()
		if (err != nil) != tc.fail || (err != nil && !strings.Contains(err.Error(), "$DEEPSEEK_API_KEY")) {
			t.Errorf("%q: %v", tc.base, err)
		}
	}
}

func TestPlainOpenAIProfileForDeepSeekIsWarnedAbout(t *testing.T) {
	c, err := Load(write(t, `{"profiles":{"old":{"base_url":"https://api.deepseek.com","api_key_env":"K"},"ok":{"type":"deepseek"},"other":{"base_url":"http://localhost:1/v1"}}}`, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], `"old"`) || !strings.Contains(c.Warnings[0], `"type": "deepseek"`) {
		t.Fatalf("%v", c.Warnings)
	}
}

func TestExampleConfigStillLoadsAndHasADeepSeekProfile(t *testing.T) {
	c, err := Load(write(t, Example, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := c.Resolve(Overrides{Profile: "deepseek"}, env()); err != nil || r.Type != "deepseek" || r.Model != "deepseek-flash" {
		t.Fatalf("%+v %v", r, err)
	}
	if len(c.Warnings) != 0 {
		t.Fatal(c.Warnings)
	}
}

func TestUserIDsMustBePositiveEverywhere(t *testing.T) {
	for _, bad := range []string{`{"telegram":{"allowed_users":[0]}}`, `{"telegram":{"allowed_users":[-5]}}`, `{"discord":{"allowed_users":["0"]}}`, `{"telegram":{"allowed_users":["x"]}}`} {
		if _, err := Load(write(t, bad, 0o600)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	c, err := Load(write(t, `{"telegram":{"allowed_users":[123456789,"42"]}}`, 0o600))
	if err != nil || len(c.Telegram.AllowedUsers) != 2 || c.Telegram.AllowedUsers[1] != 42 {
		t.Fatalf("%+v %v", c.Telegram.AllowedUsers, err)
	}
}

// The usual key variables are for the provider they name. A profile pointed anywhere else is not
// handed a real key it was never given: the shipped "local" example would otherwise get $OPENAI_API_KEY.
func TestTheDefaultKeyIsOnlySentToItsOwnProvider(t *testing.T) {
	e := env("OPENAI_API_KEY", "sk-real", "ANTHROPIC_API_KEY", "sk-ant-real", "DEEPSEEK_API_KEY", "ds-real")
	for name, tc := range map[string]struct {
		p       Profile
		o       Overrides
		wantKey string
	}{
		"openai at openai":       {Profile{}, Overrides{}, "sk-real"},
		"openai at a local one":  {Profile{BaseURL: "http://localhost:11434/v1"}, Overrides{}, ""},
		"openai at a proxy":      {Profile{BaseURL: "https://proxy.example/v1"}, Overrides{}, ""},
		"base url from a flag":   {Profile{}, Overrides{BaseURL: "http://localhost:1/v1"}, ""},
		"named variable":         {Profile{BaseURL: "https://proxy.example/v1", APIKeyEnv: "OPENAI_API_KEY"}, Overrides{}, "sk-real"},
		"anthropic at anthropic": {Profile{Type: "anthropic"}, Overrides{}, "sk-ant-real"},
		"anthropic via a proxy":  {Profile{Type: "anthropic", BaseURL: "https://proxy.example"}, Overrides{}, ""},
		"deepseek at deepseek":   {Profile{Type: "deepseek"}, Overrides{}, "ds-real"},
		"deepseek elsewhere":     {Profile{Type: "deepseek", BaseURL: "http://localhost:8000/v1"}, Overrides{}, ""},
		"the explicit override":  {Profile{BaseURL: "http://localhost:1/v1"}, Overrides{APIKey: "given"}, "given"},
	} {
		r, err := (&Config{Profiles: map[string]Profile{"p": tc.p}}).Resolve(tc.o, e)
		if err != nil || r.APIKey != tc.wantKey {
			t.Errorf("%s: key %q, want %q (%v)", name, r.APIKey, tc.wantKey, err)
		}
	}
	// A custom address that needs a key says why none was sent, instead of asking for a key that is set.
	r, _ := (&Config{Profiles: map[string]Profile{"p": {Type: "anthropic", BaseURL: "https://proxy.example"}}}).Resolve(Overrides{}, e)
	err := r.CheckKey()
	if err == nil || !strings.Contains(err.Error(), "api_key_env") || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") || strings.Contains(err.Error(), "sk-ant") {
		t.Fatalf("%v", err)
	}
}

func TestPlainHTTPToAModelIsWarnedAboutExceptOnThisMachine(t *testing.T) {
	c, err := Load(write(t, `{"profiles":{"a":{"base_url":"http://models.example/v1"},"b":{"base_url":"http://localhost:11434/v1"},"c":{"base_url":"http://127.0.0.1:1/v1"},"d":{"base_url":"https://models.example/v1"}}}`, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], `"a"`) || !strings.Contains(c.Warnings[0], "https") {
		t.Fatalf("%v", c.Warnings)
	}
}

func TestLimitsAndOpsSettings(t *testing.T) {
	c, err := Load(write(t, `{}`, 0o600))
	if err != nil || c.Limits.Messages() != 30 || c.Limits.ModelCalls() != 1000 || c.Ops.Listen != "" {
		t.Fatalf("defaults: %d %d %v", c.Limits.Messages(), c.Limits.ModelCalls(), err)
	}
	c, _ = Load(write(t, `{"limits":{"messages_per_minute":5,"model_calls_per_hour":50},"ops":{"listen":"127.0.0.1:9100","token_env":"T"}}`, 0o600))
	if c.Limits.Messages() != 5 || c.Limits.ModelCalls() != 50 || c.Ops.Listen != "127.0.0.1:9100" || c.Ops.TokenEnv != "T" {
		t.Fatalf("%+v %+v", c.Limits, c.Ops)
	}
	c, _ = Load(write(t, `{"limits":{"messages_per_minute":-1,"model_calls_per_hour":-1}}`, 0o600))
	if c.Limits.Messages() != 0 || c.Limits.ModelCalls() != 0 {
		t.Fatal("negative removes the limit")
	}
	if _, err := Load(write(t, `{"limits":{"message_per_minute":5}}`, 0o600)); err == nil {
		t.Fatal("a typo is an error")
	}
}

func TestUntrustedContentSetting(t *testing.T) {
	for in, want := range map[string]bool{`{}`: true, `{"policy":{"untrusted_content":"ask"}}`: true, `{"policy":{"untrusted_content":"off"}}`: false} {
		c, err := Load(write(t, in, 0o600))
		if err != nil || c.Policy.GuardUntrusted() != want {
			t.Errorf("%s: %v %v", in, c.Policy.GuardUntrusted(), err)
		}
	}
	if _, err := Load(write(t, `{"policy":{"untrusted_content":"sometimes"}}`, 0o600)); err == nil {
		t.Fatal("anything else is an error")
	}
}

func TestProtectedPathsInTheConfig(t *testing.T) {
	c, err := Load(write(t, `{"protected_paths":["~/Documents/taxes","/srv/secret"]}`, 0o600))
	if err != nil || len(c.ProtectedPaths) != 2 {
		t.Fatalf("%v %v", c.ProtectedPaths, err)
	}
}

// A container picks its provider with an environment variable and no config file.
func TestProviderTypeFromTheEnvironmentNeedsNoConfigFile(t *testing.T) {
	c := &Config{}
	r, err := c.Resolve(Overrides{Type: "deepseek"}, env("DEEPSEEK_API_KEY", "ds-key"))
	if err != nil || r.Type != "deepseek" || r.BaseURL != "https://api.deepseek.com" || r.Model != "deepseek-flash" || r.APIKey != "ds-key" || r.KeySource != "$DEEPSEEK_API_KEY" {
		t.Fatalf("%+v %v", r, err)
	}
	if err := r.CheckKey(); err != nil {
		t.Fatal(err)
	}
	r, _ = c.Resolve(Overrides{Type: "anthropic", Model: "claude-sonnet-5-5"}, env("ANTHROPIC_API_KEY", "a"))
	if r.Type != "anthropic" || r.APIKey != "a" || r.Model != "claude-sonnet-5-5" {
		t.Fatalf("%+v", r)
	}
	if _, err := c.Resolve(Overrides{Type: "gemini"}, env()); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("%v", err)
	}
	// It overrides what a profile says, as the other overrides do.
	pc := &Config{Profiles: map[string]Profile{"p": {Type: "openai", Model: "gpt"}}}
	if r, _ := pc.Resolve(Overrides{Type: "deepseek"}, env()); r.Type != "deepseek" {
		t.Fatalf("%+v", r)
	}
	// And the DeepSeek-only settings are still refused on another type.
	bad := &Config{Profiles: map[string]Profile{"p": {Thinking: new(bool)}}}
	if _, err := bad.Resolve(Overrides{}, env()); err == nil {
		t.Fatal("thinking needs the deepseek type")
	}
}
