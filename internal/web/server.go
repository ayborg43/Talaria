package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cmp"
	"slices"
	"talaria/internal/config"

	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
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
	Started   time.Time
	Logf      func(string, ...any)
	// TrustProxy says the address of a request is the last one a front proxy put in X-Forwarded-For.
	TrustProxy bool

	ConfigPath string
	Cfg        *config.Config

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
	mux.HandleFunc("POST /api/presence", s.authed(s.presence))
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
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "seq": seq, "epoch": epoch, "running": running, "agents": s.Hub.Workers(), "model": s.Model, "version": s.Version, "paused": paused, "zone": s.zone().String()})
}

// ---- settings ----

// settingsView is what the settings page shows: everything except actual API keys.
type settingsView struct {
	DefaultProfile      string            `json:"default_profile"`
	Profiles            []settingsProfile `json:"profiles"`
	HasDefaultKey       bool              `json:"has_default_key"` // TALARIA_API_KEY is set
	DefaultKeyEnv       string            `json:"default_key_env"`
	DefaultKeySet       bool              `json:"default_key_set"`
	KeyEnvs             []string          `json:"key_envs"`
	SubagentMaxSteps    int               `json:"subagent_max_steps"`
	SubagentMaxParallel int               `json:"subagent_max_parallel"`
}

type settingsProfile struct {
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	Model     string `json:"model,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	HasKey    bool   `json:"has_key"` // a key is configured for this profile
	KeySource string `json:"key_source,omitempty"`
}

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) {
	if s.Cfg == nil {
		fail(w, http.StatusInternalServerError, "no configuration loaded")
		return
	}
	view := settingsView{
		DefaultProfile:      s.Cfg.DefaultProfile,
		SubagentMaxSteps:    s.Cfg.Subagents.MaxSteps,
		SubagentMaxParallel: s.Cfg.Subagents.MaxParallel,
		KeyEnvs:             config.DefaultKeyEnvs(),
	}
	if defKeyEnv := os.Getenv("TALARIA_API_KEY"); defKeyEnv != "" {
		view.HasDefaultKey = true
		view.DefaultKeyEnv = "TALARIA_API_KEY"
		view.DefaultKeySet = true
	}
	for name, p := range s.Cfg.Profiles {
		sp := settingsProfile{Name: name, Type: p.Type, BaseURL: p.BaseURL, Model: p.Model, APIKeyEnv: p.APIKeyEnv}
		if p.APIKey != "" {
			sp.HasKey = true
			sp.KeySource = "api_key in the config file"
		}
		view.Profiles = append(view.Profiles, sp)
	}
	slices.SortFunc(view.Profiles, func(a, b settingsProfile) int { return cmp.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, view)
}

// settingsUpdateRequest is the payload for POST /api/settings.
type settingsUpdateRequest struct {
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	Model     string `json:"model,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	Delete    bool   `json:"_delete,omitempty"`
}

func (s *Server) settingsPost(w http.ResponseWriter, r *http.Request) {
	if s.Cfg == nil || s.ConfigPath == "" {
		fail(w, http.StatusInternalServerError, "settings cannot be saved here")
		return
	}
	var in settingsUpdateRequest
	if !readJSON(w, r, &in) {
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
	if in.Delete {
		delete(s.Cfg.Profiles, in.Name)
	} else if p, ok := s.Cfg.Profiles[in.Name]; ok {
		// Edit existing profile
		if in.Type != "" {
			p.Type = in.Type
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
		s.Cfg.Profiles[in.Name] = p
	} else {
		p := config.Profile{Type: in.Type, BaseURL: in.BaseURL, Model: in.Model, APIKeyEnv: in.APIKeyEnv}
		if in.APIKey != "" {
			p.APIKey = in.APIKey
		}
		if s.Cfg.Profiles == nil {
			s.Cfg.Profiles = map[string]config.Profile{}
		}
		s.Cfg.Profiles[in.Name] = p
	}
	if err := config.Write(s.ConfigPath, s.Cfg); err != nil {
		s.logf("web: settings save: %v", err)
		fail(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	var in struct{ Text string }
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
	if name == "new" { // the old conversation is gone before the engine confirms it in the new one
		s.Hub.Reset()
	}
	s.Engine.Incoming(ChatID, UserID, text)
	w.WriteHeader(http.StatusNoContent)
}

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
		"jobs": []JobInfo{}, "links": []LinkInfo{}, "pending": len(s.Hub.Pending()),
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
