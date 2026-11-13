// Package docsearch lets the agent search the person's own files and notes: what Talaria keeps (uploads, the documents it
// made), the notes it holds about them, and any folders the owner names. It is a plain full-text index in a small SQLite
// file, so it costs disk, not memory: nothing is held in RAM beyond the file being read, and there are no embeddings and
// no model. The index is rebuilt in the background and only changed files are read again.
package docsearch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"talaria/internal/files"

	_ "modernc.org/sqlite" // pure-Go driver, FTS5 included
)

// Bounds that keep the index small and a scan quick.
const (
	MaxFileBytes   = 2 << 20 // a file larger than this is not read
	MaxContent     = 200000  // characters kept of one file
	MaxFilesTotal  = 5000    // files taken from the folders
	maxDepth       = 6
	ReadChars      = 12000 // what one docs_read returns
	staleAfter     = 10 * time.Minute
	noteRef        = "notes"
	maxSnippetHits = 20
)

const schema = `
CREATE TABLE IF NOT EXISTS docs(
	id INTEGER PRIMARY KEY,
	ref TEXT UNIQUE NOT NULL,
	kind TEXT NOT NULL,
	name TEXT NOT NULL,
	mtime INTEGER NOT NULL,
	size INTEGER NOT NULL,
	content TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS docs_fts USING fts5(name, content, content='docs', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS docs_ai AFTER INSERT ON docs BEGIN
	INSERT INTO docs_fts(rowid, name, content) VALUES (new.id, new.name, new.content);
END;
CREATE TRIGGER IF NOT EXISTS docs_ad AFTER DELETE ON docs BEGIN
	INSERT INTO docs_fts(docs_fts, rowid, name, content) VALUES ('delete', old.id, old.name, old.content);
END;
CREATE TRIGGER IF NOT EXISTS docs_au AFTER UPDATE ON docs BEGIN
	INSERT INTO docs_fts(docs_fts, rowid, name, content) VALUES ('delete', old.id, old.name, old.content);
	INSERT INTO docs_fts(rowid, name, content) VALUES (new.id, new.name, new.content);
END;
`

// Folders is the list of folders to index, changeable while Talaria runs.
type Folders struct {
	mu   sync.RWMutex
	list []string
}

// Get is the current list.
func (f *Folders) Get() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return append([]string(nil), f.list...)
}

// Set replaces the list.
func (f *Folders) Set(list []string) {
	f.mu.Lock()
	f.list = append([]string(nil), list...)
	f.mu.Unlock()
}

// Check says whether a path may be indexed: an absolute folder that exists, and is not the whole disk.
func Check(path string) (string, error) {
	p := filepath.Clean(strings.TrimSpace(path))
	switch {
	case p == "" || p == ".":
		return "", errors.New("name a folder")
	case !filepath.IsAbs(p):
		return "", fmt.Errorf("%q is not a full path (it should start with /)", path)
	case p == "/":
		return "", errors.New("the whole disk is too much to index: name the folder your notes or documents are in")
	}
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if secretName(part) && part != "" {
			return "", fmt.Errorf("%q looks like a place that holds secrets, so it is never indexed", path)
		}
	}
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%q is not a folder on this machine", path)
	}
	return p, nil
}

// Library is what Talaria keeps: the files store, for this package.
type Library interface {
	List() ([]files.File, error)
	Open(id string) (files.File, []byte, error)
}

// Sources says what to index.
type Sources struct {
	Library Library
	// Folders are directories the owner chose; their text files are indexed, read only.
	Folders func() []string
	// Notes returns the notes the agent holds, as one text; empty when there are none.
	Notes func() string
}

// Index is the searchable copy.
type Index struct {
	db   *sql.DB
	src  Sources
	logf func(string, ...any)

	mu        sync.Mutex // one refresh at a time
	refreshed time.Time
	last      Status
}

// Status is what the index holds.
type Status struct {
	Files   int       `json:"files"`   // kept files indexed
	Folders int       `json:"folders"` // files taken from the owner's folders
	Notes   bool      `json:"notes"`
	At      time.Time `json:"at"`
	Skipped int       `json:"skipped"` // too large, unreadable or not text
	Error   string    `json:"error,omitempty"`
}

// Open makes or opens the index file.
func Open(path string, src Sources, logf func(string, ...any)) (*Index, error) {
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		f.Close()
	}
	os.Chmod(path, 0o600)
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=cache_size(-2000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one connection keeps SQLite's memory use down
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Index{db: db, src: src, logf: logf}, nil
}

// Close closes the file.
func (ix *Index) Close() error { return ix.db.Close() }

// Run keeps the index current until ctx ends: once now, then every few minutes.
func (ix *Index) Run(ctx context.Context) {
	go func() {
		ix.Refresh(ctx)
		t := time.NewTicker(staleAfter)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				ix.Refresh(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Status says what was indexed at the last refresh.
func (ix *Index) Status() Status {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.last
}

var (
	tagRe   = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)
	spaceRe = regexp.MustCompile(`[ \t]+`)
	nlRe    = regexp.MustCompile(`\n{3,}`)
)

var textExt = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".org": true, ".rst": true, ".csv": true, ".tsv": true, ".json": true, ".yaml": true,
	".yml": true, ".toml": true, ".html": true, ".htm": true, ".log": true, ".ini": true, ".tex": true, ".pdf": true,
}

// secretName says a file or folder that is likely to hold a secret, so it is never read.
func secretName(name string) bool {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, ".env"), n == "id_rsa", n == "id_ed25519", strings.HasSuffix(n, ".pem"), strings.HasSuffix(n, ".key"),
		strings.HasSuffix(n, ".p12"), strings.HasSuffix(n, ".kdbx"), strings.Contains(n, "password"), strings.Contains(n, "secret"), strings.Contains(n, "credential"):
		return true
	}
	switch n {
	case ".ssh", ".gnupg", ".aws", ".kube", ".docker", ".config", "node_modules", ".git":
		return true
	}
	return false
}

// extract turns a file's bytes into text by its name, or says it is not a text file.
func extract(name string, data []byte) (string, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	if !textExt[ext] {
		return "", false
	}
	var text string
	switch ext {
	case ".pdf":
		t, err := files.ExtractText(data)
		if err != nil || strings.TrimSpace(t) == "" {
			return "", false
		}
		text = t
	case ".html", ".htm":
		text = html.UnescapeString(tagRe.ReplaceAllString(string(data), " "))
	default:
		if !validText(data) {
			return "", false
		}
		text = string(data)
	}
	text = nlRe.ReplaceAllString(spaceRe.ReplaceAllString(text, " "), "\n\n")
	if r := []rune(text); len(r) > MaxContent {
		text = string(r[:MaxContent])
	}
	return strings.TrimSpace(text), strings.TrimSpace(text) != ""
}

// validText says the bytes look like text, not a binary file with a text-like name.
func validText(b []byte) bool {
	n := min(len(b), 4096)
	for _, c := range b[:n] {
		if c == 0 {
			return false
		}
	}
	return true
}

type seen struct {
	kind, name string
	mtime      int64
	size       int64
	read       func() (string, bool) // reads the content, called only when the file changed
}

// Refresh brings the index in line with what there is now: new and changed files are read, deleted ones dropped.
func (ix *Index) Refresh(ctx context.Context) Status {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	st := Status{At: time.Now()}
	want := map[string]seen{}
	if ix.src.Library != nil {
		if list, err := ix.src.Library.List(); err == nil {
			for _, f := range list {
				f := f
				if !textExt[strings.ToLower(filepath.Ext(f.Name))] && f.Kind == "" {
					continue
				}
				name := f.Name
				if f.Kind != "" && filepath.Ext(name) == "" {
					name += ".html" // a made page is stored as markup
				}
				want["file:"+f.ID] = seen{kind: "file", name: f.Name, mtime: f.Created.Unix(), size: f.Size, read: func() (string, bool) {
					_, data, err := ix.src.Library.Open(f.ID)
					if err != nil {
						return "", false
					}
					return extract(name, data)
				}}
			}
		}
	}
	if ix.src.Folders != nil {
		count := 0
		for _, root := range ix.src.Folders() {
			root = filepath.Clean(root)
			if root == "/" || !filepath.IsAbs(root) {
				continue
			}
			filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil || ctx.Err() != nil {
					return fs.SkipDir
				}
				rel, _ := filepath.Rel(root, path)
				if d.IsDir() {
					if path != root && (secretName(d.Name()) || strings.HasPrefix(d.Name(), ".") || strings.Count(rel, string(filepath.Separator)) >= maxDepth) {
						return fs.SkipDir
					}
					return nil
				}
				if d.Type()&fs.ModeSymlink != 0 || secretName(d.Name()) || !textExt[strings.ToLower(filepath.Ext(d.Name()))] {
					return nil
				}
				info, err := d.Info()
				if err != nil || info.Size() > MaxFileBytes {
					st.Skipped++
					return nil
				}
				if count++; count > MaxFilesTotal {
					return fs.SkipAll
				}
				p := path
				want["path:"+p] = seen{kind: "folder", name: filepath.Join(filepath.Base(root), rel), mtime: info.ModTime().Unix(), size: info.Size(), read: func() (string, bool) {
					data, err := os.ReadFile(p)
					if err != nil {
						return "", false
					}
					return extract(p, data)
				}}
				return nil
			})
		}
	}
	if ix.src.Notes != nil {
		if t := strings.TrimSpace(ix.src.Notes()); t != "" {
			want[noteRef] = seen{kind: "note", name: "Talaria's notes", mtime: 0, size: int64(len(t)), read: func() (string, bool) { return t, true }}
		}
	}

	have := map[string][2]int64{} // ref -> mtime, size
	rows, err := ix.db.QueryContext(ctx, `SELECT ref, mtime, size FROM docs`)
	if err != nil {
		st.Error = err.Error()
		ix.last = st
		return st
	}
	for rows.Next() {
		var ref string
		var m, s int64
		rows.Scan(&ref, &m, &s)
		have[ref] = [2]int64{m, s}
	}
	rows.Close()

	refs := make([]string, 0, len(want))
	for ref := range want {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		if ctx.Err() != nil {
			break
		}
		w := want[ref]
		if h, ok := have[ref]; ok && h[0] == w.mtime && h[1] == w.size && ref != noteRef {
			count(&st, w.kind)
			continue
		}
		text, ok := w.read()
		if !ok {
			st.Skipped++
			ix.db.ExecContext(ctx, `DELETE FROM docs WHERE ref = ?`, ref)
			continue
		}
		_, err := ix.db.ExecContext(ctx, `INSERT INTO docs(ref, kind, name, mtime, size, content) VALUES (?,?,?,?,?,?)
			ON CONFLICT(ref) DO UPDATE SET kind=excluded.kind, name=excluded.name, mtime=excluded.mtime, size=excluded.size, content=excluded.content`,
			ref, w.kind, w.name, w.mtime, w.size, text)
		if err != nil {
			ix.logf("docsearch: %s: %v", ref, err)
			st.Error = err.Error()
			continue
		}
		count(&st, w.kind)
	}
	if ctx.Err() == nil {
		for ref := range have {
			if _, ok := want[ref]; !ok {
				ix.db.ExecContext(ctx, `DELETE FROM docs WHERE ref = ?`, ref)
			}
		}
	}
	ix.refreshed, ix.last = time.Now(), st
	return st
}

func count(st *Status, kind string) {
	switch kind {
	case "file":
		st.Files++
	case "folder":
		st.Folders++
	case "note":
		st.Notes = true
	}
}

// Hit is one result.
type Hit struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind"` // file (kept by Talaria), folder (one of the owner's) or note
	Name    string `json:"name"`
	Snippet string `json:"snippet"`
}

// Search finds the files and notes that match, best first. The query is plain words: every word must appear.
func (ix *Index) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	if limit <= 0 || limit > maxSnippetHits {
		limit = 6
	}
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return nil, errors.New("say what to look for")
	}
	for i, t := range terms {
		terms[i] = `"` + strings.ReplaceAll(t, `"`, "") + `"` // quoting sidesteps FTS5 operator syntax
	}
	if ix.stale() {
		ix.Refresh(ctx)
	}
	rows, err := ix.db.QueryContext(ctx, `SELECT d.ref, d.kind, d.name, snippet(docs_fts, 1, '[', ']', '…', 18)
		FROM docs_fts JOIN docs d ON d.id = docs_fts.rowid WHERE docs_fts MATCH ? ORDER BY bm25(docs_fts) LIMIT ?`, strings.Join(terms, " "), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Ref, &h.Kind, &h.Name, &h.Snippet); err != nil {
			return nil, err
		}
		h.Snippet = strings.Join(strings.Fields(h.Snippet), " ")
		out = append(out, h)
	}
	return out, rows.Err()
}

func (ix *Index) stale() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return time.Since(ix.refreshed) > staleAfter
}

// Read returns part of an indexed document, from the offset (in characters), with whether there is more.
func (ix *Index) Read(ctx context.Context, ref string, offset int) (name, text string, more bool, err error) {
	var content string
	if err = ix.db.QueryRowContext(ctx, `SELECT name, content FROM docs WHERE ref = ?`, ref).Scan(&name, &content); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", false, fmt.Errorf("nothing is indexed as %q: use a ref from docs_search", ref)
		}
		return "", "", false, err
	}
	r := []rune(content)
	if offset < 0 || offset > len(r) {
		offset = 0
	}
	end := min(offset+ReadChars, len(r))
	return name, string(r[offset:end]), end < len(r), nil
}
