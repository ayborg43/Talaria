package memory

import (
	"strings"
	"testing"
	"time"
)

// A note keeps the day it was learned and the conversation that learned it, so
// a newer note can visibly outrank an older one.
func TestNotesCarryTheirDayAndTheirSource(t *testing.T) {
	n := &Notes{Dir: t.TempDir()}
	if err := n.Add(User, "lives in Lagos", "20261127-150405-ab12cd"); err != nil {
		t.Fatal(err)
	}
	if err := n.Add(User, "writes Go at work", ""); err != nil {
		t.Fatal(err)
	}
	entries, _, _, err := n.Entries(User)
	if err != nil || len(entries) != 2 {
		t.Fatalf("%v %v", entries, err)
	}
	if entries[0].Date != today() || entries[0].Source != "20261127-150405-ab12cd" {
		t.Fatalf("the date or source was lost: %+v", entries[0])
	}
	if entries[1].Source != "" || entries[1].Date != today() {
		t.Fatalf("a note with no source should still be dated: %+v", entries[1])
	}
	prompt := (&Notes{Dir: n.Dir}).Prompt() // a fresh instance proves it hit disk
	if !strings.Contains(prompt, "- lives in Lagos (learned today)") || !strings.Contains(prompt, "- writes Go at work (learned today)") {
		t.Fatalf("prompt = %q", prompt)
	}
}

// The age is what tells the model which of two contradicting notes is newer.
func TestTheAgeOfANoteReadsAsADate(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"bogus":      "",
		today():      "today",
		ago(1):       "yesterday",
		ago(5):       "5 days ago",
		ago(21):      "3 weeks ago",
		ago(90):      "3 months ago",
		ago(3 * 365): "3 years ago",
	}
	for date, want := range cases {
		if got := (Entry{Date: date}).Age(); got != want {
			t.Errorf("%q is %q, want %q", date, got, want)
		}
	}
}

func ago(days int) string { return time.Now().AddDate(0, 0, -days).Format("2006-01-02") }

// A later conversation that contradicts an earlier one replaces it, and the
// trail keeps what the older note said so the person can put it back.
func TestSupersedingANoteCanBeUndone(t *testing.T) {
	log := OpenLog(t.TempDir() + "/changes.jsonl")
	n := &Notes{Dir: t.TempDir(), Log: log}
	if err := n.Add(User, "lives in Lagos", "chat-1"); err != nil {
		t.Fatal(err)
	}
	if err := n.Supersede(User, "Lagos", "lives in Abuja", "chat-2"); err != nil {
		t.Fatal(err)
	}
	entries, _, _, _ := n.Entries(User)
	if len(entries) != 1 || entries[0].Text != "lives in Abuja" || strings.Contains(n.Prompt(), "Lagos") {
		t.Fatalf("after superseding: %+v", entries)
	}
	changes := n.Changes(10)
	if len(changes) != 2 || changes[0].Action != "supersede" || changes[0].Before != "lives in Lagos" || changes[0].ID == "" {
		t.Fatalf("the trail reads %+v", changes)
	}
	if err := n.Undo(changes[0].ID); err != nil {
		t.Fatal(err)
	}
	entries, _, _, _ = n.Entries(User)
	if len(entries) != 1 || entries[0].Text != "lives in Lagos" {
		t.Fatalf("after undoing: %+v", entries)
	}
	// The same undo a second time has nothing to land on, and says so.
	if err := n.Undo(changes[0].ID); err == nil {
		t.Fatal("an undo applied twice")
	}
}

// A pass counts a replacement as one change, not a removal and an addition.
func TestDiffCountsAddsChangesAndRemovals(t *testing.T) {
	before := map[Target][]Entry{User: {{Text: "lives in Lagos"}, {Text: "likes tea"}}, Agent: nil}
	after := map[Target][]Entry{User: {{Text: "lives in Abuja"}, {Text: "likes tea"}, {Text: "works nights"}}, Agent: nil}
	added, changed, removed := Diff(before, after)
	if added != 1 || changed != 1 || removed != 0 {
		t.Fatalf("added %d changed %d removed %d", added, changed, removed)
	}
}

// The review stamp is what stops a pass repeating itself over the same history.
func TestTheReviewStampSurvivesAndCanBeForcedOn(t *testing.T) {
	n := &Notes{Dir: t.TempDir()}
	if !n.Reviewed().IsZero() {
		t.Fatal("a fresh home has already been reviewed")
	}
	at := time.Now().Add(-time.Hour)
	if err := n.MarkReviewed(at); err != nil {
		t.Fatal(err)
	}
	if got := (&Notes{Dir: n.Dir}).Reviewed(); got.Sub(at).Abs() > time.Second {
		t.Fatalf("stamp = %v, want %v", got, at)
	}
}

// The prompt the reviewer is given names every conversation it may read, and
// forbids inventing: everything it writes must come from them.
func TestTheReviewPromptCarriesTheConversations(t *testing.T) {
	got := ReviewPrompt([]SessionText{{ID: "chat-1", Started: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), Text: "user: I moved to Abuja\nassistant: noted"}})
	for _, want := range []string{"chat-1", "2026-11-02 09:00", "I moved to Abuja"} {
		if !strings.Contains(got, want) {
			t.Fatalf("the prompt does not mention %q:\n%s", want, got)
		}
	}
	if sys := ReviewSystem("notes here"); !strings.Contains(sys, "Never guess") || !strings.Contains(sys, "notes here") {
		t.Fatalf("the reviewer's rules are not strict, or the notes are missing:\n%s", sys)
	}
}
