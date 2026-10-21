package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatRoundTrip(t *testing.T) {
	var got wireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("unexpected request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"ls\"}"}}]}}]}`))
	}))
	defer srv.Close()

	msg, err := NewOpenAI(srv.URL+"/", "k").Chat(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolSpec{{Name: "shell", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "m" || len(got.Tools) != 1 || got.Tools[0].Function.Name != "shell" || got.Messages[0].Content != "hi" {
		t.Fatalf("bad request body: %+v", got)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "c1" || msg.ToolCalls[0].Arguments != `{"command":"ls"}` {
		t.Fatalf("bad parsed tool call: %+v", msg.ToolCalls)
	}
}

func TestOpenAIHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := NewOpenAI(srv.URL, "").Chat(context.Background(), Request{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}
