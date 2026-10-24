package web

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"talaria/internal/provider"
)

// Limits: a stored conversation keeps the last of each, and the shelf keeps
// the newest hundred conversations.
const (
	maxConvoMessages = 400
	maxConvos        = 100
)

// Convo is one conversation the page can go back to: what it looked like,
// what the agent had been told, and when it was last touched.
type Convo struct {
	ID         string             `json:"id"`
	Title      string             `json:"title,omitempty"`
	UpdatedAt  time.Time          `json:"updated_at"`
	Display    []*Message         `json:"display,omitempty"`
	Transcript []provider.Message `json:"transcript,omitempty"`
}

// ConvoMeta is a conversation as the list shows it: never its contents.
type ConvoMeta struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Updated  time.Time `json:"updated_at"`
	Messages int       `json:"messages"`
	Active   bool      `json:"active"`
}

// Convos keeps every conversation of the web chat, newest first, in one file
// readable by the owner only: transcripts can hold secrets the model was
// given. Everything is safe for use from many goroutines.
type Convos struct {
	path string

	mu     sync.Mutex
	items  map[string]*Convo
	order  []string // newest first
	active string
	seq    int64
}

// OpenConvos reads the kept conversations, or starts empty when there are none.
func OpenConvos(path string) (*Convos, error) {
	c := &Convos{path: path, items: map[string]*Convo{}}
	if path == "" {
		return c, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	var f struct {
		Active string   `json:"active"`
		Convos []*Convo `json:"convos"`
		Seq    int64    `json:"seq"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("conversations file is not JSON: %w", err)
	}
	for _, cv := range f.Convos {
		if cv == nil || cv.ID == "" {
			continue
		}
		c.items[cv.ID] = cv
		c.order = append(c.order, cv.ID)
	}
	c.active = f.Active
	if _, ok := c.items[c.active]; !ok {
		c.active = ""
	}
	if c.active == "" && len(c.order) > 0 {
		c.active = c.order[0]
	}
	c.seq = f.Seq
	if c.seq == 0 {
		c.seq = int64(len(c.order))
	}
	c.sortLocked()
	return c, nil
}

func (c *Convos) sortLocked() {
	ordered := make([]string, 0, len(c.order))
	seen := map[string]bool{}
	for _, id := range c.order {
		if _, ok := c.items[id]; ok && !seen[id] {
			seen[id] = true
			ordered = append(ordered, id)
		}
	}
	for id := range c.items {
		if !seen[id] {
			ordered = append(ordered, id)
		}
	}
	c.order = ordered
}

// List names every kept conversation, newest first, without their contents.
func (c *Convos) List() []ConvoMeta {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ConvoMeta, 0, len(c.order))
	for _, id := range c.order {
		cv := c.items[id]
		title := cv.Title
		if title == "" {
			title = "New chat"
		}
		out = append(out, ConvoMeta{ID: id, Title: title, Updated: cv.UpdatedAt, Messages: len(cv.Display), Active: id == c.active})
	}
	return out
}

// ActiveID is the conversation the page shows.
func (c *Convos) ActiveID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// Get returns a copy of one conversation for loading into the page and the agent.
func (c *Convos) Get(id string) (*Convo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.items[id]
	if !ok {
		return nil, false
	}
	out := *cv
	out.Display = append([]*Message(nil), cv.Display...)
	out.Transcript = append([]provider.Message(nil), cv.Transcript...)
	return &out, true
}

// New starts a blank conversation and shows it: whatever was open is already
// kept, because every change to it was saved when it happened.
func (c *Convos) New() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := fmt.Sprintf("c%d", c.seq)
	c.items[id] = &Convo{ID: id, UpdatedAt: time.Now()}
	c.order = append([]string{id}, c.order...)
	c.active = id
	c.trimLocked()
	c.saveLocked()
	return id
}

// Open shows a kept conversation.
func (c *Convos) Open(id string) (*Convo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.items[id]
	if !ok {
		return nil, false
	}
	c.active = id
	c.order = append([]string{id}, without(c.order, id)...)
	c.saveLocked()
	out := *cv
	out.Display = append([]*Message(nil), cv.Display...)
	out.Transcript = append([]provider.Message(nil), cv.Transcript...)
	return &out, true
}

// Save keeps what the open conversation looks like and what its agent knows.
// The title comes from its first user message, so a conversation names
// itself after what started it.
func (c *Convos) Save(id string, display []*Message, transcript []provider.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.items[id]
	if !ok {
		return
	}
	d := append([]*Message(nil), display...)
	if len(d) > maxConvoMessages {
		d = d[len(d)-maxConvoMessages:]
	}
	t := append([]provider.Message(nil), transcript...)
	if len(t) > maxConvoMessages {
		t = t[len(t)-maxConvoMessages:]
	}
	cv.Display, cv.Transcript = d, t
	if cv.Title == "" {
		for _, m := range d {
			if m.Role == "user" && strings.TrimSpace(m.Text) != "" {
				cv.Title = tidyTitle(m.Text)
				break
			}
		}
	}
	cv.UpdatedAt = time.Now()
	c.order = append([]string{id}, without(c.order, id)...)
	c.saveLocked()
}

func without(ids []string, drop string) []string {
	out := ids[:0]
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
}

func (c *Convos) trimLocked() {
	for len(c.order) > maxConvos {
		last := c.order[len(c.order)-1]
		if last == c.active {
			break // the shelf is full of the open conversation: keep it all
		}
		delete(c.items, last)
		c.order = c.order[:len(c.order)-1]
	}
}

func (c *Convos) saveLocked() {
	if c.path == "" {
		return
	}
	ordered := make([]*Convo, 0, len(c.order))
	for _, id := range c.order {
		ordered = append(ordered, c.items[id])
	}
	raw, err := json.Marshal(struct {
		Active string   `json:"active"`
		Convos []*Convo `json:"convos"`
		Seq    int64    `json:"seq"`
	}{c.active, ordered, c.seq})
	if err != nil {
		return
	}
	_ = writeFile(c.path, raw)
}

// tidyTitle names a conversation after the message that started it.
func tidyTitle(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if r := []rune(t); len(r) > 48 {
		t = string(r[:48]) + "…"
	}
	if t == "" {
		return "New chat"
	}
	return t
}
