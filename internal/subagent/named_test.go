package subagent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"talaria/internal/provider"
)

func agentCall(agent, name string) provider.Message {
	args, _ := json.Marshal(map[string]string{"task": "look into it", "agent": agent, "name": name})
	return toolCalls(provider.ToolCall{ID: "1", Name: "delegate", Arguments: string(args)})
}

func TestATaskHandedToASavedAgentBringsItsInstructionsAndModel(t *testing.T) {
	var subReq provider.Request
	var subModel string
	cheap := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		subReq, subModel = req, "cheap-provider"
		return text("scouted"), nil
	})
	var seenDesc, seenSchema string
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		if lastTool(req) {
			return text("final: " + req.Messages[len(req.Messages)-1].Content), nil
		}
		for _, tl := range req.Tools {
			if tl.Name == "delegate" {
				seenDesc, seenSchema = tl.Description, string(tl.Parameters)
			}
		}
		return agentCall("SCOUT", ""), nil // the name is matched however it is written
	})
	pool := NewPool(Limits{})
	pool.Helpers = func() []Helper {
		return []Helper{{Name: "fast", Desc: "flash", Open: func() (provider.Provider, string, error) { return cheap, "flash-1", nil }},
			{Name: "other", Desc: "x", Open: func() (provider.Provider, string, error) { return cheap, "x-1", nil }}}
	}
	pool.Named = func() []Named {
		return []Named{{Name: "Scout", About: "finds things out", Instructions: "Always cite the file you read.", Profile: "fast", ReadOnly: true}}
	}
	parent := newParent(p, pool)
	got, err := parent.Run(context.Background(), "go")
	if err != nil || !strings.Contains(got, "scouted") || subModel != "cheap-provider" {
		t.Fatalf("%q %v %q", got, err, subModel)
	}
	if sys := subReq.Messages[0].Content; !strings.Contains(sys, "Always cite the file you read.") || !strings.Contains(sys, "You are Scout") || !strings.Contains(sys, "look only") {
		t.Errorf("its instructions did not reach the subagent:\n%s", sys)
	}
	if subReq.Model != "flash-1" {
		t.Errorf("its own model was not used: %s", subReq.Model)
	}
	if !strings.Contains(seenDesc, "Scout: finds things out (can only look things up)") || !strings.Contains(seenSchema, `"agent"`) {
		t.Errorf("the saved agent was not offered:\n%s\n%s", seenDesc, seenSchema)
	}
}

func TestAReadOnlySavedAgentCannotDoWhatNeedsApproval(t *testing.T) {
	var result string
	step := 0
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req) && lastTool(req):
			result = req.Messages[len(req.Messages)-1].Content
			return text("could not"), nil
		case isSub(req):
			return toolCalls(provider.ToolCall{ID: "w", Name: "write_file", Arguments: `{"path":"x.txt","content":"hi"}`}), nil
		case lastTool(req):
			return text("final"), nil
		}
		step++
		return agentCall("scout", ""), nil
	})
	pool := NewPool(Limits{})
	pool.Named = func() []Named { return []Named{{Name: "scout", Instructions: "look", ReadOnly: true}} }
	parent := newParent(p, pool)
	asked := false
	parent.Approve = func(string, string) bool { asked = true; return true }
	if _, err := parent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if asked || !strings.Contains(result, "read-only") {
		t.Fatalf("a look-only agent was allowed to act or even to ask: asked=%v %q", asked, result)
	}
}

func TestAnUnknownSavedAgentSaysWhatThereIs_AndNoneMeansNoChoice(t *testing.T) {
	pool := NewPool(Limits{})
	parent := newParent(chatFunc(func(context.Context, provider.Request) (provider.Message, error) { return text("x"), nil }), pool)
	if tl, _ := parent.Tools.Get("delegate"); strings.Contains(string(tl.Schema), `"agent"`) {
		t.Fatalf("an agent choice with none saved: %s", tl.Schema)
	}
	if _, err := pool.agentNamed("ghost"); err == nil || !strings.Contains(err.Error(), "none are set up") {
		t.Errorf("%v", err)
	}
	pool.Named = func() []Named { return []Named{{Name: "Alpha"}, {Name: "Beta"}} }
	if _, err := pool.agentNamed("ghost"); err == nil || !strings.Contains(err.Error(), "choose one of Alpha, Beta") {
		t.Errorf("%v", err)
	}
	if n, err := pool.agentNamed(" beta "); err != nil || n == nil || n.Name != "Beta" {
		t.Errorf("%v %v", n, err)
	}
	if n, err := pool.agentNamed(""); err != nil || n != nil {
		t.Errorf("none asked for must mean an ordinary subagent: %v %v", n, err)
	}
}

func TestASavedAgentWhoseModelIsGoneUsesTheMainAgentsOwn(t *testing.T) {
	var model string
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		if isSub(req) {
			model = req.Model
			return text("done"), nil
		}
		if lastTool(req) {
			return text("final: " + req.Messages[len(req.Messages)-1].Content), nil
		}
		return agentCall("scout", ""), nil
	})
	pool := NewPool(Limits{})
	pool.Helpers = func() []Helper { return nil }
	pool.Named = func() []Named { return []Named{{Name: "scout", Instructions: "x", Profile: "removed"}} }
	got, err := newParent(p, pool).Run(context.Background(), "go")
	if err != nil || !strings.Contains(got, "done") || model != "parent-model" {
		t.Fatalf("%q %v %q", got, err, model)
	}
}

func TestADelegateOfALookOnlyConversationIsLookOnlyToo(t *testing.T) {
	var result string
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req) && lastTool(req):
			result = req.Messages[len(req.Messages)-1].Content
			return text("could not"), nil
		case isSub(req):
			return toolCalls(provider.ToolCall{ID: "w", Name: "write_file", Arguments: `{"path":"x.txt","content":"hi"}`}), nil
		case lastTool(req):
			return text("final"), nil
		}
		return toolCalls(delegateCall("1", "write a file", "writer")), nil
	})
	parent := newParent(p, NewPool(Limits{}))
	parent.ReadOnly = true
	if _, err := parent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "read-only") {
		t.Fatalf("a look-only chat got round it by delegating: %q", result)
	}
}

func TestAScopedSavedAgentGetsTheHandoffAndNotTheClosedTools(t *testing.T) {
	var subReq provider.Request
	var result string
	p := chatFunc(func(_ context.Context, req provider.Request) (provider.Message, error) {
		switch {
		case isSub(req) && lastTool(req):
			result = req.Messages[len(req.Messages)-1].Content
			return text("could not"), nil
		case isSub(req):
			subReq = req
			return toolCalls(provider.ToolCall{ID: "s", Name: "shell", Arguments: `{"command":"ls"}`}), nil
		case lastTool(req):
			return text("final"), nil
		}
		args, _ := json.Marshal(map[string]string{"task": "look into it", "agent": "scout", "handoff": "We already ruled out the cache."})
		return toolCalls(provider.ToolCall{ID: "1", Name: "delegate", Arguments: string(args)}), nil
	})
	pool := NewPool(Limits{})
	pool.Named = func() []Named {
		return []Named{{Name: "scout", About: "finds things in files", Instructions: "look", Scopes: []string{"files"}}}
	}
	parent := newParent(p, pool)
	if _, err := parent.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if sys := subReq.Messages[0].Content; !strings.Contains(sys, "Context handed over") || !strings.Contains(sys, "ruled out the cache") || !strings.Contains(sys, "limited to these areas: files") {
		t.Errorf("the handoff or the scope did not reach the subagent:\n%s", sys)
	}
	if names := toolNames(subReq); names["shell"] || !names["read_file"] {
		t.Errorf("the closed tool was offered, or the open one was not: %v", names)
	}
	if !strings.Contains(result, "outside this agent's tool areas") {
		t.Fatalf("a call to a closed tool was not refused: %q", result)
	}
}

func TestASubagentCannotCreateAgents(t *testing.T) {
	if !slices.Contains(excluded, "agent_create") {
		t.Fatal("a helper could suggest agents, and the team could grow from inside a task")
	}
}
