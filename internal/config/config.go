// Package config reads Talaria's optional config file and resolves which
// model provider to use.
//
// Precedence, highest first: command-line flags, environment variables, the
// selected profile in the file, built-in defaults. With no config file,
// Talaria behaves as it always has: an OpenAI-compatible API configured by
// environment variables.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"talaria/internal/execenv"
	"talaria/internal/guard"
	"talaria/internal/mcp"
	"talaria/internal/provider"
	"talaria/internal/subagent"
)

// Write saves cfg to path as a JSON file with mode 0600 (replacing any older
// file). It is used by the settings API to keep secrets in the config file.
func Write(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "\t")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Profile is one way to reach a model.
type Profile struct {
	Type      string `json:"type,omitempty"`        // "openai" (default), "anthropic" or "deepseek"
	BaseURL   string `json:"base_url,omitempty"`    // default depends on type
	APIKey    string `json:"api_key,omitempty"`     // prefer api_key_env: keep secrets out of files
	APIKeyEnv string `json:"api_key_env,omitempty"` // name of the environment variable holding the key
	Model     string `json:"model,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"` // anthropic only: per-reply limit
	// Vision says whether the model can see images (screenshots from the
	// browser). On unless set to false; a model that cannot see is told a
	// picture was there and sent text only.
	Vision *bool `json:"vision,omitempty"`
	// Thinking turns DeepSeek's thinking mode on or off (the service's default
	// is on); ReasoningEffort is "low", "high" or "max". DeepSeek profiles only.
	Thinking        *bool  `json:"thinking,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// Telegram holds gateway settings. The TALARIA_TELEGRAM_* environment
// variables override these.
type Telegram struct {
	Token        string  `json:"token,omitempty"`
	TokenEnv     string  `json:"token_env,omitempty"`
	AllowedUsers UserIDs `json:"allowed_users,omitempty"`
	// Stream shows replies as they are written, by editing a message in place.
	// On unless set to false.
	Stream *bool `json:"stream,omitempty"`
	// Environments are the execution environments a chat may switch to with
	// /env, besides the one the gateway starts in. Empty (the default) means
	// chats cannot switch. "local", this machine, is allowed only if listed.
	Environments []string `json:"environments,omitempty"`
}

// StreamReplies reports whether replies are shown as they are written.
func (t Telegram) StreamReplies() bool { return t.Stream == nil || *t.Stream }

// UserIDs is a list of numeric user IDs. In JSON each is a number or, as Discord
// shows them, a string; strings are the safer form, since a long number can be
// rounded by the tools that edit JSON.
type UserIDs []int64

func (u *UserIDs) UnmarshalJSON(raw []byte) error {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return fmt.Errorf("a list of user IDs: %w", err)
	}
	ids := make(UserIDs, 0, len(items))
	for _, it := range items {
		text := strings.Trim(string(it), `"`)
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("%s is not a user ID (a positive whole number)", it)
		}
		ids = append(ids, id)
	}
	*u = ids
	return nil
}

func (u UserIDs) MarshalJSON() ([]byte, error) {
	out := make([]string, len(u))
	for i, id := range u {
		out[i] = strconv.FormatInt(id, 10)
	}
	return json.Marshal(out)
}

// Discord holds the Discord gateway's settings. The TALARIA_DISCORD_*
// environment variables override these.
type Discord struct {
	Token        string  `json:"token,omitempty"`
	TokenEnv     string  `json:"token_env,omitempty"`
	AllowedUsers UserIDs `json:"allowed_users,omitempty"`
	// Stream shows replies as they are written, by editing a message in place.
	// On unless set to false.
	Stream *bool `json:"stream,omitempty"`
	// Environments are the execution environments a chat may switch to with
	// /env, as for Telegram.
	Environments []string `json:"environments,omitempty"`
}

// StreamReplies reports whether replies are shown as they are written.
func (d Discord) StreamReplies() bool { return d.Stream == nil || *d.Stream }

// Calendar is one calendar feed.
type Calendar struct {
	// URLEnv names the environment variable holding the feed's address (https:// or webcal://).
	URLEnv string `json:"url_env"`
	// AllowPrivate lets the feed be on this machine or a private network, for a calendar you host yourself.
	AllowPrivate bool `json:"allow_private,omitempty"`
}

// Ops is the health and metrics endpoint. Off unless Listen is set.
type Ops struct {
	// Listen is host:port, such as "127.0.0.1:9100". An address that is not on this machine needs TokenEnv.
	Listen string `json:"listen,omitempty"`
	// TokenEnv names the environment variable holding a bearer token that /metrics then requires.
	TokenEnv string `json:"token_env,omitempty"`
}

// Limits bound what a runaway client, a loop, or something steering the agent could cost.
type Limits struct {
	// MessagesPerMinute is how fast one chat may give the agent work, a burst of that size included.
	// Default 30; negative removes the limit.
	MessagesPerMinute int `json:"messages_per_minute,omitempty"`
	// ModelCallsPerHour caps requests to the model across everything Talaria runs, jobs and
	// watches included. Default 1000; negative removes the limit.
	ModelCallsPerHour int `json:"model_calls_per_hour,omitempty"`
}

// Messages is the effective messages-per-minute limit; 0 means none.
func (l Limits) Messages() int {
	switch {
	case l.MessagesPerMinute < 0:
		return 0
	case l.MessagesPerMinute == 0:
		return 30
	}
	return l.MessagesPerMinute
}

// ModelCalls is the effective hourly cap on model requests; 0 means none.
func (l Limits) ModelCalls() int {
	switch {
	case l.ModelCallsPerHour < 0:
		return 0
	case l.ModelCallsPerHour == 0:
		return 1000
	}
	return l.ModelCallsPerHour
}

// Policy holds the permission rules and the activity log's settings.
type Policy struct {
	// Rules say what happens when the agent calls a tool: "allow" (run it),
	// "ask" (run it if the user approves) or "deny" (never). The key is a tool's
	// name, or a name ending in * for a family ("mcp_mail_*"). A tool with no
	// rule does what it always did: write_file, shell and the like ask, the
	// rest run. An exact name beats a family, and a longer family beats a shorter.
	Rules map[string]string `json:"rules,omitempty"`
	// NoRemember lists tools the "always allow" button is not offered for.
	// Unset, it is ["shell"]; [] offers the button for every tool without a rule.
	NoRemember *[]string `json:"no_remember,omitempty"`
	// Audit keeps the log of tool calls (audit.log). On unless set to false.
	Audit *bool `json:"audit,omitempty"`
	// UntrustedContent says what happens once a conversation has read something from outside (a
	// web page, a mail, a connected service's reply), which may carry instructions meant for the
	// agent: "ask" (the default) makes anything that could send something out, run a command or
	// leave a note behind ask first, even if it would normally run, except where a rule above says
	// "allow"; "off" does not.
	UntrustedContent string `json:"untrusted_content,omitempty"`
}

// GuardUntrusted reports whether calls that could send something out ask once outside content was read.
func (p Policy) GuardUntrusted() bool { return p.UntrustedContent != "off" }

// AuditEnabled reports whether tool calls are logged.
func (p Policy) AuditEnabled() bool { return p.Audit == nil || *p.Audit }

// Parse validates the rules.
func (p Policy) Parse() (*guard.Policy, error) {
	switch p.UntrustedContent {
	case "", "ask", "off":
	default:
		return nil, fmt.Errorf("untrusted_content must be \"ask\" or \"off\", not %q", p.UntrustedContent)
	}
	var noRemember []string
	if p.NoRemember != nil {
		noRemember = append([]string{}, *p.NoRemember...)
	}
	return guard.ParsePolicy(p.Rules, noRemember)
}

// Browser holds the web browser's settings. The browser tools are on by default
// in the workspace (see `tal workspace`), and off elsewhere unless enabled here.
type Browser struct {
	// Enabled turns the browser tools on or off. Unset, they are on when the
	// environment is the workspace.
	Enabled *bool `json:"enabled,omitempty"`
	// AllowPrivate lets the browser open addresses on this machine and on
	// private networks. Off by default, so a page cannot be used to reach
	// services that trust anything on the network.
	AllowPrivate bool `json:"allow_private,omitempty"`
	// IdleMinutes closes a conversation's tab after this long unused (default 10).
	IdleMinutes int `json:"idle_minutes,omitempty"`
	// Binary is the browser to run, in the environment's own filesystem. Unset,
	// Talaria looks for chromium, chromium-browser or Chrome.
	Binary string `json:"binary,omitempty"`
	// ProfileDir is where the browser keeps cookies and logins, in the
	// environment's filesystem. Unset: /workspace/.browser-profile in the
	// workspace, <home>/browser on this machine, ~/.talaria-browser elsewhere.
	ProfileDir string `json:"profile_dir,omitempty"`
	// NoSandbox turns off Chromium's own sandbox, which cannot run inside most
	// containers. Unset, it is off in any environment but this machine, where
	// the container is the sandbox.
	NoSandbox *bool `json:"no_sandbox,omitempty"`
}

// Vault says where the passphrase for the encrypted vault of saved logins comes
// from when no one is there to type it (a gateway, a scheduled job). The
// TALARIA_VAULT_PASSPHRASE environment variable takes precedence.
type Vault struct {
	// KeyFile is a file holding the passphrase, readable only by you (mode 0600).
	// `tal vault init -keyfile PATH` makes one.
	KeyFile string `json:"key_file,omitempty"`
}

// Workspace holds the settings `tal workspace up` creates the container with.
type Workspace struct {
	Network string  `json:"network,omitempty"` // a Docker network; "none" gives it no network at all
	Memory  string  `json:"memory,omitempty"`  // default 2g
	CPUs    float64 `json:"cpus,omitempty"`
}

type Config struct {
	DefaultProfile string             `json:"default_profile,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"`
	Telegram       Telegram           `json:"telegram,omitzero"`
	Discord        Discord            `json:"discord,omitzero"`
	Policy         Policy             `json:"policy,omitzero"`
	Browser        Browser            `json:"browser,omitzero"`
	Workspace      Workspace          `json:"workspace,omitzero"`
	Vault          Vault              `json:"vault,omitzero"`
	// MCPServers are external tool servers, keyed by a short name.
	MCPServers map[string]mcp.Server `json:"mcp_servers,omitempty"`
	// Environments are places the shell and file tools can run instead of this
	// machine: a Docker container or a remote host over SSH. "local" is built in.
	Environments       map[string]execenv.Spec `json:"environments,omitempty"`
	DefaultEnvironment string                  `json:"default_environment,omitempty"`
	// Subagents bounds the delegate tool: how many run at once and how long each may work.
	Subagents subagent.Limits `json:"subagents,omitzero"`

	Limits Limits `json:"limits,omitzero"`
	// Calendars are read-only calendar feeds the agent can look at, such as the secret iCal address
	// Google Calendar gives each calendar. The address is a secret, so it is kept in an environment
	// variable and the config names it. TALARIA_CALENDARS (name=address pairs) adds more.
	Calendars map[string]Calendar `json:"calendars,omitempty"`
	// Timezone is the zone calendar times are shown in, such as "Africa/Lagos". Default: TALARIA_TIMEZONE, else UTC.
	Timezone string `json:"timezone,omitempty"`
	// Ops serves the gateway's health and metrics for a monitor to poll.
	Ops Ops `json:"ops,omitzero"`
	// ProtectedPaths are places the agent's file tools may not read, list or write,
	// besides Talaria's own state and the usual credential directories (~/.ssh, ~/.aws, ...).
	// "~/" means your home directory. Everything below a path is protected too.
	ProtectedPaths []string `json:"protected_paths,omitempty"`

	// Warnings are problems worth telling the user about that did not stop loading.
	Warnings []string `json:"-"`
}

// Path is where the config file lives: $TALARIA_CONFIG, else <home>/config.json.
func Path(home string) string {
	if p := os.Getenv("TALARIA_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(home, "config.json")
}

// Load reads the file. A missing file is not an error: it yields an empty
// config. Unknown fields are rejected so typos do not silently do nothing.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			c := &Config{}
			c.addBuiltins()
			return c, nil
		}
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.addBuiltins()

	for _, name := range c.EnvironmentNames() {
		spec := c.Environments[name]
		if err := spec.Validate(name); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		c.Warnings = append(c.Warnings, spec.Warnings(name)...)
	}
	if d := c.DefaultEnvironment; d != "" && d != execenv.LocalName {
		if _, ok := c.Environments[d]; !ok {
			return nil, fmt.Errorf("%s: default_environment %q is not defined (environments: %s)", path, d, c.environmentList())
		}
	}
	for _, n := range c.Telegram.Environments {
		if _, defined := c.Environments[n]; n != execenv.LocalName && !defined {
			return nil, fmt.Errorf("%s: telegram.environments lists %q, which is not defined (environments: %s)", path, n, c.environmentList())
		}
	}
	for _, n := range c.Discord.Environments {
		if _, defined := c.Environments[n]; n != execenv.LocalName && !defined {
			return nil, fmt.Errorf("%s: discord.environments lists %q, which is not defined (environments: %s)", path, n, c.environmentList())
		}
	}
	for _, name := range c.ProfileNames() {
		if p := c.Profiles[name]; p.BaseURL != "" {
			if u, err := url.Parse(p.BaseURL); err == nil && u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
				c.Warnings = append(c.Warnings, fmt.Sprintf("profile %q reaches its model over plain http to %s: the key and the whole conversation cross the network readable by anyone on the way. Use https", name, u.Hostname()))
			}
		}
		if p := c.Profiles[name]; (p.Type == "" || p.Type == provider.TypeOpenAI) && strings.Contains(p.BaseURL, "api.deepseek.com") {
			c.Warnings = append(c.Warnings, fmt.Sprintf("profile %q reaches DeepSeek as a plain openai profile, which drops the reasoning DeepSeek needs back once tools are used: set \"type\": \"deepseek\"", name))
		}
	}
	for name, cal := range c.Calendars {
		if !calendarName.MatchString(name) {
			return nil, fmt.Errorf("%s: calendar name %q: use lower-case letters, digits, - and _ (up to 32)", path, name)
		}
		if cal.URLEnv == "" {
			return nil, fmt.Errorf("%s: calendar %q needs url_env, the name of the environment variable holding its address (the address is a secret and is not written in the file)", path, name)
		}
	}
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			return nil, fmt.Errorf("%s: timezone %q is not a time zone name (such as Africa/Lagos)", path, c.Timezone)
		}
	}
	if _, err := c.Policy.Parse(); err != nil {
		return nil, fmt.Errorf("%s: policy: %w", path, err)
	}
	if err := c.Subagents.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, name := range c.MCPServerNames() {
		srv := c.MCPServers[name]
		if err := srv.Validate(name); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		c.Warnings = append(c.Warnings, srv.Warnings(name)...)
	}

	if c.hasInlineSecrets() {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s holds secrets but other users can read it; run: chmod 600 %s", path, path))
		}
	}
	return &c, nil
}

// addBuiltins adds the environments that need no definition: the workspace,
// unless the file defines its own under that name.
func (c *Config) addBuiltins() {
	if _, ok := c.Environments[execenv.WorkspaceName]; ok {
		return
	}
	if c.Environments == nil {
		c.Environments = map[string]execenv.Spec{}
	}
	c.Environments[execenv.WorkspaceName] = execenv.WorkspaceSpec()
}

func (c *Config) hasInlineSecrets() bool {
	if c.Telegram.Token != "" || c.Discord.Token != "" {
		return true
	}
	for _, p := range c.Profiles {
		if p.APIKey != "" {
			return true
		}
	}
	for _, srv := range c.MCPServers {
		if srv.HasInlineSecret() {
			return true
		}
	}
	for _, e := range c.Environments {
		if e.HasInlineSecret() {
			return true
		}
	}
	return false
}

// EnvironmentNames lists the configured environments, sorted.
func (c *Config) EnvironmentNames() []string {
	names := make([]string, 0, len(c.Environments))
	for n := range c.Environments {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *Config) environmentList() string {
	if len(c.Environments) == 0 {
		return "none defined"
	}
	return strings.Join(c.EnvironmentNames(), ", ")
}

// Environment picks where the shell and file tools run: selected (from -env or
// TALARIA_ENV), else default_environment, else this machine. A nil spec means
// this machine.
func (c *Config) Environment(selected string) (name string, spec *execenv.Spec, err error) {
	name = selected
	if name == "" {
		name = c.DefaultEnvironment
	}
	if name == "" || name == execenv.LocalName {
		return execenv.LocalName, nil, nil
	}
	s, ok := c.Environments[name]
	if !ok {
		return "", nil, fmt.Errorf("no environment named %q (environments: %s)", name, c.environmentList())
	}
	return name, &s, nil
}

// MCPServerNames lists the configured MCP servers, sorted.
func (c *Config) MCPServerNames() []string {
	names := make([]string, 0, len(c.MCPServers))
	for n := range c.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Overrides are the values from flags and environment variables that win over
// the file. Empty means "not set".
type Overrides struct {
	// Type forces the provider type ("openai", "anthropic", "deepseek"), so a deployment can pick
	// one with an environment variable and no config file.
	Type    string
	Profile string
	Model   string
	BaseURL string
	APIKey  string
}

// Resolved is a fully worked-out provider setting.
type Resolved struct {
	Name      string // profile name, "default" when there is no profile
	Type      string
	BaseURL   string
	APIKey    string
	KeySource string // where the key came from, for display; never the key itself
	KeyEnv    string // environment variable to suggest when the key is missing
	Model     string
	MaxTokens int
	NoVision  bool // the model cannot see images
	// Withheld names a key variable that is set but was not used, because this profile points
	// somewhere other than the provider the variable is for.
	Withheld string
	// Thinking and ReasoningEffort are DeepSeek's settings, "" when not set.
	Thinking        string
	ReasoningEffort string
}

var calendarName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

var (
	defaultModel  = map[string]string{provider.TypeOpenAI: "gpt-4o-mini", provider.TypeAnthropic: "claude-sonnet-5-5", provider.TypeDeepSeek: "deepseek-flash"}
	defaultKeyEnv = map[string]string{provider.TypeOpenAI: "OPENAI_API_KEY", provider.TypeAnthropic: "ANTHROPIC_API_KEY", provider.TypeDeepSeek: "DEEPSEEK_API_KEY"}
)

// DefaultKeyEnvs are the variables each provider's key is read from when a profile does not name one.
func DefaultKeyEnvs() []string {
	var out []string
	for _, v := range defaultKeyEnv {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Resolve picks a profile and applies overrides. It does not check the key;
// call CheckKey before using the result.
func (c *Config) Resolve(o Overrides, getenv func(string) string) (Resolved, error) {
	name := o.Profile
	if name == "" {
		name = c.DefaultProfile
	}
	if name == "" && len(c.Profiles) == 1 {
		for only := range c.Profiles {
			name = only
		}
	}

	explicit := name != ""
	if !explicit {
		name = "default"
	}
	p, found := c.Profiles[name]
	if !found && explicit {
		return Resolved{}, fmt.Errorf("no profile named %q (profiles: %s)", name, c.profileNames())
	}

	typ := p.Type
	if o.Type != "" {
		typ = o.Type
	}
	if typ == "" {
		typ = provider.TypeOpenAI
	}
	if _, known := defaultKeyEnv[typ]; !known {
		return Resolved{}, fmt.Errorf("profile %q: unknown type %q (use %q, %q or %q)", name, typ, provider.TypeOpenAI, provider.TypeAnthropic, provider.TypeDeepSeek)
	}
	if err := p.validateThinking(name, typ); err != nil {
		return Resolved{}, err
	}

	r := Resolved{
		Name:      name,
		Type:      typ,
		BaseURL:   first(o.BaseURL, p.BaseURL, provider.DefaultBaseURL(typ)),
		Model:     first(o.Model, p.Model, defaultModel[typ]),
		MaxTokens: p.MaxTokens,
		NoVision:  p.Vision != nil && !*p.Vision,
		KeyEnv:    first(p.APIKeyEnv, defaultKeyEnv[typ]),
	}
	if typ == provider.TypeDeepSeek {
		// Whether its models can see pictures is not something the service documents, so say yes only when told.
		r.NoVision = p.Vision == nil || !*p.Vision
		r.ReasoningEffort = p.ReasoningEffort
		if p.Thinking != nil {
			r.Thinking = map[bool]string{true: "enabled", false: "disabled"}[*p.Thinking]
		}
	}

	switch {
	case o.APIKey != "":
		r.APIKey, r.KeySource = o.APIKey, "TALARIA_API_KEY"
	case p.APIKeyEnv != "" && getenv(p.APIKeyEnv) != "":
		r.APIKey, r.KeySource = getenv(p.APIKeyEnv), "$"+p.APIKeyEnv
	case p.APIKey != "":
		r.APIKey, r.KeySource = p.APIKey, "api_key in the config file"
	case p.APIKeyEnv == "" && getenv(defaultKeyEnv[typ]) != "":
		// The usual variable is only sent to the provider it is for. A profile pointed anywhere else
		// (a local server, a proxy, an address from a flag) would otherwise be handed a real key it was never given.
		if sameHost(r.BaseURL, provider.DefaultBaseURL(typ)) {
			r.APIKey, r.KeySource = getenv(defaultKeyEnv[typ]), "$"+defaultKeyEnv[typ]
		} else {
			r.Withheld = defaultKeyEnv[typ]
		}
	}
	return r, nil
}

// CheckKey reports a missing API key. Self-hosted OpenAI-compatible servers
// (Ollama, vLLM, ...) need none; the hosted APIs always do.
func (r Resolved) CheckKey() error {
	hosted := r.Type == provider.TypeAnthropic || strings.Contains(r.BaseURL, "api.openai.com") || strings.Contains(r.BaseURL, "api.deepseek.com")
	if hosted && r.APIKey == "" {
		if r.Withheld != "" {
			return fmt.Errorf("profile %q points at %s, which is not where $%s belongs, so that key was not sent there. Name the variable this address takes in the profile's api_key_env, or set TALARIA_API_KEY", r.Name, r.BaseURL, r.Withheld)
		}
		return fmt.Errorf("profile %q needs an API key: set $%s (or TALARIA_API_KEY)", r.Name, r.KeyEnv)
	}
	return nil
}

// Spec converts to what provider.New takes.
func (r Resolved) Spec() provider.Spec {
	return provider.Spec{Type: r.Type, BaseURL: r.BaseURL, APIKey: r.APIKey, MaxTokens: r.MaxTokens, NoVision: r.NoVision,
		Thinking: r.Thinking, ReasoningEffort: r.ReasoningEffort}
}

// validateThinking checks the DeepSeek-only settings.
func (p Profile) validateThinking(name, typ string) error {
	if typ != provider.TypeDeepSeek {
		if p.Thinking != nil || p.ReasoningEffort != "" {
			return fmt.Errorf("profile %q: thinking and reasoning_effort only apply to type %q", name, provider.TypeDeepSeek)
		}
		return nil
	}
	switch p.ReasoningEffort {
	case "", "low", "high", "max":
		return nil
	}
	return fmt.Errorf("profile %q: reasoning_effort must be low, high or max, not %q", name, p.ReasoningEffort)
}

func isLoopbackHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// sameHost reports whether two addresses are on the same host.
func sameHost(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && ua.Hostname() != "" && strings.EqualFold(ua.Hostname(), ub.Hostname())
}

func (c *Config) profileNames() string {
	if len(c.Profiles) == 0 {
		return "none defined"
	}
	return strings.Join(c.ProfileNames(), ", ")
}

// ProfileNames lists the configured profiles, sorted.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// TelegramToken returns the bot token: TALARIA_TELEGRAM_TOKEN, else the
// variable named by telegram.token_env, else telegram.token.
func (c *Config) TelegramToken(getenv func(string) string) string {
	if v := getenv("TALARIA_TELEGRAM_TOKEN"); v != "" {
		return v
	}
	if c.Telegram.TokenEnv != "" {
		if v := getenv(c.Telegram.TokenEnv); v != "" {
			return v
		}
	}
	return c.Telegram.Token
}

// DiscordToken returns the bot token: TALARIA_DISCORD_TOKEN, else the variable
// named by discord.token_env, else discord.token.
func (c *Config) DiscordToken(getenv func(string) string) string {
	if v := getenv("TALARIA_DISCORD_TOKEN"); v != "" {
		return v
	}
	if c.Discord.TokenEnv != "" {
		if v := getenv(c.Discord.TokenEnv); v != "" {
			return v
		}
	}
	return c.Discord.Token
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Example is what `tal config init` writes.
const Example = `{
  "default_profile": "openai",
  "profiles": {
    "openai": {
      "type": "openai",
      "api_key_env": "OPENAI_API_KEY",
      "model": "gpt-4o-mini"
    },
    "claude": {
      "type": "anthropic",
      "api_key_env": "ANTHROPIC_API_KEY",
      "model": "claude-sonnet-5-5"
    },
    "deepseek": {
      "type": "deepseek",
      "api_key_env": "DEEPSEEK_API_KEY",
      "model": "deepseek-flash"
    },
    "local": {
      "type": "openai",
      "base_url": "http://localhost:11434/v1",
      "model": "llama3.1"
    }
  },
  "telegram": {
    "token_env": "TALARIA_TELEGRAM_TOKEN",
    "allowed_users": [],
    "stream": true
  },
  "discord": {
    "token_env": "TALARIA_DISCORD_TOKEN",
    "allowed_users": [],
    "stream": true
  },
  "mcp_servers": {}
}
`

// Init writes the example config, refusing to overwrite an existing file.
func Init(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists", path)
		}
		return err
	}
	defer f.Close()
	_, err = f.WriteString(Example)
	return err
}
