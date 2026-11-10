package main

import (
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/cron"
	"talaria/internal/idea"
	"talaria/internal/web"
)

func TestStartingASuggestionRunsItAsATaskAndHidingDropsIt(t *testing.T) {
	dir := t.TempDir()
	ideas := &idea.Store{Path: filepath.Join(dir, "sessions.db")}
	jobs := &cron.Store{Path: filepath.Join(dir, "jobs.json")}
	list, start, hide := webSuggestions(ideas, jobs, func() cron.Control { return nil })
	src := []idea.Source{{Title: "Ana", URL: "mail:12"}}
	run, _ := ideas.AddTask("web", web.ChatID, "Draft a reply to Ana", "She asked.", "Read message 12 and draft a reply.", src)
	note, _ := ideas.Add("web", web.ChatID, "Compare kiln suppliers", "", []idea.Source{{Title: "x", URL: "https://example.com"}})
	gone, _ := ideas.AddTask("web", web.ChatID, "Not wanted", "", "do it", src)
	other, _ := ideas.AddTask("telegram", 5, "Someone else's", "", "do it", src)

	got := list()
	if len(got) != 3 {
		t.Fatalf("listed %d suggestions: %+v", len(got), got)
	}
	for _, g := range got {
		if g.ID == other {
			t.Fatal("another chat's suggestion was listed")
		}
		if (g.ID == note) == g.Startable {
			t.Errorf("%s: startable = %v", g.Title, g.Startable)
		}
	}

	name, err := start(run)
	if err != nil || name != "Draft a reply to Ana" {
		t.Fatalf("start: %q %v", name, err)
	}
	all, _ := jobs.List()
	if len(all) != 1 || all[0].Kind != cron.KindTask || all[0].Gateway != "web" || all[0].AllowRisky || !strings.Contains(all[0].Prompt, "message 12") {
		t.Fatalf("the task is wrong (it must not be allowed risky tools): %+v", all)
	}
	if _, err := start(run); err == nil {
		t.Fatal("a suggestion was started twice")
	}
	if _, err := start(note); err == nil {
		t.Fatal("a note with nothing to run was started")
	}
	if _, err := start("nope"); err == nil {
		t.Fatal("an unknown suggestion was started")
	}
	if err := hide(gone); err != nil {
		t.Fatal(err)
	}
	if left := list(); len(left) != 1 || left[0].ID != note {
		t.Fatalf("after start and hide: %+v", left)
	}
}

func TestASuggestionStaysWhenNoTaskSlotIsFree(t *testing.T) {
	dir := t.TempDir()
	ideas := &idea.Store{Path: filepath.Join(dir, "sessions.db")}
	jobs := &cron.Store{Path: filepath.Join(dir, "jobs.json")}
	_, start, _ := webSuggestions(ideas, jobs, func() cron.Control { return fullSlots{} })
	id, _ := ideas.AddTask("web", web.ChatID, "One more", "", "do it", []idea.Source{{Title: "s", URL: "mail:1"}})
	if _, err := start(id); err == nil {
		t.Fatal("a task was started with every slot taken")
	}
	if it, _ := ideas.Get("web", web.ChatID, id); it.Status != idea.Open {
		t.Fatalf("a failed start used the suggestion up: %s", it.Status)
	}
}

// fullSlots reports three tasks already running for the chat.
type fullSlots struct{}

func (fullSlots) Wake() {}
func (fullSlots) Runs(string, int64) []cron.RunInfo {
	return []cron.RunInfo{{ID: "a", Kind: cron.KindTask, Running: true}, {ID: "b", Kind: cron.KindTask, Running: true}, {ID: "c", Kind: cron.KindTask, Running: true}}
}
func (fullSlots) Cancel(string, int64, string) bool { return false }
