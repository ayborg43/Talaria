package memory

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPruneForgetsOldMessagesAndKeepsTheSearchIndexInStep(t *testing.T) {
	s, err := OpenSessions(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	oldID, _ := s.Start()
	s.Add(oldID, "user", "the quarterly zebrafish report")
	// Age that session's rows by hand: Add stamps "now".
	old := time.Now().AddDate(0, 0, -100).Unix()
	if _, err := s.db.Exec(`UPDATE messages SET ts = ?`, old); err != nil {
		t.Fatal(err)
	}
	s.db.Exec(`UPDATE sessions SET started_at = ?`, old)
	newID, _ := s.Start()
	s.Add(newID, "user", "today's zebrafish question")

	n, err := s.Prune(time.Now().AddDate(0, 0, -30))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	hits, err := s.Search("zebrafish", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("the old message should be gone from search, the new one kept: %+v", hits)
	}
	var sessions int
	s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions)
	if sessions != 1 {
		t.Fatalf("the emptied old session should go too: %d left", sessions)
	}
	if n, err := s.Prune(time.Now().AddDate(0, 0, -30)); err != nil || n != 0 {
		t.Fatalf("a second sweep finds nothing: %d %v", n, err)
	}
}
