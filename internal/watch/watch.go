// Package watch lets the agent keep an eye on a connected service for the user:
// a read-only tool is polled on a schedule without the model, and only when its
// answer changes is the model asked whether the change is worth telling anyone.
// Waiting costs a tool call; a change costs one model run.
package watch

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
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

// Status of a watch.
const (
	Active = "active"
	Paused = "paused"
)

// Limits.
const (
	MaxActivePerChat = 5
	MinEvery         = 5 * time.Minute
	MaxEvery         = 24 * time.Hour
	DefaultEvery     = 15 * time.Minute
	DefaultTriggers  = 30
	MaxTriggers      = 200
	// MaxErrors is how many polls in a row may fail before the watch pauses and says so.
	MaxErrors     = 5
	maxResultKept = 4000
	maxArgs       = 2000
)

// Watch is one thing being watched.
type Watch struct {
	ID           string          `json:"id"`
	Gateway      string          `json:"gateway"`
	ChatID       int64           `json:"chat_id"`
	Title        string          `json:"title"`
	Tool         string          `json:"tool"`
	Args         json.RawMessage `json:"args"`
	Instructions string          `json:"instructions,omitempty"`
	Every        time.Duration   `json:"every"`
	Status       string          `json:"status"`

	LastHash    string    `json:"last_hash,omitempty"`
	LastResult  string    `json:"last_result,omitempty"` // an excerpt, to show the model what changed
	LastRun     time.Time `json:"last_run,omitzero"`
	LastChange  time.Time `json:"last_change,omitzero"`
	Polls       int       `json:"polls"`
	Triggers    int       `json:"triggers"` // changes looked at by the model since the user last wrote
	MaxTriggers int       `json:"max_triggers"`
	Errors      int       `json:"errors"` // polls that failed in a row

	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Outcome is what one poll came to.
type Outcome int

const (
	// Quiet: nothing changed, or this was the first look, which only sets the baseline.
	Quiet Outcome = iota
	// Changed: the answer is not what it was. The model is asked about it.
	Changed
	// Failed: the poll failed, but not enough times in a row to matter yet.
	Failed
	// PausedByErrors: it failed too many times in a row, and the watch paused.
	PausedByErrors
	// PausedByBudget: it has changed more often than the allowance for one
	// stretch without the user, and the watch paused.
	PausedByBudget
)

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Observe records the result of a poll on w, and says what it comes to. result
// is the tool's answer, or err why it failed. previous is the answer before,
// for the model to compare with.
func (w *Watch) Observe(now time.Time, result string, err error) (out Outcome, previous string) {
	w.LastRun = now
	if err != nil {
		w.Errors++
		if w.Errors >= MaxErrors {
			w.Status = Paused
			return PausedByErrors, ""
		}
		return Failed, ""
	}
	w.Errors = 0
	w.Polls++
	h := hash(result)
	switch {
	case w.LastHash == "": // the first look: what to compare with from now on
		w.LastHash, w.LastResult = h, clip(result, maxResultKept)
		return Quiet, ""
	case h == w.LastHash:
		return Quiet, ""
	}
	previous = w.LastResult
	w.LastHash, w.LastResult, w.LastChange = h, clip(result, maxResultKept), now
	w.Triggers++
	if w.Triggers > w.MaxTriggers {
		w.Status = Paused
		return PausedByBudget, previous
	}
	return Changed, previous
}

// Store keeps watches in one JSON file, reread on every call.
type Store struct {
	Path string
	Now  func() time.Time

	mu sync.Mutex
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) load() ([]Watch, error) {
	raw, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Watches []Watch `json:"watches"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt (fix or delete it): %w", s.Path, err)
	}
	return f.Watches, nil
}

func (s *Store) save(ws []Watch) error {
	raw, err := json.MarshalIndent(struct {
		Watches []Watch `json:"watches"`
	}{ws}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "watches-*.tmp")
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
	if err := os.Chmod(tmp.Name(), 0o600); err != nil { // the last result may be a line of someone's mail
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

func validate(w *Watch) error {
	w.Title = strings.Join(strings.Fields(w.Title), " ")
	w.Instructions = strings.TrimSpace(w.Instructions)
	switch {
	case w.Title == "":
		return errors.New("a watch needs a title")
	case len([]rune(w.Title)) > 100:
		return errors.New("the title is too long (100 characters at most)")
	case w.Tool == "":
		return errors.New("a watch needs a tool to poll")
	case len([]rune(w.Instructions)) > 1000:
		return errors.New("the instructions are too long (1000 characters at most)")
	case w.Every < MinEvery || w.Every > MaxEvery:
		return fmt.Errorf("a watch polls between every %s and every %s", MinEvery, MaxEvery)
	case w.MaxTriggers < 1 || w.MaxTriggers > MaxTriggers:
		return fmt.Errorf("the allowance of changes must be between 1 and %d", MaxTriggers)
	}
	if len(w.Args) == 0 {
		w.Args = json.RawMessage(`{}`)
	}
	var obj map[string]any
	if json.Unmarshal(w.Args, &obj) != nil || obj == nil {
		return errors.New("the arguments must be a JSON object")
	}
	var compact bytes.Buffer
	if json.Compact(&compact, w.Args) == nil { // as the model wrote it, minus the spaces
		w.Args = compact.Bytes()
	}
	if len(w.Args) > maxArgs {
		return fmt.Errorf("the arguments are too long (%d bytes at most)", maxArgs)
	}
	return nil
}

func newID(existing []Watch) string {
	for {
		var b [4]byte
		rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		taken := false
		for _, w := range existing {
			taken = taken || w.ID == id
		}
		if !taken {
			return id
		}
	}
}

// Add stores a new, active watch.
func (s *Store) Add(w Watch) (Watch, error) {
	if w.Every == 0 {
		w.Every = DefaultEvery
	}
	if w.MaxTriggers == 0 {
		w.MaxTriggers = DefaultTriggers
	}
	if w.Gateway == "" || w.ChatID == 0 {
		return Watch{}, errors.New("a watch belongs to a chat")
	}
	w.Status = Active
	if err := validate(&w); err != nil {
		return Watch{}, err
	}
	w.Created = s.now()
	w.Updated = w.Created

	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Watch{}, err
	}
	active := 0
	for _, x := range all {
		if x.Gateway == w.Gateway && x.ChatID == w.ChatID && x.Status == Active {
			active++
		}
	}
	if active >= MaxActivePerChat {
		return Watch{}, fmt.Errorf("this chat already has %d active watches (the most is %d): delete or pause one first", active, MaxActivePerChat)
	}
	w.ID = newID(all)
	return w, s.save(append(all, w))
}

// GetAny returns a watch by id whoever it belongs to; the scheduler knows which chat it is for.
func (s *Store) GetAny(id string) (Watch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Watch{}, err
	}
	for _, w := range all {
		if w.ID == id {
			return w, nil
		}
	}
	return Watch{}, fmt.Errorf("no watch with id %q", id)
}

// List returns a chat's watches, oldest first.
func (s *Store) List(gateway string, chatID int64) ([]Watch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	var out []Watch
	for _, w := range all {
		if w.Gateway == gateway && w.ChatID == chatID {
			out = append(out, w)
		}
	}
	slices.SortFunc(out, func(a, b Watch) int { return a.Created.Compare(b.Created) })
	return out, nil
}

// Update changes a watch under the store's lock. chatID of 0 means any chat's
// (for the scheduler). The result is validated before it is saved.
func (s *Store) Update(gateway string, chatID int64, id string, change func(*Watch) error) (Watch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Watch{}, err
	}
	for i := range all {
		w := &all[i]
		if w.ID != id || (chatID != 0 && (w.Gateway != gateway || w.ChatID != chatID)) {
			continue
		}
		next := *w
		if err := change(&next); err != nil {
			return Watch{}, err
		}
		if err := validate(&next); err != nil {
			return Watch{}, err
		}
		if next.Status == Active && w.Status != Active {
			active := 0
			for _, x := range all {
				if x.ID != id && x.Gateway == next.Gateway && x.ChatID == next.ChatID && x.Status == Active {
					active++
				}
			}
			if active >= MaxActivePerChat {
				return Watch{}, fmt.Errorf("this chat already has %d active watches (the most is %d)", active, MaxActivePerChat)
			}
		}
		next.Updated = s.now()
		all[i] = next
		return next, s.save(all)
	}
	return Watch{}, fmt.Errorf("no watch with id %q in this chat", id)
}

// Remove deletes a chat's watch.
func (s *Store) Remove(gateway string, chatID int64, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	for i, w := range all {
		if w.ID == id && w.Gateway == gateway && w.ChatID == chatID {
			return s.save(append(all[:i], all[i+1:]...))
		}
	}
	return fmt.Errorf("no watch with id %q in this chat", id)
}

// ResetTriggers records that the user has had a say in a chat: its watches
// get a fresh allowance of changes to look at. Paused watches stay paused.
func (s *Store) ResetTriggers(gateway string, chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	changed := false
	for i := range all {
		if w := &all[i]; w.Gateway == gateway && w.ChatID == chatID && w.Status == Active && w.Triggers != 0 {
			w.Triggers, changed = 0, true
		}
	}
	if !changed {
		return nil
	}
	return s.save(all)
}
