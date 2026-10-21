package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"talaria/internal/provider"
	"talaria/internal/tool"
)

// fakeGate rules by tool name and records what it is told.
type fakeGate struct {
	mu      sync.Mutex
	rulings map[string]Verdict
	secrets []string
	entries []Entry
}

func (g *fakeGate) Verdict(tool string, needs bool) Verdict {
	if v, ok := g.rulings[tool]; ok {
		return v
	}
	if needs {
		return Verdict{Action: Ask, Via: "auto"}
	}
	return Verdict{Action: Allow, Via: "auto"}
}

func (g *fakeGate) Scrub(s string) string {
	for _, sec := range g.secrets {
		s = strings.ReplaceAll(s, sec, "[redacted]")
	}
	return s
}

func (g *fakeGate) Record(e Entry) {
	g.mu.Lock()
	g.entries = append(g.entries, e)
	g.mu.Unlock()
}

func callScript(name, args string) *scripted {
	return &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: name, Arguments: args}}},
		{Role: provider.RoleAssistant, Content: "ok"},
	}}
}

func lastToolResult(p *scripted) string { return p.seen[1][len(p.seen[1])-1].Content }

func TestGateDenyStopsATool(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	p := callScript("write_file", `{"path":"`+target+`","content":"x"}`)
	a := newAgent(p)
	asked := false
	a.Approve = func(string, string) bool { asked = true; return true }
	a.Gate = &fakeGate{rulings: map[string]Verdict{"write_file": {Action: Deny, Via: "rule", Rule: "write_file = deny"}}}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a denied tool ran")
	}
	if asked {
		t.Fatal("a denied tool is not put to the user: they could only approve what is forbidden")
	}
	if got := lastToolResult(p); !strings.Contains(got, "permission rules") {
		t.Fatalf("the model should be told why: %q", got)
	}
	g := a.Gate.(*fakeGate)
	if len(g.entries) != 1 || g.entries[0].Allowed || g.entries[0].Via != "rule" || g.entries[0].Status != "not run" {
		t.Fatalf("%+v", g.entries)
	}
}

func TestGateAllowRunsWithoutAsking(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	a := newAgent(callScript("write_file", `{"path":"`+target+`","content":"x"}`))
	a.Approve = func(string, string) bool { t.Fatal("must not ask"); return false }
	a.Gate = &fakeGate{rulings: map[string]Verdict{"write_file": {Action: Allow, Via: "grant"}}}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("an allowed tool did not run")
	}
	if e := a.Gate.(*fakeGate).entries[0]; !e.Allowed || e.Via != "grant" || e.Status != "ok" {
		t.Fatalf("%+v", e)
	}
}

// A rule can make an ordinarily free tool ask first.
func TestGateAskAppliesToToolsThatNeverAsked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(path, []byte("contents"), 0o644)
	for _, approve := range []bool{true, false} {
		p := callScript("read_file", `{"path":"`+path+`"}`)
		a := newAgent(p)
		var asked string
		a.Approve = func(name, _ string) bool { asked = name; return approve }
		a.Gate = &fakeGate{rulings: map[string]Verdict{"read_file": {Action: Ask, Via: "rule"}}}
		a.Run(context.Background(), "go")
		if asked != "read_file" {
			t.Fatalf("approve=%v: asked %q", approve, asked)
		}
		got := lastToolResult(p)
		if approve != (got == "contents") {
			t.Fatalf("approve=%v: result %q", approve, got)
		}
		if e := a.Gate.(*fakeGate).entries[0]; e.Via != "prompt" || e.Allowed != approve {
			t.Fatalf("%+v", e)
		}
	}
	// With no one to ask, an Ask is a refusal.
	a := newAgent(callScript("read_file", `{"path":"`+path+`"}`))
	a.Gate = &fakeGate{rulings: map[string]Verdict{"read_file": {Action: Ask}}}
	a.Run(context.Background(), "go")
	if a.Gate.(*fakeGate).entries[0].Allowed {
		t.Fatal("no Approve means no")
	}
}

func TestGateScrubsOutputBeforeTheModelSeesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	os.WriteFile(path, []byte("API_KEY=sk-very-secret-value\n"), 0o644)
	p := callScript("read_file", `{"path":"`+path+`"}`)
	a := newAgent(p)
	a.Gate = &fakeGate{secrets: []string{"sk-very-secret-value"}}
	a.Run(context.Background(), "go")
	got := lastToolResult(p)
	if strings.Contains(got, "sk-very") || !strings.Contains(got, "API_KEY=[redacted]") {
		t.Fatalf("%q", got)
	}
}

// Errors are text the model reads too, and tools put the values they were
// given into their errors.
func TestGateScrubsErrorsToo(t *testing.T) {
	p := callScript("read_file", `{"path":"/no/such/sk-very-secret-value"}`)
	a := newAgent(p)
	a.Gate = &fakeGate{secrets: []string{"sk-very-secret-value"}}
	a.Run(context.Background(), "go")
	if got := lastToolResult(p); strings.Contains(got, "sk-very") || !strings.HasPrefix(got, "error:") {
		t.Fatalf("%q", got)
	}
}

// A secret straddling the truncation point must not be left half-visible.
func TestGateScrubsBeforeTruncating(t *testing.T) {
	secret := "SECRET-" + strings.Repeat("z", 40)
	path := filepath.Join(t.TempDir(), "big")
	os.WriteFile(path, []byte(strings.Repeat("a", maxToolOutput-10)+secret+"\n"), 0o644)
	p := callScript("read_file", `{"path":"`+path+`"}`)
	a := newAgent(p)
	a.Gate = &fakeGate{secrets: []string{secret}}
	a.Run(context.Background(), "go")
	if got := lastToolResult(p); strings.Contains(got, "SECRET-") {
		t.Fatal("part of a secret survived truncation")
	}
}

func TestGateRecordsUnknownToolsAndFailures(t *testing.T) {
	a := newAgent(callScript("no_such_tool", `{}`))
	a.Gate = &fakeGate{}
	a.Run(context.Background(), "go")
	if e := a.Gate.(*fakeGate).entries[0]; e.Allowed || e.Via != "unknown tool" {
		t.Fatalf("%+v", e)
	}

	a = newAgent(callScript("read_file", `{"path":"/definitely/not/here"}`))
	a.Gate = &fakeGate{}
	a.Run(context.Background(), "go")
	if e := a.Gate.(*fakeGate).entries[0]; !e.Allowed || e.Status != "error" || !strings.HasPrefix(e.Result, "error:") {
		t.Fatalf("a call that ran and failed is allowed and errored: %+v", e)
	}
}

func TestGateRecordsACrashingTool(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.Tool{Name: "boom", Schema: json.RawMessage(`{}`), Run: func(context.Context, json.RawMessage) (string, error) { panic("kaboom") }})
	p := callScript("boom", `{}`)
	a := &Agent{Provider: p, Model: "m", Tools: reg, Gate: &fakeGate{}}
	a.Run(context.Background(), "go")
	e := a.Gate.(*fakeGate).entries[0]
	if e.Status != "error" || !strings.Contains(e.Result, "kaboom") || !strings.Contains(lastToolResult(p), "crashed") {
		t.Fatalf("%+v", e)
	}
}

func TestGateRecordsArgumentsAndSubagentLabel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	os.WriteFile(path, []byte("x"), 0o644)
	a := newAgent(callScript("read_file", `{"path":"`+path+`"}`))
	a.Gate = &fakeGate{}
	a.Label = "research"
	a.Run(context.Background(), "go")
	e := a.Gate.(*fakeGate).entries[0]
	if e.Tool != "read_file" || e.Label != "research" || !strings.Contains(e.Args, path) || e.Result != "x" || e.Took < 0 {
		t.Fatalf("%+v", e)
	}
}

// With no Gate nothing changes: tools that need approval ask, the rest run.
func TestNoGateKeepsTheOldBehaviour(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	os.WriteFile(path, []byte("x"), 0o644)
	p := callScript("read_file", `{"path":"`+path+`"}`)
	a := newAgent(p)
	a.Approve = func(string, string) bool { t.Fatal("read_file never asked"); return false }
	a.Run(context.Background(), "go")
	if lastToolResult(p) != "x" {
		t.Fatal("did not run")
	}
}

func TestLabelledAndBaseTool(t *testing.T) {
	if got := Labelled("write_file", "notes"); got != "write_file (subagent notes)" {
		t.Fatal(got)
	}
	for in, want := range map[string]string{"write_file (subagent notes)": "write_file", "write_file": "write_file", "a (subagent b (c))": "a", "": ""} {
		if got := BaseTool(in); got != want {
			t.Errorf("BaseTool(%q) = %q, want %q", in, got, want)
		}
	}
}

// Call runs one tool on its own: through the gate, never asking anyone.
func TestCallRunsAToolWithoutAskingAndRefusesOneThatNeedsApproval(t *testing.T) {
	asked := 0
	reg := tool.NewRegistry()
	var got string
	reg.Register(tool.Tool{Name: "read", Run: func(_ context.Context, args json.RawMessage) (string, error) { got = string(args); return "inbox", nil }})
	reg.Register(tool.Tool{Name: "send", NeedsApproval: true, Run: func(context.Context, json.RawMessage) (string, error) { return "sent", nil }})
	reg.Register(tool.Tool{Name: "broken", Run: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("boom") }})
	gate := &fakeGate{}
	a := &Agent{Provider: &scripted{}, Model: "m", Tools: reg, Gate: gate, Approve: func(string, string) bool { asked++; return true }}

	if out, err := a.Call(context.Background(), "read", nil); err != nil || out != "inbox" || got != "{}" {
		t.Fatalf("%q %v %q", out, err, got)
	}
	if out, err := a.Call(context.Background(), "read", json.RawMessage(`{"q":"x"}`)); err != nil || out != "inbox" || got != `{"q":"x"}` {
		t.Fatalf("%q %v %q", out, err, got)
	}
	if _, err := a.Call(context.Background(), "send", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a tool that needs approval is refused, though the agent has an approver")
	}
	if asked != 0 {
		t.Fatal("nobody is asked")
	}
	if _, err := a.Call(context.Background(), "broken", nil); err == nil || err.Error() != "boom" {
		t.Fatalf("the tool's own message, without the prefix: %v", err)
	}
	if _, err := a.Call(context.Background(), "nope", nil); err == nil {
		t.Fatal("unknown tool")
	}
	if a.Approve == nil {
		t.Fatal("the agent keeps its approver for its own runs")
	}
	if len(gate.entries) != 5 {
		t.Fatalf("every call is in the activity log: %d", len(gate.entries))
	}

	gate.rulings = map[string]Verdict{"read": {Action: Deny, Via: "policy"}}
	if _, err := a.Call(context.Background(), "read", nil); err == nil {
		t.Fatal("the permission rules apply to a poll as to any call")
	}
}
