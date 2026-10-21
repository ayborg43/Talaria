package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"talaria/internal/metrics"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

// A conversation with a tool that reads from outside, one that could send something out, and a
// record of what ran and what was asked.
type taintRig struct {
	a     *Agent
	gate  *fakeGate
	ran   []string
	asked []string
	allow bool
}

func newTaintRig(t *testing.T, guard bool, turns ...[]provider.ToolCall) *taintRig {
	t.Helper()
	r := &taintRig{gate: &fakeGate{}}
	reg := tool.NewRegistry()
	record := func(name string) func(context.Context, json.RawMessage) (string, error) {
		return func(context.Context, json.RawMessage) (string, error) {
			r.ran = append(r.ran, name)
			return name + " done", nil
		}
	}
	reg.Register(tool.Tool{Name: "read_web", Untrusted: true, Parallel: true, Run: record("read_web")})
	reg.Register(tool.Tool{Name: "send_out", Egress: tool.Always, Run: record("send_out")})
	reg.Register(tool.Tool{Name: "plain", Run: record("plain")})
	reg.Register(tool.Tool{Name: "pick", Egress: func(raw json.RawMessage) bool { return strings.Contains(string(raw), "elsewhere") }, Run: record("pick")})
	var replies []provider.Message
	for i, calls := range turns {
		for j := range calls {
			calls[j].ID = string(rune('a'+i)) + string(rune('0'+j))
		}
		replies = append(replies, provider.Message{Role: provider.RoleAssistant, ToolCalls: calls})
	}
	replies = append(replies, provider.Message{Role: provider.RoleAssistant, Content: "done"})
	r.a = &Agent{Provider: &scripted{replies: replies}, Model: "m", Tools: reg, Gate: r.gate, GuardUntrusted: guard,
		Approve: func(name, args string) bool { r.asked = append(r.asked, name); return r.allow }}
	return r
}

func call(name, args string) provider.ToolCall { return provider.ToolCall{Name: name, Arguments: args} }

func (r *taintRig) run(t *testing.T) {
	t.Helper()
	if _, err := r.a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
}

func TestNothingChangesUntilOutsideContentHasBeenRead(t *testing.T) {
	r := newTaintRig(t, true, []provider.ToolCall{call("send_out", "{}")}, []provider.ToolCall{call("plain", "{}")})
	r.run(t)
	if strings.Join(r.ran, ",") != "send_out,plain" || len(r.asked) != 0 || r.a.ReadOutside() {
		t.Fatalf("ran %v asked %v", r.ran, r.asked)
	}
}

func TestAfterReadingOutsideContentAnythingThatCouldSendOutAsksFirst(t *testing.T) {
	r := newTaintRig(t, true, []provider.ToolCall{call("read_web", "{}")}, []provider.ToolCall{call("send_out", "{}")}, []provider.ToolCall{call("plain", "{}")})
	r.run(t)
	if strings.Join(r.ran, ",") != "read_web,plain" {
		t.Fatalf("send_out must not run unasked: %v", r.ran)
	}
	if len(r.asked) != 1 || r.asked[0] != "send_out (after reading outside content)" {
		t.Fatalf("it asks, and says why: %v", r.asked)
	}
	if BaseTool(r.asked[0]) != "send_out" || !IsFlagged(r.asked[0]) || IsFlagged("send_out") {
		t.Fatal("the flagged name still names the tool")
	}
	last := r.gate.entries[len(r.gate.entries)-2]
	if last.Tool != "send_out" || last.Allowed || last.Via != "outside content" {
		t.Fatalf("the record says why it was asked: %+v", last)
	}
	// The user can still say yes, and then it runs.
	r2 := newTaintRig(t, true, []provider.ToolCall{call("read_web", "{}")}, []provider.ToolCall{call("send_out", "{}")})
	r2.allow = true
	r2.run(t)
	if strings.Join(r2.ran, ",") != "read_web,send_out" {
		t.Fatalf("%v", r2.ran)
	}
}

func TestCallsMadeTogetherAreJudgedTogether(t *testing.T) {
	r := newTaintRig(t, true, []provider.ToolCall{call("read_web", "{}"), call("send_out", "{}")})
	r.run(t)
	if strings.Join(r.ran, ",") != "read_web" || len(r.asked) != 1 {
		t.Fatalf("a send in the same turn as a read must not slip past: ran %v asked %v", r.ran, r.asked)
	}
}

func TestOnlyCallsThatCouldSendOutAreAffectedAndSomeOnlyForSomeArguments(t *testing.T) {
	r := newTaintRig(t, true, []provider.ToolCall{call("read_web", "{}")},
		[]provider.ToolCall{call("pick", `{"to":"here"}`)}, []provider.ToolCall{call("pick", `{"to":"elsewhere"}`)}, []provider.ToolCall{call("plain", "{}")})
	r.run(t)
	if strings.Join(r.ran, ",") != "read_web,pick,plain" || len(r.asked) != 1 {
		t.Fatalf("ran %v asked %v", r.ran, r.asked)
	}
}

func TestAPermissionRuleThatSaysAllowIsTheUsersOwnChoice(t *testing.T) {
	r := newTaintRig(t, true, []provider.ToolCall{call("read_web", "{}")}, []provider.ToolCall{call("send_out", "{}")})
	r.gate.rulings = map[string]Verdict{"send_out": {Action: Allow, Via: "rule", Rule: "send_out = allow"}}
	r.run(t)
	if strings.Join(r.ran, ",") != "read_web,send_out" || len(r.asked) != 0 {
		t.Fatalf("ran %v asked %v", r.ran, r.asked)
	}
	// A grant (the Always button) is not a rule: it was given before the page was read.
	r = newTaintRig(t, true, []provider.ToolCall{call("read_web", "{}")}, []provider.ToolCall{call("send_out", "{}")})
	r.gate.rulings = map[string]Verdict{"send_out": {Action: Allow, Via: "grant"}}
	r.run(t)
	if len(r.asked) != 1 {
		t.Fatalf("a grant does not cover this: %v", r.asked)
	}
	// And a deny rule is still a deny.
	r = newTaintRig(t, true, []provider.ToolCall{call("send_out", "{}")})
	r.gate.rulings = map[string]Verdict{"send_out": {Action: Deny, Via: "rule"}}
	r.run(t)
	if len(r.ran) != 0 {
		t.Fatal("denied")
	}
}

func TestTheGuardCanBeSwitchedOff(t *testing.T) {
	r := newTaintRig(t, false, []provider.ToolCall{call("read_web", "{}")}, []provider.ToolCall{call("send_out", "{}")})
	r.run(t)
	if strings.Join(r.ran, ",") != "read_web,send_out" || len(r.asked) != 0 {
		t.Fatalf("ran %v asked %v", r.ran, r.asked)
	}
	if !r.a.ReadOutside() {
		t.Fatal("what was read is still noted")
	}
}

func TestNobodyToAskMeansNo(t *testing.T) {
	r := newTaintRig(t, true, []provider.ToolCall{call("read_web", "{}")}, []provider.ToolCall{call("send_out", "{}")})
	r.a.Approve = nil // a scheduled job or a watch
	r.run(t)
	if strings.Join(r.ran, ",") != "read_web" {
		t.Fatalf("%v", r.ran)
	}
	// A poll of a tool is refused the same way once the conversation is tainted.
	r.a.MarkUntrusted()
	if _, err := r.a.Call(context.Background(), "send_out", nil); err == nil {
		t.Fatal("refused")
	}
}

func TestMarkUntrustedForWhatWasHandedToTheModel(t *testing.T) {
	r := newTaintRig(t, true, []provider.ToolCall{call("send_out", "{}")})
	r.a.MarkUntrusted() // as when a watch gives the model what a service returned
	r.run(t)
	if len(r.ran) != 0 || len(r.asked) != 1 {
		t.Fatalf("ran %v asked %v", r.ran, r.asked)
	}
}

func TestPerCallApprovalCanDependOnTheArguments(t *testing.T) {
	r := newTaintRig(t, false, []provider.ToolCall{call("gated", `{"action":"pause"}`)}, []provider.ToolCall{call("gated", `{"action":"delete"}`)})
	r.a.Tools.Register(tool.Tool{Name: "gated", NeedsApprovalFor: func(raw json.RawMessage) bool { return strings.Contains(string(raw), "delete") },
		Run: func(context.Context, json.RawMessage) (string, error) {
			r.ran = append(r.ran, "gated")
			return "ok", nil
		}})
	r.run(t)
	if len(r.ran) != 1 || len(r.asked) != 1 || r.asked[0] != "gated" {
		t.Fatalf("only the delete asks, and is refused: ran %v asked %v", r.ran, r.asked)
	}
}

// A tool that both returns outside text and could send something out (a subagent, a service's
// call that is neither read nor draft) does not ask about itself on its first use; what it brings back
// is what makes the calls after it ask.
func TestAToolThatReadsAndCouldSendDoesNotAskAboutItselfFirst(t *testing.T) {
	r := newTaintRig(t, true, []provider.ToolCall{call("both", "{}")}, []provider.ToolCall{call("send_out", "{}")}, []provider.ToolCall{call("both", "{}")})
	r.a.Tools.Register(tool.Tool{Name: "both", Untrusted: true, Egress: tool.Always,
		Run: func(context.Context, json.RawMessage) (string, error) {
			r.ran = append(r.ran, "both")
			return "ok", nil
		}})
	r.run(t)
	if strings.Join(r.ran, ",") != "both" {
		t.Fatalf("the first call runs, and then the conversation has read outside content: %v", r.ran)
	}
	if len(r.asked) != 2 || r.asked[0] != "send_out (after reading outside content)" || r.asked[1] != "both (after reading outside content)" {
		t.Fatalf("%v", r.asked)
	}
}

func TestTheLoopCountsModelCallsToolCallsAndApprovals(t *testing.T) {
	metrics.Reset()
	r := newTaintRig(t, true, []provider.ToolCall{call("read_web", "{}")}, []provider.ToolCall{call("send_out", "{}")}, []provider.ToolCall{call("plain", "{}")})
	r.allow = false
	r.run(t)
	if got := metrics.Get("talaria_model_calls_total", "result", "ok"); got != 4 {
		t.Errorf("model calls: %v", got)
	}
	if metrics.Get("talaria_tool_calls_total", "status", "ok") != 2 || metrics.Get("talaria_tool_calls_total", "status", "not_run") != 1 {
		t.Errorf("tool calls: ok=%v refused=%v", metrics.Get("talaria_tool_calls_total", "status", "ok"), metrics.Get("talaria_tool_calls_total", "status", "not_run"))
	}
	if metrics.Get("talaria_approvals_total", "decision", "denied") != 1 || metrics.Get("talaria_approvals_total", "decision", "allowed") != 0 {
		t.Errorf("approvals")
	}
	if metrics.Get("talaria_model_call_seconds_count") != 4 {
		t.Errorf("durations: %v", metrics.Get("talaria_model_call_seconds_count"))
	}
	r2 := newTaintRig(t, true)
	r2.a.Provider = errProvider{}
	r2.a.Run(context.Background(), "x")
	if metrics.Get("talaria_model_calls_total", "result", "error") != 1 {
		t.Error("a failed model call is counted")
	}
	// Nothing a user wrote is in what is recorded.
	var b strings.Builder
	metrics.Write(&b)
	if strings.Contains(b.String(), "send_out") || strings.Contains(b.String(), "read_web") {
		t.Errorf("no tool names or arguments in the metrics:\n%s", b.String())
	}
}

type errProvider struct{}

func (errProvider) Chat(context.Context, provider.Request) (provider.Message, error) {
	return provider.Message{}, errors.New("down")
}
