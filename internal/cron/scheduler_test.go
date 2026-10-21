package cron

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type delivery struct {
	chat int64
	text string
}

// harness runs one scheduler tick at a chosen time and records deliveries.
type harness struct {
	t      *testing.T
	store  *Store
	sched  *Scheduler
	mu     sync.Mutex
	got    []delivery
	execed []string
}

func newHarness(t *testing.T, exec func(context.Context, Job) (string, error)) *harness {
	h := &harness{t: t, store: newStore(t, utc("2026-10-20 10:00"))}
	h.sched = &Scheduler{
		Store: h.store,
		Exec: func(ctx context.Context, j Job) (string, error) {
			h.mu.Lock()
			h.execed = append(h.execed, j.Prompt)
			h.mu.Unlock()
			return exec(ctx, j)
		},
		Deliver: func(_ context.Context, chat int64, text string) {
			h.mu.Lock()
			h.got = append(h.got, delivery{chat, text})
			h.mu.Unlock()
		},
	}
	return h
}

func (h *harness) fireAt(now string) {
	h.sched.now = func() time.Time { return utc(now) }
	h.sched.tick(context.Background())
	h.sched.wg.Wait()
}

func TestDeliversResultToJobsChat(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "  3 new emails  ", nil })
	h.store.Add(Job{Name: "inbox", Schedule: "@every 5m", Prompt: "check mail", ChatID: 42})

	h.fireAt("2026-10-20 10:06")
	if len(h.got) != 1 || h.got[0].chat != 42 || h.got[0].text != "⏰ inbox\n\n3 new emails" {
		t.Fatalf("unexpected delivery: %+v", h.got)
	}
	jobs, _ := h.store.List()
	if jobs[0].LastStatus != "ok" || jobs[0].LastRun.IsZero() {
		t.Fatalf("run not recorded: %+v", jobs[0])
	}
}

func TestSilentRepliesAreNotDelivered(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "[SILENT] nothing changed", nil })
	h.store.Add(Job{Schedule: "@every 5m", Prompt: "watch", ChatID: 1})

	h.fireAt("2026-10-20 10:06")
	if len(h.execed) != 1 || len(h.got) != 0 {
		t.Fatalf("job should run silently: execed=%v got=%+v", h.execed, h.got)
	}
	if jobs, _ := h.store.List(); jobs[0].LastStatus != "ok (silent)" {
		t.Fatalf("status %q", jobs[0].LastStatus)
	}
}

func TestFailuresAreReported(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "", errors.New("model unreachable") })
	h.store.Add(Job{Name: "report", Schedule: "@every 5m", Prompt: "p", ChatID: 9})

	h.fireAt("2026-10-20 10:06")
	if len(h.got) != 1 || !strings.Contains(h.got[0].text, "report failed: model unreachable") {
		t.Fatalf("failure not delivered: %+v", h.got)
	}
	// A failing job keeps its schedule.
	if jobs, _ := h.store.List(); len(jobs) != 1 || !strings.HasPrefix(jobs[0].LastStatus, "error:") {
		t.Fatalf("job state: %+v", jobs)
	}
}

func TestOneShotRunsOnceThenIsGone(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "time to stretch", nil })
	h.store.Add(Job{Schedule: "@at 2026-10-20T10:30:00Z", Prompt: "remind me", ChatID: 1})

	h.fireAt("2026-10-20 10:29")
	h.fireAt("2026-10-20 10:31")
	h.fireAt("2026-10-20 10:40")
	if len(h.execed) != 1 || len(h.got) != 1 {
		t.Fatalf("one-shot ran %d times", len(h.execed))
	}
	if jobs, _ := h.store.List(); len(jobs) != 0 {
		t.Fatalf("one-shot not removed: %+v", jobs)
	}
}

func TestErrDropRemovesJobWithoutDelivery(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "", ErrDrop })
	h.store.Add(Job{Schedule: "@every 5m", Prompt: "p", ChatID: 1})

	h.fireAt("2026-10-20 10:06")
	if len(h.got) != 0 {
		t.Fatalf("dropped job delivered: %+v", h.got)
	}
	if jobs, _ := h.store.List(); len(jobs) != 0 {
		t.Fatalf("dropped job still stored: %+v", jobs)
	}
}

func TestOverlappingRunsAreSkipped(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	h := newHarness(t, func(context.Context, Job) (string, error) {
		started <- struct{}{}
		<-release
		return "done", nil
	})
	h.store.Add(Job{Schedule: "@every 5m", Prompt: "slow", ChatID: 1})

	h.sched.now = func() time.Time { return utc("2026-10-20 10:06") }
	h.sched.tick(context.Background())
	<-started
	// The job is still running when it comes due again.
	h.sched.now = func() time.Time { return utc("2026-10-20 10:12") }
	h.sched.tick(context.Background())
	close(release)
	h.sched.wg.Wait()

	if len(h.execed) != 1 {
		t.Fatalf("job ran %d times concurrently, want 1", len(h.execed))
	}
}

func TestRunStopsOnCancelAndDoesNotReportCancelledJobs(t *testing.T) {
	started := make(chan struct{})
	h := newHarness(t, func(ctx context.Context, _ Job) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	h.store.Add(Job{Schedule: "@every 5m", Prompt: "p", ChatID: 1})
	h.sched.now = func() time.Time { return utc("2026-10-20 10:06") }
	h.sched.Tick = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sched.Run(ctx); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if len(h.got) != 0 {
		t.Fatalf("shutdown reported as a job failure: %+v", h.got)
	}
}

func TestRunTimeout(t *testing.T) {
	h := newHarness(t, func(ctx context.Context, _ Job) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	h.sched.Timeout = 20 * time.Millisecond
	h.store.Add(Job{Name: "hang", Schedule: "@every 5m", Prompt: "p", ChatID: 1})

	h.fireAt("2026-10-20 10:06")
	if len(h.got) != 1 || !strings.Contains(h.got[0].text, "hang failed") {
		t.Fatalf("timeout should be reported as a failure: %+v", h.got)
	}
}
