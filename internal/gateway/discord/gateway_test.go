package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// fakeDiscord is Discord's REST API and Gateway in one server. It records what
// the bot does and lets a test play the part of users.
type fakeDiscord struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	next      int
	seq       int64
	conn      *serverConn
	sent      []sentMsg
	events    []event // every send and edit, in order
	callbacks []callback
	opened    []string // users whose DM was opened
	typing    atomic.Int32
	token     string // the token the bot must present; "" accepts any
}

type sentMsg struct {
	Channel, ID, Text string
	Buttons           []string // custom_ids
	Parse             any      // allowed_mentions.parse as sent
	HasMentions       bool
}

type event struct {
	Kind, ID, Text string
	ClearedButtons bool
}

type callback struct {
	ID, Token string
	Type      int
	Content   string
	Flags     int
	Cleared   bool
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{t: t, next: 1000}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDiscord) wsURL() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/gw" }

func (f *fakeDiscord) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/gw") {
		f.gateway(w, r)
		return
	}
	if f.token != "" && r.Header.Get("Authorization") != "Bot "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"401: Unauthorized","code":0}`))
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "GET" && r.URL.Path == "/users/@me":
		w.Write([]byte(`{"id":"900","username":"talbot","bot":true}`))
	case r.Method == "GET" && r.URL.Path == "/gateway/bot":
		fmt.Fprintf(w, `{"url":%q}`, f.wsURL())
	case r.Method == "POST" && r.URL.Path == "/users/@me/channels":
		uid, _ := body["recipient_id"].(string)
		f.opened = append(f.opened, uid)
		fmt.Fprintf(w, `{"id":"chan-%s"}`, uid)
	case r.Method == "POST" && len(parts) == 3 && parts[0] == "channels" && parts[2] == "messages":
		f.next++
		id := strconv.Itoa(f.next)
		m := sentMsg{Channel: parts[1], ID: id, Text: fmt.Sprint(body["content"])}
		if am, ok := body["allowed_mentions"].(map[string]any); ok {
			m.HasMentions, m.Parse = true, am["parse"]
		}
		if rows, ok := body["components"].([]any); ok {
			for _, row := range rows {
				for _, b := range row.(map[string]any)["components"].([]any) {
					m.Buttons = append(m.Buttons, b.(map[string]any)["custom_id"].(string))
				}
			}
		}
		f.sent = append(f.sent, m)
		f.events = append(f.events, event{Kind: "send", ID: id, Text: m.Text})
		fmt.Fprintf(w, `{"id":%q,"channel_id":%q}`, id, parts[1])
	case r.Method == "PATCH" && len(parts) == 4 && parts[0] == "channels":
		comps, has := body["components"].([]any)
		f.events = append(f.events, event{Kind: "edit", ID: parts[3], Text: fmt.Sprint(body["content"]), ClearedButtons: has && len(comps) == 0})
		w.Write([]byte(`{}`))
	case r.Method == "POST" && len(parts) == 3 && parts[0] == "channels" && parts[2] == "typing":
		f.typing.Add(1)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "POST" && len(parts) == 4 && parts[0] == "interactions":
		data, _ := body["data"].(map[string]any)
		cb := callback{ID: parts[1], Token: parts[2], Type: int(body["type"].(float64)), Content: fmt.Sprint(data["content"])}
		if fl, ok := data["flags"].(float64); ok {
			cb.Flags = int(fl)
		}
		if comps, ok := data["components"].([]any); ok && len(comps) == 0 {
			cb.Cleared = true
		}
		f.callbacks = append(f.callbacks, cb)
		w.WriteHeader(http.StatusNoContent)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeDiscord) gateway(w http.ResponseWriter, r *http.Request) {
	c, err := upgrade(w, r)
	if err != nil {
		return
	}
	defer c.drop()
	hello(c, 30000)
	m, err := c.recv() // identify
	if err != nil {
		return
	}
	if d, _ := m["d"].(map[string]any); f.token != "" && d["token"] != f.token {
		c.close(4004)
		return
	}
	ready(c, f.bumpSeq(), "")
	f.mu.Lock()
	f.conn = c
	f.mu.Unlock()
	serve(c, nil)
}

func (f *fakeDiscord) bumpSeq() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	return f.seq
}

func (f *fakeDiscord) connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn != nil
}

// push delivers a gateway event to the bot.
func (f *fakeDiscord) push(event string, d any) {
	f.t.Helper()
	f.waitFor("the bot to connect", f.connected)
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	if err := c.send(opDispatch, d, f.bumpSeq(), event); err != nil {
		f.t.Fatalf("push %s: %v", event, err)
	}
}

// dm plays a user sending the bot a direct message.
func (f *fakeDiscord) dm(user int64, text string) {
	f.t.Helper()
	f.push("MESSAGE_CREATE", map[string]any{
		"id": "1", "channel_id": fmt.Sprintf("chan-%d", user), "content": text,
		"author": map[string]any{"id": strconv.FormatInt(user, 10), "username": "u"},
	})
}

// press plays a user pressing a button on a message in a DM.
func (f *fakeDiscord) press(user int64, channel, customID, content string) {
	f.t.Helper()
	f.push("INTERACTION_CREATE", map[string]any{
		"id": "ix-" + customID, "token": "itoken", "type": 3, "channel_id": channel,
		"user":    map[string]any{"id": strconv.FormatInt(user, 10)},
		"data":    map[string]any{"custom_id": customID},
		"message": map[string]any{"id": "5", "content": content},
	})
}

func (f *fakeDiscord) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		out = append(out, m.Text)
	}
	return out
}

func (f *fakeDiscord) eventLog() []event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]event(nil), f.events...)
}

func (f *fakeDiscord) callbackLog() []callback {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]callback(nil), f.callbacks...)
}

func (f *fakeDiscord) waitFor(desc string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %s; sent=%q", desc, f.texts())
}

func (f *fakeDiscord) sawText(sub string) bool {
	for _, s := range f.texts() {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// promptWithButtons waits for an approval prompt and returns it.
func (f *fakeDiscord) promptWithButtons() sentMsg {
	f.t.Helper()
	var prompt sentMsg
	f.waitFor("an approval prompt", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, m := range f.sent {
			if len(m.Buttons) > 0 {
				prompt = m
				return true
			}
		}
		return false
	})
	return prompt
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

func echoAgent(p provider.Provider, calls *atomic.Int32) func(int64, func(string, string) bool) *agent.Agent {
	return func(_ int64, approve func(string, string) bool) *agent.Agent {
		calls.Add(1)
		reg := tool.NewRegistry()
		tool.RegisterBuiltins(reg)
		return &agent.Agent{Provider: p, Model: "m", Tools: reg, Approve: approve}
	}
}

// start runs a gateway against the fake. Its connection to the fake is made
// through Discord's own REST route for the gateway address.
func start(t *testing.T, f *fakeDiscord, allowed []int64, newAgent func(int64, func(string, string) bool) *agent.Agent, opts ...func(*Gateway)) (*Gateway, context.CancelFunc, chan error) {
	api := NewAPI("TESTTOKEN")
	api.Base = f.srv.URL
	f.token = "TESTTOKEN"
	set := map[int64]bool{}
	for _, id := range allowed {
		set[id] = true
	}
	g := &Gateway{API: api, Allowed: set, NewAgent: newAgent, ApprovalTimeout: 2 * time.Second, Logf: t.Logf,
		Backoff: func(int) time.Duration { return 10 * time.Millisecond }}
	for _, opt := range opts {
		opt(g)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("the gateway did not stop")
		}
	})
	return g, cancel, done
}

func TestOnlyDirectMessagesFromAllowedUsersAreServed(t *testing.T) {
	f := newFakeDiscord(t)
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(&scripted{replies: []provider.Message{{Role: provider.RoleAssistant, Content: "hello back"}}}, &calls))

	f.dm(99, "hack the planet")              // a stranger: ignored silently
	f.dm(99, "!whoami")                      // a stranger may learn their own ID
	f.push("MESSAGE_CREATE", map[string]any{ // allowed user, but in a server channel
		"id": "2", "channel_id": "serverchan", "guild_id": "g1", "content": "hi from a server",
		"author": map[string]any{"id": "1", "username": "u"},
	})
	f.push("MESSAGE_CREATE", map[string]any{ // another bot
		"id": "3", "channel_id": "chan-5", "content": "beep",
		"author": map[string]any{"id": "1", "bot": true}, // even if the ID is one that is allowed
	})
	f.dm(1, "   ") // nothing to say
	f.dm(1, "hi")
	f.waitFor("the reply to the allowed user", func() bool { return f.sawText("hello back") })

	if !f.sawText("Your Discord user ID is 99") {
		t.Fatalf("a stranger should be told their ID: %q", f.texts())
	}
	if !f.sawText("TALARIA_DISCORD_ALLOWED_USERS") {
		t.Fatalf("and told where the owner adds it: %q", f.texts())
	}
	if f.sawText("hack the planet") || len(f.texts()) != 2 {
		t.Fatalf("unexpected traffic: %q", f.texts())
	}
	if calls.Load() != 1 {
		t.Fatalf("agent built %d times, want 1", calls.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.sent {
		if !m.HasMentions || fmt.Sprint(m.Parse) != "[]" {
			t.Fatalf("every message must suppress mentions: %+v", m)
		}
	}
}

func TestSetupModeNeverRunsAgent(t *testing.T) {
	f := newFakeDiscord(t)
	var calls atomic.Int32
	start(t, f, nil, echoAgent(&scripted{}, &calls))

	f.dm(5, "run rm -rf")
	f.dm(5, "/start") // the slash form works too
	f.waitFor("the setup reply", func() bool { return f.sawText("Your Discord user ID is 5") })
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("agent must not run in setup mode")
	}
}

// Users type "!new"; the gateway's help says so. A message that merely starts
// with "!" is just a message.
func TestBangCommands(t *testing.T) {
	f := newFakeDiscord(t)
	var calls atomic.Int32
	var mu sync.Mutex
	var prompts []string
	p := providerFunc(func(req provider.Request) provider.Message {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, req.Messages[len(req.Messages)-1].Content)
		return provider.Message{Role: provider.RoleAssistant, Content: "ok"}
	})
	start(t, f, []int64{1}, echoAgent(p, &calls))

	f.dm(1, "!help")
	f.waitFor("help", func() bool { return f.sawText("Talaria is ready") })
	if !f.sawText("!new starts a fresh conversation") || !f.sawText("!stop cancels") || f.sawText("/new") {
		t.Fatalf("help should use this service's prefix: %q", f.texts())
	}

	f.dm(1, "!important: the build is red")
	f.waitFor("a plain message to reach the model", func() bool { mu.Lock(); defer mu.Unlock(); return len(prompts) == 1 })
	if prompts[0] != "!important: the build is red" {
		t.Fatalf("got %q", prompts[0])
	}

	f.dm(1, "!NEW")
	f.waitFor("the reset", func() bool { return f.sawText("Started a new conversation") })
	f.dm(1, "again")
	f.waitFor("a second agent", func() bool { return calls.Load() == 2 })
}

type providerFunc func(provider.Request) provider.Message

func (p providerFunc) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	return p(req), nil
}

func TestApprovalButtons(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allow   bool
		verdict string
	}{{"allow", true, "Allowed."}, {"deny", false, "Denied."}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDiscord(t)
			target := filepath.Join(t.TempDir(), "out.txt")
			args, _ := json.Marshal(map[string]string{"path": target, "content": "hi"})
			p := &scripted{replies: []provider.Message{
				{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: string(args)}}},
				{Role: provider.RoleAssistant, Content: "finished"},
			}}
			var calls atomic.Int32
			start(t, f, []int64{1}, echoAgent(p, &calls))

			f.dm(1, "write the file")
			prompt := f.promptWithButtons()
			if !strings.Contains(prompt.Text, "write_file") || !strings.Contains(prompt.Text, "path: "+target) {
				t.Fatalf("the prompt should show the tool and its arguments: %q", prompt.Text)
			}
			if !strings.Contains(prompt.Text, "```") {
				t.Fatalf("Discord renders Markdown, so the arguments are shown as code: %q", prompt.Text)
			}
			if len(prompt.Buttons) != 2 || !strings.HasSuffix(prompt.Buttons[0], ":y") || !strings.HasSuffix(prompt.Buttons[1], ":n") {
				t.Fatalf("buttons: %v", prompt.Buttons)
			}
			if _, err := os.Stat(target); err == nil {
				t.Fatal("file written before approval")
			}

			// Someone else pressing the button must not count, and is told so privately.
			f.press(99, prompt.Channel, prompt.Buttons[0], prompt.Text)
			f.waitFor("the refusal", func() bool { return len(f.callbackLog()) == 1 })
			cb := f.callbackLog()[0]
			if cb.Type != 4 || cb.Flags != 64 || cb.Content != chat.MsgNotAllowed {
				t.Fatalf("a stranger's press should get a private refusal: %+v", cb)
			}
			// So must a press that comes from a channel the prompt was not sent to.
			f.press(1, "some-other-channel", prompt.Buttons[0], prompt.Text)
			f.waitFor("the second refusal", func() bool { return len(f.callbackLog()) == 2 })
			if cb := f.callbackLog()[1]; cb.Content != chat.MsgNotAllowed {
				t.Fatalf("%+v", cb)
			}
			time.Sleep(100 * time.Millisecond)
			if _, err := os.Stat(target); err == nil {
				t.Fatal("an outsider approved a tool call")
			}

			button := prompt.Buttons[0]
			if !tc.allow {
				button = prompt.Buttons[1]
			}
			f.press(1, prompt.Channel, button, prompt.Text)
			f.waitFor("the final reply", func() bool { return f.sawText("finished") })

			if _, err := os.Stat(target); (err == nil) != tc.allow {
				t.Fatalf("file written=%v, want %v", err == nil, tc.allow)
			}
			cb = f.callbackLog()[2]
			if cb.Type != 7 || !cb.Cleared || cb.Content != prompt.Text+"\n\n"+tc.verdict {
				t.Fatalf("the prompt should be updated in place, without buttons, to show %q: %+v", tc.verdict, cb)
			}

			// The same button again is stale.
			f.press(1, prompt.Channel, button, prompt.Text)
			f.waitFor("the stale press to be answered", func() bool { return len(f.callbackLog()) == 4 })
			if cb := f.callbackLog()[3]; cb.Content != chat.MsgExpired || cb.Flags != 64 {
				t.Fatalf("%+v", cb)
			}
		})
	}
}

func TestApprovalTimeoutDeniesAndRemovesTheButtons(t *testing.T) {
	f := newFakeDiscord(t)
	target := filepath.Join(t.TempDir(), "out.txt")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "hi"})
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: string(args)}}},
		{Role: provider.RoleAssistant, Content: "gave up"},
	}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls), func(g *Gateway) { g.ApprovalTimeout = 150 * time.Millisecond })

	f.dm(1, "write")
	f.waitFor("the reply after the timeout", func() bool { return f.sawText("gave up") })
	if _, err := os.Stat(target); err == nil {
		t.Fatal("an unanswered approval must deny")
	}
	var edit event
	for _, e := range f.eventLog() {
		if e.Kind == "edit" {
			edit = e
		}
	}
	if !strings.HasSuffix(edit.Text, "Timed out, denied.") || !edit.ClearedButtons {
		t.Fatalf("the prompt should say it timed out and stop offering the buttons: %+v", edit)
	}
}

// A scheduled job can deliver to someone who has not written since the bot
// started: the DM channel is opened for them. Long results are split, and a
// code block cut by the split stays a code block in both messages.
func TestSendOpensTheDirectMessageAndSplits(t *testing.T) {
	f := newFakeDiscord(t)
	g, _, _ := start(t, f, []int64{77}, echoAgent(&scripted{}, new(atomic.Int32)))

	var code []string
	for i := 0; i < 150; i++ {
		code = append(code, fmt.Sprintf("line %03d of the report", i))
	}
	text := "Report:\n```\n" + strings.Join(code, "\n") + "\n```\nDone."
	g.Send(context.Background(), 77, text)

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.opened) != 1 || f.opened[0] != "77" {
		t.Fatalf("the DM should be opened once: %v", f.opened)
	}
	if len(f.sent) < 2 {
		t.Fatalf("expected the report in several messages, got %d", len(f.sent))
	}
	lines := 0
	for i, m := range f.sent {
		if m.Channel != "chan-77" {
			t.Fatalf("message %d went to %s", i, m.Channel)
		}
		if n := len([]rune(m.Text)); n > 2000 {
			t.Fatalf("message %d has %d characters; Discord's limit is 2000", i, n)
		}
		if strings.Count(m.Text, "```")%2 != 0 {
			t.Fatalf("message %d leaves a code block open: %q", i, m.Text)
		}
		lines += strings.Count(m.Text, "of the report")
	}
	if lines != 150 {
		t.Fatalf("%d of 150 lines delivered", lines)
	}
	if !strings.HasSuffix(f.sent[len(f.sent)-1].Text, "Done.") {
		t.Fatalf("the end is missing: %q", f.sent[len(f.sent)-1].Text)
	}
}

// Streaming: one message is sent, then edited as the reply grows; typing shows meanwhile.
func TestStreamedReplyIsEditedInPlace(t *testing.T) {
	f := newFakeDiscord(t)
	var full strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&full, "word%02d ", i)
	}
	want := strings.TrimSpace(full.String())
	p := &streamer{chunks: chunked(want, 14), pause: 30 * time.Millisecond}
	start(t, f, []int64{1}, echoAgent(p, new(atomic.Int32)), func(g *Gateway) {
		g.Stream, g.EditInterval = true, 30*time.Millisecond
	})

	f.dm(1, "tell me")
	f.waitFor("the finished reply", func() bool {
		ev := f.eventLog()
		return len(ev) > 0 && ev[len(ev)-1].Text == want
	})
	ev := f.eventLog()
	if ev[0].Kind != "send" {
		t.Fatalf("%+v", ev[0])
	}
	edits := 0
	for _, e := range ev {
		if e.ID != ev[0].ID {
			t.Fatalf("a streamed reply stays in one message: %+v", ev)
		}
		if e.Kind == "edit" {
			edits++
		}
	}
	if edits < 2 {
		t.Fatalf("the message should be edited as the reply grows, saw %d edits", edits)
	}
	if !strings.HasSuffix(ev[0].Text, chat.StreamCursor) || strings.HasSuffix(ev[len(ev)-1].Text, chat.StreamCursor) {
		t.Fatalf("working shows a cursor; done does not: first %q last %q", ev[0].Text, ev[len(ev)-1].Text)
	}
	if f.typing.Load() == 0 {
		t.Fatal("the bot should show that it is typing while it works")
	}
}

type streamer struct {
	chunks []string
	pause  time.Duration
}

func (p *streamer) Chat(ctx context.Context, req provider.Request) (provider.Message, error) {
	var b strings.Builder
	for _, c := range p.chunks {
		if req.OnText != nil {
			req.OnText(c)
		}
		b.WriteString(c)
		time.Sleep(p.pause)
	}
	return provider.Message{Role: provider.RoleAssistant, Content: b.String()}, nil
}

func chunked(s string, size int) []string {
	r := []rune(s)
	var out []string
	for len(r) > 0 {
		n := min(size, len(r))
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return out
}

func TestModelCommandUsesTheBangPrefix(t *testing.T) {
	f := newFakeDiscord(t)
	b := &chat.Backends{Default: "a", Names: []string{"a", "b"}}
	b.Open = func(name string) (chat.Backend, error) {
		return chat.Backend{Provider: &scripted{}, Model: "m-" + name, Desc: name}, nil
	}
	start(t, f, []int64{1}, echoAgent(&scripted{}, new(atomic.Int32)), func(g *Gateway) { g.Backends = b })
	f.dm(1, "!model")
	f.waitFor("the model listing", func() bool { return f.sawText("Switch with !model <profile>") })
	f.dm(1, "!model b")
	f.waitFor("the switch", func() bool { return f.sawText("b") && len(f.texts()) >= 2 })
}

// A rejected token ends the gateway with an error naming the cause, rather than
// retrying forever.
func TestRunReturnsAnErrorForARejectedToken(t *testing.T) {
	f := newFakeDiscord(t)
	api := NewAPI("WRONG")
	api.Base = f.srv.URL
	f.token = "RIGHT"
	g := &Gateway{API: api, Allowed: map[int64]bool{1: true}, Backoff: func(int) time.Duration { return time.Millisecond }}
	done := make(chan error, 1)
	go func() { done <- g.Run(context.Background()) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "token") {
			t.Fatalf("%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the gateway kept retrying with a bad token")
	}
}

// Anyone may message the bot, so what it remembers about strangers is bounded;
// people past the bound still get their answer, just with a lookup each time.
func TestStrangersAreNotRememberedWithoutLimit(t *testing.T) {
	old := maxRemembered
	maxRemembered = 3
	defer func() { maxRemembered = old }()

	f := newFakeDiscord(t)
	g, _, _ := start(t, f, nil, echoAgent(&scripted{}, new(atomic.Int32)))
	for uid := int64(1); uid <= 6; uid++ {
		g.Send(context.Background(), uid, "hello")
	}
	g.Send(context.Background(), 6, "again") // past the bound: looked up again
	g.Send(context.Background(), 1, "again") // within it: remembered

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 8 {
		t.Fatalf("every message is still delivered: %d", len(f.sent))
	}
	if len(f.opened) != 7 { // six first contacts, plus 6 again, but not 1
		t.Fatalf("opened %v", f.opened)
	}
	g.tr.mu.Lock()
	defer g.tr.mu.Unlock()
	if len(g.tr.channels) != 3 || len(g.tr.users) != 3 {
		t.Fatalf("kept %d channels and %d users, want 3", len(g.tr.channels), len(g.tr.users))
	}
}
