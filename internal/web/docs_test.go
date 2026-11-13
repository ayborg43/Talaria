package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/config"
	"talaria/internal/docsearch"
)

func TestFoldersAreCheckedSavedAndIndexedAtOnce(t *testing.T) {
	r := newRig(t)
	r.srv.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	r.srv.Cfg = &config.Config{}
	folders := &docsearch.Folders{}
	notes := t.TempDir()
	os.WriteFile(filepath.Join(notes, "trip.md"), []byte("We fly to Lisbon on the ninth"), 0o644)
	ix, err := docsearch.Open(filepath.Join(t.TempDir(), "d.db"), docsearch.Sources{Folders: folders.Get}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	r.srv.Docs, r.srv.DocFolders = ix, folders
	r.login()

	for _, bad := range []string{"/", "relative", "/definitely/not/here", filepath.Join(notes, "trip.md"), "/home/me/.ssh"} {
		if resp, _ := r.do("POST", "/api/docs", map[string]any{"folders": []string{bad}}); resp.StatusCode != 400 {
			t.Errorf("%q was accepted: %d", bad, resp.StatusCode)
		}
	}
	if resp, _ := r.do("POST", "/api/docs", map[string]any{"folders": []string{notes, notes}}); resp.StatusCode != 200 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	if got := r.srv.Cfg.DocSearch.Folders; len(got) != 1 {
		t.Errorf("saved folders: %v", got)
	}
	var v docsView
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		_, body := r.do("GET", "/api/docs", nil)
		json.Unmarshal(body, &v)
		if v.Status.Folders == 1 {
			break
		}
	}
	if !v.Enabled || v.Status.Folders != 1 || len(v.Folders) != 1 {
		t.Fatalf("%+v", v)
	}
	if hits, _ := ix.Search(t.Context(), "Lisbon", 3); len(hits) != 1 || !strings.Contains(hits[0].Name, "trip.md") {
		t.Errorf("%+v", hits)
	}
	if resp, _ := r.do("POST", "/api/docs", map[string]any{"folders": []string{}}); resp.StatusCode != 200 {
		t.Errorf("clearing: %d", resp.StatusCode)
	}
}
