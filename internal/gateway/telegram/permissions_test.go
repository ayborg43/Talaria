package telegram

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/agent"
	"talaria/internal/guard"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

func TestAlwaysAllowButtonOnTelegram(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	pol, _ := guard.ParsePolicy(nil, []string{"shell"}) // write_file stands in for an ordinary rememberable tool
	g := &guard.Guard{Policy: pol, Grants: &guard.Grants{Path: filepath.Join(dir, "grants.json")}, Audit: &guard.Log{Path: filepath.Join(dir, "audit.log")}}

	args, _ := json.Marshal(map[string]string{"path": target, "content": "x"})
	call := provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: string(args)}}}
	p := &scripted{replies: []provider.Message{call, {Role: provider.RoleAssistant, Content: "first"}, call, {Role: provider.RoleAssistant, Content: "second"}}}
	newAgent := func(chatID int64, approve func(string, string) bool) *agent.Agent {
		reg := tool.NewRegistry()
		tool.RegisterBuiltins(reg)
		return &agent.Agent{Provider: p, Model: "m", Tools: reg, Approve: approve, Gate: g.For("chat", "telegram", chatID)}
	}
	start(t, f, []int64{1}, newAgent, func(gw *Gateway) { gw.Permissions = guard.ChatPermissions{G: g, Gateway: "telegram"} })

	f.updates <- []Update{userMsg(1, 1, 1, "private", "write")}
	var prompt sentMsg
	f.waitFor("the prompt", func() bool {
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
	if len(prompt.Callback) != 3 || !strings.HasSuffix(prompt.Callback[0], ":y") || !strings.HasSuffix(prompt.Callback[1], ":a") || !strings.HasSuffix(prompt.Callback[2], ":n") {
		t.Fatalf("Allow, Always, Deny: %v", prompt.Callback)
	}
	f.updates <- []Update{{UpdateID: 2, CallbackQuery: &CallbackQuery{
		ID: "q", From: User{ID: 1}, Data: prompt.Callback[1],
		Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}, Text: prompt.Text},
	}}}
	f.waitFor("the first reply", func() bool { return f.sawText("first") })
	f.mu.Lock()
	lastEdit := f.edit[len(f.edit)-1]
	f.mu.Unlock()
	if !strings.HasSuffix(lastEdit, "Allowed, and always allowed in this chat.") {
		t.Fatalf("%q", lastEdit)
	}

	f.updates <- []Update{userMsg(3, 1, 1, "private", "again")}
	f.waitFor("the second reply, with no prompt", func() bool { return f.sawText("second") })
	n := 0
	f.mu.Lock()
	for _, m := range f.sent {
		if m.HasKB {
			n++
		}
	}
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("asked %d times", n)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}

// A callback can arrive without a message: inline mode carries an inline ID
// instead of one. Answering it must not crash the gateway, and there is no
// message to rewrite with the verdict.
func TestCallbackWithoutAMessageDoesNotCrash(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	pol, _ := guard.ParsePolicy(nil, nil)
	g := &guard.Guard{Policy: pol, Grants: &guard.Grants{Path: filepath.Join(dir, "grants.json")}, Audit: &guard.Log{Path: filepath.Join(dir, "audit.log")}}

	args, _ := json.Marshal(map[string]string{"path": target, "content": "x"})
	call := provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "write_file", Arguments: string(args)}}}
	p := &scripted{replies: []provider.Message{call, {Role: provider.RoleAssistant, Content: "done"}}}
	newAgent := func(chatID int64, approve func(string, string) bool) *agent.Agent {
		reg := tool.NewRegistry()
		tool.RegisterBuiltins(reg)
		return &agent.Agent{Provider: p, Model: "m", Tools: reg, Approve: approve, Gate: g.For("chat", "telegram", chatID)}
	}
	start(t, f, []int64{1}, newAgent)

	// Chat 0 is not a real Telegram chat, but a self-hosted Bot API server can
	// send one, and the gateway must survive what it sends.
	f.updates <- []Update{userMsg(1, 1, 0, "private", "write")}
	var prompt sentMsg
	f.waitFor("the prompt", func() bool {
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
	f.updates <- []Update{{UpdateID: 2, CallbackQuery: &CallbackQuery{
		ID: "q", From: User{ID: 1}, Data: prompt.Callback[0], Message: nil,
	}}}
	f.waitFor("the reply", func() bool { return f.sawText("done") })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.edit) != 0 {
		t.Fatalf("edited %d messages for a callback with no message", len(f.edit))
	}
}
