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
	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

// scripted is a model that answers from a script: "run it" makes it ask to run a command, and after the
// command it says it is done; anything else it repeats back.
type scripted struct{}

func (scripted) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	last := req.Messages[len(req.Messages)-1]
	say := func(s string) (provider.Message, error) {
		if req.OnText != nil {
			req.OnText(s)
		}
		return provider.Message{Role: provider.RoleAssistant, Content: s}, nil
	}
	if last.Role == provider.RoleTool {
		return say("done: " + last.Content)
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
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t}
	dir := t.TempDir()
	r.hub = &Hub{Path: filepath.Join(dir, "log.json")}
	reg := tool.NewRegistry()
	reg.Register(tool.Tool{Name: "shell", Description: "x", Schema: json.RawMessage(`{"type":"object"}`), NeedsApproval: true,
		Run: func(context.Context, json.RawMessage) (string, error) { r.ran.Add(1); return "listing", nil }})
	eng := &chat.Engine{T: r.hub, Allowed: map[int64]bool{UserID: true}, Stream: true, EditInterval: 5 * time.Millisecond,
		NewAgent: func(_ int64, approve func(string, string) bool) *agent.Agent {
			return &agent.Agent{Provider: scripted{}, Model: "m", Tools: reg, MaxSteps: 4, Approve: approve}
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
		{"GET", "/api/state"}, {"GET", "/api/events"}, {"POST", "/api/send"}, {"POST", "/api/approve"}, {"POST", "/api/presence"}, {"GET", "/api/today"},
		{"GET", "/api/control"}, {"POST", "/api/pause"}, {"GET", "/api/push/key"}, {"POST", "/api/push/subscribe"}, {"POST", "/api/push/unsubscribe"}, {"POST", "/api/logout"},
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
