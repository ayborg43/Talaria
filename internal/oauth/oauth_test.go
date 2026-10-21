package oauth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/oauth/oauthtest"
	"talaria/internal/vault"
)

func newVault(t *testing.T) *vault.Vault {
	t.Helper()
	vault.ForTests(t)
	v, err := vault.Create(filepath.Join(t.TempDir(), "vault.enc"), "a long enough passphrase")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func echoTools() []oauthtest.Tool {
	return []oauthtest.Tool{{Name: "list_events", Run: func(map[string]any) string { return "standup at 9" }}}
}

func TestDiscoverFindsTheAuthorizationServer(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	d, err := Discover(context.Background(), NewHTTPClient(), s.MCPURL(), "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Resource != s.MCPURL() || d.Server.Issuer != s.URL || d.Server.TokenEndpoint != s.URL+"/token" || d.Server.RegistrationEndpoint == "" || d.Scope != "calendar mail" {
		t.Fatalf("%+v", d)
	}
}

func TestDiscoverRefusesWhatCannotBeTrusted(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*oauthtest.Server)
		want string
	}{
		"metadata for another server":       {func(s *oauthtest.Server) { s.WrongResource = true }, "not"},
		"an issuer that is not the address": {func(s *oauthtest.Server) { s.WrongIssuer = true }, "refusing"},
		"no PKCE":                           {func(s *oauthtest.Server) { s.NoPKCE = true }, "PKCE"},
		"nothing to say who signs in":       {func(s *oauthtest.Server) { s.NoMetadata = true }, "did not say which authorization server"},
	} {
		s := oauthtest.New()
		tc.set(s)
		_, err := Discover(context.Background(), NewHTTPClient(), s.MCPURL(), "")
		s.Close()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDiscoverWithTheAuthorizationServerNamedInTheConfig(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	s.NoMetadata = true // the server says nothing, but the user knows who signs in
	d, err := Discover(context.Background(), NewHTTPClient(), s.MCPURL(), s.URL)
	if err != nil || d.Server.TokenEndpoint == "" {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestDiscoverNotesAServerThatNeedsNoSignIn(t *testing.T) {
	open := newServerNoAuth(t)
	_, err := Discover(context.Background(), NewHTTPClient(), open, "")
	if err == nil || !strings.Contains(err.Error(), "did not ask for a sign-in") {
		t.Fatalf("%v", err)
	}
}

// Endpoints are checked as a real, public server would be: nothing in the clear, and
// nothing that sends Talaria into the machine's own network.
func TestEndpointsAreChecked(t *testing.T) {
	for _, bad := range []string{
		"http://auth.example.com/token", "ftp://auth.example.com/", "https://127.0.0.1/token", "https://localhost/token",
		"https://10.0.0.5/token", "https://192.168.1.1/x", "https://169.254.169.254/latest", "not a url", "https:///x",
	} {
		if err := checkEndpoint("token endpoint", bad, "mcp.example.com"); err == nil {
			t.Errorf("%s should be refused for a public server", bad)
		}
	}
	for _, ok := range []string{"https://auth.example.com/token", "https://accounts.google.com/o/oauth2/v2/auth"} {
		if err := checkEndpoint("token endpoint", ok, "mcp.example.com"); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// A server on this machine may use endpoints on this machine, over plain http.
	if err := checkEndpoint("token endpoint", "http://127.0.0.1:9000/token", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := checkEndpoint("token endpoint", "http://auth.example.com/token", "127.0.0.1"); err == nil {
		t.Fatal("plain http to a remote host is not fine even for a local server")
	}
}

// browser plays the part of the user's browser: it visits the address and
// follows the redirects, which end at Talaria's own callback.
func browser(t *testing.T) func(string) error {
	return func(addr string) error {
		resp, err := http.Get(addr)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil
	}
}

func login(t *testing.T, s *oauthtest.Server, cfg Config) (*vault.OAuthData, error) {
	t.Helper()
	var out bytes.Buffer
	return Login{ServerURL: s.MCPURL(), Config: cfg, Out: &out, Open: browser(t), Timeout: 10 * time.Second}.Run(context.Background())
}

func TestLoginRegistersSignsInAndGetsTokens(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	data, err := login(t, s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Registrations != 1 {
		t.Fatalf("Talaria registers itself when it has no client: %d", s.Registrations)
	}
	if !strings.HasPrefix(data.AccessToken, "at-") || !strings.HasPrefix(data.RefreshToken, "rt-") || !strings.HasPrefix(data.ClientID, "client-") ||
		data.TokenEndpoint != s.URL+"/token" || data.Resource != s.MCPURL() || data.Issuer != s.URL || data.ClientSecret != "" {
		t.Fatalf("%+v", data)
	}
	if data.Expiry.Before(time.Now().Add(50*time.Minute)) || strings.Join(data.Scopes, " ") != "calendar mail" {
		t.Fatalf("%v %v", data.Expiry, data.Scopes)
	}
	form := s.TokenForms[0]
	if form.Get("code_verifier") == "" || form.Get("resource") != s.MCPURL() || form.Get("client_secret") != "" || form.Get("grant_type") != "authorization_code" {
		t.Fatalf("the token request: %v", form)
	}
	if v := form.Get("code_verifier"); len(v) < 43 || len(v) > 128 {
		t.Fatalf("RFC 7636 asks for 43 to 128 characters: %d", len(v))
	}
}

func TestLoginWithAClientFromTheConfig(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	s.ConfidentialID, s.ConfidentialSecret = "my-client", "my-client-secret"
	data, err := login(t, s, Config{ClientID: "my-client", ClientSecret: "my-client-secret", Scopes: []string{"calendar"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Registrations != 0 {
		t.Fatal("a configured client is not registered again")
	}
	if data.ClientID != "my-client" || data.ClientSecret != "my-client-secret" || strings.Join(data.Scopes, " ") != "calendar mail" {
		t.Fatalf("%+v", data)
	}
	if s.TokenForms[0].Get("client_secret") != "my-client-secret" {
		t.Fatalf("a confidential client proves itself: %v", s.TokenForms[0])
	}
}

func TestLoginWhenTheServerWillNotRegisterClients(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	s.NoRegistration = true
	_, err := login(t, s, Config{})
	if err == nil || !strings.Contains(err.Error(), "oauth.client_id") {
		t.Fatalf("%v", err)
	}
}

func TestLoginDeclined(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	s.Deny = true
	_, err := login(t, s, Config{})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("%v", err)
	}
}

func TestLoginIgnoresACallbackThatIsNotOurs(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	var out bytes.Buffer
	forged := func(context.Context) (string, error) {
		return "http://127.0.0.1:1/callback?code=stolen&state=not-our-state", nil
	}
	_, err := Login{ServerURL: s.MCPURL(), Out: &out, Paste: forged, Timeout: 5 * time.Second}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "wrong state") {
		t.Fatalf("%v", err)
	}
	if len(s.TokenForms) != 0 {
		t.Fatal("a code from a callback with the wrong state must never be exchanged")
	}
}

// For a Talaria on a machine whose browser cannot reach it: the address the
// browser ended on is pasted back.
func TestLoginByPastingTheRedirectAddress(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	var out bytes.Buffer
	var mu sync.Mutex
	paste := func(ctx context.Context) (string, error) {
		// Find the address Talaria printed, visit it without following the redirect, and hand back where it pointed.
		var addr string
		for i := 0; i < 100 && addr == ""; i++ {
			mu.Lock()
			text := out.String()
			mu.Unlock()
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), s.URL+"/authorize") {
					addr = strings.TrimSpace(line)
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.Get(addr)
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		return resp.Header.Get("Location"), nil
	}
	w := &lockedWriter{mu: &mu, w: &out}
	data, err := Login{ServerURL: s.MCPURL(), Out: w, Paste: paste, Timeout: 10 * time.Second}.Run(context.Background())
	if err != nil || data.AccessToken == "" {
		t.Fatalf("%v", err)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestLoginTimesOut(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	var out bytes.Buffer
	_, err := Login{ServerURL: s.MCPURL(), Out: &out, Timeout: 100 * time.Millisecond}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no sign-in came back") {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(out.String(), s.URL+"/authorize?") {
		t.Fatalf("the address to visit was printed: %s", out.String())
	}
}

func TestLoginSendsWhatAServerNeedsToCheck(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	var out bytes.Buffer
	var addr string
	open := func(a string) error { addr = a; return browser(t)(a) }
	if _, err := (Login{ServerURL: s.MCPURL(), Out: &out, Open: open, Timeout: 10 * time.Second}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(addr)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("resource") != s.MCPURL() || q.Get("response_type") != "code" || q.Get("scope") != "calendar mail" {
		t.Fatalf("%v", q)
	}
	if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") || !strings.HasSuffix(q.Get("redirect_uri"), "/callback") {
		t.Fatalf("a loopback address, as RFC 8252 asks for a native app: %s", q.Get("redirect_uri"))
	}
	if q.Get("code_verifier") != "" {
		t.Fatal("the verifier is a secret: it must not be in the address")
	}
}

func signedIn(t *testing.T, s *oauthtest.Server, v *vault.Vault) {
	t.Helper()
	data, err := login(t, s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(v, "mail", data); err != nil {
		t.Fatal(err)
	}
}

func TestSourceHandsOutTheStoredTokenWithoutAsking(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	v := newVault(t)
	signedIn(t, s, v)
	src := &Source{Vault: v, Server: "mail"}
	tok, err := src.Token(context.Background())
	if err != nil || !strings.HasPrefix(tok, "at-") {
		t.Fatalf("%q %v", tok, err)
	}
	if s.Refreshes != 0 {
		t.Fatal("a fresh token is just used")
	}
	if c, ok := v.Get("oauth.mail"); !ok || c.Kind != vault.KindOAuth || c.Sites[0] != s.URL {
		t.Fatalf("%+v", c)
	}
}

func TestSourceRenewsAnExpiringTokenAndKeepsItInTheVault(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	s.RotateRefresh = true
	v := newVault(t)
	signedIn(t, s, v)
	first, _ := v.Get("oauth.mail")

	now := time.Now().Add(59*time.Minute + 30*time.Second) // within the minute before expiry
	src := &Source{Vault: v, Server: "mail", Now: func() time.Time { return now }}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok == first.OAuth.AccessToken || s.Refreshes != 1 {
		t.Fatalf("an about-to-expire token is renewed first: %q %d", tok, s.Refreshes)
	}
	got, _ := v.Get("oauth.mail")
	if got.OAuth.AccessToken != tok || got.OAuth.RefreshToken == first.OAuth.RefreshToken {
		t.Fatal("the renewed tokens, and the new refresh token the server rotated to, are saved")
	}
	if form := s.TokenForms[len(s.TokenForms)-1]; form.Get("resource") != s.MCPURL() || form.Get("grant_type") != "refresh_token" {
		t.Fatalf("%v", form)
	}
	// Used again straight away, it is not renewed again.
	src.Now = func() time.Time { return time.Now() }
	if tok2, _ := src.Token(context.Background()); tok2 != tok || s.Refreshes != 1 {
		t.Fatalf("%q %d", tok2, s.Refreshes)
	}
}

func TestSourceKeepsTheRefreshTokenWhenTheServerDoesNotRotateIt(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	v := newVault(t)
	signedIn(t, s, v)
	first, _ := v.Get("oauth.mail")
	src := &Source{Vault: v, Server: "mail", Now: func() time.Time { return time.Now().Add(2 * time.Hour) }}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Get("oauth.mail"); got.OAuth.RefreshToken != first.OAuth.RefreshToken {
		t.Fatal("no new refresh token was issued, so the old one stays")
	}
}

func TestSourceSaysToSignInAgainWhenTheSignInIsGone(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	v := newVault(t)
	signedIn(t, s, v)
	s.RevokeEverything()
	src := &Source{Vault: v, Server: "mail", Now: func() time.Time { return time.Now().Add(2 * time.Hour) }}
	_, err := src.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tal connect mail") || !strings.Contains(err.Error(), "expired or was revoked") {
		t.Fatalf("%v", err)
	}
	// Never signed in.
	if _, err := (&Source{Vault: v, Server: "other"}).Token(context.Background()); err == nil || !strings.Contains(err.Error(), "tal connect other") {
		t.Fatalf("%v", err)
	}
	// Errors never carry a token.
	if strings.Contains(err.Error(), "at-") || strings.Contains(err.Error(), "rt-") {
		t.Fatal("a token in an error message")
	}
}

func TestRefreshAfterARefusalRenewsOnlyOnce(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	s.RotateRefresh = true
	v := newVault(t)
	signedIn(t, s, v)
	src := &Source{Vault: v, Server: "mail"}
	tok, _ := src.Token(context.Background())
	// Two requests are refused for the same old token at once: both ask to renew.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); src.Refresh(context.Background()) }()
	}
	wg.Wait()
	if s.Refreshes != 1 {
		t.Fatalf("one renewal for one refused token, not %d", s.Refreshes)
	}
	if now, _ := src.Token(context.Background()); now == tok {
		t.Fatal("the token should be a new one")
	}
}

func TestSourceCoalescesConcurrentRenewals(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	s.RotateRefresh = true // a second renewal with the old token would fail
	v := newVault(t)
	signedIn(t, s, v)
	src := &Source{Vault: v, Server: "mail", Now: func() time.Time { return time.Now().Add(61 * time.Minute) }}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := src.Token(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("%v", err)
		}
	}
	if s.Refreshes != 1 {
		t.Fatalf("%d renewals", s.Refreshes)
	}
}

func TestSignInsAreForTheServerTheyWereMadeFor(t *testing.T) {
	v := newVault(t)
	if err := Save(v, "x", &vault.OAuthData{TokenEndpoint: "https://a.example/token", ClientID: "c", AccessToken: "t"}); err == nil {
		t.Fatal("a sign-in that does not name its server")
	}
	if err := Save(v, "x", &vault.OAuthData{TokenEndpoint: "https://a.example/token", ClientID: "c", AccessToken: "t", Resource: "https://mcp.example.com/mcp"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := v.Get("oauth.x"); c.Sites[0] != "https://mcp.example.com" || c.Kind != vault.KindOAuth {
		t.Fatalf("%+v", c)
	}
}

func TestRevoke(t *testing.T) {
	s := oauthtest.New()
	defer s.Close()
	v := newVault(t)
	signedIn(t, s, v)
	c, _ := v.Get("oauth.mail")
	if !Revoke(context.Background(), NewHTTPClient(), c.OAuth) || len(s.Revoked) != 1 || s.Revoked[0] != c.OAuth.RefreshToken {
		t.Fatalf("the refresh token is what is revoked: %v", s.Revoked)
	}
	if Revoke(context.Background(), NewHTTPClient(), &vault.OAuthData{AccessToken: "x"}) || Revoke(context.Background(), NewHTTPClient(), nil) {
		t.Fatal("nothing to revoke at")
	}
}
