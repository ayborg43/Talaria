package agent

import (
	"context"
	"testing"

	"talaria/internal/provider"
)

func TestOnMessageSeesEveryTurn(t *testing.T) {
	p := &scripted{replies: []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "1", Name: "nope", Arguments: `{}`}}},
		{Role: provider.RoleAssistant, Content: "done"},
	}}
	a := newAgent(p)
	a.System = "sys"
	var roles []provider.Role
	a.OnMessage = func(m provider.Message) { roles = append(roles, m.Role) }

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	want := []provider.Role{provider.RoleUser, provider.RoleAssistant, provider.RoleTool, provider.RoleAssistant}
	if len(roles) != len(want) {
		t.Fatalf("got roles %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("got roles %v, want %v", roles, want)
		}
	}
}
