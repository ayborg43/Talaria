// Package memory holds Talaria's small, always-in-prompt notes (MEMORY.md and
// USER.md) and the searchable session history.
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Target selects which note file an operation applies to.
type Target string

const (
	Agent Target = "memory" // MEMORY.md: the agent's own notes
	User  Target = "user"   // USER.md: what the agent knows about the user
)

// Entry is one note. Date is the day it was written ("2006-01-02"), empty for
// a note added by hand or migrated from an older file that carried no dates.
// Source is where it came from when that is known: the session that wrote it,
// or "you" for a hand edit. Both let a later pass drop a stale or superseded
// note in favour of a newer one, and let the person see what changed and undo it.
type Entry struct {
	Date   string
	Source string
	Text   string
}

// Line renders the entry as it is kept in the file: the date, then the source
// in a "^"-marked word, then the note. A note written by hand may have neither.
func (e Entry) Line() string {
	text := oneLine(e.Text)
	src := sourceWord(e.Source)
	switch {
	case e.Date != "" && src != "":
		return e.Date + " ^" + src + " " + text
	case e.Date != "":
		return e.Date + " " + text
	case src != "":
		return "^" + src + " " + text
	}
	return text
}

// Age says how old the note is in words a person reads at a glance ("3 weeks
// ago"), and "" when it carries no date.
func (e Entry) Age() string { return ageWords(e.Date) }

// caps keep the prompt small; the agent must consolidate to stay under them.
var caps = map[Target]int{Agent: 2200, User: 1375}

var files = map[Target]string{Agent: "MEMORY.md", User: "USER.md"}

// Notes persists entries for both targets under Dir. Changes are recorded in
// Log (if set) so the user can see, and undo, what the agent recently learned.
type Notes struct {
	Dir string
	Log *Log
}

func (n *Notes) logPath(t Target) string { return filepath.Join(n.Dir, files[t]) }

// today is the date stamped onto a note, in the server's own time zone.
func today() string { return time.Now().Format("2006-01-02") }

// oneLine collapses an entry to a single line: the files hold one note per
// line, so a date prefix can sit in front of it without ambiguity.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "§", "")
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
}

// sourceWord makes a session id or "you" safe to sit in a line: one word, no
// control characters, and short enough not to crowd the note it marks.
func sourceWord(s string) string {
	s = strings.Join(strings.Fields(s), "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func checkTarget(t Target) error {
	if _, ok := caps[t]; !ok {
		return fmt.Errorf("unknown target %q: use %q or %q", t, Agent, User)
	}
	return nil
}

// ageWords turns a stored date into how long ago it was, in the largest unit
// that still reads as a date: "today", "yesterday", "3 weeks ago".
func ageWords(date string) string {
	if date == "" {
		return ""
	}
	d, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return ""
	}
	days := int(time.Since(d).Hours() / 24)
	switch {
	case days <= 0:
		return "today"
	case days == 1:
		return "yesterday"
	case days < 14:
		return fmt.Sprintf("%d days ago", days)
	case days < 60:
		return fmt.Sprintf("%d weeks ago", days/7)
	case days < 730:
		return fmt.Sprintf("%d months ago", days/30)
	default:
		return fmt.Sprintf("%d years ago", days/365)
	}
}

// parseLine reads one line of the current format: an optional leading date, an
// optional "^source" word, then the note itself. A line with neither is a
// hand-written note.
func parseLine(line string) Entry {
	line = strings.TrimSpace(line)
	if len(line) >= 11 && line[4] == '-' && line[7] == '-' && line[10] == ' ' {
		if _, err := time.Parse("2006-01-02", line[:10]); err == nil {
			e := Entry{Date: line[:10]}
			line = strings.TrimSpace(line[10:])
			if strings.HasPrefix(line, "^") {
				src, rest, _ := strings.Cut(line, " ")
				e.Source, line = strings.TrimPrefix(src, "^"), strings.TrimSpace(rest)
			}
			e.Text = line
			return e
		}
	}
	if strings.HasPrefix(line, "^") {
		src, rest, _ := strings.Cut(line, " ")
		return Entry{Source: strings.TrimPrefix(src, "^"), Text: strings.TrimSpace(rest)}
	}
	return Entry{Text: line}
}

// load reads the entries of one target. Files from before the dates existed
// (notes separated by "§", possibly over several lines) are read as one note
// each, with no date.
func (n *Notes) load(t Target) ([]Entry, error) {
	raw, err := os.ReadFile(n.logPath(t))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	s := string(raw)
	var out []Entry
	if strings.Contains(s, "§") { // the old format
		for _, chunk := range strings.Split(s, "§") {
			if text := oneLine(chunk); text != "" {
				out = append(out, Entry{Text: text})
			}
		}
		return out, nil
	}
	for _, line := range strings.Split(s, "\n") {
		if e := parseLine(line); e.Text != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

// save writes the entries, one per line, dated and sourced where that is known.
func (n *Notes) save(t Target, entries []Entry) error {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.Line())
	}
	text := strings.Join(lines, "\n")
	if len(text) > caps[t] {
		return fmt.Errorf("%s would be %d characters, over the %d limit: replace or remove entries to make room",
			files[t], len(text), caps[t])
	}
	if err := os.MkdirAll(n.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(n.logPath(t), []byte(text), 0o600)
}

func (n *Notes) record(t Target, action, before, text, source string) {
	if n.Log == nil {
		return
	}
	n.Log.Record(Change{At: time.Now(), Target: string(t), Action: action, Before: before, Text: text, Source: source})
}

// Add appends a dated note; an exact duplicate is a no-op.
func (n *Notes) Add(t Target, text, source string) error {
	if err := checkTarget(t); err != nil {
		return err
	}
	text = oneLine(text)
	if text == "" {
		return errors.New("content is empty")
	}
	entries, err := n.load(t)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.EqualFold(e.Text, text) {
			return nil
		}
	}
	if err := n.save(t, append(entries, Entry{Date: today(), Source: source, Text: text})); err != nil {
		return err
	}
	n.record(t, "add", "", text, source)
	return nil
}

// find returns the index of the single entry whose text contains substr.
func find(entries []Entry, substr string) (int, error) {
	if strings.TrimSpace(substr) == "" {
		return 0, errors.New("old text is empty")
	}
	idx := -1
	for i, e := range entries {
		if strings.Contains(e.Text, substr) {
			if idx >= 0 {
				return 0, fmt.Errorf("%q matches more than one entry; use a longer snippet", substr)
			}
			idx = i
		}
	}
	if idx < 0 {
		return 0, fmt.Errorf("no entry contains %q", substr)
	}
	return idx, nil
}

// Replace swaps the one entry containing old for text, and restamps it today.
func (n *Notes) Replace(t Target, old, text, source string) error {
	return n.rewrite(t, old, text, source, "change")
}

// Supersede is a Replace that says the older note is no longer true, rather
// than merely corrected: consolidation uses it when a later conversation
// contradicts an earlier one. The trail keeps what it said, so it can be undone.
func (n *Notes) Supersede(t Target, old, text, source string) error {
	return n.rewrite(t, old, text, source, "supersede")
}

func (n *Notes) rewrite(t Target, old, text, source, action string) error {
	if err := checkTarget(t); err != nil {
		return err
	}
	text = oneLine(text)
	if text == "" {
		return errors.New("content is empty")
	}
	entries, err := n.load(t)
	if err != nil {
		return err
	}
	i, err := find(entries, old)
	if err != nil {
		return err
	}
	before := entries[i].Text
	entries[i] = Entry{Date: today(), Source: source, Text: text}
	if err := n.save(t, entries); err != nil {
		return err
	}
	n.record(t, action, before, text, source)
	return nil
}

// Remove deletes the one entry containing old.
func (n *Notes) Remove(t Target, old, source string) error {
	if err := checkTarget(t); err != nil {
		return err
	}
	entries, err := n.load(t)
	if err != nil {
		return err
	}
	i, err := find(entries, old)
	if err != nil {
		return err
	}
	before := entries[i].Text
	if err := n.save(t, append(entries[:i], entries[i+1:]...)); err != nil {
		return err
	}
	n.record(t, "remove", before, "", source)
	return nil
}

// Usage describes how full a target is, for the model to manage space.
func (n *Notes) Usage(t Target) string {
	entries, _ := n.load(t)
	used := 0
	for _, e := range entries {
		used += len(e.Line()) + 1
	}
	return fmt.Sprintf("%d/%d chars", used, caps[t])
}

// Prompt renders both files for the system prompt. It is read once at session
// start, so edits made during a session show up in the next one. Each note says
// how long ago it was learned, so a newer note visibly outranks an older one.
func (n *Notes) Prompt() string {
	var b strings.Builder
	b.WriteString(n.profilePrompt())
	for _, t := range []Target{Agent, User} {
		entries, _ := n.load(t)
		title := map[Target]string{Agent: "Your notes (MEMORY.md)", User: "About the user (USER.md)"}[t]
		fmt.Fprintf(&b, "%s [%s]:\n", title, n.Usage(t))
		if len(entries) == 0 {
			b.WriteString("(empty)\n")
		}
		for _, e := range entries {
			if age := e.Age(); age != "" {
				fmt.Fprintf(&b, "- %s (learned %s)\n", e.Text, age)
			} else {
				fmt.Fprintf(&b, "- %s\n", e.Text)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Changes lists the most recent note edits, newest first, for the "recently
// learned" list the person reviews and undoes from.
func (n *Notes) Changes(limit int) []Change { return n.Log.Recent(limit) }

// Undo puts one recorded change back the way it was, and records the undo so
// the trail still shows both. It refuses when the note has moved on since: an
// undo aimed at one edit must never land on another.
func (n *Notes) Undo(id string) error {
	if n.Log == nil {
		return errors.New("there is no record of changes here")
	}
	c, ok := n.Log.Find(id)
	if !ok {
		return errors.New("that change is no longer in the record")
	}
	if err := checkTarget(Target(c.Target)); err != nil {
		return err
	}
	t := Target(c.Target)
	switch c.Action {
	case "add":
		if err := n.Remove(t, c.Text, "you"); err != nil {
			return err
		}
	case "change", "supersede":
		entries, err := n.load(t)
		if err != nil {
			return err
		}
		i := exact(entries, c.Text)
		if i < 0 {
			return errors.New("the note has changed since, so there is nothing to put back")
		}
		entries[i] = Entry{Date: today(), Source: "you", Text: c.Before}
		if err := n.save(t, entries); err != nil {
			return err
		}
		n.record(t, "undo", c.Text, c.Before, "you")
	case "remove":
		entries, err := n.load(t)
		if err != nil {
			return err
		}
		if exact(entries, c.Before) >= 0 {
			return errors.New("the note is already back")
		}
		if err := n.save(t, append(entries, Entry{Date: today(), Source: "you", Text: c.Before})); err != nil {
			return err
		}
		n.record(t, "undo", "", c.Before, "you")
	default:
		return errors.New("that change cannot be undone")
	}
	return nil
}

// exact returns the index of the entry whose text is exactly s, or -1.
func exact(entries []Entry, s string) int {
	for i, e := range entries {
		if e.Text == strings.TrimSpace(s) {
			return i
		}
	}
	return -1
}
