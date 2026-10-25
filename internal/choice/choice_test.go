package choice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "sessions.db")}
}

func src() []Source { return []Source{{Title: "aquarium", URL: "https://example.org/visits"}} }

func TestPresentValidatesLikeJev(t *testing.T) {
	s := testStore(t)
	good := []Option{
		{ID: "a", Label: "Morning", Details: []string{"opens at 9"}, Sources: src()},
		{ID: "b", Label: "Afternoon", Details: []string{"opens at 1"}, Sources: src()},
	}
	if _, err := s.Present("discord", 1, "", Comparison, "When?", good, ""); err != nil {
		t.Fatalf("valid comparison: %v", err)
	}
	dup := []Option{
		{ID: "a", Label: "One", Sources: src()},
		{ID: "a", Label: "Two", Sources: src()},
	}
	if _, err := s.Present("discord", 1, "", Clarification, "Pick?", dup, ""); err == nil {
		t.Fatal("duplicate option IDs were kept")
	}
	nosrc := []Option{{ID: "a", Label: "One"}}
	if _, err := s.Present("discord", 1, "", Comparison, "Pick?", nosrc, ""); err == nil {
		t.Fatal("a comparison option without sources was kept")
	}
	four := []Option{
		{ID: "a", Label: "1", Sources: src()}, {ID: "b", Label: "2", Sources: src()},
		{ID: "c", Label: "3", Sources: src()}, {ID: "d", Label: "4", Sources: src()},
	}
	if _, err := s.Present("discord", 1, "", Comparison, "Pick?", four, ""); err == nil {
		t.Fatal("a comparison with four options was kept")
	}
	if _, err := s.Present("discord", 1, "", Clarification, "Pick?", good, "ghost"); err == nil {
		t.Fatal("an unknown preferred option was kept")
	}
	if _, err := s.Present("discord", 1, "", "duel", "Pick?", good, ""); err == nil {
		t.Fatal("an unknown panel type was kept")
	}
}

func TestSelectAnswersAndSupersedes(t *testing.T) {
	s := testStore(t)
	opts := []Option{
		{ID: "a", Label: "Morning", Sources: src()},
		{ID: "b", Label: "Afternoon", Sources: src()},
	}
	first, err := s.Present("discord", 1, "trip", Clarification, "When?", opts, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 || first.Status != Open {
		t.Fatalf("%+v", first)
	}
	second, err := s.Present("discord", 1, "trip", Clarification, "When?", opts, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 {
		t.Fatalf("a new presentation is a new version: %+v", second)
	}
	if _, err := s.Select("discord", 1, first.ID, "a"); err == nil {
		t.Fatal("a superseded panel still answers")
	}
	got, err := s.Select("discord", 1, second.ID, "b")
	if err != nil || got.Selected != "b" || got.Status != Answered {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := s.Select("discord", 1, second.ID, "a"); err == nil {
		t.Fatal("an answered panel answers twice")
	}
	if _, err := s.Select("telegram", 1, second.ID, "a"); err == nil {
		t.Fatal("another service answers this chat's panel")
	}
}

func TestRenderNamesOptions(t *testing.T) {
	s := testStore(t)
	p, err := s.Present("discord", 1, "", Clarification, "When?",
		[]Option{{ID: "a", Label: "Morning", Details: []string{"opens at 9"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	out := Render(p)
	if !strings.Contains(out, "Morning") || !strings.Contains(out, "opens at 9") {
		t.Fatalf("the user cannot pick from this:\n%s", out)
	}
}

func TestLegacyJSONIsImportedOnce(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "choices.json")
	raw := `{"panels":[{"id":"ab12","gateway":"discord","chat_id":1,"thread":"main","type":"clarification","title":"When?","options":[{"id":"a","label":"Morning"}],"status":"open","version":1,"created":"2026-10-20T10:00:00Z","updated":"2026-10-20T10:00:00Z"}]}`
	if err := os.WriteFile(legacy, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: filepath.Join(dir, "sessions.db")}
	got, err := s.Get("discord", 1, "ab12")
	if err != nil || got.Title != "When?" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Fatalf("legacy renamed after import: %v", err)
	}
	if _, err := s.Present("discord", 1, "trip", Clarification, "Where?",
		[]Option{{ID: "a", Label: "Morning"}}, ""); err != nil {
		t.Fatal(err)
	}
	again, err := (&Store{Path: s.Path}).Get("discord", 1, "ab12")
	if err != nil || again.Title != "When?" {
		t.Fatalf("imported panels persist beside new ones: %+v %v", again, err)
	}
}
