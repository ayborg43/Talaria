package digest

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)

func TestEmptyIsSaidPlainly(t *testing.T) {
	d := Build(Input{Since: t0.Add(-9 * time.Hour), Until: t0})
	if !d.Empty || !strings.Contains(d.Text, "Nothing happened") || !strings.Contains(d.Text, "9 hours") {
		t.Fatalf("%+v", d)
	}
}

func TestWaitingAloneIsNotEmpty(t *testing.T) {
	d := Build(Input{Since: t0.Add(-9 * time.Hour), Until: t0, Approvals: 1})
	if d.Empty || !strings.Contains(d.Text, "1 question is waiting") {
		t.Fatalf("%+v", d)
	}
}

func TestFullDigest(t *testing.T) {
	in := Input{Since: t0.Add(-24 * time.Hour), Until: t0, Cost: "Yesterday: 12 requests, about $0.40.",
		Runs: []Run{
			{Name: "Summarise the inbox", Status: "done", Result: "3 need you: Ana, Bo, Cy"},
			{Name: "Price check", Status: "failed", Result: "error: the page would not load"},
			{Name: "Old chore", Status: "cancelled"},
		},
		Watches: []Item{{Title: "Flight price", Note: "now $210", At: t0}},
		Board:   []Item{{Title: "Draft the report", Note: "in Review", At: t0}},
		Calls: []Call{{Tool: "email_triage", Allowed: true, Origin: "job:a"}, {Tool: "email_triage", Allowed: true, Origin: "job:a"},
			{Tool: "shell", Allowed: false, Origin: "job:b"}, {Tool: "web_fetch", Allowed: true, Failed: true, Origin: "chat"}},
		ToReview: 2}
	d := Build(in)
	for _, want := range []string{"the last 24 hours", "Failed: Price check", "Done: Summarise the inbox: 3 need you", "1 task was stopped",
		"Flight price: now $210", "Draft the report", "4 tool calls, 3 of them on its own", "email_triage ×2", "1 call was refused", "1 call reported an error",
		"about $0.40", "2 finished tasks are waiting"} {
		if !strings.Contains(d.Text, want) {
			t.Errorf("missing %q in:\n%s", want, d.Text)
		}
	}
	if strings.Index(d.Text, "Failed:") > strings.Index(d.Text, "Done:") {
		t.Error("failures should come before successes")
	}
}

func TestLongListsAreCut(t *testing.T) {
	var runs []Run
	for i := 0; i < 10; i++ {
		runs = append(runs, Run{Name: "job", Status: "done"})
	}
	d := Build(Input{Since: t0.Add(-time.Hour), Until: t0, Runs: runs})
	if !strings.Contains(d.Text, "and 4 more") || strings.Count(d.Text, "Done: job") != 6 {
		t.Fatal(d.Text)
	}
}

func TestSpan(t *testing.T) {
	zone := time.UTC
	if s := Span(t0.AddDate(0, 0, -3), t0, zone); s != "since Wednesday 18 November" {
		t.Fatal(s)
	}
	if s := Span(t0.Add(-30*time.Minute), t0, zone); s != "the last hour" {
		t.Fatal(s)
	}
}
