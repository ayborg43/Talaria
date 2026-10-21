package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/provider"
	"talaria/internal/tool"
)

type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (l *eventLog) add(s string) {
	l.mu.Lock()
	l.ev = append(l.ev, s)
	l.mu.Unlock()
}

func (l *eventLog) index(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, e := range l.ev {
		if e == s {
			return i
		}
	}
	return -1
}

func calls(names ...string) []provider.ToolCall {
	var out []provider.ToolCall
	for i, n := range names {
		out = append(out, provider.ToolCall{ID: string(rune('a' + i)), Name: n, Arguments: "{}"})
	}
	return out
}

// toolTurn scripts one model turn that asks for the given tools, then a final answer.
func toolTurn(names ...string) *scripted {
	return &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: calls(names...)},
		{Role: provider.RoleAssistant, Content: "done"},
	}}
}

func agentWith(p provider.Provider, tools ...tool.Tool) *Agent {
	reg := tool.NewRegistry()
	for _, t := range tools {
		reg.Register(t)
	}
	return &Agent{Provider: p, Model: "m", Tools: reg}
}

func TestOnToolStartComesAfterApprovalAndBeforeTheTool(t *testing.T) {
	var log eventLog
	risky := tool.Tool{Name: "risky", NeedsApproval: true, Run: func(context.Context, json.RawMessage) (string, error) {
		log.add("run:risky")
		return "ok", nil
	}}
	plain := tool.Tool{Name: "plain", Run: func(context.Context, json.RawMessage) (string, error) {
		log.add("run:plain")
		return "ok", nil
	}}

	for _, allow := range []bool{true, false} {
		log = eventLog{}
		a := agentWith(toolTurn("risky", "plain", "ghost"), risky, plain)
		a.Approve = func(name, _ string) bool { log.add("approve:" + name); return allow }
		a.OnToolStart = func(name, _ string) { log.add("start:" + name) }
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}

		if allow {
			if !(log.index("approve:risky") < log.index("start:risky") && log.index("start:risky") < log.index("run:risky")) {
				t.Fatalf("expected approve, start, run in that order: %v", log.ev)
			}
		} else if log.index("start:risky") >= 0 || log.index("run:risky") >= 0 {
			t.Fatalf("a denied tool never starts: %v", log.ev)
		}
		if log.index("start:plain") < 0 || log.index("start:ghost") >= 0 {
			t.Fatalf("every tool that runs starts, and an unknown one does not: %v", log.ev)
		}
	}
}

// Each of three tools waits until all three are running, so this only
// finishes if they really run at the same time.
func TestParallelToolsRunTogetherAndResultsKeepTheirOrder(t *testing.T) {
	var running sync.WaitGroup
	running.Add(3)
	meet := func(name, result string, extra time.Duration) tool.Tool {
		return tool.Tool{Name: name, Parallel: true, Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			running.Done()
			done := make(chan struct{})
			go func() { running.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				return "", errors.New("never ran alongside the others")
			}
			time.Sleep(extra) // the first one finishes last
			return result, nil
		}}
	}
	p := toolTurn("one", "two", "three")
	a := agentWith(p, meet("one", "R1", 80*time.Millisecond), meet("two", "R2", 0), meet("three", "R3", 0))

	var starts sync.Map
	a.OnToolStart = func(name, _ string) { starts.Store(name, true) }
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	last := p.seen[1]
	got := []string{}
	for _, m := range last[len(last)-3:] {
		got = append(got, m.ToolCallID+"="+m.Content)
	}
	if strings.Join(got, ",") != "a=R1,b=R2,c=R3" {
		t.Fatalf("results must be recorded in the order the model asked, whatever finished first: %v", got)
	}
	for _, n := range []string{"one", "two", "three"} {
		if _, ok := starts.Load(n); !ok {
			t.Fatalf("%s never reported starting", n)
		}
	}
}

func TestSequentialToolSplitsAParallelRun(t *testing.T) {
	var log eventLog
	mk := func(name string, parallel bool) tool.Tool {
		return tool.Tool{Name: name, Parallel: parallel, Run: func(context.Context, json.RawMessage) (string, error) {
			log.add("start:" + name)
			time.Sleep(30 * time.Millisecond)
			log.add("end:" + name)
			return name, nil
		}}
	}
	p := toolTurn("p1", "p2", "serial", "p3")
	a := agentWith(p, mk("p1", true), mk("p2", true), mk("serial", false), mk("p3", true))
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	if !(log.index("start:serial") > log.index("end:p1") && log.index("start:serial") > log.index("end:p2")) {
		t.Fatalf("a tool that is not parallel-safe waits for the ones before it: %v", log.ev)
	}
	if log.index("start:p3") < log.index("end:serial") {
		t.Fatalf("and the ones after it wait for it: %v", log.ev)
	}
	if !(log.index("start:p2") < log.index("end:p1")) {
		t.Fatalf("the two parallel tools at the front should overlap: %v", log.ev)
	}
	last := p.seen[1]
	var order []string
	for _, m := range last[len(last)-4:] {
		order = append(order, m.Content)
	}
	if strings.Join(order, ",") != "p1,p2,serial,p3" {
		t.Fatalf("order: %v", order)
	}
}

func TestACrashingToolBecomesAnErrorTheModelCanSee(t *testing.T) {
	boom := tool.Tool{Name: "boom", Parallel: true, Run: func(context.Context, json.RawMessage) (string, error) { panic("kaboom") }}
	fine := tool.Tool{Name: "fine", Parallel: true, Run: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }}
	p := toolTurn("boom", "fine")
	a := agentWith(p, boom, fine)

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("a tool crash must not take the agent down: %v", err)
	}
	last := p.seen[1]
	if got := last[len(last)-2].Content; !strings.Contains(got, "the tool crashed: kaboom") {
		t.Fatalf("the model should be told: %q", got)
	}
	if got := last[len(last)-1].Content; got != "ok" {
		t.Fatalf("the other tool's result is unaffected: %q", got)
	}
}
