package agent

import (
	"context"
	"strings"
	"testing"

	"talaria/internal/provider"
)

// streamingProvider speaks its reply through req.OnText the way a real provider does.
type streamingProvider struct {
	replies []provider.Message
	sawText []bool // whether each request asked for streaming
}

func (s *streamingProvider) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	s.sawText = append(s.sawText, req.OnText != nil)
	r := s.replies[0]
	s.replies = s.replies[1:]
	if req.OnText != nil {
		for _, word := range strings.SplitAfter(r.Content, " ") {
			req.OnText(word)
		}
	}
	return r, nil
}

func TestOnTextStreamsEveryTurnIncludingBeforeToolCalls(t *testing.T) {
	p := &streamingProvider{replies: []provider.Message{
		{Role: provider.RoleAssistant, Content: "Checking the time. ", ToolCalls: []provider.ToolCall{{ID: "1", Name: "current_time", Arguments: "{}"}}},
		{Role: provider.RoleAssistant, Content: "It is late."},
	}}
	a := newAgent(p)
	var streamed strings.Builder
	a.OnText = func(s string) { streamed.WriteString(s) }

	reply, err := a.Run(context.Background(), "what time is it")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "It is late." {
		t.Fatalf("Run still returns the final reply, got %q", reply)
	}
	if streamed.String() != "Checking the time. It is late." {
		t.Fatalf("streamed %q", streamed.String())
	}
	if len(p.sawText) != 2 || !p.sawText[0] || !p.sawText[1] {
		t.Fatalf("every request should ask for streaming: %v", p.sawText)
	}
}

func TestNoOnTextMeansNoStreaming(t *testing.T) {
	p := &streamingProvider{replies: []provider.Message{{Role: provider.RoleAssistant, Content: "hi"}}}
	if _, err := newAgent(p).Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if len(p.sawText) != 1 || p.sawText[0] {
		t.Fatalf("streaming must stay off unless OnText is set: %v", p.sawText)
	}
}

// Context is extra system text asked for on every call. It must reach the model
// each time but never be written into the conversation, so a later change to it
// cannot leave a stale statement at the top of the history.
func TestContextIsAddedToEachRequestButNotStored(t *testing.T) {
	note := "tools run in the sandbox"
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, Content: "one"},
		{Role: provider.RoleAssistant, Content: "two"},
		{Role: provider.RoleAssistant, Content: "three"},
	}}
	a := newAgent(p)
	a.System = "base prompt"
	a.Context = func() string { return note }

	for _, in := range []string{"a", "b"} {
		if _, err := a.Run(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	for i, req := range p.seen[:2] {
		if got := req[0].Content; got != "base prompt\n\ntools run in the sandbox" {
			t.Fatalf("request %d system prompt: %q", i, got)
		}
	}

	note = "tools run on the build host" // changes between turns
	if _, err := a.Run(context.Background(), "c"); err != nil {
		t.Fatal(err)
	}
	if got := p.seen[2][0].Content; got != "base prompt\n\ntools run on the build host" {
		t.Fatalf("the new text applies from the next call, replacing the old: %q", got)
	}
	if strings.Contains(a.messages[0].Content, "sandbox") || a.messages[0].Content != "base prompt" {
		t.Fatalf("the stored history must not hold it: %q", a.messages[0].Content)
	}
}

func TestContextWithNoSystemPromptOrNoText(t *testing.T) {
	p := &scripted{replies: []provider.Message{{Role: provider.RoleAssistant, Content: "x"}, {Role: provider.RoleAssistant, Content: "y"}}}
	a := newAgent(p) // no System
	a.Context = func() string { return "only context" }
	a.Run(context.Background(), "hi")
	if r := p.seen[0]; r[0].Role != provider.RoleSystem || r[0].Content != "only context" || r[1].Content != "hi" {
		t.Fatalf("context becomes the system prompt when there is none: %+v", r)
	}

	b := newAgent(p)
	b.System = "base"
	b.Context = func() string { return "" }
	b.Run(context.Background(), "hi")
	if got := p.seen[1][0].Content; got != "base" {
		t.Fatalf("empty context adds nothing, not a stray separator: %q", got)
	}
}
