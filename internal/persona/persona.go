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
	"unicode"
)

// Limits.
const (
	MaxName         = 30
	MaxIcon         = 8
	MaxInstructions = 1500
	MaxProfile      = 60
	MaxDescription  = 120
)

// ScopeGroups are the tool areas an agent can be limited to. An agent with no scopes may use them all (subject
// to ReadOnly); naming some leaves only those areas, plus the tools that belong to no area (telling the time,
// remembering, listing models), which are always available.
var ScopeGroups = []string{"web", "files", "shell", "browser", "mail", "calendar", "phone", "docs", "social", "apps", "finance"}

// ToolGroup says which scope area a tool belongs to, or "" for a core tool no scope turns off.
func ToolGroup(name string) string {
	switch {
	case name == "web_search" || name == "web_fetch":
		return "web"
	case name == "read_file" || name == "list_dir" || name == "write_file" || strings.HasPrefix(name, "files_") || strings.HasPrefix(name, "pdf_form_"):
		return "files"
	case name == "shell":
		return "shell"
	case strings.HasPrefix(name, "browser_"):
		return "browser"
	case strings.HasPrefix(name, "email_"):
		return "mail"
	case strings.HasPrefix(name, "calendar_"):
		return "calendar"
	case strings.HasPrefix(name, "phone_"):
		return "phone"
	case name == "docs_search" || name == "docs_read" || name == "session_search":
		return "docs"
	case strings.HasPrefix(name, "x_") || name == "telegram_notify":
		return "social"
	case strings.HasPrefix(name, "finance_"):
		return "finance"
	case strings.HasPrefix(name, "mcp_"):
		return "apps"
	}
	return ""
}

// Agent is one named agent.
type Agent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Icon         string   `json:"icon,omitempty"`
	Color        string   `json:"color,omitempty"`
	Description  string   `json:"description,omitempty"` // one line saying when to choose this agent; empty falls back to its instructions
	Instructions string   `json:"instructions"`
	Profile      string   `json:"profile,omitempty"`   // the model profile it uses; empty follows the chat's
	ReadOnly     bool     `json:"read_only,omitempty"` // it may only look: nothing that needs approval is available to it
	Scopes       []string `json:"scopes,omitempty"`    // tool areas it may use; empty is all of them
	// PrevInstructions is the instructions this agent had before the last edit, so an edit can be undone.
	PrevInstructions string    `json:"prev_instructions,omitempty"`
	Created          time.Time `json:"created"`
}

// About is the one line used to choose this agent by: its description when it has one, else the start of its
// instructions.
func (a Agent) About() string {
	if a.Description != "" {
		return a.Description
	}
	about := strings.Join(strings.Fields(a.Instructions), " ")
	if r := []rune(about); len(r) > 100 {
		about = string(r[:100]) + "…"
	}
	return about
}

// AllowsTool says whether this agent's scopes leave a tool available.
func (a Agent) AllowsTool(name string) bool {
	if len(a.Scopes) == 0 {
		return true
	}
	g := ToolGroup(name)
	return g == "" || slices.Contains(a.Scopes, g)
}

// invisible says a rune draws nothing and so can hide text from the person reading it: zero-width and bidirectional
// controls, and the "tag" block that can spell out a whole hidden message. The joiner that builds emoji sequences stays,
// since an icon needs it.
func invisible(r rune) bool {
	return unicode.Is(unicode.Cf, r) && r != 0x200D
}

// stripInvisible removes what invisible finds.
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if invisible(r) {
			return -1
		}
		return r
	}, s)
}

func line(s string, n int) string {
	s = strings.Join(strings.Fields(stripInvisible(s)), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Check cleans an agent and says what is wrong with it. Colour is checked by the caller, which knows the names.
func Check(a Agent) (Agent, error) {
	a.Name, a.Icon, a.Profile, a.Description = line(a.Name, MaxName+1), line(a.Icon, MaxIcon+1), line(a.Profile, MaxProfile+1), line(a.Description, MaxDescription+1)
	for _, s := range a.Scopes {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" && !slices.Contains(ScopeGroups, s) {
			return a, fmt.Errorf("there is no tool area called %q: use one of %s", s, strings.Join(ScopeGroups, ", "))
		}
	}
	scopes := a.Scopes[:0]
	for _, s := range a.Scopes {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	a.Scopes = scopes
	a.Instructions = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < ' ' && r != '\n' && r != '\t' || unicode.Is(unicode.Cf, r) { // no hidden text in what steers the agent
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
	case len([]rune(a.Description)) > MaxDescription:
		return a, fmt.Errorf("the description is longer than %d characters", MaxDescription)
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
	if len(a.Scopes) > 0 {
		fmt.Fprintf(&b, " Your tools are limited to these areas: %s. Tools outside them are refused here, so do not try; say what you found and what you would do.", strings.Join(a.Scopes, ", "))
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
				if a.Instructions != all[i].Instructions {
					a.PrevInstructions = all[i].Instructions // keep the last wording, so the edit can be undone
				} else {
					a.PrevInstructions = all[i].PrevInstructions
				}
				all[i] = a
				return a, s.save(all)
			}
		}
		return a, errors.New("there is no such agent")
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

// Duplicate copies an agent under a new name ("X copy", then "X copy 2", and so on), with a new ID and no
// previous wording: a copy is a place to try a variant without touching the original.
func (s *Store) Duplicate(id string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Agent{}, err
	}
	var src *Agent
	for i := range all {
		if all[i].ID == id {
			src = &all[i]
		}
	}
	if src == nil {
		return Agent{}, errors.New("there is no such agent")
	}
	name := src.Name + " copy"
	for n := 2; slices.ContainsFunc(all, func(a Agent) bool { return strings.EqualFold(a.Name, name) }); n++ {
		name = fmt.Sprintf("%s copy %d", src.Name, n)
	}
	cp := *src
	cp.ID, cp.Name, cp.PrevInstructions, cp.Created = newID(all), name, "", time.Now()
	cp.Scopes = slices.Clone(src.Scopes)
	return cp, s.save(append(all, cp))
}

// Revert swaps an agent's instructions with the wording it had before the last edit, so reverting twice puts
// the newer wording back.
func (s *Store) Revert(id string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Agent{}, err
	}
	for i := range all {
		if all[i].ID == id {
			if all[i].PrevInstructions == "" {
				return Agent{}, errors.New("there is no earlier wording to go back to")
			}
			all[i].Instructions, all[i].PrevInstructions = all[i].PrevInstructions, all[i].Instructions
			return all[i], s.save(all)
		}
	}
	return Agent{}, errors.New("there is no such agent")
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
