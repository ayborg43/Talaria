package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmp"
	"slices"
	"talaria/internal/calendar"
	"talaria/internal/config"
	"talaria/internal/files"

	"talaria/internal/gateway/chat"
	"talaria/internal/mailbox"
	"talaria/internal/provider"
	"talaria/internal/x"
)

//go:embed static
var staticFS embed.FS

const (
	maxBody    = 64 << 10
	maxMessage = 8000
)

// AgendaItem is one event, for the Today screen.
type AgendaItem struct {
	Title    string    `json:"title"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	AllDay   bool      `json:"allDay"`
	Where    string    `json:"where,omitempty"`
	Calendar string    `json:"calendar,omitempty"`
}

// MailItem is one message, for the Today screen.
type MailItem struct {
	ID      uint32    `json:"id"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	Unread  bool      `json:"unread"`
}

// JobInfo is something Talaria does on its own: a goal, a watch or a scheduled reminder.
type JobInfo struct {
	Kind  string `json:"kind"` // GOAL, WATCH, TIME
	Title string `json:"title"`
	Sub   string `json:"sub"`
	State string `json:"state"`
}

// LinkInfo is something Talaria is connected to.
type LinkInfo struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Tag    string `json:"tag"`
}

// Sources is where the Today and Control screens get what they show. Any may be nil.
type Sources struct {
	Agenda func(ctx context.Context) (items []AgendaItem, note string, err error)
	Mail   func(ctx context.Context) (unread int, items []MailItem, err error)
	Jobs   func() []JobInfo
	Links  func() []LinkInfo
	// Zone is the time zone the person lives in.
	Zone *time.Location
}

// Pauser holds and releases everything Talaria does on its own.
type Pauser interface {
	Paused() bool
	SetPaused(bool) error
}

// Server is the web app's HTTP side.
type Server struct {
	Hub       *Hub
	Auth      *Auth
	Engine    *chat.Engine
	Push      *Pusher
	Sources   Sources
	Pause     Pauser
	PublicURL string // https://talaria.example.com: where people reach it, for the origin check
	Model     string
	Version   string
	// Telegram says the agent can message the user on Telegram, so the page offers to send a result there.
	Telegram bool
	// Permissions is which calls ask first in this run: config.PermissionsDeletesAndPosts or config.PermissionsStrict.
	Permissions string
	Started     time.Time
	Logf        func(string, ...any)
	// TrustProxy says the address of a request is the last one a front proxy put in X-Forwarded-For.
	TrustProxy bool

	ConfigPath string
	Cfg        *config.Config
	// Mailbox and Calendars are the live services Settings manages; nil when
	// not set up. Getenv reads the environment for what Settings leaves empty.
	Mailbox   *mailbox.Service
	Calendars *calendar.Service
	X         *x.Service
	Getenv    func(string) string
	// Convos keeps every conversation of this chat: the page shows the
	// active one and can go back to any of them. Nil keeps one thread only.
	Convos *Convos
	// Files keeps uploads from the page and PDFs the agent makes, served back
	// as downloads. Nil uses a store next to the config file.
	Files *files.Store

	cacheMu   sync.Mutex
	todayAt   time.Time
	todayJSON []byte
}

func (s *Server) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

func (s *Server) origin() string {
	u, err := url.Parse(s.PublicURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Handler is everything the web app serves.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", s.static())

	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.authed(s.logout))
	mux.HandleFunc("GET /api/state", s.authed(s.state))
	mux.HandleFunc("GET /api/settings", s.authed(s.settingsGet))
	mux.HandleFunc("POST /api/settings", s.authed(s.settingsPost))
	mux.HandleFunc("GET /api/events", s.authed(s.events))
	mux.HandleFunc("POST /api/send", s.authed(s.send))
	mux.HandleFunc("POST /api/approve", s.authed(s.approve))
	mux.HandleFunc("GET /api/chats", s.authed(s.chats))
	mux.HandleFunc("POST /api/chats", s.authed(s.switchChat))
	mux.HandleFunc("POST /api/presence", s.authed(s.presence))
	mux.HandleFunc("POST /api/files", s.authed(s.upload))
	mux.HandleFunc("GET /api/files", s.authed(s.filesList))
	mux.HandleFunc("GET /api/files/{id}", s.authed(s.download))
	mux.HandleFunc("GET /api/today", s.authed(s.today))
	mux.HandleFunc("GET /api/agents", s.authed(s.agents))
	mux.HandleFunc("GET /api/control", s.authed(s.control))
	mux.HandleFunc("POST /api/pause", s.authed(s.pause))
	mux.HandleFunc("GET /api/push/key", s.authed(s.pushKey))
	mux.HandleFunc("POST /api/push/subscribe", s.authed(s.pushSubscribe))
	mux.HandleFunc("POST /api/push/unsubscribe", s.authed(s.pushUnsubscribe))
	return s.headers(mux)
}

// Serve listens until ctx ends.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		// No write timeout: the event stream stays open for as long as the page does.
	}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(sh)
	}()
	s.purgeFiles()
	go s.sweepFiles(ctx)
	s.logf("web: serving the app on %s", ln.Addr())
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return ctx.Err()
}

// headers puts the protections on every response.
func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), geolocation=(), payment=(), microphone=(self)")
		if s.Auth != nil && s.Auth.Secure {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) static() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/" || path == "/index.html":
			r.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
		case path == "/sw.js":
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Service-Worker-Allowed", "/")
		case strings.HasPrefix(path, "/fonts/") || strings.HasPrefix(path, "/icons/"):
			w.Header().Set("Cache-Control", "public, max-age=604800")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		if path == "/manifest.webmanifest" {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		files.ServeHTTP(w, r)
	})
}

// ---- helpers ----

func (s *Server) ip(r *http.Request) string {
	if s.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "that request could not be read")
		return false
	}
	return true
}

// authed is a handler that needs a signed-in person, and, when it changes anything, a request from this app.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !SameOrigin(r, s.origin()) {
			fail(w, http.StatusForbidden, "that request did not come from the app")
			return
		}
		if !s.Auth.Valid(r) {
			fail(w, http.StatusUnauthorized, "sign in first")
			return
		}
		next(w, r)
	}
}

// ---- sign in ----

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !SameOrigin(r, s.origin()) {
		fail(w, http.StatusForbidden, "that request did not come from the app")
		return
	}
	var in struct{ Password string }
	if !readJSON(w, r, &in) {
		return
	}
	token, err := s.Auth.Login(s.ip(r), in.Password)
	var locked LockedError
	switch {
	case errors.As(err, &locked):
		w.Header().Set("Retry-After", strconv.Itoa(int(locked.RetryAfter.Seconds())+1))
		fail(w, http.StatusTooManyRequests, err.Error())
	case err != nil:
		s.logf("web: a wrong password from %s", s.ip(r))
		fail(w, http.StatusUnauthorized, "That password is not right.")
	default:
		http.SetCookie(w, s.Auth.Cookie(token))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Logout(r)
	http.SetCookie(w, s.Auth.Cookie(""))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- conversation ----

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	msgs, seq, epoch, running := s.Hub.Snapshot()
	paused := s.Pause != nil && s.Pause.Paused()
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "seq": seq, "epoch": epoch, "running": running, "agents": s.Hub.Workers(), "model": s.activeModel(), "version": s.Version, "paused": paused, "zone": s.zone().String(), "telegram": s.Telegram})
}

// activeModel is the model this chat is talking to: what answered last, or
// its chosen profile's, or the server's when switching is not set up or the
// choice cannot be used.
func (s *Server) activeModel() string {
	if m := s.Hub.Model(); m != "" {
		return m
	}
	if s.Engine != nil && s.Engine.Backends != nil {
		if be, err := s.Engine.Backends.Open(s.Engine.ActiveProfile(ChatID)); err == nil && be.Model != "" {
			return be.Model
		}
	}
	return s.Model
}

// ---- settings ----

// settingsView is what the settings page shows: everything except actual API keys.
type settingsView struct {
	DefaultProfile string             `json:"default_profile"`
	ActiveProfile  string             `json:"active_profile"` // the profile this chat is using; "" when switching is not set up
	Profiles       []settingsProfile  `json:"profiles"`
	Presets        []config.Preset    `json:"presets"`   // one-click starting points for a new profile
	Strengths      []string           `json:"strengths"` // labels a profile can claim, so the router picks it
	Email          settingsEmail      `json:"email"`
	Calendars      []settingsCalendar `json:"calendars"`
	Chats          []settingsChat     `json:"chats"`           // telegram and discord: status, never secrets
	HasDefaultKey  bool               `json:"has_default_key"` // TALARIA_API_KEY is set
	DefaultKeyEnv  string             `json:"default_key_env"`
	DefaultKeySet  bool               `json:"default_key_set"`
	KeyEnvs        []string           `json:"key_envs"`
	// Permissions is which calls ask first once saved; PermissionsRunning is what is in force now (it differs until a
	// restart), and PermissionsEnv says TALARIA_PERMISSIONS overrides the file.
	Permissions         string `json:"permissions"`
	PermissionsRunning  string `json:"permissions_running"`
	PermissionsEnv      bool   `json:"permissions_env,omitempty"`
	SubagentMaxSteps    int    `json:"subagent_max_steps"`
	SubagentMaxParallel int    `json:"subagent_max_parallel"`
	FilesRetentionHours int    `json:"files_retention_hours"`
	FilesMaxTotalMB     int    `json:"files_max_total_mb"`
}

// settingsEmail is the mailbox as Settings shows it: everything but the password.
type settingsEmail struct {
	Address        string `json:"address"`
	User           string `json:"user,omitempty"`
	Server         string `json:"server,omitempty"`
	HasPassword    bool   `json:"has_password"`
	PasswordSource string `json:"password_source,omitempty"` // settings or environment
	Configured     bool   `json:"configured"`
	Available      bool   `json:"available"`
	Status         string `json:"status,omitempty"`
}

// settingsCalendar is one feed as Settings shows it: never the address itself.
type settingsCalendar struct {
	Name       string `json:"name"`
	Builtin    bool   `json:"builtin,omitempty"` // Talaria's own calendar: not editable
	Source     string `json:"source,omitempty"`  // settings or environment
	HasAddress bool   `json:"has_address,omitempty"`
	Status     string `json:"status,omitempty"`
}

// settingsChat is a chat service as Settings shows it: never any token.
type settingsChat struct {
	Service   string `json:"service"` // telegram, discord or x
	Connected bool   `json:"connected"`
	Source    string `json:"source,omitempty"` // settings or environment
	HasToken  bool   `json:"has_token,omitempty"`
}

// emailUpdate replaces the mailbox settings wholesale: empty means unset.
type emailUpdate struct {
	Address     string `json:"address,omitempty"`
	User        string `json:"user,omitempty"`
	Server      string `json:"server,omitempty"`
	Password    string `json:"password,omitempty"`
	PasswordEnv string `json:"password_env,omitempty"`
}

// calendarUpdate adds, edits or deletes one feed kept in Settings.
type calendarUpdate struct {
	Op           string `json:"op"` // add, edit or delete
	Name         string `json:"name"`
	URL          string `json:"url,omitempty"`
	AllowPrivate bool   `json:"allow_private,omitempty"`
}

// chatTokenUpdate replaces one chat service's token. It takes effect after a
// restart: the gateway signs in when it starts.
type chatTokenUpdate struct {
	Service  string `json:"service"` // telegram, discord or x
	Token    string `json:"token,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
}

type settingsProfile struct {
	Name      string   `json:"name"`
	Type      string   `json:"type,omitempty"`
	BaseURL   string   `json:"base_url,omitempty"`
	Model     string   `json:"model,omitempty"`
	APIKeyEnv string   `json:"api_key_env,omitempty"`
	HasKey    bool     `json:"has_key"` // a key is configured for this profile
	KeySource string   `json:"key_source,omitempty"`
	Active    bool     `json:"active,omitempty"`    // this chat is using it
	Default   bool     `json:"default,omitempty"`   // chats use it until they pick another
	Available bool     `json:"available,omitempty"` // it can be used right now
	Status    string   `json:"status,omitempty"`    // what it is, or why it cannot be used
	Best      []string `json:"best,omitempty"`      // what it is good at, for the router
	Traits    []string `json:"traits,omitempty"`    // what it can do: vision, reasoning
}

func (s *Server) getenv(k string) string {
	return s.GetenvOrEmpty(k)
}

// GetenvOrEmpty reads the host environment through the wired lookup, or
// empty when no lookup is wired (tests wire config directly).
func (s *Server) GetenvOrEmpty(key string) string {
	if s.Getenv != nil {
		return s.Getenv(key)
	}
	return ""
}

// emailView describes the mailbox: everything but the password.
func (s *Server) emailView() settingsEmail {
	v := settingsEmail{}
	if s.Cfg == nil {
		return v
	}
	e := s.Cfg.Email
	v.Address, v.User, v.Server = e.Address, e.User, e.Server
	switch {
	case strings.TrimSpace(e.Password) != "":
		v.HasPassword, v.PasswordSource = true, "settings"
	case strings.TrimSpace(e.PasswordEnv) != "" && strings.TrimSpace(s.getenv(e.PasswordEnv)) != "":
		v.HasPassword, v.PasswordSource = true, "settings"
	case strings.TrimSpace(s.getenv("TALARIA_EMAIL_PASSWORD")) != "":
		v.HasPassword, v.PasswordSource = true, "environment"
	}
	addr, _, pw, srv := mailbox.Merge(e.Address, e.User, e.Password, e.PasswordEnv, e.Server, s.Getenv)
	_, _, _, problems, configured := mailbox.Resolve(addr, pw, srv)
	v.Configured = configured
	v.Available = configured && len(problems) == 0
	if len(problems) > 0 {
		v.Status = problems[0]
	} else if configured {
		v.Status = "reads " + addr
	}
	return v
}

// calendarsView describes every feed: settings entries, environment entries,
// and the built-in calendar. Never any address.
func (s *Server) calendarsView() []settingsCalendar {
	if s.Cfg == nil {
		return nil
	}
	out := []settingsCalendar{{Name: calendar.LocalCalendar, Builtin: true, HasAddress: true, Status: "lives on this server"}}
	names := make([]string, 0, len(s.Cfg.Calendars))
	for n := range s.Cfg.Calendars {
		names = append(names, n)
	}
	slices.Sort(names)
	known := map[string]bool{calendar.LocalCalendar: true}
	for _, n := range names {
		c := s.Cfg.Calendars[n]
		sc := settingsCalendar{Name: n}
		switch {
		case strings.TrimSpace(c.URL) != "":
			sc.Source, sc.HasAddress, sc.Status = "settings", true, "reads when opened"
		case strings.TrimSpace(c.URLEnv) != "" && strings.TrimSpace(s.getenv(c.URLEnv)) != "":
			sc.Source, sc.HasAddress, sc.Status = "environment", true, "reads when opened"
		case strings.TrimSpace(c.URLEnv) != "":
			sc.Status = "$" + c.URLEnv + " is not set"
		default:
			sc.Status = "no address"
		}
		known[n] = true
		out = append(out, sc)
	}
	if s.Calendars != nil {
		for _, n := range s.Calendars.Names() {
			if !known[n] {
				out = append(out, settingsCalendar{Name: n, Source: "environment", HasAddress: true, Status: "reads when opened"})
			}
		}
	}
	return out
}

// chatsView describes the chat services: status, never any token.
func (s *Server) chatsView() []settingsChat {
	if s.Cfg == nil {
		return nil
	}
	var out []settingsChat
	for _, c := range []struct {
		service string
		token   func(func(string) string) string
		saved   string
	}{
		{"telegram", s.Cfg.TelegramToken, s.Cfg.Telegram.Token},
		{"discord", s.Cfg.DiscordToken, s.Cfg.Discord.Token},
		{"x", s.Cfg.XToken, s.Cfg.X.Token},
	} {
		sc := settingsChat{Service: c.service}
		if t := c.token(s.GetenvOrEmpty); t != "" {
			sc.Connected, sc.HasToken = true, true
		}
		switch {
		case strings.TrimSpace(c.saved) != "":
			sc.Source = "settings"
		case sc.Connected:
			sc.Source = "environment"
		}
		out = append(out, sc)
	}
	return out
}

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) {
	if s.Cfg == nil {
		fail(w, http.StatusInternalServerError, "no configuration loaded")
		return
	}
	view := settingsView{
		DefaultProfile:      s.Cfg.DefaultProfile,
		SubagentMaxSteps:    s.Cfg.Subagents.MaxSteps,
		Permissions:         permissionsName(s.Cfg.Policy.Permissions),
		PermissionsRunning:  permissionsName(s.Permissions),
		PermissionsEnv:      s.Getenv != nil && s.Getenv("TALARIA_PERMISSIONS") != "",
		SubagentMaxParallel: s.Cfg.Subagents.MaxParallel,
		FilesRetentionHours: int(s.fileRetention() / time.Hour),
		FilesMaxTotalMB:     s.fileQuotaMB(),
		KeyEnvs:             config.DefaultKeyEnvs(),
		Presets:             config.Presets(),
		Strengths:           config.ProfileStrengths,
		Email:               s.emailView(),
		Calendars:           s.calendarsView(),
		Chats:               s.chatsView(),
	}
	var backends *chat.Backends
	if s.Engine != nil {
		backends = s.Engine.Backends
	}
	if backends != nil {
		view.ActiveProfile = s.Engine.ActiveProfile(ChatID)
	}
	if defKeyEnv := os.Getenv("TALARIA_API_KEY"); defKeyEnv != "" {
		view.HasDefaultKey = true
		view.DefaultKeyEnv = "TALARIA_API_KEY"
		view.DefaultKeySet = true
	}
	for name, p := range s.Cfg.Profiles {
		sp := settingsProfile{Name: name, Type: p.Type, BaseURL: p.BaseURL, Model: p.Model, APIKeyEnv: p.APIKeyEnv, Best: p.BestFor}
		if p.APIKey != "" {
			sp.HasKey = true
			sp.KeySource = "api_key in the config file"
		}
		if backends != nil {
			sp.Active = name == view.ActiveProfile
			sp.Default = name == backends.Default
			if be, err := backends.Open(name); err != nil {
				sp.Status = err.Error()
			} else {
				sp.Available = true
				sp.Status = be.Desc
				if be.Vision {
					sp.Traits = append(sp.Traits, "vision")
				}
				if be.Reasoning {
					sp.Traits = append(sp.Traits, "reasoning")
				}
			}
		}
		view.Profiles = append(view.Profiles, sp)
	}
	slices.SortFunc(view.Profiles, func(a, b settingsProfile) int { return cmp.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, view)
}

// settingsUpdateRequest is the payload for POST /api/settings.
type settingsUpdateRequest struct {
	Name                string           `json:"name"`
	Type                string           `json:"type,omitempty"`
	BaseURL             string           `json:"base_url,omitempty"`
	Model               string           `json:"model,omitempty"`
	APIKey              string           `json:"api_key,omitempty"`
	APIKeyEnv           string           `json:"api_key_env,omitempty"`
	Delete              bool             `json:"_delete,omitempty"`
	UseProfile          string           `json:"use_profile,omitempty"`           // switch this chat to a profile, saving nothing
	TestProfile         string           `json:"test_profile,omitempty"`          // send one tiny prompt through a profile, saving nothing
	BestFor             []string         `json:"best_for,omitempty"`              // what the profile is good at, for the router
	Email               *emailUpdate     `json:"email,omitempty"`                 // replace the mailbox settings
	Calendar            *calendarUpdate  `json:"calendar,omitempty"`              // add, edit or delete a feed
	ChatToken           *chatTokenUpdate `json:"chat_token,omitempty"`            // replace a bot token
	TestEmail           bool             `json:"test_email,omitempty"`            // list the mailbox's folders, saving nothing
	TestCalendar        string           `json:"test_calendar,omitempty"`         // read one feed for a day, saving nothing
	TestX               bool             `json:"test_x,omitempty"`                // check the X sign-in, saving nothing
	Permissions         string           `json:"permissions,omitempty"`           // which calls ask first: deletes_and_posts or strict
	FilesRetentionHours *int             `json:"files_retention_hours,omitempty"` // how long downloads live, 1 to 168 hours
	FilesMaxTotalMB     *int             `json:"files_max_total_mb,omitempty"`    // cap on everything kept, 10 to 1024 MB
}

func (s *Server) settingsPost(w http.ResponseWriter, r *http.Request) {
	var in settingsUpdateRequest
	if !readJSON(w, r, &in) {
		return
	}
	if in.UseProfile != "" {
		if s.Engine == nil || s.Engine.Backends == nil {
			fail(w, http.StatusServiceUnavailable, "only one model is configured")
			return
		}
		if err := s.Engine.SetProfile(ChatID, strings.TrimSpace(in.UseProfile)); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if in.TestProfile != "" {
		if s.Engine == nil || s.Engine.Backends == nil {
			fail(w, http.StatusServiceUnavailable, "only one model is configured")
			return
		}
		name := strings.TrimSpace(in.TestProfile)
		be, err := s.Engine.Backends.Open(name)
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		msg, err := be.Provider.Chat(ctx, provider.Request{Model: be.Model, Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "Reply with exactly the word: ok"},
		}})
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": be.Model, "reply": firstLine(msg.Content)})
		return
	}
	if in.TestEmail {
		if s.Mailbox == nil {
			fail(w, http.StatusBadRequest, "no mailbox is set up")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		folders, err := s.Mailbox.Folders(ctx)
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "folders": len(folders)})
		return
	}
	if in.TestCalendar != "" {
		if s.Calendars == nil {
			fail(w, http.StatusBadRequest, "no calendar is set up")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		now := time.Now().In(s.zone())
		from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		events, _, err := s.Calendars.Events(ctx, from, from.AddDate(0, 0, 1), strings.TrimSpace(in.TestCalendar), "")
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "events": len(events)})
		return
	}
	if in.TestX {
		if s.X == nil {
			fail(w, http.StatusBadRequest, "no X account is set up")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		account, err := s.X.Verify(ctx)
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": account})
		return
	}
	if s.Cfg == nil || s.ConfigPath == "" {
		fail(w, http.StatusInternalServerError, "settings cannot be saved here")
		return
	}
	if in.Permissions != "" {
		s.savePermissions(w, in.Permissions)
		return
	}
	if in.FilesRetentionHours != nil {
		s.saveFilesRetention(w, *in.FilesRetentionHours)
		return
	}
	if in.FilesMaxTotalMB != nil {
		s.saveFilesQuota(w, *in.FilesMaxTotalMB)
		return
	}
	if in.Email != nil {
		s.saveEmail(w, in.Email)
		return
	}
	if in.Calendar != nil {
		s.saveCalendar(w, in.Calendar)
		return
	}
	if in.ChatToken != nil {
		s.saveChatToken(w, in.ChatToken)
		return
	}
	if in.Name == "" {
		fail(w, http.StatusBadRequest, "give the profile a name")
		return
	}
	switch in.Type {
	case "", provider.TypeOpenAI, provider.TypeAnthropic, provider.TypeDeepSeek:
	default:
		fail(w, http.StatusBadRequest, fmt.Sprintf("unknown profile type %q (use %q, %q or %q)", in.Type, provider.TypeOpenAI, provider.TypeAnthropic, provider.TypeDeepSeek))
		return
	}
	for _, b := range in.BestFor {
		if !slices.Contains(config.ProfileStrengths, b) {
			fail(w, http.StatusBadRequest, fmt.Sprintf("best_for must be any of: %s", strings.Join(config.ProfileStrengths, ", ")))
			return
		}
	}
	// Copy on write: the engine reads these profiles while chats run.
	profiles := make(map[string]config.Profile, len(s.Cfg.Profiles)+1)
	for k, v := range s.Cfg.Profiles {
		profiles[k] = v
	}
	if in.Delete {
		delete(profiles, in.Name)
	} else if p, ok := profiles[in.Name]; ok {
		// Edit existing profile
		if in.Type != "" {
			p.Type = in.Type
		}
		if in.BestFor != nil {
			p.BestFor = in.BestFor
		}
		if in.BaseURL != "" {
			p.BaseURL = in.BaseURL
		}
		if in.Model != "" {
			p.Model = in.Model
		}
		if in.APIKeyEnv != "" { // a named variable replaces a pasted key: keep secrets out of the file
			p.APIKeyEnv = in.APIKeyEnv
			p.APIKey = ""
		}
		if in.APIKey != "" {
			p.APIKey = in.APIKey
		}
		profiles[in.Name] = p
	} else {
		p := config.Profile{Type: in.Type, BaseURL: in.BaseURL, Model: in.Model, APIKeyEnv: in.APIKeyEnv, BestFor: in.BestFor}
		if in.APIKey != "" {
			p.APIKey = in.APIKey
		}
		profiles[in.Name] = p
	}
	s.Cfg.Profiles = profiles
	if err := config.Write(s.ConfigPath, s.Cfg); err != nil {
		s.logf("web: settings save: %v", err)
		fail(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	if s.Engine != nil && s.Engine.Backends != nil && s.Engine.Backends.Forget != nil {
		s.Engine.Backends.Forget() // added and changed profiles work at once, with no restart
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// writeConfig saves the config file, reporting failure.
func (s *Server) writeConfig(w http.ResponseWriter) bool {
	if err := config.Write(s.ConfigPath, s.Cfg); err != nil {
		s.logf("web: settings save: %v", err)
		fail(w, http.StatusInternalServerError, "could not save settings")
		return false
	}
	return true
}

// okRestart answers a save: a restart is needed when there is no live service
// to update, or the service only reads its settings when it starts.
func okRestart(w http.ResponseWriter, restart bool) {
	if restart {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_needed": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// saveFilesRetention sets how long downloads live on the server. The
// sweeper picks it up within the hour; nothing restarts.
func (s *Server) saveFilesRetention(w http.ResponseWriter, hours int) {
	if hours < files.MinRetentionHours || hours > files.MaxRetentionHours {
		fail(w, http.StatusBadRequest, fmt.Sprintf("files_retention_hours must be %d to %d", files.MinRetentionHours, files.MaxRetentionHours))
		return
	}
	s.Cfg.Files.RetentionHours = hours
	if !s.writeConfig(w) {
		return
	}
	okRestart(w, false)
}

// saveFilesQuota caps everything kept on the server, oldest evicted first.
// It applies to the next file saved; nothing restarts.
func (s *Server) saveFilesQuota(w http.ResponseWriter, mb int) {
	if mb < files.MinQuotaMB || mb > files.MaxQuotaMB {
		fail(w, http.StatusBadRequest, fmt.Sprintf("files_max_total_mb must be %d to %d", files.MinQuotaMB, files.MaxQuotaMB))
		return
	}
	s.Cfg.Files.MaxTotalMB = mb
	if !s.writeConfig(w) {
		return
	}
	okRestart(w, false)
}

// permissionsName is a permission mode as the page shows it: the empty one means the default.
func permissionsName(p string) string {
	if p == config.PermissionsStrict {
		return config.PermissionsStrict
	}
	return config.PermissionsDeletesAndPosts
}

// savePermissions chooses which calls ask the user first. The agents read it when they are built, so it applies
// after a restart.
func (s *Server) savePermissions(w http.ResponseWriter, mode string) {
	if mode != config.PermissionsDeletesAndPosts && mode != config.PermissionsStrict {
		fail(w, http.StatusBadRequest, fmt.Sprintf("permissions must be %q or %q", config.PermissionsDeletesAndPosts, config.PermissionsStrict))
		return
	}
	s.Cfg.Policy.Permissions = mode
	if !s.writeConfig(w) {
		return
	}
	okRestart(w, permissionsName(s.Permissions) != mode || (s.Getenv != nil && s.Getenv("TALARIA_PERMISSIONS") != ""))
}

func (s *Server) saveEmail(w http.ResponseWriter, in *emailUpdate) {
	if a := strings.TrimSpace(in.Address); a != "" {
		if p, err := mail.ParseAddress(a); err != nil || p.Address != a {
			fail(w, http.StatusBadRequest, "the address is not an email address: write it as you@example.com")
			return
		}
	}
	e := s.Cfg.Email
	e.Address, e.User, e.Server = strings.TrimSpace(in.Address), strings.TrimSpace(in.User), strings.TrimSpace(in.Server)
	if in.Password != "" {
		e.Password = in.Password
	}
	if in.PasswordEnv != "" { // a named variable replaces a pasted password: keep secrets out of the file
		e.PasswordEnv, e.Password = strings.TrimSpace(in.PasswordEnv), ""
	}
	s.Cfg.Email = e
	if !s.writeConfig(w) {
		return
	}
	restart := false
	addr, user, pw, srv := mailbox.Merge(e.Address, e.User, e.Password, e.PasswordEnv, e.Server, s.Getenv)
	_, _, _, problems, configured := mailbox.Resolve(addr, pw, srv)
	if s.Mailbox != nil {
		if configured && len(problems) == 0 {
			s.Mailbox.Set(addr, user, pw, srv)
		} else {
			restart = true // the live sign-in stays until a restart
		}
	} else if configured && len(problems) == 0 {
		restart = true // nothing live to update yet
	}
	okRestart(w, restart)
}

func (s *Server) saveCalendar(w http.ResponseWriter, in *calendarUpdate) {
	name := strings.TrimSpace(in.Name)
	if !config.ValidCalendarName(name) {
		fail(w, http.StatusBadRequest, "calendar names use lower-case letters, digits, - and _ (up to 32)")
		return
	}
	if in.Op != "add" && in.Op != "edit" && in.Op != "delete" {
		fail(w, http.StatusBadRequest, "op is add, edit or delete")
		return
	}
	if name == calendar.LocalCalendar {
		fail(w, http.StatusBadRequest, "Talaria's own calendar is always there")
		return
	}
	_, inCfg := s.Cfg.Calendars[name]
	inService := false
	if s.Calendars != nil {
		for _, n := range s.Calendars.Names() {
			if n == name && n != calendar.LocalCalendar && !inCfg {
				inService = true
			}
		}
	}
	switch in.Op {
	case "add":
		if inCfg || inService {
			fail(w, http.StatusBadRequest, "a calendar called "+name+" is already there")
			return
		}
	case "edit":
		if !inCfg {
			fail(w, http.StatusBadRequest, "it comes from the environment: change it where it is set")
			return
		}
	case "delete":
		if !inCfg {
			fail(w, http.StatusBadRequest, "it comes from the environment: remove it where it is set")
			return
		}
	}
	if in.Op == "delete" {
		calendars := make(map[string]config.Calendar, len(s.Cfg.Calendars))
		for k, v := range s.Cfg.Calendars {
			if k != name {
				calendars[k] = v
			}
		}
		s.Cfg.Calendars = calendars
		if !s.writeConfig(w) {
			return
		}
		if s.Calendars != nil {
			s.Calendars.Remove(name)
			okRestart(w, false)
			return
		}
		okRestart(w, true)
		return
	}
	url, err := calendar.NormalizeURL(in.URL)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	calendars := make(map[string]config.Calendar, len(s.Cfg.Calendars)+1)
	for k, v := range s.Cfg.Calendars {
		calendars[k] = v
	}
	calendars[name] = config.Calendar{URL: url, AllowPrivate: in.AllowPrivate}
	s.Cfg.Calendars = calendars
	if !s.writeConfig(w) {
		return
	}
	if s.Calendars != nil {
		s.Calendars.SetDirect(name, url, in.AllowPrivate)
		okRestart(w, false)
		return
	}
	okRestart(w, true)
}

func (s *Server) saveChatToken(w http.ResponseWriter, in *chatTokenUpdate) {
	if in.Service != "telegram" && in.Service != "discord" && in.Service != "x" {
		fail(w, http.StatusBadRequest, "service is telegram, discord or x")
		return
	}
	if in.Token != "" && in.TokenEnv != "" {
		fail(w, http.StatusBadRequest, "set a token or a variable holding it, not both")
		return
	}
	gw := &s.Cfg.Telegram
	if in.Service == "discord" {
		gw = &s.Cfg.Discord
	}
	if in.Service == "x" {
		gw = &s.Cfg.X
	}
	if in.TokenEnv != "" {
		gw.TokenEnv, gw.Token = strings.TrimSpace(in.TokenEnv), ""
	} else {
		gw.Token, gw.TokenEnv = in.Token, ""
	}
	if !s.writeConfig(w) {
		return
	}
	okRestart(w, true) // the gateway signs in when it starts
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	epoch, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	ch, cancel := s.Hub.Subscribe(epoch, after)
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 2000\n\n")
	rc.Flush()
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			rc.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			raw, _ := json.Marshal(ev)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return
			}
			rc.Flush()
		}
	}
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text     string
		Telegram bool
	}
	if !readJSON(w, r, &in) {
		return
	}
	text := strings.TrimSpace(in.Text)
	switch {
	case text == "":
		fail(w, http.StatusBadRequest, "there is nothing to send")
		return
	case len([]rune(text)) > maxMessage:
		fail(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("that is longer than %d characters", maxMessage))
		return
	}
	s.Hub.Seen()
	name := ""
	if strings.HasPrefix(text, "/") {
		name = strings.ToLower(strings.TrimPrefix(strings.Fields(text)[0], "/"))
	}
	if name == "" || !s.Engine.HasCommand(name) { // a command is not part of the conversation
		s.Hub.AddUser(text)
	}
	if name == "new" { // the open conversation is kept, and a blank one starts
		s.newConversation()
	}
	toAgent := text
	if in.Telegram && s.Telegram && name == "" {
		toAgent += telegramAsk
	}
	s.Engine.Incoming(ChatID, UserID, toAgent)
	w.WriteHeader(http.StatusNoContent)
}

// telegramAsk is added, out of sight, to a message sent with "also send to Telegram" on.
const telegramAsk = "\n\n(When you have finished, also send me the result on Telegram with telegram_notify, written so it reads on its own.)"

func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, Choice string }
	if !readJSON(w, r, &in) {
		return
	}
	choice, ok := map[string]chat.Choice{"allow": chat.ChoiceAllow, "always": chat.ChoiceAlways, "deny": chat.ChoiceDeny}[in.Choice]
	if !ok || in.Token == "" || len(in.Token) > 64 {
		fail(w, http.StatusBadRequest, "that answer is not one of allow, always or deny")
		return
	}
	decision, got := s.Engine.Decide(in.Token, UserID, ChatID, choice)
	switch decision {
	case chat.DecisionExpired:
		fail(w, http.StatusGone, chat.MsgExpired)
	case chat.DecisionForbidden:
		fail(w, http.StatusForbidden, chat.MsgNotAllowed)
	case chat.DecisionAnswered:
		fail(w, http.StatusConflict, chat.MsgAnswered)
	default:
		s.Hub.Resolve(in.Token, got)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) presence(w http.ResponseWriter, r *http.Request) {
	var in struct{ Visible bool }
	if !readJSON(w, r, &in) {
		return
	}
	if in.Visible {
		s.Hub.Seen()
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- files ----

// fileStore is where uploads and generated PDFs live. An explicit store wins
// (tests set one); otherwise it is a files directory next to the config file,
// shared with the agent's file tools.
func (s *Server) fileStore() *files.Store {
	if s.Files != nil {
		return s.Files
	}
	if s.ConfigPath == "" {
		return nil
	}
	mb := 0
	if s.Cfg != nil {
		mb = s.Cfg.Files.MaxTotalMB
	}
	return &files.Store{Dir: filepath.Join(filepath.Dir(s.ConfigPath), "files"), MaxTotalBytes: files.QuotaBytes(mb),
		Retention: s.fileRetention()}
}

// fileQuotaMB is the cap on everything kept, in megabytes.
func (s *Server) fileQuotaMB() int {
	if s.Cfg != nil && s.Cfg.Files.MaxTotalMB > 0 {
		return s.Cfg.Files.MaxTotalMB
	}
	return int(files.DefaultMaxTotalBytes >> 20)
}

// fileRetention is the download lifetime the admin sets, 12 hours by default.
func (s *Server) fileRetention() time.Duration {
	hours := files.DefaultRetentionHours
	if s.Cfg != nil {
		hours = files.ClampRetention(s.Cfg.Files.RetentionHours)
	}
	return time.Duration(hours) * time.Hour
}

func (s *Server) purgeFiles() {
	st := s.fileStore()
	if st == nil {
		return
	}
	if n, err := st.Purge(s.fileRetention()); err != nil {
		s.logf("web: files purge: %v", err)
	} else if n > 0 {
		s.logf("web: files purge: deleted %d expired", n)
	}
}

// sweepFiles deletes expired downloads every hour until the server stops.
func (s *Server) sweepFiles(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.purgeFiles()
		}
	}
}

// upload keeps one PDF or image from the page and tells the conversation, so
// the agent sees it the same way it sees anything the user sends.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	st := s.fileStore()
	if st == nil {
		fail(w, http.StatusInternalServerError, "files are not set up here")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, files.MaxBytes+1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, http.StatusBadRequest, "that upload did not arrive whole; try a smaller file")
		return
	}
	fh, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, "attach a PDF or an image")
		return
	}
	defer fh.Close()
	raw, err := io.ReadAll(io.LimitReader(fh, files.MaxBytes+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "that upload could not be read")
		return
	}
	f, err := st.Save(hdr.Filename, raw, files.OriginUpload)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.purgeFiles()
	s.Hub.AddUser("Uploaded " + f.Name + " — /api/files/" + f.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": f.ID, "name": f.Name, "mime": f.MIME, "size": f.Size,
	})
}

// filesList names every kept file, newest first.
func (s *Server) filesList(w http.ResponseWriter, r *http.Request) {
	st := s.fileStore()
	if st == nil {
		fail(w, http.StatusInternalServerError, "files are not set up here")
		return
	}
	s.purgeFiles()
	list, err := st.List()
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not list files")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": list, "retention_hours": int(s.fileRetention() / time.Hour)})
}

// download serves one kept file. Expired files are already gone: purging on
// every download means a stale link stops working within the hour.
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	st := s.fileStore()
	if st == nil {
		fail(w, http.StatusInternalServerError, "files are not set up here")
		return
	}
	s.purgeFiles()
	f, raw, err := st.Open(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, "no such file: it may have expired")
		return
	}
	w.Header().Set("Content-Type", f.MIME)
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(f.Name))
	http.ServeContent(w, r, f.Name, f.Created, bytes.NewReader(raw))
}

// ---- today ----

func (s *Server) zone() *time.Location {
	if s.Sources.Zone != nil {
		return s.Sources.Zone
	}
	return time.UTC
}

func (s *Server) today(w http.ResponseWriter, r *http.Request) {
	s.cacheMu.Lock()
	if r.URL.Query().Get("fresh") == "" && time.Since(s.todayAt) < 45*time.Second && s.todayJSON != nil {
		raw := s.todayJSON
		s.cacheMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
		return
	}
	s.cacheMu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	out := map[string]any{"now": time.Now().In(s.zone()), "zone": s.zone().String(), "agenda": []AgendaItem{}, "mail": map[string]any{"unread": 0, "items": []MailItem{}}}
	var wg sync.WaitGroup
	if s.Sources.Agenda != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, note, err := s.Sources.Agenda(ctx)
			s.cacheMu.Lock()
			defer s.cacheMu.Unlock()
			out["agendaOn"] = true
			if err != nil {
				out["agendaError"] = err.Error()
				return
			}
			if items != nil {
				out["agenda"] = items
			}
			if note != "" {
				out["agendaNote"] = note
			}
		}()
	}
	if s.Sources.Mail != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unread, items, err := s.Sources.Mail(ctx)
			s.cacheMu.Lock()
			defer s.cacheMu.Unlock()
			out["mailOn"] = true
			if err != nil {
				out["mailError"] = err.Error()
				return
			}
			if items == nil {
				items = []MailItem{}
			}
			out["mail"] = map[string]any{"unread": unread, "items": items}
		}()
	}
	wg.Wait()
	s.cacheMu.Lock()
	out["jobs"] = []JobInfo{}
	s.cacheMu.Unlock()
	if s.Sources.Jobs != nil {
		out["jobs"] = s.Sources.Jobs()
	}
	s.purgeFiles()
	out["files"] = []files.File{}
	out["files_retention_hours"] = int(s.fileRetention() / time.Hour)
	out["files_quota_mb"] = s.fileQuotaMB()
	if st := s.fileStore(); st != nil {
		if list, err := st.List(); err == nil {
			out["files"] = list
		}
	}
	out["pending"] = len(s.Hub.Pending())
	raw, _ := json.Marshal(out)
	s.cacheMu.Lock()
	// Only a complete answer is kept: one with a section that failed is tried again next time.
	if out["agendaError"] == nil && out["mailError"] == nil {
		s.todayAt, s.todayJSON = time.Now(), raw
	}
	s.cacheMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// ---- agents ----

// agents is what the Agents screen shows besides the live list, which arrives over the event stream: what is set up
// to run on its own, and what has finished lately.
func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"agents": s.Hub.Workers(), "recent": s.Hub.Recent(), "jobs": []JobInfo{}}
	if s.Sources.Jobs != nil {
		out["jobs"] = s.Sources.Jobs()
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- control ----

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"version": s.Version, "model": s.Model, "uptimeSec": int64(time.Since(s.Started).Seconds()),
		"paused": s.Pause != nil && s.Pause.Paused(), "activity": s.Hub.Activity(24),
		"jobs": []JobInfo{}, "links": []LinkInfo{}, "pending": len(s.Hub.Pending()), "permissions": permissionsName(s.Permissions),
	}
	if s.Sources.Jobs != nil {
		out["jobs"] = s.Sources.Jobs()
	}
	if s.Sources.Links != nil {
		out["links"] = s.Sources.Links()
	}
	if s.Push != nil {
		out["push"] = map[string]any{"available": true, "devices": s.Push.Count()}
	} else {
		out["push"] = map[string]any{"available": false, "devices": 0}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	var in struct{ Paused bool }
	if !readJSON(w, r, &in) {
		return
	}
	if s.Pause == nil {
		fail(w, http.StatusNotImplemented, "pausing is not set up")
		return
	}
	if err := s.Pause.SetPaused(in.Paused); err != nil {
		fail(w, http.StatusInternalServerError, "could not change that: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": s.Pause.Paused()})
}

// ---- push ----

func (s *Server) pushKey(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		fail(w, http.StatusNotImplemented, "notifications are not set up")
		return
	}
	key, err := s.Push.PublicKey()
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not make the notification key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key})
}

func (s *Server) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		fail(w, http.StatusNotImplemented, "notifications are not set up")
		return
	}
	var sub Subscription
	if !readJSON(w, r, &sub) {
		return
	}
	if err := s.Push.Subscribe(sub); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		fail(w, http.StatusNotImplemented, "notifications are not set up")
		return
	}
	var in struct{ Endpoint string }
	if !readJSON(w, r, &in) {
		return
	}
	s.Push.Unsubscribe(in.Endpoint)
	w.WriteHeader(http.StatusNoContent)
}
