package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestToAnthropicConversion(t *testing.T) {
	system, msgs := toAnthropic([]Message{
		{Role: RoleSystem, Content: "you are talaria"},
		{Role: RoleSystem, Content: "notes here"},
		{Role: RoleUser, Content: "list files and the time"},
		{Role: RoleAssistant, Content: "On it.", ToolCalls: []ToolCall{
			{ID: "t1", Name: "shell", Arguments: `{"command":"ls"}`},
			{ID: "t2", Name: "current_time", Arguments: `{}`},
		}},
		{Role: RoleTool, ToolCallID: "t1", Content: "a.txt"},
		{Role: RoleTool, ToolCallID: "t2", Content: ""},
		{Role: RoleAssistant}, // empty turn: must be dropped, the API rejects empty content
		{Role: RoleAssistant, Content: "done"},
	}, false)

	if len(system) != 1 || system[0].Text != "you are talaria\n\nnotes here" || system[0].CacheControl == nil || system[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("system: %+v", system)
	}

	got := mustJSON(t, msgs)
	want := `[` +
		`{"role":"user","content":[{"type":"text","text":"list files and the time"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"On it."},` +
		`{"type":"tool_use","id":"t1","name":"shell","input":{"command":"ls"}},` +
		`{"type":"tool_use","id":"t2","name":"current_time","input":{}}]},` +
		// Both results travel together in ONE user message, the empty one given a placeholder.
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.txt"},` +
		`{"type":"tool_result","tool_use_id":"t2","content":"(no output)"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"done"}]}]`
	if got != want {
		t.Fatalf("messages:\n got %s\nwant %s", got, want)
	}
}

func TestToAnthropicRepairsBadToolInput(t *testing.T) {
	_, msgs := toAnthropic([]Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t", Name: "x", Arguments: "not json"}}}}, false)
	if got := mustJSON(t, msgs); !strings.Contains(got, `"input":{}`) {
		t.Fatalf("invalid arguments must become an empty object: %s", got)
	}
}

func TestAnthropicRequestShape(t *testing.T) {
	var gotPath string
	var gotHeader http.Header
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHeader = r.URL.Path, r.Header.Clone()
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"Let me look. "},` +
			`{"type":"tool_use","id":"toolu_1","name":"shell","input":{"command":"ls"}}],"stop_reason":"tool_use"}`))
	}))
	defer srv.Close()

	msg, err := NewAnthropic(srv.URL, "sk-ant", 0).Chat(context.Background(), Request{
		Model:    "claude-test",
		Messages: []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "hi"}},
		Tools: []ToolSpec{
			{Name: "shell", Description: "run", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)},
			{Name: "noschema"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/v1/messages" || gotHeader.Get("x-api-key") != "sk-ant" || gotHeader.Get("anthropic-version") != anthropicVersion {
		t.Fatalf("path=%s headers=%v", gotPath, gotHeader)
	}
	if gotHeader.Get("Authorization") != "" {
		t.Fatal("Anthropic must not receive a Bearer token")
	}
	if gotBody["model"] != "claude-test" || gotBody["max_tokens"] != float64(defaultMaxTokens) {
		t.Fatalf("body: %v", gotBody)
	}
	if _, stream := gotBody["stream"]; stream {
		t.Fatal("plain request should not set stream")
	}
	tools := gotBody["tools"].([]any)
	if first := tools[0].(map[string]any); first["name"] != "shell" || first["input_schema"] == nil {
		t.Fatalf("tool conversion: %v", tools[0])
	}
	if second := tools[1].(map[string]any); mustJSON(t, second["input_schema"]) != `{"type":"object"}` {
		t.Fatalf("schemaless tool needs a default schema: %v", second)
	}

	if msg.Content != "Let me look. " || len(msg.ToolCalls) != 1 || msg.ToolCalls[0] != (ToolCall{ID: "toolu_1", Name: "shell", Arguments: `{"command":"ls"}`}) {
		t.Fatalf("reply: %+v", msg)
	}
}

func TestAnthropicURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.anthropic.com":       "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/":      "https://api.anthropic.com/v1/messages",
		"https://proxy.example/v1":        "https://proxy.example/v1/messages",
		"https://proxy.example/v1/":       "https://proxy.example/v1/messages",
		"https://proxy.example/anthropic": "https://proxy.example/anthropic/v1/messages",
	} {
		if got := NewAnthropic(base, "k", 0).url(); got != want {
			t.Errorf("%s -> %s, want %s", base, got, want)
		}
	}
}

func TestAnthropicStreaming(t *testing.T) {
	srv := sseServer(t,
		`{"type":"message_start","message":{"id":"m","role":"assistant","content":[]}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Looking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" now."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"shell","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"l"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"s -la\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_10","name":"current_time","input":{}}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
		`{"type":"message_stop"}`,
	)
	var pieces []string
	msg, err := NewAnthropic(srv.URL, "k", 0).Chat(context.Background(), Request{Model: "m", OnText: func(s string) { pieces = append(pieces, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pieces, "|") != "Looking| now." || msg.Content != "Looking now." {
		t.Fatalf("text: pieces=%q content=%q", pieces, msg.Content)
	}
	want := []ToolCall{
		{ID: "toolu_9", Name: "shell", Arguments: `{"command": "ls -la"}`},
		{ID: "toolu_10", Name: "current_time", Arguments: "{}"}, // no deltas at all: empty input
	}
	if len(msg.ToolCalls) != 2 || msg.ToolCalls[0] != want[0] || msg.ToolCalls[1] != want[1] {
		t.Fatalf("tool calls: %+v", msg.ToolCalls)
	}
}

func TestAnthropicStreamSetsStreamFlag(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	if _, err := NewAnthropic(srv.URL, "k", 0).Chat(context.Background(), Request{Model: "m", OnText: func(string) {}}); err != nil {
		t.Fatal(err)
	}
	if body["stream"] != true {
		t.Fatalf("stream flag missing: %v", body)
	}
}

func TestAnthropicStreamFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		events []string
		want   string
	}{
		"error event": {[]string{
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`,
			`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`}, "Overloaded"},
		"dropped": {[]string{`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`}, "before the reply was complete"},
		"cut off in a tool call": {[]string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"shell"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"l"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`,
			`{"type":"message_stop"}`}, "cut off inside a tool call"},
	} {
		_, err := NewAnthropic(sseServer(t, tc.events...).URL, "k", 0).Chat(context.Background(), Request{Model: "m", OnText: func(string) {}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", name, err, tc.want)
		}
	}
}

func TestAnthropicTokenLimitIsFlagged(t *testing.T) {
	var pieces []string
	srv := sseServer(t,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Once upon"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`,
		`{"type":"message_stop"}`)
	msg, err := NewAnthropic(srv.URL, "k", 0).Chat(context.Background(), Request{Model: "m", OnText: func(s string) { pieces = append(pieces, s) }})
	if err != nil || msg.Content != "Once upon"+cutOffNote || pieces[len(pieces)-1] != cutOffNote {
		t.Fatalf("streaming: %q %q %v", msg.Content, pieces, err)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"Once upon"}],"stop_reason":"max_tokens"}`))
	}))
	defer plain.Close()
	msg, err = NewAnthropic(plain.URL, "k", 0).Chat(context.Background(), Request{Model: "m"})
	if err != nil || msg.Content != "Once upon"+cutOffNote {
		t.Fatalf("non-streaming: %q %v", msg.Content, err)
	}
}

func TestAnthropicErrorsAndRetries(t *testing.T) {
	noBackoff(t)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"model: unknown model"}}`)
	}))
	defer bad.Close()
	if _, err := NewAnthropic(bad.URL, "k", 0).Chat(context.Background(), Request{Model: "nope"}); err == nil ||
		!strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("got %v", err)
	}

	var calls atomic.Int32
	overloaded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(529) // Anthropic's "overloaded"
			io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
			return
		}
		io.WriteString(w, `{"type":"message","content":[{"type":"text","text":"recovered"}],"stop_reason":"end_turn"}`)
	}))
	defer overloaded.Close()
	msg, err := NewAnthropic(overloaded.URL, "k", 0).Chat(context.Background(), Request{Model: "m"})
	if err != nil || msg.Content != "recovered" || calls.Load() != 2 {
		t.Fatalf("529 should be retried: %q %v calls=%d", msg.Content, err, calls.Load())
	}
}
