// Package goal keeps the objectives a user has given the agent to pursue over
// time: a plan, notes on progress, the next step, and a schedule of check-ins
// at which the agent works on it on its own.
package goal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status of a goal.
const (
	Open    = "open"    // being worked on at each check-in
	Paused  = "paused"  // not checked in on until resumed
	Done    = "done"    // finished
	Dropped = "dropped" // given up
)

// Limits, which keep a goal's own record, and the cost of pursuing it, bounded.
const (
	MaxOpenPerChat  = 5
	MaxSteps        = 20
	MaxNotes        = 40
	MinEvery        = 15 * time.Minute
	MaxEvery        = 7 * 24 * time.Hour
	DefaultEvery    = 6 * time.Hour
	DefaultCheckins = 20
	MaxCheckins     = 100
)

type Step struct {
	Text string `json:"text"`
	Done bool   `json:"done,omitempty"`
}

type Note struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Goal is one objective.
type Goal struct {
	ID        string `json:"id"`
	Gateway   string `json:"gateway"`
	ChatID    int64  `json:"chat_id"`
	Title     string `json:"title"`
	Objective string `json:"objective"`
	Status    string `json:"status"`
	Plan      []Step `json:"plan,omitempty"`
	Notes     []Note `json:"notes,omitempty"`
	Next      string `json:"next,omitempty"` // what the next check-in should do first

	Every       time.Duration `json:"every"`        // between check-ins
	Checkins    int           `json:"checkins"`     // made since the user last had a say
	MaxCheckins int           `json:"max_checkins"` // after this many without them, the goal pauses
	AllowRisky  bool          `json:"allow_risky"`
	Profile     string        `json:"profile,omitempty"`
	Environment string        `json:"environment,omitempty"`

	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	LastCheckin time.Time `json:"last_checkin,omitzero"`
}

// Store keeps goals in one JSON file, reread on every call.
type Store struct {
	Path string
	Now  func() time.Time // defaults to time.Now; tests override

	mu sync.Mutex
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) load() ([]Goal, error) {
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Goals []Goal `json:"goals"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt (fix or delete it): %w", s.Path, err)
	}
	return f.Goals, nil
}

func (s *Store) save(goals []Goal) error {
	raw, err := json.MarshalIndent(struct {
		Goals []Goal `json:"goals"`
	}{goals}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "goals-*.tmp")
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
	if err := os.Chmod(tmp.Name(), 0o600); err != nil { // goals can hold private plans
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

func clean(s string, max int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// validate normalises a goal and rejects one outside the limits.
func validate(g *Goal) error {
	g.Title = strings.Join(strings.Fields(g.Title), " ")
	g.Objective = strings.TrimSpace(g.Objective)
	switch {
	case g.Title == "":
		return errors.New("a goal needs a title")
	case len([]rune(g.Title)) > 100:
		return errors.New("the title is too long (100 characters at most)")
	case g.Objective == "":
		return errors.New("a goal needs an objective: what done looks like")
	case len([]rune(g.Objective)) > 2000:
		return errors.New("the objective is too long (2000 characters at most)")
	case len(g.Plan) > MaxSteps:
		return fmt.Errorf("a plan has at most %d steps", MaxSteps)
	case g.Every < MinEvery || g.Every > MaxEvery:
		return fmt.Errorf("check-ins must be between %s and %s apart", MinEvery, MaxEvery)
	case g.MaxCheckins < 1 || g.MaxCheckins > MaxCheckins:
		return fmt.Errorf("the check-in limit must be between 1 and %d", MaxCheckins)
	}
	for i := range g.Plan {
		g.Plan[i].Text = clean(g.Plan[i].Text, 200)
		if g.Plan[i].Text == "" {
			return errors.New("a plan step cannot be empty")
		}
	}
	g.Next = clean(g.Next, 300)
	if len(g.Notes) > MaxNotes {
		g.Notes = g.Notes[len(g.Notes)-MaxNotes:]
	}
	return nil
}

func newID(existing []Goal) string {
	for {
		var b [4]byte
		rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		taken := false
		for _, g := range existing {
			taken = taken || g.ID == id
		}
		if !taken {
			return id
		}
	}
}

// Add stores a new, open goal.
func (s *Store) Add(g Goal) (Goal, error) {
	if g.Every == 0 {
		g.Every = DefaultEvery
	}
	if g.MaxCheckins == 0 {
		g.MaxCheckins = DefaultCheckins
	}
	if g.Gateway == "" || g.ChatID == 0 {
		return Goal{}, errors.New("a goal belongs to a chat")
	}
	g.Status = Open
	if err := validate(&g); err != nil {
		return Goal{}, err
	}
	g.Created = s.now()
	g.Updated = g.Created

	s.mu.Lock()
	defer s.mu.Unlock()
	goals, err := s.load()
	if err != nil {
		return Goal{}, err
	}
	open := 0
	for _, x := range goals {
		if x.Gateway == g.Gateway && x.ChatID == g.ChatID && x.Status == Open {
			open++
		}
	}
	if open >= MaxOpenPerChat {
		return Goal{}, fmt.Errorf("this chat already has %d open goals (the most is %d): finish, pause or drop one first", open, MaxOpenPerChat)
	}
	g.ID = newID(goals)
	return g, s.save(append(goals, g))
}

// Get returns one goal of a chat.
func (s *Store) Get(gateway string, chatID int64, id string) (Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	goals, err := s.load()
	if err != nil {
		return Goal{}, err
	}
	for _, g := range goals {
		if g.ID == id && g.Gateway == gateway && g.ChatID == chatID {
			return g, nil
		}
	}
	return Goal{}, fmt.Errorf("no goal with id %q in this chat", id)
}

// GetAny returns a goal by id whoever it belongs to. The scheduler uses it:
// a check-in already knows which chat it is for.
func (s *Store) GetAny(id string) (Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	goals, err := s.load()
	if err != nil {
		return Goal{}, err
	}
	for _, g := range goals {
		if g.ID == id {
			return g, nil
		}
	}
	return Goal{}, fmt.Errorf("no goal with id %q", id)
}

// List returns a chat's goals, oldest first.
func (s *Store) List(gateway string, chatID int64) ([]Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	goals, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []Goal
	for _, g := range goals {
		if g.Gateway == gateway && g.ChatID == chatID {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out, nil
}

// Update changes a goal in place under the store's lock. chatID of 0 means any
// chat's goal (for the scheduler). The change is validated before it is saved.
func (s *Store) Update(gateway string, chatID int64, id string, change func(*Goal) error) (Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	goals, err := s.load()
	if err != nil {
		return Goal{}, err
	}
	for i := range goals {
		g := &goals[i]
		if g.ID != id || (chatID != 0 && (g.Gateway != gateway || g.ChatID != chatID)) {
			continue
		}
		next := *g
		next.Plan = append([]Step(nil), g.Plan...)
		next.Notes = append([]Note(nil), g.Notes...)
		if err := change(&next); err != nil {
			return Goal{}, err
		}
		if err := validate(&next); err != nil {
			return Goal{}, err
		}
		if next.Status == Open && g.Status != Open {
			open := 0
			for _, x := range goals {
				if x.ID != id && x.Gateway == next.Gateway && x.ChatID == next.ChatID && x.Status == Open {
					open++
				}
			}
			if open >= MaxOpenPerChat {
				return Goal{}, fmt.Errorf("this chat already has %d open goals (the most is %d)", open, MaxOpenPerChat)
			}
		}
		next.Updated = s.now()
		goals[i] = next
		return next, s.save(goals)
	}
	return Goal{}, fmt.Errorf("no goal with id %q in this chat", id)
}

// AddNote appends a progress note.
func (g *Goal) AddNote(at time.Time, text string) {
	if text = clean(text, 500); text != "" {
		g.Notes = append(g.Notes, Note{At: at, Text: text})
	}
}

// Progress says how much of the plan is done, as "3 of 5 steps".
func (g Goal) Progress() string {
	if len(g.Plan) == 0 {
		return "no plan yet"
	}
	done := 0
	for _, s := range g.Plan {
		if s.Done {
			done++
		}
	}
	return fmt.Sprintf("%d of %d steps", done, len(g.Plan))
}

// ResetCheckins records that the user has had a say in a chat: its goals get a
// fresh allowance of check-ins. Goals that paused for lack of one are not resumed.
func (s *Store) ResetCheckins(gateway string, chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	goals, err := s.load()
	if err != nil {
		return err
	}
	changed := false
	for i := range goals {
		if g := &goals[i]; g.Gateway == gateway && g.ChatID == chatID && g.Status == Open && g.Checkins != 0 {
			g.Checkins, changed = 0, true
		}
	}
	if !changed {
		return nil
	}
	return s.save(goals)
}
