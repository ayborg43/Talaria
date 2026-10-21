// Package oauth signs Talaria in to MCP servers that ask for it, the way the
// MCP authorization specification describes: the server says where its
// authorization server is (RFC 9728), the authorization server describes itself
// (RFC 8414), Talaria registers itself if it has to (RFC 7591) and then runs
// the authorization-code flow with PKCE (RFC 7636) through a browser, naming
// the server the tokens are for (RFC 8707). Tokens live in the vault.
package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const maxBody = 1 << 20

// NewHTTPClient is the client used for everything that talks to an
// authorization server. It never follows a redirect: a token request carries
// secrets, and metadata that redirects somewhere else is not to be trusted.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Metadata is what an authorization server says about itself.
type Metadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported       []string `json:"scopes_supported"`
}

// Discovery is everything learned about how to sign in to one server.
type Discovery struct {
	Resource string   // the server, as the tokens will name it
	Server   Metadata // its authorization server
	Scope    string   // what the server asked for when it refused us, if it said
}

var (
	resourceMetadataRE = regexp.MustCompile(`resource_metadata="([^"]+)"`)
	scopeRE            = regexp.MustCompile(`scope="([^"]+)"`)
)

// canonical reduces a URL to what identifies a server: scheme, host, port and
// path, without a trailing slash, query or fragment.
func canonical(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("%q is not a server address", raw)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/"), nil
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isPrivateHost(host string) bool {
	if isLoopback(host) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
}

// checkEndpoint refuses an authorization-server address that would send
// secrets in the clear, or that a public server is using to point Talaria at
// the machine's own network.
func checkEndpoint(what, raw string, serverHost string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("the %s %q is not an address", what, raw)
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(host)):
		return fmt.Errorf("the %s %s is not https, so a sign-in through it would be readable on the way", what, raw)
	case isPrivateHost(host) && !isPrivateHost(serverHost):
		return fmt.Errorf("the %s %s is on a private network, but the server it belongs to is not: refusing to follow it there", what, raw)
	}
	return nil
}

func getJSON(ctx context.Context, hc *http.Client, rawURL string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("%s: unreadable reply: %w", rawURL, err)
	}
	return resp.StatusCode, nil
}

// Discover works out how to sign in to the MCP server at serverURL.
// authServer, if not empty, names the authorization server instead of taking
// the one the server points to.
func Discover(ctx context.Context, hc *http.Client, serverURL, authServer string) (*Discovery, error) {
	resource, err := canonical(serverURL)
	if err != nil {
		return nil, err
	}
	su, _ := url.Parse(resource)
	serverHost := strings.ToLower(su.Hostname())
	d := &Discovery{Resource: resource}

	// A server that wants a sign-in says so when asked for anything without one,
	// and says where to read about it.
	probe, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "talaria", "version": "0.1.0"}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, bytes.NewReader(probe))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the server: %w", err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	challenge := strings.Join(resp.Header.Values("WWW-Authenticate"), " ")
	if resp.StatusCode != http.StatusUnauthorized && !resourceMetadataRE.MatchString(challenge) {
		return nil, fmt.Errorf("the server did not ask for a sign-in (it answered HTTP %d): it may not need one, or use headers instead", resp.StatusCode)
	}
	if m := scopeRE.FindStringSubmatch(challenge); m != nil {
		d.Scope = m[1]
	}

	// What the server says it is.
	var prmURLs []string
	if m := resourceMetadataRE.FindStringSubmatch(challenge); m != nil {
		prmURLs = append(prmURLs, m[1])
	}
	pathPart := strings.TrimPrefix(resource, su.Scheme+"://"+su.Host)
	origin := su.Scheme + "://" + su.Host
	if pathPart != "" {
		prmURLs = append(prmURLs, origin+"/.well-known/oauth-protected-resource"+pathPart)
	}
	prmURLs = append(prmURLs, origin+"/.well-known/oauth-protected-resource")

	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	found := false
	for _, u := range prmURLs {
		if err := checkEndpoint("resource metadata address", u, serverHost); err != nil {
			return nil, err
		}
		status, err := getJSON(ctx, hc, u, &prm)
		if err != nil {
			return nil, err
		}
		if status == http.StatusOK {
			found = true
			break
		}
	}
	if !found && authServer == "" {
		return nil, errors.New("the server did not say which authorization server to use: name one with oauth.authorization_server in the config")
	}
	if found {
		got, err := canonical(prm.Resource)
		if err != nil || got != resource { // a server must name itself, or its metadata could be a different server's
			return nil, fmt.Errorf("the server's metadata is for %q, not %q: refusing to sign in on that basis", prm.Resource, resource)
		}
	}
	issuer := authServer
	if issuer == "" {
		if len(prm.AuthorizationServers) == 0 {
			return nil, errors.New("the server's metadata names no authorization server")
		}
		issuer = prm.AuthorizationServers[0]
	}

	md, err := fetchMetadata(ctx, hc, issuer, serverHost)
	if err != nil {
		return nil, err
	}
	d.Server = *md
	return d, nil
}

// fetchMetadata reads an authorization server's description from where RFC 8414
// and OpenID Connect put it, and checks it is for that server and fit to use.
func fetchMetadata(ctx context.Context, hc *http.Client, issuer, serverHost string) (*Metadata, error) {
	if err := checkEndpoint("authorization server", issuer, serverHost); err != nil {
		return nil, err
	}
	iss, err := canonical(issuer)
	if err != nil {
		return nil, err
	}
	iu, _ := url.Parse(iss)
	origin := iu.Scheme + "://" + iu.Host
	path := strings.TrimPrefix(iss, origin)
	candidates := []string{origin + "/.well-known/oauth-authorization-server" + path}
	if path != "" {
		candidates = append(candidates, origin+"/.well-known/openid-configuration"+path, iss+"/.well-known/openid-configuration")
	} else {
		candidates = append(candidates, origin+"/.well-known/openid-configuration")
	}

	var md Metadata
	ok := false
	for _, c := range candidates {
		md = Metadata{}
		status, err := getJSON(ctx, hc, c, &md)
		if err != nil {
			return nil, err
		}
		if status == http.StatusOK {
			ok = true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("the authorization server %s did not describe itself (tried %s)", issuer, strings.Join(candidates, ", "))
	}
	if got, err := canonical(md.Issuer); err != nil || got != iss { // RFC 8414: the issuer must be the address it was read from
		return nil, fmt.Errorf("the authorization server described itself as %q, not %q: refusing, as it may be another server's description", md.Issuer, issuer)
	}
	if md.AuthorizationEndpoint == "" || md.TokenEndpoint == "" {
		return nil, errors.New("the authorization server does not say where to sign in or where to get tokens")
	}
	for what, u := range map[string]string{"authorization endpoint": md.AuthorizationEndpoint, "token endpoint": md.TokenEndpoint,
		"registration endpoint": md.RegistrationEndpoint, "revocation endpoint": md.RevocationEndpoint} {
		if u == "" {
			continue
		}
		if err := checkEndpoint(what, u, serverHost); err != nil {
			return nil, err
		}
	}
	if !slices.Contains(md.CodeChallengeMethods, "S256") {
		return nil, errors.New("the authorization server does not advertise PKCE (S256), so Talaria will not sign in through it: without PKCE a stolen authorization code could be used by anyone")
	}
	return &md, nil
}
