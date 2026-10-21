package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"talaria/internal/vault"
)

// ErrNotSignedIn means there is no sign-in for the server yet.
var ErrNotSignedIn = errors.New("not signed in")

// CredentialName is what a server's sign-in is called in the vault.
func CredentialName(server string) string { return "oauth." + server }

// renewBefore is how long before a token expires it is renewed, so a request
// never goes out with one that dies on the way.
const renewBefore = time.Minute

// Source hands out a server's access token, renewing it when it is old and
// keeping the renewed one in the vault. It is safe for concurrent use.
type Source struct {
	Vault  *vault.Vault
	Server string // the MCP server's name in the config
	HC     *http.Client
	Now    func() time.Time // defaults to time.Now; tests override

	mu       sync.Mutex
	lastSent string // the token last handed out, so a refusal of it can be told from a refusal of a newer one
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) hc() *http.Client {
	if s.HC != nil {
		return s.HC
	}
	return NewHTTPClient()
}

func (s *Source) name() string { return CredentialName(s.Server) }

func (s *Source) get() (vault.Credential, error) {
	c, ok := s.Vault.Get(s.name())
	if !ok || c.OAuth == nil {
		return vault.Credential{}, fmt.Errorf("%w: run `tal connect %s`", ErrNotSignedIn, s.Server)
	}
	return c, nil
}

// Token returns an access token to send, renewing it first if it is about to expire.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.get()
	if err != nil {
		return "", err
	}
	o := c.OAuth
	stale := o.AccessToken == "" || (!o.Expiry.IsZero() && !s.now().Add(renewBefore).Before(o.Expiry))
	if stale {
		if o.RefreshToken == "" {
			return "", fmt.Errorf("%w: run `tal connect %s`", ErrSignedOut, s.Server)
		}
		if c, err = s.refreshLocked(ctx, c); err != nil {
			return "", err
		}
	}
	s.lastSent = c.OAuth.AccessToken
	return c.OAuth.AccessToken, nil
}

// Refresh renews the token because the server refused the last one handed out.
// If something else has already renewed it, there is nothing to do.
func (s *Source) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.get()
	if err != nil {
		return err
	}
	if s.lastSent != "" && c.OAuth.AccessToken != s.lastSent { // renewed since, perhaps by another process
		return nil
	}
	if c.OAuth.RefreshToken == "" {
		return fmt.Errorf("%w: run `tal connect %s`", ErrSignedOut, s.Server)
	}
	_, err = s.refreshLocked(ctx, c)
	return err
}

func (s *Source) refreshLocked(ctx context.Context, c vault.Credential) (vault.Credential, error) {
	o := c.OAuth
	md := Metadata{TokenEndpoint: o.TokenEndpoint}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {o.RefreshToken}}
	if o.Resource != "" {
		form.Set("resource", o.Resource)
	}
	tok, err := exchange(ctx, s.hc(), md, Client{ID: o.ClientID, Secret: o.ClientSecret}, form)
	if err != nil {
		if errors.Is(err, ErrSignedOut) {
			return c, fmt.Errorf("%w: run `tal connect %s`", ErrSignedOut, s.Server)
		}
		return c, err
	}
	now := s.now()
	updated, err := s.Vault.Update(s.name(), func(c *vault.Credential) error {
		c.OAuth.AccessToken = tok.AccessToken
		c.OAuth.Expiry = tok.expiry(now)
		if tok.RefreshToken != "" { // servers that rotate them send a new one; the old one is then dead
			c.OAuth.RefreshToken = tok.RefreshToken
		}
		if tok.Scope != "" {
			c.OAuth.Scopes = strings.Fields(tok.Scope)
		}
		return nil
	})
	if err != nil {
		return c, fmt.Errorf("the token was renewed but could not be saved: %w", err)
	}
	return updated, nil
}

// Save stores a new sign-in for a server, replacing any it had.
func Save(v *vault.Vault, server string, data *vault.OAuthData) error {
	if data.Resource == "" {
		return errors.New("the sign-in does not say which server it is for")
	}
	u, err := url.Parse(data.Resource)
	if err != nil || u.Host == "" {
		return errors.New("the sign-in names no server")
	}
	return v.Add(vault.Credential{Name: CredentialName(server), Kind: vault.KindOAuth, Sites: []string{u.Scheme + "://" + u.Host}, OAuth: data})
}

// Revoke tells the authorization server to forget the sign-in's refresh token,
// as far as it supports that (RFC 7009), and reports whether it did. Failing
// to is not an error: the sign-in is removed from the vault either way.
func Revoke(ctx context.Context, hc *http.Client, o *vault.OAuthData) bool {
	if o == nil || o.RevocationEndpoint == "" || (o.RefreshToken == "" && o.AccessToken == "") {
		return false
	}
	token, hint := o.RefreshToken, "refresh_token"
	if token == "" {
		token, hint = o.AccessToken, "access_token"
	}
	form := url.Values{"token": {token}, "token_type_hint": {hint}, "client_id": {o.ClientID}}
	if o.ClientSecret != "" {
		form.Set("client_secret", o.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode/100 == 2
}
