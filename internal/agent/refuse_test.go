package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"talaria/internal/provider"
	"talaria/internal/tool"
)

func TestRefuseRiskyStopsADeletingCallEvenWhenTheApproverSaysYes(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		var ran bool
		p := callThenAnswer("click_thing", `{"risky":true}`)
		a := newAgent(p)
		a.Tools.Register(tool.Tool{Name: "click_thing", Description: "x", Schema: json.RawMessage(`{"type":"object"}`), NeedsApproval: true,
			RiskFor: func(raw json.RawMessage) tool.Risk {
				if strings.Contains(string(raw), "true") {
					return tool.RiskDeletes
				}
				return 0
			},
			Run: func(context.Context, json.RawMessage) (string, error) { ran = true; return "done", nil }})
		a.Approve = func(string, string) bool { return true }
		a.RefuseRisky = refuse
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		if ran == refuse {
			t.Fatalf("refuse=%v ran=%v", refuse, ran)
		}
		if refuse && !strings.Contains(told(p), "may not do") {
			t.Fatalf("the model is told why: %q", told(p))
		}
	}
}

func TestAReadOnlyAgentCannotUseAToolThatNeedsApprovalEvenIfARuleAllowsIt(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		var ran bool
		p := callThenAnswer("shout", `{}`)
		a := newAgent(p)
		a.Tools.Register(tool.Tool{Name: "shout", Description: "x", Schema: json.RawMessage(`{"type":"object"}`), NeedsApproval: true,
			Run: func(context.Context, json.RawMessage) (string, error) { ran = true; return "done", nil }})
		a.Gate = &fakeGate{rulings: map[string]Verdict{"shout": {Action: Allow, Via: "rule", Rule: "shout = allow"}}} // a rule that says yes
		a.ReadOnly = readOnly
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		if ran == readOnly {
			t.Fatalf("readOnly=%v ran=%v", readOnly, ran)
		}
		if readOnly && !strings.Contains(told(p), "read-only") {
			t.Fatalf("%q", told(p))
		}
	}
}

func TestSetSystemChangesThePromptWithoutLosingTheConversation(t *testing.T) {
	a := newAgent(&scripted{})
	a.System = "one"
	a.Restore([]provider.Message{{Role: provider.RoleUser, Content: "hi"}})
	a.SetSystem("two")
	if len(a.messages) != 2 || a.messages[0].Content != "two" || a.messages[1].Content != "hi" {
		t.Fatalf("%+v", a.messages)
	}
	a.SetSystem("")
	if len(a.messages) != 1 || a.messages[0].Content != "hi" {
		t.Fatalf("%+v", a.messages)
	}
	a.SetSystem("three")
	if len(a.messages) != 2 || a.messages[0].Role != provider.RoleSystem || a.messages[0].Content != "three" {
		t.Fatalf("%+v", a.messages)
	}
}
