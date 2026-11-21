package chat

import (
	"context"
	"strings"
	"sync"
	"testing"

	"talaria/internal/agent"
	"talaria/internal/provider"
	"talaria/internal/tool"
)

type recorder struct {
	mu       sync.Mutex
	systems  []string
	lastUser []string
}

func (r *recorder) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range req.Messages {
		if m.Role == provider.RoleSystem {
			r.systems = append(r.systems, m.Content)
		}
	}
	r.lastUser = append(r.lastUser, req.Messages[len(req.Messages)-1].Content)
	return provider.Message{Role: provider.RoleAssistant, Content: "ok"}, nil
}

func mentionEngine(rec *recorder, agents ...Persona) (*Engine, *fakeService) {
	fake := newFakeService(20000, false)
	e := &Engine{T: fake, root: context.Background(), Allowed: map[int64]bool{1: true},
		Agents: func() []Persona { return agents },
		NewAgent: func(int64, func(string, string) bool) *agent.Agent {
			return &agent.Agent{Provider: rec, Model: "m", Tools: tool.NewRegistry(), MaxSteps: 2, System: "You are Talaria."}
		}}
	e.chats = map[int64]*chat{}
	return e, fake
}

func TestMentionHandsOneMessageToTheAgentAndTheUsualOnesComeBack(t *testing.T) {
	rec := &recorder{}
	e, fake := mentionEngine(rec,
		Persona{Name: "Inbox", System: "In this conversation you are Inbox.", ReadOnly: true},
		Persona{Name: "Inbox Helper", System: "In this conversation you are Inbox Helper."})
	c := e.chatFor(1)

	e.runOne(c, "@inbox sort my mail")
	if len(rec.systems) == 0 || !strings.Contains(rec.systems[len(rec.systems)-1], "you are Inbox.") {
		t.Fatalf("the agent's instructions were not used: %q", rec.systems)
	}
	if got := rec.lastUser[len(rec.lastUser)-1]; got != "sort my mail" {
		t.Fatalf("the model should see the message without the @name: %q", got)
	}
	if !strings.Contains(sentText(fake), "→ Inbox") {
		t.Fatalf("the person is told who has it: %q", sentText(fake))
	}
	if c.persona != nil || c.agent.System != "You are Talaria." || c.agent.ReadOnly {
		t.Fatalf("the usual agent should be back: persona=%v system=%q readonly=%v", c.persona, c.agent.System, c.agent.ReadOnly)
	}

	// The next message is the usual agent's.
	e.runOne(c, "and now?")
	if s := rec.systems[len(rec.systems)-1]; strings.Contains(s, "Inbox") {
		t.Fatalf("the agent stayed on: %q", s)
	}

	// The longest name wins, so "Inbox Helper" is not read as "Inbox" plus text.
	e.runOne(c, "@Inbox Helper tidy up")
	if s := rec.systems[len(rec.systems)-1]; !strings.Contains(s, "Inbox Helper") {
		t.Fatalf("%q", s)
	}
	if got := rec.lastUser[len(rec.lastUser)-1]; got != "tidy up" {
		t.Fatalf("%q", got)
	}
}

func TestMentionNeedsAKnownNameAndSomethingToDo(t *testing.T) {
	rec := &recorder{}
	e, _ := mentionEngine(rec, Persona{Name: "Inbox", System: "x"})
	for _, text := range []string{"@nobody do this", "@Inbox", "@Inbox   ", "email @Inbox about it", "@Inboxes sort", "@"} {
		if _, _, ok := e.mention(text); ok {
			t.Errorf("%q was taken as a mention", text)
		}
	}
	if p, rest, ok := e.mention("  @INBOX\nsort my mail "); !ok || p.Name != "Inbox" || rest != "sort my mail" {
		t.Fatalf("%v %q %v", p, rest, ok)
	}
	e.Agents = nil
	if _, _, ok := e.mention("@Inbox sort"); ok {
		t.Fatal("with no agents listed, nothing is a mention")
	}
}

func TestMentionIsIgnoredWhileTalkingWithAnAgent(t *testing.T) {
	rec := &recorder{}
	e, _ := mentionEngine(rec, Persona{Name: "Inbox", System: "In this conversation you are Inbox."}, Persona{Name: "Other", System: "In this conversation you are Other."})
	c := e.chatFor(1)
	e.SetPersona(1, &Persona{Name: "Other", System: "In this conversation you are Other."})
	e.runOne(c, "@Inbox sort my mail")
	if got := rec.lastUser[len(rec.lastUser)-1]; got != "@Inbox sort my mail" {
		t.Fatalf("a conversation with an agent keeps its agent: %q", got)
	}
	if c.persona == nil || c.persona.Name != "Other" {
		t.Fatalf("%v", c.persona)
	}
}
