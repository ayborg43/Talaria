// Package phone is the agent's link to the user's phone. The phone opens one connection to the gateway and keeps it
// for as long as it is signed in and has something to share; the agent's phone tools send a request down it and wait
// for the answer. Nothing here reaches into the phone: the phone decides, per kind of data, what it will hand over,
// and says so in its hello, and a request for anything else is refused here before it is sent.
package phone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// What the phone can be asked for. Each is a switch on the phone itself.
const (
	OpStatus        = "status"        // battery, network, free space, do-not-disturb
	OpLocation      = "location"      // where the phone last was
	OpContacts      = "contacts"      // look someone up by name
	OpCalendar      = "calendar"      // the phone's own calendars, for the next few days
	OpNotifications = "notifications" // what is showing in the notification shade
	OpTriggers      = "triggers"      // the phone may tell the agent when something happens on it (battery, charger, Wi-Fi, Bluetooth)
)

// Ops lists every operation, in the order the phone's settings show them.
var Ops = []string{OpStatus, OpLocation, OpContacts, OpCalendar, OpNotifications, OpTriggers}

// ErrNotConnected is what a tool gets when no phone is linked.
var ErrNotConnected = errors.New("the phone is not connected. Talaria on the phone keeps a link while its alerts are on; opening the app brings it back")

// Conn is the gateway's end of the phone's connection.
type Conn interface {
	Send(msg []byte) error
	Close()
}

// Info is what the phone has said about itself.
type Info struct {
	Name     string    `json:"name"`
	Caps     []string  `json:"caps"`
	Since    time.Time `json:"since"`
	LastSeen time.Time `json:"lastSeen"`
}

// Status is Info plus whether a phone is linked now.
type Status struct {
	Connected bool `json:"connected"`
	Info
}

type reply struct {
	data json.RawMessage
	err  string
}

// Hub holds the one linked phone. A newer connection replaces an older one: it is the same person on the same phone
// after a network change, and the old connection is likely dead already.
type Hub struct {
	// ConnectWait is how long a request waits for a phone that has just dropped to come back. Zero means 6 seconds.
	ConnectWait time.Duration
	// CallTimeout is how long the phone has to answer. Zero means 25 seconds (a fresh location fix can take a while).
	CallTimeout time.Duration

	// Triggers, if set, lists what the phone should watch for; it is sent when the phone links and when it changes.
	// OnEvent is told when the phone reports that one of them happened.
	Triggers func() []Spec
	OnEvent  func(id string, data json.RawMessage)

	mu     sync.Mutex
	cur    *Link
	joined chan struct{} // closed, and replaced, each time a phone links
}

// Spec is one thing for the phone to watch for.
type Spec struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Match string `json:"match,omitempty"`
	Level int    `json:"level,omitempty"`
}

// PushTriggers sends the phone the current list, if it has allowed events. Call it after a trigger is added or removed.
func (h *Hub) PushTriggers() {
	h.mu.Lock()
	l := h.cur
	h.mu.Unlock()
	if l != nil {
		l.sendTriggers()
	}
}

func (l *Link) sendTriggers() {
	l.mu.Lock()
	ok := slices.Contains(l.info.Caps, OpTriggers)
	l.mu.Unlock()
	if !ok || l.h.Triggers == nil {
		return
	}
	list := l.h.Triggers()
	if list == nil {
		list = []Spec{}
	}
	msg, _ := json.Marshal(map[string]any{"t": "triggers", "list": list})
	l.conn.Send(msg)
}

// NewHub makes a hub with no phone.
func NewHub() *Hub { return &Hub{joined: make(chan struct{})} }

// Link is one connection from a phone.
type Link struct {
	h     *Hub
	conn  Conn
	done  chan struct{}
	ready chan struct{} // closed once the phone has said what it shares

	mu   sync.Mutex
	info Info
	next uint64
	wait map[uint64]chan reply
}

// Attach links a phone's connection. The caller feeds what arrives to Receive and calls Detach when it ends.
func (h *Hub) Attach(c Conn) *Link {
	l := &Link{h: h, conn: c, done: make(chan struct{}), ready: make(chan struct{}), wait: map[uint64]chan reply{}}
	l.info.Since, l.info.LastSeen = time.Now(), time.Now()
	h.mu.Lock()
	old := h.cur
	h.cur = l
	if h.joined == nil {
		h.joined = make(chan struct{})
	}
	close(h.joined)
	h.joined = make(chan struct{})
	h.mu.Unlock()
	if old != nil {
		old.end()
		old.conn.Close()
	}
	return l
}

// Detach ends a connection. A phone that was replaced by a newer one leaves the newer one in place.
func (l *Link) Detach() {
	l.h.mu.Lock()
	if l.h.cur == l {
		l.h.cur = nil
	}
	l.h.mu.Unlock()
	l.end()
}

func (l *Link) end() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
	default:
		close(l.done)
	}
}

type incoming struct {
	T     string          `json:"t"`
	ID    uint64          `json:"id"`
	Name  string          `json:"name"`
	Trig  string          `json:"trigger"`
	Caps  []string        `json:"caps"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

// Receive handles one message from the phone: its hello, a change to what it shares, or an answer.
func (l *Link) Receive(raw []byte) {
	var m incoming
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	l.mu.Lock()
	l.info.LastSeen = time.Now()
	push := false
	switch m.T {
	case "event":
		l.mu.Unlock()
		if m.Trig != "" && l.h.OnEvent != nil {
			go l.h.OnEvent(m.Trig, m.Data)
		}
		return
	case "hello":
		if len(m.Name) > 80 {
			m.Name = m.Name[:80]
		}
		l.info.Name = m.Name
		l.info.Caps = known(m.Caps)
		push = true
		select {
		case <-l.ready:
		default:
			close(l.ready)
		}
	case "caps":
		had := slices.Contains(l.info.Caps, OpTriggers)
		l.info.Caps = known(m.Caps)
		push = !had && slices.Contains(l.info.Caps, OpTriggers)
	case "res":
		ch, ok := l.wait[m.ID]
		if !ok {
			l.mu.Unlock()
			return
		}
		delete(l.wait, m.ID)
		if m.OK {
			ch <- reply{data: m.Data}
		} else {
			ch <- reply{err: m.Error}
		}
	}
	l.mu.Unlock()
	if push {
		l.sendTriggers() // what to watch for goes down once the phone has said what it allows
	}
}

// known keeps the operations this gateway has heard of.
func known(caps []string) []string {
	out := []string{}
	for _, c := range caps {
		if slices.Contains(Ops, c) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// Status says whether a phone is linked and what it shares.
func (h *Hub) Status() Status {
	h.mu.Lock()
	l := h.cur
	h.mu.Unlock()
	if l == nil {
		return Status{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	info := l.info
	info.Caps = slices.Clone(info.Caps)
	return Status{Connected: true, Info: info}
}

// Call asks the phone for op and returns what it answered. args may be nil.
func (h *Hub) Call(ctx context.Context, op string, args json.RawMessage) (json.RawMessage, error) {
	l, err := h.linked(ctx)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	allowed := slices.Contains(l.info.Caps, op)
	l.mu.Unlock()
	if !allowed {
		return nil, fmt.Errorf("the user has not allowed this on their phone (%s). They can turn it on in Talaria's Settings, under Your phone", op)
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	ch := make(chan reply, 1)
	l.mu.Lock()
	l.next++
	id := l.next
	l.wait[id] = ch
	l.mu.Unlock()
	drop := func() { l.mu.Lock(); delete(l.wait, id); l.mu.Unlock() }
	msg, _ := json.Marshal(map[string]any{"t": "req", "id": id, "op": op, "args": args})
	if err := l.conn.Send(msg); err != nil {
		drop()
		return nil, ErrNotConnected
	}
	limit := h.CallTimeout
	if limit <= 0 {
		limit = 25 * time.Second
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.err != "" {
			return nil, errors.New(r.err)
		}
		return r.data, nil
	case <-l.done:
		drop()
		return nil, ErrNotConnected
	case <-timer.C:
		drop()
		return nil, errors.New("the phone did not answer in time")
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

// linked returns the current connection, waiting a few seconds for one if the phone has just dropped.
func (h *Hub) linked(ctx context.Context) (*Link, error) {
	wait := h.ConnectWait
	if wait <= 0 {
		wait = 6 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		h.mu.Lock()
		l, joined := h.cur, h.joined
		h.mu.Unlock()
		if l != nil {
			// A phone that has just connected has not said yet what it shares: give it a moment to.
			select {
			case <-l.done: // replaced or ended while we looked
			case <-l.ready:
				return l, nil
			case <-deadline.C:
				return nil, ErrNotConnected
			case <-ctx.Done():
				return nil, ErrNotConnected
			}
			continue
		}
		if joined == nil {
			joined = make(chan struct{}) // a zero Hub: nothing will ever join, so this just runs out
		}
		select {
		case <-joined:
		case <-deadline.C:
			return nil, ErrNotConnected
		case <-ctx.Done():
			return nil, ErrNotConnected
		}
	}
}
