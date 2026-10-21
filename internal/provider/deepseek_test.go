package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDeepSeek holds DeepSeek to its rule: once tools are offered, every
// assistant message the model wrote with reasoning must come back with it.
type fakeDeepSeek struct {
	mu       sync.Mutex
	requests []map[string]any
	// replies are given in order; each is a JSON message object.
	replies []string
	stream  bool
}

func (f *fakeDeepSeek) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(raw, &req)
		f.mu.Lock()
		n := len(f.requests)
		f.requests = append(f.requests, req)
		f.mu.Unlock()

		if _, tools := req["tools"]; tools {
			for _, m := range req["messages"].([]any) {
				mm := m.(map[string]any)
				if mm["role"] == "assistant" && mm["reasoning_content"] == nil {
					w.WriteHeader(400)
					io.WriteString(w, `{"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API."}}`)
					return
				}
			}
		}
		msg := f.replies[min(n, len(f.replies)-1)]
		if f.stream {
			w.Header().Set("Content-Type", "text/event-stream")
			var m map[string]any
			json.Unmarshal([]byte(msg), &m)
			if r, _ := m["reasoning_content"].(string); r != "" {
				for _, piece := range []string{r[:len(r)/2], r[len(r)/2:]} {
					b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": piece}}}})
					io.WriteString(w, "data: "+string(b)+"\n\n")
				}
			}
			io.WriteString(w, ": keep-alive\n\n")
			if c, _ := m["content"].(string); c != "" {
				b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": c}}}})
				io.WriteString(w, "data: "+string(b)+"\n\n")
			}
			if tcs, ok := m["tool_calls"].([]any); ok {
				for i, tc := range tcs {
					tcm := tc.(map[string]any)
					tcm["index"] = i
					b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{tcm}}}}})
					io.WriteString(w, "data: "+string(b)+"\n\n")
				}
			}
			io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		io.WriteString(w, `{"choices":[{"message":`+msg+`}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const toolReply = `{"role":"assistant","content":"","reasoning_content":"I should list the files first.","tool_calls":[{"id":"c1","type":"function","function":{"name":"list_dir","arguments":"{}"}}]}`
const finalReply = `{"role":"assistant","content":"Two files.","reasoning_content":"The list is short."}`

func deepseek(t *testing.T, f *fakeDeepSeek) Provider {
	p, err := New(Spec{Type: TypeDeepSeek, BaseURL: f.server(t).URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var tools = []ToolSpec{{Name: "list_dir", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}

func TestDeepSeekGivesReasoningBackWithToolCalls(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		f := &fakeDeepSeek{replies: []string{toolReply, finalReply}, stream: streaming}
		p := deepseek(t, f)
		var shown []string
		var onText func(string)
		if streaming {
			onText = func(s string) { shown = append(shown, s) }
		}
		msgs := []Message{{Role: RoleUser, Content: "what is here?"}}

		first, err := p.Chat(context.Background(), Request{Model: "deepseek-flash", Messages: msgs, Tools: tools, OnText: onText})
		if err != nil || first.Reasoning != "I should list the files first." || len(first.ToolCalls) != 1 {
			t.Fatalf("stream=%v: %+v %v", streaming, first, err)
		}
		msgs = append(msgs, first, Message{Role: RoleTool, ToolCallID: "c1", Content: "a.txt b.txt"})
		second, err := p.Chat(context.Background(), Request{Model: "deepseek-flash", Messages: msgs, Tools: tools, OnText: onText})
		if err != nil {
			t.Fatalf("stream=%v: the follow-up was refused: %v", streaming, err)
		}
		if second.Content != "Two files." || second.Reasoning != "The list is short." {
			t.Fatalf("%+v", second)
		}
		sent := f.requests[1]["messages"].([]any)
		if got := sent[1].(map[string]any)["reasoning_content"]; got != "I should list the files first." {
			t.Fatalf("stream=%v: %v", streaming, got)
		}
		if streaming && strings.Contains(strings.Join(shown, ""), "list the files first") {
			t.Fatal("the model's thinking is not shown to the user")
		}
		if streaming && strings.Join(shown, "") != "Two files." {
			t.Fatalf("%q", shown)
		}
	}
}

// What a plain openai profile does: the same conversation is refused by DeepSeek,
// and other servers are never sent a field they may not know.
func TestOtherOpenAIServersAreNotSentReasoning(t *testing.T) {
	f := &fakeDeepSeek{replies: []string{finalReply}}
	srv := f.server(t)
	o := NewOpenAI(srv.URL, "k") // PassReasoning off
	msgs := []Message{{Role: RoleUser, Content: "hi"}, {Role: RoleAssistant, Content: "x", Reasoning: "secret thoughts"}, {Role: RoleUser, Content: "again"}}
	if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: msgs}); err != nil {
		t.Fatal(err)
	}
	if _, found := f.requests[0]["messages"].([]any)[1].(map[string]any)["reasoning_content"]; found {
		t.Fatal("sent to a server that did not ask for it")
	}
	if _, err := o.Chat(context.Background(), Request{Model: "m", Messages: msgs, Tools: tools}); err == nil || !strings.Contains(err.Error(), "reasoning_content") {
		t.Fatalf("the fake holds the rule, so a plain profile fails with tools: %v", err)
	}
}

func TestReasoningIsOnlyGivenBackOnAssistantMessages(t *testing.T) {
	o := NewOpenAI("http://x", "k")
	o.PassReasoning = true
	out := o.toWire([]Message{{Role: RoleUser, Content: "u", Reasoning: "r"}, {Role: RoleTool, Content: "t", ToolCallID: "1", Reasoning: "r"}, {Role: RoleAssistant, Content: "a", Reasoning: "r"}, {Role: RoleAssistant, Content: "b"}})
	for i, want := range []string{"", "", "r", ""} {
		if out[i].ReasoningContent != want {
			t.Errorf("message %d: %q, want %q", i, out[i].ReasoningContent, want)
		}
	}
}

func TestDeepSeekRequestSettings(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	ask := func(s Spec) map[string]any {
		s.Type, s.BaseURL, s.APIKey = TypeDeepSeek, srv.URL, "k"
		p, err := New(s)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Chat(context.Background(), Request{Model: "deepseek-flash", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if g := ask(Spec{}); g["thinking"] != nil || g["reasoning_effort"] != nil {
		t.Fatalf("by default the service decides: %v", g)
	}
	if g := ask(Spec{Thinking: "enabled", ReasoningEffort: "max"}); g["thinking"].(map[string]any)["type"] != "enabled" || g["reasoning_effort"] != "max" {
		t.Fatalf("%v", g)
	}
	if g := ask(Spec{Thinking: "disabled", ReasoningEffort: "max"}); g["thinking"].(map[string]any)["type"] != "disabled" || g["reasoning_effort"] != nil {
		t.Fatalf("an effort is not sent when thinking is off: %v", g)
	}
	// The plain openai type never sends them.
	p, _ := New(Spec{Type: TypeOpenAI, BaseURL: srv.URL, Thinking: "enabled", ReasoningEffort: "high"})
	p.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if got["thinking"] != nil || got["reasoning_effort"] != nil {
		t.Fatalf("%v", got)
	}
}

func TestDeepSeekDefaults(t *testing.T) {
	if DefaultBaseURL(TypeDeepSeek) != "https://api.deepseek.com" {
		t.Fatal(DefaultBaseURL(TypeDeepSeek))
	}
	p, err := New(Spec{Type: TypeDeepSeek, APIKey: "k"})
	o, ok := p.(*OpenAI)
	if err != nil || !ok || o.BaseURL != "https://api.deepseek.com" || !o.PassReasoning {
		t.Fatalf("%#v %v", p, err)
	}
	if _, err := New(Spec{Type: "gemini"}); err == nil || !strings.Contains(err.Error(), TypeDeepSeek) {
		t.Fatalf("the list of types names deepseek: %v", err)
	}
}
