package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"talaria/internal/provider"
	"talaria/internal/tool"
)

func shotTool(approve bool, describe func(json.RawMessage) string) tool.Tool {
	return tool.Tool{
		Name:          "shot",
		Schema:        json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`),
		NeedsApproval: approve,
		Describe:      describe,
		RunRich: func(_ context.Context, raw json.RawMessage) (tool.Result, error) {
			var a struct{ N int }
			json.Unmarshal(raw, &a)
			return tool.Result{Text: fmt.Sprintf("picture %d, key sk-secret-value-1234", a.N), Images: []provider.Image{{MIME: "image/png", Data: []byte{byte(a.N)}}}}, nil
		},
	}
}

// recorder keeps every request, so a test can look at what the model was sent.
type recorder struct{ reqs [][]provider.Message }

func (r *recorder) Chat(_ context.Context, req provider.Request) (provider.Message, error) {
	r.reqs = append(r.reqs, append([]provider.Message(nil), req.Messages...))
	last := req.Messages[len(req.Messages)-1]
	if last.Role == provider.RoleTool && strings.Contains(last.Content, "picture 4") {
		return provider.Message{Role: provider.RoleAssistant, Content: "done"}, nil
	}
	n := 0
	for _, m := range req.Messages {
		if m.Role == provider.RoleTool {
			n++
		}
	}
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: fmt.Sprint(n), Name: "shot", Arguments: fmt.Sprintf(`{"n":%d}`, n+1)}}}, nil
}

func TestToolPicturesReachTheModel(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(shotTool(false, nil))
	rec := &recorder{}
	a := &Agent{Provider: rec, Model: "m", Tools: reg, Gate: &fakeGate{secrets: []string{"sk-secret-value-1234"}}}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	last := rec.reqs[1][len(rec.reqs[1])-1]
	if last.Role != provider.RoleTool || len(last.Images) != 1 || last.Images[0].Data[0] != 1 {
		t.Fatalf("the picture should be on the tool message: %+v", last)
	}
	if strings.Contains(last.Content, "sk-secret") || !strings.Contains(last.Content, "[redacted]") {
		t.Fatalf("the text is still scrubbed: %q", last.Content)
	}
}

// Only the newest pictures are sent; the rest become a note, and the stored
// conversation keeps them all.
func TestOldPicturesAreDroppedFromWhatIsSent(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(shotTool(false, nil))
	rec := &recorder{}
	a := &Agent{Provider: rec, Model: "m", Tools: reg}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	final := rec.reqs[len(rec.reqs)-1] // after four pictures
	var withImage, noted int
	for _, m := range final {
		if len(m.Images) > 0 {
			withImage++
		}
		if strings.Contains(m.Content, oldImageNote) {
			noted++
		}
	}
	if withImage != maxImagesKept || noted != 2 {
		t.Fatalf("sent %d pictures and %d notes, want %d and 2", withImage, noted, maxImagesKept)
	}
	// The newest are the ones kept.
	var kept []byte
	for _, m := range final {
		for _, img := range m.Images {
			kept = append(kept, img.Data[0])
		}
	}
	if string(kept) != string([]byte{3, 4}) {
		t.Fatalf("kept pictures %v, want the newest two", kept)
	}
	// The conversation itself is untouched.
	stored := 0
	for _, m := range a.messages {
		stored += len(m.Images)
	}
	if stored != 4 {
		t.Fatalf("the history keeps every picture: %d", stored)
	}
}

func TestDropOldImagesLeavesTextOnlyConversationsAlone(t *testing.T) {
	a := &Agent{}
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	if got := a.dropOldImages(msgs); &got[0] != &msgs[0] {
		t.Fatal("nothing to change, so nothing is copied")
	}
}

// The user is asked about what the call will do, not about "n: 1".
func TestDescribeIsWhatTheUserIsAsked(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(shotTool(true, func(raw json.RawMessage) string { return "take picture number " + string(raw) }))
	rec := &recorder{}
	var asked []string
	a := &Agent{Provider: rec, Model: "m", Tools: reg, Approve: func(name, args string) bool { asked = append(asked, name+": "+args); return false }}
	a.Run(context.Background(), "go")
	if len(asked) == 0 || asked[0] != `shot: take picture number {"n":1}` {
		t.Fatalf("%v", asked)
	}

	// An empty description falls back to the arguments.
	reg2 := tool.NewRegistry()
	reg2.Register(shotTool(true, func(json.RawMessage) string { return "" }))
	asked = nil
	b := &Agent{Provider: &recorder{}, Model: "m", Tools: reg2, Approve: func(name, args string) bool { asked = append(asked, args); return false }}
	b.Run(context.Background(), "go")
	if len(asked) == 0 || asked[0] != `{"n":1}` {
		t.Fatalf("%v", asked)
	}
}

func TestExecuteUsesRunWhenThereIsNoRunRich(t *testing.T) {
	tl := tool.Tool{Name: "t", Run: func(context.Context, json.RawMessage) (string, error) { return "plain", nil }}
	if r, err := tl.Execute(context.Background(), nil); err != nil || r.Text != "plain" || len(r.Images) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestLogArgsReplaceWhatTheLogKeeps(t *testing.T) {
	reg := tool.NewRegistry()
	tl := shotTool(false, nil)
	tl.LogArgs = func(json.RawMessage) string { return "(left out)" }
	reg.Register(tl)
	g := &fakeGate{}
	a := &Agent{Provider: &recorder{}, Model: "m", Tools: reg, Gate: g}
	a.Run(context.Background(), "go")
	if len(g.entries) == 0 || g.entries[0].Args != "(left out)" {
		t.Fatalf("%+v", g.entries)
	}
}
