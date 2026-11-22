package persona

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/tool"
)

func proposeRig(t *testing.T) (*Store, tool.Tool) {
	store := &Store{Path: filepath.Join(t.TempDir(), "agents.json")}
	reg := tool.NewRegistry()
	RegisterTools(reg, store, func() []string { return []string{"cheap", "strong"} })
	tl, ok := reg.Get("agent_create")
	if !ok {
		t.Fatal("agent_create is not registered")
	}
	return store, tl
}

func TestTheAgentCanSuggestAnAgentButOnlyAfterTheUserApproves(t *testing.T) {
	store, tl := proposeRig(t)
	if tl.RiskOf(nil) == 0 {
		t.Fatal("it would not ask where only deleting and posting ask, which is the default")
	}
	if !tl.NeedsApproval || tl.Egress == nil {
		t.Fatalf("a new agent must always ask: needs=%v egress=%v", tl.NeedsApproval, tl.Egress != nil)
	}
	args := json.RawMessage(`{"name":"Researcher","instructions":"Look things up and cite every source.","profile":"cheap"}`)
	if d := tl.Describe(args); !strings.Contains(d, "Researcher") || !strings.Contains(d, "looks only") || !strings.Contains(d, "model cheap") || !strings.Contains(d, "cite every source") {
		t.Errorf("the prompt does not show what would be created: %q", d)
	}
	if len(store.List()) != 0 {
		t.Fatal("describing a proposal created it")
	}
	out, err := tl.Run(context.Background(), args)
	if err != nil || !strings.Contains(out, `delegate agent="Researcher"`) {
		t.Fatalf("%q %v", out, err)
	}
	got := store.List()
	if len(got) != 1 || got[0].Name != "Researcher" || got[0].Profile != "cheap" || !got[0].ReadOnly {
		t.Fatalf("%+v", got)
	}
}

func TestASuggestedAgentMayBeAllowedToActButOnlyWhenSaidSo(t *testing.T) {
	store, tl := proposeRig(t)
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"name":"Coder","instructions":"Write and fix code.","read_only":false}`)); err != nil {
		t.Fatal(err)
	}
	if a := store.List()[0]; a.ReadOnly {
		t.Errorf("an agent asked to act was made look-only: %+v", a)
	}
	if d := tl.Describe(json.RawMessage(`{"name":"Coder","instructions":"x","read_only":false}`)); !strings.Contains(d, "can act, with the usual approvals") {
		t.Errorf("the prompt hides that it can act: %q", d)
	}
}

func TestABadSuggestionIsRefusedWithAReason(t *testing.T) {
	store, tl := proposeRig(t)
	for name, in := range map[string]string{
		"no name":         `{"instructions":"x"}`,
		"no instructions": `{"name":"A"}`,
		"unknown model":   `{"name":"A","instructions":"x","profile":"ghost"}`,
		"not json":        `nope`,
	} {
		if _, err := tl.Run(context.Background(), json.RawMessage(in)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(store.List()) != 0 {
		t.Fatal("a refused suggestion was kept")
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"name":"Dup","instructions":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"name":"dup","instructions":"y"}`)); err == nil || !strings.Contains(err.Error(), "already an agent") {
		t.Errorf("a second agent with the same name: %v", err)
	}
	// There is no cap on how many agents there are: well past the old limit of eight still works.
	for i := 0; i < 12; i++ {
		if _, err := tl.Run(context.Background(), json.RawMessage(`{"name":"N`+strings.Repeat("a", i)+`","instructions":"x"}`)); err != nil {
			t.Fatalf("agent %d: %v", i, err)
		}
	}
	if len(store.List()) != 13 {
		t.Errorf("%d agents", len(store.List()))
	}
}

func TestTheAgentHasNoToolToChangeOrRemoveAgents(t *testing.T) {
	_, _ = proposeRig(t)
	reg := tool.NewRegistry()
	RegisterTools(reg, &Store{Path: filepath.Join(t.TempDir(), "a.json")}, nil)
	for _, n := range []string{"agent_remove", "agent_update", "agent_delete", "agent_edit"} {
		if _, ok := reg.Get(n); ok {
			t.Errorf("%s exists", n)
		}
	}
}
