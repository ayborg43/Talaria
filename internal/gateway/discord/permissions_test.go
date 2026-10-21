package discord

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"talaria/internal/agent"
	"talaria/internal/gateway/chat"
	"talaria/internal/guard"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

// guarded builds agents whose tool calls go through a real Guard, as in the app.
func guarded(p provider.Provider, g *guard.Guard) func(int64, func(string, string) bool) *agent.Agent {
	return func(chatID int64, approve func(string, string) bool) *agent.Agent {
		reg := tool.NewRegistry()
		tool.RegisterBuiltins(reg)
		return &agent.Agent{Provider: p, Model: "m", Tools: reg, Approve: approve, Gate: g.For("chat", "discord", chatID)}
	}
}

func newGuard(t *testing.T, rules map[string]string) *guard.Guard {
	t.Helper()
	pol, err := guard.ParsePolicy(rules, []string{"shell"}) // these tests use write_file as an ordinary rememberable tool
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return &guard.Guard{
		Policy: pol,
		Grants: &guard.Grants{Path: filepath.Join(dir, "grants.json")},
		Audit:  &guard.Log{Path: filepath.Join(dir, "audit.log")},
		Redact: guard.NewRedactor([]string{"hunter2hunter2"}),
	}
}

func withPermissions(g *guard.Guard) func(*Gateway) {
	return func(gw *Gateway) { gw.Permissions = guard.ChatPermissions{G: g, Gateway: "discord"} }
}

func writeCall(path, content string) provider.Message {
	args, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: string(args)}}}
}

func say(s string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: s}
}

func countPrompts(f *fakeDiscord) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.sent {
		if len(m.Buttons) > 0 {
			n++
		}
	}
	return n
}

// Choosing Always lets that tool through in that chat without asking again,
// until the chat takes it back.
func TestAlwaysAllowIsRememberedUntilReset(t *testing.T) {
	f := newFakeDiscord(t)
	target := filepath.Join(t.TempDir(), "out.txt")
	g := newGuard(t, nil)
	p := &scripted{replies: []provider.Message{
		writeCall(target, "one"), say("first done"),
		writeCall(target, "two"), say("second done"),
		writeCall(target, "three"), say("third done"),
	}}
	start(t, f, []int64{1}, guarded(p, g), withPermissions(g))

	f.dm(1, "write it")
	prompt := f.promptWithButtons()
	if len(prompt.Buttons) != 3 || !strings.HasSuffix(prompt.Buttons[0], ":y") || !strings.HasSuffix(prompt.Buttons[1], ":a") || !strings.HasSuffix(prompt.Buttons[2], ":n") {
		t.Fatalf("Allow, Always and Deny: %v", prompt.Buttons)
	}
	f.press(1, prompt.Channel, prompt.Buttons[1], prompt.Text)
	f.waitFor("the first reply", func() bool { return f.sawText("first done") })
	if b, _ := os.ReadFile(target); string(b) != "one" {
		t.Fatalf("file %q", b)
	}
	cb := f.callbackLog()[0]
	if cb.Content != prompt.Text+"\n\n"+chat.Verdict(chat.ChoiceAlways) || !cb.Cleared {
		t.Fatalf("%+v", cb)
	}

	// The next write needs no one.
	f.dm(1, "again")
	f.waitFor("the second reply", func() bool { return f.sawText("second done") })
	if b, _ := os.ReadFile(target); string(b) != "two" {
		t.Fatalf("file %q: the call should have run", b)
	}
	if countPrompts(f) != 1 {
		t.Fatalf("asked %d times, want once", countPrompts(f))
	}

	// What is allowed is on show, and so is what happened.
	f.dm(1, "!permissions")
	f.waitFor("the permissions", func() bool { return f.sawText("Tools this chat always allows: write_file") })
	f.dm(1, "!log")
	f.waitFor("the log", func() bool { return f.sawText("write_file ran (grant)") })
	if !f.sawText("write_file ran (prompt)") {
		t.Fatalf("the first call was asked about, and the log says so: %q", f.texts())
	}

	f.dm(1, "!permissions reset")
	f.waitFor("the reset", func() bool { return f.sawText("Forgot 1 always-allowed tool(s)") })
	f.dm(1, "once more")
	f.waitFor("a prompt again", func() bool { return countPrompts(f) == 2 })
}

// shell is kept out of "always allow" by default: approving one command is not
// approving the next.
func TestShellOffersNoAlwaysButton(t *testing.T) {
	f := newFakeDiscord(t)
	g := newGuard(t, nil)
	args, _ := json.Marshal(map[string]string{"command": "echo hi"})
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "shell", Arguments: string(args)}}}, say("done"),
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "2", Name: "shell", Arguments: string(args)}}}, say("done again"),
	}}
	start(t, f, []int64{1}, guarded(p, g), withPermissions(g))

	f.dm(1, "run it")
	prompt := f.promptWithButtons()
	if len(prompt.Buttons) != 2 {
		t.Fatalf("no Always for shell: %v", prompt.Buttons)
	}
	// A press of an Always button that was never offered counts as a plain Allow, and keeps nothing.
	forged := strings.TrimSuffix(prompt.Buttons[0], "y") + "a"
	f.press(1, prompt.Channel, forged, prompt.Text)
	f.waitFor("the reply", func() bool { return f.sawText("done") })
	if g.Grants.List("discord:1") != nil {
		t.Fatalf("a forged Always was remembered: %v", g.Grants.List("discord:1"))
	}
	cb := f.callbackLog()[0]
	if !strings.HasSuffix(cb.Content, "Allowed.") {
		t.Fatalf("%+v", cb)
	}
	f.dm(1, "again")
	f.waitFor("a second prompt", func() bool { return countPrompts(f) == 2 })
}

func TestRulesAllowAndDenyWithoutAsking(t *testing.T) {
	f := newFakeDiscord(t)
	allowed := filepath.Join(t.TempDir(), "a.txt")
	g := newGuard(t, map[string]string{"write_file": "allow", "skill_*": "deny"})
	p := &scripted{replies: []provider.Message{writeCall(allowed, "fine"), say("wrote")}}
	start(t, f, []int64{1}, guarded(p, g), withPermissions(g))

	f.dm(1, "write")
	f.waitFor("the reply", func() bool { return f.sawText("wrote") })
	if b, _ := os.ReadFile(allowed); string(b) != "fine" || countPrompts(f) != 0 {
		t.Fatalf("an allowed tool runs unasked: %q, %d prompts", b, countPrompts(f))
	}

	// A tool in a denied family never runs, and nobody is asked about it.
	reg := tool.NewRegistry()
	ran := false
	reg.Register(tool.Tool{Name: "skill_save", NeedsApproval: true, Schema: json.RawMessage(`{}`), Run: func(context.Context, json.RawMessage) (string, error) {
		ran = true
		return "saved", nil
	}})
	a := &agent.Agent{Model: "m", Tools: reg, Gate: g.For("chat", "discord", 1), Approve: func(string, string) bool { t.Error("asked about a denied tool"); return true },
		Provider: &scripted{replies: []provider.Message{
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "skill_save", Arguments: `{}`}}}, say("done"),
		}}}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("a denied family ran")
	}

	f.dm(1, "!log")
	f.waitFor("the log", func() bool { return f.sawText("write_file ran (rule: write_file = allow)") })
	denials, _ := g.Audit.Recent(guard.Filter{Denied: true}, 5)
	if len(denials) != 1 || denials[0].Tool != "skill_save" || denials[0].Rule != "skill_* = deny" {
		t.Fatalf("%+v", denials)
	}
}

// Without a Guard the engine behaves as before: two buttons, and no extra commands.
func TestWithoutPermissionsThereIsNoAlwaysButton(t *testing.T) {
	f := newFakeDiscord(t)
	target := filepath.Join(t.TempDir(), "x")
	p := &scripted{replies: []provider.Message{writeCall(target, "x"), say("ok")}}
	var calls atomic.Int32
	start(t, f, []int64{1}, echoAgent(p, &calls))
	f.dm(1, "write")
	if prompt := f.promptWithButtons(); len(prompt.Buttons) != 2 {
		t.Fatalf("%v", prompt.Buttons)
	}
	f.dm(1, "!log")
	f.waitFor("an answer", func() bool { return f.sawText("not set up") })
	f.dm(1, "!help")
	f.waitFor("help", func() bool { return f.sawText("Talaria is ready") })
	if f.sawText("!permissions") {
		t.Fatal("help should not mention commands that are not available")
	}
}
