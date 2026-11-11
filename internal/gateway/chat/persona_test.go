package chat

import (
	"context"
	"strings"
	"testing"

	"talaria/internal/agent"
	"talaria/internal/provider"
)

func TestANamedAgentAddsItsInstructionsAndTheUsualOnesComeBack(t *testing.T) {
	e := &Engine{T: newFakeService(20000, false), root: context.Background()}
	e.chats = map[int64]*chat{}
	c := e.chatFor(1)
	c.mu.Lock()
	c.agent, c.baseSystem = &agent.Agent{System: "You are Talaria."}, "You are Talaria."
	c.mu.Unlock()
	e.SetHistory(1, []provider.Message{{Role: provider.RoleUser, Content: "hi"}})

	e.SetPersona(1, &Persona{Name: "Inbox", System: "In this conversation you are Inbox.", ReadOnly: true})
	if got := c.agent.System; !strings.HasPrefix(got, "You are Talaria.") || !strings.Contains(got, "you are Inbox") {
		t.Fatalf("%q", got)
	}
	if !c.agent.ReadOnly {
		t.Fatal("a read-only agent makes the chat's agent read-only")
	}
	// A different agent replaces the first one's words rather than adding to them.
	e.SetPersona(1, &Persona{Name: "Research", System: "In this conversation you are Research."})
	if got := c.agent.System; strings.Contains(got, "Inbox") || !strings.Contains(got, "Research") || c.agent.ReadOnly {
		t.Fatalf("%q %v", got, c.agent.ReadOnly)
	}
	// Back to the usual agent: nothing of it is left, and what was said stays.
	e.SetPersona(1, nil)
	if got := c.agent.System; got != "You are Talaria." || c.agent.ReadOnly {
		t.Fatalf("%q %v", got, c.agent.ReadOnly)
	}
	if h := e.History(1); len(h) != 1 || h[0].Content != "hi" {
		t.Fatalf("%+v", h)
	}
}

func TestAnAgentThatDoesNotExistYetIsAppliedWhenItIsBuilt(t *testing.T) {
	e := &Engine{T: newFakeService(20000, false), root: context.Background()}
	e.chats = map[int64]*chat{}
	e.SetPersona(1, &Persona{Name: "Inbox", System: "You are Inbox.", Profile: "cheap"})
	c := e.chatFor(1)
	c.mu.Lock()
	c.agent, c.baseSystem = &agent.Agent{System: "base"}, "base"
	c.applyPersona() // what the engine does the moment it builds the agent
	c.mu.Unlock()
	if got := c.agent.System; got != "base\n\nYou are Inbox." {
		t.Fatalf("%q", got)
	}
	if p, _ := e.Selection(1); p != "cheap" {
		t.Fatalf("a job started from this chat uses the agent's model: %q", p)
	}
}
