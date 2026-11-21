// Package digest answers "what did you do while I was away?": a plain account of the work Talaria did on its own
// over a stretch of time, written from what it already keeps (the activity log, finished tasks, watches, goals, the
// board and what the models cost). It asks no model: the same records always give the same account, and it costs
// nothing to send every morning.
package digest

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Call is one tool call from the activity log.
type Call struct {
	Tool    string
	Allowed bool
	Failed  bool   // it ran and reported an error
	Origin  string // "chat", "cli" or "job:<id>"
}

// Run is a background run that ended in the window.
type Run struct {
	Name   string
	Kind   string // task, goal, watch or "" for a scheduled prompt
	Status string // done, failed, cancelled
	Result string
	Ended  time.Time
}

// Change is a watch that saw something, a goal that moved, or a card that finished.
type Item struct {
	Title string
	Note  string
	At    time.Time
}

// Input is everything the digest is written from. Any part may be empty.
type Input struct {
	Since, Until time.Time
	Zone         *time.Location
	Calls        []Call
	Runs         []Run
	Watches      []Item // watches that changed in the window
	Goals        []Item // goals worked on in the window
	Board        []Item // cards that reached Review or failed
	Approvals    int    // questions waiting for the owner now
	ToReview     int    // finished tasks not yet looked at
	Cost         string // one line from the usage ledger; "" leaves it out
}

// Digest is the written account.
type Digest struct {
	Title string
	Text  string // Markdown
	Empty bool   // nothing happened: a scheduled digest then says nothing
}

const (
	maxListed = 6
	maxNote   = 140
)

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Span says how long the window is, as a person would: "the last 9 hours", "since Friday".
func Span(since, until time.Time, zone *time.Location) string {
	if zone == nil {
		zone = time.UTC
	}
	d := until.Sub(since)
	switch {
	case d < 90*time.Minute:
		return "the last hour"
	case d < 36*time.Hour:
		return fmt.Sprintf("the last %d hours", int(d.Hours()+0.5))
	}
	return "since " + since.In(zone).Format("Monday 2 January")
}

// Build writes the digest.
func Build(in Input) Digest {
	d := Digest{Title: "While you were away"}
	var b strings.Builder
	any := false
	section := func(head string, lines []string, more int) {
		if len(lines) == 0 {
			return
		}
		any = true
		fmt.Fprintf(&b, "\n**%s**\n", head)
		for _, l := range lines {
			fmt.Fprintf(&b, "- %s\n", l)
		}
		if more > 0 {
			fmt.Fprintf(&b, "- and %d more\n", more)
		}
	}
	span := Span(in.Since, in.Until, in.Zone)

	// Tasks and scheduled work that ended.
	var done, failed, cancelled []Run
	for _, r := range in.Runs {
		switch r.Status {
		case "failed":
			failed = append(failed, r)
		case "cancelled":
			cancelled = append(cancelled, r)
		default:
			done = append(done, r)
		}
	}
	runLine := func(r Run) string {
		l := r.Name
		if res := clip(r.Result, maxNote); res != "" && res != "Nothing to report." {
			l += ": " + res
		}
		return l
	}
	var lines []string
	extra, room := 0, maxListed
	for _, r := range failed { // what went wrong comes first
		if room == 0 {
			extra++
			continue
		}
		lines, room = append(lines, "Failed: "+runLine(r)), room-1
	}
	for _, r := range done {
		if room == 0 {
			extra++
			continue
		}
		lines, room = append(lines, "Done: "+runLine(r)), room-1
	}
	if len(cancelled) > 0 {
		lines = append(lines, plural(len(cancelled), "task was stopped", "tasks were stopped"))
	}
	section("Work that finished", lines, extra)

	itemLines := func(items []Item) ([]string, int) {
		sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })
		var out []string
		for i, it := range items {
			if i == maxListed {
				return out, len(items) - maxListed
			}
			l := it.Title
			if n := clip(it.Note, maxNote); n != "" {
				l += ": " + n
			}
			out = append(out, l)
		}
		return out, 0
	}
	if ls, more := itemLines(in.Watches); len(ls) > 0 {
		section("Watches that saw something", ls, more)
	}
	if ls, more := itemLines(in.Board); len(ls) > 0 {
		section("On the board", ls, more)
	}
	if ls, more := itemLines(in.Goals); len(ls) > 0 {
		section("Goals", ls, more)
	}

	// What it did, in numbers.
	if len(in.Calls) > 0 {
		any = true
		counts := map[string]int{}
		refused, errs, onOwn := 0, 0, 0
		for _, c := range in.Calls {
			counts[c.Tool]++
			switch {
			case !c.Allowed:
				refused++
			case c.Failed:
				errs++
			}
			if strings.HasPrefix(c.Origin, "job:") {
				onOwn++
			}
		}
		type kv struct {
			k string
			n int
		}
		var top []kv
		for k, n := range counts {
			top = append(top, kv{k, n})
		}
		sort.Slice(top, func(i, j int) bool {
			if top[i].n != top[j].n {
				return top[i].n > top[j].n
			}
			return top[i].k < top[j].k
		})
		var names []string
		for i, t := range top {
			if i == 4 {
				break
			}
			names = append(names, fmt.Sprintf("%s ×%d", t.k, t.n))
		}
		line := fmt.Sprintf("%s, %d of them on its own (%s)", plural(len(in.Calls), "tool call", "tool calls"), onOwn, strings.Join(names, ", "))
		fmt.Fprintf(&b, "\n**What it did**\n- %s\n", line)
		if refused > 0 {
			fmt.Fprintf(&b, "- %s refused by your rules or by you\n", plural(refused, "call was", "calls were"))
		}
		if errs > 0 {
			fmt.Fprintf(&b, "- %s\n", plural(errs, "call reported an error", "calls reported errors"))
		}
	}
	if in.Cost != "" {
		fmt.Fprintf(&b, "\n**What it cost**\n- %s\n", in.Cost)
	}

	// What waits for the owner. Shown even when nothing else happened, since it is the reason to open the app.
	var waiting []string
	if in.Approvals > 0 {
		waiting = append(waiting, plural(in.Approvals, "question is waiting for your answer", "questions are waiting for your answer"))
	}
	if in.ToReview > 0 {
		waiting = append(waiting, plural(in.ToReview, "finished task is waiting for you to look at it", "finished tasks are waiting for you to look at them"))
	}
	if len(waiting) > 0 {
		any = true
		fmt.Fprintf(&b, "\n**Waiting for you**\n")
		for _, w := range waiting {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	if !any {
		d.Empty = true
		d.Text = "Nothing happened on its own in " + span + "."
		return d
	}
	d.Text = fmt.Sprintf("Here is what Talaria did on its own in %s.\n%s", span, strings.TrimRight(b.String(), "\n"))
	return d
}
