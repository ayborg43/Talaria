package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

// namedModel answers "from <name>" and remembers the conversations it was shown.
type namedModel struct {
	name  string
	block chan struct{} // if set, the first call waits for this to close

	mu   sync.Mutex
	reqs [][]provider.Message
}

func (m *namedModel) Chat(ctx context.Context, req provider.Request) (provider.Message, error) {
	m.mu.Lock()
	m.reqs = append(m.reqs, append([]provider.Message(nil), req.Messages...))
	block := m.block
	m.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return provider.Message{}, ctx.Err()
		}
	}
	return provider.Message{Role: provider.RoleAssistant, Content: "from " + m.name}, nil
}

func (m *namedModel) lastRequest() []provider.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reqs[len(m.reqs)-1]
}

// backends offers profiles a and b, and a profile c that cannot be used.
func testBackends(a, b *namedModel) *chat.Backends {
	return &chat.Backends{
		Default: "a",
		Names:   []string{"a", "b", "c"},
		Open: func(name string) (chat.Backend, error) {
			switch name {
			case "a":
				return chat.Backend{Provider: a, Model: "model-a", Desc: "openai, model-a"}, nil
			case "b":
				return chat.Backend{Provider: b, Model: "model-b", Desc: "anthropic, model-b"}, nil
			}
			return chat.Backend{}, errors.New("needs an API key")
		},
	}
}

// agentOn builds chat agents that start out on the given default model.
func agentOn(def provider.Provider) func(int64, func(string, string) bool) *agent.Agent {
	return func(_ int64, approve func(string, string) bool) *agent.Agent {
		reg := tool.NewRegistry()
		tool.RegisterBuiltins(reg)
		return &agent.Agent{Provider: def, Model: "model-a", Tools: reg, Approve: approve}
	}
}

func withBackends(b *chat.Backends, prefs chat.ChatPrefs) func(*Gateway) {
	return func(g *Gateway) { g.Backends, g.Prefs = b, prefs }
}

func (f *fakeTelegram) count(substr string) int {
	n := 0
	for _, s := range f.texts() {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

func say(f *fakeTelegram, id int, from, chat int64, text string) {
	f.updates <- []Update{userMsg(id, from, chat, "private", text)}
}

func TestModelListSwitchAndKeepTheConversation(t *testing.T) {
	f := newFake(t)
	a, b := &namedModel{name: "a"}, &namedModel{name: "b"}
	start(t, f, []int64{1}, agentOn(a), withBackends(testBackends(a, b), nil))

	say(f, 1, 1, 1, "hi")
	f.waitFor("a's reply", func() bool { return f.count("from a") == 1 })

	say(f, 2, 1, 1, "/model")
	f.waitFor("the listing", func() bool { return f.sawText("This chat uses: a") })
	var list string
	for _, s := range f.texts() {
		if strings.Contains(s, "Profiles:") {
			list = s
		}
	}
	for _, want := range []string{
		"a (openai, model-a)  <- this chat  [default]",
		"b (anthropic, model-b)",
		"c (unavailable: needs an API key)",
		"/model <profile>",
	} {
		if !strings.Contains(list, want) {
			t.Errorf("listing lacks %q:\n%s", want, list)
		}
	}
	if strings.Contains(list, "b (anthropic, model-b)  <-") {
		t.Errorf("only the current profile is marked:\n%s", list)
	}

	say(f, 3, 1, 1, "/model b")
	f.waitFor("the switch", func() bool { return f.sawText("Switched to b (anthropic, model-b)") })
	say(f, 4, 1, 1, "again")
	f.waitFor("b's reply", func() bool { return f.count("from b") == 1 })

	// The conversation carried over: b was shown the earlier exchange.
	var got []string
	for _, m := range b.lastRequest() {
		got = append(got, string(m.Role)+":"+m.Content)
	}
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "user:hi") || !strings.Contains(joined, "assistant:from a") || !strings.HasSuffix(joined, "user:again") {
		t.Fatalf("switching must keep the history: %s", joined)
	}

	// An unusable or unknown profile changes nothing.
	say(f, 5, 1, 1, "/model c")
	f.waitFor("the refusal", func() bool { return f.sawText("Cannot switch to c: needs an API key") })
	say(f, 6, 1, 1, "/model nope")
	f.waitFor("the unknown-profile message", func() bool { return f.sawText(`There is no profile named "nope"`) })
	say(f, 7, 1, 1, "still b?")
	f.waitFor("b again", func() bool { return f.count("from b") == 2 })

	say(f, 8, 1, 1, "/model default")
	f.waitFor("the return to the default", func() bool { return f.sawText("Switched to a (openai, model-a)") })
	say(f, 9, 1, 1, "back on a?")
	f.waitFor("a again", func() bool { return f.count("from a") == 2 })
}

func TestModelChoiceIsPerChat(t *testing.T) {
	f := newFake(t)
	a, b := &namedModel{name: "a"}, &namedModel{name: "b"}
	start(t, f, []int64{1, 2}, agentOn(a), withBackends(testBackends(a, b), nil))

	say(f, 1, 1, 1, "/model b")
	f.waitFor("the switch", func() bool { return f.sawText("Switched to b") })
	say(f, 2, 2, 2, "hello from the other chat")
	f.waitFor("chat 2's reply", func() bool { return f.count("from a") == 1 })
	if f.count("from b") != 0 {
		t.Fatal("another chat's choice must not leak")
	}
}

func TestModelChoiceSurvivesARestart(t *testing.T) {
	f := newFake(t)
	prefs := &chat.FilePrefs{Path: filepath.Join(t.TempDir(), "chats.json")}
	a, b := &namedModel{name: "a"}, &namedModel{name: "b"}

	stop := start(t, f, []int64{1}, agentOn(a), withBackends(testBackends(a, b), prefs))
	say(f, 1, 1, 1, "/model b")
	f.waitFor("the switch", func() bool { return f.sawText("Switched to b") })
	stop()

	// A new gateway process, same preferences file.
	start(t, f, []int64{1}, agentOn(a), withBackends(testBackends(a, b), &chat.FilePrefs{Path: prefs.Path}))
	say(f, 2, 1, 1, "hello again")
	f.waitFor("b's reply after the restart", func() bool { return f.count("from b") == 1 })
	if f.count("from a") != 0 {
		t.Fatal("the saved choice should apply from the first message")
	}

	// Going back to the default clears the saved choice.
	say(f, 3, 1, 1, "/model default")
	f.waitFor("the return to the default", func() bool { return f.sawText("Switched to a") })
	if got := prefs.Profile(1); got != "" {
		t.Fatalf("the default is stored as no choice, got %q", got)
	}
}

func TestModelSavedProfileThatStoppedWorkingFallsBack(t *testing.T) {
	f := newFake(t)
	prefs := &chat.FilePrefs{Path: filepath.Join(t.TempDir(), "chats.json")}
	if err := prefs.SetProfile(1, "c"); err != nil { // c exists but cannot be used now
		t.Fatal(err)
	}
	a, b := &namedModel{name: "a"}, &namedModel{name: "b"}
	start(t, f, []int64{1}, agentOn(a), withBackends(testBackends(a, b), prefs))

	say(f, 1, 1, 1, "hi")
	f.waitFor("the reply", func() bool { return f.count("from a") == 1 })
	if !f.sawText("The c profile cannot be used now (needs an API key), so I'm using a.") {
		t.Fatalf("the user should be told why the model changed: %q", f.texts())
	}
	if prefs.Profile(1) != "" {
		t.Fatal("the unusable choice should be cleared so the notice is not repeated")
	}
}

func TestModelCommandWithoutBackends(t *testing.T) {
	f := newFake(t)
	a := &namedModel{name: "a"}
	start(t, f, []int64{1}, agentOn(a))

	say(f, 1, 1, 1, "/model")
	f.waitFor("the explanation", func() bool { return f.sawText("Model switching is not set up") })
	say(f, 2, 1, 1, "/help")
	f.waitFor("the help", func() bool { return f.sawText("Talaria is ready") })
	if f.sawText("/model") && f.count("/model shows") > 0 {
		t.Fatal("help should not advertise a command that is not available")
	}

	f2 := newFake(t)
	start(t, f2, []int64{1}, agentOn(a), withBackends(testBackends(a, &namedModel{name: "b"}), nil))
	say(f2, 1, 1, 1, "/help")
	f2.waitFor("the help", func() bool { return f2.sawText("/model shows or changes the model") })
}

func TestModelCommandIsAllowlisted(t *testing.T) {
	f := newFake(t)
	a, b := &namedModel{name: "a"}, &namedModel{name: "b"}
	start(t, f, []int64{1}, agentOn(a), withBackends(testBackends(a, b), nil))

	say(f, 1, 99, 99, "/model b") // a stranger
	say(f, 2, 1, 1, "/whoami")
	f.waitFor("the owner's reply", func() bool { return f.sawText("Your Telegram user ID is 1") })
	if got := f.texts(); len(got) != 1 {
		t.Fatalf("a stranger gets no answer from /model: %q", got)
	}
}

func TestModelSwitchDuringATaskTakesEffectNextMessage(t *testing.T) {
	f := newFake(t)
	release := make(chan struct{})
	a, b := &namedModel{name: "a", block: release}, &namedModel{name: "b"}
	start(t, f, []int64{1}, agentOn(a), withBackends(testBackends(a, b), nil))

	say(f, 1, 1, 1, "slow task")
	f.waitFor("the task to start", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.reqs) == 1 })
	say(f, 2, 1, 1, "/model b")
	f.waitFor("the switch", func() bool { return f.sawText("finishes on the old one") })

	close(release)
	f.waitFor("the running task to finish on a", func() bool { return f.count("from a") == 1 })
	say(f, 3, 1, 1, "next")
	f.waitFor("b's reply", func() bool { return f.count("from b") == 1 })
}

func TestFilePrefs(t *testing.T) {
	p := &chat.FilePrefs{Path: filepath.Join(t.TempDir(), "sub", "chats.json")}
	if p.Profile(1) != "" {
		t.Fatal("no file, no preference")
	}
	if err := p.SetProfile(1, "claude"); err != nil {
		t.Fatal(err)
	}
	if err := p.SetProfile(2, "local"); err != nil {
		t.Fatal(err)
	}
	if p.Profile(1) != "claude" || p.Profile(2) != "local" || p.Profile(3) != "" {
		t.Fatal("each chat keeps its own choice")
	}
	if err := p.SetProfile(1, ""); err != nil || p.Profile(1) != "" || p.Profile(2) != "local" {
		t.Fatalf("clearing one chat must not touch another: %v", err)
	}

	// A damaged file is reported on write and never silently overwritten.
	os.WriteFile(p.Path, []byte("{broken"), 0o600)
	if err := p.SetProfile(1, "x"); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("got %v", err)
	}
	if raw, _ := os.ReadFile(p.Path); string(raw) != "{broken" {
		t.Fatal("the damaged file was overwritten")
	}
	if p.Profile(2) != "" {
		t.Fatal("a damaged file reads as no preference")
	}
}

func TestRateLimitRetryInTheAPIClient(t *testing.T) {
	var attempts atomic.Int32
	limited := func(retryAfter int, times int32) *httptest.Server {
		attempts.Store(0)
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if attempts.Add(1) <= times {
				w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":` + strconv.Itoa(retryAfter) + `}}`))
				return
			}
			w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
		}))
	}
	newAPI := func(srv *httptest.Server, waits *[]time.Duration) *API {
		api := NewAPI("TOKEN")
		api.Base = srv.URL
		api.RetrySleep = func(_ context.Context, d time.Duration) error { *waits = append(*waits, d); return nil }
		return api
	}

	var waits []time.Duration
	srv := limited(2, 2)
	id, err := newAPI(srv, &waits).SendMessage(context.Background(), 1, "hi", nil)
	srv.Close()
	if err != nil || id != 7 || len(waits) != 2 || waits[0] != 2*time.Second {
		t.Fatalf("a rate limit should be waited out and retried: id=%d err=%v waits=%v", id, err, waits)
	}

	waits = nil
	srv = limited(2, 99)
	_, err = newAPI(srv, &waits).SendMessage(context.Background(), 1, "hi", nil)
	srv.Close()
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != 429 || ae.RetryAfter != 2*time.Second || attempts.Load() != maxRateLimitTries {
		t.Fatalf("it should give up after %d tries with a typed error: %v (attempts %d)", maxRateLimitTries, err, attempts.Load())
	}

	waits = nil
	srv = limited(3600, 99) // an hour is too long to stand in the way of a chat
	_, err = newAPI(srv, &waits).SendMessage(context.Background(), 1, "hi", nil)
	srv.Close()
	if err == nil || attempts.Load() != 1 || len(waits) != 0 {
		t.Fatalf("a very long wait is not retried: err=%v attempts=%d waits=%v", err, attempts.Load(), waits)
	}

	// Other errors are not retried at all.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`))
	}))
	defer bad.Close()
	attempts.Store(0)
	waits = nil
	if _, err := newAPI(bad, &waits).SendMessage(context.Background(), 1, "hi", nil); err == nil || attempts.Load() != 1 {
		t.Fatalf("a 400 is final: %v attempts=%d", err, attempts.Load())
	}
}
