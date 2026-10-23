// Package web is Talaria's own chat page: a small web app (installable on a phone) that talks to the
// same chat engine Telegram and Discord use. It signs one person in, shows the conversation as it is
// written, asks for approval with the same tokens, and can send a push notification when the app
// is closed.
package web

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"talaria/internal/activity"
	"talaria/internal/gateway/chat"
)

const (
	// ChatID and UserID are the one conversation and the one person of the web app. They share a number
	// with nothing else: a chat belongs to a service, and this one's jobs and history are the web's own.
	ChatID int64 = 1
	UserID int64 = 1

	maxMessages  = 400
	maxChanges   = 3000
	maxText      = 60000
	activityDays = 2
)

var errGone = errors.New("no such message")

// Message is one thing in the conversation: what the user said, what the agent wrote, a tool the agent
// ran, or a question waiting for an answer.
type Message struct {
	ID   int64     `json:"id"`
	Role string    `json:"role"` // user, assistant, tool, approval
	Text string    `json:"text,omitempty"`
	At   time.Time `json:"at"`
	// Tool messages: which tool, how it is going (running, ok, error) and how long it took.
	Tool  string `json:"tool,omitempty"`
	State string `json:"state,omitempty"` // tool: running ok error; approval: pending allowed always denied expired
	Ms    int64  `json:"ms,omitempty"`
	// Approval messages: the token that answers it, only while it is waiting, and whether "always" is offered.
	Token  string `json:"token,omitempty"`
	Always bool   `json:"always,omitempty"`

	started time.Time
}

// Event is a change the page is told about.
type Event struct {
	Seq  int64    `json:"seq"`
	Type string   `json:"type"` // msg, typing, reset
	Msg  *Message `json:"msg,omitempty"`
	On   bool     `json:"on,omitempty"` // run: whether the agent is working
	// Agents is who is working, on an event of type agents. Empty (and so left out) means nobody.
	Agents []activity.Worker `json:"agents,omitempty"`
}

// Finished is a helper or a piece of background work that has ended, for the Agents screen.
type Finished struct {
	Kind    string    `json:"kind"`
	Label   string    `json:"label"`
	Seconds int64     `json:"seconds"` // how long it worked
	At      time.Time `json:"at"`
	Stopped bool      `json:"stopped,omitempty"` // cut short when the agent was stopped, rather than finishing
}

const maxRecent = 30

// Notifier tells the person something when the page is not open.
type Notifier interface {
	Notify(title, body, tag, path string)
}

type change struct {
	seq int64
	id  int64
}

type sub struct {
	ch chan Event
}

// Hub is the web app as the chat engine sees it (a chat.Transport), and the record of the conversation
// the page shows. Everything is safe for use from many goroutines.
type Hub struct {
	Path     string   // where the conversation is kept between runs; "" keeps it in memory only
	Notifier Notifier // may be nil
	Now      func() time.Time
	// QuietAfter is how long a reply must stay unchanged before a notification is sent about it.
	QuietAfter time.Duration

	mu       sync.Mutex
	loaded   bool
	msgs     []*Message
	byID     map[int64]*Message
	nextID   int64
	seq      int64
	epoch    int64 // which run of Talaria this is: a page from an earlier run starts again
	changes  []change
	subs     map[*sub]struct{}
	running  bool
	recent   []Finished                 // newest last
	workers  map[int64]*activity.Worker // who is working now
	lastWork int64
	mainID   int64          // the main agent's number while it works
	tools    map[string]int // tools the main agent is running now, and how many of each
	seen     time.Time      // when the page last said it was open and in view
	timers   map[int64]*time.Timer
	activity map[int64]int // tool calls and messages per hour, by the hour's Unix time / 3600
	saveT    *time.Timer
	closed   bool
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Hub) quiet() time.Duration {
	if h.QuietAfter > 0 {
		return h.QuietAfter
	}
	return 4 * time.Second
}

// init prepares the hub's maps and reads what was kept. It is called under mu by every method that needs it.
func (h *Hub) init() {
	if h.loaded {
		return
	}
	h.loaded = true
	h.epoch = time.Now().UnixNano()
	h.byID = map[int64]*Message{}
	h.subs = map[*sub]struct{}{}
	h.timers = map[int64]*time.Timer{}
	h.activity = map[int64]int{}
	h.workers = map[int64]*activity.Worker{}
	h.tools = map[string]int{}
	h.nextID = 1
	if h.Path == "" {
		return
	}
	raw, err := os.ReadFile(h.Path)
	if err != nil {
		return
	}
	var f struct {
		Next     int64         `json:"next"`
		Messages []*Message    `json:"messages"`
		Activity map[int64]int `json:"activity"`
		Recent   []Finished    `json:"recent"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	if len(f.Recent) > maxRecent {
		f.Recent = f.Recent[len(f.Recent)-maxRecent:]
	}
	h.recent = f.Recent
	for _, m := range f.Messages {
		// Anything that was running or waiting when Talaria stopped is not any more.
		switch {
		case m.Role == "tool" && m.State == "running":
			m.State = "error"
		case m.Role == "approval" && m.State == "pending":
			m.State, m.Token = "expired", ""
		}
		h.msgs = append(h.msgs, m)
		h.byID[m.ID] = m
	}
	if f.Next > h.nextID {
		h.nextID = f.Next
	}
	if f.Activity != nil {
		h.activity = f.Activity
	}
}

func (h *Hub) save() {
	if h.Path == "" || h.closed {
		return
	}
	raw, err := json.Marshal(struct {
		Next     int64         `json:"next"`
		Messages []*Message    `json:"messages"`
		Activity map[int64]int `json:"activity"`
		Recent   []Finished    `json:"recent"`
	}{h.nextID, h.msgs, h.activity, h.recent})
	if err != nil {
		return
	}
	writeFile(h.Path, raw)
}

// writeFile replaces a file in one step, readable by the owner only: a half-written file is never seen.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Close writes the conversation down and stops the timers.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.save()
	h.closed = true
	for _, t := range h.timers {
		t.Stop()
	}
	if h.saveT != nil {
		h.saveT.Stop()
	}
	for s := range h.subs {
		close(s.ch)
		delete(h.subs, s)
	}
}

func (h *Hub) scheduleSave() {
	if h.Path == "" || h.saveT != nil || h.closed {
		return
	}
	h.saveT = time.AfterFunc(2*time.Second, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.saveT = nil
		h.save()
	})
}

// publish records that a message changed and tells every open page. Callers hold mu.
func (h *Hub) publish(m *Message) {
	h.seq++
	h.changes = append(h.changes, change{h.seq, m.ID})
	if len(h.changes) > maxChanges {
		h.changes = append([]change(nil), h.changes[len(h.changes)-maxChanges:]...)
	}
	h.fan(Event{Seq: h.seq, Type: "msg", Msg: copyMsg(m)})
	h.scheduleSave()
}

func (h *Hub) fan(ev Event) {
	for s := range h.subs {
		select {
		case s.ch <- ev:
		default: // too far behind: drop it, and the page will fetch the whole conversation again
			close(s.ch)
			delete(h.subs, s)
		}
	}
}

func copyMsg(m *Message) *Message { c := *m; return &c }

func (h *Hub) add(m *Message) *Message {
	m.ID = h.nextID
	h.nextID++
	if m.At.IsZero() {
		m.At = h.now()
	}
	h.msgs = append(h.msgs, m)
	h.byID[m.ID] = m
	if len(h.msgs) > maxMessages {
		drop := len(h.msgs) - maxMessages
		for _, old := range h.msgs[:drop] {
			delete(h.byID, old.ID)
			if t := h.timers[old.ID]; t != nil {
				t.Stop()
				delete(h.timers, old.ID)
			}
		}
		h.msgs = append([]*Message(nil), h.msgs[drop:]...)
	}
	h.publish(m)
	return m
}

// count adds to the hour's tally of what Talaria did.
func (h *Hub) count() {
	hr := h.now().Unix() / 3600
	h.activity[hr]++
	for k := range h.activity {
		if k < hr-24*activityDays {
			delete(h.activity, k)
		}
	}
}

// Activity is the number of things done in each of the last n hours, oldest first.
func (h *Hub) Activity(n int) []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	hr := h.now().Unix() / 3600
	out := make([]int, n)
	for i := range out {
		out[i] = h.activity[hr-int64(n-1-i)]
	}
	return out
}

// ---- what the page does ----

// AddUser records something the user wrote.
func (h *Hub) AddUser(text string) *Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.count()
	return copyMsg(h.add(&Message{Role: "user", Text: clip(text)}))
}

// Snapshot is the whole conversation, the point in the changes it is current to, and which run of Talaria that is.
func (h *Hub) Snapshot() (msgs []*Message, seq, epoch int64, running bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	out := make([]*Message, len(h.msgs))
	for i, m := range h.msgs {
		out[i] = copyMsg(m)
	}
	return out, h.seq, h.epoch, h.running
}

// Pending is the questions waiting for an answer.
func (h *Hub) Pending() []*Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	var out []*Message
	for _, m := range h.msgs {
		if m.Role == "approval" && m.State == "pending" {
			out = append(out, copyMsg(m))
		}
	}
	return out
}

// Reset forgets the conversation the page shows, for a new chat: open pages are told to fetch it
// again, and a page that reconnects with an earlier epoch is told the same. What finished and the
// activity tally are not the conversation, so they stay.
func (h *Hub) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	for id, t := range h.timers {
		t.Stop()
		delete(h.timers, id)
	}
	h.msgs = nil
	h.byID = map[int64]*Message{}
	h.changes = nil
	h.running = false
	h.epoch = time.Now().UnixNano()
	h.seq++
	h.fan(Event{Seq: h.seq, Type: "reset"})
	h.scheduleSave()
}

// Subscribe starts a stream of changes after the given point. The first event may be a reset, which means
// the page missed too much and must fetch the conversation again.
func (h *Hub) Subscribe(epoch, after int64) (<-chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	s := &sub{ch: make(chan Event, 512)}
	switch {
	case epoch != h.epoch || after > h.seq || (len(h.changes) > 0 && after < h.changes[0].seq-1) || (len(h.changes) == 0 && after < h.seq):
		s.ch <- Event{Seq: h.seq, Type: "reset"}
	default:
		seen := map[int64]bool{}
		var ids []int64
		for i := len(h.changes) - 1; i >= 0 && h.changes[i].seq > after; i-- {
			if !seen[h.changes[i].id] {
				seen[h.changes[i].id] = true
				ids = append(ids, h.changes[i].id)
			}
		}
		for i := len(ids) - 1; i >= 0; i-- {
			if m := h.byID[ids[i]]; m != nil {
				s.ch <- Event{Seq: h.seq, Type: "msg", Msg: copyMsg(m)}
			}
		}
	}
	s.ch <- Event{Seq: h.seq, Type: "run", On: h.running}
	s.ch <- Event{Seq: h.seq, Type: "agents", Agents: h.workersLocked()}
	h.subs[s] = struct{}{}
	return s.ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
	}
}

// Seen records that the page is open and in view, so a notification is not needed.
func (h *Hub) Seen() {
	h.mu.Lock()
	h.seen = h.now()
	h.mu.Unlock()
}

// Away reports whether nobody has the page open and in view.
func (h *Hub) Away() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now().Sub(h.seen) > 45*time.Second
}

// Resolve records the answer to a question: its buttons go, and the verdict is added.
func (h *Hub) Resolve(token string, choice chat.Choice) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	for _, m := range h.msgs {
		if m.Role == "approval" && m.State == "pending" && m.Token == token {
			m.State = map[chat.Choice]string{chat.ChoiceAllow: "allowed", chat.ChoiceAlways: "always", chat.ChoiceDeny: "denied"}[choice]
			m.Token = ""
			h.publish(m)
			return
		}
	}
}

// ---- chat.Transport ----

func (*Hub) Service() string             { return "Web" }
func (*Hub) MaxMessage() int             { return maxText }
func (*Hub) EditInterval() time.Duration { return 250 * time.Millisecond }
func (*Hub) CommandPrefix() string       { return "/" }
func (*Hub) Markdown() bool              { return true }

// clip trims text to what is kept and drops the cursor mark the engine puts on a reply still being written:
// the page draws its own.
func clip(s string) string {
	s = strings.TrimSuffix(s, chat.StreamCursor)
	if r := []rune(s); len(r) > maxText {
		return string(r[:maxText])
	}
	return s
}

func (h *Hub) Send(_ context.Context, _ int64, text string) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.count()
	m := h.add(&Message{Role: "assistant", Text: clip(text)})
	h.tell(m.ID, "Talaria")
	return m.ID, nil
}

func (h *Hub) Edit(_ context.Context, _, msg int64, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	m := h.byID[msg]
	if m == nil {
		return errGone
	}
	m.Text = clip(text)
	if m.Role == "approval" && m.State == "pending" { // the engine edits a question only to say it ended
		m.State, m.Token = "expired", ""
	}
	h.publish(m)
	if m.Role == "assistant" {
		h.tell(m.ID, "Talaria")
	}
	return nil
}

func (*Hub) Classify(err error) chat.EditOutcome {
	if errors.Is(err, errGone) {
		return chat.EditGone
	}
	return chat.EditFailed
}

func (h *Hub) Typing(context.Context, int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.fan(Event{Seq: h.seq, Type: "typing"})
	return nil
}

func (h *Hub) SendApproval(_ context.Context, _ int64, text, token string, always bool) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	m := h.add(&Message{Role: "approval", Text: clip(text), State: "pending", Token: token, Always: always})
	if h.Notifier != nil && h.awayLocked() {
		go h.Notifier.Notify("Needs your OK", firstLine(text), "approval", "/#needs")
	}
	return m.ID, nil
}

// ---- chat.RunWatcher ----

func (h *Hub) RunStart(int64) { h.setRunning(true) }
func (h *Hub) RunEnd(int64)   { h.setRunning(false) }

func (h *Hub) setRunning(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.running = on
	h.fan(Event{Seq: h.seq, Type: "run", On: on})
	if on {
		h.mainID = h.startLocked(activity.Main, "Talaria")
		return
	}
	// The run is over, so nothing it started is still going, and no tool is still running: whatever says
	// otherwise was cut short (stopped, timed out), and must not be left spinning on the page.
	for id, w := range h.workers {
		if id == h.mainID || w.Kind == activity.Sub {
			h.finishLocked(w, true)
			delete(h.workers, id)
		}
	}
	h.mainID = 0
	h.tools = map[string]int{}
	for _, m := range h.msgs {
		if m.Role == "tool" && m.State == "running" {
			m.State = "error"
			h.publish(m)
		}
	}
	h.pushAgents()
}

// ---- activity.Sink: who is working ----

// pruneAfter is how long a worker may be shown as working: a limit on how wrong the page can be if a
// worker's end was never reported.
const pruneAfter = 3 * time.Hour

func (h *Hub) startLocked(kind, label string) int64 {
	h.lastWork++
	id := h.lastWork
	h.workers[id] = &activity.Worker{ID: id, Kind: kind, Label: clipRunes(label, 60), Since: h.now()}
	h.pushAgents()
	return id
}

// finishLocked remembers that a worker has ended. The main agent is left out: it ends with every answer, and a
// line for each would bury the helpers and the background work, which are what the list is for.
func (h *Hub) finishLocked(w *activity.Worker, stopped bool) {
	if w.Kind == activity.Main {
		return
	}
	h.recent = append(h.recent, Finished{Kind: w.Kind, Label: w.Label, Seconds: int64(h.now().Sub(w.Since).Seconds()), At: h.now(), Stopped: stopped})
	if len(h.recent) > maxRecent {
		h.recent = append([]Finished(nil), h.recent[len(h.recent)-maxRecent:]...)
	}
	h.scheduleSave()
}

// Recent is what has finished lately, newest first.
func (h *Hub) Recent() []Finished {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	out := make([]Finished, len(h.recent))
	for i, f := range h.recent {
		out[len(h.recent)-1-i] = f
	}
	return out
}

// workersLocked lists who is working: the main agent first, then the others in the order they began.
func (h *Hub) workersLocked() []activity.Worker {
	now := h.now()
	out := make([]activity.Worker, 0, len(h.workers))
	for id, w := range h.workers {
		if now.Sub(w.Since) > pruneAfter {
			delete(h.workers, id)
			continue
		}
		c := *w
		c.AgeMs = now.Sub(w.Since).Milliseconds()
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b activity.Worker) int {
		am, bm := a.Kind == activity.Main, b.Kind == activity.Main
		if am != bm {
			if am {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

func (h *Hub) pushAgents() { h.fan(Event{Seq: h.seq, Type: "agents", Agents: h.workersLocked()}) }

// Workers is who is working now.
func (h *Hub) Workers() []activity.Worker {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	return h.workersLocked()
}

func (h *Hub) Start(kind, label string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	return h.startLocked(kind, label)
}

func (h *Hub) Doing(id int64, what string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	if w := h.workers[id]; w != nil && w.Doing != what {
		w.Doing = clipRunes(what, 80)
		h.pushAgents()
	}
}

func (h *Hub) Stop(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	if w, ok := h.workers[id]; ok {
		h.finishLocked(w, false)
		delete(h.workers, id)
		h.pushAgents()
	}
}

// toolsDoing says what the main agent is in the middle of: the tools it is running, if any.
func (h *Hub) toolsDoing() string {
	names := make([]string, 0, len(h.tools))
	for n := range h.tools {
		names = append(names, n)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func (h *Hub) noteMain() {
	if w := h.workers[h.mainID]; w != nil && w.Doing != h.toolsDoing() {
		w.Doing = clipRunes(h.toolsDoing(), 80)
		h.pushAgents()
	}
}

// ---- chat.ToolWatcher ----

func (h *Hub) ToolStart(_ int64, name string) {
	// A subagent's progress arrives as a tool of its parent, named "  ↳ [label] tool", and never ends as one:
	// the helper has its own place in the roster, so it is not a chip.
	if strings.HasPrefix(name, "  ↳") {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.count()
	m := h.add(&Message{Role: "tool", Tool: name, State: "running"})
	m.started = h.now()
	h.tools[name]++
	h.noteMain()
}

func (h *Hub) ToolEnd(_ int64, name, result string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	if h.tools[name] > 1 {
		h.tools[name]--
	} else {
		delete(h.tools, name)
	}
	h.noteMain()
	for _, m := range h.msgs {
		if m.Role == "tool" && m.Tool == name && m.State == "running" {
			m.State = "ok"
			if len(result) >= 7 && result[:7] == "error: " {
				m.State = "error"
			}
			if !m.started.IsZero() {
				m.Ms = h.now().Sub(m.started).Milliseconds()
			}
			h.publish(m)
			return
		}
	}
}

// ---- notifications ----

func (h *Hub) awayLocked() bool { return h.now().Sub(h.seen) > 45*time.Second }

// tell sends a notification about a reply once it has stopped changing, if nobody is looking.
func (h *Hub) tell(id int64, title string) {
	if h.Notifier == nil || h.closed {
		return
	}
	if t := h.timers[id]; t != nil {
		t.Stop()
	}
	h.timers[id] = time.AfterFunc(h.quiet(), func() {
		h.mu.Lock()
		delete(h.timers, id)
		m := h.byID[id]
		away := h.awayLocked()
		var body string
		if m != nil {
			body = firstLine(m.Text)
		}
		h.mu.Unlock()
		if m != nil && away && body != "" {
			h.Notifier.Notify(title, body, "reply", "/")
		}
	})
}

// firstLine is the start of a message, short enough for a notification.
func firstLine(s string) string {
	r := []rune(strings.TrimSpace(s))
	for i, c := range r {
		if c == '\n' {
			r = r[:i]
			break
		}
	}
	if len(r) > 110 {
		r = append(r[:110], '…')
	}
	return string(r)
}
