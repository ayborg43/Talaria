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
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Archived bool   `json:"archived,omitempty"`
	// Parent is the conversation this one was opened beside: a side chat, for
	// a tangent that should not crowd the conversation it came from.
	Parent     string             `json:"parent,omitempty"`
	UpdatedAt  time.Time          `json:"updated_at"`
	Display    []*Message         `json:"display,omitempty"`
	Transcript []provider.Message `json:"transcript,omitempty"`
}

// ConvoMeta is a conversation as the list shows it: never its contents.
type ConvoMeta struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Archived bool   `json:"archived,omitempty"`
	// Main marks the one conversation that is always there: listed first,
	// never archived and never dropped to make room. Side names the
	// conversation a side chat was opened beside.
	Main     bool      `json:"main,omitempty"`
	Side     string    `json:"side,omitempty"`
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
	main   string
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
		Main   string   `json:"main"`
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
	c.main = f.Main
	c.ensureMainLocked()
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

// List names every kept conversation that is not archived, newest first,
// without their contents. The open one is listed even if archived, so the
// page never shows a conversation its list denies exists.
func (c *Convos) List() []ConvoMeta { return c.list(false) }

// ListArchived names the archived conversations, newest first.
func (c *Convos) ListArchived() []ConvoMeta { return c.list(true) }

// ensureMainLocked keeps the main conversation real: when none is named, or
// the named one is gone, the one that has been there longest takes its place.
func (c *Convos) ensureMainLocked() {
	if _, ok := c.items[c.main]; ok {
		return
	}
	c.main = ""
	for i := len(c.order) - 1; i >= 0; i-- { // newest first, so the last is the oldest
		if cv := c.items[c.order[i]]; cv != nil && cv.Parent == "" && !cv.Archived {
			c.main = cv.ID
			return
		}
	}
	if len(c.order) > 0 {
		c.main = c.order[len(c.order)-1]
	}
}

// MainID is the conversation that is always there.
func (c *Convos) MainID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.main
}

// SetMain makes a kept conversation the main one. It stops being a side chat
// or an archived one: the main conversation is neither.
func (c *Convos) SetMain(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.items[id]
	if !ok {
		return errUnknownConversation
	}
	cv.Parent, cv.Archived = "", false
	c.main = id
	c.saveLocked()
	return nil
}

// NewSide starts a blank conversation beside the open one and shows it. The
// open one is already kept, and stays exactly as it was.
func (c *Convos) NewSide() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	parent := c.active
	if p := c.items[parent]; p != nil && p.Parent != "" {
		parent = p.Parent // a side chat of a side chat hangs from the same conversation
	}
	c.seq++
	id := fmt.Sprintf("c%d", c.seq)
	c.items[id] = &Convo{ID: id, Parent: parent, UpdatedAt: time.Now()}
	c.order = append([]string{id}, c.order...)
	c.active = id
	c.trimLocked()
	c.saveLocked()
	return id
}

// SideOf names the conversation the open one was opened beside, or "" when
// the open one is not a side chat.
func (c *Convos) SideOf() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv := c.items[c.active]
	if cv == nil {
		return ""
	}
	if _, ok := c.items[cv.Parent]; !ok {
		return ""
	}
	return c.titleLocked(cv.Parent)
}

func (c *Convos) titleLocked(id string) string {
	if cv := c.items[id]; cv != nil && cv.Title != "" {
		return cv.Title
	}
	return "New chat"
}

func (c *Convos) list(archived bool) []ConvoMeta {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ConvoMeta, 0, len(c.order))
	for _, id := range c.order { // newest first; the page puts the main one at the top
		cv := c.items[id]
		if cv == nil {
			continue
		}
		if cv.Archived != archived && id != c.active {
			continue
		}
		if archived && !cv.Archived {
			continue
		}
		title := cv.Title
		if title == "" {
			title = "New chat"
		}
		meta := ConvoMeta{ID: id, Title: title, Archived: cv.Archived, Main: id == c.main, Updated: cv.UpdatedAt, Messages: len(cv.Display), Active: id == c.active}
		if _, ok := c.items[cv.Parent]; ok {
			meta.Side = c.titleLocked(cv.Parent)
		}
		out = append(out, meta)
	}
	return out
}

// Rename gives a conversation the user's own name. It sticks: the automatic
// title is only ever filled in while a conversation has no name at all.
func (c *Convos) Rename(id, title string) error {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return fmt.Errorf("give the conversation a name")
	}
	if r := []rune(title); len(r) > 80 {
		title = string(r[:80])
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.items[id]
	if !ok {
		return errUnknownConversation
	}
	cv.Title = title
	c.saveLocked()
	return nil
}

// SetArchived shelves a conversation, or brings it back. Nothing is deleted.
func (c *Convos) SetArchived(id string, archived bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cv, ok := c.items[id]
	if !ok {
		return errUnknownConversation
	}
	if archived && id == c.main {
		return errorString("the main conversation stays: make another one main first, or start a side chat")
	}
	cv.Archived = archived
	c.saveLocked()
	return nil
}

// Delete removes a conversation for good, archived or not. The main
// conversation stays. It reports whether the removed one was the open one, in
// which case the caller shows another.
func (c *Convos) Delete(id string) (wasActive bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[id]; !ok {
		return false, errUnknownConversation
	}
	if id == c.main {
		return false, errorString("the main conversation stays: make another one main first, then delete this one")
	}
	delete(c.items, id)
	c.order = without(c.order, id)
	if wasActive = id == c.active; wasActive {
		c.active = c.main
	}
	c.saveLocked()
	return wasActive, nil
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
	c.ensureMainLocked()
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
		// The oldest goes, but never the open conversation or the main one.
		drop := -1
		for i := len(c.order) - 1; i >= 0; i-- {
			if id := c.order[i]; id != c.active && id != c.main {
				drop = i
				break
			}
		}
		if drop < 0 {
			break
		}
		delete(c.items, c.order[drop])
		c.order = append(c.order[:drop], c.order[drop+1:]...)
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
		Main   string   `json:"main,omitempty"`
		Convos []*Convo `json:"convos"`
		Seq    int64    `json:"seq"`
	}{c.active, c.main, ordered, c.seq})
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
