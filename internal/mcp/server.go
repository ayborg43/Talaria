package mcp

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Server describes one MCP server in the config file. Give it either command
// (run as a subprocess, talking over stdio) or url (Streamable HTTP).
//
// Values in args, env, headers and url may contain ${NAME}, replaced from the
// environment when Talaria starts, so secrets can stay out of the file.
type Server struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"` // added to a minimal environment, not the full one
	Cwd     string            `json:"cwd,omitempty"`

	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// OAuth, if present (an empty object is enough), says the server wants an OAuth
	// sign-in instead of fixed headers. Sign in once with `tal connect NAME`.
	OAuth *OAuthConfig `json:"oauth,omitempty"`

	Disabled bool `json:"disabled,omitempty"`
	// Trusted skips the approval prompt for every tool of this server;
	// AutoApprove does so for the named tools only. By default every call asks.
	Trusted     bool     `json:"trusted,omitempty"`
	AutoApprove []string `json:"auto_approve,omitempty"`
	// Permissions says which of the server's tools may run without asking:
	// "ask" (the default) none; "reads" the ones that only read; "reads_and_drafts"
	// those and the ones that only prepare something to be sent. A tool that
	// sends, posts, deletes or the like always asks, whatever this says, and
	// cannot be made to stop asking with the Always button.
	Permissions string `json:"permissions,omitempty"`
	// TimeoutSeconds limits one tool call (default 120).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// OAuthConfig is what the config file says about a server's OAuth sign-in.
// Everything is optional: with none of it, Talaria finds the authorization
// server from the MCP server and registers itself there.
type OAuthConfig struct {
	ClientID string `json:"client_id,omitempty"`
	// ClientSecretEnv names the environment variable holding the client's secret,
	// for an authorization server that gave Talaria one. The secret itself is
	// then kept in the vault.
	ClientSecretEnv string   `json:"client_secret_env,omitempty"`
	Scopes          []string `json:"scopes,omitempty"`
	// RedirectPort is the local port the sign-in comes back to, for an
	// authorization server that only accepts the one address registered with it.
	RedirectPort int `json:"redirect_port,omitempty"`
	// AuthorizationServer names the authorization server when the MCP server
	// does not say.
	AuthorizationServer string `json:"authorization_server,omitempty"`
}

// Permission levels for Server.Permissions.
const (
	PermAsk           = "ask"
	PermReads         = "reads"
	PermReadsAndDraft = "reads_and_drafts"
)

const defaultCallTimeout = 120 * time.Second

var (
	serverNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	varRE        = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	secretKeyRE  = regexp.MustCompile(`(?i)auth|key|token|secret|password|credential`)
)

func (s Server) callTimeout() time.Duration {
	if s.TimeoutSeconds > 0 {
		return time.Duration(s.TimeoutSeconds) * time.Second
	}
	return defaultCallTimeout
}

func (s Server) isHTTP() bool { return s.URL != "" }

// approves reports whether a call to the tool runs without asking: the user said
// so for this server or this tool, or the server's permissions level frees tools
// of its class.
func (s Server) approves(tool string, class Class) bool {
	if s.Trusted {
		return true
	}
	for _, t := range s.AutoApprove {
		if t == tool {
			return true
		}
	}
	switch s.Permissions {
	case PermReads:
		return class == ClassRead
	case PermReadsAndDraft:
		return class == ClassRead || class == ClassDraft
	}
	return false
}

// Validate checks a server entry. name is its key in the config.
func (s Server) Validate(name string) error {
	if !serverNameRE.MatchString(name) {
		return fmt.Errorf("mcp server name %q: use letters, digits, hyphens and underscores (max 32)", name)
	}
	switch {
	case s.Command != "" && s.URL != "":
		return fmt.Errorf("mcp server %q: give command or url, not both", name)
	case s.Command == "" && s.URL == "":
		return fmt.Errorf("mcp server %q: give a command (stdio) or a url (HTTP)", name)
	case s.URL != "" && (len(s.Args) > 0 || len(s.Env) > 0 || s.Cwd != ""):
		return fmt.Errorf("mcp server %q: args, env and cwd only apply to a command", name)
	case s.Command != "" && len(s.Headers) > 0:
		return fmt.Errorf("mcp server %q: headers only apply to a url", name)
	case s.TimeoutSeconds < 0:
		return fmt.Errorf("mcp server %q: timeout_seconds cannot be negative", name)
	case s.OAuth != nil && s.Command != "":
		return fmt.Errorf("mcp server %q: oauth only applies to a url", name)
	case s.OAuth != nil && len(s.Headers) > 0:
		return fmt.Errorf("mcp server %q: give oauth or headers, not both: a sign-in sets the Authorization header itself", name)
	case s.OAuth != nil && (s.OAuth.RedirectPort < 0 || s.OAuth.RedirectPort > 65535):
		return fmt.Errorf("mcp server %q: oauth.redirect_port is not a port", name)
	case s.OAuth != nil && s.OAuth.ClientSecretEnv != "" && s.OAuth.ClientID == "":
		return fmt.Errorf("mcp server %q: oauth.client_secret_env needs oauth.client_id", name)
	}
	switch s.Permissions {
	case "", PermAsk, PermReads, PermReadsAndDraft:
	default:
		return fmt.Errorf("mcp server %q: permissions must be %q, %q or %q, not %q", name, PermAsk, PermReads, PermReadsAndDraft, s.Permissions)
	}
	if s.URL != "" {
		// Do not expand yet: ${VAR} may be part of the URL.
		probe := varRE.ReplaceAllString(s.URL, "x")
		u, err := url.Parse(probe)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("mcp server %q: url must be an http:// or https:// address", name)
		}
	}
	return nil
}

// Warnings lists risky-but-valid settings.
func (s Server) Warnings(name string) []string {
	var w []string
	if u, err := url.Parse(varRE.ReplaceAllString(s.URL, "x")); err == nil && u.Scheme == "http" && !isLoopback(u.Hostname()) && len(s.Headers) > 0 {
		w = append(w, fmt.Sprintf("mcp server %q sends headers over plain http to %s: use https", name, u.Hostname()))
	}
	return w
}

// HasInlineSecret reports whether a secret-looking header or env value is
// written literally instead of as ${VAR}.
func (s Server) HasInlineSecret() bool {
	check := func(m map[string]string) bool {
		for k, v := range m {
			if secretKeyRE.MatchString(k) && v != "" && !varRE.MatchString(v) {
				return true
			}
		}
		return false
	}
	return check(s.Env) || check(s.Headers)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// expand returns a copy with every ${NAME} replaced. An unset variable is an
// error: a silently empty token would only fail later and more confusingly.
func (s Server) expand(getenv func(string) string) (Server, error) {
	sub := func(in string) (string, error) {
		var missing string
		out := varRE.ReplaceAllStringFunc(in, func(m string) string {
			v := getenv(m[2 : len(m)-1])
			if v == "" && missing == "" {
				missing = m[2 : len(m)-1]
			}
			return v
		})
		if missing != "" {
			return "", fmt.Errorf("environment variable %s is not set", missing)
		}
		return out, nil
	}

	out := s
	var err error
	if out.URL, err = sub(s.URL); err != nil {
		return Server{}, err
	}
	out.Args = make([]string, len(s.Args))
	for i, a := range s.Args {
		if out.Args[i], err = sub(a); err != nil {
			return Server{}, err
		}
	}
	out.Env = make(map[string]string, len(s.Env))
	for k, v := range s.Env {
		if out.Env[k], err = sub(v); err != nil {
			return Server{}, fmt.Errorf("env %s: %w", k, err)
		}
	}
	out.Headers = make(map[string]string, len(s.Headers))
	for k, v := range s.Headers {
		if out.Headers[k], err = sub(v); err != nil {
			return Server{}, fmt.Errorf("header %s: %w", k, err)
		}
	}
	return out, nil
}

// safeEnvNames are the only variables a subprocess server inherits. Passing
// the whole environment would hand every API key Talaria can see (OPENAI_API_KEY,
// the Telegram token, ...) to third-party code; a server that needs a secret
// gets it through its own env block.
var safeEnvNames = []string{
	"HOME", "LOGNAME", "PATH", "SHELL", "TERM", "USER", "LANG", "LC_ALL", "TMPDIR", "TZ",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
}

var windowsEnvNames = []string{
	"APPDATA", "HOMEDRIVE", "HOMEPATH", "LOCALAPPDATA", "PATHEXT", "PROCESSOR_ARCHITECTURE",
	"SYSTEMDRIVE", "SYSTEMROOT", "TEMP", "TMP", "USERNAME", "USERPROFILE", "PROGRAMFILES",
}

func (s Server) environment(getenv func(string) string) []string {
	names := safeEnvNames
	if runtime.GOOS == "windows" {
		names = append(append([]string{}, names...), windowsEnvNames...)
	}
	var env []string
	for _, n := range names {
		if v := getenv(n); v != "" {
			env = append(env, n+"="+v)
		}
	}
	for k, v := range s.Env { // later entries win
		env = append(env, k+"="+v)
	}
	return env
}

// label describes the transport for display; it never includes headers or a query string.
func (s Server) label() string {
	if s.isHTTP() {
		if u, err := url.Parse(s.URL); err == nil {
			return "http " + u.Scheme + "://" + u.Host + u.Path
		}
		return "http"
	}
	return "stdio " + strings.Join(append([]string{s.Command}, s.Args...), " ")
}
