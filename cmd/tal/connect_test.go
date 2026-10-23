package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/config"
	"talaria/internal/mcp"
	"talaria/internal/oauth"
	"talaria/internal/oauth/oauthtest"
	"talaria/internal/vault"
)

func followAddress(addr string) error {
	resp, err := http.Get(addr)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

func fakeDeps(env map[string]string) func(bool) connectDeps {
	return func(noBrowser bool) connectDeps {
		d := connectDeps{hc: oauth.NewHTTPClient(), getenv: func(k string) string { return env[k] }}
		if !noBrowser {
			d.open = followAddress
		}
		return d
	}
}

func connectorServer(t *testing.T) *oauthtest.Server {
	s := oauthtest.New()
	t.Cleanup(s.Close)
	s.Tools = []oauthtest.Tool{
		{Name: "list_events", Annotations: map[string]any{"readOnlyHint": true}, Run: func(map[string]any) string { return "standup" }},
		{Name: "send_email", Annotations: map[string]any{"openWorldHint": true}, Run: func(map[string]any) string { return "sent" }},
	}
	return s
}

func connectCfg(s *oauthtest.Server, perm string) *config.Config {
	return &config.Config{MCPServers: map[string]mcp.Server{
		"mail":  {URL: s.MCPURL(), OAuth: &mcp.OAuthConfig{}, Permissions: perm},
		"plain": {Command: "true"},
	}}
}

func setUpVault(t *testing.T, cfg *config.Config, home string) {
	t.Helper()
	tal(t, cfg, home, &scripted{answers: []string{passphrase, passphrase}}, "init")
	t.Setenv("TALARIA_VAULT_PASSPHRASE", passphrase)
}

func connectRun(t *testing.T, cfg *config.Config, home string, env map[string]string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runConnect(context.Background(), cfg, home, args, nil, &out, fakeDeps(env))
	return out.String(), err
}

func TestConnectSignsInAndShowsWhatWillAsk(t *testing.T) {
	s := connectorServer(t)
	home := t.TempDir()
	cfg := connectCfg(s, "")
	vault.ForTests(t)
	setUpVault(t, cfg, home)

	out, err := connectRun(t, cfg, home, nil)
	if err != nil || !strings.Contains(out, "mail") || !strings.Contains(out, "not signed in") || strings.Contains(out, "plain") {
		t.Fatalf("with no name, it lists the servers that sign in: %q %v", out, err)
	}

	out, err = connectRun(t, cfg, home, nil, "mail")
	if err != nil {
		t.Fatalf("%q %v", out, err)
	}
	for _, want := range []string{"Sign in by opening this address", s.URL + "/authorize?", "signed in to mail.", "connected: 2 tools (0 run without asking, 2 ask first)", `"permissions": "reads"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	v, _ := vault.Open(vaultPath(home), passphrase, nil)
	c, ok := v.Get("oauth.mail")
	if !ok || c.Kind != vault.KindOAuth || c.OAuth.AccessToken == "" {
		t.Fatalf("%+v %v", c, ok)
	}
	for _, secret := range []string{c.OAuth.AccessToken, c.OAuth.RefreshToken} {
		if strings.Contains(out, secret) {
			t.Fatalf("a token was printed:\n%s", out)
		}
	}
	if out, _ := connectRun(t, cfg, home, nil); !strings.Contains(out, "signed in") || strings.Contains(out, "not signed in") {
		t.Fatalf("%q", out)
	}
}

func TestConnectWithReadsFreeSaysHowManyRunOnTheirOwn(t *testing.T) {
	s := connectorServer(t)
	home := t.TempDir()
	cfg := connectCfg(s, mcp.PermReads)
	vault.ForTests(t)
	setUpVault(t, cfg, home)
	out, err := connectRun(t, cfg, home, nil, "mail")
	if err != nil || !strings.Contains(out, "connected: 2 tools (1 run without asking, 1 ask first)") || strings.Contains(out, "Every call asks") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestConnectRefusesWhatItCannotDo(t *testing.T) {
	s := connectorServer(t)
	home := t.TempDir()
	cfg := connectCfg(s, "")
	vault.ForTests(t)

	if _, err := connectRun(t, cfg, home, nil, "nope"); err == nil || !strings.Contains(err.Error(), "no MCP server named") || !strings.Contains(err.Error(), "mail") {
		t.Fatalf("%v", err)
	}
	if _, err := connectRun(t, cfg, home, nil, "plain"); err == nil || !strings.Contains(err.Error(), `"oauth": {}`) {
		t.Fatalf("%v", err)
	}
	t.Setenv("TALARIA_VAULT_PASSPHRASE", "")
	if _, err := connectRun(t, cfg, home, nil, "mail"); err == nil || !strings.Contains(err.Error(), "tal vault init") {
		t.Fatalf("no vault to keep the sign-in in: %v", err)
	}
	setUpVault(t, cfg, home)
	if _, err := connectRun(t, cfg, home, nil, "disconnect"); err == nil {
		t.Fatal("disconnect needs a name")
	}
	if _, err := connectRun(t, cfg, home, nil, "mail", "-bogus"); err == nil {
		t.Fatal("unknown flag")
	}
	cfg.MCPServers["mail"] = mcp.Server{URL: s.MCPURL(), OAuth: &mcp.OAuthConfig{ClientID: "x", ClientSecretEnv: "MAIL_SECRET"}}
	if _, err := connectRun(t, cfg, home, nil, "mail"); err == nil || !strings.Contains(err.Error(), "MAIL_SECRET") {
		t.Fatalf("%v", err)
	}
}

func TestConnectWithAClientSecretFromTheEnvironment(t *testing.T) {
	s := connectorServer(t)
	s.ConfidentialID, s.ConfidentialSecret = "my-client", "client-secret-from-env"
	home := t.TempDir()
	cfg := &config.Config{MCPServers: map[string]mcp.Server{"mail": {URL: s.MCPURL(), OAuth: &mcp.OAuthConfig{ClientID: "my-client", ClientSecretEnv: "MAIL_SECRET", Scopes: []string{"mail"}}}}}
	vault.ForTests(t)
	setUpVault(t, cfg, home)
	out, err := connectRun(t, cfg, home, map[string]string{"MAIL_SECRET": "client-secret-from-env"}, "mail")
	if err != nil || !strings.Contains(out, "signed in") {
		t.Fatalf("%q %v", out, err)
	}
	v, _ := vault.Open(vaultPath(home), passphrase, nil)
	c, _ := v.Get("oauth.mail")
	if c.OAuth.ClientSecret != "client-secret-from-env" {
		t.Fatal("the secret is kept in the vault, so renewals need no environment variable")
	}
	if strings.Contains(out, "client-secret-from-env") {
		t.Fatal("printed")
	}
}

func TestConnectNoBrowserPrintsTheAddressAndWaits(t *testing.T) {
	s := connectorServer(t)
	home := t.TempDir()
	cfg := connectCfg(s, "")
	vault.ForTests(t)
	setUpVault(t, cfg, home)
	out := &safeBuffer{}
	deps := func(noBrowser bool) connectDeps {
		d := fakeDeps(nil)(noBrowser)
		if noBrowser {
			d.open = nil
			d.paste = func(context.Context) (string, error) { // the user opens the printed address and pastes where it ended
				return "", io.EOF
			}
		}
		return d
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- runConnect(ctx, cfg, home, []string{"mail", "-no-browser"}, nil, out, deps) }()
	for i := 0; i < 200 && !strings.Contains(out.String(), "/authorize?"); i++ {
		waitABit()
	}
	cancel()
	<-done
	if !strings.Contains(out.String(), s.URL+"/authorize?") || strings.Contains(out.String(), "could not open a browser") {
		t.Fatalf("%s", out.String())
	}
}

func TestDisconnectRevokesAndRemoves(t *testing.T) {
	s := connectorServer(t)
	home := t.TempDir()
	cfg := connectCfg(s, "")
	vault.ForTests(t)
	setUpVault(t, cfg, home)
	connectRun(t, cfg, home, nil, "mail")
	v, _ := vault.Open(vaultPath(home), passphrase, nil)
	c, _ := v.Get("oauth.mail")

	out, err := connectRun(t, cfg, home, nil, "disconnect", "mail")
	if err != nil || !strings.Contains(out, "signed out of mail") || !strings.Contains(out, "told to forget") {
		t.Fatalf("%q %v", out, err)
	}
	if len(s.Revoked) != 1 || s.Revoked[0] != c.OAuth.RefreshToken {
		t.Fatalf("the service is told: %v", s.Revoked)
	}
	if _, ok := v.Get("oauth.mail"); ok {
		t.Fatal("removed from the vault")
	}
	if _, err := connectRun(t, cfg, home, nil, "disconnect", "mail"); err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("%v", err)
	}
	if out, _ := connectRun(t, cfg, home, nil); !strings.Contains(out, "not signed in") {
		t.Fatalf("%q", out)
	}
}

func TestMCPAuthGivesSourcesOnlyToOAuthServersAndOnlyWithAVault(t *testing.T) {
	s := connectorServer(t)
	vault.ForTests(t)
	home := t.TempDir()
	cfg := connectCfg(s, "")
	cfg.MCPServers["off"] = mcp.Server{URL: s.MCPURL(), OAuth: &mcp.OAuthConfig{}, Disabled: true}
	if got := mcpAuth(cfg, nil); got != nil {
		t.Fatalf("no vault, no sign-ins: %v", got)
	}
	setUpVault(t, cfg, home)
	v, _ := vault.Open(vaultPath(home), passphrase, nil)
	got := mcpAuth(cfg, v)
	if len(got) != 1 || got["mail"] == nil {
		t.Fatalf("%v", got)
	}
}

// What a gateway does with the tools of a service that can send: the ones
// that send or delete are kept out of "always allow".
func TestSendingToolsAreNeverRememberedWhenStarted(t *testing.T) {
	s := connectorServer(t)
	s.Tools = append(s.Tools, oauthtest.Tool{Name: "delete_event", Annotations: map[string]any{"destructiveHint": true}, Run: func(map[string]any) string { return "x" }})
	home := t.TempDir()
	cfg := connectCfg(s, mcp.PermReads)
	vault.ForTests(t)
	setUpVault(t, cfg, home)
	connectRun(t, cfg, home, nil, "mail")
	v, _ := vault.Open(vaultPath(home), passphrase, nil)

	g, _ := buildGuard(cfg, home, os.Getenv, nil)
	m := startMCP(context.Background(), cfg, v, slog.New(slog.DiscardHandler))
	defer m.Close()
	g.Policy.NeverRemember(m.Sensitive()...)
	if got := strings.Join(m.Sensitive(), ","); !strings.Contains(got, "mcp_mail_send_email") || !strings.Contains(got, "mcp_mail_delete_event") || strings.Contains(got, "list_events") {
		t.Fatalf("%s", got)
	}
	for _, n := range m.Sensitive() {
		if g.Policy.Rememberable(n) {
			t.Errorf("%s may be remembered", n)
		}
	}
	if !g.Policy.Rememberable("mcp_mail_list_events") {
		t.Fatal("reads are not affected")
	}
}

func waitABit() { time.Sleep(25 * time.Millisecond) }

// safeBuffer is a buffer one goroutine may write while another reads.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
