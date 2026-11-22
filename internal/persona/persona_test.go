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

func TestDescriptionIsTheChooserLineAndScopesLimitTools(t *testing.T) {
	a := Agent{Name: "Inbox", Description: "sorts mail", Instructions: "Triage my mail carefully.", Scopes: []string{"mail", "docs"}}
	if a.About() != "sorts mail" {
		t.Fatalf("about: %q", a.About())
	}
	if got := (Agent{Name: "R", Instructions: "Look things up and cite."}).About(); got != "Look things up and cite." {
		t.Fatalf("fallback about: %q", got)
	}
	for name, want := range map[string]bool{"email_list": true, "docs_search": true, "shell": false, "browser_open": false, "current_time": true, "web_search": false} {
		if got := a.AllowsTool(name); got != want {
			t.Errorf("AllowsTool(%s) = %v, want %v", name, got, want)
		}
	}
	if (Agent{Name: "A", Instructions: "x"}).AllowsTool("shell") != true {
		t.Fatal("no scopes means every area is open")
	}
	s := &Store{Path: filepath.Join(t.TempDir(), "agents.json")}
	if _, err := s.Save(Agent{Name: "Bad", Instructions: "x", Scopes: []string{"nope"}}); err == nil {
		t.Fatal("an unknown tool area was accepted")
	}
	if _, err := s.Save(Agent{Name: "Bad", Instructions: "x", Description: strings.Repeat("d", MaxDescription+1)}); err == nil {
		t.Fatal("a long description was accepted")
	}
	if !strings.Contains(a.System(), "limited to these areas: mail, docs") {
		t.Fatalf("system does not say the limits: %q", a.System())
	}
}

func TestDuplicateAndRevert(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "agents.json")}
	a, err := s.Save(Agent{Name: "Inbox", Instructions: "first wording", Description: "sorts mail", Scopes: []string{"mail"}})
	if err != nil {
		t.Fatal(err)
	}
	a.Instructions = "second wording"
	b, err := s.Save(a)
	if err != nil || b.PrevInstructions != "first wording" {
		t.Fatalf("edit did not keep the last wording: %+v %v", b, err)
	}
	cp, err := s.Duplicate(a.ID)
	if err != nil || cp.Name != "Inbox copy" || cp.ID == a.ID || cp.Instructions != "second wording" || cp.Description != "sorts mail" || len(cp.Scopes) != 1 {
		t.Fatalf("copy: %+v %v", cp, err)
	}
	if cp2, err := s.Duplicate(a.ID); err != nil || cp2.Name != "Inbox copy 2" {
		t.Fatalf("second copy: %+v %v", cp2, err)
	}
	r, err := s.Revert(a.ID)
	if err != nil || r.Instructions != "first wording" || r.PrevInstructions != "second wording" {
		t.Fatalf("revert: %+v %v", r, err)
	}
	if r, err := s.Revert(a.ID); err != nil || r.Instructions != "second wording" {
		t.Fatalf("revert again should put the newer wording back: %+v %v", r, err)
	}
	if _, err := s.Revert(cp.ID); err == nil {
		t.Fatal("a copy has no earlier wording to revert to")
	}
	if _, err := s.Duplicate("nope"); err == nil {
		t.Fatal("duplicating one that is not there")
	}
}
