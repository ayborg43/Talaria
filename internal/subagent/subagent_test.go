package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"talaria/internal/agent"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

type chatFunc func(ctx context.Context, req provider.Request) (provider.Message, error)

func (f chatFunc) Chat(ctx context.Context, req provider.Request) (provider.Message, error) {
	return f(ctx, req)
}

func isSub(req provider.Request) bool {
	return len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "You are a subagent working for Talaria")
}

func text(s string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: s}
}

func toolCalls(calls ...provider.ToolCall) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: calls}
}

func delegateCall(id, task, name string) provider.ToolCall {
	args, _ := json.Marshal(map[string]string{"task": task, "name": name})
	return provider.ToolCall{ID: id, Name: "delegate", Arguments: string(args)}
}

func lastTool(req provider.Request) bool {
	return req.Messages[len(req.Messages)-1].Role == provider.RoleTool
}

func toolNames(req provider.Request) map[string]bool {
	out := map[string]bool{}
	for _, t := range req.Tools {
		out[t.Name] = true
	}
	return out
}

// newParent builds an agent whose registry holds the built-ins plus stand-ins
// for the tools subagents must not get.
func newParent(p provider.Provider, pool *Pool) *agent.Agent {
	reg := tool.NewRegistry()
	tool.RegisterBuiltins(reg)
	for _, name := range []string{"memory", "skill_save", "skill_delete", "skill_view", "cron_add", "cron_remove", "cron_list"} {
		reg.Register(tool.Tool{Name: name, Run: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }})
	}
	a := &agent.Agent{Provider: p, Model: "parent-model", Tools: reg, System: "parent system prompt"}
	pool.Register(a, func() string { return "BACKGROUND: the user likes brevity" })
	return a
}

func TestDelegationIsAnIsolatedConversation(t *testing.T) {
	var subReq, parentFinal provider.Request
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req):
			subReq = req
			return text("found it in /srv/app"), nil
		case lastTool(req):
			parentFinal = req
			return text("all done: " + req.Messages[len(req.Messages)-1].Content), nil
		}
		return toolCalls(delegateCall("1", "find where the config lives", "finder")), nil
	})
	a := newParent(p, NewPool(Limits{}))

	got, err := a.Run(context.Background(), "where is the config?")
	if err != nil || got != "all done: found it in /srv/app" {
		t.Fatalf("%q %v", got, err)
	}

	// The subagent started from nothing but its instructions and the task.
	if len(subReq.Messages) != 2 || subReq.Messages[1].Role != provider.RoleUser || subReq.Messages[1].Content != "find where the config lives" {
		t.Fatalf("a subagent sees only its system prompt and the task: %+v", subReq.Messages)
	}
	sys := subReq.Messages[0].Content
	if !strings.Contains(sys, "cannot see the main conversation") || !strings.Contains(sys, "BACKGROUND: the user likes brevity") || strings.Contains(sys, "parent system prompt") {
		t.Fatalf("subagent system prompt: %q", sys)
	}
	if subReq.Model != "parent-model" {
		t.Fatalf("model %q", subReq.Model)
	}

	// The parent's history holds the task and the answer, not the subagent's work.
	var roles []string
	for _, m := range parentFinal.Messages {
		roles = append(roles, string(m.Role))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" {
		t.Fatalf("the parent's conversation should not contain the subagent's turns: %v", roles)
	}

	// Subagents get the working tools but never the ones that change Talaria itself.
	have := toolNames(subReq)
	for _, want := range []string{"read_file", "write_file", "shell", "list_dir", "current_time", "skill_view", "cron_list"} {
		if !have[want] {
			t.Errorf("a subagent should have %s", want)
		}
	}
	// Written out here, not read from the package, so the test cannot agree with a wrong list.
	for _, banned := range []string{"delegate", "memory", "skill_save", "skill_delete", "cron_add", "cron_remove"} {
		if have[banned] {
			t.Errorf("a subagent must not have %s", banned)
		}
	}
	if !toolNames(parentFinal)["delegate"] {
		t.Error("the parent keeps delegate")
	}
}

func TestSubagentRisksGoToTheUserLabelled(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "x"})
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req) && !lastTool(req):
			return toolCalls(provider.ToolCall{ID: "w", Name: "write_file", Arguments: string(args)}), nil
		case isSub(req):
			return text("write result: " + req.Messages[len(req.Messages)-1].Content), nil
		case lastTool(req):
			return text(req.Messages[len(req.Messages)-1].Content), nil
		}
		return toolCalls(delegateCall("1", "write the file", "writer")), nil
	})

	for _, allow := range []bool{false, true} {
		os.Remove(target)
		a := newParent(p, NewPool(Limits{}))
		var asked []string
		var mu sync.Mutex
		a.Approve = func(name, args string) bool {
			mu.Lock()
			asked = append(asked, name)
			mu.Unlock()
			return allow
		}
		got, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		if len(asked) != 1 || asked[0] != "write_file (subagent writer)" {
			t.Fatalf("the user is asked, and told which subagent is asking: %v", asked)
		}
		_, statErr := os.Stat(target)
		if allow != (statErr == nil) {
			t.Fatalf("allow=%v but file exists=%v", allow, statErr == nil)
		}
		if !allow && !strings.Contains(got, "denied") {
			t.Fatalf("a refusal reaches the subagent, and so its answer: %q", got)
		}
	}

	// With no approver at all, nothing risky runs.
	os.Remove(target)
	a := newParent(p, NewPool(Limits{}))
	if got, _ := a.Run(context.Background(), "go"); !strings.Contains(got, "denied") {
		t.Fatalf("no approver means denied: %q", got)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("written without approval")
	}
}

// Every subagent waits until all of them are running, so this only passes if
// delegations in one turn really run at the same time.
func TestDelegationsInOneTurnRunConcurrently(t *testing.T) {
	var inside sync.WaitGroup
	inside.Add(3)
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req):
			task := req.Messages[1].Content
			inside.Done()
			met := make(chan struct{})
			go func() { inside.Wait(); close(met) }()
			select {
			case <-met:
			case <-time.After(3 * time.Second):
				return provider.Message{}, fmt.Errorf("never ran alongside the others")
			}
			if task == "task one" {
				time.Sleep(80 * time.Millisecond) // finishes last
			}
			return text("result of " + task), nil
		case lastTool(req):
			var parts []string
			for _, m := range req.Messages {
				if m.Role == provider.RoleTool {
					parts = append(parts, m.Content)
				}
			}
			return text(strings.Join(parts, " | ")), nil
		}
		return toolCalls(delegateCall("1", "task one", "one"), delegateCall("2", "task two", "two"), delegateCall("3", "task three", "three")), nil
	})
	a := newParent(p, NewPool(Limits{}))

	got, err := a.Run(context.Background(), "go")
	if err != nil || got != "result of task one | result of task two | result of task three" {
		t.Fatalf("results should come back in the order they were asked for: %q %v", got, err)
	}
}

func TestAtMostMaxParallelSubagentsRunAtOnce(t *testing.T) {
	var now, peak atomic.Int32
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req):
			n := now.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			now.Add(-1)
			return text("done " + req.Messages[1].Content), nil
		case lastTool(req):
			n := 0
			for _, m := range req.Messages {
				if m.Role == provider.RoleTool && strings.HasPrefix(m.Content, "done ") {
					n++
				}
			}
			return text(fmt.Sprintf("%d finished", n)), nil
		}
		var cs []provider.ToolCall
		for i := 0; i < 6; i++ {
			cs = append(cs, delegateCall(fmt.Sprint(i), fmt.Sprintf("job %d", i), ""))
		}
		return toolCalls(cs...), nil
	})
	a := newParent(p, NewPool(Limits{MaxParallel: 2}))

	got, err := a.Run(context.Background(), "go")
	if err != nil || got != "6 finished" {
		t.Fatalf("%q %v", got, err)
	}
	if peak.Load() != 2 {
		t.Fatalf("at most 2 may run at once, and 2 should have: peak %d", peak.Load())
	}
}

func TestSubagentThatNeverFinishesHitsItsStepLimit(t *testing.T) {
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req): // always wants one more tool call
			return toolCalls(provider.ToolCall{ID: "t", Name: "current_time", Arguments: "{}"}), nil
		case lastTool(req):
			return text(req.Messages[len(req.Messages)-1].Content), nil
		}
		return toolCalls(delegateCall("1", "loop forever", "looper")), nil
	})
	a := newParent(p, NewPool(Limits{MaxSteps: 3}))

	got, err := a.Run(context.Background(), "go")
	if err != nil || !strings.Contains(got, `subagent "looper" failed: stopped after 3 steps`) {
		t.Fatalf("the parent should be told, so it can recover: %q %v", got, err)
	}
}

func TestStoppingTheParentStopsItsSubagents(t *testing.T) {
	started := make(chan struct{})
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		if isSub(req) {
			close(started)
			<-ctx.Done()
			return provider.Message{}, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return provider.Message{}, err
		}
		return toolCalls(delegateCall("1", "long task", "")), nil
	})
	a := newParent(p, NewPool(Limits{}))

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := a.Run(ctx, "go"); errc <- err }()
	<-started
	cancel()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected the run to end with an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling the parent left a subagent running")
	}
}

func TestSubagentTimeout(t *testing.T) {
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req):
			<-ctx.Done()
			return provider.Message{}, ctx.Err()
		case lastTool(req):
			return text(req.Messages[len(req.Messages)-1].Content), nil
		}
		return toolCalls(delegateCall("1", "slow", "slowpoke")), nil
	})
	pool := NewPool(Limits{})
	pool.Timeout = 100 * time.Millisecond
	a := newParent(p, pool)

	got, err := a.Run(context.Background(), "go")
	if err != nil || !strings.Contains(got, `subagent "slowpoke" timed out after 100ms`) {
		t.Fatalf("%q %v", got, err)
	}
}

func TestSubagentUsesTheParentsCurrentModel(t *testing.T) {
	var gotModel string
	original := chatFunc(func(context.Context, provider.Request) (provider.Message, error) {
		t.Error("the subagent should not use the model the parent has since switched away from")
		return text("x"), nil
	})
	switched := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req):
			gotModel = req.Model
			return text("from the new model"), nil
		case lastTool(req):
			return text(req.Messages[len(req.Messages)-1].Content), nil
		}
		return toolCalls(delegateCall("1", "task", "")), nil
	})
	a := newParent(original, NewPool(Limits{}))
	a.Provider, a.Model = switched, "switched-model" // as /model does between messages

	if got, err := a.Run(context.Background(), "go"); err != nil || got != "from the new model" || gotModel != "switched-model" {
		t.Fatalf("%q %v model=%q", got, err, gotModel)
	}
}

func TestSubagentProgressShowsUpInTheParentsHook(t *testing.T) {
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req) && !lastTool(req):
			return toolCalls(provider.ToolCall{ID: "t", Name: "current_time", Arguments: "{}"}), nil
		case isSub(req):
			return text("checked"), nil
		case lastTool(req):
			return text("fine"), nil
		}
		return toolCalls(delegateCall("1", "check the clock", "clock [check]")), nil
	})
	a := newParent(p, NewPool(Limits{}))
	var seen []string
	var mu sync.Mutex
	a.OnToolStart = func(name, _ string) { mu.Lock(); seen = append(seen, name); mu.Unlock() }

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	want := []string{"delegate", "  ↳ [clock (check)] current_time"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", seen, want)
	}
}

func TestBadCallsAndOddAnswers(t *testing.T) {
	run := func(raw string) (string, error) {
		a := newParent(chatFunc(func(context.Context, provider.Request) (provider.Message, error) { return text("x"), nil }), NewPool(Limits{}))
		tl, ok := a.Tools.Get("delegate")
		if !ok {
			t.Fatal("no delegate tool")
		}
		return tl.Run(context.Background(), json.RawMessage(raw))
	}
	if _, err := run(`{"task":"   "}`); err == nil || !strings.Contains(err.Error(), "task is empty") {
		t.Fatalf("got %v", err)
	}
	if _, err := run(`not json`); err == nil {
		t.Fatal("malformed arguments are an error")
	}

	answering := func(answer string) string {
		p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
			switch {
			case isSub(req):
				return text(answer), nil
			case lastTool(req):
				return text(req.Messages[len(req.Messages)-1].Content), nil
			}
			return toolCalls(delegateCall("1", "task", "")), nil
		})
		got, err := newParent(p, NewPool(Limits{})).Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := answering("  \n "); got != "(the subagent finished without giving a result)" {
		t.Fatalf("an empty answer gets a placeholder: %q", got)
	}
	long := answering(strings.Repeat("é", 20000)) // 40 KB of two-byte characters
	if !utf8.ValidString(long) || !strings.Contains(long, "cut here") || len(long) > maxResult+200 {
		t.Fatalf("an over-long answer is cut cleanly (%d bytes, valid=%v)", len(long), utf8.ValidString(long))
	}
}

func TestToolShape(t *testing.T) {
	a := newParent(chatFunc(func(context.Context, provider.Request) (provider.Message, error) { return text("x"), nil }), NewPool(Limits{}))
	tl, _ := a.Tools.Get("delegate")
	if !tl.Parallel {
		t.Error("delegations in one turn must be able to run together")
	}
	if tl.NeedsApproval {
		t.Error("delegating is not itself risky: the subagent's own risky calls ask the user")
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(tl.Schema, &schema) != nil || len(schema.Required) != 1 || schema.Required[0] != "task" {
		t.Fatalf("schema: %s", tl.Schema)
	}
}

func TestCleanLabel(t *testing.T) {
	for _, c := range []struct{ name, task, want string }{
		{"research", "x", "research"},
		{"  two\nlines  ", "x", "two lines"},
		{"", "Find the\nconfig file", "Find the config file"},
		{"a [tag] b", "x", "a (tag) b"},
		{strings.Repeat("é", 50), "x", strings.Repeat("é", 40) + "…"},
	} {
		if got := cleanLabel(c.name, c.task); got != c.want {
			t.Errorf("cleanLabel(%q, %q) = %q, want %q", c.name, c.task, got, c.want)
		}
	}
}

func TestLimits(t *testing.T) {
	pool := NewPool(Limits{})
	if cap(pool.sem) != 4 || pool.MaxSteps != 15 {
		t.Fatalf("defaults: parallel %d, steps %d", cap(pool.sem), pool.MaxSteps)
	}
	pool = NewPool(Limits{MaxParallel: 2, MaxSteps: 7})
	if cap(pool.sem) != 2 || pool.MaxSteps != 7 {
		t.Fatalf("explicit: parallel %d, steps %d", cap(pool.sem), pool.MaxSteps)
	}
	for _, bad := range []Limits{{MaxParallel: -1}, {MaxParallel: 17}, {MaxSteps: -1}, {MaxSteps: 101}} {
		if bad.Validate() == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
	if err := (Limits{MaxParallel: 16, MaxSteps: 100}).Validate(); err != nil {
		t.Fatalf("the ceilings themselves are allowed: %v", err)
	}
}

// The background is asked for at each delegation, so what it says (the user's
// notes, where the tools run) is current for every subagent, not as of the
// moment the tool was registered.
func TestBackgroundIsCurrentAtEachDelegation(t *testing.T) {
	var systems []string
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req):
			systems = append(systems, req.Messages[0].Content)
			return text("ok"), nil
		case lastTool(req):
			return text("fine"), nil
		}
		return toolCalls(delegateCall("1", "task", "")), nil
	})
	a := newParent(p, NewPool(Limits{}))
	where := "the first place"
	NewPool(Limits{}).Register(a, func() string { return "tools run in " + where })

	for _, w := range []string{"the first place", "the second place"} {
		where = w
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
	}
	if len(systems) != 2 || !strings.HasSuffix(systems[0], "tools run in the first place") || !strings.HasSuffix(systems[1], "tools run in the second place") {
		t.Fatalf("each subagent should get the background as it is now: %q", systems)
	}

	// No background at all is fine.
	b := newParent(p, NewPool(Limits{}))
	NewPool(Limits{}).Register(b, nil)
	systems = nil
	if _, err := b.Run(context.Background(), "go"); err != nil || len(systems) != 1 || strings.Contains(systems[0], "\n\n\n") {
		t.Fatalf("%v %q", err, systems)
	}
}

type recordingGate struct {
	mu      sync.Mutex
	deny    string
	entries []agent.Entry
}

func (g *recordingGate) Verdict(tool string, needs bool) agent.Verdict {
	switch {
	case tool == g.deny:
		return agent.Verdict{Action: agent.Deny, Via: "rule"}
	case needs:
		return agent.Verdict{Action: agent.Ask, Via: "auto"}
	}
	return agent.Verdict{Action: agent.Allow, Via: "auto"}
}
func (g *recordingGate) Scrub(s string) string {
	return strings.ReplaceAll(s, "hunter2hunter2", "[redacted]")
}
func (g *recordingGate) Record(e agent.Entry) {
	g.mu.Lock()
	g.entries = append(g.entries, e)
	g.mu.Unlock()
}

// A subagent is bound by the same rules as the agent that started it, so
// delegating is no way round a rule, and its calls are on the record under its name.
func TestSubagentsAreHeldToTheParentsGate(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "x"})
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req) && !lastTool(req):
			return toolCalls(provider.ToolCall{ID: "w", Name: "write_file", Arguments: string(args)}), nil
		case isSub(req):
			return text("result: " + req.Messages[len(req.Messages)-1].Content), nil
		case lastTool(req):
			return text(req.Messages[len(req.Messages)-1].Content), nil
		}
		return toolCalls(delegateCall("1", "write the file", "writer")), nil
	})
	a := newParent(p, NewPool(Limits{}))
	gate := &recordingGate{deny: "write_file"}
	a.Gate = gate
	a.Approve = func(string, string) bool { t.Fatal("a forbidden call is never put to the user"); return true }

	got, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("the subagent wrote a file the rules forbid")
	}
	if !strings.Contains(got, "permission rules") {
		t.Fatalf("%q", got)
	}
	var sub *agent.Entry
	for i, e := range gate.entries {
		if e.Tool == "write_file" {
			sub = &gate.entries[i]
		}
	}
	if sub == nil || sub.Label != "writer" || sub.Allowed {
		t.Fatalf("the refused call should be recorded under the subagent's name: %+v", gate.entries)
	}
}

func TestBaseToolOfASubagentPrompt(t *testing.T) {
	if got := agent.BaseTool(agent.Labelled("write_file", "writer")); got != "write_file" {
		t.Fatal(got)
	}
}

// A conversation has one browser tab, and subagents run at the same time.
func TestSubagentsDoNotGetTheBrowser(t *testing.T) {
	var subTools, parentTools map[string]bool
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req):
			subTools = toolNames(req)
			return text("done"), nil
		case lastTool(req):
			return text("ok"), nil
		}
		parentTools = toolNames(req)
		return toolCalls(delegateCall("1", "look", "scout")), nil
	})
	a := newParent(p, NewPool(Limits{}))
	for _, name := range []string{"browser_open", "browser_click"} {
		a.Tools.Register(tool.Tool{Name: name, Run: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }})
	}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if !parentTools["browser_open"] || subTools["browser_open"] || subTools["browser_click"] || !subTools["read_file"] {
		t.Fatalf("parent %v, subagent %v", parentTools, subTools)
	}
}

// A subagent spends from the same budget of model requests as the agent that started it, and holds the same
// guard on outside content, and starts as tainted as the conversation was.
func TestASubagentInheritsTheBudgetAndWhatTheParentHasRead(t *testing.T) {
	var sawSub bool
	p := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req):
			sawSub = true
			return text("sub done"), nil
		case lastTool(req):
			return text("fin"), nil
		}
		return toolCalls(delegateCall("1", "do a thing", "w")), nil
	})
	pool := NewPool(Limits{})
	a := newParent(p, pool)
	var spent int
	a.BeforeModel = func() error { spent++; return nil }
	if _, err := a.Run(context.Background(), "go"); err != nil || !sawSub {
		t.Fatalf("%v %v", err, sawSub)
	}
	if spent != 3 {
		t.Fatalf("parent twice and the subagent once, all counted: %d", spent)
	}

	// A subagent that the budget refuses does not reach the model.
	sawSub = false
	a2 := newParent(p, pool)
	n := 0
	a2.BeforeModel = func() error {
		n++
		if n > 1 {
			return fmt.Errorf("budget used up")
		}
		return nil
	}
	got, _ := a2.Run(context.Background(), "go")
	if sawSub {
		t.Fatalf("the subagent ran past the budget: %q", got)
	}

	// What the parent has read, the subagent has read.
	var subAsked []string
	p3 := chatFunc(func(ctx context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req) && lastTool(req):
			return text("sub done"), nil
		case isSub(req):
			return toolCalls(provider.ToolCall{ID: "s", Name: "poster", Arguments: `{}`}), nil
		case lastTool(req):
			return text("fin"), nil
		}
		return toolCalls(delegateCall("1", "do a thing", "w")), nil
	})
	a3 := newParent(p3, pool)
	a3.Tools.Register(tool.Tool{Name: "poster", Egress: tool.Always, Run: func(context.Context, json.RawMessage) (string, error) { return "posted", nil }})
	a3.GuardUntrusted = true
	a3.Approve = func(name, args string) bool {
		subAsked = append(subAsked, name)
		return strings.HasPrefix(name, "delegate")
	}
	a3.MarkUntrusted()
	a3.Run(context.Background(), "go")
	var sawFlagged bool
	for _, n := range subAsked {
		sawFlagged = sawFlagged || strings.Contains(n, "poster (after reading outside content) (subagent w)")
	}
	if !sawFlagged {
		t.Fatalf("a subagent of a conversation that read outside content asks before something that could send it out, with its own label on it too: %v", subAsked)
	}
}
