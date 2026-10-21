package memory

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // pure-Go driver, FTS5 included
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions(
	id TEXT PRIMARY KEY,
	started_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS messages(
	id INTEGER PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id),
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	ts INTEGER NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(content, content='messages', content_rowid='id');
CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, content) VALUES (new.id, new.content);
END;
`

// Sessions is the full-text-searchable history of past conversations.
type Sessions struct {
	db *sql.DB
}

// Hit is one search result.
type Hit struct {
	SessionID string
	Role      string
	When      time.Time
	Snippet   string
}

func OpenSessions(path string) (*Sessions, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Sessions{db: db}, nil
}

func (s *Sessions) Close() error { return s.db.Close() }

// Start records a new session and returns its id.
func (s *Sessions) Start() (string, error) {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	id := now.Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
	_, err := s.db.Exec(`INSERT INTO sessions(id, started_at) VALUES (?, ?)`, id, now.Unix())
	return id, err
}

// Add stores one message. Empty content is ignored.
func (s *Sessions) Add(sessionID, role, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO messages(session_id, role, content, ts) VALUES (?, ?, ?, ?)`,
		sessionID, role, content, time.Now().Unix())
	return err
}

// Search finds messages matching any word in query, best matches first,
// skipping the session named by exclude (usually the live one).
func (s *Sessions) Search(query string, limit int, exclude string) ([]Hit, error) {
	terms := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(terms) == 0 {
		return nil, errors.New("query has no searchable words")
	}
	for i, t := range terms {
		terms[i] = `"` + t + `"` // quoting sidesteps FTS5 operator syntax
	}
	if limit <= 0 || limit > 20 {
		limit = 5
	}

	rows, err := s.db.Query(`
		SELECT m.session_id, m.role, m.ts, snippet(messages_fts, 0, '[', ']', '…', 24)
		FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid
		WHERE messages_fts MATCH ? AND m.session_id != ?
		ORDER BY bm25(messages_fts) LIMIT ?`,
		strings.Join(terms, " OR "), exclude, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var h Hit
		var ts int64
		if err := rows.Scan(&h.SessionID, &h.Role, &ts, &h.Snippet); err != nil {
			return nil, err
		}
		h.When = time.Unix(ts, 0)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}
