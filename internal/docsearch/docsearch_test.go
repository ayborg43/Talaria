package docsearch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"talaria/internal/files"
	"talaria/internal/tool"
)

type library struct {
	list  []files.File
	data  map[string][]byte
	opens atomic.Int32
}

func (l *library) List() ([]files.File, error) { return l.list, nil }
func (l *library) Open(id string) (files.File, []byte, error) {
	l.opens.Add(1)
	for _, f := range l.list {
		if f.ID == id {
			return f, l.data[id], nil
		}
	}
	return files.File{}, nil, os.ErrNotExist
}

func write(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func open(t *testing.T, src Sources) *Index {
	t.Helper()
	ix, err := Open(filepath.Join(t.TempDir(), "docs.db"), src, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func TestFilesNotesAndFoldersAreAllSearchedAndSaySoWhere(t *testing.T) {
	notes := t.TempDir()
	write(t, notes, "trips/lisbon.md", "# Lisbon\nWe stay near Alfama and eat at the tiles museum café.")
	write(t, notes, "work/plan.txt", "Quarterly plan: hire two engineers in March.")
	pdf := files.GeneratePDF("Lease", "The lease ends on the thirty-first of August and renews yearly.")
	lib := &library{
		list: []files.File{
			{ID: "f1", Name: "lease.pdf", MIME: "application/pdf", Size: int64(len(pdf)), Created: time.Unix(1000, 0)},
			{ID: "f2", Name: "photo.png", Size: 10, Created: time.Unix(1000, 0)},
			{ID: "f3", Name: "invite.html", Size: 100, Created: time.Unix(1000, 0)},
		},
		data: map[string][]byte{"f1": pdf, "f2": {0x89, 'P', 'N', 'G', 0}, "f3": []byte("<html><style>x{}</style><body><p>Dinner at <b>Chez Paul</b> on Friday</p></body></html>")},
	}
	ix := open(t, Sources{Library: lib, Folders: func() []string { return []string{notes} }, Notes: func() string { return "The user prefers morning meetings." }})
	st := ix.Refresh(t.Context())
	if st.Files != 2 || st.Folders != 2 || !st.Notes {
		t.Fatalf("%+v", st)
	}
	for query, want := range map[string]string{
		"Alfama":           "lisbon.md",
		"hire engineers":   "plan.txt",
		"lease August":     "lease.pdf",
		"Chez Paul":        "invite.html",
		"morning meetings": "notes",
	} {
		hits, err := ix.Search(t.Context(), query, 5)
		if err != nil || len(hits) == 0 || !strings.Contains(strings.ToLower(hits[0].Name), strings.ToLower(want)) {
			t.Errorf("%q: %+v %v", query, hits, err)
		}
	}
	if hits, _ := ix.Search(t.Context(), "Chez Paul", 5); strings.Contains(hits[0].Snippet, "<") || strings.Contains(hits[0].Snippet, "x{}") {
		t.Errorf("markup in a snippet: %q", hits[0].Snippet)
	}
	if hits, _ := ix.Search(t.Context(), "dinosaur", 5); len(hits) != 0 {
		t.Errorf("a miss found something: %+v", hits)
	}
	if _, err := ix.Search(t.Context(), "   ", 5); err == nil {
		t.Error("an empty search was accepted")
	}
	// the words are plain words, not query syntax
	if _, err := ix.Search(t.Context(), `lisbon" OR NEAR(a b) -`, 5); err != nil {
		t.Errorf("query syntax leaked through: %v", err)
	}
}

func TestSecretsBigAndBinaryFilesAreLeftOut(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ok.md", "visible words")
	write(t, dir, ".env", "TOKEN=sk-abcdefghijklmnopqrstuvwxyz")
	write(t, dir, "passwords.txt", "hunter2")
	write(t, dir, "key.pem", "-----BEGIN PRIVATE KEY-----")
	write(t, dir, ".ssh/notes.txt", "ssh hosts")
	write(t, dir, ".git/config.txt", "git thing")
	write(t, dir, "node_modules/x/readme.md", "vendored")
	write(t, dir, "blob.txt", "a\x00b")
	big := make([]byte, MaxFileBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	os.WriteFile(filepath.Join(dir, "huge.txt"), big, 0o644)
	os.Symlink("/etc/hosts", filepath.Join(dir, "hosts.txt"))
	ix := open(t, Sources{Folders: func() []string { return []string{dir, "/", "relative/path"} }})
	st := ix.Refresh(t.Context())
	if st.Folders != 1 {
		t.Fatalf("only one file should be indexed: %+v", st)
	}
	for _, q := range []string{"TOKEN", "hunter2", "PRIVATE", "ssh", "vendored", "localhost"} {
		if hits, _ := ix.Search(t.Context(), q, 5); len(hits) != 0 {
			t.Errorf("%q was found: %+v", q, hits)
		}
	}
}

func TestOnlyChangedFilesAreReadAgainAndDeletedOnesDisappear(t *testing.T) {
	lib := &library{list: []files.File{{ID: "a", Name: "a.txt", Size: 5, Created: time.Unix(1, 0)}}, data: map[string][]byte{"a": []byte("alpha")}}
	dir := t.TempDir()
	p := write(t, dir, "b.md", "bravo")
	ix := open(t, Sources{Library: lib, Folders: func() []string { return []string{dir} }})
	ix.Refresh(t.Context())
	opens := lib.opens.Load()
	ix.Refresh(t.Context())
	if lib.opens.Load() != opens {
		t.Error("an unchanged kept file was read again")
	}
	// a changed folder file is picked up
	os.WriteFile(p, []byte("bravo and charlie"), 0o644)
	os.Chtimes(p, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	ix.Refresh(t.Context())
	if hits, _ := ix.Search(t.Context(), "charlie", 3); len(hits) != 1 {
		t.Errorf("a changed file: %+v", hits)
	}
	// a deleted one is dropped, and so is one that left the kept files
	os.Remove(p)
	lib.list = nil
	ix.Refresh(t.Context())
	for _, q := range []string{"bravo", "alpha"} {
		if hits, _ := ix.Search(t.Context(), q, 3); len(hits) != 0 {
			t.Errorf("%q survived its file: %+v", q, hits)
		}
	}
}

func TestALongDocumentIsReadInPartsAndRefsAreChecked(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "long.txt", strings.Repeat("filler ", ReadChars)+"needle")
	ix := open(t, Sources{Folders: func() []string { return []string{dir} }})
	ix.Refresh(t.Context())
	hits, _ := ix.Search(t.Context(), "needle", 1)
	name, text, more, err := ix.Read(t.Context(), hits[0].Ref, 0)
	if err != nil || !more || len([]rune(text)) != ReadChars || !strings.Contains(name, "long.txt") {
		t.Fatalf("%q %d %v %v", name, len(text), more, err)
	}
	if _, _, more, _ := ix.Read(t.Context(), hits[0].Ref, 7*ReadChars); more {
		t.Error("the end of the document says there is more")
	}
	if _, _, _, err := ix.Read(t.Context(), "path:/etc/passwd", 0); err == nil {
		t.Error("a path that was never indexed could be read")
	}
}

func TestTheToolsAreReadOnlyAndTreatTheTextAsOutsideContent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "n.md", "remember the dentist on Thursday")
	ix := open(t, Sources{Folders: func() []string { return []string{dir} }})
	ix.Refresh(t.Context())
	reg := tool.NewRegistry()
	RegisterTools(reg, ix)
	s, _ := reg.Get("docs_search")
	r, _ := reg.Get("docs_read")
	if !s.Untrusted || !r.Untrusted || s.NeedsApproval || r.NeedsApproval {
		t.Error("flags")
	}
	out, err := s.Run(context.Background(), []byte(`{"query":"dentist"}`))
	if err != nil || !strings.Contains(out, "n.md") || !strings.Contains(out, "ref=path:") {
		t.Fatalf("%q %v", out, err)
	}
	ref := out[strings.Index(out, "ref=")+4 : strings.Index(out, "\n  …")]
	got, err := r.Run(context.Background(), []byte(`{"ref":"`+ref+`"}`))
	if err != nil || !strings.Contains(got, "dentist on Thursday") {
		t.Errorf("%q %v", got, err)
	}
	if none, _ := s.Run(context.Background(), []byte(`{"query":"zebra"}`)); !strings.Contains(none, "Nothing") {
		t.Errorf("a miss: %q", none)
	}
}
