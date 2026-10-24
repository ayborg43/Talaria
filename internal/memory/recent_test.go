package memory

import (
	"path/filepath"
	"testing"
)

func TestRecentReturnsTheLatestMessagesOfARole(t *testing.T) {
	s, err := OpenSessions(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range [][2]string{
		{"user", "what is on today?"},
		{"assistant", "here is your morning summary"},
		{"assistant", "and the inbox is quiet"},
	} {
		if err := s.Add(id, m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Recent(id, "assistant", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "and the inbox is quiet" || got[1] != "here is your morning summary" {
		t.Fatalf("newest first, assistants only: %q", got)
	}
	if got, err := s.Recent(id, "assistant", 1); err != nil || len(got) != 1 {
		t.Fatalf("limit holds: %q %v", got, err)
	}
	if got, err := s.Recent("nope", "assistant", 10); err != nil || len(got) != 0 {
		t.Fatalf("unknown session: %q %v", got, err)
	}
}
