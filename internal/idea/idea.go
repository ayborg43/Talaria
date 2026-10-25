// Package idea keeps suggestions with source evidence: each idea names where
// it came from, and Suggest leaves out what was already said or done. Sent
// replies and completed matching work are excluded, and repeats collapse to
// one, so the user is never asked about the same thing twice.
package idea

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, shared with the sessions history
)

// Status of an idea.
const (
	Open      = "open"
	Accepted  = "accepted"
	Edited    = "edited"
	Dismissed = "dismissed"
	Done      = "done"
)

// Limits keep one chat's suggestion list bounded.
const (
	MaxOpenPerChat = 20
	MaxTitleRunes  = 200
	MaxBodyRunes   = 2000
	MaxSources     = 5
)

// Source is one piece of evidence behind an idea.
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Idea is one suggestion.
type Idea struct {
	ID      string   `json:"id"`
	Gateway string   `json:"gateway"`
	ChatID  int64    `json:"chat_id"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Sources []Source `json:"sources"`
	Status  string   `json:"status"`

	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Store keeps ideas in a SQLite table, reread on every call. Path names the
// shared SQLite file; the ideas table is created on first use. If the table
// is empty and a legacy ideas.json sits beside the database, it is imported
// once and renamed to ideas.json.migrated.
type Store struct {
	Path string
	Now  func() time.Time

	mu sync.Mutex
}

const ideaSchema = `
CREATE TABLE IF NOT EXISTS ideas(
	id TEXT PRIMARY KEY,
	gateway TEXT NOT NULL,
	chat_id INTEGER NOT NULL,
	status TEXT NOT NULL,
	created INTEGER NOT NULL,
	updated INTEGER NOT NULL,
	data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ideas_chat ON ideas(gateway, chat_id);
`

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func norm(s string) string {
	return strings.ToLower(squash(s))
}

// stopwords ignored when deciding whether a reply or a done item already
// covers an idea's title.
var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "to": true,
	"for": true, "you": true, "your": true, "my": true, "me": true,
	"i": true, "it": true, "on": true, "in": true,
}

func keywords(s string) []string {
	out := []string{}
	for _, w := range strings.Fields(s) {
		if len([]rune(w)) <= 2 || stopwords[w] {
			continue
		}
		out = append(out, w)
	}
	return out
}

// covers reports whether hay already says title: every significant word of
// the title appears in the hay ("summarized the inbox" covers "summarize the
// inbox"). A title with no significant words never matches.
func covers(hay, title string) bool {
	words := keywords(title)
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func validSource(src Source) error {
	title := squash(src.Title)
	if title == "" || len([]rune(title)) > MaxTitleRunes {
		return errors.New("each source needs a short title")
	}
	u, err := url.Parse(strings.TrimSpace(src.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("each source URL must use HTTP or HTTPS")
	}
	return nil
}

func (s *Store) db() (*sql.DB, error) {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Created owner-only before SQLite does it with the default mode.
	if f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		f.Close()
	}
	os.Chmod(s.Path, 0o600)
	db, err := sql.Open("sqlite", s.Path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(ideaSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.importLegacy(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Store) legacyPath() string {
	return filepath.Join(filepath.Dir(s.Path), "ideas.json")
}

func (s *Store) importLegacy(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ideas`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return nil
	}
	leg := s.legacyPath()
	if leg == s.Path {
		return nil
	}
	raw, err := os.ReadFile(leg)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var f struct {
		Ideas []Idea `json:"ideas"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("%s is corrupt (fix or delete it): %w", leg, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, it := range f.Ideas {
		if err := insertIdea(tx.Exec, it); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return os.Rename(leg, leg+".migrated")
}

func insertIdea(exec func(query string, args ...any) (sql.Result, error), it Idea) error {
	raw, err := json.Marshal(it)
	if err != nil {
		return err
	}
	_, err = exec(`INSERT OR REPLACE INTO ideas(id, gateway, chat_id, status, created, updated, data) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		it.ID, it.Gateway, it.ChatID, it.Status, it.Created.UnixNano(), it.Updated.UnixNano(), string(raw))
	return err
}

func (s *Store) load() ([]Idea, error) {
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT data FROM ideas`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Idea
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var it Idea
		if err := json.Unmarshal([]byte(raw), &it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) save(ideas []Idea) error {
	db, err := s.db()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ideas`); err != nil {
		tx.Rollback()
		return err
	}
	for _, it := range ideas {
		if err := insertIdea(tx.Exec, it); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Add keeps a new suggestion with its evidence. It refuses ideas without a
// source, so every suggestion can point at where it came from.
func (s *Store) Add(gateway string, chatID int64, title, body string, sources []Source) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	title, body = squash(title), strings.TrimSpace(body)
	switch {
	case title == "":
		return "", errors.New("an idea needs a title")
	case len([]rune(title)) > MaxTitleRunes:
		return "", errors.New("the title is too long (200 characters at most)")
	case len([]rune(body)) > MaxBodyRunes:
		return "", errors.New("the body is too long (2000 characters at most)")
	case len(sources) == 0:
		return "", errors.New("an idea needs at least one source")
	case len(sources) > MaxSources:
		return "", errors.New("an idea holds at most 5 sources")
	}
	for _, src := range sources {
		if err := validSource(src); err != nil {
			return "", err
		}
	}
	ideas, err := s.load()
	if err != nil {
		return "", err
	}
	open := 0
	for _, it := range ideas {
		if it.Gateway == gateway && it.ChatID == chatID && it.Status == Open {
			open++
		}
	}
	if open >= MaxOpenPerChat {
		return "", errors.New("too many open ideas; accept or dismiss some first")
	}
	now := s.now()
	it := Idea{ID: newID(), Gateway: gateway, ChatID: chatID, Title: title, Body: body, Sources: sources, Status: Open, Created: now, Updated: now}
	ideas = append(ideas, it)
	if err := s.save(ideas); err != nil {
		return "", err
	}
	return it.ID, nil
}

func (s *Store) update(gateway string, chatID int64, id string, fn func(*Idea) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ideas, err := s.load()
	if err != nil {
		return err
	}
	for i := range ideas {
		if ideas[i].ID != id {
			continue
		}
		if ideas[i].Gateway != gateway || ideas[i].ChatID != chatID {
			return errors.New("no such idea")
		}
		if err := fn(&ideas[i]); err != nil {
			return err
		}
		ideas[i].Updated = s.now()
		return s.save(ideas)
	}
	return errors.New("no such idea")
}

// Accept marks an idea accepted: the user wants it.
func (s *Store) Accept(gateway string, chatID int64, id string) error {
	return s.update(gateway, chatID, id, func(it *Idea) error {
		if it.Status != Open && it.Status != Edited {
			return errors.New("only an open idea can be accepted")
		}
		it.Status = Accepted
		return nil
	})
}

// Edit changes an open idea's title and body, keeping its sources.
func (s *Store) Edit(gateway string, chatID int64, id, title, body string) error {
	title, body = squash(title), strings.TrimSpace(body)
	if title == "" {
		return errors.New("an idea needs a title")
	}
	if len([]rune(title)) > MaxTitleRunes {
		return errors.New("the title is too long (200 characters at most)")
	}
	if len([]rune(body)) > MaxBodyRunes {
		return errors.New("the body is too long (2000 characters at most)")
	}
	return s.update(gateway, chatID, id, func(it *Idea) error {
		if it.Status != Open && it.Status != Edited {
			return errors.New("only an open idea can be edited")
		}
		it.Title, it.Body, it.Status = title, body, Edited
		return nil
	})
}

// Dismiss drops an idea; it is never suggested again.
func (s *Store) Dismiss(gateway string, chatID int64, id string) error {
	return s.update(gateway, chatID, id, func(it *Idea) error {
		it.Status = Dismissed
		return nil
	})
}

// Suggest returns the chat's open ideas, newest first, leaving out what was
// already said or done: an idea whose title matches a sent reply or a done
// piece of work is excluded, and repeats of one idea collapse to the newest.
func (s *Store) Suggest(gateway string, chatID int64, sentReplies, doneWork []string) []Idea {
	s.mu.Lock()
	defer s.mu.Unlock()
	ideas, err := s.load()
	if err != nil {
		return nil
	}
	sent := make([]string, 0, len(sentReplies))
	for _, r := range sentReplies {
		sent = append(sent, norm(r))
	}
	done := make([]string, 0, len(doneWork))
	for _, d := range doneWork {
		done = append(done, norm(d))
	}
	seen := map[string]bool{}
	out := []Idea{}
	// Newest first, so the first of a repeated title wins.
	for i := len(ideas) - 1; i >= 0; i-- {
		it := ideas[i]
		if it.Gateway != gateway || it.ChatID != chatID || (it.Status != Open && it.Status != Edited) {
			continue
		}
		n := norm(it.Title)
		if seen[n] {
			continue
		}
		seen[n] = true
		excluded := false
		for _, r := range sent {
			if r != "" && covers(r, n) {
				excluded = true
				break
			}
		}
		for _, d := range done {
			if !excluded && d != "" && (d == n || covers(d, n) || covers(n, d)) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		out = append(out, it)
	}
	return out
}
