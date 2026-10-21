package memory

import (
	"database/sql"
	"path/filepath"
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

// With more than one person on the allowlist, one chat must not be able to search another's history.
func TestAChatSearchesOnlyItsOwnHistory(t *testing.T) {
	s, err := OpenSessions(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	alice, _ := s.StartFor("discord:1")
	bob, _ := s.StartFor("discord:2")
	local, _ := s.Start()
	s.Add(alice, "user", "alice's secret plan: pineapple")
	s.Add(bob, "user", "bob's secret plan: pineapple too")
	s.Add(local, "user", "the owner's pineapple note")

	count := func(owner string) int {
		hits, err := s.SearchFor(owner, "pineapple", 20, "")
		if err != nil {
			t.Fatal(err)
		}
		return len(hits)
	}
	if count("discord:1") != 1 || count("discord:2") != 1 {
		t.Fatalf("each chat sees only its own: %d %d", count("discord:1"), count("discord:2"))
	}
	if count("telegram:1") != 0 {
		t.Fatal("the same number on another service is another chat")
	}
	if count("") != 3 {
		t.Fatalf("the command line is the person who runs Talaria and sees everything: %d", count(""))
	}
	hits, _ := s.Search("pineapple", 20, "")
	if len(hits) != 3 {
		t.Fatal("Search is the unscoped form")
	}
}

// A history made before sessions had owners keeps working: its sessions belong to no chat.
func TestAnOlderHistoryIsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, _ := sql.Open("sqlite", path)
	for _, q := range []string{
		`CREATE TABLE sessions(id TEXT PRIMARY KEY, started_at INTEGER NOT NULL)`,
		`CREATE TABLE messages(id INTEGER PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), role TEXT NOT NULL, content TEXT NOT NULL, ts INTEGER NOT NULL)`,
		`CREATE VIRTUAL TABLE messages_fts USING fts5(content, content='messages', content_rowid='id')`,
		`CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content); END`,
		`INSERT INTO sessions VALUES ('old', 1)`,
		`INSERT INTO messages(session_id, role, content, ts) VALUES ('old', 'user', 'a kiwi from before', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := OpenSessions(path)
	if err != nil {
		t.Fatalf("an older database must open: %v", err)
	}
	defer s.Close()
	if hits, _ := s.Search("kiwi", 5, ""); len(hits) != 1 {
		t.Fatal("the command line still finds it")
	}
	if hits, _ := s.SearchFor("discord:1", "kiwi", 5, ""); len(hits) != 0 {
		t.Fatal("a chat does not see what belongs to nobody")
	}
	s2, err := OpenSessions(path) // and opening again changes nothing
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}
