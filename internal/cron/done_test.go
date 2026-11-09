package cron

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAFinishedTaskCanBeRunAgain(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	j, err := s.AddTask(Job{Name: "research", Kind: KindTask, Prompt: "look it up", Gateway: "web", ChatID: 1, AllowRisky: true, TimeoutSec: 600}, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	// A task is a one-shot: it is tried MaxAttempts times, then it is gone.
	for i := 0; i < MaxAttempts; i++ {
		due, err := s.ClaimDue(time.Now().Add(time.Hour), "web")
		if err != nil || len(due) != 1 {
			t.Fatalf("claim %d: %v %v", i, due, err)
		}
		if err := s.Finish(j.ID, time.Now(), "error: the model was down"); err != nil {
			t.Fatal(err)
		}
	}
	if s.Exists(j.ID) {
		t.Fatal("the failed task is still scheduled")
	}
	done := s.DoneTasks("web", 1)
	if len(done) != 1 || !done[0].Failed() || done[0].Job.Prompt != "look it up" {
		t.Fatalf("done = %+v", done)
	}
	if got := s.DoneTasks("web", 2); len(got) != 0 {
		t.Fatalf("another chat sees it: %+v", got)
	}
	if _, err := s.RetryTask(j.ID, "web", 2, nil, 3); err == nil {
		t.Fatal("another chat could retry it")
	}

	again, err := s.RetryTask(j.ID, "web", 1, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == j.ID || again.Prompt != "look it up" || !again.AllowRisky || again.TimeoutSec != 600 || again.Kind != KindTask {
		t.Fatalf("retried as %+v", again)
	}
	if len(s.DoneTasks("web", 1)) != 0 {
		t.Fatal("the record stayed, so it could be retried twice")
	}
	if _, err := s.RetryTask(j.ID, "web", 1, nil, 3); err == nil {
		t.Fatal("a second retry of the same failure was accepted")
	}
}

func TestDoneTasksAreBounded(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	for i := 0; i < maxDoneTasks+5; i++ {
		s.recordDone(Job{ID: string(rune('a' + i)), Kind: KindTask, Gateway: "web", ChatID: 1}, "ok", time.Now())
	}
	if n := len(s.DoneTasks("web", 1)); n != maxDoneTasks {
		t.Fatalf("kept %d, want %d", n, maxDoneTasks)
	}
}
