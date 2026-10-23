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
