package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadListDelete(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if err := s.Save("deploy-site", "Deploy the static site.\nUse when asked to ship.", "1. build\n2. rsync"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("deploy-site")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "Deploy the static site. Use when asked to ship." || got.Body != "1. build\n2. rsync" {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	// Overwrite is how edits work.
	if err := s.Save("deploy-site", "Deploy v2", "new body"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load("deploy-site"); got.Body != "new body" {
		t.Fatalf("overwrite failed: %+v", got)
	}

	if idx := s.Index(); !strings.Contains(idx, "- deploy-site: Deploy v2") {
		t.Fatalf("index missing skill: %q", idx)
	}
	if err := s.Delete("deploy-site"); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 || s.Index() != "" {
		t.Fatal("skill still listed after delete")
	}
}

func TestRejectsBadNamesAndPathTraversal(t *testing.T) {
	root := t.TempDir()
	s := &Store{Dir: filepath.Join(root, "skills")}
	for _, name := range []string{"", "../evil", "a/b", "Upper", "-lead", strings.Repeat("a", 65)} {
		if err := s.Save(name, "d", "b"); err == nil {
			t.Errorf("Save accepted bad name %q", name)
		}
		if _, err := s.Load(name); err == nil {
			t.Errorf("Load accepted bad name %q", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
		t.Fatal("path traversal wrote outside the store")
	}
}

func TestSaveValidatesContent(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if s.Save("x", "  ", "body") == nil || s.Save("x", "d", " ") == nil || s.Save("x", "d", strings.Repeat("a", maxBody+1)) == nil {
		t.Fatal("expected validation errors")
	}
}
