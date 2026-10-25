package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// fakeTelegram serves getUpdates from a queue and records everything the bot sends.
type fakeTelegram struct {
	t       *testing.T
	updates chan []Update
	srv     *httptest.Server

	mu     sync.Mutex
	sent   []sentMsg
	edit   []string
	next   int
	events []event // every send and edit, in order

	offsets []int // the offset of every getUpdates poll, in order

	polling    atomic.Int32 // getUpdates requests currently open
	failEdits  atomic.Int32 // answer this many edits with a 429 first
	editCalls  atomic.Int32 // edits received, including refused ones
	retryAfter int
}

// event is one message the bot sent or edited.
type event struct {
	Kind string // "send" or "edit"
	ID   int
	Text string
	KB   bool
}

type sentMsg struct {
	ChatID   int64
	Text     string
	HasKB    bool
	Callback []string
}

func newFake(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{t: t, updates: make(chan []Update, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var p struct {
		ChatID      int64  `json:"chat_id"`
		MessageID   int    `json:"message_id"`
		Text        string `json:"text"`
		Offset      int    `json:"offset"`
		ReplyMarkup *struct {
			Rows [][]struct {
				Data string `json:"callback_data"`
			} `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	json.NewDecoder(r.Body).Decode(&p)
	reply := func(result any) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}

	switch method {
	case "getUpdates":
		f.polling.Add(1)
		defer f.polling.Add(-1)
		f.mu.Lock()
		f.offsets = append(f.offsets, p.Offset)
		f.mu.Unlock()
		select {
		case b := <-f.updates:
			reply(b)
		case <-time.After(50 * time.Millisecond):
			reply([]Update{})
		case <-r.Context().Done():
		}
	case "sendMessage":
		m := sentMsg{ChatID: p.ChatID, Text: p.Text}
		if p.ReplyMarkup != nil {
			m.HasKB = true
			for _, row := range p.ReplyMarkup.Rows {
				for _, b := range row {
					m.Callback = append(m.Callback, b.Data)
				}
			}
		}
		f.mu.Lock()
		f.sent = append(f.sent, m)
		f.next++
		id := f.next
		f.events = append(f.events, event{"send", id, p.Text, m.HasKB})
		f.mu.Unlock()
		reply(map[string]any{"message_id": id})
	case "editMessageText":
		f.editCalls.Add(1)
		if f.failEdits.Add(-1) >= 0 {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 429,
				"description": "Too Many Requests: retry after 1", "parameters": map[string]any{"retry_after": 1}})
			return
		}
		f.mu.Lock()
		f.edit = append(f.edit, p.Text)
		f.events = append(f.events, event{"edit", p.MessageID, p.Text, false})
		f.mu.Unlock()
		reply(true)
	default:
		reply(true)
	}
}

func (f *fakeTelegram) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		out = append(out, m.Text)
	}
	return out
}

func (f *fakeTelegram) waitFor(desc string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %s; sent=%q", desc, f.texts())
}

func (f *fakeTelegram) sawText(substr string) bool {
	for _, s := range f.texts() {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func userMsg(updateID int, from, chatID int64, chatType, text string) Update {
	return Update{UpdateID: updateID, Message: &Message{
		MessageID: updateID, From: &User{ID: from}, Chat: Chat{ID: chatID, Type: chatType}, Text: text,
	}}
}

// scripted replays canned assistant messages.
type scripted struct {
	mu      sync.Mutex
	replies []provider.Message
}

func (s *scripted) Chat(context.Context, provider.Request) (provider.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.replies) == 0 {
		return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

// start runs a gateway against the fake; agents are built by newAgent. opts
// adjust the gateway before it starts. The returned func stops it early.
func start(t *testing.T, f *fakeTelegram, allowed []int64, newAgent func(int64, func(string, string) bool) *agent.Agent, opts ...func(*Gateway)) func() {
	api := NewAPI("TESTTOKEN")
	api.Base = f.srv.URL
	set := map[int64]bool{}
	for _, id := range allowed {
		set[id] = true
	}
	g := &Gateway{API: api, Allowed: set, NewAgent: newAgent, PollTimeout: 1, ApprovalTimeout: 2 * time.Second}
	for _, opt := range opts {
		opt(g)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.Run(ctx); close(done) }()
	stop := func() {
		cancel()
		<-done
		// The gateway's last poll may still be open on the fake server for a moment,
		// and an update sent now could be swallowed by it. Telegram would redeliver an
		// update nobody acknowledged; the fake does not, so wait for the poll to end.
		deadline := time.Now().Add(2 * time.Second)
		for f.polling.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Cleanup(stop)
	return stop
}

func echoAgent(p provider.Provider, calls *atomic.Int32) func(int64, func(string, string) bool) *agent.Agent {
	return func(_ int64, approve func(string, string) bool) *agent.Agent {
		calls.Add(1)
		reg := tool.NewRegistry()
		tool.RegisterBuiltins(reg)
		return &agent.Agent{Provider: p, Model: "m", Tools: reg, Approve: approve}
	}
}

func TestAllowlistAndChatTypes(t *testing.T) {
	f := newFake(t)
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(&scripted{replies: []provider.Message{{Role: provider.RoleAssistant, Content: "hello back"}}}, &calls))

	f.updates <- []Update{
		userMsg(1, 99, 99, "private", "hack the planet"), // stranger: ignored silently
		userMsg(2, 99, 99, "private", "/whoami"),         // stranger: gets their own ID only
		userMsg(3, 1, -500, "group", "hi from a group"),  // allowed user but not private: ignored
		userMsg(4, 1, 1, "private", "hi"),
	}
	f.waitFor("reply to allowed user", func() bool { return f.sawText("hello back") })

	if !f.sawText("Your Telegram user ID is 99") {
		t.Fatalf("stranger should be told their ID: %q", f.texts())
	}
	if f.sawText("hack the planet") || len(f.texts()) != 2 {
		t.Fatalf("unexpected traffic: %q", f.texts())
	}
	if calls.Load() != 1 {
		t.Fatalf("agent built %d times, want 1 (only for the allowed private chat)", calls.Load())
	}
}

func TestSetupModeNeverRunsAgent(t *testing.T) {
	f := newFake(t)
	var calls atomic.Int32
	start(t, f, nil, echoAgent(&scripted{}, &calls))

	f.updates <- []Update{userMsg(1, 5, 5, "private", "run rm -rf"), userMsg(2, 5, 5, "private", "/start")}
	f.waitFor("setup-mode reply", func() bool { return f.sawText("Your Telegram user ID is 5") })
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("agent must not run in setup mode")
	}
}

func TestApprovalButtons(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string // callback suffix
		written bool
		verdict string
	}{
		{"allow", "y", true, "Allowed."},
		{"deny", "n", false, "Denied."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			target := filepath.Join(t.TempDir(), "out.txt")
			args, _ := json.Marshal(map[string]string{"path": target, "content": "hi"})
			p := &scripted{replies: []provider.Message{
				{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: string(args)}}},
				{Role: provider.RoleAssistant, Content: "finished"},
			}}
			var calls atomic.Int32
			start(t, f, []int64{1}, echoAgent(p, &calls))

			f.updates <- []Update{userMsg(1, 1, 1, "private", "write the file")}
			var prompt sentMsg
			f.waitFor("approval prompt", func() bool {
				f.mu.Lock()
				defer f.mu.Unlock()
				for _, m := range f.sent {
					if m.HasKB {
						prompt = m
						return true
					}
				}
				return false
			})
			if !strings.Contains(prompt.Text, "write_file") || !strings.Contains(prompt.Text, "path: "+target) {
				t.Fatalf("prompt should show the tool and arguments: %q", prompt.Text)
			}
			if _, err := os.Stat(target); err == nil {
				t.Fatal("file written before approval")
			}

			// A stranger pressing the button must not count.
			f.updates <- []Update{{UpdateID: 2, CallbackQuery: &CallbackQuery{
				ID: "x", From: User{ID: 99}, Data: prompt.Callback[0],
				Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}, Text: prompt.Text},
			}}}
			time.Sleep(150 * time.Millisecond)
			if _, err := os.Stat(target); err == nil {
				t.Fatal("stranger approved a tool call")
			}

			data := prompt.Callback[0][:strings.LastIndex(prompt.Callback[0], ":")+1] + tc.answer
			f.updates <- []Update{{UpdateID: 3, CallbackQuery: &CallbackQuery{
				ID: "y", From: User{ID: 1}, Data: data,
				Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}, Text: prompt.Text},
			}}}
			f.waitFor("final reply", func() bool { return f.sawText("finished") })

			_, err := os.Stat(target)
			if (err == nil) != tc.written {
				t.Fatalf("file written=%v, want %v", err == nil, tc.written)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.edit) == 0 || !strings.HasSuffix(f.edit[len(f.edit)-1], tc.verdict) {
				t.Fatalf("prompt message should be edited to show %q: %q", tc.verdict, f.edit)
			}
		})
	}
}

func TestChoiceButtons(t *testing.T) {
	f := newFake(t)
	panel := "question aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:\nWhen? (pick one, 2 options \u2014 reply with the number or name):\n1. Morning\n2. Afternoon"
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, Content: panel},
		{Role: provider.RoleAssistant, Content: "got it"},
	}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls))

	f.updates <- []Update{userMsg(1, 1, 1, "private", "when should we go?")}
	var prompt sentMsg
	f.waitFor("choice prompt", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, m := range f.sent {
			if m.HasKB {
				prompt = m
				return true
			}
		}
		return false
	})
	if len(prompt.Callback) != 2 {
		t.Fatalf("two options make two buttons: %+v", prompt)
	}
	for _, d := range prompt.Callback {
		if _, _, ok := chat.ParseChoice(d); !ok {
			t.Fatalf("button data is not a choice: %q", d)
		}
	}

	// Tapping the second button records the pick where the agent sees it.
	f.updates <- []Update{{UpdateID: 2, CallbackQuery: &CallbackQuery{
		ID: "c", From: User{ID: 1}, Data: prompt.Callback[1],
		Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}, Text: prompt.Text},
	}}}
	f.waitFor("recorded reply", func() bool { return f.sawText("got it") })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.edit) == 0 || !strings.HasSuffix(f.edit[len(f.edit)-1], "Picked option 2.") {
		t.Fatalf("prompt message should show the pick: %q", f.edit)
	}
}

func TestApprovalTimeoutDenies(t *testing.T) {
	f := newFake(t)
	target := filepath.Join(t.TempDir(), "out.txt")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "hi"})
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: string(args)}}},
		{Role: provider.RoleAssistant, Content: "gave up"},
	}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls)) // ApprovalTimeout is 2s in start()

	f.updates <- []Update{userMsg(1, 1, 1, "private", "write")}
	f.waitFor("reply after timeout", func() bool { return f.sawText("gave up") })
	if _, err := os.Stat(target); err == nil {
		t.Fatal("unanswered approval must deny")
	}
}

func TestNewResetsConversation(t *testing.T) {
	f := newFake(t)
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(&scripted{}, &calls))

	f.updates <- []Update{userMsg(1, 1, 1, "private", "one")}
	f.waitFor("first reply", func() bool { return f.sawText("ok") })
	f.updates <- []Update{userMsg(2, 1, 1, "private", "/new")}
	f.waitFor("reset ack", func() bool { return f.sawText("new conversation") })
	f.updates <- []Update{userMsg(3, 1, 1, "private", "two")}
	f.waitFor("second agent", func() bool { return calls.Load() == 2 })
}

func TestAPIErrorsNeverLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	api := NewAPI("SECRET:TOKEN")
	api.Base = srv.URL
	srv.Close() // connection refused: net/http puts the full URL in the error

	_, err := api.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SECRET:TOKEN") {
		t.Fatalf("token leaked: %v", err)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	}))
	defer srv2.Close()
	api.Base = srv2.URL
	if _, err := api.GetMe(context.Background()); err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("bad token should surface Telegram's message, got %v", err)
	}
}
