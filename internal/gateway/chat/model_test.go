package chat

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A profile switch from Settings says nothing in the chat, takes effect on
// the next message, and is remembered across restarts.
func TestSwitchingProfilesFromSettingsIsQuietAndPersists(t *testing.T) {
	open := func(name string) (Backend, error) {
		switch name {
		case "a":
			return Backend{Model: "model-a", Desc: "openai, model-a"}, nil
		case "b":
			return Backend{Model: "model-b", Desc: "anthropic, model-b"}, nil
		}
		return Backend{}, errors.New("no profile named " + name)
	}
	prefs := &FilePrefs{Path: filepath.Join(t.TempDir(), "chats.json")}
	fake := newFakeService(4000, false)
	e := &Engine{T: fake, Allowed: map[int64]bool{1: true}, Backends: &Backends{Default: "a", Names: []string{"a", "b"}, Open: open}, Prefs: prefs}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)

	if got := e.ActiveProfile(7); got != "a" {
		t.Fatalf("a chat starts on the default: %q", got)
	}
	if err := e.SetProfile(7, "b"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := e.ActiveProfile(7); got != "b" {
		t.Fatalf("after switching: %q", got)
	}
	if fake.sends != 0 {
		t.Fatalf("the switch said %d things in the chat", fake.sends)
	}
	// Back to the default is stored as "follow the default".
	if err := e.SetProfile(7, "a"); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if got := prefs.Profile(7); got != "" {
		t.Fatalf("the default is stored as %q, want \"\"", got)
	}
	if err := e.SetProfile(7, "b"); err != nil {
		t.Fatalf("switch again: %v", err)
	}
	// A restart keeps the choice: a new engine over the same prefs file.
	e2 := &Engine{T: fake, Allowed: map[int64]bool{1: true}, Backends: e.Backends, Prefs: prefs}
	e2.Start(ctx)
	if got := e2.ActiveProfile(7); got != "b" {
		t.Fatalf("after a restart: %q", got)
	}
	if err := e2.SetProfile(7, "nope"); err == nil || !strings.Contains(err.Error(), "no profile named") {
		t.Fatalf("an unknown profile: %v", err)
	}
	plain := &Engine{}
	if err := plain.SetProfile(7, "a"); err == nil {
		t.Fatal("switching with one model should fail")
	}
	if got := plain.ActiveProfile(7); got != "" {
		t.Fatalf("active with one model: %q", got)
	}
}

// The router sends code to the coder, hard questions to the thinker, and the
// plain rest to the cheap seat, tuning effort where thinking is supported. An
// explicit choice always wins, and one backend means no routing at all.
func TestTheRouterReadsTheMessage(t *testing.T) {
	open := func(name string) (Backend, error) {
		switch name {
		case "main":
			return Backend{Model: "m-main"}, nil
		case "coder":
			return Backend{Model: "m-code", Reasoning: true, Best: []string{"code"}}, nil
		case "lite":
			return Backend{Model: "m-lite", Best: []string{"cheap"}}, nil
		}
		return Backend{}, errors.New("no profile named " + name)
	}
	names := []string{"coder", "lite", "main"}
	e := &Engine{Backends: &Backends{Default: "main", Names: names, Open: open}}
	for _, c := range []struct {
		text, pinned, name, effort string
	}{
		{"hi", "", "lite", ""},
		{"what time is it", "", "lite", ""},
		{"please summarize this long document carefully, covering every section in detail for me", "", "", ""},
		{"prove the theorem", "", "coder", "high"},
		{"fix this func(x)", "", "coder", "high"},
		{"```go\nfunc main() {}\n```", "", "coder", "high"},
		{"hi", "lite", "", ""},                    // pinned: no switch...
		{"fix this func(x)", "lite", "", ""},      // ...even for code, when it cannot think
		{"fix this func(x)", "coder", "", "high"}, // ...but effort is still tuned
		{"prove the theorem", "main", "", ""},     // ...even for hard questions, without a thinking model
	} {
		if name, effort := e.route(c.text, c.pinned); name != c.name || effort != c.effort {
			t.Errorf("route(%q, %q) = (%q, %q), want (%q, %q)", c.text, c.pinned, name, effort, c.name, c.effort)
		}
	}
	solo := &Engine{Backends: &Backends{Default: "main", Names: []string{"main"}, Open: open}}
	if name, effort := solo.route("fix this func(x)", ""); name != "" || effort != "" {
		t.Errorf("one backend means no routing: (%q, %q)", name, effort)
	}
	broken := &Engine{Backends: &Backends{Default: "main", Names: []string{"gone", "main"}, Open: open}}
	if name, effort := broken.route("fix this func(x)", ""); name != "" || effort != "" {
		t.Errorf("an unusable profile is skipped, leaving one backend: (%q, %q)", name, effort)
	}
}
