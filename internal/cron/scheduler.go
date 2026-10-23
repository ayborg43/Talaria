package cron

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"talaria/internal/activity"
	"talaria/internal/metrics"
	"time"
)

// ErrDrop, returned by Exec, deletes the job and delivers nothing. Use it for
// jobs that must no longer run (for example, their owner lost access).
var ErrDrop = errors.New("drop job")

// Silent is the reply a job gives when there is nothing worth reporting.
const Silent = "[SILENT]"

// Scheduler fires due jobs and delivers their results.
type Scheduler struct {
	Store *Store
	// Gateway limits the scheduler to one gateway's jobs, which Deliver then
	// sends through. Empty runs every job.
	Gateway string
	// Exec runs a job's prompt unattended and returns the text to deliver.
	Exec func(ctx context.Context, j Job) (string, error)
	// Deliver sends text to a chat.
	Deliver func(ctx context.Context, chatID int64, text string)
	Logf    func(format string, args ...any)
	// Activity, if set, is told when a job begins and ends, and the context given to Exec carries its
	// worker number (activity.ID), so what runs it can say what it is doing.
	Activity activity.Sink
	// Paused, if set and true, holds every job: none is claimed, so each stays due and runs when
	// the scheduler is no longer paused.
	Paused func() bool

	Tick        time.Duration // how often to look for due jobs; default 15s
	Timeout     time.Duration // per-run limit; default 10m
	MaxTimeout  time.Duration // the most a job may ask for with its own timeout; default 2h
	MaxParallel int           // concurrent runs; default 2
	// ShutdownTimeout bounds how long Run waits for in-flight runs after ctx
	// ends; default 30s. Runs that ignore cancellation are abandoned after it.
	ShutdownTimeout time.Duration

	now     func() time.Time // tests override
	sem     chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	running map[string]bool
	runs    map[string]*run
	wake    chan struct{}
	wakeMu  sync.Once
}

// run is a job that has been claimed and has not yet finished.
type run struct {
	job       Job
	started   time.Time // when it began running; zero while it waits for a free slot
	queuedAt  time.Time
	cancel    context.CancelFunc
	cancelled bool
}

// RunInfo describes a job that is waiting for a slot or running.
type RunInfo struct {
	ID, Name, Kind string
	Running        bool // false while it waits its turn
	Since          time.Time
}

func (s *Scheduler) wakeChan() chan struct{} {
	s.wakeMu.Do(func() { s.wake = make(chan struct{}, 1) })
	return s.wake
}

// Wake makes the scheduler look for due jobs now instead of at its next tick.
// A background task uses it so it starts at once.
func (s *Scheduler) Wake() {
	select {
	case s.wakeChan() <- struct{}{}:
	default:
	}
}

// Runs lists the jobs of one chat that are running or waiting to, oldest first.
func (s *Scheduler) Runs(gateway string, chatID int64) []RunInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RunInfo
	for _, r := range s.runs {
		if r.job.ChatID == chatID && r.job.Gateway == gateway {
			since := r.queuedAt
			if !r.started.IsZero() {
				since = r.started
			}
			out = append(out, RunInfo{ID: r.job.ID, Name: r.job.Name, Kind: r.job.Kind, Running: !r.started.IsZero(), Since: since})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}

// Cancel stops a running job of that chat, or one waiting for a slot. It says
// whether there was one.
func (s *Scheduler) Cancel(gateway string, chatID int64, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil || r.job.ChatID != chatID || r.job.Gateway != gateway {
		return false
	}
	r.cancelled = true
	r.cancel()
	return true
}

func (s *Scheduler) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Scheduler) timeNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Run checks for due jobs until ctx ends, then waits for in-flight runs.
func (s *Scheduler) Run(ctx context.Context) {
	if s.Tick == 0 {
		s.Tick = 15 * time.Second
	}
	t := time.NewTicker(s.Tick)
	defer t.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			s.waitForRuns()
			return
		case <-t.C:
		case <-s.wakeChan():
		}
	}
}

// waitForRuns waits for in-flight runs, for at most ShutdownTimeout: a job
// that ignores cancellation must not hold the shutdown forever.
func (s *Scheduler) waitForRuns() {
	grace := s.ShutdownTimeout
	if grace <= 0 {
		grace = 30 * time.Second
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
		s.logf("cron: shutdown timed out waiting for runs; abandoning them")
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	if s.sem == nil {
		n := s.MaxParallel
		if n <= 0 {
			n = 2
		}
		s.sem = make(chan struct{}, n)
		s.running = map[string]bool{}
		s.runs = map[string]*run{}
	}
	if s.Paused != nil && s.Paused() {
		return
	}
	due, err := s.Store.ClaimDue(s.timeNow(), s.Gateway)
	if err != nil {
		s.logf("cron: %v", err)
		return
	}
	for _, j := range due {
		s.mu.Lock()
		busy := s.running[j.ID]
		s.running[j.ID] = true
		rctx, cancel := context.WithCancel(ctx)
		r := &run{job: j, queuedAt: s.timeNow(), cancel: cancel}
		if !busy {
			s.runs[j.ID] = r
		}
		s.mu.Unlock()
		if busy {
			cancel()
			s.logf("cron: job %s is still running; skipping this run", j.ID)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer cancel()
			defer func() {
				s.mu.Lock()
				delete(s.running, j.ID)
				delete(s.runs, j.ID)
				s.mu.Unlock()
			}()
			select {
			case s.sem <- struct{}{}:
				defer func() { <-s.sem }()
			case <-rctx.Done():
				switch {
				case ctx.Err() == nil: // cancelled while waiting for a slot
					s.finishCancelled(ctx, j)
				case !s.Store.Exists(j.ID): // shutdown before it started, and claiming deleted it
					s.deliverShutdownNote(j, "Talaria shut down before it started; start it again if you still want it")
				}
				return
			}
			s.mu.Lock()
			r.started = s.timeNow()
			s.mu.Unlock()
			s.runJob(ctx, rctx, r, j)
		}()
	}
}

// timeoutFor is how long a job may run: its own limit, within the scheduler's
// maximum, else the scheduler's default.
func (s *Scheduler) timeoutFor(j Job) time.Duration {
	def := s.Timeout
	if def == 0 {
		def = 10 * time.Minute
	}
	if j.TimeoutSec <= 0 {
		return def
	}
	most := s.MaxTimeout
	if most == 0 {
		most = 2 * time.Hour
	}
	return min(time.Duration(j.TimeoutSec)*time.Second, most)
}

// say writes what a job delivers, in the form its kind calls for.
func say(j Job, ok bool, body string) string {
	switch j.Kind {
	case KindTask:
		if !ok {
			return fmt.Sprintf("⚠️ Task %s failed: %s", j.Name, body)
		}
		return fmt.Sprintf("✅ Task %s\n\n%s", j.Name, body)
	case KindGoal:
		if !ok {
			return fmt.Sprintf("🎯 %s: the check-in failed: %s", j.Name, body)
		}
		return fmt.Sprintf("🎯 %s\n\n%s", j.Name, body)
	case KindWatch:
		if !ok {
			return fmt.Sprintf("👀 %s: looking at the change failed: %s", j.Name, body)
		}
		return fmt.Sprintf("👀 %s\n\n%s", j.Name, body)
	}
	if !ok {
		return fmt.Sprintf("⏰ %s failed: %s", j.Name, body)
	}
	return fmt.Sprintf("⏰ %s\n\n%s", j.Name, body)
}

// finishCancelled reports a job that was cancelled before it started.
func (s *Scheduler) finishCancelled(ctx context.Context, j Job) {
	if err := s.Store.Finish(j.ID, s.timeNow(), "cancelled"); err != nil {
		s.logf("cron: %v", err)
	}
	s.Deliver(ctx, j.ChatID, fmt.Sprintf("🛑 %s was cancelled before it started.", j.Name))
}

// deliverShutdownNote tells the chat that a claimed one-shot died with the
// shutdown. It uses a fresh context: the scheduler's own is already done.
func (s *Scheduler) deliverShutdownNote(j Job, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.Deliver(ctx, j.ChatID, say(j, false, why))
}

func (s *Scheduler) runJob(ctx, rctx context.Context, r *run, j Job) {
	runCtx, cancel := context.WithTimeout(rctx, s.timeoutFor(j))
	defer cancel()

	started := s.timeNow()
	id, end := activity.Begin(s.Activity, workerKind(j), j.Name)
	runCtx = activity.With(runCtx, id)
	reply, err := s.Exec(runCtx, j)
	end()
	if ctx.Err() != nil {
		// Shutting down: not a failure of the job, so a recurring job stays
		// silent and runs again. But a claimed one-shot was deleted from the
		// store, so without a note here the user would never learn it died.
		// Dropped jobs stay silent whatever stopped them.
		if !errors.Is(err, ErrDrop) && !s.Store.Exists(j.ID) {
			s.deliverShutdownNote(j, "Talaria shut down while it was running; start it again if you still want it")
		}
		return
	}
	s.mu.Lock()
	cancelled := r.cancelled
	s.mu.Unlock()
	if cancelled { // the user stopped it: say so, whatever it was in the middle of
		if err := s.Store.Finish(j.ID, started, "cancelled"); err != nil {
			s.logf("cron: %v", err)
		}
		s.Deliver(ctx, j.ChatID, fmt.Sprintf("🛑 %s was cancelled.", j.Name))
		return
	}
	kind := j.Kind
	if kind == "" {
		kind = "job"
	}
	switch {
	case errors.Is(err, ErrDrop):
		metrics.Inc("talaria_jobs_total", "kind", kind, "result", "dropped")
	case err != nil:
		metrics.Inc("talaria_jobs_total", "kind", kind, "result", "error")
	case strings.HasPrefix(strings.TrimSpace(reply), Silent):
		metrics.Inc("talaria_jobs_total", "kind", kind, "result", "silent")
	default:
		metrics.Inc("talaria_jobs_total", "kind", kind, "result", "delivered")
	}
	if errors.Is(err, ErrDrop) {
		s.logf("cron: dropping job %s", j.ID)
		if err := s.Store.Remove(j.ID, "", 0); err != nil {
			s.logf("cron: %v", err)
		}
		return
	}

	status, text := "ok", ""
	switch {
	case err != nil && errors.Is(err, context.DeadlineExceeded) && runCtx.Err() != nil:
		status = "error: timed out"
		text = say(j, false, fmt.Sprintf("it ran out of time (%s)", s.timeoutFor(j)))
	case err != nil:
		status = "error: " + err.Error()
		text = say(j, false, err.Error())
	case strings.HasPrefix(strings.TrimSpace(reply), Silent):
		status = "ok (silent)"
	default:
		text = say(j, true, strings.TrimSpace(reply))
	}
	if err := s.Store.Finish(j.ID, started, status); err != nil {
		s.logf("cron: %v", err)
	}
	if text != "" {
		s.Deliver(ctx, j.ChatID, text)
	}
}

// workerKind is how a job is named to a front end that shows who is working.
func workerKind(j Job) string {
	switch j.Kind {
	case KindTask:
		return activity.Task
	case KindGoal:
		return activity.Goal
	case KindWatch:
		return activity.Watch
	}
	return activity.Job
}
