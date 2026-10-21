package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer replies to every request with the given events as an event stream.
func sseServer(t *testing.T, events ...string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			io.WriteString(w, "data: "+e+"\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func stream(t *testing.T, srv *httptest.Server) (Message, []string, error) {
	var pieces []string
	msg, err := NewOpenAI(srv.URL, "k").Chat(context.Background(), Request{
		Model:  "m",
		OnText: func(s string) { pieces = append(pieces, s) },
	})
	return msg, pieces, err
}

func TestOpenAIStreamsTextAndToolCallFragments(t *testing.T) {
	srv := sseServer(t,
		`{"choices":[{"delta":{"role":"assistant","content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"shell","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"comm"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]}}]}`,
		// A second call with no id and no arguments, as some local servers send.
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"current_time"}}]}}]}`,
		`{"choices":[],"usage":{"total_tokens":9}}`, // usage-only chunk
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	)
	msg, pieces, err := stream(t, srv)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pieces, "|") != "Hel|lo" || msg.Content != "Hello" {
		t.Fatalf("text: pieces=%q content=%q", pieces, msg.Content)
	}
	want := []ToolCall{
		{ID: "c1", Name: "shell", Arguments: `{"command":"ls"}`},
		{ID: "call_1", Name: "current_time", Arguments: "{}"},
	}
	if len(msg.ToolCalls) != 2 || msg.ToolCalls[0] != want[0] || msg.ToolCalls[1] != want[1] {
		t.Fatalf("tool calls: %+v", msg.ToolCalls)
	}
}

func TestOpenAIRequestsStreamingOnlyWhenAsked(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(srv.URL, "k")
	p.Chat(context.Background(), Request{Model: "m"})
	p.Chat(context.Background(), Request{Model: "m", OnText: func(string) {}})
	if _, has := bodies[0]["stream"]; has {
		t.Fatalf("plain request should not mention stream: %v", bodies[0])
	}
	if bodies[1]["stream"] != true {
		t.Fatalf("streaming request must set stream=true: %v", bodies[1])
	}
}

// A server that ignores stream=true still works: the whole reply is delivered at once.
func TestOpenAIStreamFallsBackToPlainJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"whole reply"}}]}`))
	}))
	defer srv.Close()

	msg, pieces, err := stream(t, srv)
	if err != nil || msg.Content != "whole reply" || len(pieces) != 1 || pieces[0] != "whole reply" {
		t.Fatalf("msg=%q pieces=%q err=%v", msg.Content, pieces, err)
	}
}

func TestOpenAIStreamFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		events []string
		want   string
	}{
		"dropped before done": {[]string{`{"choices":[{"delta":{"content":"partial"}}]}`}, "before the reply was complete"},
		"error chunk":         {[]string{`{"choices":[{"delta":{"content":"a"}}]}`, `{"error":{"message":"context length exceeded"}}`}, "context length exceeded"},
		"garbled chunk":       {[]string{`{not json`}, "decode stream"},
		"cut inside tool call": {[]string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"shell","arguments":"{\"command\":\"l"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"length"}]}`, `[DONE]`}, "cut off inside a tool call"},
	} {
		_, _, err := stream(t, sseServer(t, tc.events...))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", name, err, tc.want)
		}
	}
}

func TestOpenAITokenLimitIsFlagged(t *testing.T) {
	srv := sseServer(t,
		`{"choices":[{"delta":{"content":"Once upon"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"length"}]}`, `[DONE]`)
	msg, pieces, err := stream(t, srv)
	if err != nil || !strings.HasSuffix(msg.Content, cutOffNote) || pieces[len(pieces)-1] != cutOffNote {
		t.Fatalf("msg=%q pieces=%q err=%v", msg.Content, pieces, err)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Once upon"},"finish_reason":"length"}]}`))
	}))
	defer plain.Close()
	msg, err = NewOpenAI(plain.URL, "k").Chat(context.Background(), Request{Model: "m"})
	if err != nil || msg.Content != "Once upon"+cutOffNote {
		t.Fatalf("non-streaming: %q %v", msg.Content, err)
	}
}

func TestOpenAIFillsMissingToolCallFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"current_time","arguments":""}}]}}]}`))
	}))
	defer srv.Close()
	msg, err := NewOpenAI(srv.URL, "k").Chat(context.Background(), Request{Model: "m"})
	if err != nil || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_0" || msg.ToolCalls[0].Arguments != "{}" {
		t.Fatalf("%+v %v", msg.ToolCalls, err)
	}
}

func TestNewFactory(t *testing.T) {
	if p, err := New(Spec{}); err != nil || p.(*OpenAI).BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("default should be OpenAI at its hosted URL: %+v %v", p, err)
	}
	p, err := New(Spec{Type: TypeAnthropic, APIKey: "k", MaxTokens: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if a := p.(*Anthropic); a.BaseURL != "https://api.anthropic.com" || a.MaxTokens != 1000 {
		t.Fatalf("%+v", a)
	}
	if p, _ := New(Spec{Type: TypeOpenAI, BaseURL: "http://localhost:11434/v1/"}); p.(*OpenAI).BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("trailing slash should be trimmed: %+v", p)
	}
	if _, err := New(Spec{Type: "gemini"}); err == nil || !strings.Contains(err.Error(), "unknown provider type") {
		t.Fatalf("got %v", err)
	}
}
