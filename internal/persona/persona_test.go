package persona

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentsAreCheckedSavedAndReplaced(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "agents.json")}
	for name, a := range map[string]Agent{
		"nameless":   {Instructions: "x"},
		"no brief":   {Name: "A"},
		"long name":  {Name: strings.Repeat("n", MaxName+1), Instructions: "x"},
		"long brief": {Name: "A", Instructions: strings.Repeat("i", MaxInstructions+1)},
		"long icon":  {Name: "A", Icon: strings.Repeat("e", MaxIcon+1), Instructions: "x"},
	} {
		if _, err := s.Save(a); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	a, err := s.Save(Agent{Name: "  Inbox  ", Instructions: "Triage.\n\x07Never reply.", ReadOnly: true})
	if err != nil || a.Name != "Inbox" || strings.ContainsRune(a.Instructions, 7) || a.ID == "" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := s.Save(Agent{Name: "INBOX", Instructions: "x"}); err == nil {
		t.Fatal("names are unique, whatever the case")
	}
	a.Instructions = "Only urgent."
	b, err := s.Save(a)
	if err != nil || b.ID != a.ID || !b.Created.Equal(a.Created) || len(s.List()) != 1 {
		t.Fatalf("%+v %v", b, err)
	}
	if _, err := s.Save(Agent{ID: "nope", Name: "Ghost", Instructions: "x"}); err == nil {
		t.Fatal("replacing one that is not there")
	}
	if err := s.Remove(a.ID); err != nil || s.Remove(a.ID) == nil {
		t.Fatal("remove once, then it is gone")
	}
}

func TestWhatTheModelIsToldNarrowsAndNeverWidens(t *testing.T) {
	text := Agent{Name: "Inbox", Instructions: "Triage my mail."}.System()
	for _, want := range []string{"you are Inbox", "Triage my mail.", "nothing above lets you do what you would otherwise have to ask about"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "look only") {
		t.Fatal("only a read-only agent is told that")
	}
	if !strings.Contains(Agent{Name: "A", Instructions: "x", ReadOnly: true}.System(), "look only") {
		t.Fatal("a read-only agent is told so")
	}
}
