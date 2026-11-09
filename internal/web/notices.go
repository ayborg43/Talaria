package web

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// maxNotices is how many notifications are kept to read in the app.
const maxNotices = 50

// Notice is a notification as the app lists it: what would have been on the
// lock screen, kept so it can be read later.
type Notice struct {
	At    time.Time `json:"at"`
	Title string    `json:"title"`
	Body  string    `json:"body,omitempty"`
	Tag   string    `json:"tag"` // approval or reply
	Path  string    `json:"path,omitempty"`
}

// NoticePrefs says which kinds of notification are pushed to the phone. Every
// kind is still kept in the app's own list: muting one stops the buzz, not
// the record.
type NoticePrefs struct {
	Approvals bool `json:"approvals"`
	Replies   bool `json:"replies"`
}

type noticeFile struct {
	// Mute holds the tags that are not pushed. Recording what is off, rather
	// than what is on, makes a missing file mean "everything on".
	Mute  map[string]bool `json:"mute,omitempty"`
	Items []Notice        `json:"items,omitempty"`
}

// Notices sits between the conversation and the push service: it writes every
// notification down, and passes on the kinds the user wants on their phone.
// Nothing is held in memory between calls; the file is a few kilobytes.
type Notices struct {
	Path string
	Next Notifier // the push service; may be nil
	Now  func() time.Time

	mu sync.Mutex
}

func (n *Notices) load() noticeFile {
	var f noticeFile
	if raw, err := os.ReadFile(n.Path); err == nil {
		_ = json.Unmarshal(raw, &f)
	}
	return f
}

func (n *Notices) save(f noticeFile) {
	if n.Path == "" {
		return
	}
	if raw, err := json.Marshal(f); err == nil {
		_ = writeFile(n.Path, raw)
	}
}

// Notify records a notification and pushes it, unless its kind is muted.
func (n *Notices) Notify(title, body, tag, path string) {
	now := time.Now()
	if n.Now != nil {
		now = n.Now()
	}
	n.mu.Lock()
	f := n.load()
	f.Items = append(f.Items, Notice{At: now, Title: clipRunes(title, 80), Body: clipRunes(body, 200), Tag: tag, Path: path})
	if len(f.Items) > maxNotices {
		f.Items = f.Items[len(f.Items)-maxNotices:]
	}
	n.save(f)
	muted := f.Mute[tag]
	n.mu.Unlock()
	if n.Next != nil && !muted {
		n.Next.Notify(title, body, tag, path)
	}
}

// List returns the kept notifications, newest first, and the preferences.
func (n *Notices) List() ([]Notice, NoticePrefs) {
	n.mu.Lock()
	f := n.load()
	n.mu.Unlock()
	out := make([]Notice, len(f.Items))
	for i, it := range f.Items {
		out[len(f.Items)-1-i] = it
	}
	return out, NoticePrefs{Approvals: !f.Mute["approval"], Replies: !f.Mute["reply"]}
}

// SetPrefs changes which kinds are pushed.
func (n *Notices) SetPrefs(p NoticePrefs) {
	n.mu.Lock()
	defer n.mu.Unlock()
	f := n.load()
	f.Mute = map[string]bool{}
	if !p.Approvals {
		f.Mute["approval"] = true
	}
	if !p.Replies {
		f.Mute["reply"] = true
	}
	n.save(f)
}

// Clear empties the list. The preferences stay.
func (n *Notices) Clear() {
	n.mu.Lock()
	defer n.mu.Unlock()
	f := n.load()
	f.Items = nil
	n.save(f)
}
