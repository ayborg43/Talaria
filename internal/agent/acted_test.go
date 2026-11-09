package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"talaria/internal/provider"
	"talaria/internal/tool"
)

// Reading does not count as acting; a call that asks for approval does, and
// it counts even when it fails, since a failure half way may have done the work.
func TestActedIsSetByCallsThatChangeOrSendSomething(t *testing.T) {
	reg := tool.NewRegistry()
	obj := json.RawMessage(`{"type":"object"}`)
	reg.Register(tool.Tool{Name: "read_thing", Schema: obj, Run: func(context.Context, json.RawMessage) (string, error) { return "read", nil }})
	reg.Register(tool.Tool{Name: "send_thing", Schema: obj, NeedsApproval: true, Run: func(context.Context, json.RawMessage) (string, error) {
		return "", errors.New("the connection dropped after the message left")
	}})
	reg.Register(tool.Tool{Name: "mcp_chat_post", Schema: obj, Egress: tool.Always, Run: func(context.Context, json.RawMessage) (string, error) { return "posted", nil }})
	reg.Register(tool.Tool{Name: "memory", Schema: obj, Egress: tool.Always, Run: func(context.Context, json.RawMessage) (string, error) { return "noted", nil }})

	a := &Agent{Tools: reg, Approve: func(string, string) bool { return true }}
	for _, name := range []string{"read_thing", "memory"} {
		a.execute(context.Background(), provider.ToolCall{Name: name, Arguments: "{}"})
		if a.Acted() {
			t.Fatalf("%s counted as acting", name)
		}
	}
	a.execute(context.Background(), provider.ToolCall{Name: "send_thing", Arguments: "{}"})
	if !a.Acted() {
		t.Fatal("a failed send did not count as acting")
	}

	b := &Agent{Tools: reg}
	if _, err := b.Call(context.Background(), "mcp_chat_post", nil); err != nil || !b.Acted() {
		t.Fatalf("a connected service's sending tool: err %v, acted %v", err, b.Acted())
	}

	denied := &Agent{Tools: reg, Approve: func(string, string) bool { return false }}
	denied.execute(context.Background(), provider.ToolCall{Name: "send_thing", Arguments: "{}"})
	if denied.Acted() {
		t.Fatal("a call the user denied never ran, so nothing was done")
	}
}

// A tool's typed record reaches the front end scrubbed, and never the model.
func TestAToolsArtifactGoesToTheFrontEndOnly(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.Tool{Name: "list", Schema: json.RawMessage(`{"type":"object"}`), RunRich: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{Text: "one item", Artifact: tool.Artifact("mail_list", map[string]any{"items": []string{"hello"}})}, nil
	}})
	a := &Agent{Tools: reg}
	res := a.execute(context.Background(), provider.ToolCall{Name: "list", Arguments: "{}"})
	var got struct {
		Kind  string
		Items []string
	}
	if err := json.Unmarshal(res.artifact, &got); err != nil || got.Kind != "mail_list" || len(got.Items) != 1 || res.text != "one item" {
		t.Fatalf("artifact %s text %q err %v", res.artifact, res.text, err)
	}
	big := &Agent{Tools: reg}
	if out := big.scrubArtifact(make(json.RawMessage, maxArtifact+1)); out != nil {
		t.Fatal("an oversized record was kept")
	}
}
