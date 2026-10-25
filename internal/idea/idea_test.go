package idea

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/tool"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(t.TempDir(), "sessions.db")}
}

func TestAddNeedsSourceEvidence(t *testing.T) {
	s := testStore(t)
	if _, err := s.Add("discord", 1, "Track prices", "watch the price page", nil); err == nil {
		t.Fatal("an idea without source evidence was kept")
	}
	if _, err := s.Add("discord", 1, "Track prices", "watch the price page", []Source{{Title: "x", URL: "gopher://x"}}); err == nil {
		t.Fatal("a non-HTTP source was kept")
	}
}

func TestSuggestExcludesSentRepliesAndDoneWork(t *testing.T) {
	s := testStore(t)
	now := time.Now()
	must := func(title string) {
		t.Helper()
		if _, err := s.Add("discord", 1, title, "body", []Source{{Title: "mail", URL: "https://mail.example/inbox"}}); err != nil {
			t.Fatal(err)
		}
	}
	must("Summarize the inbox")
	must("Watch the price page")
	must("Plan the trip")
	_ = now
	got := s.Suggest("discord", 1, []string{"I summarized the inbox for you"}, []string{"Plan the TRIP"})
	if len(got) != 1 || got[0].Title != "Watch the price page" {
		t.Fatalf("sent replies and done work must be excluded: %+v", got)
	}
}

func TestSuggestDedupesRepeats(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Add("discord", 1, "  Watch   the price page ", "body", []Source{{Title: "p", URL: "https://p.example/"}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Suggest("discord", 1, nil, nil); len(got) != 1 {
		t.Fatalf("repeats collapse to one: %+v", got)
	}
}

func TestListLeavesOutWhatWasAlreadySaid(t *testing.T) {
	s := testStore(t)
	for _, title := range []string{"Summarize the inbox", "Watch the price page"} {
		if _, err := s.Add("discord", 1, title, "body", []Source{{Title: "mail", URL: "https://mail.example/inbox"}}); err != nil {
			t.Fatal(err)
		}
	}
	reg := tool.NewRegistry()
	RegisterTools(reg, Binding{Store: s, Gateway: "discord", ChatID: 1,
		Sent: func() []string { return []string{"I summarized the inbox for you"} }})
	list, ok := reg.Get("idea_list")
	if !ok {
		t.Fatal("idea_list not registered")
	}
	out, err := list.Run(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Summarize") || !strings.Contains(out, "Watch the price") {
		t.Fatalf("the tool must hide what was said: %q", out)
	}
}

func TestAcceptEditDismiss(t *testing.T) {
	s := testStore(t)
	id, err := s.Add("discord", 1, "Watch the price page", "body", []Source{{Title: "p", URL: "https://p.example/"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Edit("discord", 1, id, "Watch the price page daily", "body"); err != nil {
		t.Fatal(err)
	}
	if err := s.Dismiss("discord", 1, id); err != nil {
		t.Fatal(err)
	}
	if got := s.Suggest("discord", 1, nil, nil); len(got) != 0 {
		t.Fatalf("dismissed ideas stay gone: %+v", got)
	}
	if err := s.Dismiss("telegram", 1, id); err == nil {
		t.Fatal("another service cannot dismiss this chat's idea")
	}
}

func TestLegacyJSONIsImportedOnce(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "ideas.json")
	raw := `{"ideas":[{"id":"ab12","gateway":"discord","chat_id":1,"title":"Watch the price page","body":"body","sources":[{"title":"p","url":"https://p.example/"}],"status":"open","created":"2026-10-20T10:00:00Z","updated":"2026-10-20T10:00:00Z"}]}`
	if err := os.WriteFile(legacy, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: filepath.Join(dir, "sessions.db")}
	got := s.Suggest("discord", 1, nil, nil)
	if len(got) != 1 || got[0].ID != "ab12" || got[0].Title != "Watch the price page" {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Fatalf("legacy renamed after import: %v", err)
	}
	if _, err := s.Add("discord", 1, "Plan the trip", "body", []Source{{Title: "p", URL: "https://p.example/"}}); err != nil {
		t.Fatal(err)
	}
	if again := (&Store{Path: s.Path}).Suggest("discord", 1, nil, nil); len(again) != 2 {
		t.Fatalf("imported ideas persist beside new ones: %+v", again)
	}
}
