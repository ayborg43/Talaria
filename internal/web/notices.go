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
	// QuietFrom and QuietTo ("22:00" and "07:00") are hours when replies and finished work stay quiet: they are
	// kept in the list and not pushed. A request that needs you always comes through. Empty means no quiet hours.
	QuietFrom string `json:"quiet_from,omitempty"`
	QuietTo   string `json:"quiet_to,omitempty"`
}

type noticeFile struct {
	// Mute holds the tags that are not pushed. Recording what is off, rather
	// than what is on, makes a missing file mean "everything on".
	Mute      map[string]bool `json:"mute,omitempty"`
	Items     []Notice        `json:"items,omitempty"`
	QuietFrom string          `json:"quiet_from,omitempty"`
	QuietTo   string          `json:"quiet_to,omitempty"`
}

// Notices sits between the conversation and the push service: it writes every
// notification down, and passes on the kinds the user wants on their phone.
// Nothing is held in memory between calls; the file is a few kilobytes.
type Notices struct {
	Path string
	Next Notifier // the push service; may be nil
	Now  func() time.Time
	// Zone is where quiet hours are read; nil is UTC.
	Zone *time.Location

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
	muted := f.Mute[tag] || (tag != "approval" && n.quiet(f, now)) // a request that needs you is never kept quiet
	n.mu.Unlock()
	if n.Next != nil && !muted {
		n.Next.Notify(title, body, tag, path)
	}
}

// quiet says whether now falls in the quiet hours. They may run past midnight (22:00 to 07:00).
func (n *Notices) quiet(f noticeFile, now time.Time) bool {
	from, to := minutes(f.QuietFrom), minutes(f.QuietTo)
	if from < 0 || to < 0 || from == to {
		return false
	}
	loc := n.Zone
	if loc == nil {
		loc = time.UTC
	}
	t := now.In(loc)
	cur := t.Hour()*60 + t.Minute()
	if from < to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to
}

// minutes reads "HH:MM" as minutes into the day, or -1 when it is not one.
func minutes(s string) int {
	if !ValidClock(s) {
		return -1
	}
	return int(s[0]-'0')*600 + int(s[1]-'0')*60 + int(s[3]-'0')*10 + int(s[4]-'0')
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
	return out, NoticePrefs{Approvals: !f.Mute["approval"], Replies: !f.Mute["reply"], QuietFrom: f.QuietFrom, QuietTo: f.QuietTo}
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
	f.QuietFrom, f.QuietTo = "", ""
	if ValidClock(p.QuietFrom) && ValidClock(p.QuietTo) && p.QuietFrom != p.QuietTo {
		f.QuietFrom, f.QuietTo = p.QuietFrom, p.QuietTo
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
