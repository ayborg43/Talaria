package memory

import (
	"strings"
	"testing"
)

func TestNotesAddReplaceRemovePersist(t *testing.T) {
	dir := t.TempDir()
	n := &Notes{Dir: dir}

	for _, text := range []string{"uses macOS", "prefers Go", "prefers Go"} { // duplicate ignored
		if err := n.Add(Agent, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.Add(User, "name is Ayo"); err != nil {
		t.Fatal(err)
	}
	if err := n.Replace(Agent, "macOS", "uses macOS 27"); err != nil {
		t.Fatal(err)
	}
	if err := n.Remove(Agent, "prefers"); err != nil {
		t.Fatal(err)
	}

	p := (&Notes{Dir: dir}).Prompt() // fresh instance proves it hit disk
	if !strings.Contains(p, "- uses macOS 27") || strings.Contains(p, "prefers Go") || !strings.Contains(p, "- name is Ayo") {
		t.Fatalf("unexpected prompt:\n%s", p)
	}
}

func TestNotesErrors(t *testing.T) {
	n := &Notes{Dir: t.TempDir()}
	n.Add(Agent, "alpha one")
	n.Add(Agent, "alpha two")

	if err := n.Replace(Agent, "alpha", "x"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("ambiguous match should fail, got %v", err)
	}
	if err := n.Remove(Agent, "missing"); err == nil {
		t.Fatal("removing a missing entry should fail")
	}
	if err := n.Add("bogus", "x"); err == nil {
		t.Fatal("unknown target should fail")
	}
	if err := n.Add(Agent, "  "); err == nil {
		t.Fatal("empty content should fail")
	}
}

func TestNotesEnforceCap(t *testing.T) {
	n := &Notes{Dir: t.TempDir()}
	if err := n.Add(User, strings.Repeat("a", caps[User])); err != nil {
		t.Fatalf("entry exactly at cap should fit: %v", err)
	}
	if err := n.Add(User, "b"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected cap error, got %v", err)
	}
}

func TestSessionSearch(t *testing.T) {
	s, err := OpenSessions(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	old, _ := s.Start()
	live, _ := s.Start()
	s.Add(old, "user", "How do I configure the nginx reverse proxy for websockets?")
	s.Add(old, "assistant", "Set proxy_http_version 1.1 and the Upgrade header.")
	s.Add(old, "tool", "") // empty ignored
	s.Add(live, "user", "nginx again, in the live session")

	hits, err := s.Search("nginx websockets", 5, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].SessionID != old || !strings.Contains(hits[0].Snippet, "[nginx]") {
		t.Fatalf("unexpected hits: %+v", hits)
	}

	// Punctuation and FTS operators must not break the query.
	if _, err := s.Search(`proxy_http_version "1.1" AND (Upgrade`, 5, live); err != nil {
		t.Fatalf("special characters broke search: %v", err)
	}
	if hits, _ := s.Search("kubernetes", 5, live); len(hits) != 0 {
		t.Fatalf("expected no hits, got %+v", hits)
	}
	if _, err := s.Search("!!!", 5, live); err == nil {
		t.Fatal("query without words should error")
	}
}
