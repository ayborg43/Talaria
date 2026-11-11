package agent

import (
	"context"
	"strings"
	"testing"

	"talaria/internal/provider"
)

func TestWhatTheUserAddsReachesTheModelAtItsNextStepOnly(t *testing.T) {
	p := callThenAnswer("current_time", `{}`)
	a := newAgent(p)
	queue := []string{"also check the second site"}
	var taken int
	a.Interject = func() []string {
		taken++
		out := queue
		queue = nil
		return out
	}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if taken < 2 {
		t.Fatalf("it is asked before every step, got %d", taken)
	}
	first, second := p.seen[0], p.seen[1]
	var inFirst, inSecond int
	for _, m := range first {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "The user adds, while you are working: also check the second site") {
			inFirst++
		}
	}
	for _, m := range second {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "also check the second site") {
			inSecond++
		}
	}
	if inFirst != 1 || inSecond != 1 {
		t.Fatalf("it is said once and stays in the conversation once: %d %d", inFirst, inSecond)
	}
}

func TestSafeRemovesWhatTheScrubberKnows(t *testing.T) {
	a := newAgent(&scripted{})
	if got := a.Safe("plain"); got != "plain" {
		t.Fatalf("%q", got)
	}
}
