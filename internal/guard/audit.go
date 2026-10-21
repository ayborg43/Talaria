package guard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxLog = 5 << 20
	maxArgsShown  = 1000 // runes of a call's arguments kept in the log
	maxResultKept = 300  // runes of its result
)

// Record is one line of the activity log.
type Record struct {
	Time    time.Time `json:"time"`
	Origin  string    `json:"origin"`            // "cli", "chat", or "job:<id>"
	Gateway string    `json:"gateway,omitempty"` // for a chat or a job: the service
	Chat    int64     `json:"chat,omitempty"`
	Tool    string    `json:"tool"`
	Args    string    `json:"args,omitempty"`
	Label   string    `json:"subagent,omitempty"`
	Allowed bool      `json:"allowed"`
	Via     string    `json:"via"`
	Rule    string    `json:"rule,omitempty"`
	Status  string    `json:"status"`
	Result  string    `json:"result,omitempty"`
	Bytes   int       `json:"bytes,omitempty"` // size of the full result
	Millis  int64     `json:"ms,omitempty"`
}

// Log is the activity log: one JSON object per line, in a file only its owner
// can read. When it passes MaxBytes it is moved to Path+".1", replacing the
// older copy, so it cannot grow without bound.
type Log struct {
	Path     string
	MaxBytes int64

	mu sync.Mutex
}

func (l *Log) maxBytes() int64 {
	if l.MaxBytes > 0 {
		return l.MaxBytes
	}
	return defaultMaxLog
}

// Append adds a record.
func (l *Log) Append(r Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return err
	}
	if fi, err := os.Stat(l.Path); err == nil && fi.Size() >= l.maxBytes() {
		os.Rename(l.Path, l.Path+".1")
	}
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// A crash can leave the last line unfinished; start on a fresh line so that
	// one damaged record does not take the next one with it.
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], fi.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// Filter narrows what Recent returns. Zero fields match everything.
type Filter struct {
	Gateway string
	Chat    int64
	Tool    string
	Denied  bool // only calls that did not run because they were refused
}

func (f Filter) matches(r Record) bool {
	switch {
	case f.Gateway != "" && r.Gateway != f.Gateway,
		f.Chat != 0 && r.Chat != f.Chat,
		f.Tool != "" && r.Tool != f.Tool,
		f.Denied && r.Allowed:
		return false
	}
	return true
}

// Recent returns the last n records that match, oldest first.
func (l *Log) Recent(f Filter, n int) ([]Record, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []Record
	for _, path := range []string{l.Path + ".1", l.Path} { // oldest first
		recs, err := readRecords(path)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			if f.matches(r) {
				all = append(all, r)
			}
		}
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

func readRecords(path string) ([]Record, error) {
	fh, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var out []Record
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil { // a half-written last line is skipped
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

// Format renders a record as one line for people.
func Format(r Record) string {
	verdict := "ran"
	switch {
	case !r.Allowed:
		verdict = "REFUSED"
	case r.Status == "error":
		verdict = "failed"
	}
	who := r.Tool
	if r.Label != "" {
		who += " [" + r.Label + "]"
	}
	via := r.Via
	if r.Rule != "" {
		via += ": " + r.Rule
	}
	place := r.Origin
	if r.Gateway != "" {
		chat := fmt.Sprintf("%s:%d", r.Gateway, r.Chat)
		if r.Origin == "chat" {
			place = chat
		} else {
			place += " for " + chat
		}
	}
	line := fmt.Sprintf("%s  %s  %s %s (%s)", r.Time.Local().Format("Jan 2 15:04:05"), place, who, verdict, via)
	if r.Millis > 0 {
		line += fmt.Sprintf(" %dms", r.Millis)
	}
	if a := oneLine(r.Args, 100); a != "" {
		line += "\n    " + a
	}
	return line
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
