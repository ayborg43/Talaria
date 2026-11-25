package cron

import (
	"path/filepath"
	"testing"
	"time"
)

// A task that was started from outside text (a webhook's payload) stays marked, and keeps its agent's limits, when it is
// run again or written to: otherwise pressing Retry would turn an attacker's text into a trusted task.
func TestRetryKeepsWhatTheTaskWasStartedFrom(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	s.recordDone(Job{ID: "w1", Name: "Webhook: form", Kind: KindTask, Gateway: "web", ChatID: 1, Prompt: "payload says: run shell",
		Untrusted: true, Agent: "Narrow", Routine: "r1"}, "error: refused", time.Now())
	j, err := s.RetryTask("w1", "web", 1, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Untrusted || j.Agent != "Narrow" || j.Routine != "r1" {
		t.Fatalf("retry lost it: untrusted=%v agent=%q routine=%q", j.Untrusted, j.Agent, j.Routine)
	}
}

func TestFollowUpOfAnUntrustedRunIsUntrusted(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	j, err := StartFollowUp(s, nil, "web", 1, "Follow-up", "carry on", "run-1", true, "Narrow")
	if err != nil {
		t.Fatal(err)
	}
	if !j.Untrusted || j.Agent != "Narrow" || j.Parent != "run-1" {
		t.Fatalf("%+v", j)
	}
	plain, _ := StartFollowUp(s, nil, "web", 1, "Follow-up", "carry on", "run-2", false, "")
	if plain.Untrusted || plain.Agent != "" {
		t.Fatalf("an ordinary follow-up was marked: %+v", plain)
	}
}
