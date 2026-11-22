package usage

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/provider"
)

func spent(profile, source, agent, kind string, in, out int) provider.Spent {
	return provider.Spent{Profile: profile, Model: profile + "-m", Usage: provider.Usage{In: in, Out: out},
		Label: provider.Label{Source: source, Agent: agent, Kind: kind}}
}

func TestReportByDayAndPrice(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	l := Open(filepath.Join(t.TempDir(), "usage.json"), time.UTC)
	l.now = func() time.Time { return now }
	l.Add(spent("strong", "chat", "", "code", 1_000_000, 200_000))
	l.now = func() time.Time { return now.AddDate(0, 0, -3) }
	l.Add(spent("cheap", "job", "researcher", "simple", 500_000, 100_000))
	l.now = func() time.Time { return now.AddDate(0, 0, -10) }
	l.Add(spent("strong", "chat", "", "code", 9_000_000, 0)) // outside a week
	l.now = func() time.Time { return now }

	prices := func(p string) (Price, bool) {
		if p == "strong" {
			return Price{In: 3, Out: 15}, true
		}
		return Price{}, false
	}
	rep := l.Report(7, prices)
	if rep.Total.Totals.Calls != 2 || rep.Total.Totals.In != 1_500_000 {
		t.Fatalf("totals %+v", rep.Total.Totals)
	}
	if rep.Total.Cost != 6 { // 1M*3 + 0.2M*15 = 3 + 3
		t.Fatalf("cost %v", rep.Total.Cost)
	}
	if rep.Total.Priced {
		t.Fatal("the cheap profile has no price, so the total is not fully priced")
	}
	if len(rep.Unpriced) != 1 || rep.Unpriced[0] != "cheap" {
		t.Fatalf("unpriced %v", rep.Unpriced)
	}
	if len(rep.PerDay) != 7 || rep.PerDay[6].Tokens != 1_200_000 || rep.PerDay[3].Tokens != 600_000 {
		t.Fatalf("per day %+v", rep.PerDay)
	}
	if rep.ByAgent[0].Name != "main agent" {
		t.Fatalf("by agent %+v", rep.ByAgent)
	}
	s := rep.Summary("this week")
	if !strings.Contains(s, "$6.00") || !strings.Contains(s, "Mostly on code") || !strings.Contains(s, "cheap") {
		t.Fatalf("summary %q", s)
	}
}

func TestFlushAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	l := Open(path, time.UTC)
	l.Add(spent("a", "", "", "", 10, 5))
	l.Add(spent("a", "", "", "", 10, 5))
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	again := Open(path, time.UTC)
	rep := again.Report(1, nil)
	if rep.Total.Totals.Calls != 2 || rep.Total.Totals.In != 20 || rep.BySource[0].Name != "chat" {
		t.Fatalf("%+v", rep)
	}
}

func TestEstimatedIsFlagged(t *testing.T) {
	l := Open("", time.UTC)
	l.Add(provider.Spent{Profile: "p", Usage: provider.Usage{In: 8, Out: 4, Estimated: true}})
	rep := l.Report(1, nil)
	if rep.Total.Totals.Estimated != 1 || !strings.Contains(rep.Summary("today"), "estimates") {
		t.Fatalf("%+v %s", rep.Total, rep.Summary("today"))
	}
}

func TestNothingYet(t *testing.T) {
	l := Open("", time.UTC)
	if s := l.Report(7, nil).Summary("this week"); !strings.Contains(s, "Nothing") {
		t.Fatal(s)
	}
}

func TestTinyCostIsNotWrittenAboutUnder(t *testing.T) {
	l := Open("", time.UTC)
	l.Add(provider.Spent{Profile: "p", Usage: provider.Usage{In: 100, Out: 10}})
	s := l.Report(1, func(string) (Price, bool) { return Price{In: 1, Out: 1}, true }).Summary("today")
	if strings.Contains(s, "about under") || !strings.Contains(s, "under $0.01") {
		t.Fatal(s)
	}
}
