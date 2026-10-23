package oauth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"talaria/internal/vault"
)

// Client is who Talaria is to an authorization server: given in the config, or
// registered at the first sign-in.
type Client struct {
	ID, Secret string
}

// Config is what the config file may say about a server's sign-in.
type Config struct {
	ClientID, ClientSecret string
	Scopes                 []string
	RedirectPort           int    // 0 picks a free port; some servers only accept the one registered
	AuthServer             string // overrides the authorization server the MCP server points to
}

// Login is one interactive sign-in.
type Login struct {
	HC        *http.Client
	ServerURL string
	Config    Config
	// Out gets the instructions and the address to visit.
	Out io.Writer
	// Open tries to open the address in a browser; nil prints it only.
	Open func(url string) error
	// Paste returns an address the user pasted: the page their browser ended on,
	// for when it could not reach the local callback (Talaria on another machine).
	// It may be nil.
	Paste func(ctx context.Context) (string, error)
	// Timeout is how long to wait for the user; default 5 minutes.
	Timeout time.Duration
}

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func pkce() (verifier, challenge string) {
	verifier = randomString(48) // 64 characters, within RFC 7636's 43 to 128
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// OpenBrowser asks the system to open an address.
func OpenBrowser(addr string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", addr).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", addr).Start()
	}
	return exec.Command("xdg-open", addr).Start()
}

// Run signs in and returns what to keep: the tokens, and what is needed to renew them.
func (l Login) Run(ctx context.Context) (*vault.OAuthData, error) {
	hc := l.HC
	if hc == nil {
		hc = NewHTTPClient()
	}
	disc, err := Discover(ctx, hc, l.ServerURL, l.Config.AuthServer)
	if err != nil {
		return nil, err
	}
	md := disc.Server

	// Where the browser is sent back to: this machine, on a port of its own.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", l.Config.RedirectPort))
	if err != nil {
		return nil, fmt.Errorf("cannot listen for the sign-in to come back: %w", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	scopes := l.Config.Scopes
	scope := strings.Join(scopes, " ")
	if scope == "" {
		scope = disc.Scope
	}

	client := Client{ID: l.Config.ClientID, Secret: l.Config.ClientSecret}
	if client.ID == "" {
		if md.RegistrationEndpoint == "" {
			return nil, errors.New("the authorization server does not let clients register themselves: register Talaria with it yourself (redirect address http://127.0.0.1:PORT/callback), then give oauth.client_id and oauth.redirect_port in the config")
		}
		if client, err = Register(ctx, hc, md, redirect, scope); err != nil {
			return nil, err
		}
	}

	verifier, challenge := pkce()
	state := randomString(24)
	q := url.Values{
		"response_type": {"code"}, "client_id": {client.ID}, "redirect_uri": {redirect},
		"state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"resource": {disc.Resource},
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	authURL, err := withQuery(md.AuthorizationEndpoint, q)
	if err != nil {
		return nil, err
	}

	// The page the browser ends up on carries the code.
	got := make(chan callback, 2)
	// Only an answer that carries the state Talaria made is heard. Anything else, such as a web
	// page probing this port, is turned away and the wait goes on: it must not be able to end the sign-in.
	ours := func(cb callback) bool { return subtle.ConstantTimeCompare([]byte(cb.state), []byte(state)) == 1 }
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		cb := parseCallback(r.URL.Query())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !ours(cb) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, "<h1>Not a sign-in Talaria started</h1>")
			return
		}
		if cb.err != "" {
			fmt.Fprintf(w, "<h1>Sign-in failed</h1><p>%s</p><p>You can close this tab.</p>", html.EscapeString(cb.err))
		} else {
			io.WriteString(w, "<h1>Signed in</h1><p>You can close this tab and go back to Talaria.</p>")
		}
		select {
		case got <- cb:
		default:
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	out := l.Out
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintf(out, "Sign in by opening this address in your browser:\n\n  %s\n\n", authURL)
	if l.Open != nil {
		if err := l.Open(authURL); err != nil {
			fmt.Fprintf(out, "(could not open a browser: %v)\n", err)
		}
	}
	if l.Paste != nil {
		fmt.Fprintln(out, "If the browser cannot reach this machine, paste the address it ends up on here, and press Enter.")
		go func() {
			if s, err := l.Paste(ctx); err == nil && s != "" {
				u, perr := url.Parse(strings.TrimSpace(s))
				if perr != nil {
					return
				}
				if cb := parseCallback(u.Query()); ours(cb) {
					select {
					case got <- cb:
					default:
					}
				}
			}
		}()
	}

	timeout := l.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	var cb callback
	select {
	case cb = <-got:
	case <-time.After(timeout):
		return nil, fmt.Errorf("no sign-in came back within %s", timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	switch {
	case cb.err != "":
		return nil, fmt.Errorf("the server refused the sign-in: %s", cb.err)
	case cb.code == "":
		return nil, errors.New("the sign-in came back without a code")
	}

	tok, err := exchange(ctx, hc, md, client, url.Values{
		"grant_type": {"authorization_code"}, "code": {cb.code}, "redirect_uri": {redirect},
		"code_verifier": {verifier}, "resource": {disc.Resource},
	})
	if err != nil {
		return nil, err
	}
	data := &vault.OAuthData{
		Issuer: md.Issuer, TokenEndpoint: md.TokenEndpoint, RevocationEndpoint: md.RevocationEndpoint,
		ClientID: client.ID, ClientSecret: client.Secret, TokenAuthMethods: md.TokenAuthMethods, Resource: disc.Resource,
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, Expiry: tok.expiry(time.Now()),
	}
	if tok.Scope != "" {
		data.Scopes = strings.Fields(tok.Scope)
	} else if scope != "" {
		data.Scopes = strings.Fields(scope)
	}
	return data, nil
}

type callback struct {
	code, state, err string
}

func parseCallback(q url.Values) callback {
	cb := callback{code: q.Get("code"), state: q.Get("state")}
	if e := q.Get("error"); e != "" {
		cb.err = e
		if d := q.Get("error_description"); d != "" {
			cb.err += ": " + d
		}
	}
	return cb
}

func withQuery(base string, extra url.Values) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range extra {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Register makes Talaria known to an authorization server that allows it
// (RFC 7591): a public client, which has no secret to keep.
func Register(ctx context.Context, hc *http.Client, md Metadata, redirectURI, scope string) (Client, error) {
	body := map[string]any{
		"client_name": "Talaria", "redirect_uris": []string{redirectURI},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	if scope != "" {
		body["scope"] = scope
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.RegistrationEndpoint, bytes.NewReader(raw))
	if err != nil {
		return Client{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return Client{}, fmt.Errorf("registering with the authorization server: %w", err)
	}
	defer resp.Body.Close()
	var r struct {
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		RedirectURIs []string `json:"redirect_uris"`
		Error        string   `json:"error"`
		Description  string   `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&r); err != nil && resp.StatusCode/100 == 2 {
		return Client{}, fmt.Errorf("registration reply unreadable: %w", err)
	}
	if resp.StatusCode/100 != 2 || r.ClientID == "" {
		return Client{}, fmt.Errorf("the authorization server would not register Talaria (HTTP %d %s %s)", resp.StatusCode, r.Error, r.Description)
	}
	if len(r.RedirectURIs) > 0 && !slices.Contains(r.RedirectURIs, redirectURI) {
		return Client{}, errors.New("the authorization server registered a different redirect address than the one asked for")
	}
	return Client{ID: r.ClientID, Secret: r.ClientSecret}, nil
}

// tokenResponse is what a token endpoint returns.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func (t tokenResponse) expiry(now time.Time) time.Time {
	if t.ExpiresIn <= 0 {
		return time.Time{} // not told: renewed when the server refuses the token
	}
	return now.Add(time.Duration(t.ExpiresIn) * time.Second)
}

// ErrSignedOut means the sign-in can no longer be renewed: the user must sign in again.
var ErrSignedOut = errors.New("the sign-in has expired or was revoked")

// exchange asks the token endpoint for tokens, with the form given plus who we are.
func exchange(ctx context.Context, hc *http.Client, md Metadata, c Client, form url.Values) (*tokenResponse, error) {
	basic := c.Secret != "" && slices.Contains(md.TokenAuthMethods, "client_secret_basic") && !slices.Contains(md.TokenAuthMethods, "client_secret_post")
	if !basic {
		form.Set("client_id", c.ID)
		if c.Secret != "" {
			form.Set("client_secret", c.Secret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(c.ID), url.QueryEscape(c.Secret))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("asking for tokens: %w", err)
	}
	defer resp.Body.Close()
	var t tokenResponse
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	jsonErr := json.Unmarshal(raw, &t)
	if resp.StatusCode/100 != 2 {
		if t.Error == "invalid_grant" {
			return nil, ErrSignedOut
		}
		// Only the server's own error code and description: never anything we sent.
		return nil, fmt.Errorf("the token endpoint refused (HTTP %d %s %s)", resp.StatusCode, t.Error, t.Description)
	}
	if jsonErr != nil || t.AccessToken == "" {
		return nil, errors.New("the token endpoint's reply had no access token")
	}
	if t.TokenType != "" && !strings.EqualFold(t.TokenType, "bearer") {
		return nil, fmt.Errorf("the token endpoint issued a %q token, which Talaria cannot use", t.TokenType)
	}
	return &t, nil
}

// Stdin reads one line a user types, for Login.Paste.
func Stdin(ctx context.Context) (string, error) {
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := bufio.NewReader(os.Stdin).ReadString('\n')
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		return strings.TrimSpace(r.s), r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
