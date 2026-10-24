package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/provider"
	"talaria/internal/tool"
)

// scripted replays canned assistant messages and records what it was sent.
type scripted struct {
	replies []provider.Message
	seen    [][]provider.Message
}

func (s *scripted) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	s.seen = append(s.seen, append([]provider.Message(nil), req.Messages...))
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}

func newAgent(p provider.Provider) *Agent {
	reg := tool.NewRegistry()
	tool.RegisterBuiltins(reg)
	return &Agent{Provider: p, Model: "test", Tools: reg}
}

// A tool that ignores cancellation cannot be killed, but the chat must not
// wedge behind it: when the time is up the call is abandoned and the model is
// told it timed out.
func TestATranscriptSurvivesARestore(t *testing.T) {
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, Content: "first answer"},
		{Role: provider.RoleAssistant, Content: "second answer"},
	}}
	a := newAgent(p)
	a.System = "be brief"
	if _, err := a.Run(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	taken := a.Transcript()
	if len(taken) != 2 || taken[0].Content != "one" || taken[1].Content != "first answer" {
		t.Fatalf("the transcript is the conversation without the system prompt: %+v", taken)
	}
	b := newAgent(p)
	b.System = "be brief"
	b.Restore(taken)
	if _, err := b.Run(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	seen := p.seen[len(p.seen)-1]
	if len(seen) != 4 || seen[0].Role != provider.RoleSystem || seen[0].Content != "be brief" ||
		seen[1].Content != "one" || seen[2].Content != "first answer" || seen[3].Content != "two" {
		t.Fatalf("the restored conversation carries on with its history: %+v", seen)
	}
	if b.ReadOutside() {
		t.Fatal("nothing from outside was read, so nothing is tainted")
	}
}

func TestAToolThatIgnoresCancellationIsAbandonedOnTimeout(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.Tool{Name: "hang", Run: func(context.Context, json.RawMessage) (string, error) {
		select {}
	}})
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "hang", Arguments: `{}`}}},
		{Role: provider.RoleAssistant, Content: "done"},
	}}
	a := &Agent{Provider: p, Model: "test", Tools: reg, ToolTimeout: 50 * time.Millisecond}
	start := time.Now()
	got, err := a.Run(context.Background(), "hang")
	if err != nil {
		t.Fatal(err)
	}
	if got != "done" || time.Since(start) > 5*time.Second {
		t.Fatalf("the run wedged: %q in %s", got, time.Since(start))
	}
	last := p.seen[1][len(p.seen[1])-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "timed out after 50ms") {
		t.Fatalf("the model was not told about the timeout: %+v", last)
	}
}

// When the run itself ends first, the tool reports that — not a timeout.
func TestAToolReportsACancelledRunAsCancelled(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.Tool{Name: "hang", Run: func(context.Context, json.RawMessage) (string, error) {
		select {}
	}})
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "hang", Arguments: `{}`}}},
		{Role: provider.RoleAssistant, Content: "done"},
	}}
	a := &Agent{Provider: p, Model: "test", Tools: reg}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := a.Run(ctx, "hang"); err != nil {
		t.Fatal(err)
	}
	last := p.seen[1][len(p.seen[1])-1]
	if !strings.Contains(last.Content, "context canceled") || strings.Contains(last.Content, "timed out") {
		t.Fatalf("a cancelled run must not look like a timeout: %+v", last)
	}
}

func TestToolTimeoutStretchesToTheRunsRemainingTime(t *testing.T) {
	a := &Agent{}
	if got := a.toolTimeout(context.Background()); got != 10*time.Minute {
		t.Fatalf("no deadline: %s", got)
	}
	long, cancelLong := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancelLong()
	if got := a.toolTimeout(long); got < time.Hour || got > 2*time.Hour {
		t.Fatalf("a 2h run keeps less than its budget: %s", got)
	}
	short, cancelShort := context.WithTimeout(context.Background(), time.Minute)
	defer cancelShort()
	if got := a.toolTimeout(short); got != 10*time.Minute {
		t.Fatalf("a short run must not shrink the ceiling: %s", got)
	}
	explicit := &Agent{ToolTimeout: time.Minute}
	if got := explicit.toolTimeout(long); got != time.Minute {
		t.Fatalf("an explicit timeout loses: %s", got)
	}
}

func TestRunExecutesToolThenAnswers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("hello talaria"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "read_file", Arguments: `{"path":"` + path + `"}`}}},
		{Role: provider.RoleAssistant, Content: "done"},
	}}

	got, err := newAgent(p).Run(context.Background(), "read it")
	if err != nil {
		t.Fatal(err)
	}
	if got != "done" {
		t.Fatalf("got %q, want done", got)
	}
	last := p.seen[1][len(p.seen[1])-1]
	if last.Role != provider.RoleTool || last.ToolCallID != "1" || last.Content != "hello talaria" {
		t.Fatalf("tool result not fed back correctly: %+v", last)
	}
}

func TestApprovalRequiredForSideEffects(t *testing.T) {
	target := filepath.Join(t.TempDir(), "out.txt")
	call := provider.ToolCall{ID: "1", Name: "write_file", Arguments: `{"path":"` + target + `","content":"x"}`}
	newScript := func() *scripted {
		return &scripted{replies: []provider.Message{
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
			{Role: provider.RoleAssistant, Content: "ok"},
		}}
	}

	// No Approve func: denied, file must not exist.
	p := newScript()
	if _, err := newAgent(p).Run(context.Background(), "write"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("file was written without approval")
	}
	if got := p.seen[1][len(p.seen[1])-1].Content; !strings.Contains(got, "denied") {
		t.Fatalf("model was not told the call was denied: %q", got)
	}

	// Approved: file is written.
	a := newAgent(newScript())
	a.Approve = func(string, string) bool { return true }
	if _, err := a.Run(context.Background(), "write"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "x" {
		t.Fatalf("file not written after approval: %q %v", b, err)
	}
}

func TestUnknownToolAndStepLimit(t *testing.T) {
	loop := provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "nope", Arguments: `{}`}}}
	p := &scripted{replies: []provider.Message{loop, loop, loop}}
	a := newAgent(p)
	a.MaxSteps = 3
	if _, err := a.Run(context.Background(), "go"); err == nil || !strings.Contains(err.Error(), "3 steps") {
		t.Fatalf("expected step-limit error, got %v", err)
	}
	if got := p.seen[1][len(p.seen[1])-1].Content; !strings.Contains(got, "unknown tool") {
		t.Fatalf("unknown tool not reported to model: %q", got)
	}
}

// A model's thinking is part of the conversation as far as the API is
// concerned, so the loop must keep it on the message it came with, and across
// further turns.
func TestTheLoopKeepsTheModelsReasoningOnItsMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "n.txt")
	os.WriteFile(path, []byte("x"), 0o644)
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, Reasoning: "read it first", ToolCalls: []provider.ToolCall{{ID: "1", Name: "read_file", Arguments: `{"path":"` + path + `"}`}}},
		{Role: provider.RoleAssistant, Content: "done", Reasoning: "it said x"},
		{Role: provider.RoleAssistant, Content: "yes", Reasoning: "easy"},
	}}
	a := newAgent(p)
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "really?"); err != nil {
		t.Fatal(err)
	}
	last := p.seen[2]
	var got []string
	for _, m := range last {
		if m.Role == provider.RoleAssistant {
			got = append(got, m.Reasoning)
		}
	}
	if strings.Join(got, "|") != "read it first|it said x" {
		t.Fatalf("every earlier assistant message keeps its reasoning: %q", got)
	}
}
