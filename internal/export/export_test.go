package export

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"talaria/internal/provider"
)

func convo() Source {
	return Source{ID: "c1", Title: "Weather", Updated: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Transcript: []provider.Message{
		{Role: provider.RoleSystem, Content: "NOTES ABOUT THE USER"},
		{Role: provider.RoleUser, Content: "what is the weather? my key is sk-SECRET123"},
		{Role: provider.RoleAssistant, Reasoning: "hidden thoughts", ToolCalls: []provider.ToolCall{{ID: "t1", Name: "web_fetch", Arguments: `{"url":"https://w.example/sk-SECRET123"}`}, {ID: "t2", Name: "web_search", Arguments: `{"q":"x"}`}}},
		{Role: provider.RoleTool, ToolCallID: "t1", Content: "sunny, token sk-SECRET123", Images: []provider.Image{{MIME: "image/png", Data: []byte("PNG")}}},
		{Role: provider.RoleAssistant, Content: "It is sunny."},
	}}
}

func scrub(s string) string { return strings.ReplaceAll(s, "sk-SECRET123", "[redacted]") }

func TestOneLinePerConversationInChatFormat(t *testing.T) {
	var buf bytes.Buffer
	n, err := Write(&buf, []Source{convo()}, Options{Scrub: scrub})
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("expected one line: %q", buf.String())
	}
	var l Line
	if err := json.Unmarshal(buf.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Messages) != 4 || l.Messages[0].Role != "user" || l.Messages[1].ToolCalls[0].Function.Name != "web_fetch" || l.Messages[2].ToolCallID != "t1" {
		t.Fatalf("%+v", l.Messages)
	}
	if l.Stats.Turns != 1 || l.Stats.ToolCalls != 2 || l.Stats.Unanswered != 1 {
		t.Fatalf("%+v", l.Stats)
	}
}

func TestNothingPrivateLeaks(t *testing.T) {
	var buf bytes.Buffer
	Write(&buf, []Source{convo()}, Options{Scrub: scrub})
	out := buf.String()
	for _, bad := range []string{"sk-SECRET123", "NOTES ABOUT THE USER", "hidden thoughts", "PNG"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q is in the export", bad)
		}
	}
	if !strings.Contains(out, "a picture was here") {
		t.Error("the picture should leave a note")
	}
}

func TestFilters(t *testing.T) {
	old := convo()
	old.ID, old.Updated = "old", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	archived := convo()
	archived.ID, archived.Archived = "arch", true
	empty := Source{ID: "e", Transcript: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}
	plain := Source{ID: "p", Updated: time.Now(), Transcript: []provider.Message{{Role: provider.RoleUser, Content: "hi"}, {Role: provider.RoleAssistant, Content: "hello"}}}

	count := func(o Options, srcs ...Source) int {
		var b bytes.Buffer
		n, _ := Write(&b, srcs, o)
		return n
	}
	all := []Source{convo(), old, archived, empty, plain}
	if n := count(Options{}, all...); n != 3 { // convo, old, plain (not archived, not empty)
		t.Fatalf("%d", n)
	}
	if n := count(Options{IncludeArchived: true}, all...); n != 4 {
		t.Fatalf("%d", n)
	}
	if n := count(Options{Since: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}, all...); n != 2 {
		t.Fatalf("%d", n)
	}
	if n := count(Options{MinToolCalls: 1}, all...); n != 2 {
		t.Fatalf("%d", n)
	}
}
