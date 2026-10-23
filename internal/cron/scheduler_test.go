package cron

import (
	"context"
	"errors"
	"strings"
	"sync"
	"talaria/internal/activity"
	"talaria/internal/metrics"
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

// A claimed one-shot was deleted from the store; if the shutdown kills it mid
// run, a note is the only trace left, so one is delivered.
func TestShutdownReportsAOneShotThatDiedWithIt(t *testing.T) {
	started := make(chan struct{})
	h := newHarness(t, func(ctx context.Context, _ Job) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	h.store.AddNow(Job{Kind: KindTask, Name: "report", Prompt: "p", ChatID: 1})
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
	if len(h.got) != 1 || !strings.Contains(h.got[0].text, "report") || !strings.Contains(h.got[0].text, "shut down") {
		t.Fatalf("a task that died with the shutdown was not reported: %+v", h.got)
	}
}

// A run that ignores cancellation must not hold the shutdown forever.
func TestRunDoesNotWaitForeverForJobsThatIgnoreCancellation(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) {
		select {} // never returns, whatever the context does
	})
	h.store.Add(Job{Schedule: "@every 5m", Prompt: "p", ChatID: 1})
	h.sched.now = func() time.Time { return utc("2026-10-20 10:06") }
	h.sched.Tick = 10 * time.Millisecond
	h.sched.ShutdownTimeout = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sched.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	started := 0
	for {
		h.mu.Lock()
		started = len(h.execed)
		h.mu.Unlock()
		if started > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if started == 0 {
		t.Fatal("the job never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run waited forever for a run that ignores cancellation")
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

func TestJobsAreCountedByKindAndWhatBecameOfThem(t *testing.T) {
	metrics.Reset()
	h := newHarness(t, func(_ context.Context, j Job) (string, error) {
		switch j.Prompt {
		case "quiet":
			return Silent, nil
		case "broken":
			return "", errors.New("boom")
		case "gone":
			return "", ErrDrop
		}
		return "hello", nil
	})
	for _, p := range []string{"quiet", "broken", "gone", "loud"} {
		h.store.AddNow(Job{Name: p, Kind: KindWatch, Watch: "w", Prompt: p, ChatID: 2})
	}
	h.store.AddNow(Job{Name: "plain", Prompt: "loud", ChatID: 2})
	h.fireAt("2026-10-20 10:00")
	for result, want := range map[string]float64{"silent": 1, "error": 1, "dropped": 1, "delivered": 1} {
		if got := metrics.Get("talaria_jobs_total", "kind", "watch", "result", result); got != want {
			t.Errorf("watch %s: %v, want %v", result, got, want)
		}
	}
	if metrics.Get("talaria_jobs_total", "kind", "job", "result", "delivered") != 1 {
		t.Error("an ordinary job is kind job")
	}
}

func TestAPausedSchedulerHoldsEveryJobAndRunsThemWhenResumed(t *testing.T) {
	h := newHarness(t, func(context.Context, Job) (string, error) { return "done", nil })
	paused := true
	h.sched.Paused = func() bool { return paused }
	h.store.Add(Job{Name: "inbox", Schedule: "@every 5m", Prompt: "check mail", ChatID: 42})

	h.fireAt("2026-10-20 10:06")
	h.fireAt("2026-10-20 10:30")
	if len(h.got) != 0 || len(h.execed) != 0 {
		t.Fatalf("a paused scheduler ran something: %+v %v", h.got, h.execed)
	}
	jobs, _ := h.store.List()
	if len(jobs) != 1 || jobs[0].LastRun.After(utc("2026-10-20 10:00")) {
		t.Fatalf("the job was claimed while paused: %+v", jobs)
	}

	paused = false
	h.fireAt("2026-10-20 10:31")
	if len(h.got) != 1 || h.got[0].chat != 42 {
		t.Fatalf("the held job did not run after resuming: %+v", h.got)
	}
}

type workSink struct {
	mu  sync.Mutex
	log []string
}

func (w *workSink) add(x string) { w.mu.Lock(); w.log = append(w.log, x); w.mu.Unlock() }
func (w *workSink) Start(kind, label string) int64 {
	w.add("start " + kind + " " + label)
	return 99
}
func (w *workSink) Doing(int64, string) {}
func (w *workSink) Stop(id int64)       { w.add("stop") }
func (w *workSink) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.log...)
}

func TestEachRunIsReportedAsWorkWhileItRunsAndItsNumberIsPassedOn(t *testing.T) {
	var seenID int64
	var during []string
	var sink *workSink
	h := newHarness(t, func(ctx context.Context, j Job) (string, error) {
		seenID = activity.ID(ctx)
		during = sink.lines() // what was reported by the time the job is in the middle of running
		return "done", nil
	})
	sink = &workSink{}
	h.sched.Activity = sink
	h.store.Add(Job{Name: "inbox", Schedule: "@every 5m", Prompt: "check mail", ChatID: 42, Kind: KindTask})
	h.fireAt("2026-10-20 10:06")

	if seenID != 99 {
		t.Fatalf("the run did not get its worker number: %d", seenID)
	}
	if strings.Join(during, "|") != "start task inbox" {
		t.Fatalf("while it ran, the page had been told: %v", during)
	}
	if all := strings.Join(sink.lines(), "|"); all != "start task inbox|stop" {
		t.Fatalf("%s", all)
	}
}

func TestARunThatFailsIsStillEndedAndKindsAreNamed(t *testing.T) {
	sink := &workSink{}
	h := newHarness(t, func(context.Context, Job) (string, error) { return "", errors.New("boom") })
	h.sched.Activity = sink
	h.store.Add(Job{Name: "x", Schedule: "@every 5m", Prompt: "p", ChatID: 1})
	h.fireAt("2026-10-20 10:06")
	if all := strings.Join(sink.lines(), "|"); all != "start job x|stop" {
		t.Fatalf("%s", all)
	}
	for kind, want := range map[string]string{KindTask: "task", KindGoal: "goal", KindWatch: "watch", "": "job"} {
		if got := workerKind(Job{Kind: kind}); got != want {
			t.Errorf("%q: %q", kind, got)
		}
	}
}
