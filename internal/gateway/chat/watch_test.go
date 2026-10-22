package chat

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/agent"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

// watcher is a chat service that also wants to see tool calls and when the agent is working.
type watcher struct {
	*fakeService
	mu  sync.Mutex
	log []string
}

func (w *watcher) note(s string) { w.mu.Lock(); w.log = append(w.log, s); w.mu.Unlock() }
func (w *watcher) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.log...)
}
func (w *watcher) RunStart(int64)               { w.note("run start") }
func (w *watcher) RunEnd(int64)                 { w.note("run end") }
func (w *watcher) ToolStart(_ int64, n string)  { w.note("tool start " + n) }
func (w *watcher) ToolEnd(_ int64, n, r string) { w.note("tool end " + n + ": " + r) }
func (w *watcher) Send(ctx context.Context, c int64, text string) (int64, error) {
	w.note("send " + text)
	return w.fakeService.Send(ctx, c, text)
}

type toolThenAnswer struct{}

func (toolThenAnswer) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	if req.Messages[len(req.Messages)-1].Role == provider.RoleTool {
		return provider.Message{Role: provider.RoleAssistant, Content: "all done"}, nil
	}
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "ok_tool", Arguments: `{}`}, {ID: "2", Name: "bad_tool", Arguments: `{}`}}}, nil
}

func TestATransportThatWatchesIsToldOfEachToolAndOfTheRunFromStartToFinish(t *testing.T) {
	svc := &watcher{fakeService: newFakeService(4000, false)}
	reg := tool.NewRegistry()
	reg.Register(tool.Tool{Name: "ok_tool", Description: "x", Schema: json.RawMessage(`{"type":"object"}`), Run: func(context.Context, json.RawMessage) (string, error) { return "fine", nil }})
	reg.Register(tool.Tool{Name: "bad_tool", Description: "x", Schema: json.RawMessage(`{"type":"object"}`), Run: func(context.Context, json.RawMessage) (string, error) { return "", context.DeadlineExceeded }})
	e := &Engine{T: svc, Allowed: map[int64]bool{1: true}, Logf: func(string, ...any) {},
		NewAgent: func(int64, func(string, string) bool) *agent.Agent {
			return &agent.Agent{Provider: toolThenAnswer{}, Model: "m", Tools: reg, MaxSteps: 4}
		}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); e.Wait(2 * time.Second) })
	e.Start(ctx)
	e.Incoming(1, 1, "go")

	waitFor(t, "the run to end", func() bool { return strings.Contains(strings.Join(svc.lines(), "\n"), "run end") })
	got := strings.Join(svc.lines(), "\n")
	for _, want := range []string{"run start", "tool start ok_tool", "tool end ok_tool: fine", "tool start bad_tool", "send all done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "tool end bad_tool: error: ") {
		t.Fatalf("a failing tool is not reported as an error:\n%s", got)
	}
	lines := svc.lines()
	idx := func(prefix string) int {
		for i, l := range lines {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		return -1
	}
	if !(idx("run start") < idx("tool start") && idx("tool start") < idx("tool end") && idx("tool end") < idx("send all done") && idx("send all done") < idx("run end")) {
		t.Fatalf("out of order, and the run must end only after the reply is delivered:\n%s", got)
	}
	// Later runs are told too, and a run that ends leaves no hooks on the agent.
	e.Incoming(1, 1, "again")
	waitFor(t, "a second run", func() bool { return strings.Count(strings.Join(svc.lines(), "\n"), "run end") == 2 })
}

func TestACommandIsNotARun(t *testing.T) {
	svc := &watcher{fakeService: newFakeService(4000, false)}
	e := &Engine{T: svc, Allowed: map[int64]bool{1: true}, Logf: func(string, ...any) {},
		NewAgent: func(int64, func(string, string) bool) *agent.Agent {
			return &agent.Agent{Provider: toolThenAnswer{}, Model: "m", Tools: tool.NewRegistry()}
		}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); e.Wait(time.Second) })
	e.Start(ctx)
	e.Incoming(1, 1, "/help")
	waitFor(t, "the help text", func() bool { return strings.Contains(strings.Join(svc.lines(), "\n"), "send Talaria is ready") })
	if strings.Contains(strings.Join(svc.lines(), "\n"), "run start") {
		t.Fatal("a command made the page think the agent was working")
	}
}
