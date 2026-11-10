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
	var first, second string
	for i := 0; i < maxConvos+5; i++ {
		id := c.New()
		switch i {
		case 0:
			first = id
		case 1:
			second = id
		}
	}
	if got := c.List(); len(got) != maxConvos {
		t.Fatalf("the shelf is bounded: %d", len(got))
	}
	if _, ok := c.Get(second); ok {
		t.Fatal("the oldest inactive conversation makes room")
	}
	if _, ok := c.Get(first); !ok || c.MainID() != first {
		t.Fatal("the main conversation was dropped to make room")
	}
	for _, m := range c.List() {
		if strings.Contains(m.Title, "…") {
			t.Fatalf("titles are short already: %q", m.Title)
		}
	}
}

func TestAConversationCanBeRenamedAndArchived(t *testing.T) {
	c := testConvos(t)
	id := c.New()
	other := c.New() // newest, and open
	if err := c.Rename(id, "  Trip to Kyoto  "); err != nil {
		t.Fatal(err)
	}
	if err := c.Rename(id, "  "); err == nil {
		t.Fatal("an empty name was accepted")
	}
	if err := c.Rename("nope", "x"); err == nil {
		t.Fatal("a missing conversation was renamed")
	}
	got := c.List()
	if len(got) != 2 || got[1].Title != "Trip to Kyoto" {
		t.Fatalf("rename not listed: %+v", got)
	}
	// A rename survives later saves: the auto-title only fills an empty one.
	c.Save(id, []*Message{{Role: "user", Text: "something else entirely"}}, nil)
	if c.List()[0].ID != id || c.List()[0].Title != "Trip to Kyoto" {
		t.Fatalf("save overwrote the rename: %+v", c.List())
	}
	if err := c.SetArchived(id, true); err == nil {
		t.Fatal("the main conversation was archived")
	}
	if err := c.SetMain(other); err != nil {
		t.Fatal(err)
	}
	if err := c.SetArchived(id, true); err != nil {
		t.Fatal(err)
	}
	if got := c.List(); len(got) != 1 || got[0].ID != other {
		t.Fatalf("archived still listed: %+v", got)
	}
	if got := c.ListArchived(); len(got) != 1 || got[0].ID != id || !got[0].Archived {
		t.Fatalf("archive not listed separately: %+v", got)
	}
	if err := c.SetArchived(id, false); err != nil {
		t.Fatal(err)
	}
	if len(c.List()) != 2 {
		t.Fatalf("unarchived not back: %+v", c.List())
	}
}

// A side chat opens beside the conversation it came from, which stays as it
// was; the main conversation is marked, and survives a reload.
func TestSideChatsHangFromTheConversationTheyWereOpenedBeside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chats.json")
	c, err := OpenConvos(path)
	if err != nil {
		t.Fatal(err)
	}
	main := c.New()
	c.Save(main, []*Message{{Role: "user", Text: "plan the trip"}}, nil)
	side := c.NewSide()
	if c.ActiveID() != side {
		t.Fatal("the side chat is not the open one")
	}
	nested := c.NewSide() // a side chat of a side chat hangs from the same conversation
	var metas = map[string]ConvoMeta{}
	for _, m := range c.List() {
		metas[m.ID] = m
	}
	if !metas[main].Main || metas[main].Side != "" || metas[main].Messages != 1 {
		t.Fatalf("main = %+v", metas[main])
	}
	if metas[side].Side != "plan the trip" || metas[nested].Side != "plan the trip" || metas[side].Main {
		t.Fatalf("side = %+v nested = %+v", metas[side], metas[nested])
	}
	again, err := OpenConvos(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.MainID() != main {
		t.Fatalf("after a reload the main conversation is %q, want %q", again.MainID(), main)
	}
	if err := again.SetMain(side); err != nil || again.MainID() != side {
		t.Fatalf("SetMain: %v", err)
	}
	for _, m := range again.List() {
		if m.ID == side && (m.Side != "" || !m.Main) {
			t.Fatalf("the new main conversation still reads as a side chat: %+v", m)
		}
	}
}

func TestAConversationCanBeDeleted(t *testing.T) {
	c := testConvos(t)
	main := c.New() // the first one kept becomes the main conversation
	old := c.New()
	open := c.New()
	if c.MainID() != main {
		t.Fatalf("main is %q, want %q", c.MainID(), main)
	}
	if _, err := c.Delete(main); err == nil {
		t.Fatal("the main conversation was deleted")
	}
	if _, err := c.Delete("nope"); err == nil {
		t.Fatal("a missing conversation was deleted")
	}
	if err := c.SetArchived(old, true); err != nil {
		t.Fatal(err)
	}
	if was, err := c.Delete(old); err != nil || was { // an archived one goes too
		t.Fatalf("delete archived: %v active=%v", err, was)
	}
	if len(c.ListArchived()) != 0 {
		t.Fatalf("deleted conversation still archived: %+v", c.ListArchived())
	}
	if was, err := c.Delete(open); err != nil || !was {
		t.Fatalf("delete open: %v active=%v", err, was)
	}
	if c.ActiveID() != main {
		t.Fatalf("after deleting the open conversation %q is open, want main %q", c.ActiveID(), main)
	}
	if _, ok := c.Get(open); ok {
		t.Fatal("deleted conversation can still be loaded")
	}
}

func TestAChannelIsAConversationWithAPurpose(t *testing.T) {
	c := testConvos(t)
	main := c.New()
	id, err := c.NewChannel("  Wholesale   orders ", "📦", "ok", "Orders from shops: quotes, stock, shipping.")
	if err != nil {
		t.Fatal(err)
	}
	if c.ActiveID() != id || c.MainID() != main {
		t.Fatalf("active %q main %q", c.ActiveID(), c.MainID())
	}
	var got ConvoMeta
	for _, m := range c.List() {
		if m.ID == id {
			got = m
		}
	}
	if got.Title != "Wholesale orders" || got.Icon != "📦" || got.Color != "ok" || got.Purpose == "" {
		t.Fatalf("listed as %+v", got)
	}
	for name, args := range map[string][4]string{
		"no name":    {"", "x", "ok", "p"},
		"long name":  {strings.Repeat("a", 41), "x", "ok", "p"},
		"long icon":  {"n", "123456789", "ok", "p"},
		"bad colour": {"n", "x", "red; background:url(//evil)", "p"},
		"long why":   {"n", "x", "ok", strings.Repeat("a", 241)},
	} {
		if _, err := c.NewChannel(args[0], args[1], args[2], args[3]); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := c.NewChannel("Quiet", "", "", ""); err != nil { // a colour and a purpose are optional
		t.Fatal(err)
	}
}
