package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/provider"
)

func testConvos(t *testing.T) *Convos {
	t.Helper()
	c, err := OpenConvos(filepath.Join(t.TempDir(), "convos.json"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAConversationNamesItselfAfterItsFirstMessage(t *testing.T) {
	c := testConvos(t)
	id := c.New()
	if got := c.List(); len(got) != 1 || got[0].Title != "New chat" || !got[0].Active {
		t.Fatalf("a fresh conversation is blank and open: %+v", got)
	}
	c.Save(id,
		[]*Message{{Role: "user", Text: "  plan my trip to  Kyoto "}, {Role: "assistant", Text: "lovely"}},
		[]provider.Message{{Role: provider.RoleUser, Content: "plan my trip to Kyoto"}})
	got := c.List()
	if len(got) != 1 || got[0].Title != "plan my trip to Kyoto" || got[0].Messages != 2 {
		t.Fatalf("it names itself after what started it: %+v", got)
	}
	cv, ok := c.Get(id)
	if !ok || len(cv.Transcript) != 1 || cv.Transcript[0].Content != "plan my trip to Kyoto" {
		t.Fatalf("the agent's history is kept: %+v", cv)
	}
}

func TestSwitchingKeepsEveryConversation(t *testing.T) {
	c := testConvos(t)
	first := c.New()
	c.Save(first, []*Message{{Role: "user", Text: "first"}}, nil)
	second := c.New()
	if c.ActiveID() != second {
		t.Fatal("the new conversation opens at once")
	}
	if got := c.List(); len(got) != 2 || got[0].ID != second || got[1].ID != first || !got[0].Active || got[1].Active {
		t.Fatalf("newest first, and only the open one is active: %+v", got)
	}
	back, ok := c.Open(first)
	if !ok || len(back.Display) != 1 || c.ActiveID() != first {
		t.Fatalf("the old conversation opens with what it held: %+v", back)
	}
	if _, ok := c.Open("missing"); ok {
		t.Fatal("a conversation that was never there does not open")
	}
}

func TestConversationsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "convos.json")
	c, err := OpenConvos(path)
	if err != nil {
		t.Fatal(err)
	}
	id := c.New()
	c.Save(id, []*Message{{Role: "user", Text: "remember this"}}, []provider.Message{{Role: provider.RoleAssistant, Content: "kept"}})
	again, err := OpenConvos(path)
	if err != nil {
		t.Fatal(err)
	}
	got := again.List()
	if len(got) != 1 || got[0].Title != "remember this" || !got[0].Active || again.ActiveID() != id {
		t.Fatalf("everything comes back, and the open one stays open: %+v", got)
	}
	cv, _ := again.Get(id)
	if len(cv.Transcript) != 1 {
		t.Fatalf("the transcript comes back too: %+v", cv)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("transcripts can hold secrets, so the file stays private: %v", fi.Mode())
	}
	if _, err := OpenConvos(filepath.Join(dir, "missing.json")); err != nil {
		t.Fatalf("no file yet is an empty shelf, not an error: %v", err)
	}
	if _, err := OpenConvos(""); err != nil {
		t.Fatalf("no path is memory only: %v", err)
	}
}

func TestTheShelfHoldsTheNewestHundred(t *testing.T) {
	c := testConvos(t)
	var first string
	for i := 0; i < maxConvos+5; i++ {
		id := c.New()
		if i == 0 {
			first = id
		}
	}
	if got := c.List(); len(got) != maxConvos {
		t.Fatalf("the shelf is bounded: %d", len(got))
	}
	if _, ok := c.Get(first); ok {
		t.Fatal("the oldest inactive conversation makes room")
	}
	for _, m := range c.List() {
		if strings.Contains(m.Title, "…") {
			t.Fatalf("titles are short already: %q", m.Title)
		}
	}
}
