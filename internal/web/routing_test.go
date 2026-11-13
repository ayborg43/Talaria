package web

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/config"
	"talaria/internal/gateway/chat"
	"talaria/internal/provider"
	"talaria/internal/routing"
)

func routingRig(t *testing.T) *rig {
	r := newRig(t)
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Cfg = &config.Config{Profiles: map[string]config.Profile{"main": {Model: "m1"}, "lite": {Model: "m2"}, "coder": {Model: "m3"}}}
	r.srv.Learn = &routing.Store{}
	r.srv.Router = &chat.RouterBox{}
	r.login()
	return r
}

func lastAssistant(t *testing.T, r *rig) *Message {
	t.Helper()
	msgs, _, _ := r.state()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			return msgs[i]
		}
	}
	t.Fatal("no assistant message")
	return nil
}

func TestRepliesNameTheModelThatWroteThemAndWhy(t *testing.T) {
	r := routingRig(t)
	r.hub.NoteRoute(1, "lite", "m-lite", "simple", "a short, plain message: the cheap model")
	r.hub.Send(t.Context(), 1, "hello")
	m := lastAssistant(t, r)
	if m.Profile != "lite" || m.Model != "m-lite" || m.Kind != "simple" || !strings.Contains(m.Why, "cheap") {
		t.Fatalf("%+v", m)
	}
	// streaming edits keep the name
	r.hub.Edit(t.Context(), 1, m.ID, "hello there")
	if got := lastAssistant(t, r); got.Model != "m-lite" || got.Text != "hello there" {
		t.Errorf("after an edit: %+v", got)
	}
	// when the chain moved on, it is the model that answered that is named
	r.hub.Served = func(primary string) (provider.Served, bool) {
		return provider.Served{Name: "main", Model: "m-main"}, primary == "lite"
	}
	r.hub.Send(t.Context(), 1, "again")
	if got := lastAssistant(t, r); got.Profile != "main" || got.Model != "m-main" || !strings.Contains(got.Why, "lite was not working") {
		t.Errorf("fallback: %+v", got)
	}
}

func TestAThumbIsCountedAgainstTheModelAndKind(t *testing.T) {
	r := routingRig(t)
	r.hub.NoteRoute(1, "lite", "m-lite", "simple", "x")
	r.hub.Send(t.Context(), 1, "hello")
	id := lastAssistant(t, r).ID
	if resp, _ := r.do("POST", "/api/feedback", map[string]any{"id": id, "vote": "up"}); resp.StatusCode != 204 {
		t.Fatalf("vote: %d", resp.StatusCode)
	}
	if got := lastAssistant(t, r).Vote; got != "up" {
		t.Errorf("vote shown: %q", got)
	}
	rows := r.srv.Learn.Rows()
	if len(rows) != 1 || rows[0].Kind != "simple" || rows[0].Profile != "lite" || rows[0].Up != 1 {
		t.Errorf("%+v", rows)
	}
	if resp, _ := r.do("POST", "/api/feedback", map[string]any{"id": id, "vote": "sideways"}); resp.StatusCode != 400 {
		t.Errorf("a bad vote: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/feedback", map[string]any{"id": 9999, "vote": "up"}); resp.StatusCode != 404 {
		t.Errorf("a missing reply: %d", resp.StatusCode)
	}
	r.do("POST", "/api/feedback", map[string]any{"id": id, "vote": ""}) // taking the vote back
	if got := lastAssistant(t, r).Vote; got != "" {
		t.Errorf("the vote stayed: %q", got)
	}
}

func TestCarryingOnAndCorrectingAreCountedFromTheNextMessage(t *testing.T) {
	r := routingRig(t)
	r.hub.NoteRoute(1, "lite", "m-lite", "general", "x")
	r.hub.Send(t.Context(), 1, "first answer")
	r.srv.noteReception("great, now do the second part")
	r.srv.noteReception("and one more thing") // the same reply is not counted twice
	r.hub.NoteRoute(1, "lite", "m-lite", "general", "x")
	r.hub.Send(t.Context(), 1, "second answer")
	r.srv.noteReception("No, that's wrong")
	rows := r.srv.Learn.Rows()
	if len(rows) != 1 || rows[0].Continue != 1 || rows[0].Correction != 1 {
		t.Errorf("%+v", rows)
	}
	// a reply that was voted on is not also counted for carrying on
	r.hub.NoteRoute(1, "main", "m1", "code", "x")
	r.hub.Send(t.Context(), 1, "third")
	r.do("POST", "/api/feedback", map[string]any{"id": lastAssistant(t, r).ID, "vote": "down"})
	r.srv.noteReception("ok thanks")
	for _, row := range r.srv.Learn.Rows() {
		if row.Kind == "code" && (row.Continue != 0 || row.Down != 1) {
			t.Errorf("%+v", row)
		}
	}
}

func TestRetryDropsTheLatestAnswerAndNeedsASecondModel(t *testing.T) {
	r := routingRig(t)
	if resp, _ := r.do("POST", "/api/retry", map[string]any{"id": 1}); resp.StatusCode != 400 {
		t.Errorf("with no second model: %d", resp.StatusCode)
	}
	r.srv.Engine.Backends = &chat.Backends{Default: "main", Names: []string{"coder", "lite", "main"}, Open: func(n string) (chat.Backend, error) {
		return chat.Backend{Model: "m-" + n, Reasoning: n == "coder", Provider: scripted{gate: r.gate}}, nil
	}}
	r.hub.AddUser("explain this to me")
	r.hub.NoteRoute(1, "lite", "m-lite", "simple", "x")
	r.hub.Send(t.Context(), 1, "a thin answer")
	id := lastAssistant(t, r).ID
	if resp, _ := r.do("POST", "/api/retry", map[string]any{"id": id + 50}); resp.StatusCode != 400 {
		t.Errorf("a reply that is not there: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/api/retry", map[string]any{"id": id, "profile": "ghost"}); resp.StatusCode != 400 {
		t.Errorf("a model that does not exist: %d", resp.StatusCode)
	}
	resp, body := r.do("POST", "/api/retry", map[string]any{"id": id})
	if resp.StatusCode != 200 {
		t.Fatalf("retry: %d %s", resp.StatusCode, body)
	}
	var out struct{ Profile string }
	json.Unmarshal(body, &out)
	if out.Profile != "coder" {
		t.Errorf("the stronger model chosen for it: %q", out.Profile)
	}
	rows := r.srv.Learn.Rows()
	if len(rows) != 1 || rows[0].Retry != 1 || rows[0].Profile != "lite" {
		t.Errorf("the retry was not counted against the model that was sent back: %+v", rows)
	}
	msgs, _, _ := r.state()
	for _, m := range msgs {
		if m.Text == "a thin answer" {
			t.Error("the answer it replaces is still shown")
		}
	}
}

func TestRoutingSettingsApplyAtOnceAndWhatWasLearnedCanBeForgotten(t *testing.T) {
	r := routingRig(t)
	r.srv.Learn.Record(routing.Code, "main", routing.Up)
	if resp, _ := r.do("POST", "/api/routing", map[string]any{"smart": true, "profile": "lite", "learn": false}); resp.StatusCode != 200 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	if got := r.srv.Router.Get(); !got.Smart || got.Profile != "lite" || got.Learn {
		t.Errorf("the engine's settings: %+v", got)
	}
	if !r.srv.Cfg.Routing.Smart || r.srv.Cfg.Routing.Learning() {
		t.Errorf("config: %+v", r.srv.Cfg.Routing)
	}
	_, body := r.do("GET", "/api/routing", nil)
	var v struct {
		Smart bool
		Rows  []routing.Row
	}
	json.Unmarshal(body, &v)
	if !v.Smart || len(v.Rows) != 1 {
		t.Errorf("view: %s", body)
	}
	if resp, _ := r.do("POST", "/api/routing", map[string]any{"profile": "ghost"}); resp.StatusCode != 400 {
		t.Errorf("an unknown profile: %d", resp.StatusCode)
	}
	r.do("POST", "/api/routing", map[string]any{"reset": true})
	if len(r.srv.Learn.Rows()) != 0 {
		t.Error("reset did not forget")
	}
}
