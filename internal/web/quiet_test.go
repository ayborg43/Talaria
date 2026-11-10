package web

import (
	"path/filepath"
	"testing"
	"time"
)

type pushLog struct{ tags []string }

func (p *pushLog) Notify(title, body, tag, path string) { p.tags = append(p.tags, tag) }

func TestQuietHoursHoldBackRepliesButNeverARequestForYou(t *testing.T) {
	lagos, _ := time.LoadLocation("Africa/Lagos") // UTC+1 all year
	at := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, lagos) }
	push := &pushLog{}
	var now time.Time
	n := &Notices{Path: filepath.Join(t.TempDir(), "n.json"), Next: push, Zone: lagos, Now: func() time.Time { return now }}
	n.SetPrefs(NoticePrefs{Approvals: true, Replies: true, QuietFrom: "22:00", QuietTo: "07:00"})

	for _, c := range []struct {
		h, m  int
		tag   string
		pushd bool
	}{
		{23, 30, "reply", false},   // late evening: quiet
		{2, 15, "reply", false},    // after midnight: still quiet
		{23, 30, "approval", true}, // a request for you always comes through
		{6, 59, "reply", false},    // the last quiet minute
		{7, 0, "reply", true},      // quiet hours end at 07:00
		{12, 0, "reply", true},     // the middle of the day
		{21, 59, "reply", true},    // just before they start
	} {
		now = at(c.h, c.m)
		before := len(push.tags)
		n.Notify("t", "b", c.tag, "/")
		if got := len(push.tags) > before; got != c.pushd {
			t.Errorf("%02d:%02d %s: pushed=%v, want %v", c.h, c.m, c.tag, got, c.pushd)
		}
	}
	items, prefs := n.List()
	if len(items) != 7 {
		t.Fatalf("every notification is kept in the list, quiet or not: %d", len(items))
	}
	if prefs.QuietFrom != "22:00" || prefs.QuietTo != "07:00" {
		t.Fatalf("%+v", prefs)
	}

	// a window inside one day, and a wrong one that is dropped rather than half applied
	n.SetPrefs(NoticePrefs{Approvals: true, Replies: true, QuietFrom: "13:00", QuietTo: "15:00"})
	now = at(14, 0)
	before := len(push.tags)
	n.Notify("t", "b", "reply", "/")
	if len(push.tags) != before {
		t.Fatal("a reply was pushed inside a same-day quiet window")
	}
	n.SetPrefs(NoticePrefs{Approvals: true, Replies: true, QuietFrom: "25:00", QuietTo: "07:00"})
	if _, p := n.List(); p.QuietFrom != "" || p.QuietTo != "" {
		t.Fatalf("an invalid window was kept: %+v", p)
	}
	n.SetPrefs(NoticePrefs{Approvals: true, Replies: true, QuietFrom: "08:00", QuietTo: "08:00"})
	if _, p := n.List(); p.QuietFrom != "" {
		t.Fatalf("an empty window was kept: %+v", p)
	}
}
