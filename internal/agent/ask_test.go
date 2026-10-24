package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"talaria/internal/provider"
	"talaria/internal/tool"
)

// A model that makes one tool call and then answers, and what it was told of the call.
func callThenAnswer(name, args string) *scripted {
	return &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: name, Arguments: args}}},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}
}

func told(p *scripted) string { return p.seen[1][len(p.seen[1])-1].Content }

func TestInTheNewModeOnlyDeletingAndPostingAsk(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	os.WriteFile(victim, []byte("x"), 0o600)
	cases := []struct {
		name, tool, args string
		asks             bool
	}{
		{"writing a file", "write_file", `{"path":"` + filepath.Join(dir, "new.txt") + `","content":"hi"}`, false},
		{"an ordinary command", "shell", `{"command":"echo hello"}`, false},
		{"a command that builds", "shell", `{"command":"mkdir -p ` + dir + `/a && ls ` + dir + `"}`, false},
		{"a command that deletes", "shell", `{"command":"rm ` + victim + `"}`, true},
		{"deleting by another route", "shell", `{"command":"find ` + dir + ` -name victim.txt -delete"}`, true},
	}
	for _, c := range cases {
		var asked []string
		a := newAgent(callThenAnswer(c.tool, c.args))
		a.Ask = AskDestructive
		a.Approve = func(name, _ string) bool { asked = append(asked, name); return false }
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		if (len(asked) > 0) != c.asks {
			t.Errorf("%s: asked=%v, want %v", c.name, asked, c.asks)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("a file was deleted when the user was asked and said no")
	}
}

func TestTheStrictWayStillAsksAboutEverythingRisky(t *testing.T) {
	var asked []string
	a := newAgent(callThenAnswer("shell", `{"command":"echo hello"}`))
	a.Approve = func(name, _ string) bool { asked = append(asked, name); return false }
	a.Run(context.Background(), "go")
	if len(asked) != 1 {
		t.Fatalf("the strict way did not ask about an ordinary command: %v", asked)
	}
}

func TestPostingAsksWhateverTheModeAndANoteSaysWhy(t *testing.T) {
	reg := tool.NewRegistry()
	ran := false
	reg.Register(tool.Tool{Name: "x_post", Description: "x", Schema: json.RawMessage(`{"type":"object"}`), NeedsApproval: true, Risk: tool.RiskPosts,
		Run: func(context.Context, json.RawMessage) (string, error) { ran = true; return "posted", nil }})
	reg.Register(tool.Tool{Name: "lookup", Description: "x", Schema: json.RawMessage(`{"type":"object"}`), NeedsApproval: true,
		Run: func(context.Context, json.RawMessage) (string, error) { return "found", nil }})

	var reqs []Request
	a := &Agent{Provider: callThenAnswer("x_post", `{"text":"hello world"}`), Model: "m", Tools: reg, Ask: AskDestructive}
	a.Defer = func(_ context.Context, r Request) (string, bool) {
		reqs = append(reqs, r)
		return "waiting for the user", true
	}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if ran || len(reqs) != 1 || reqs[0].Why != "This posts on social media, as you." || reqs[0].Tool != "x_post" || reqs[0].Args != `{"text":"hello world"}` {
		t.Fatalf("ran=%v %+v", ran, reqs)
	}

	reqs = nil
	b := &Agent{Provider: callThenAnswer("lookup", `{}`), Model: "m", Tools: reg, Ask: AskDestructive}
	b.Defer = func(_ context.Context, r Request) (string, bool) { reqs = append(reqs, r); return "x", true }
	if _, err := b.Run(context.Background(), "go"); err != nil || len(reqs) != 0 {
		t.Fatalf("a lookup was held for permission: %v %v", reqs, err)
	}
}

func TestAHeldRequestLetsTheRunGoOnAndIsRunLaterExactlyAsSaved(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	os.WriteFile(victim, []byte("x"), 0o600)
	args := `{"command":"rm ` + victim + `"}`
	p := callThenAnswer("shell", args)
	var saved []Request
	var mu sync.Mutex
	a := newAgent(p)
	a.Ask = AskDestructive
	a.Defer = func(_ context.Context, r Request) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, r)
		return "Permission has been asked. Nothing was done yet.", true
	}
	reply, err := a.Run(context.Background(), "tidy up")
	if err != nil || reply != "ok" {
		t.Fatalf("the run did not carry on after the request was held: %q %v", reply, err)
	}
	if !strings.Contains(told(p), "Permission has been asked") {
		t.Fatalf("the model was not told: %q", told(p))
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("the call ran before anyone allowed it")
	}
	if len(saved) != 1 || saved[0].Args != args {
		t.Fatalf("%+v", saved)
	}

	// Later, on a fresh agent (the program may have restarted), the saved call is run as it was.
	b := newAgent(&scripted{})
	b.Ask = AskDestructive
	out, err := b.CallApproved(context.Background(), saved[0].Tool, json.RawMessage(saved[0].Args))
	if err != nil {
		t.Fatalf("%v (%s)", err, out)
	}
	if _, err := os.Stat(victim); err == nil {
		t.Fatal("the approved call did not run")
	}
}

func TestAnApprovedCallStillObeysARuleThatDeniesTheTool(t *testing.T) {
	a := newAgent(&scripted{})
	a.Ask = AskDestructive
	a.Gate = denyAll{}
	if _, err := a.CallApproved(context.Background(), "shell", json.RawMessage(`{"command":"rm x"}`)); err == nil || !strings.Contains(err.Error(), "permission rules") {
		t.Fatalf("a rule was got round by an earlier yes: %v", err)
	}
}

func TestIfARequestCannotBeHeldTheUserIsAskedAsBefore(t *testing.T) {
	var asked int
	a := newAgent(callThenAnswer("shell", `{"command":"rm nothing-here"}`))
	a.Ask = AskDestructive
	a.Defer = func(context.Context, Request) (string, bool) { return "", false }
	a.Approve = func(string, string) bool { asked++; return false }
	a.Run(context.Background(), "go")
	if asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
}

type denyAll struct{}

func (denyAll) Verdict(string, bool) tool.Verdict {
	return tool.Verdict{Action: tool.Deny, Via: "rule", Rule: "x = deny"}
}
func (denyAll) Scrub(s string) string { return s }
func (denyAll) Record(tool.Entry)     {}
