// Package kanban keeps a board of cards for the work the user and the agent line up: Backlog, Ready, Running, Review
// and Done. A card is a title, a few lines of detail and, optionally, the saved agent that should do it. The board
// does no work itself. A Ready card that has an agent is handed to the scheduler as an ordinary background task (see
// Dispatcher), so it keeps every limit and rule a task has, and the card follows the task: Running while it works,
// Review when it ends, with what it reported.
//
// Nothing here makes a card start by itself: only a person, or the agent with the user's approval, moves one to Ready.
package kanban

import (
	"crypto/rand"
	"database/sql"
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

	_ "modernc.org/sqlite" // pure-Go driver, shared with the sessions history
)

// The columns, left to right.
const (
	Backlog = "backlog"
	Ready   = "ready"
	Running = "running"
	Review  = "review"
	Done    = "done"
)

// Columns is the board in order.
var Columns = []string{Backlog, Ready, Running, Review, Done}

// Main as a card's agent means Talaria's own agent, with no saved agent's instructions. An empty agent means nobody has
// been picked yet: such a card waits in Ready until someone picks.
const Main = "main"

// How a run that ended without a normal result is told apart on its card.
const (
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusLost      = "lost"
)

// Limits keep a chat's board, and each card, small.
const (
	MaxCards       = 200
	MaxTitleRunes  = 120
	MaxBodyRunes   = 4000
	MaxResultRunes = 4000
	MaxAgentRunes  = 60
)

// Card is one piece of work.
type Card struct {
	ID      string `json:"id"`
	Gateway string `json:"gateway"`
	ChatID  int64  `json:"chat_id"`
	Title   string `json:"title"`
	Body    string `json:"body,omitempty"`
	Column  string `json:"column"`
	// Agent is the saved agent that does it (by name), Main for Talaria's own, or "" for nobody yet.
	Agent string `json:"agent,omitempty"`
	// AllowRisky lets the task use tools that normally ask first. The person who moves the card to Ready decides it,
	// and it is shown on the card; without it those tools are refused, since a task cannot ask anyone.
	AllowRisky bool `json:"allow_risky,omitempty"`
	// Job is the background task running it or that last ran it; it is how the app opens the run.
	Job string `json:"job,omitempty"`
	// Status is "" or how the last run ended badly (StatusFailed, StatusCancelled, StatusLost).
	Status string `json:"status,omitempty"`
	// Result is what the last run reported, or why it did not.
	Result string `json:"result,omitempty"`
	// Pos orders cards within a column: a card goes to the bottom of the column it is moved to.
	Pos      int64     `json:"pos"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
}

// Store keeps the cards in a SQLite table of the shared database. It is read afresh on every call, like the other
// stores, so that a short-lived process and the gateway see the same board.
type Store struct {
	Path string
	Now  func() time.Time

	mu sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS cards(
	id TEXT PRIMARY KEY,
	gateway TEXT NOT NULL,
	chat_id INTEGER NOT NULL,
	col TEXT NOT NULL,
	job TEXT NOT NULL DEFAULT '',
	created INTEGER NOT NULL,
	updated INTEGER NOT NULL,
	data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS cards_chat ON cards(gateway, chat_id);
CREATE INDEX IF NOT EXISTS cards_job ON cards(job) WHERE job != '';
`

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "k" + hex.EncodeToString(b[:])
}

func (s *Store) db() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return nil, err
	}
	// Created owner-only before SQLite does it with the default mode.
	if f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		f.Close()
	}
	os.Chmod(s.Path, 0o600)
	db, err := sql.Open("sqlite", s.Path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func clean(s string, n int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < ' ' && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s))
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Check cleans a card's text fields and says what is wrong with it.
func Check(c Card) (Card, error) {
	c.Title = squash(c.Title)
	c.Body = clean(c.Body, MaxBodyRunes+1)
	c.Agent = squash(c.Agent)
	switch {
	case c.Title == "":
		return c, errors.New("a card needs a title")
	case len([]rune(c.Title)) > MaxTitleRunes:
		return c, fmt.Errorf("the title is longer than %d characters: put the detail in the body", MaxTitleRunes)
	case len([]rune(c.Body)) > MaxBodyRunes:
		return c, fmt.Errorf("the body is longer than %d characters", MaxBodyRunes)
	case len([]rune(c.Agent)) > MaxAgentRunes:
		return c, errors.New("that agent name is too long")
	}
	if strings.EqualFold(c.Agent, Main) {
		c.Agent = Main
	}
	return c, nil
}

func insert(exec func(string, ...any) (sql.Result, error), c Card, replace bool) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	verb := "INSERT"
	if replace {
		verb = "INSERT OR REPLACE"
	}
	_, err = exec(verb+` INTO cards(id, gateway, chat_id, col, job, created, updated, data) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Gateway, c.ChatID, c.Column, c.Job, c.Created.UnixNano(), c.Updated.UnixNano(), string(raw))
	return err
}

func scan(rows *sql.Rows) ([]Card, error) {
	defer rows.Close()
	var out []Card
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c Card
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func sorted(cards []Card) []Card {
	slices.SortStableFunc(cards, func(a, b Card) int {
		if ca, cb := slices.Index(Columns, a.Column), slices.Index(Columns, b.Column); ca != cb {
			return ca - cb
		}
		switch {
		case a.Pos < b.Pos:
			return -1
		case a.Pos > b.Pos:
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return cards
}

// Add puts a new card at the bottom of a column (Backlog if none is named). A card cannot be added to Running.
func (s *Store) Add(gateway string, chatID int64, c Card) (Card, error) {
	c, err := Check(c)
	if err != nil {
		return c, err
	}
	if c.Column == "" {
		c.Column = Backlog
	}
	if !slices.Contains(Columns, c.Column) {
		return c, fmt.Errorf("there is no column %q: use one of %s", c.Column, strings.Join(Columns, ", "))
	}
	if c.Column == Running {
		return c, errors.New("a card runs when it is Ready and has an agent; it cannot be added as running")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.db()
	if err != nil {
		return c, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cards WHERE gateway = ? AND chat_id = ?`, gateway, chatID).Scan(&n); err != nil {
		return c, err
	}
	if n >= MaxCards {
		return c, fmt.Errorf("the board already has %d cards (the most is %d): remove some of the finished ones first", n, MaxCards)
	}
	now := s.now()
	c.ID, c.Gateway, c.ChatID = newID(), gateway, chatID
	c.Created, c.Updated, c.Pos = now, now, now.UnixNano()
	c.Job, c.Status, c.Result = "", "", ""
	if err := insert(db.Exec, c, false); err != nil {
		return c, err
	}
	return c, nil
}

// List is a chat's board: the columns in order, each card in the order it was placed.
func (s *Store) List(gateway string, chatID int64) ([]Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT data FROM cards WHERE gateway = ? AND chat_id = ?`, gateway, chatID)
	if err != nil {
		return nil, err
	}
	cards, err := scan(rows)
	return sorted(cards), err
}

// All is every card on every board, for a summary across chats (such as how a saved agent's work has gone).
func (s *Store) All() ([]Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT data FROM cards`)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// ErrNoCard is what a card that is not on this chat's board gets.
var ErrNoCard = errors.New("there is no such card on this board (kanban_list shows them)")

func get(db *sql.DB, gateway string, chatID int64, id string) (Card, error) {
	rows, err := db.Query(`SELECT data FROM cards WHERE id = ? AND gateway = ? AND chat_id = ?`, id, gateway, chatID)
	if err != nil {
		return Card{}, err
	}
	cards, err := scan(rows)
	if err != nil {
		return Card{}, err
	}
	if len(cards) == 0 {
		return Card{}, ErrNoCard
	}
	return cards[0], nil
}

// Get is one card of a chat's board.
func (s *Store) Get(gateway string, chatID int64, id string) (Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.db()
	if err != nil {
		return Card{}, err
	}
	defer db.Close()
	return get(db, gateway, chatID, strings.TrimSpace(id))
}

// change reads a card, lets f alter it, and writes it back, all under the store's lock.
func (s *Store) change(gateway string, chatID int64, id string, f func(*Card) error) (Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.db()
	if err != nil {
		return Card{}, err
	}
	defer db.Close()
	c, err := get(db, gateway, chatID, strings.TrimSpace(id))
	if err != nil {
		return c, err
	}
	if err := f(&c); err != nil {
		return c, err
	}
	c.Updated = s.now()
	return c, insert(db.Exec, c, true)
}

var errRunning = errors.New("it is running: stop it first (or wait for it to finish)")

// Edit changes a card's title, body, agent and risky flag. A running card cannot be edited: what it was started with
// is what it runs.
func (s *Store) Edit(gateway string, chatID int64, id string, f func(*Card)) (Card, error) {
	return s.change(gateway, chatID, id, func(c *Card) error {
		if c.Column == Running {
			return errRunning
		}
		before := *c
		f(c)
		// Only those four fields are the caller's to change.
		keep := before
		keep.Title, keep.Body, keep.Agent, keep.AllowRisky = c.Title, c.Body, c.Agent, c.AllowRisky
		checked, err := Check(keep)
		if err != nil {
			return err
		}
		*c = checked
		return nil
	})
}

// Move puts a card at the bottom of another column. Cards enter Running only by being dispatched, and leave it only by
// ending, so neither can be done by hand. Moving a card to Ready for another go clears what the last run left.
func (s *Store) Move(gateway string, chatID int64, id, column string) (Card, error) {
	column = strings.ToLower(strings.TrimSpace(column))
	if !slices.Contains(Columns, column) {
		return Card{}, fmt.Errorf("there is no column %q: use one of %s", column, strings.Join(Columns, ", "))
	}
	if column == Running {
		return Card{}, errors.New("cards start running by themselves: move it to ready, with an agent picked, and it starts when there is room")
	}
	return s.change(gateway, chatID, id, func(c *Card) error {
		if c.Column == Running {
			return errRunning
		}
		if c.Column == column {
			return nil
		}
		c.Column = column
		c.Pos = s.now().UnixNano()
		if column == Ready {
			c.Status, c.Result, c.Job = "", "", ""
			c.Started, c.Finished = time.Time{}, time.Time{}
		}
		return nil
	})
}

// Delete removes a card that is not running.
func (s *Store) Delete(gateway string, chatID int64, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.db()
	if err != nil {
		return err
	}
	defer db.Close()
	c, err := get(db, gateway, chatID, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if c.Column == Running {
		return errRunning
	}
	_, err = db.Exec(`DELETE FROM cards WHERE id = ?`, c.ID)
	return err
}

// Dispatchable is the Ready cards of a gateway that have an agent, oldest placed first.
func (s *Store) Dispatchable(gateway string) ([]Card, error) {
	return s.inColumn(gateway, Ready, func(c Card) bool { return c.Agent != "" })
}

// RunningCards is a gateway's cards that are in Running.
func (s *Store) RunningCards(gateway string) ([]Card, error) {
	return s.inColumn(gateway, Running, nil)
}

func (s *Store) inColumn(gateway, column string, keep func(Card) bool) ([]Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT data FROM cards WHERE gateway = ? AND col = ?`, gateway, column)
	if err != nil {
		return nil, err
	}
	cards, err := scan(rows)
	if err != nil {
		return nil, err
	}
	if keep != nil {
		cards = slices.DeleteFunc(cards, func(c Card) bool { return !keep(c) })
	}
	return sorted(cards), nil
}

// byID is for the dispatcher, which knows a card only by its ID: a card is never found across gateways by anyone else.
func (s *Store) byID(id string) (Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.db()
	if err != nil {
		return Card{}, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT data FROM cards WHERE id = ?`, id)
	if err != nil {
		return Card{}, err
	}
	cards, err := scan(rows)
	if err != nil || len(cards) == 0 {
		return Card{}, errors.Join(err, ErrNoCard)
	}
	return cards[0], nil
}

func (s *Store) changeByID(id string, f func(*Card) error) (Card, error) {
	c, err := s.byID(id)
	if err != nil {
		return c, err
	}
	return s.change(c.Gateway, c.ChatID, id, f)
}

// Claim moves a Ready card to Running before its task is made, so that two sweeps never start it twice. It reports
// false when the card is no longer Ready.
func (s *Store) Claim(id string) (Card, bool, error) {
	claimed := false
	c, err := s.changeByID(id, func(c *Card) error {
		if c.Column != Ready {
			return nil
		}
		// Its place in line is kept: if there is no room for its task it goes back to Ready where it was.
		c.Column, c.Started = Running, s.now()
		c.Job, c.Status, c.Result = "", "", ""
		claimed = true
		return nil
	})
	return c, claimed && err == nil, err
}

// Attach records the task that is running a claimed card.
func (s *Store) Attach(id, job string) error {
	_, err := s.changeByID(id, func(c *Card) error {
		if c.Column == Running {
			c.Job = job
		}
		return nil
	})
	return err
}

// Unclaim puts a claimed card back to Ready, for when its task could not be made just now.
func (s *Store) Unclaim(id string) error {
	_, err := s.changeByID(id, func(c *Card) error {
		if c.Column == Running && c.Job == "" {
			c.Column, c.Started = Ready, time.Time{}
		}
		return nil
	})
	return err
}

// Settle moves a running card to Review, saying how its run ended. It is used when the card cannot be run, or when
// its run is known to have ended some way other than normally.
func (s *Store) Settle(id, status, result string) error {
	_, err := s.changeByID(id, func(c *Card) error {
		if c.Column != Running && c.Column != Ready {
			return nil
		}
		c.Column, c.Pos, c.Finished = Review, s.now().UnixNano(), s.now()
		c.Status, c.Result = status, clean(result, MaxResultRunes)
		return nil
	})
	return err
}

// Finish is told that the task job, which worked on card, ended. If the card is running that task (or has not yet been
// told which task it is running, because the task was quicker than the sweep that started it), it moves to Review with
// what the task reported. It reports whether the card moved.
func (s *Store) Finish(card, job, status, result string) (Card, bool) {
	if card == "" {
		return Card{}, false
	}
	moved := false
	c, err := s.changeByID(card, func(c *Card) error {
		if c.Column != Running || (c.Job != "" && c.Job != job) {
			return nil // already settled, or a later run owns the card now
		}
		c.Column, c.Pos, c.Finished = Review, s.now().UnixNano(), s.now()
		c.Job, c.Status, c.Result = job, status, clean(result, MaxResultRunes)
		moved = true
		return nil
	})
	return c, moved && err == nil
}
