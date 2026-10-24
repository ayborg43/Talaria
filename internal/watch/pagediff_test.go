package watch

import (
	"strings"
	"testing"
	"time"
)

func TestWithoutRelativeTimesBlanking(t *testing.T) {
	a := WithoutRelativeTimes("posted 2 hours ago by Sam")
	b := WithoutRelativeTimes("posted 3 hours ago by Sam")
	if a != b || !strings.Contains(a, "<time>") {
		t.Fatalf("relative times should compare equal: %q vs %q", a, b)
	}
	if WithoutRelativeTimes("price $12.50 in stock") == WithoutRelativeTimes("price $13.00 in stock") {
		t.Fatal("a price change is not a relative time")
	}
}

func TestDiffPageIgnoresRetimeOnly(t *testing.T) {
	prev := PageLines("Hacker News\nposted 1 day ago\nbest story ever")
	cur := PageLines("Hacker News\nposted 2 days ago\nbest story ever")
	d := DiffPage(prev, cur)
	if !d.Empty() {
		t.Fatalf("a relative-time tick alone is not news: %+v", d)
	}
}

func TestDiffPageReportsNumberChangesAsUpdates(t *testing.T) {
	prev := PageLines("Widget in stock: 14 units")
	cur := PageLines("Widget in stock: 9 units")
	d := DiffPage(prev, cur)
	if len(d.Updated) != 1 || len(d.Added) != 0 || len(d.Removed) != 0 {
		t.Fatalf("a count change is one update: %+v", d)
	}
}

func TestDiffPageSeparatesAddedAndRemoved(t *testing.T) {
	prev := PageLines("alpha\nbeta")
	cur := PageLines("alpha\ngamma")
	d := DiffPage(prev, cur)
	if len(d.Added) != 1 || d.Added[0] != "gamma" || len(d.Removed) != 1 || d.Removed[0] != "beta" {
		t.Fatalf("got %+v", d)
	}
	if got := CountPageDiff(d); !strings.Contains(got, "1 new") || !strings.Contains(got, "1 removed") {
		t.Fatalf("count: %q", got)
	}
	if got := DescribePageDiff(d); !strings.Contains(got, "New:") || !strings.Contains(got, "Removed:") {
		t.Fatalf("describe: %q", got)
	}
}

func TestObserveTreatsRetimeOnlyAsQuiet(t *testing.T) {
	w := Watch{MaxTriggers: 5}
	now := time.Now()
	if out, _ := w.Observe(now, "Best story\nposted 1 day ago", nil); out != Quiet {
		t.Fatalf("baseline: %v", out)
	}
	if out, _ := w.Observe(now, "Best story\nposted 2 days ago", nil); out != Quiet {
		t.Fatalf("a relative-time tick must not cost a model run: %v", out)
	}
	if w.Triggers != 0 {
		t.Fatalf("no allowance spent: %+v", w)
	}
	if out, _ := w.Observe(now, "Best story\nposted 2 days ago\nbrand new story", nil); out != Changed {
		t.Fatalf("a real addition still reports: %v", out)
	}
}
