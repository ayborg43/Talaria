// Package persona keeps the named agents a person has set up: a name, what it is for and how it should behave, the
// model it uses, and whether it may only look. A conversation with a named agent is the ordinary chat with that
// agent's instructions added to the model's, its own model profile if it has one, and, if it is set to look only, no
// tool that needs approval. Nothing here gives an agent more power than the chat has; it can only narrow.
package persona

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Limits.
const (
	MaxAgents       = 8
	MaxName         = 30
	MaxIcon         = 8
	MaxInstructions = 1500
	MaxProfile      = 60
)

// Agent is one named agent.
type Agent struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Icon         string    `json:"icon,omitempty"`
	Color        string    `json:"color,omitempty"`
	Instructions string    `json:"instructions"`
	Profile      string    `json:"profile,omitempty"`   // the model profile it uses; empty follows the chat's
	ReadOnly     bool      `json:"read_only,omitempty"` // it may only look: nothing that needs approval is available to it
	Created      time.Time `json:"created"`
}

func line(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Check cleans an agent and says what is wrong with it. Colour is checked by the caller, which knows the names.
func Check(a Agent) (Agent, error) {
	a.Name, a.Icon, a.Profile = line(a.Name, MaxName+1), line(a.Icon, MaxIcon+1), line(a.Profile, MaxProfile+1)
	a.Instructions = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < ' ' && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, a.Instructions))
	switch {
	case a.Name == "":
		return a, errors.New("give the agent a name")
	case len([]rune(a.Name)) > MaxName:
		return a, fmt.Errorf("the name is longer than %d characters", MaxName)
	case len([]rune(a.Icon)) > MaxIcon:
		return a, errors.New("the icon is one emoji or a couple of letters")
	case a.Instructions == "":
		return a, errors.New("say what it is for and how it should behave")
	case len([]rune(a.Instructions)) > MaxInstructions:
		return a, fmt.Errorf("the instructions are longer than %d characters", MaxInstructions)
	case len([]rune(a.Profile)) > MaxProfile:
		return a, errors.New("that model profile name is too long")
	}
	return a, nil
}

// System is what the model is told about this agent, added after its ordinary instructions. The user wrote it, so it
// is as trusted as the user's own words; what it adds can only narrow the agent, never lift a limit.
func (a Agent) System() string {
	var b strings.Builder
	fmt.Fprintf(&b, "In this conversation you are %s, one of the user's named agents. The user wrote your instructions:\n\n%s\n\n", a.Name, a.Instructions)
	b.WriteString("Stay in this role. The user's safety rules and approvals are unchanged by it: nothing above lets you do what you would otherwise have to ask about.")
	if a.ReadOnly {
		b.WriteString(" You are set to look only: anything that needs approval is refused here, so do not try; say what you found and what you would do.")
	}
	return b.String()
}

// Store keeps the agents in a file readable by the owner only.
type Store struct {
	Path string

	mu sync.Mutex
}

func (s *Store) load() ([]Agent, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Agent
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("agents file is not JSON: %w", err)
	}
	return out, nil
}

func (s *Store) save(as []Agent) error {
	raw, err := json.Marshal(as)
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

func newID(existing []Agent) string {
	for {
		b := make([]byte, 3)
		rand.Read(b)
		id := "a" + hex.EncodeToString(b)
		if !slices.ContainsFunc(existing, func(a Agent) bool { return a.ID == id }) {
			return id
		}
	}
}

// Save adds an agent, or, with its ID set, replaces the one with that ID.
func (s *Store) Save(a Agent) (Agent, error) {
	a, err := Check(a)
	if err != nil {
		return a, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return a, err
	}
	for _, o := range all {
		if o.ID != a.ID && strings.EqualFold(o.Name, a.Name) {
			return a, fmt.Errorf("there is already an agent called %s", o.Name)
		}
	}
	if a.ID != "" {
		for i := range all {
			if all[i].ID == a.ID {
				a.Created = all[i].Created
				all[i] = a
				return a, s.save(all)
			}
		}
		return a, errors.New("there is no such agent")
	}
	if len(all) >= MaxAgents {
		return a, fmt.Errorf("there are already %d agents (the most is %d): remove one first", len(all), MaxAgents)
	}
	a.ID, a.Created = newID(all), time.Now()
	return a, s.save(append(all, a))
}

// List is every agent, oldest first.
func (s *Store) List() []Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, _ := s.load()
	return all
}

// Get finds one agent.
func (s *Store) Get(id string) (Agent, bool) {
	for _, a := range s.List() {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// Remove deletes an agent.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	keep := all[:0]
	for _, a := range all {
		if a.ID != id {
			keep = append(keep, a)
		}
	}
	if len(keep) == len(all) {
		return errors.New("there is no such agent")
	}
	return s.save(keep)
}
