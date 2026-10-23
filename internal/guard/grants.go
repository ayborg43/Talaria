package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"talaria/internal/filelock"
)

// Grants are the tools a user has chosen to always allow, per chat. They live
// in one small JSON file that is reread when it changes, so editing it, or
// `tal policy forget`, takes effect without a restart.
type Grants struct {
	Path string

	mu     sync.Mutex
	cached map[string][]string
	mtime  time.Time
	size   int64
}

type grantsFile struct {
	Grants map[string][]string `json:"grants"`
}

// load returns the current contents; the caller holds mu.
func (g *Grants) load() (map[string][]string, error) {
	fi, err := os.Stat(g.Path)
	if os.IsNotExist(err) {
		g.cached, g.mtime, g.size = map[string][]string{}, time.Time{}, 0
		return g.cached, nil
	}
	if err != nil {
		return nil, err
	}
	if g.cached != nil && fi.ModTime().Equal(g.mtime) && fi.Size() == g.size {
		return g.cached, nil
	}
	raw, err := os.ReadFile(g.Path)
	if err != nil {
		return nil, err
	}
	var f grantsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt (fix or delete it): %w", g.Path, err)
	}
	if f.Grants == nil {
		f.Grants = map[string][]string{}
	}
	g.cached, g.mtime, g.size = f.Grants, fi.ModTime(), fi.Size()
	return g.cached, nil
}

// Allowed reports whether the chat has always allowed the tool. A file that
// cannot be read grants nothing: the fail-safe answer is to ask.
func (g *Grants) Allowed(chat, tool string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, err := g.load()
	return err == nil && slices.Contains(m[chat], tool)
}

// List returns the tools a chat has always allowed.
func (g *Grants) List(chat string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, _ := g.load()
	return slices.Clone(m[chat])
}

// Chats returns every chat that has grants, with its tools.
func (g *Grants) Chats() map[string][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, _ := g.load()
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = slices.Clone(v)
	}
	return out
}

// Add records that the chat always allows the tool.
func (g *Grants) Add(chat, tool string) error {
	return g.edit(func(m map[string][]string) {
		if !slices.Contains(m[chat], tool) {
			m[chat] = append(m[chat], tool)
			slices.Sort(m[chat])
		}
	})
}

// Reset removes a chat's grants and says how many there were.
func (g *Grants) Reset(chat string) (int, error) {
	n := 0
	err := g.edit(func(m map[string][]string) {
		n = len(m[chat])
		delete(m, chat)
	})
	return n, err
}

func (g *Grants) edit(change func(map[string][]string)) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(g.Path), 0o700); err != nil {
		return err
	}
	// The CLI (`tal policy forget`) and the gateway share this file; without
	// the inter-process lock two edits at once silently drop one of them.
	unlock, err := filelock.Lock(g.Path)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := g.load()
	if err != nil {
		return err
	}
	next := make(map[string][]string, len(m))
	for k, v := range m {
		next[k] = slices.Clone(v)
	}
	change(next)

	raw, err := json.MarshalIndent(grantsFile{next}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(g.Path), "grants-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), g.Path); err != nil {
		return err
	}
	g.cached = nil // reread, so the cache matches the file's new time
	return nil
}
