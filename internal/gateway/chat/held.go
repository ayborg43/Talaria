package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HeldTTL is how long a request waits for an answer before it lapses.
const HeldTTL = 7 * 24 * time.Hour

// Held is a request for permission that waits for the user: the exact call the agent wanted to make, and what the
// user had asked for, so that once the user answers, even after Talaria has restarted, the call can be run (or dropped)
// and the work taken up again.
type Held struct {
	Token   string    `json:"token"`
	Chat    int64     `json:"chat"`
	Name    string    `json:"name"` // the tool as the user was told of it
	Tool    string    `json:"tool"` // the tool's own name
	Args    string    `json:"args"`
	Shown   string    `json:"shown,omitempty"` // what the user was asked about
	Why     string    `json:"why,omitempty"`
	Flagged bool      `json:"flagged,omitempty"`
	Task    string    `json:"task,omitempty"` // what the user had asked for when this came up
	At      time.Time `json:"at"`
}

// HeldStore keeps the requests that are waiting for an answer in a file, so they outlive a restart.
type HeldStore struct {
	Path string
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu     sync.Mutex
	loaded bool
	items  []Held
}

func (s *HeldStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// load reads the file once and drops what has lapsed. mu must be held.
func (s *HeldStore) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	if s.Path == "" {
		return
	}
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return
	}
	var f struct {
		Held []Held `json:"held"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	s.items = f.Held
	s.pruneLocked()
}

func (s *HeldStore) pruneLocked() {
	keep := s.items[:0]
	for _, h := range s.items {
		if s.now().Sub(h.At) < HeldTTL {
			keep = append(keep, h)
		}
	}
	s.items = keep
}

// save writes the file atomically. mu must be held.
func (s *HeldStore) save() error {
	if s.Path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(struct {
		Held []Held `json:"held"`
	}{s.items}, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Add keeps a request. It fails, and keeps nothing, if it cannot be written down.
func (s *HeldStore) Add(h Held) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.pruneLocked()
	if h.At.IsZero() {
		h.At = s.now()
	}
	s.items = append(s.items, h)
	if err := s.save(); err != nil {
		s.items = s.items[:len(s.items)-1]
		return err
	}
	return nil
}

// Has reports whether a token names a request that is still waiting.
func (s *HeldStore) Has(token string) bool {
	_, ok := s.Get(token)
	return ok
}

// Get finds a waiting request.
func (s *HeldStore) Get(token string) (Held, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.pruneLocked()
	for _, h := range s.items {
		if h.Token == token {
			return h, true
		}
	}
	return Held{}, false
}

// Take removes a request and returns it, once: a second answer finds nothing.
func (s *HeldStore) Take(token string) (Held, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.pruneLocked()
	for i, h := range s.items {
		if h.Token == token {
			s.items = append(s.items[:i:i], s.items[i+1:]...)
			s.save()
			return h, true
		}
	}
	return Held{}, false
}

// Same finds a request already waiting for the same call in a chat, so asking twice does not pile up questions.
func (s *HeldStore) Same(chat int64, tool, args string) (Held, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.pruneLocked()
	for _, h := range s.items {
		if h.Chat == chat && h.Tool == tool && h.Args == args {
			return h, true
		}
	}
	return Held{}, false
}

// List is every waiting request, oldest first.
func (s *HeldStore) List() []Held {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	s.pruneLocked()
	return append([]Held(nil), s.items...)
}
