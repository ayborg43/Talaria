package usage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/provider"
)

func capAt(t *testing.T, now time.Time, lim Limits) (*Cap, *[]string) {
	t.Helper()
	l := Open(filepath.Join(t.TempDir(), "usage.json"), time.UTC)
	l.now = func() time.Time { return now }
	var notes []string
	c := &Cap{Ledger: l, Limits: func() Limits { return lim }, Notify: func(title, body string) { notes = append(notes, title+": "+body) },
		Prices: func(p string) (Price, bool) {
			if p == "paid" {
				return Price{In: 10, Out: 10}, true // $10 a million: 100k tokens is $1
			}
			return Price{}, false
		}}
	return c, &notes
}

var (
	chatCtx = provider.WithLabel(context.Background(), provider.Label{Source: "chat"})
	jobCtx  = provider.WithLabel(context.Background(), provider.Label{Source: "job", Background: true})
)

func TestCapPausesOnlyBackgroundWork(t *testing.T) {
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	c, _ := capAt(t, now, Limits{Day: 2})
	c.Ledger.Add(spent("paid", "chat", "", "", 100_000, 0)) // $1
	if err := c.Check(jobCtx); err != nil {
		t.Fatalf("under the limit: %v", err)
	}
	c.Ledger.Add(spent("paid", "job", "", "", 100_000, 0)) // $2: reached
	err := c.Check(jobCtx)
	if !errors.Is(err, ErrPaused) || !strings.Contains(err.Error(), "today's spending limit of $2.00") || !strings.Contains(err.Error(), "Nov 26 00:00 UTC") {
		t.Fatalf("background work at the limit: %v", err)
	}
	if err := c.Check(chatCtx); err != nil {
		t.Fatalf("the owner's chat is never stopped: %v", err)
	}
	// A helper started from background work is background too, whatever it calls itself.
	helper := provider.WithLabel(jobCtx, provider.Label{Source: "delegate", Agent: "researcher"})
	if err := c.Check(helper); !errors.Is(err, ErrPaused) {
		t.Fatalf("a helper of a job: %v", err)
	}
	if err := (*Cap)(nil).Check(jobCtx); err != nil {
		t.Fatal("no cap, no limit")
	}
}

func TestCapMonthCountsTheCalendarMonthAndUnpriced(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	c, _ := capAt(t, now, Limits{Month: 5})
	c.Ledger.now = func() time.Time { return time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC) }
	c.Ledger.Add(spent("paid", "job", "", "", 1_000_000, 0)) // $10 last month: does not count
	c.Ledger.now = func() time.Time { return now }
	c.Ledger.Add(spent("paid", "job", "", "", 300_000, 0)) // $3
	c.Ledger.Add(spent("free", "job", "", "", 9_000_000, 0))
	st := c.Status()
	if st.Paused || st.Month != 3 || len(st.Unpriced) != 1 || st.Unpriced[0] != "free" {
		t.Fatalf("status %+v", st)
	}
	c.Ledger.Add(spent("paid", "job", "", "", 200_000, 0)) // $5
	st = c.Status()
	if !st.Paused || !st.Until.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("at the month's limit: %+v", st)
	}
	if line := st.Line(); !strings.Contains(line, "this month $5.00 of $5.00") || !strings.Contains(line, "paused") {
		t.Fatalf("line %q", line)
	}
	if (Status{}).Line() != "" {
		t.Fatal("no limit, no line")
	}
}

func TestCapNoticesOnceEach(t *testing.T) {
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	c, notes := capAt(t, now, Limits{Day: 10})
	add := func(tokens int) { c.Ledger.Add(spent("paid", "job", "", "", tokens, 0)); c.Observe() }
	add(500_000) // $5
	add(300_000) // $8: 80%
	add(50_000)  // $8.50: already told
	if len(*notes) != 1 || !strings.HasPrefix((*notes)[0], "Spending is close to the limit") {
		t.Fatalf("notes %q", *notes)
	}
	add(200_000) // $10.50
	add(100_000)
	if len(*notes) != 2 || !strings.HasPrefix((*notes)[1], "Spending limit reached") {
		t.Fatalf("notes %q", *notes)
	}
	// A new day starts afresh.
	c.Ledger.now = func() time.Time { return now.AddDate(0, 0, 1) }
	add(900_000)
	if len(*notes) != 3 {
		t.Fatalf("next day: %q", *notes)
	}
}
