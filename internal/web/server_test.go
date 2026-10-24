package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"talaria/internal/agent"
	"talaria/internal/calendar"
	"talaria/internal/config"
	"talaria/internal/gateway/chat"
	"talaria/internal/mailbox"
	"talaria/internal/provider"
	"talaria/internal/subagent"
	"talaria/internal/tool"
	"talaria/internal/x"
)

// scripted is a model that answers from a script: "run it" makes it ask to run a command, and after the
// command it says it is done; anything else it repeats back.
type scripted struct{ gate chan struct{} }

func (s scripted) Chat(ctx context.Context, req provider.Request) (provider.Message, error) {
	last := req.Messages[len(req.Messages)-1]
	say := func(s string) (provider.Message, error) {
		if req.OnText != nil {
			req.OnText(s)
		}
		return provider.Message{Role: provider.RoleAssistant, Content: s}, nil
	}
	if strings.Contains(req.Messages[0].Content, "You are a subagent working for Talaria") { // a helper: held until released
		if s.gate != nil {
			select {
			case <-s.gate:
			case <-ctx.Done():
				return provider.Message{}, ctx.Err()
			}
		}
		return say("found it")
	}
	if last.Role == provider.RoleTool {
		return say("done: " + last.Content)
	}
	if strings.Contains(strings.ToLower(last.Content), "delegate it") {
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "d", Name: "delegate", Arguments: `{"task":"look into it","name":"researcher"}`}}}, nil
	}
	if strings.Contains(strings.ToLower(last.Content), "run it") {
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "shell", Arguments: `{"command":"ls -la"}`}}}, nil
	}
	return say("echo: " + last.Content)
}

type pauser struct {
	mu sync.Mutex
	on bool
}

func (p *pauser) Paused() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.on }
func (p *pauser) SetPaused(b bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.on = b
	return nil
}

type rig struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	hub    *Hub
	c      *http.Client
	ran    atomic.Int32
	agenda atomic.Int32
	mail   atomic.Int32
	cancel context.CancelFunc
	gate   chan struct{}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t}
	dir := t.TempDir()
	r.hub = &Hub{Path: filepath.Join(dir, "log.json")}
	reg := tool.NewRegistry()
	reg.Register(tool.Tool{Name: "shell", Description: "x", Schema: json.RawMessage(`{"type":"object"}`), NeedsApproval: true,
		Run: func(context.Context, json.RawMessage) (string, error) { r.ran.Add(1); return "listing", nil }})
	r.gate = make(chan struct{})
	pool := subagent.NewPool(subagent.Limits{})
	eng := &chat.Engine{T: r.hub, Allowed: map[int64]bool{UserID: true}, Stream: true, EditInterval: 5 * time.Millisecond,
		NewAgent: func(_ int64, approve func(string, string) bool) *agent.Agent {
			ag := &agent.Agent{Provider: scripted{gate: r.gate}, Model: "m", Tools: reg, MaxSteps: 4, Approve: approve, Activity: r.hub}
			pool.Register(ag, nil)
			return ag
		}}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	eng.Start(ctx)
	r.srv = &Server{
		Hub: r.hub, Auth: &Auth{Password: goodPassword, Path: filepath.Join(dir, "s.json"), Delay: time.Millisecond}, Engine: eng,
		Push: &Pusher{Path: filepath.Join(dir, "p.json")}, Pause: &pauser{}, Model: "test-model", Version: "v1", Started: time.Now().Add(-90 * time.Minute),
		Sources: Sources{
			Agenda: func(context.Context) ([]AgendaItem, string, error) {
				r.agenda.Add(1)
				return []AgendaItem{{Title: "<img src=x onerror=alert(1)>", Start: time.Now(), End: time.Now().Add(time.Hour)}}, "", nil
			},
			Mail: func(context.Context) (int, []MailItem, error) {
				r.mail.Add(1)
				return 2, []MailItem{{ID: 1, From: "A <a@x.example>", Subject: "Hi", At: time.Now(), Unread: true}}, nil
			},
			Jobs:  func() []JobInfo { return []JobInfo{{Kind: "GOAL", Title: "g", Sub: "s", State: "Active"}} },
			Links: func() []LinkInfo { return []LinkInfo{{Name: "Calendar", Detail: "1", Tag: "READ-ONLY"}} },
		},
	}
	r.ts = httptest.NewServer(r.srv.Handler())
	jar, _ := cookiejar.New(nil)
	r.c = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() { cancel(); r.ts.Close(); r.hub.Close() })
	return r
}

func (r *rig) do(method, path string, body any, hdr ...string) (*http.Response, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, r.ts.URL+path, rd)
	if method != "GET" {
		req.Header.Set("X-Talaria", "1")
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	resp, err := r.c.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (r *rig) login() {
	r.t.Helper()
	if resp, _ := r.do("POST", "/api/login", map[string]string{"password": goodPassword}); resp.StatusCode != 200 {
		r.t.Fatalf("sign in: %d", resp.StatusCode)
	}
}

// watch follows the event stream until the test ends.
func (r *rig) watch(after, epoch int64) <-chan Event {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", r.ts.URL+"/api/events?epoch="+itoa(epoch)+"&after="+itoa(after), nil)
	resp, err := r.c.Do(req)
	if err != nil || resp.StatusCode != 200 {
		r.t.Fatalf("events: %v %v", err, resp)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		r.t.Fatalf("content type %q", ct)
	}
	out := make(chan Event, 1000)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev Event
				if json.Unmarshal([]byte(d), &ev) == nil {
					out <- ev
				}
			}
		}
	}()
	return out
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func (r *rig) state() (msgs []*Message, seq, epoch int64) {
	r.t.Helper()
	_, b := r.do("GET", "/api/state", nil)
	var s struct {
		Messages []*Message
		Seq      int64
		Epoch    int64
	}
	if err := json.Unmarshal(b, &s); err != nil {
		r.t.Fatalf("%s", b)
	}
	return s.Messages, s.Seq, s.Epoch
}

func until(t *testing.T, ch <-chan Event, what string, ok func(Event) bool) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ok(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("never saw %s", what)
		}
	}
}

func TestEveryRouteNeedsASignIn(t *testing.T) {
	r := newRig(t)
	for _, rt := range []struct{ m, p string }{
		{"GET", "/api/state"}, {"GET", "/api/settings"}, {"POST", "/api/settings"}, {"GET", "/api/events"}, {"POST", "/api/send"}, {"POST", "/api/approve"}, {"POST", "/api/presence"}, {"GET", "/api/today"},
		{"GET", "/api/control"}, {"GET", "/api/agents"}, {"POST", "/api/pause"}, {"GET", "/api/push/key"}, {"POST", "/api/push/subscribe"}, {"POST", "/api/push/unsubscribe"}, {"POST", "/api/logout"},
	} {
		resp, body := r.do(rt.m, rt.p, map[string]string{})
		if resp.StatusCode != 401 {
			t.Errorf("%s %s without signing in: %d %s", rt.m, rt.p, resp.StatusCode, body)
		}
	}
	// A stranger who changes the cookie to anything gets nowhere either.
	req, _ := http.NewRequest("GET", r.ts.URL+"/api/state", nil)
	req.AddCookie(&http.Cookie{Name: "tal_session", Value: strings.Repeat("a", 43)})
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 401 {
		t.Fatal("a made-up cookie was accepted")
	}
	for _, p := range []string{"/api/nonsense", "/api/../api/state"} {
		if resp, _ := r.do("GET", p, nil); resp.StatusCode == 200 {
			t.Errorf("%s answered", p)
		}
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")

	// Needs sign in
	resp, _ := r.do("GET", "/api/settings", nil)
	if resp.StatusCode != 401 {
		t.Errorf("GET /api/settings without sign in: %d", resp.StatusCode)
	}
	resp, _ = r.do("POST", "/api/settings", map[string]string{})
	if resp.StatusCode != 401 {
		t.Errorf("POST /api/settings without sign in: %d", resp.StatusCode)
	}

	r.login()

	// GET returns the current config
	resp, body := r.do("GET", "/api/settings", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/settings: %d %s", resp.StatusCode, body)
	}
	var view struct {
		Profiles []struct {
			Name string `json:"name"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("bad settings json: %v", err)
	}
	if len(view.Profiles) != 0 {
		t.Errorf("expected no profiles, got %d", len(view.Profiles))
	}

	// POST adds a profile (keys as the web app sends them: snake_case)
	resp, body = r.do("POST", "/api/settings", map[string]string{"name": "my-profile", "type": "openai", "api_key": "test-key-value", "base_url": "https://api.openai.com/v1", "model": "gpt-4o-mini"})
	if resp.StatusCode != 200 {
		t.Fatalf("POST /api/settings add: %d %s", resp.StatusCode, body)
	}
	if p := r.srv.Cfg.Profiles["my-profile"]; p.Type != "openai" || p.BaseURL != "https://api.openai.com/v1" || p.Model != "gpt-4o-mini" || p.APIKey != "test-key-value" {
		t.Fatalf("fields the app sent were not stored: %+v", p)
	}

	// GET now shows the profile
	resp, body = r.do("GET", "/api/settings", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/settings after add: %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("bad settings json: %v", err)
	}
	if len(view.Profiles) != 1 || view.Profiles[0].Name != "my-profile" {
		t.Errorf("expected my-profile, got %+v", view.Profiles)
	}

	// POST edits the profile
	resp, body = r.do("POST", "/api/settings", map[string]string{"name": "my-profile", "model": "gpt-4o"})
	if resp.StatusCode != 200 {
		t.Fatalf("POST /api/settings edit: %d %s", resp.StatusCode, body)
	}
	if p := r.srv.Cfg.Profiles["my-profile"]; p.Model != "gpt-4o" || p.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("an edit must change only what it names: %+v", p)
	}

	// DELETE the profile
	resp, body = r.do("POST", "/api/settings", map[string]any{"name": "my-profile", "_delete": true})
	if resp.StatusCode != 200 {
		t.Fatalf("POST /api/settings delete: %d %s", resp.StatusCode, body)
	}

	// GET shows no profiles again
	resp, body = r.do("GET", "/api/settings", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/settings after delete: %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("bad settings json: %v", err)
	}
	if len(view.Profiles) != 0 {
		t.Errorf("expected no profiles after delete, got %d", len(view.Profiles))
	}
}

func TestSettingsRejectsAnUnknownProfileType(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.login()
	resp, body := r.do("POST", "/api/settings", map[string]string{"name": "bad", "type": "anthropicc"})
	if resp.StatusCode != 400 || !strings.Contains(string(body), "unknown profile type") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, ok := r.srv.Cfg.Profiles["bad"]; ok {
		t.Fatal("a profile with a bad type was stored")
	}
}

func TestSettingsPrefersAKeyVariableOverAPastedKey(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.login()
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"name": "p", "type": "openai", "api_key": "pasted-key-value"}); resp.StatusCode != 200 {
		t.Fatalf("add: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"name": "p", "api_key_env": "MY_KEY"}); resp.StatusCode != 200 {
		t.Fatalf("edit: %d", resp.StatusCode)
	}
	got := r.srv.Cfg.Profiles["p"]
	if got.APIKeyEnv != "MY_KEY" || got.APIKey != "" {
		t.Fatalf("a named variable must replace a pasted key: %+v", got)
	}
}

// liveBackends wires the rig's engine to its config the way production does:
// profiles resolve from the config as it is now, providers are cached, and
// Forget drops the cache. It reports how often the cache was dropped.
func liveBackends(t *testing.T, cfg *config.Config) (*chat.Backends, *int) {
	t.Helper()
	cache := map[string]chat.Backend{}
	forgot := 0
	return &chat.Backends{
		Default: "main",
		Names:   []string{"main"},
		Open: func(name string) (chat.Backend, error) {
			if b, ok := cache[name]; ok {
				return b, nil
			}
			p, ok := cfg.Profiles[name]
			if !ok {
				return chat.Backend{}, errors.New("no profile named " + name)
			}
			if p.APIKey == "" && p.APIKeyEnv == "" {
				return chat.Backend{}, errors.New("needs an API key")
			}
			b := chat.Backend{Provider: scripted{}, Model: p.Model, Desc: p.Type + ", " + p.Model}
			cache[name] = b
			return b, nil
		},
		Forget: func() { forgot++; cache = map[string]chat.Backend{} },
	}, &forgot
}

func getSettings(t *testing.T, r *rig) (profiles []struct {
	Name      string `json:"name"`
	Active    bool   `json:"active"`
	Default   bool   `json:"default"`
	Available bool   `json:"available"`
	Status    string `json:"status"`
}, active string, presets int,
) {
	t.Helper()
	_, b := r.do("GET", "/api/settings", nil)
	var view struct {
		ActiveProfile string `json:"active_profile"`
		Profiles      []struct {
			Name      string `json:"name"`
			Active    bool   `json:"active"`
			Default   bool   `json:"default"`
			Available bool   `json:"available"`
			Status    string `json:"status"`
		} `json:"profiles"`
		Presets []struct {
			Label string `json:"label"`
		} `json:"presets"`
	}
	if err := json.Unmarshal(b, &view); err != nil {
		t.Fatalf("bad settings json: %v", err)
	}
	return view.Profiles, view.ActiveProfile, len(view.Presets)
}

// A profile added, changed or removed in Settings works at once: no restart.
func TestAProfileAddedInSettingsWorksWithoutARestart(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{Profiles: map[string]config.Profile{
		"main": {Type: "openai", Model: "m1", APIKey: "k"},
	}}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	backends, forgot := liveBackends(t, r.srv.Cfg)
	r.srv.Engine.Backends = backends
	r.login()

	profiles, active, presets := getSettings(t, r)
	if active != "main" || len(profiles) != 1 || !profiles[0].Active || !profiles[0].Default || !profiles[0].Available {
		t.Fatalf("at first: active=%q profiles=%+v", active, profiles)
	}
	if presets == 0 {
		t.Fatal("the settings page offers no provider presets")
	}

	if resp, _ := r.do("POST", "/api/settings", map[string]string{"name": "alt", "type": "openai", "model": "m2", "api_key": "k2"}); resp.StatusCode != 200 {
		t.Fatalf("add: %d", resp.StatusCode)
	}
	if *forgot == 0 {
		t.Fatal("saving did not drop the cached providers")
	}
	profiles, active, _ = getSettings(t, r)
	if len(profiles) != 2 || active != "main" {
		t.Fatalf("after adding: active=%q profiles=%+v", active, profiles)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"use_profile": "alt"}); resp.StatusCode != 200 {
		t.Fatalf("switch: %d", resp.StatusCode)
	}
	_, b := r.do("GET", "/api/state", nil)
	var st struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(b, &st); err != nil || st.Model != "m2" {
		t.Fatalf("the chat still reports %q", st.Model)
	}
	// An edit takes effect too, and removing a profile unplugs it.
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"name": "alt", "model": "m3"}); resp.StatusCode != 200 {
		t.Fatalf("edit: %d", resp.StatusCode)
	}
	profiles, _, _ = getSettings(t, r)
	for _, p := range profiles {
		if p.Name == "alt" && p.Status != "openai, m3" {
			t.Fatalf("after editing: %+v", p)
		}
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"name": "alt", "_delete": true}); resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"use_profile": "alt"}); resp.StatusCode != 400 {
		t.Fatalf("switching to a removed profile: %d", resp.StatusCode)
	}
}

func TestSwitchingModelsNeedsMoreThanOne(t *testing.T) {
	r := newRig(t)
	r.login()
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"use_profile": "x"}); resp.StatusCode != 503 {
		t.Fatalf("%d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"test_profile": "x"}); resp.StatusCode != 503 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

// chatFailer is a model that answers nothing: the key is dead.
type chatFailer struct{}

func (chatFailer) Chat(context.Context, provider.Request) (provider.Message, error) {
	return provider.Message{}, errors.New("the key was refused")
}

func TestProfilesAnswerWhenTested(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{Profiles: map[string]config.Profile{
		"main": {Type: "openai", Model: "m1", APIKey: "k"},
	}}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Engine.Backends = &chat.Backends{
		Default: "main",
		Open: func(name string) (chat.Backend, error) {
			switch name {
			case "main":
				return chat.Backend{Provider: scripted{}, Model: "m1"}, nil
			case "flaky":
				return chat.Backend{Provider: chatFailer{}, Model: "m9"}, nil
			}
			return chat.Backend{}, errors.New("no profile named " + name)
		},
	}
	r.login()

	resp, b := r.do("POST", "/api/settings", map[string]string{"test_profile": "main"})
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var got struct {
		Model string `json:"model"`
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal(b, &got); err != nil || got.Model != "m1" || got.Reply == "" {
		t.Fatalf("a working profile: %s %v", b, err)
	}
	for name, want := range map[string]int{"flaky": 502, "nope": 502} {
		if resp, b := r.do("POST", "/api/settings", map[string]string{"test_profile": name}); resp.StatusCode != want {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, b)
		}
	}
}

func TestProfileStrengthsRoundTrip(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.login()
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"name": "coder", "type": "openai", "model": "m", "api_key": "k", "best_for": []string{"code", "cheap"}}); resp.StatusCode != 200 {
		t.Fatalf("add: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"name": "bad", "best_for": []string{"fly"}}); resp.StatusCode != 400 {
		t.Fatalf("an unknown strength: %d", resp.StatusCode)
	}
	_, b := r.do("GET", "/api/settings", nil)
	var view struct {
		Strengths []string `json:"strengths"`
		Profiles  []struct {
			Name string   `json:"name"`
			Best []string `json:"best"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(b, &view); err != nil {
		t.Fatalf("bad settings json: %v", err)
	}
	if len(view.Strengths) != 2 || len(view.Profiles) != 1 || len(view.Profiles[0].Best) != 2 {
		t.Fatalf("%+v", view)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"name": "coder", "best_for": []string{}}); resp.StatusCode != 200 {
		t.Fatalf("clear: %d", resp.StatusCode)
	}
	if got := r.srv.Cfg.Profiles["coder"].BestFor; len(got) != 0 {
		t.Fatalf("clearing leaves %v", got)
	}
}

func TestTheMailboxIsManagedInSettings(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Getenv = func(string) string { return "" }
	r.login()

	if resp, _ := r.do("POST", "/api/settings", map[string]any{"email": map[string]string{"address": "nope"}}); resp.StatusCode != 400 {
		t.Fatalf("a bad address: %d", resp.StatusCode)
	}
	resp, b := r.do("POST", "/api/settings", map[string]any{"email": map[string]string{"address": "me@gmail.com", "password": "abcd efgh ijkl mnop"}})
	if resp.StatusCode != 200 {
		t.Fatalf("save: %d %s", resp.StatusCode, b)
	}
	var saved struct {
		OK          bool `json:"ok"`
		RestartNeed bool `json:"restart_needed"`
	}
	if err := json.Unmarshal(b, &saved); err != nil || !saved.OK || !saved.RestartNeed {
		t.Fatalf("a first mailbox needs a restart: %s", b)
	}
	if got := r.srv.Cfg.Email; got.Address != "me@gmail.com" || got.Password != "abcd efgh ijkl mnop" {
		t.Fatalf("the file keeps what was pasted: %+v", got)
	}

	_, b = r.do("GET", "/api/settings", nil)
	var view struct {
		Email struct {
			Address        string `json:"address"`
			HasPassword    bool   `json:"has_password"`
			PasswordSource string `json:"password_source"`
			Available      bool   `json:"available"`
			Status         string `json:"status"`
		} `json:"email"`
	}
	if err := json.Unmarshal(b, &view); err != nil {
		t.Fatalf("bad settings json: %v", err)
	}
	e := view.Email
	if e.Address != "me@gmail.com" || !e.HasPassword || e.PasswordSource != "settings" || !e.Available || !strings.Contains(e.Status, "me@gmail.com") {
		t.Fatalf("%+v", e)
	}

	// With a live mailbox, saving swaps the sign-in at once.
	r.srv.Mailbox = &mailbox.Service{}
	resp, b = r.do("POST", "/api/settings", map[string]any{"email": map[string]string{"address": "me@corp.example", "user": "me-login", "server": "mail.corp.example:1993", "password": "pw"}})
	if resp.StatusCode != 200 || strings.Contains(string(b), "restart_needed") {
		t.Fatalf("hot swap: %d %s", resp.StatusCode, b)
	}
	if got := r.srv.Mailbox; got.Address != "me@corp.example" || got.User != "me-login" || got.Server != "mail.corp.example:1993" {
		t.Fatalf("the live sign-in did not change: %+v", got)
	}
}

func TestCalendarFeedsAreManagedInSettings(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Calendars = &calendar.Service{}
	r.login()

	for name, body := range map[string]any{
		"bad name":      map[string]string{"op": "add", "name": "My Cal", "url": "https://x.example/a.ics"},
		"bad address":   map[string]string{"op": "add", "name": "a", "url": "http://x.example/a.ics"},
		"builtin":       map[string]string{"op": "delete", "name": "Talaria"},
		"unknown op":    map[string]string{"op": "rename", "name": "a"},
		"unknown entry": map[string]string{"op": "delete", "name": "a"},
	} {
		if resp, b := r.do("POST", "/api/settings", map[string]any{"calendar": body}); resp.StatusCode != 400 {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, b)
		}
	}
	if resp, b := r.do("POST", "/api/settings", map[string]any{"calendar": map[string]string{"op": "add", "name": "family", "url": "https://x.example/a.ics"}}); resp.StatusCode != 200 || strings.Contains(string(b), "restart_needed") {
		t.Fatalf("add: %d %s", resp.StatusCode, b)
	}
	if _, ok := r.srv.Cfg.Calendars["family"]; !ok {
		t.Fatal("not saved")
	}
	found := false
	for _, n := range r.srv.Calendars.Names() {
		found = found || n == "family"
	}
	if !found {
		t.Fatal("the live service does not list it")
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"calendar": map[string]string{"op": "add", "name": "family", "url": "https://x.example/b.ics"}}); resp.StatusCode != 400 {
		t.Fatalf("a duplicate: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"calendar": map[string]string{"op": "edit", "name": "family", "url": "https://x.example/b.ics"}}); resp.StatusCode != 200 {
		t.Fatalf("edit: %d", resp.StatusCode)
	}
	if r.srv.Calendars.Names()[len(r.srv.Calendars.Names())-1] == "" {
		t.Fatal("empty name listed")
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"calendar": map[string]string{"op": "delete", "name": "family"}}); resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, ok := r.srv.Cfg.Calendars["family"]; ok {
		t.Fatal("still saved")
	}
	for _, n := range r.srv.Calendars.Names() {
		if n == "family" {
			t.Fatal("the live service still lists it")
		}
	}
}

func TestChatTokensAreSavedForARestart(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.login()

	if resp, _ := r.do("POST", "/api/settings", map[string]any{"chat_token": map[string]string{"service": "sms", "token": "x"}}); resp.StatusCode != 400 {
		t.Fatalf("a bad service: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]any{"chat_token": map[string]string{"service": "telegram", "token": "t", "token_env": "T"}}); resp.StatusCode != 400 {
		t.Fatalf("both a token and a variable: %d", resp.StatusCode)
	}
	resp, b := r.do("POST", "/api/settings", map[string]any{"chat_token": map[string]string{"service": "telegram", "token": "tok"}})
	if resp.StatusCode != 200 || !strings.Contains(string(b), "restart_needed") {
		t.Fatalf("save: %d %s", resp.StatusCode, b)
	}
	if r.srv.Cfg.Telegram.Token != "tok" {
		t.Fatalf("%+v", r.srv.Cfg.Telegram)
	}
	resp, b = r.do("POST", "/api/settings", map[string]any{"chat_token": map[string]string{"service": "x", "token": "xtok"}})
	if resp.StatusCode != 200 || !strings.Contains(string(b), "restart_needed") {
		t.Fatalf("save x: %d %s", resp.StatusCode, b)
	}
	if r.srv.Cfg.X.Token != "xtok" {
		t.Fatalf("%+v", r.srv.Cfg.X)
	}
	_, b = r.do("GET", "/api/settings", nil)
	var view struct {
		Chats []struct {
			Service   string `json:"service"`
			Connected bool   `json:"connected"`
			Source    string `json:"source"`
		} `json:"chats"`
	}
	if err := json.Unmarshal(b, &view); err != nil {
		t.Fatalf("bad settings json: %v", err)
	}
	if len(view.Chats) != 3 || !view.Chats[0].Connected || view.Chats[0].Source != "settings" || view.Chats[1].Connected || !view.Chats[2].Connected || view.Chats[2].Source != "settings" {
		t.Fatalf("%+v", view.Chats)
	}
}

func TestConnectionsTestLive(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.login()

	if resp, _ := r.do("POST", "/api/settings", map[string]bool{"test_email": true}); resp.StatusCode != 400 {
		t.Fatalf("no mailbox: %d", resp.StatusCode)
	}
	r.srv.Mailbox = &mailbox.Service{Server: "127.0.0.1:1"}
	if resp, _ := r.do("POST", "/api/settings", map[string]bool{"test_email": true}); resp.StatusCode != 502 {
		t.Fatalf("an unreachable mailbox: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]string{"test_calendar": "nope"}); resp.StatusCode != 400 {
		t.Fatalf("no calendar: %d", resp.StatusCode)
	}
	r.srv.Calendars = &calendar.Service{
		Sources: []calendar.Source{{Name: "x", URL: "https://x.example/a.ics"}},
		Client:  func(calendar.Source) *http.Client { return http.DefaultClient },
	}
	if resp, b := r.do("POST", "/api/settings", map[string]string{"test_calendar": "nope"}); resp.StatusCode != 502 || !strings.Contains(string(b), "no calendar named") {
		t.Fatalf("an unknown feed: %d %s", resp.StatusCode, b)
	}
	if resp, _ := r.do("POST", "/api/settings", map[string]bool{"test_x": true}); resp.StatusCode != 400 {
		t.Fatalf("no X account: %d", resp.StatusCode)
	}
	xback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/users/me" {
			w.Write([]byte(`{"data":{"id":"42","name":"Ada","username":"ada"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":[{"message":"no such endpoint"}]}`))
	}))
	defer xback.Close()
	r.srv.X = &x.Service{Client: &x.Client{Token: "tok", BaseURL: xback.URL, HTTP: xback.Client()}}
	if resp, b := r.do("POST", "/api/settings", map[string]bool{"test_x": true}); resp.StatusCode != 200 || !strings.Contains(string(b), "Posting as @ada") {
		t.Fatalf("the X sign-in should verify: %d %s", resp.StatusCode, b)
	}
}

func TestThePageSeesWhichModelAnswered(t *testing.T) {
	r := newRig(t)
	r.srv.Engine.Backends = &chat.Backends{
		Default: "main",
		Open: func(name string) (chat.Backend, error) {
			if name != "main" {
				return chat.Backend{}, errors.New("no profile named " + name)
			}
			return chat.Backend{Provider: scripted{}, Model: "m1"}, nil
		},
	}
	r.login()
	_, seq, epoch := r.state()
	ch := r.watch(seq, epoch)
	r.do("POST", "/api/send", map[string]string{"text": "hello"})
	until(t, ch, "the run finishing with its model", func(e Event) bool {
		return e.Type == "run" && !e.On && e.Model == "m1"
	})
	_, b := r.do("GET", "/api/state", nil)
	var st struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(b, &st); err != nil || st.Model != "m1" {
		t.Fatalf("the state names %q: %s", st.Model, b)
	}
}

func TestEveryResponseCarriesTheProtections(t *testing.T) {
	r := newRig(t)
	for _, p := range []string{"/", "/app.js", "/app.css", "/sw.js", "/manifest.webmanifest", "/icons/icon-192.png", "/fonts/instrument.woff2", "/api/state", "/nothing-here"} {
		resp, _ := r.do("GET", p, nil)
		csp := resp.Header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "connect-src 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: policy lacks %q: %s", p, want, csp)
			}
		}
		if strings.Contains(csp, "unsafe") || strings.Contains(csp, "http") || strings.Contains(csp, "*") {
			t.Errorf("%s: policy allows too much: %s", p, csp)
		}
		for h, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cross-Origin-Opener-Policy": "same-origin"} {
			if resp.Header.Get(h) != want {
				t.Errorf("%s: %s is %q", p, h, resp.Header.Get(h))
			}
		}
	}
	if resp, _ := r.do("GET", "/api/state", nil); resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("an API answer can be cached")
	}
	if resp, _ := r.do("GET", "/sw.js", nil); resp.Header.Get("Service-Worker-Allowed") != "/" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("service worker headers: %v", resp.Header)
	}
	if resp, _ := r.do("GET", "/manifest.webmanifest", nil); !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/manifest+json") {
		t.Errorf("manifest type %q", resp.Header.Get("Content-Type"))
	}
	if resp, _ := r.do("GET", "/nothing-here", nil); resp.StatusCode != 404 {
		t.Errorf("%d", resp.StatusCode)
	}
	// Over https, the browser is told to keep using it.
	r.srv.Auth.Secure = true
	if resp, _ := r.do("GET", "/", nil); !strings.Contains(resp.Header.Get("Strict-Transport-Security"), "max-age=") {
		t.Error("no HSTS over https")
	}
}

// The page must stay inside its own policy and never turn text from outside into markup.
func TestThePageCannotRunOrShowAnythingItWasNotGiven(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("static", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	html, js, css, sw := read("index.html"), read("app.js"), read("app.css"), read("sw.js")
	inline := false
	for _, tag := range regexp.MustCompile(`(?i)<script[^>]*>`).FindAllString(html, -1) {
		if !strings.Contains(tag, "src=") {
			inline = true
		}
	}
	if inline || regexp.MustCompile(`(?i)\sstyle\s*=`).MatchString(html) || regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(html) {
		t.Error("the page has inline script or style, which its own policy forbids")
	}
	if regexp.MustCompile(`https?://`).MatchString(strings.ReplaceAll(strings.ReplaceAll(html, "http://www.w3.org", ""), "https://", "")) {
		t.Error("the page loads something from outside")
	}
	if strings.Count(js, "innerHTML") != 1 || strings.Contains(js, "outerHTML") || strings.Contains(js, "insertAdjacentHTML") || strings.Contains(js, "document.write") || strings.Contains(js, "eval(") || strings.Contains(js, "new Function") {
		t.Errorf("the script builds markup from strings (innerHTML used %d times, once is for fixed icons)", strings.Count(js, "innerHTML"))
	}
	if strings.Contains(js, "localStorage") || strings.Contains(js, "sessionStorage") || strings.Contains(js, "indexedDB") {
		t.Error("the script keeps things on the phone")
	}
	if strings.Contains(js, "setAttribute('style'") || strings.Contains(js, `setAttribute("style"`) {
		t.Error("an inline style attribute is set, which the policy blocks")
	}
	if regexp.MustCompile(`url\(\s*['"]?https?:`).MatchString(css) || strings.Contains(css, "@import") {
		t.Error("the stylesheet loads something from outside")
	}
	if strings.Contains(sw, "/api/") && !strings.Contains(sw, "startsWith('/api/')") {
		t.Error("the service worker does not leave /api alone")
	}
	if regexp.MustCompile(`cache\.(put|add)\([^)]*api`).MatchString(sw) {
		t.Error("the service worker keeps API answers")
	}
}

func TestSigningInAndOutAndBeingLockedOut(t *testing.T) {
	r := newRig(t)
	resp, body := r.do("POST", "/api/login", map[string]string{"password": "not it, not it at all"})
	if resp.StatusCode != 401 || !strings.Contains(string(body), "not right") || len(resp.Cookies()) != 0 {
		t.Fatalf("%d %s %v", resp.StatusCode, body, resp.Cookies())
	}
	if resp, _ := r.do("GET", "/api/state", nil); resp.StatusCode != 401 {
		t.Fatal("signed in by a wrong password")
	}
	r.login()
	if resp, _ := r.do("GET", "/api/state", nil); resp.StatusCode != 200 {
		t.Fatal("not signed in after signing in")
	}
	r.do("POST", "/api/logout", nil)
	if resp, _ := r.do("GET", "/api/state", nil); resp.StatusCode != 401 {
		t.Fatal("still signed in after signing out")
	}

	for i := 0; i < ipFailLimit; i++ {
		r.do("POST", "/api/login", map[string]string{"password": "wrong wrong wrong"})
	}
	resp, body = r.do("POST", "/api/login", map[string]string{"password": goodPassword})
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || !strings.Contains(string(body), "too many") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	// Signing in is a request that changes something, so it too must come from the app.
	if resp, _ := r.do("POST", "/api/login", map[string]string{"password": goodPassword}, "X-Talaria", ""); resp.StatusCode != 403 {
		t.Fatalf("a sign-in from outside the app: %d", resp.StatusCode)
	}
}

func TestRequestsThatChangeThingsMustComeFromTheApp(t *testing.T) {
	r := newRig(t)
	r.login()
	for name, hdr := range map[string][]string{
		"no marker":    {"X-Talaria", ""},
		"other origin": {"Origin", "https://evil.example"},
		"cross-site":   {"Sec-Fetch-Site", "cross-site"},
	} {
		for _, p := range []string{"/api/send", "/api/approve", "/api/pause", "/api/push/subscribe", "/api/logout"} {
			if resp, _ := r.do("POST", p, map[string]any{"text": "hi", "paused": true}, hdr...); resp.StatusCode != 403 {
				t.Errorf("%s %s: %d", name, p, resp.StatusCode)
			}
		}
	}
	if msgs, _, _ := r.state(); len(msgs) != 0 {
		t.Fatalf("a request from another site put something in the conversation: %+v", msgs)
	}
	if r.srv.Pause.Paused() {
		t.Fatal("a request from another site paused everything")
	}
}

func TestAMessageGetsAnAnswerAndCommandsAreNotPartOfTheConversation(t *testing.T) {
	r := newRig(t)
	r.login()
	_, seq, epoch := r.state()
	ch := r.watch(seq, epoch)

	if resp, _ := r.do("POST", "/api/send", map[string]string{"text": "  hello there  "}); resp.StatusCode != 204 {
		t.Fatalf("%d", resp.StatusCode)
	}
	until(t, ch, "the user's message", func(e Event) bool { return e.Type == "msg" && e.Msg.Role == "user" && e.Msg.Text == "hello there" })
	until(t, ch, "the agent starting", func(e Event) bool { return e.Type == "run" && e.On })
	until(t, ch, "the answer", func(e Event) bool {
		return e.Type == "msg" && e.Msg.Role == "assistant" && e.Msg.Text == "echo: hello there"
	})
	until(t, ch, "the agent finishing", func(e Event) bool { return e.Type == "run" && !e.On })

	r.do("POST", "/api/send", map[string]string{"text": "/new"})
	until(t, ch, "the reply to /new", func(e Event) bool { return e.Type == "msg" && strings.Contains(e.Msg.Text, "new conversation") })
	msgs, _, _ := r.state()
	for _, m := range msgs {
		if m.Role == "user" && strings.HasPrefix(m.Text, "/") {
			t.Fatalf("a command was recorded as something the user said: %+v", m)
		}
	}

	for name, body := range map[string]any{"empty": map[string]string{"text": "   "}, "missing": map[string]string{}, "bad": "not an object"} {
		if resp, _ := r.do("POST", "/api/send", body); resp.StatusCode != 400 {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if resp, _ := r.do("POST", "/api/send", map[string]string{"text": strings.Repeat("x", maxMessage+1)}); resp.StatusCode != 413 {
		t.Errorf("too long: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/send", map[string]string{"text": strings.Repeat("é", maxMessage)}); resp.StatusCode != 204 {
		t.Errorf("the longest allowed: %d", resp.StatusCode)
	}
	big := bytes.Repeat([]byte("x"), maxBody+10)
	req, _ := http.NewRequest("POST", r.ts.URL+"/api/send", bytes.NewReader(append([]byte(`{"text":"`), append(big, '"', '}')...)))
	req.Header.Set("X-Talaria", "1")
	if resp, _ := r.c.Do(req); resp.StatusCode != 400 {
		t.Errorf("a body larger than allowed: %d", resp.StatusCode)
	}
}

func TestANewChatStartsEmpty(t *testing.T) {
	r := newRig(t)
	r.login()
	_, seq, epoch := r.state()
	ch := r.watch(seq, epoch)
	r.do("POST", "/api/send", map[string]string{"text": "hello there"})
	until(t, ch, "the answer", func(e Event) bool {
		return e.Type == "msg" && e.Msg.Role == "assistant" && e.Msg.Text == "echo: hello there"
	})
	r.do("POST", "/api/send", map[string]string{"text": "/new"})
	until(t, ch, "being told to start again", func(e Event) bool { return e.Type == "reset" })
	until(t, ch, "the reply to /new", func(e Event) bool { return e.Type == "msg" && strings.Contains(e.Msg.Text, "new conversation") })
	msgs, _, _ := r.state()
	if len(msgs) != 1 || msgs[0].Role != "assistant" {
		t.Fatalf("the old conversation is still there: %+v", msgs)
	}
}

func TestACommandIsOnlyRunWhenAllowedInWords(t *testing.T) {
	r := newRig(t)
	r.login()
	_, seq, epoch := r.state()
	ch := r.watch(seq, epoch)
	r.do("POST", "/api/send", map[string]string{"text": "please run it now"})

	ask := until(t, ch, "the question", func(e Event) bool { return e.Type == "msg" && e.Msg.Role == "approval" && e.Msg.State == "pending" })
	if !strings.Contains(ask.Msg.Text, "ls -la") || ask.Msg.Token == "" || !strings.HasPrefix(ask.Msg.Text, "Allow shell?") {
		t.Fatalf("the question does not show what would run: %+v", ask.Msg)
	}
	time.Sleep(100 * time.Millisecond)
	if r.ran.Load() != 0 {
		t.Fatal("the command ran before anyone said yes")
	}
	if pend := r.hubPending(); len(pend) != 1 {
		t.Fatalf("%d waiting", len(pend))
	}

	// A wrong answer, a made-up token, and a token left over from a different question all do nothing.
	if resp, _ := r.do("POST", "/api/approve", map[string]string{"token": ask.Msg.Token, "choice": "yes please"}); resp.StatusCode != 400 {
		t.Errorf("a made-up answer: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/approve", map[string]string{"token": "0000000000000000", "choice": "allow"}); resp.StatusCode != 410 {
		t.Errorf("a made-up token: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/approve", map[string]string{"token": "", "choice": "allow"}); resp.StatusCode != 400 {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	if r.ran.Load() != 0 {
		t.Fatal("something ran without being allowed")
	}

	if resp, _ := r.do("POST", "/api/approve", map[string]string{"token": ask.Msg.Token, "choice": "allow"}); resp.StatusCode != 204 {
		t.Fatalf("%d", resp.StatusCode)
	}
	until(t, ch, "the question marked allowed", func(e Event) bool {
		return e.Type == "msg" && e.Msg.Role == "approval" && e.Msg.State == "allowed" && e.Msg.Token == ""
	})
	until(t, ch, "the tool's chip", func(e Event) bool {
		return e.Type == "msg" && e.Msg.Role == "tool" && e.Msg.Tool == "shell" && e.Msg.State == "ok"
	})
	until(t, ch, "the final answer", func(e Event) bool {
		return e.Type == "msg" && e.Msg.Role == "assistant" && e.Msg.Text == "done: listing"
	})
	if r.ran.Load() != 1 {
		t.Fatalf("ran %d times", r.ran.Load())
	}
	// The same answer again is too late, and does not run it again.
	if resp, _ := r.do("POST", "/api/approve", map[string]string{"token": ask.Msg.Token, "choice": "allow"}); resp.StatusCode != 410 {
		t.Errorf("an answer sent twice: %d", resp.StatusCode)
	}
	if r.ran.Load() != 1 {
		t.Fatal("it ran again")
	}
}

func (r *rig) hubPending() []*Message { return r.hub.Pending() }

func TestDenyingMeansItNeverRuns(t *testing.T) {
	r := newRig(t)
	r.login()
	_, seq, epoch := r.state()
	ch := r.watch(seq, epoch)
	r.do("POST", "/api/send", map[string]string{"text": "run it"})
	ask := until(t, ch, "the question", func(e Event) bool { return e.Type == "msg" && e.Msg.Role == "approval" && e.Msg.State == "pending" })
	r.do("POST", "/api/approve", map[string]string{"token": ask.Msg.Token, "choice": "deny"})
	until(t, ch, "denied", func(e Event) bool { return e.Type == "msg" && e.Msg.Role == "approval" && e.Msg.State == "denied" })
	until(t, ch, "the agent finishing", func(e Event) bool { return e.Type == "run" && !e.On })
	if r.ran.Load() != 0 {
		t.Fatal("a denied command ran")
	}
	// /stop while a question is waiting also ends it, unanswered.
	r.do("POST", "/api/send", map[string]string{"text": "run it"})
	ask = until(t, ch, "a second question", func(e Event) bool { return e.Type == "msg" && e.Msg.Role == "approval" && e.Msg.State == "pending" })
	r.do("POST", "/api/send", map[string]string{"text": "/stop"})
	until(t, ch, "the question closed", func(e Event) bool { return e.Type == "msg" && e.Msg.ID == ask.Msg.ID && e.Msg.State == "expired" })
	if resp, _ := r.do("POST", "/api/approve", map[string]string{"token": ask.Msg.Token, "choice": "allow"}); resp.StatusCode != 410 {
		t.Errorf("answering a question that was stopped: %d", resp.StatusCode)
	}
	time.Sleep(100 * time.Millisecond)
	if r.ran.Load() != 0 {
		t.Fatal("it ran")
	}
}

func TestAPageThatReconnectsGetsWhatItMissed(t *testing.T) {
	r := newRig(t)
	r.login()
	r.do("POST", "/api/send", map[string]string{"text": "first"})
	until(t, r.watch(0, 0), "the reset for an unknown run", func(e Event) bool { return e.Type == "reset" })
	_, seq, epoch := r.state()
	r.do("POST", "/api/send", map[string]string{"text": "second"})
	ch := r.watch(seq, epoch)
	until(t, ch, "what came after", func(e Event) bool { return e.Type == "msg" && e.Msg.Text == "echo: second" })
}

func TestTodayReadsEachSourceOnceAndKeepsOnlyCompleteAnswers(t *testing.T) {
	r := newRig(t)
	r.login()
	get := func(q string) map[string]any {
		resp, b := r.do("GET", "/api/today"+q, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("%d %s", resp.StatusCode, b)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	m := get("")
	if m["agendaOn"] != true || m["mailOn"] != true || len(m["agenda"].([]any)) != 1 || m["mail"].(map[string]any)["unread"].(float64) != 2 || len(m["jobs"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}
	if got := m["agenda"].([]any)[0].(map[string]any)["title"]; got != "<img src=x onerror=alert(1)>" {
		t.Fatalf("the title was changed on the way: %v", got) // shown as text by the page, never as markup
	}
	get("")
	if r.agenda.Load() != 1 || r.mail.Load() != 1 {
		t.Fatalf("read %d and %d times for two requests", r.agenda.Load(), r.mail.Load())
	}
	get("?fresh=1")
	if r.agenda.Load() != 2 {
		t.Fatal("refresh did not read again")
	}

	// A source that fails is reported, in its own section, and not remembered.
	var calls atomic.Int32
	r.srv.Sources.Agenda = func(context.Context) ([]AgendaItem, string, error) {
		if calls.Add(1) == 1 {
			return nil, "", errors.New("the calendar address was refused (HTTP 404)")
		}
		return nil, "", nil
	}
	r.srv.todayJSON = nil
	m = get("")
	if m["agendaError"] != "the calendar address was refused (HTTP 404)" || m["mailError"] != nil || m["mailOn"] != true {
		t.Fatalf("%v", m)
	}
	m = get("")
	if m["agendaError"] != nil || calls.Load() != 2 {
		t.Fatalf("a failed answer was kept: %v (%d reads)", m, calls.Load())
	}

	// With nothing connected, the page is told so rather than shown an empty day.
	r.srv.Sources = Sources{}
	r.srv.todayJSON = nil
	m = get("")
	if m["agendaOn"] != nil || m["mailOn"] != nil {
		t.Fatalf("%v", m)
	}
}

func TestControlShowsHowThingsAreAndPausingWorks(t *testing.T) {
	r := newRig(t)
	r.login()
	r.hub.Send(context.Background(), ChatID, "x")
	_, b := r.do("GET", "/api/control", nil)
	var c struct {
		Version, Model string
		UptimeSec      int64
		Paused         bool
		Activity       []int
		Jobs           []JobInfo
		Links          []LinkInfo
		Push           struct {
			Available bool
			Devices   int
		}
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("%s", b)
	}
	if c.Version != "v1" || c.Model != "test-model" || c.UptimeSec < 90*60-5 || c.Paused || len(c.Activity) != 24 || c.Activity[23] != 1 || len(c.Jobs) != 1 || len(c.Links) != 1 || !c.Push.Available {
		t.Fatalf("%+v", c)
	}
	resp, b := r.do("POST", "/api/pause", map[string]bool{"paused": true})
	if resp.StatusCode != 200 || !r.srv.Pause.Paused() || !strings.Contains(string(b), `"paused":true`) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	_, b = r.do("GET", "/api/state", nil)
	if !strings.Contains(string(b), `"paused":true`) {
		t.Fatalf("the page is not told everything is paused: %s", b)
	}
	r.do("POST", "/api/pause", map[string]bool{"paused": false})
	if r.srv.Pause.Paused() {
		t.Fatal("not resumed")
	}
	r.srv.Pause = nil
	if resp, _ := r.do("POST", "/api/pause", map[string]bool{"paused": true}); resp.StatusCode != 501 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestNotificationRoutesRefuseWhatTheServerShouldNotSendTo(t *testing.T) {
	r := newRig(t)
	r.login()
	resp, b := r.do("GET", "/api/push/key", nil)
	var k struct{ Key string }
	json.Unmarshal(b, &k)
	if resp.StatusCode != 200 || len(k.Key) != 87 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	ph := newPhone(t, "https://169.254.169.254/latest/meta-data")
	if resp, _ := r.do("POST", "/api/push/subscribe", ph.sub); resp.StatusCode != 400 {
		t.Fatalf("an address inside the network was accepted: %d", resp.StatusCode)
	}
	ph.sub.Endpoint = "https://fcm.googleapis.com/fcm/send/abc"
	if resp, _ := r.do("POST", "/api/push/subscribe", ph.sub); resp.StatusCode != 204 || r.srv.Push.Count() != 1 {
		t.Fatalf("%d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/push/unsubscribe", map[string]string{"endpoint": ph.sub.Endpoint}); resp.StatusCode != 204 || r.srv.Push.Count() != 0 {
		t.Fatalf("%d", resp.StatusCode)
	}
	r.srv.Push = nil
	if resp, _ := r.do("GET", "/api/push/key", nil); resp.StatusCode != 501 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestSayingThePageIsInViewKeepsNotificationsQuiet(t *testing.T) {
	r := newRig(t)
	r.login()
	if !r.hub.Away() {
		t.Fatal("nobody has looked yet")
	}
	r.do("POST", "/api/presence", map[string]bool{"visible": true})
	if r.hub.Away() {
		t.Fatal("the page said it is in view")
	}
	r.hub.mu.Lock()
	r.hub.seen = time.Now().Add(-time.Minute)
	r.hub.mu.Unlock()
	r.do("POST", "/api/presence", map[string]bool{"visible": false})
	if !r.hub.Away() {
		t.Fatal("a page that is not in view counted as present")
	}
}

func TestTheAddressBehindAProxyIsTheOneItAdded(t *testing.T) {
	s := &Server{TrustProxy: true}
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:5555"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := s.ip(req); got != "203.0.113.9" { // what the proxy saw, not what the client claimed first
		t.Fatalf("%s", got)
	}
	s.TrustProxy = false
	if got := s.ip(req); got != "10.0.0.2" {
		t.Fatalf("%s", got)
	}
}

// Installing has to be offered where people will see it, and explained where the browser offers no button.
func TestInstallingIsOfferedAtTheTopAndExplainedWhereTheBrowserHasNoButton(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("static", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	html, js := read("index.html"), read("app.js")
	for _, want := range []string{`id="install"`, `id="install-go"`, `id="install-close"`} {
		if !strings.Contains(html, want) {
			t.Errorf("the page has no %s", want)
		}
	}
	for _, want := range []string{"beforeinstallprompt", "appinstalled", "Install app", "Add to Home Screen", "renderInstall()"} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lost %q", want)
		}
	}
	var m struct {
		Display string
		Icons   []struct{ Sizes, Type, Purpose string }
		Start   string `json:"start_url"`
		Name    string
	}
	if err := json.Unmarshal([]byte(read("manifest.webmanifest")), &m); err != nil {
		t.Fatal(err)
	}
	has := func(size string) bool {
		for _, i := range m.Icons {
			if i.Sizes == size && i.Type == "image/png" && (i.Purpose == "" || strings.Contains(i.Purpose, "any")) {
				return true
			}
		}
		return false
	}
	if m.Name == "" || m.Start == "" || m.Display != "standalone" || !has("192x192") || !has("512x512") {
		t.Errorf("the manifest does not meet what Chrome needs to offer installing: %+v", m)
	}
}

func workersOf(ev Event) string {
	var out []string
	for _, w := range ev.Agents {
		out = append(out, w.Kind+":"+w.Label)
	}
	return strings.Join(out, ",")
}

// The agent hands a task to a helper, and the page can see both while the helper works.
func TestThePageSeesTheAgentAndItsHelperWhileTheyWork(t *testing.T) {
	r := newRig(t)
	r.login()
	_, seq, epoch := r.state()
	ch := r.watch(seq, epoch)
	r.do("POST", "/api/send", map[string]string{"text": "please delegate it"})

	until(t, ch, "the agent and its helper", func(e Event) bool { return e.Type == "agents" && workersOf(e) == "main:Talaria,sub:researcher" })

	// A page that opens now is told, too, and so is one that asks for the state.
	_, b := r.do("GET", "/api/state", nil)
	var st struct {
		Agents []struct{ Kind, Label, Doing string }
	}
	json.Unmarshal(b, &st)
	if len(st.Agents) != 2 || st.Agents[0].Kind != "main" || st.Agents[0].Doing != "delegate" || st.Agents[1].Kind != "sub" || st.Agents[1].Label != "researcher" {
		t.Fatalf("%s", b)
	}
	_, _, epoch2 := r.state()
	late := r.watch(0, epoch2)
	until(t, late, "the roster for a late page", func(e Event) bool { return e.Type == "agents" && workersOf(e) == "main:Talaria,sub:researcher" })

	msgs, _, _ := r.state()
	for _, m := range msgs {
		if m.Role == "tool" && (strings.Contains(m.Tool, "↳") || m.State == "running" && m.Tool != "delegate") {
			t.Fatalf("a helper's progress is shown as a chip of its own: %+v", m)
		}
	}

	close(r.gate) // the helper finishes
	until(t, ch, "the final answer", func(e Event) bool {
		return e.Type == "msg" && e.Msg.Role == "assistant" && e.Msg.Text == "done: found it"
	})
	until(t, ch, "nobody working", func(e Event) bool { return e.Type == "agents" && len(e.Agents) == 0 })
	msgs, _, _ = r.state()
	for _, m := range msgs {
		if m.Role == "tool" && m.State == "running" {
			t.Fatalf("a chip is still spinning after everything finished: %+v", m)
		}
	}
	if len(r.hub.Workers()) != 0 {
		t.Fatal("somebody is still shown working")
	}
}

func TestStoppingTheAgentClearsItsHelpersFromThePage(t *testing.T) {
	r := newRig(t)
	r.login()
	_, seq, epoch := r.state()
	ch := r.watch(seq, epoch)
	r.do("POST", "/api/send", map[string]string{"text": "delegate it"})
	until(t, ch, "the helper", func(e Event) bool { return e.Type == "agents" && workersOf(e) == "main:Talaria,sub:researcher" })
	r.do("POST", "/api/send", map[string]string{"text": "/stop"})
	until(t, ch, "nobody working after /stop", func(e Event) bool { return e.Type == "agents" && len(e.Agents) == 0 })
	if got := r.hub.Workers(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestThePageKnowsHowToShowWhoIsWorking(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("static", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{"ev.type === 'agents'", "setAgents(d.agents)", "function renderAgents", "function nowCard", "agents working", "S.agents.length"} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lost %q", want)
		}
	}
	h, _ := os.ReadFile(filepath.Join("static", "index.html"))
	if !strings.Contains(string(h), `id="agents"`) {
		t.Error("the page has nowhere to show who is working")
	}
}

func TestTheAgentsScreenGetsWhoIsWorkingWhatIsSetUpAndWhatFinished(t *testing.T) {
	r := newRig(t)
	r.login()
	id := r.hub.Start("sub", "price researcher")
	r.hub.Stop(id)
	r.hub.Start("watch", "mail from Amina")
	_, b := r.do("GET", "/api/agents", nil)
	var d struct {
		Agents []struct{ Kind, Label string }
		Recent []struct {
			Kind, Label string
			Seconds     int64
			Stopped     bool
		}
		Jobs []JobInfo
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%s", b)
	}
	if len(d.Agents) != 1 || d.Agents[0].Label != "mail from Amina" {
		t.Fatalf("working now: %s", b)
	}
	if len(d.Recent) != 1 || d.Recent[0].Kind != "sub" || d.Recent[0].Label != "price researcher" || d.Recent[0].Stopped {
		t.Fatalf("finished lately: %s", b)
	}
	if len(d.Jobs) != 1 || d.Jobs[0].Title != "g" {
		t.Fatalf("set up to run on their own: %s", b)
	}
}

func TestThePageHasAnAgentsTabWithALiveBadge(t *testing.T) {
	read := func(n string) string { b, _ := os.ReadFile(filepath.Join("static", n)); return string(b) }
	html, js := read("index.html"), read("app.js")
	for _, want := range []string{`data-tab="agents"`, `href="#agents"`, `id="screen-agents"`, `id="agents-body"`, `id="agent-badge"`} {
		if !strings.Contains(html, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	for _, want := range []string{"const TABS = ['chat', 'today', 'agents', 'settings'];", "loadAgentsPage", "/api/agents", "agent-badge"} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
}

// The chat thread must not move under a reader. These are the things that made it jump, and the pieces that stop it:
// they cannot all be tested without a browser, so what can be checked here is that none of the causes comes back.
func TestTheChatThreadHasNothingThatMovesTheReader(t *testing.T) {
	read := func(n string) string {
		b, err := os.ReadFile(filepath.Join("static", n))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	css, js := read("app.css"), read("app.js")

	// Messages drawn at a guessed height and corrected as they scroll into view shift everything around them.
	for _, bad := range []string{"content-visibility", "contain-intrinsic-size"} {
		if strings.Contains(css, bad) {
			t.Errorf("the stylesheet uses %s, which makes messages change height as they scroll into view", bad)
		}
	}
	if !regexp.MustCompile(`\.thread\{[^}]*overflow-anchor:none`).MatchString(css) {
		t.Error("the thread leaves scroll anchoring to the browser, which fights the page's own handling of the reader's place")
	}

	// The reader's place is kept by an explicit pin that only the reader's own scrolling changes.
	for _, want := range []string{"function setTop(v)", "function noticeReader()", "function followBottom()", "S.pinned = gapBelow(t) < 24", "const interacting = ", "anchorKey"} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lost %q", want)
		}
	}
	// Whether to follow the bottom must not be worked out from layout at each update.
	for _, bad := range []string{"< 90", "S.fresh", "restoreOff", "minHeight"} {
		if strings.Contains(js, bad) {
			t.Errorf("the script has %q again: a way of guessing at the reader's place from layout", bad)
		}
	}
	// A reload of the conversation (after a dropped connection) must reuse what is on screen, not rebuild it.
	i, j := strings.Index(js, "async function loadState()"), strings.Index(js, "function connect()")
	if i < 0 || j < i {
		t.Fatal("cannot find loadState")
	}
	body := js[i:j]
	for _, bad := range []string{".remove()", "blocks.clear()", "replaceChildren"} {
		if strings.Contains(body, bad) {
			t.Errorf("loadState uses %s, which throws away the conversation on screen and with it the reader's place", bad)
		}
	}
	// Every scroll the page does by itself is recorded, so the reader's scrolls can be told apart.
	for _, line := range strings.Split(js, "\n") {
		if strings.Contains(line, ".scrollTop =") && !strings.Contains(line, "t.scrollTop = v") && !strings.Contains(line, "t.scrollTop = t.") {
			if strings.Contains(line, "thread.scrollTop =") || strings.Contains(line, "threadEl().scrollTop =") {
				t.Errorf("the page scrolls the thread without recording it: %s", strings.TrimSpace(line))
			}
		}
	}
}

// recorder is a model that keeps what the user last said to it.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (m *recorder) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == provider.RoleUser {
			m.seen = append(m.seen, req.Messages[i].Content)
			break
		}
	}
	return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
}

func (m *recorder) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.seen) == 0 {
		return ""
	}
	return m.seen[len(m.seen)-1]
}

// "Also send the result to Telegram" reaches the agent as a request, out of sight, only where Telegram can be reached;
// the conversation on the page shows what the person wrote.
func TestSendAlsoToTelegramAsksTheAgentAndShowsNothingExtra(t *testing.T) {
	r := newRig(t)
	rec := &recorder{}
	eng := &chat.Engine{T: r.hub, Allowed: map[int64]bool{UserID: true}, EditInterval: 5 * time.Millisecond,
		NewAgent: func(_ int64, approve func(string, string) bool) *agent.Agent {
			return &agent.Agent{Provider: rec, Model: "m", Tools: tool.NewRegistry(), MaxSteps: 2, Approve: approve}
		}}
	eng.Start(context.Background())
	r.srv.Engine = eng
	r.login()

	if _, b := r.do("GET", "/api/state", nil); strings.Contains(string(b), `"telegram":true`) {
		t.Fatalf("Telegram is offered where it cannot be reached: %s", b)
	}
	r.do("POST", "/api/send", map[string]any{"text": "research frogs", "telegram": true})
	waitUntil(t, func() bool { return rec.last() != "" })
	if strings.Contains(rec.last(), "telegram_notify") {
		t.Fatalf("asked for Telegram where there is none: %q", rec.last())
	}

	r.srv.Telegram = true
	if _, b := r.do("GET", "/api/state", nil); !strings.Contains(string(b), `"telegram":true`) {
		t.Fatalf("Telegram is not offered: %s", b)
	}
	r.do("POST", "/api/send", map[string]any{"text": "research toads", "telegram": true})
	waitUntil(t, func() bool { return strings.Contains(rec.last(), "toads") })
	if got := rec.last(); !strings.HasPrefix(got, "research toads") || !strings.Contains(got, "telegram_notify") {
		t.Fatalf("the agent was not asked to send the result: %q", got)
	}
	msgs, _, _ := r.state()
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Text, "telegram_notify") {
			t.Fatalf("the page shows the hidden request: %q", m.Text)
		}
	}
	r.do("POST", "/api/send", map[string]any{"text": "plain one"})
	waitUntil(t, func() bool { return strings.Contains(rec.last(), "plain one") })
	if strings.Contains(rec.last(), "telegram_notify") {
		t.Fatalf("a message sent without the toggle asked for Telegram: %q", rec.last())
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// Settings says which calls ask first, saves a change, says when it needs a restart, and refuses nonsense.
func TestSettingsPermissionsModeRoundTrip(t *testing.T) {
	r := newRig(t)
	r.srv.Cfg = &config.Config{}
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Permissions = config.PermissionsDeletesAndPosts
	r.srv.Getenv = func(string) string { return "" }
	r.login()

	get := func() map[string]any {
		_, b := r.do("GET", "/api/settings", nil)
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	if m := get(); m["permissions"] != "deletes_and_posts" || m["permissions_running"] != "deletes_and_posts" {
		t.Fatalf("%v", m)
	}
	if resp, b := r.do("POST", "/api/settings", map[string]string{"permissions": "yolo"}); resp.StatusCode != 400 {
		t.Fatalf("nonsense accepted: %d %s", resp.StatusCode, b)
	}
	if resp, b := r.do("POST", "/api/settings", map[string]string{"permissions": "deletes_and_posts"}); resp.StatusCode != 200 || strings.Contains(string(b), "restart_needed") {
		t.Fatalf("no change needs no restart: %d %s", resp.StatusCode, b)
	}
	resp, b := r.do("POST", "/api/settings", map[string]string{"permissions": "strict"})
	if resp.StatusCode != 200 || !strings.Contains(string(b), "restart_needed") {
		t.Fatalf("a change applies after a restart: %d %s", resp.StatusCode, b)
	}
	if m := get(); m["permissions"] != "strict" || m["permissions_running"] != "deletes_and_posts" {
		t.Fatalf("%v", m)
	}
	raw, _ := os.ReadFile(r.srv.ConfigPath)
	if !strings.Contains(string(raw), `"permissions": "strict"`) {
		t.Fatalf("not saved: %s", raw)
	}
}

// The page has four places, each with a reason: the old Needs You and Control screens are inside Agents and Settings.
func TestThePageHasOnlyTheTabsThatEarnTheirPlace(t *testing.T) {
	read := func(n string) string { b, _ := os.ReadFile(filepath.Join("static", n)); return string(b) }
	html, js := read("index.html"), read("app.js")
	if got := regexp.MustCompile(`data-tab="(\w+)"`).FindAllStringSubmatch(html, -1); len(got) != 4 {
		t.Fatalf("the nav has %d tabs: %v", len(got), got)
	}
	for _, gone := range []string{`id="screen-needs"`, `id="screen-control"`, `data-tab="needs"`, `data-tab="control"`} {
		if strings.Contains(html, gone) {
			t.Errorf("the page still has %s", gone)
		}
	}
	for _, want := range []string{"waitingSection", "historyCard", "who(w)", "SETTINGS_PAGES", "panePermissions", "paneModels", "tab === 'needs' || tab === 'control'", "renderTg"} {
		if !strings.Contains(js, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
}
