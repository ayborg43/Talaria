package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const aguiTestKey = "a-long-enough-key-for-the-test-0001"

// aguiRun posts a run with the key and returns the events of its stream.
func aguiRun(t *testing.T, r *rig, body any, key string) (int, []map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", r.ts.URL+"/api/agui", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req) // no cookie jar: this is not the page
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var events []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("an event is not JSON: %q", line)
		}
		events = append(events, ev)
	}
	return resp.StatusCode, events
}

func eventKinds(events []map[string]any) string {
	var out []string
	for _, e := range events {
		out = append(out, e["type"].(string))
	}
	return strings.Join(out, " ")
}

func TestTheAGUIDoorIsShutWithoutItsKey(t *testing.T) {
	r := newRig(t)
	run := map[string]any{"threadId": "t", "runId": "r", "messages": []map[string]any{{"role": "user", "content": "hello"}}}
	// No key is set: nothing but the signed-in page gets in, and a bearer token of any kind is refused.
	if code, _ := aguiRun(t, r, run, ""); code != 401 {
		t.Fatalf("no key set, no sign-in: %d", code)
	}
	if code, _ := aguiRun(t, r, run, "anything"); code != 401 {
		t.Fatalf("no key set, a made-up token: %d", code)
	}
	r.srv.AGUIKey = aguiTestKey
	if code, _ := aguiRun(t, r, run, aguiTestKey+"x"); code != 401 {
		t.Fatalf("a wrong key: %d", code)
	}
	if code, _ := aguiRun(t, r, run, ""); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if got := r.hub.Pending(); len(got) != 0 {
		t.Fatal("a refused run reached the conversation")
	}
	if msgs, _, _, _ := r.hub.Snapshot(); len(msgs) != 0 {
		t.Fatalf("a refused run left %d messages", len(msgs))
	}
	// The same door answers questions, and is shut the same way.
	req, _ := http.NewRequest("POST", r.ts.URL+"/api/agui/approve", strings.NewReader(`{"token":"x","choice":"allow"}`))
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != 401 {
		t.Fatalf("approving without the key: %d", resp.StatusCode)
	}
}

func TestAnAGUIRunStreamsTheAnswerAsTheProtocolSaysAndShowsOnThePage(t *testing.T) {
	r := newRig(t)
	r.srv.AGUIKey = aguiTestKey
	code, events := aguiRun(t, r, map[string]any{"threadId": "thread-1", "runId": "run-1", "messages": []map[string]any{
		{"role": "assistant", "content": "an earlier answer the client remembers"},
		{"role": "user", "content": []map[string]string{{"type": "text", "text": "hello "}, {"type": "text", "text": "there"}}},
	}}, aguiTestKey)
	if code != 200 {
		t.Fatalf("%d", code)
	}
	got := eventKinds(events)
	if !strings.HasPrefix(got, "RUN_STARTED TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT") || !strings.HasSuffix(got, "TEXT_MESSAGE_END RUN_FINISHED") {
		t.Fatalf("events: %s", got)
	}
	if events[0]["threadId"] != "thread-1" || events[0]["runId"] != "run-1" || events[len(events)-1]["runId"] != "run-1" {
		t.Fatalf("the run is not named as it was posted: %v", events[0])
	}
	// The pieces add up to the answer, all under one message id, which is opened once and closed once.
	var text, id string
	for _, e := range events {
		switch e["type"] {
		case "TEXT_MESSAGE_START":
			if id != "" {
				t.Fatalf("a second message was opened: %s", got)
			}
			id = e["messageId"].(string)
			if e["role"] != "assistant" {
				t.Fatalf("role %v", e["role"])
			}
		case "TEXT_MESSAGE_CONTENT":
			if e["messageId"] != id || e["delta"] == "" {
				t.Fatalf("a piece with the wrong id or nothing in it: %v", e)
			}
			text += e["delta"].(string)
		}
	}
	if text != "echo: hello there" {
		t.Fatalf("the pieces say %q", text)
	}
	// It is the one conversation: the page has the message and the answer too.
	msgs, _, _ := func() ([]*Message, int64, int64) { m, s, e, _ := r.hub.Snapshot(); return m, s, e }()
	if len(msgs) != 2 || msgs[0].Text != "hello there" || msgs[1].Text != "echo: hello there" {
		t.Fatalf("the page shows %+v", msgs)
	}
}

func TestAnAGUIRunReportsToolCallsAndTheQuestionsTheyRaise(t *testing.T) {
	r := newRig(t)
	r.srv.AGUIKey = aguiTestKey
	done := make(chan []map[string]any, 1)
	go func() {
		_, events := aguiRun(t, r, map[string]any{"messages": []map[string]any{{"role": "user", "content": "run it"}}}, aguiTestKey)
		done <- events
	}()
	// The command needs a yes. The question is on the stream as it is on the page; answer it by the same door.
	var token string
	for deadline := time.Now().Add(10 * time.Second); token == "" && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if p := r.hub.Pending(); len(p) == 1 {
			token = p[0].Token
		}
	}
	if token == "" {
		t.Fatal("no question was raised")
	}
	req, _ := http.NewRequest("POST", r.ts.URL+"/api/agui/approve", strings.NewReader(`{"token":"`+token+`","choice":"allow"}`))
	req.Header.Set("Authorization", "Bearer "+aguiTestKey)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 204 {
		t.Fatalf("answering: %v %v", resp, err)
	}
	var events []map[string]any
	select {
	case events = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the stream never ended")
	}
	got := eventKinds(events)
	for _, want := range []string{"CUSTOM", "TOOL_CALL_START TOOL_CALL_ARGS TOOL_CALL_END", "TOOL_CALL_RESULT", "RUN_FINISHED"} {
		if !strings.Contains(got, want) {
			t.Fatalf("events lack %q: %s", want, got)
		}
	}
	var call string
	for _, e := range events {
		switch e["type"] {
		case "CUSTOM":
			v := e["value"].(map[string]any)
			if e["name"] == "talaria.approval" && v["state"] == "pending" && v["token"] != token {
				t.Fatalf("the question does not carry the token that answers it: %v", v)
			}
		case "TOOL_CALL_START":
			call = e["toolCallId"].(string)
			if e["toolCallName"] != "shell" {
				t.Fatalf("tool %v", e["toolCallName"])
			}
		case "TOOL_CALL_RESULT":
			if e["toolCallId"] != call || e["content"] != "listing" || e["role"] != "tool" {
				t.Fatalf("result %v", e)
			}
		}
	}
	if r.ran.Load() != 1 {
		t.Fatalf("the command ran %d times", r.ran.Load())
	}
}

func TestAnAGUIRunWithNothingFromTheUserIsRefused(t *testing.T) {
	r := newRig(t)
	r.srv.AGUIKey = aguiTestKey
	for _, body := range []any{map[string]any{}, map[string]any{"messages": []map[string]any{{"role": "assistant", "content": "hi"}}}, map[string]any{"messages": []map[string]any{{"role": "user", "content": "   "}}}} {
		if code, _ := aguiRun(t, r, body, aguiTestKey); code != 400 {
			t.Errorf("%v answered %d", body, code)
		}
	}
}

// A tool's typed record travels with the end of its call, to the page and onto the AG-UI stream.
func TestAToolsArtifactIsKeptWithItsResult(t *testing.T) {
	h := &Hub{}
	h.ToolStart(ChatID, "email_list")
	h.ToolArtifact(ChatID, "email_list", json.RawMessage(`{"kind":"mail_list","items":[{"id":7}]}`))
	h.ToolArtifact(ChatID, "email_list", json.RawMessage(`not json`))
	h.ToolArtifact(ChatID, "other", make(json.RawMessage, maxArtifact+1))
	h.ToolEnd(ChatID, "email_list", "Mail in INBOX")
	msgs, _, _, _ := h.Snapshot()
	if len(msgs) != 1 || string(msgs[0].Artifact) != `{"kind":"mail_list","items":[{"id":7}]}` || msgs[0].Result != "Mail in INBOX" {
		t.Fatalf("%+v", msgs[0])
	}
	// A call that failed shows its error, not a card for a result it does not have.
	h.ToolStart(ChatID, "email_list")
	h.ToolArtifact(ChatID, "email_list", json.RawMessage(`{"kind":"mail_list"}`))
	h.ToolEnd(ChatID, "email_list", "error: the mailbox did not answer")
	msgs, _, _, _ = h.Snapshot()
	if msgs[1].State != "error" || msgs[1].Artifact != nil {
		t.Fatalf("%+v", msgs[1])
	}
}
