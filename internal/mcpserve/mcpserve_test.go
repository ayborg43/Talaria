package mcpserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func exchange(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		got = append(got, m)
	}
	return got
}

func server() *Server {
	return &Server{Name: "talaria", Version: "test", Instructions: "use me", Tools: []Tool{
		{Name: "echo", Description: "says it back", ReadOnly: true, Schema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
			Run: func(_ context.Context, a json.RawMessage) (string, error) {
				var x struct{ Text string }
				json.Unmarshal(a, &x)
				return "you said " + x.Text, nil
			}},
		{Name: "fails", Run: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("it broke") }},
		{Name: "panics", Run: func(context.Context, json.RawMessage) (string, error) { panic("boom") }},
	}}
}

func TestHandshakeListAndCall(t *testing.T) {
	got := exchange(t, server(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	if len(got) != 4 { // the notification gets no answer
		t.Fatalf("%d answers: %v", len(got), got)
	}
	init := got[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" || init["instructions"] != "use me" {
		t.Fatalf("%v", init)
	}
	tools := got[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 || tools[0].(map[string]any)["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Fatalf("%v", tools)
	}
	text := got[2]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]
	if text != "you said hi" {
		t.Fatalf("%v", text)
	}
}

func TestUnknownVersionGetsTheNewestAndErrorsAreResults(t *testing.T) {
	got := exchange(t, server(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fails"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"panics"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`,
		`not json`)
	if got[0]["result"].(map[string]any)["protocolVersion"] != Versions[0] {
		t.Fatalf("%v", got[0])
	}
	for i := 1; i <= 2; i++ {
		r := got[i]["result"].(map[string]any)
		if r["isError"] != true {
			t.Fatalf("tool failure %d should be an error result: %v", i, got[i])
		}
	}
	if got[3]["error"] == nil || got[4]["error"] == nil || got[5]["error"] == nil {
		t.Fatalf("protocol errors expected: %v", got[3:])
	}
	if msg := got[2]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string); strings.Contains(msg, "boom") {
		t.Fatal("a panic's detail reached the client")
	}
}
