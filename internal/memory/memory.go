// Package memory holds Talaria's small, always-in-prompt notes (MEMORY.md and
// USER.md) and the searchable session history.
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Target selects which note file an operation applies to.
type Target string

const (
	Agent Target = "memory" // MEMORY.md: the agent's own notes
	User  Target = "user"   // USER.md: what the agent knows about the user
)

const sep = "\n§\n"

// Caps keep the prompt small; the agent must consolidate to stay under them.
var caps = map[Target]int{Agent: 2200, User: 1375}

var files = map[Target]string{Agent: "MEMORY.md", User: "USER.md"}

// Notes persists entries for both targets under Dir.
type Notes struct {
	Dir string
}

func (n *Notes) load(t Target) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(n.Dir, files[t]))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range strings.Split(string(raw), sep) {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out, nil
}

func (n *Notes) save(t Target, entries []string) error {
	text := strings.Join(entries, sep)
	if len(text) > caps[t] {
		return fmt.Errorf("%s would be %d characters, over the %d limit: replace or remove entries to make room",
			files[t], len(text), caps[t])
	}
	if err := os.MkdirAll(n.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(n.Dir, files[t]), []byte(text), 0o600)
}

func checkTarget(t Target) error {
	if _, ok := caps[t]; !ok {
		return fmt.Errorf("unknown target %q: use %q or %q", t, Agent, User)
	}
	return nil
}

// Add appends an entry; an exact duplicate is a no-op.
func (n *Notes) Add(t Target, text string) error {
	if err := checkTarget(t); err != nil {
		return err
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "§", ""))
	if text == "" {
		return errors.New("content is empty")
	}
	entries, err := n.load(t)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e == text {
			return nil
		}
	}
	return n.save(t, append(entries, text))
}

// find returns the index of the single entry containing substr.
func find(entries []string, substr string) (int, error) {
	if strings.TrimSpace(substr) == "" {
		return 0, errors.New("old text is empty")
	}
	idx := -1
	for i, e := range entries {
		if strings.Contains(e, substr) {
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

// Replace swaps the one entry containing old for text.
func (n *Notes) Replace(t Target, old, text string) error {
	if err := checkTarget(t); err != nil {
		return err
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "§", ""))
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
	entries[i] = text
	return n.save(t, entries)
}

// Remove deletes the one entry containing old.
func (n *Notes) Remove(t Target, old string) error {
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
	return n.save(t, append(entries[:i], entries[i+1:]...))
}

// Usage describes how full a target is, for the model to manage space.
func (n *Notes) Usage(t Target) string {
	entries, _ := n.load(t)
	return fmt.Sprintf("%d/%d chars", len(strings.Join(entries, sep)), caps[t])
}

// Prompt renders both files for the system prompt. It is read once at session
// start, so edits made during a session show up in the next one.
func (n *Notes) Prompt() string {
	var b strings.Builder
	for _, t := range []Target{Agent, User} {
		entries, _ := n.load(t)
		title := map[Target]string{Agent: "Your notes (MEMORY.md)", User: "About the user (USER.md)"}[t]
		fmt.Fprintf(&b, "%s [%s]:\n", title, n.Usage(t))
		if len(entries) == 0 {
			b.WriteString("(empty)\n")
		}
		for _, e := range entries {
			fmt.Fprintf(&b, "- %s\n", e)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
