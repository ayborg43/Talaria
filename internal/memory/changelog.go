package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// maxChanges bounds the "recently learned" list, so it never grows without end.
const maxChanges = 200

// Change is one edit to the notes, kept so the user can see — and undo — what
// the agent has learned. It is written as one JSON line per change.
type Change struct {
	ID     string    `json:"id,omitempty"`
	At     time.Time `json:"at"`
	Target string    `json:"target"`           // user or memory
	Action string    `json:"action"`           // add, change, supersede, remove or undo
	Before string    `json:"before,omitempty"` // what the note said before, for undo
	Text   string    `json:"text,omitempty"`   // what it says now
	Source string    `json:"source,omitempty"` // the conversation that made it, "consolidation", or "you"
}

// Log keeps the tail of the note changes, newest first when read.
type Log struct {
	path string
}

// OpenLog opens the change log at path; it is created on first write.
func OpenLog(path string) *Log { return &Log{path: path} }

// Path is where the log lives, for tests and for the app to show.
func (l *Log) Path() string { return l.path }

// Record appends one change and trims the log to the last maxChanges.
func (l *Log) Record(c Change) {
	if l == nil || l.path == "" {
		return
	}
	if c.At.IsZero() {
		c.At = time.Now()
	}
	if c.ID == "" {
		c.ID = newID()
	}
	line, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return
	}
	f.Close()
	l.trim()
}

// newID names one change so an undo can point at it, without depending on the
// text having stayed the same.
func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102-150405.000000000")
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

func (l *Log) trim() {
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	lines := splitNonEmpty(string(raw))
	if len(lines) <= maxChanges {
		return
	}
	keep := lines[len(lines)-maxChanges:]
	_ = os.WriteFile(l.path, []byte(joinLines(keep)), 0o600)
}

// Find returns the change with that id, if it is still in the log.
func (l *Log) Find(id string) (Change, bool) {
	for _, c := range l.Recent(maxChanges) {
		if c.ID == id {
			return c, true
		}
	}
	return Change{}, false
}

// Recent returns up to n changes, newest first.
func (l *Log) Recent(n int) []Change {
	if l == nil || l.path == "" {
		return nil
	}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return nil
	}
	lines := splitNonEmpty(string(raw))
	if n <= 0 {
		n = maxChanges
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]Change, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		var c Change
		if err := json.Unmarshal([]byte(lines[i]), &c); err == nil {
			out = append(out, c)
		}
	}
	return out
}

func splitNonEmpty(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if line := s[start:i]; line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	return out
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
