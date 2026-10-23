// Package oauthtest is a fake OAuth-protected MCP server for tests: one HTTP
// server that is at once the MCP server, its authorization server and the
// place a user "approves" a sign-in. It checks what a real one checks (PKCE,
// redirect addresses, one-use codes, the resource named, token expiry and
// rotation), and records what it was asked.
package oauthtest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Tool is a tool the fake MCP server offers.
type Tool struct {
	Name        string
	Description string
	Annotations map[string]any
	Run         func(args map[string]any) string
	// Fails, if set and true for a call, makes the call come back flagged as an error.
	Fails func(args map[string]any) bool
}

// Server is the fake. Set its options before the first request.
type Server struct {
	*httptest.Server

	// Options.
	AccessTTL          time.Duration // how long access tokens live; default an hour
	RotateRefresh      bool          // issue a new refresh token at each refresh, killing the old
	NoPKCE             bool          // do not advertise PKCE
	NoRegistration     bool          // no dynamic client registration
	WrongIssuer        bool          // describe itself with an issuer that is not its address
	WrongResource      bool          // protected-resource metadata naming some other server
	NoMetadata         bool          // no protected-resource metadata at all
	Deny               bool          // the user declines at the authorization page
	PrivateTokenHost   string        // advertise a token endpoint at this address instead
	ConfidentialID     string        // if set, only this client, with ConfidentialSecret, may use the token endpoint
	ConfidentialSecret string
	BasicAuthOnly      bool // advertise and require client_secret_basic: the secret in the header, never the body
	Tools              []Tool

	mu            sync.Mutex
	clients       map[string]client
	codes         map[string]codeInfo
	access        map[string]time.Time // valid access tokens and their expiry
	refresh       map[string]string    // valid refresh tokens -> client id
	Log           []string             // every request, as "METHOD /path"
	Calls         []Call               // tool calls that were served
	Registrations int
	Refreshes     int
	Revoked       []string
	TokenForms    []url.Values // the form of every token request
	BasicAuth     []string     // client ids sent in an Authorization: Basic header
}

// Call is a tool call the server served.
type Call struct {
	Tool string
	Args map[string]any
	Auth string // the access token it came with
}

type client struct {
	redirect string
	secret   string
}

type codeInfo struct {
	clientID, redirect, challenge, resource string
}

func random(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// New starts the server.
func New() *Server {
	s := &Server{clients: map[string]client{}, codes: map[string]codeInfo{}, access: map[string]time.Time{}, refresh: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.mcp)
	mux.HandleFunc("/.well-known/oauth-protected-resource", s.prm)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.prm)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("/register", s.register)
	mux.HandleFunc("/authorize", s.authorize)
	mux.HandleFunc("/token", s.token)
	mux.HandleFunc("/revoke", s.revoke)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.Log = append(s.Log, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	return s
}

// MCPURL is the address of the MCP endpoint.
func (s *Server) MCPURL() string { return s.URL + "/mcp" }

func (s *Server) ttl() time.Duration {
	if s.AccessTTL > 0 {
		return s.AccessTTL
	}
	return time.Hour
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) prm(w http.ResponseWriter, r *http.Request) {
	if s.NoMetadata {
		http.NotFound(w, r)
		return
	}
	resource := s.MCPURL()
	if s.WrongResource {
		resource = "https://some-other-server.example/mcp"
	}
	writeJSON(w, 200, map[string]any{"resource": resource, "authorization_servers": []string{s.URL}})
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	issuer := s.URL
	if s.WrongIssuer {
		issuer = "https://evil.example"
	}
	token := s.URL + "/token"
	if s.PrivateTokenHost != "" {
		token = s.PrivateTokenHost + "/token"
	}
	md := map[string]any{
		"issuer": issuer, "authorization_endpoint": s.URL + "/authorize", "token_endpoint": token,
		"revocation_endpoint": s.URL + "/revoke", "token_endpoint_auth_methods_supported": []string{"none", "client_secret_post"},
	}
	if s.BasicAuthOnly {
		md["token_endpoint_auth_methods_supported"] = []string{"client_secret_basic"}
	}
	if !s.NoPKCE {
		md["code_challenge_methods_supported"] = []string{"S256"}
	}
	if !s.NoRegistration {
		md["registration_endpoint"] = s.URL + "/register"
	}
	writeJSON(w, 200, md)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	if len(req.RedirectURIs) != 1 || !strings.HasPrefix(req.RedirectURIs[0], "http://127.0.0.1:") {
		writeJSON(w, 400, map[string]any{"error": "invalid_redirect_uri"})
		return
	}
	id := "client-" + random(4)
	s.mu.Lock()
	s.clients[id] = client{redirect: req.RedirectURIs[0]}
	s.Registrations++
	s.mu.Unlock()
	writeJSON(w, 201, map[string]any{"client_id": id, "redirect_uris": req.RedirectURIs})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	c, known := s.clients[q.Get("client_id")]
	if q.Get("client_id") == s.ConfidentialID && s.ConfidentialID != "" {
		c, known = client{redirect: q.Get("redirect_uri")}, true
	}
	s.mu.Unlock()
	switch {
	case !known:
		http.Error(w, "unknown client", 400)
		return
	case q.Get("redirect_uri") != c.redirect:
		http.Error(w, "redirect_uri does not match the registered one", 400)
		return
	case q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		http.Error(w, "PKCE with S256 is required", 400)
		return
	case q.Get("resource") != s.MCPURL():
		http.Error(w, "the resource must name the MCP server", 400)
		return
	}
	back, _ := url.Parse(c.redirect)
	bq := back.Query()
	bq.Set("state", q.Get("state"))
	if s.Deny {
		bq.Set("error", "access_denied")
		bq.Set("error_description", "the user said no")
	} else {
		code := "code-" + random(8)
		s.mu.Lock()
		s.codes[code] = codeInfo{clientID: q.Get("client_id"), redirect: c.redirect, challenge: q.Get("code_challenge"), resource: q.Get("resource")}
		s.mu.Unlock()
		bq.Set("code", code)
	}
	back.RawQuery = bq.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (s *Server) issue(clientID string) map[string]any {
	access := "at-" + random(12)
	refresh := "rt-" + random(12)
	s.mu.Lock()
	s.access[access] = time.Now().Add(s.ttl())
	s.refresh[refresh] = clientID
	s.mu.Unlock()
	return map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(s.ttl().Seconds()), "refresh_token": refresh, "scope": "calendar mail"}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	s.mu.Lock()
	s.TokenForms = append(s.TokenForms, r.PostForm)
	s.mu.Unlock()
	clientID, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	if u, p, ok := r.BasicAuth(); ok {
		clientID, _ = url.QueryUnescape(u)
		secret, _ = url.QueryUnescape(p)
		s.mu.Lock()
		s.BasicAuth = append(s.BasicAuth, clientID)
		s.mu.Unlock()
	}
	if s.ConfidentialID != "" && (clientID != s.ConfidentialID || secret != s.ConfidentialSecret) {
		writeJSON(w, 401, map[string]any{"error": "invalid_client"})
		return
	}
	if s.BasicAuthOnly && r.PostForm.Get("client_secret") != "" {
		writeJSON(w, 401, map[string]any{"error": "invalid_client"})
		return
	}
	bad := func(code string) { writeJSON(w, 400, map[string]any{"error": code, "error_description": "no"}) }
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.mu.Lock()
		info, ok := s.codes[r.PostForm.Get("code")]
		delete(s.codes, r.PostForm.Get("code")) // a code works once
		s.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		switch {
		case !ok || info.clientID != clientID:
			bad("invalid_grant")
		case info.redirect != r.PostForm.Get("redirect_uri"):
			bad("invalid_grant")
		case base64.RawURLEncoding.EncodeToString(sum[:]) != info.challenge:
			bad("invalid_grant") // PKCE: the verifier does not match the challenge
		case r.PostForm.Get("resource") != info.resource:
			bad("invalid_target")
		default:
			writeJSON(w, 200, s.issue(clientID))
		}
	case "refresh_token":
		rt := r.PostForm.Get("refresh_token")
		s.mu.Lock()
		owner, ok := s.refresh[rt]
		if ok && s.RotateRefresh {
			delete(s.refresh, rt)
		}
		s.Refreshes++
		s.mu.Unlock()
		if !ok || owner != clientID {
			bad("invalid_grant")
			return
		}
		resp := s.issue(clientID)
		if !s.RotateRefresh {
			s.mu.Lock()
			delete(s.refresh, resp["refresh_token"].(string))
			s.mu.Unlock()
			delete(resp, "refresh_token")
		}
		writeJSON(w, 200, resp)
	default:
		bad("unsupported_grant_type")
	}
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	tok := r.PostForm.Get("token")
	s.mu.Lock()
	s.Revoked = append(s.Revoked, tok)
	delete(s.refresh, tok)
	delete(s.access, tok)
	s.mu.Unlock()
	w.WriteHeader(200)
}

// Expire makes every access token the server has issued stop working.
func (s *Server) Expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for t := range s.access {
		s.access[t] = time.Now().Add(-time.Hour)
	}
}

// RevokeEverything makes all tokens, refresh tokens included, stop working.
func (s *Server) RevokeEverything() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access, s.refresh = map[string]time.Time{}, map[string]string{}
}

// Served returns the tool calls served so far.
func (s *Server) Served() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.Calls...)
}

func (s *Server) challenge(w http.ResponseWriter) {
	hdr := fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", scope="calendar mail"`, s.URL)
	if s.NoMetadata {
		hdr = "Bearer"
	}
	w.Header().Set("WWW-Authenticate", hdr)
	writeJSON(w, 401, map[string]any{"error": "unauthorized"})
}

func (s *Server) authorized(r *http.Request) (string, bool) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.access[tok]
	return tok, ok && time.Now().Before(exp)
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	tok, ok := s.authorized(r)
	if !ok {
		s.challenge(w)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
	reply := func(result any) {
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	switch req.Method {
	case "initialize":
		reply(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake-connector", "version": "1"}})
	case "notifications/initialized":
		w.WriteHeader(202)
	case "tools/list":
		var tools []map[string]any
		for _, t := range s.Tools {
			entry := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": map[string]any{"type": "object"}}
			if t.Annotations != nil {
				entry["annotations"] = t.Annotations
			}
			tools = append(tools, entry)
		}
		reply(map[string]any{"tools": tools})
	case "tools/call":
		for _, t := range s.Tools {
			if t.Name == req.Params.Name {
				s.mu.Lock()
				s.Calls = append(s.Calls, Call{Tool: t.Name, Args: req.Params.Arguments, Auth: tok})
				s.mu.Unlock()
				failed := t.Fails != nil && t.Fails(req.Params.Arguments)
				reply(map[string]any{"isError": failed, "content": []map[string]any{{"type": "text", "text": t.Run(req.Params.Arguments)}}})
				return
			}
		}
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32602, "message": "unknown tool"}})
	default:
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "no such method"}})
	}
}
