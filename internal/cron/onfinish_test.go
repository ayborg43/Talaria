package cron

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type ended struct{ id, status, result, card string }

// watchEnds records every OnFinish the scheduler makes.
func watchEnds(h *harness) *[]ended {
	var mu sync.Mutex
	var got []ended
	h.sched.OnFinish = func(j Job, status, result string) {
		mu.Lock()
		got = append(got, ended{j.ID, status, result, j.Card})
		mu.Unlock()
	}
	return &got
}

func TestOnFinishToldOfASuccess(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "  three flights  ", nil })
	got := watchEnds(h)
	h.store.Add(Job{Schedule: "@at 2026-10-20T10:30:00Z", Prompt: "p", ChatID: 1, Card: "k1"})
	h.fireAt("2026-10-20 10:31")
	if len(*got) != 1 || (*got)[0].status != "ok" || (*got)[0].result != "three flights" || (*got)[0].card != "k1" {
		t.Fatalf("got %+v", *got)
	}
}

func TestOnFinishSilentReplyStillEndsTheJob(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "[SILENT] nothing", nil })
	got := watchEnds(h)
	h.store.Add(Job{Schedule: "@at 2026-10-20T10:30:00Z", Prompt: "p", ChatID: 1})
	h.fireAt("2026-10-20 10:31")
	if len(*got) != 1 || (*got)[0].status != "ok" || (*got)[0].result == "" {
		t.Fatalf("got %+v", *got)
	}
}

// A failure that will be tried again has not ended; only the last attempt does.
func TestOnFinishWaitsForTheLastAttempt(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "", errors.New("site down") })
	got := watchEnds(h)
	id, _ := h.store.AddTask(Job{Name: "t", Kind: KindTask, Prompt: "p", ChatID: 1, Gateway: "web", Card: "k1"}, nil, 3)

	at := utc("2026-10-20 10:01")
	for attempt := 1; attempt <= MaxAttempts+1; attempt++ {
		h.fireAt(at.Format("2006-01-02 15:04"))
		at = at.Add(time.Hour)
		if attempt < MaxAttempts && len(*got) != 0 {
			t.Fatalf("attempt %d was reported as the end: %+v", attempt, *got)
		}
	}
	if len(*got) != 1 || (*got)[0].status != "error" || !strings.Contains((*got)[0].result, "site down") || (*got)[0].id != id.ID {
		t.Fatalf("after all attempts: %+v (%d runs)", *got, len(h.execed))
	}
}

func TestOnFinishToldOfAnActedFailureAtOnce(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "", Acted(errors.New("it clicked, then broke")) })
	got := watchEnds(h)
	h.store.AddTask(Job{Name: "t", Kind: KindTask, Prompt: "p", ChatID: 1, Gateway: "web"}, nil, 3)
	h.fireAt("2026-10-20 10:01")
	if len(*got) != 1 || (*got)[0].status != "error" {
		t.Fatalf("an attempt that had already acted is final: %+v", *got)
	}
}

func TestOnFinishToldOfADrop(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "", ErrDrop })
	got := watchEnds(h)
	h.store.Add(Job{Schedule: "@every 5m", Prompt: "p", ChatID: 1})
	h.fireAt("2026-10-20 10:06")
	if len(*got) != 1 || (*got)[0].status != "dropped" {
		t.Fatalf("got %+v", *got)
	}
}

func TestOnFinishToldOfACancel(t *testing.T) {
	started := make(chan struct{})
	h := newHarness(t, func(ctx context.Context, _ Job) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	got := watchEnds(h)
	j, _ := h.store.AddTask(Job{Name: "t", Kind: KindTask, Prompt: "p", ChatID: 1, Gateway: "web"}, nil, 3)
	h.sched.now = func() time.Time { return utc("2026-10-20 10:06") }
	done := make(chan struct{})
	go func() { h.sched.tick(context.Background()); h.sched.wg.Wait(); close(done) }()
	<-started
	if !h.sched.Cancel("web", 1, j.ID) {
		t.Fatal("the running task could not be cancelled")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled task did not end")
	}
	if len(*got) != 1 || (*got)[0].status != "cancelled" {
		t.Fatalf("got %+v", *got)
	}
}

func TestOnTickRunsAtEachLook(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "x", nil })
	n := 0
	h.sched.OnTick = func() { n++ }
	h.fireAt("2026-10-20 10:06")
	h.fireAt("2026-10-20 10:07")
	if n != 2 {
		t.Fatalf("OnTick ran %d times", n)
	}
}

func TestStartTaskAndTheLimit(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	var last error
	for i := range maxTasksPerChat + 1 {
		_, last = StartTask(s, nil, Job{Name: "t", Prompt: "p", ChatID: 5, Gateway: "web", Agent: "Writer", Card: "k"})
		if i < maxTasksPerChat && last != nil {
			t.Fatalf("task %d refused: %v", i, last)
		}
	}
	if last == nil || !IsBusy(last) {
		t.Fatalf("the task over the limit gave %v, want a busy error", last)
	}
	if !strings.Contains(last.Error(), "background tasks going") {
		t.Fatalf("the message changed: %q", last)
	}
	if IsBusy(errors.New("something else")) || IsBusy(nil) {
		t.Fatal("an unrelated error was taken for busy")
	}
	jobs, _ := s.List()
	if len(jobs) != maxTasksPerChat || jobs[0].Kind != KindTask || jobs[0].Agent != "Writer" || jobs[0].Card != "k" || jobs[0].TimeoutSec != defaultTaskMinutes*60 {
		t.Fatalf("jobs = %+v", jobs)
	}
	// Another chat has room of its own.
	if _, err := StartTask(s, nil, Job{Name: "t", Prompt: "p", ChatID: 6, Gateway: "web"}); err != nil {
		t.Fatal(err)
	}
}
