package main

import (
	"context"
	"strings"
	"testing"

	"talaria/internal/app"
	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
)

// reviewProvider plays a cheap model: it first writes one note with the only
// tool it was given, then answers.
type reviewProvider struct {
	calls   int
	offered []string
	system  string
	lastAsk string
}

func (p *reviewProvider) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	p.calls++
	for _, t := range req.Tools {
		p.offered = append(p.offered, t.Name)
	}
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleSystem:
			p.system = m.Content
		case provider.RoleUser:
			p.lastAsk = m.Content
		}
	}
	if p.calls == 1 {
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID:        "call-1",
			Name:      "memory",
			Arguments: `{"action":"add","target":"user","content":"moved to Abuja"}`,
		}}}, nil
	}
	return provider.Message{Role: provider.RoleAssistant, Content: "Noted one fact."}, nil
}

// The pass that makes learning happen: it reads the conversations since the
// last review, writes what it learned, and does not read the same history twice.
func TestTheReviewLearnsFromNewConversations(t *testing.T) {
	home := t.TempDir()
	rt := app.Open(app.Config{Home: home})
	defer rt.Close()
	sid, err := rt.Sessions().StartFor("")
	if err != nil {
		t.Fatal(err)
	}
	rt.Sessions().Add(sid, "user", "We moved to Abuja last month.")
	rt.Sessions().Add(sid, "assistant", "Good to know.")

	fake := &reviewProvider{}
	backends := &chat.Backends{Default: "cheap", Names: []string{"cheap"},
		Open: func(string) (chat.Backend, error) { return chat.Backend{Provider: fake, Model: "small-model"}, nil }}
	r := newMemoryReviewer(rt, backends, func() string { return "cheap" }, nil)

	res, err := r.Run(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions != 1 || res.Added != 1 {
		t.Fatalf("review = %+v", res)
	}
	if got := rt.Notes().Prompt(); !strings.Contains(got, "moved to Abuja (learned today)") {
		t.Fatalf("nothing was learned:\n%s", got)
	}
	// Only the notes are within reach: no shell, no files, no browser.
	if len(fake.offered) != 2 || fake.offered[0] != "memory" || fake.offered[1] != "memory" {
		t.Fatalf("the reviewer was offered %v", fake.offered)
	}
	if !strings.Contains(fake.system, "Never guess") || !strings.Contains(fake.lastAsk, "Abuja") {
		t.Fatalf("the reviewer was not told what to do, or what to read")
	}

	// A second scheduled pass the same day reads nothing new, and learns nothing twice.
	again, err := r.Run(context.Background(), false)
	if err != nil || again.Sessions != 0 {
		t.Fatalf("a repeated pass = %+v %v", again, err)
	}
	entries, _, _, _ := rt.Notes().Entries("user")
	if len(entries) != 1 {
		t.Fatalf("the note was written twice: %+v", entries)
	}
}
