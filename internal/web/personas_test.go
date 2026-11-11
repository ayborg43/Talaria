package web

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/persona"
)

func personaRig(t *testing.T) *rig {
	r := newRig(t)
	r.login()
	cv, err := OpenConvos(filepath.Join(t.TempDir(), "chats.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.srv.Convos = cv
	r.srv.Agents = &persona.Store{Path: filepath.Join(t.TempDir(), "agents.json")}
	return r
}

func (r *rig) personaPost(body map[string]any) (int, map[string]any) {
	resp, raw := r.do("POST", "/api/personas", body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func TestNamedAgentsAreOffUntilSetUp(t *testing.T) {
	r := newRig(t)
	if resp, _ := r.do("GET", "/api/personas", nil); resp.StatusCode != 401 {
		t.Fatalf("%d", resp.StatusCode)
	}
	r.login()
	if resp, body := r.do("GET", "/api/personas", nil); resp.StatusCode != 200 || !strings.Contains(string(body), `"available":false`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if code, _ := r.personaPost(map[string]any{"action": "save", "name": "x", "instructions": "y"}); code != 501 {
		t.Fatalf("%d", code)
	}
}

func TestMakingAnAgentChattingWithItAndRemovingIt(t *testing.T) {
	r := personaRig(t)
	for name, body := range map[string]map[string]any{
		"no name":         {"action": "save", "instructions": "Triage my mail."},
		"no instructions": {"action": "save", "name": "Inbox"},
		"bad colour":      {"action": "save", "name": "Inbox", "instructions": "x", "color": "red; background:url(x)"},
		"unknown model":   {"action": "save", "name": "Inbox", "instructions": "x", "profile": "nonesuch"},
		"too long":        {"action": "save", "name": strings.Repeat("a", 31), "instructions": "x"},
	} {
		if code, _ := r.personaPost(body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	code, out := r.personaPost(map[string]any{"action": "save", "name": "Inbox", "icon": "📥", "color": "info", "instructions": "Triage my mail. Never reply for me.", "read_only": true})
	if code != 200 || out["saved"] != "Inbox" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := r.personaPost(map[string]any{"action": "save", "name": "inbox", "instructions": "again"}); code != 400 {
		t.Fatalf("two agents with one name: %d", code)
	}
	agents := out["agents"].([]any)
	id := agents[0].(map[string]any)["id"].(string)

	if r.srv.Engine.Persona(ChatID) != nil {
		t.Fatal("no agent is talking yet")
	}
	code, out = r.personaPost(map[string]any{"action": "chat", "id": id})
	if code != 200 || out["id"] == nil {
		t.Fatalf("%d %v", code, out)
	}
	p := r.srv.Engine.Persona(ChatID)
	if p == nil || p.Name != "Inbox" || !p.ReadOnly || !strings.Contains(p.System, "Triage my mail") || !strings.Contains(p.System, "look only") {
		t.Fatalf("%+v", p)
	}
	var st struct {
		Channel map[string]string `json:"channel"`
	}
	_, body := r.do("GET", "/api/state", nil)
	if json.Unmarshal(body, &st) != nil || st.Channel["agent"] != "Inbox" || st.Channel["read_only"] != "1" || st.Channel["icon"] != "📥" {
		t.Fatalf("the page is told who it is talking with: %s", body)
	}

	// Going back to an ordinary conversation, the agent stops talking as it.
	if resp, _ := r.do("POST", "/api/chats", map[string]any{"new": true}); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if r.srv.Engine.Persona(ChatID) != nil {
		t.Fatal("an ordinary conversation is with the usual agent")
	}
	// And opening the agent's conversation again brings it back.
	if resp, _ := r.do("POST", "/api/chats", map[string]any{"open": out["id"]}); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if p := r.srv.Engine.Persona(ChatID); p == nil || p.Name != "Inbox" {
		t.Fatalf("%+v", p)
	}
	// Editing it while it is talking changes it at once.
	if code, _ := r.personaPost(map[string]any{"action": "save", "id": id, "name": "Inbox", "instructions": "Only urgent mail.", "read_only": false}); code != 200 {
		t.Fatal(code)
	}
	if p := r.srv.Engine.Persona(ChatID); p == nil || p.ReadOnly || !strings.Contains(p.System, "Only urgent mail") {
		t.Fatalf("%+v", p)
	}
	// Removing it leaves its conversation as an ordinary one.
	if code, _ := r.personaPost(map[string]any{"action": "remove", "id": id}); code != 200 {
		t.Fatal(code)
	}
	if r.srv.Engine.Persona(ChatID) != nil || r.srv.Convos.ActiveAgent() != "" {
		t.Fatal("a removed agent no longer talks")
	}
	if code, _ := r.personaPost(map[string]any{"action": "chat", "id": id}); code != 404 {
		t.Fatalf("%d", code)
	}
}

func TestThereIsALimitToHowManyAgentsThereAre(t *testing.T) {
	r := personaRig(t)
	for i := 0; i < persona.MaxAgents; i++ {
		if code, _ := r.personaPost(map[string]any{"action": "save", "name": "Agent " + string(rune('A'+i)), "instructions": "x"}); code != 200 {
			t.Fatalf("%d: %d", i, code)
		}
	}
	if code, _ := r.personaPost(map[string]any{"action": "save", "name": "One more", "instructions": "x"}); code != 400 {
		t.Fatalf("%d", code)
	}
}
