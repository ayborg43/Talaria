package web

import (
	"encoding/json"
	"path/filepath"
	"testing"

	phonelink "talaria/internal/phone"
	"talaria/internal/trigger"
)

type quiet struct{}

func (quiet) Send([]byte) error { return nil }
func (quiet) Close()            {}

func TestThePersonCanSeeAndRemoveWhatThePhoneWatchesFor(t *testing.T) {
	r := newRig(t)
	hub := phonelink.NewHub()
	l := hub.Attach(quiet{})
	l.Receive([]byte(`{"t":"hello","name":"P","caps":["triggers"]}`))
	r.srv.Phone = hub
	r.srv.Triggers = &trigger.Store{Path: filepath.Join(t.TempDir(), "t.json")}
	tr, _ := r.srv.Triggers.Add(trigger.Trigger{Kind: trigger.WiFi, Match: "Home", Prompt: "turn on the heating"})
	r.login()
	_, body := r.do("GET", "/api/triggers", nil)
	var v triggersView
	json.Unmarshal(body, &v)
	if len(v.Triggers) != 1 || v.Triggers[0].Describe != `When the phone joins the Wi-Fi network "Home"` || !v.Allowed || !v.Linked {
		t.Fatalf("%s", body)
	}
	if resp, _ := r.do("POST", "/api/triggers", map[string]any{"delete": "nope"}); resp.StatusCode != 404 {
		t.Errorf("a missing one: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/triggers", map[string]any{"delete": tr.ID}); resp.StatusCode != 200 || len(r.srv.Triggers.List()) != 0 {
		t.Errorf("remove: %d", resp.StatusCode)
	}
}

func TestSendingTheSameMessageTwiceIsOneMessage(t *testing.T) {
	r := newRig(t)
	r.login()
	for range 3 {
		if resp, _ := r.do("POST", "/api/send", map[string]any{"text": "buy milk", "cid": "abc-1"}); resp.StatusCode != 204 {
			t.Fatalf("send: %d", resp.StatusCode)
		}
	}
	r.do("POST", "/api/send", map[string]any{"text": "buy milk", "cid": "abc-2"}) // a different message, even with the same words
	r.do("POST", "/api/send", map[string]any{"text": "and eggs"})                 // one with no id is always taken
	r.do("POST", "/api/send", map[string]any{"text": "and eggs"})
	msgs, _, _ := r.state()
	users := 0
	for _, m := range msgs {
		if m.Role == "user" {
			users++
		}
	}
	if users != 4 {
		t.Errorf("%d user messages, want 4 (abc-1 once, abc-2, and two with no id)", users)
	}
}
