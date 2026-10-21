package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"talaria/internal/config"
	"talaria/internal/guard"
	"talaria/internal/mcp"
)

func TestCollectSecretsFindsEveryKindOfSecret(t *testing.T) {
	cfg := &config.Config{
		Profiles: map[string]config.Profile{
			"openai": {Type: "openai", APIKeyEnv: "OPENAI_API_KEY"},
			"claude": {Type: "anthropic", APIKey: "inline-anthropic-key"},
			"local":  {Type: "openai", BaseURL: "http://localhost:1/v1"}, // no key
		},
		Telegram: config.Telegram{Token: "telegram-token-value"},
		Discord:  config.Discord{TokenEnv: "MY_DC"},
		MCPServers: map[string]mcp.Server{
			"mail": {URL: "https://x", Headers: map[string]string{"Authorization": "Bearer mcp-bearer-secret-12345", "X-Ref": "${SOME_VAR}"}},
			"db":   {Command: "x", Env: map[string]string{"DB_PASSWORD": "mcp-env-secret-9999"}},
		},
	}
	got := collectSecrets(cfg, envFn("OPENAI_API_KEY", "sk-from-env-0000000", "MY_DC", "discord-token-value", "TALARIA_API_KEY", "override-key-0000"),
		[]string{"SERVICE_TOKEN=from-environ-token", "PATH=/usr/bin"})
	for _, want := range []string{
		"sk-from-env-0000000", "inline-anthropic-key", "telegram-token-value", "discord-token-value", "override-key-0000",
		"Bearer mcp-bearer-secret-12345", "mcp-bearer-secret-12345", "mcp-env-secret-9999", "from-environ-token",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	for _, unwanted := range []string{"${SOME_VAR}", "/usr/bin"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("%q is not a secret", unwanted)
		}
	}

	// And the redactor made from them hides them in a transcript.
	g, err := buildGuard(cfg, t.TempDir(), envFn("OPENAI_API_KEY", "sk-from-env-0000000"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out := g.Redact.Scrub("key=sk-from-env-0000000 and inline-anthropic-key"); strings.Contains(out, "sk-from") || strings.Contains(out, "inline-anthropic") {
		t.Fatalf("%q", out)
	}
}

func TestBuildGuardHonoursAuditSetting(t *testing.T) {
	off := false
	home := t.TempDir()
	g, err := buildGuard(&config.Config{Policy: config.Policy{Audit: &off}}, home, envFn(), nil)
	if err != nil || g.Audit != nil {
		t.Fatalf("audit:false means no log: %+v %v", g, err)
	}
	g, _ = buildGuard(&config.Config{}, home, envFn(), nil)
	if g.Audit == nil || g.Audit.Path != filepath.Join(home, "audit.log") || g.Grants.Path != filepath.Join(home, "grants.json") {
		t.Fatalf("%+v", g)
	}
	if _, err := buildGuard(&config.Config{Policy: config.Policy{Rules: map[string]string{"x": "bogus"}}}, home, envFn(), nil); err == nil {
		t.Fatal("bad rules are an error")
	}
}

// capture runs f and returns what it printed to standard output.
func capture(t *testing.T, f func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	err := f()
	w.Close()
	os.Stdout = old
	return <-done, err
}

func TestTalLogAndPolicyCommands(t *testing.T) {
	home := t.TempDir()
	l := &guard.Log{Path: filepath.Join(home, "audit.log")}
	now := time.Now()
	l.Append(guard.Record{Time: now, Origin: "chat", Gateway: "discord", Chat: 9, Tool: "write_file", Args: `{"path":"a"}`, Allowed: true, Via: "prompt", Status: "ok"})
	l.Append(guard.Record{Time: now, Origin: "chat", Gateway: "discord", Chat: 9, Tool: "shell", Allowed: false, Via: "rule", Rule: "shell = deny", Status: "not run"})

	out, err := capture(t, func() error { return runLog(home, nil) })
	if err != nil || !strings.Contains(out, "write_file ran (prompt)") || !strings.Contains(out, "shell REFUSED (rule: shell = deny)") || !strings.Contains(out, "discord:9") {
		t.Fatalf("%q %v", out, err)
	}
	out, _ = capture(t, func() error { return runLog(home, []string{"denied"}) })
	if strings.Contains(out, "write_file") || !strings.Contains(out, "shell") {
		t.Fatalf("denied shows only refusals: %q", out)
	}
	out, _ = capture(t, func() error { return runLog(home, []string{"1"}) })
	if strings.Contains(out, "write_file") {
		t.Fatalf("a count limits to the newest: %q", out)
	}
	if _, err := capture(t, func() error { return runLog(home, []string{"bogus"}) }); err == nil {
		t.Fatal("unknown arguments are an error")
	}
	empty, _ := capture(t, func() error { return runLog(t.TempDir(), nil) })
	if !strings.Contains(empty, "nothing recorded") {
		t.Fatalf("%q", empty)
	}

	// tal policy lists rules and grants, and forget removes them.
	grants := &guard.Grants{Path: filepath.Join(home, "grants.json")}
	grants.Add("discord:9", "write_file")
	grants.Add("telegram:4", "skill_save")
	cfg := &config.Config{Policy: config.Policy{Rules: map[string]string{"shell": "ask"}}}
	out, err = capture(t, func() error { return runPolicy(cfg, "cfg.json", home, nil) })
	if err != nil || !strings.Contains(out, "shell: ask") || !strings.Contains(out, "discord:9") || !strings.Contains(out, "write_file") || !strings.Contains(out, "telegram:4") {
		t.Fatalf("%q %v", out, err)
	}
	out, _ = capture(t, func() error { return runPolicy(cfg, "cfg.json", home, []string{"forget", "discord:9"}) })
	if !strings.Contains(out, "forgot 1") || grants.Allowed("discord:9", "write_file") || !grants.Allowed("telegram:4", "skill_save") {
		t.Fatalf("forget removes one chat's choices only: %q", out)
	}
	out, _ = capture(t, func() error { return runPolicy(cfg, "cfg.json", home, []string{"forget", "all"}) })
	if !strings.Contains(out, "forgot 1") || grants.Allowed("telegram:4", "skill_save") {
		t.Fatalf("%q", out)
	}
	if _, err := capture(t, func() error { return runPolicy(cfg, "cfg.json", home, []string{"forget"}) }); err == nil {
		t.Fatal("forget needs a target")
	}
}
