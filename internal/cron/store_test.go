package cron

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newStore(t *testing.T, now time.Time) *Store {
	return &Store{Path: filepath.Join(t.TempDir(), "cron", "jobs.json"), Now: func() time.Time { return now }}
}

func TestStoreAddListRemove(t *testing.T) {
	now := utc("2026-10-20 10:00")
	s := newStore(t, now)

	j, err := s.Add(Job{Schedule: "0 9 * * *", Prompt: "  summarize the news  ", ChatID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if j.ID == "" || j.Name != "summarize the news" || !j.NextRun.Equal(utc("2026-10-21 09:00")) {
		t.Fatalf("unexpected job: %+v", j)
	}

	// A second Store on the same file sees it: state is on disk.
	other := &Store{Path: s.Path}
	jobs, err := other.List()
	if err != nil || len(jobs) != 1 || jobs[0].ID != j.ID {
		t.Fatalf("not persisted: %+v %v", jobs, err)
	}
	if fi, _ := os.Stat(s.Path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("jobs file mode %v, want 0600", fi.Mode().Perm())
	}

	// Another chat cannot remove it; the owner (and chat 0 = admin) can.
	if err := s.Remove(j.ID, "", 8); err == nil {
		t.Fatal("chat 8 removed chat 7's job")
	}
	if err := s.Remove(j.ID, "", 7); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.List(); len(jobs) != 0 {
		t.Fatalf("job still listed: %+v", jobs)
	}
}

func TestStoreAddValidation(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	for name, j := range map[string]Job{
		"empty prompt":    {Schedule: "@daily", Prompt: " ", ChatID: 1},
		"huge prompt":     {Schedule: "@daily", Prompt: strings.Repeat("x", maxPrompt+1), ChatID: 1},
		"no chat":         {Schedule: "@daily", Prompt: "p"},
		"bad schedule":    {Schedule: "nope", Prompt: "p", ChatID: 1},
		"past one-shot":   {Schedule: "@at 2026-10-19T09:00:00Z", Prompt: "p", ChatID: 1},
		"impossible date": {Schedule: "0 0 31 2 *", Prompt: "p", ChatID: 1},
	} {
		if _, err := s.Add(j); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
	if jobs, _ := s.List(); len(jobs) != 0 {
		t.Fatal("rejected jobs were stored")
	}
}

func TestStoreJobCap(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	for i := 0; i < maxJobs; i++ {
		if _, err := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1}); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("expected cap error, got %v", err)
	}
}

func TestClaimDue(t *testing.T) {
	now := utc("2026-10-20 10:00")
	s := newStore(t, now)
	every, _ := s.Add(Job{Schedule: "@every 10m", Prompt: "recurring", ChatID: 1})
	once, _ := s.Add(Job{Schedule: "@at 2026-10-20T10:05:00Z", Prompt: "one-shot", ChatID: 1})
	later, _ := s.Add(Job{Schedule: "@daily", Prompt: "tomorrow", ChatID: 1})

	if due, err := s.ClaimDue(utc("2026-10-20 10:04"), ""); err != nil || len(due) != 0 {
		t.Fatalf("nothing should be due yet: %+v %v", due, err)
	}

	// 20 minutes pass (say the gateway was down): each job fires once, not once per missed slot.
	at := utc("2026-10-20 10:20")
	due, err := s.ClaimDue(at, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, j := range due {
		got[j.ID] = true
	}
	if len(due) != 2 || !got[every.ID] || !got[once.ID] || got[later.ID] {
		t.Fatalf("wrong due set: %+v", due)
	}

	jobs, _ := s.List()
	if len(jobs) != 3 {
		t.Fatalf("claimed jobs stay stored under lease, have %d jobs", len(jobs))
	}
	for _, j := range jobs {
		if j.ID == every.ID && !j.NextRun.Equal(utc("2026-10-20 10:30")) {
			t.Fatalf("recurring job next run %v, want 10:30", j.NextRun)
		}
		if j.ID == once.ID && (j.ClaimedAt.IsZero() || !j.LeaseUntil.After(at)) {
			t.Fatalf("one-shot not held under lease: %+v", j)
		}
	}

	// Claiming again at the same instant finds nothing: a live lease is not handed out twice.
	if due, _ := s.ClaimDue(at, ""); len(due) != 0 {
		t.Fatalf("job claimed twice: %+v", due)
	}
}

func TestClaimDueParksInvalidSchedule(t *testing.T) {
	now := utc("2026-10-20 10:00")
	s := newStore(t, now)
	j, _ := s.Add(Job{Schedule: "@every 10m", Prompt: "p", ChatID: 1})

	// Simulate a hand edit that broke the schedule.
	raw, _ := os.ReadFile(s.Path)
	os.WriteFile(s.Path, []byte(strings.Replace(string(raw), "@every 10m", "garbage", 1)), 0o600)

	due, err := s.ClaimDue(utc("2026-10-20 11:00"), "")
	if err != nil || len(due) != 0 {
		t.Fatalf("invalid job must not run: %+v %v", due, err)
	}
	jobs, _ := s.List()
	if len(jobs) != 1 || jobs[0].ID != j.ID || !strings.Contains(jobs[0].LastStatus, "invalid schedule") {
		t.Fatalf("invalid job should be kept and flagged: %+v", jobs)
	}
}

func TestCorruptFileIsNotOverwritten(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	os.MkdirAll(filepath.Dir(s.Path), 0o755)
	os.WriteFile(s.Path, []byte("{not json"), 0o600)

	if _, err := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1}); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("expected corrupt error, got %v", err)
	}
	if raw, _ := os.ReadFile(s.Path); string(raw) != "{not json" {
		t.Fatal("corrupt file was overwritten")
	}
}

func TestFinish(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	j, _ := s.Add(Job{Schedule: "@daily", Prompt: "p", ChatID: 1})
	at := utc("2026-10-20 12:00")
	if err := s.Finish(j.ID, at, "ok"); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish("gone", at, "ok"); err != nil {
		t.Fatalf("finishing a deleted job should be a no-op: %v", err)
	}
	jobs, _ := s.List()
	if jobs[0].LastStatus != "ok" || !jobs[0].LastRun.Equal(at) {
		t.Fatalf("outcome not recorded: %+v", jobs[0])
	}
}

// The CLI and the gateway share the jobs file through separate Store handles
// (separate locks, possibly separate processes). Concurrent writers must lose
// nothing and mint no duplicate ID.
func TestConcurrentWritersFromTwoHandlesLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	a := &Store{Path: path, Now: func() time.Time { return utc("2026-10-20 10:00") }}
	b := &Store{Path: path, Now: func() time.Time { return utc("2026-10-20 10:00") }}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); a.AddNow(Job{Prompt: "a", ChatID: 1}) }()
		go func() { defer wg.Done(); b.AddNow(Job{Prompt: "b", ChatID: 1}) }()
	}
	wg.Wait()
	jobs, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 40 {
		t.Fatalf("%d jobs survived, want 40", len(jobs))
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		if seen[j.ID] {
			t.Fatalf("duplicate job ID %s", j.ID)
		}
		seen[j.ID] = true
	}
}

// Rescheduling a goal replaces its one check-in job atomically: concurrent
// reschedules cannot leave none or two.
func TestConcurrentGoalReschedulesLeaveExactlyOne(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.ScheduleGoal("g", "telegram", 7, "g", time.Hour, false, "", "")
		}()
	}
	wg.Wait()
	jobs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, j := range jobs {
		if j.Kind == KindGoal && j.Goal == "g" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d check-in jobs for one goal", n)
	}
}

// Two task_start calls in one turn race for the last free slot; exactly one
// may take it.
func TestConcurrentTaskStartsShareTheLastSlot(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	for i := 0; i < 2; i++ {
		if _, err := s.AddNow(Job{Kind: KindTask, Prompt: "p", ChatID: 7, Gateway: "telegram"}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.AddTask(Job{Kind: KindTask, Prompt: "p", ChatID: 7, Gateway: "telegram"}, nil, 3)
			if err == nil {
				won.Add(1)
			} else if !strings.Contains(err.Error(), "already has") {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("%d racers won the last slot", won.Load())
	}
	jobs, _ := s.List()
	n := 0
	for _, j := range jobs {
		if j.Kind == KindTask {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d tasks stored", n)
	}
}

func TestAClaimedOneShotStaysStoredUnderLeaseUntilItFinishes(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	once, _ := s.Add(Job{Schedule: "@at 2026-10-20T10:05:00Z", Prompt: "one-shot", ChatID: 1})
	at := utc("2026-10-20 10:06")
	due, err := s.ClaimDue(at, "")
	if err != nil || len(due) != 1 {
		t.Fatalf("%+v %v", due, err)
	}
	jobs, _ := s.List()
	if len(jobs) != 1 || jobs[0].ClaimedAt.IsZero() || !jobs[0].LeaseUntil.After(at) || jobs[0].Attempts != 1 {
		t.Fatalf("a claimed one-shot must stay stored, leased: %+v", jobs)
	}
	if due, _ := s.ClaimDue(at, ""); len(due) != 0 {
		t.Fatalf("a leased job was claimed twice: %+v", due)
	}
	if err := s.Finish(once.ID, at, "ok"); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.List(); len(jobs) != 0 {
		t.Fatalf("a finished one-shot should be gone: %+v", jobs)
	}
}

func TestAnExpiredLeaseHandsTheJobOutAgain(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	once, _ := s.Add(Job{Schedule: "@at 2026-10-20T10:05:00Z", Prompt: "one-shot", ChatID: 1})
	at := utc("2026-10-20 10:06")
	if due, _ := s.ClaimDue(at, ""); len(due) != 1 {
		t.Fatalf("%+v", due)
	}
	// The process dies without Finish. While the lease lives, nothing moves.
	if due, _ := s.ClaimDue(at.Add(5*time.Minute), ""); len(due) != 0 {
		t.Fatalf("claimed again inside its lease: %+v", due)
	}
	// After it, the same job is handed out again, counting the new attempt.
	later := at.Add(16 * time.Minute)
	due, err := s.ClaimDue(later, "")
	if err != nil || len(due) != 1 || due[0].ID != once.ID || due[0].Attempts != 2 {
		t.Fatalf("expired lease not requeued: %+v %v", due, err)
	}
	_ = once
}

func TestAFailedOneShotRetriesWithBackoffThenIsDropped(t *testing.T) {
	s := newStore(t, utc("2026-10-20 10:00"))
	j, _ := s.AddNow(Job{Kind: KindTask, Name: "report", Prompt: "p", ChatID: 1})
	at := utc("2026-10-20 10:01")
	if due, _ := s.ClaimDue(at, ""); len(due) != 1 {
		t.Fatalf("%+v", due)
	}
	if err := s.Finish(j.ID, at, "error: model unreachable"); err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.List()
	if len(jobs) != 1 || !jobs[0].NextRun.Equal(at.Add(time.Minute)) || !jobs[0].ClaimedAt.IsZero() {
		t.Fatalf("first failure should retry in a minute, unclaimed: %+v", jobs)
	}
	if due, _ := s.ClaimDue(at.Add(30*time.Second), ""); len(due) != 0 {
		t.Fatalf("retried before its backoff: %+v", due)
	}
	at2 := at.Add(time.Minute)
	if due, _ := s.ClaimDue(at2, ""); len(due) != 1 || due[0].Attempts != 2 {
		t.Fatalf("second attempt not handed out: %+v", due)
	}
	if err := s.Finish(j.ID, at2, "error: model unreachable"); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.List(); len(jobs) != 1 || !jobs[0].NextRun.Equal(at2.Add(2*time.Minute)) {
		t.Fatalf("second failure should wait two minutes: %+v", jobs)
	}
	at3 := at2.Add(2 * time.Minute)
	if due, _ := s.ClaimDue(at3, ""); len(due) != 1 || due[0].Attempts != 3 {
		t.Fatalf("third attempt not handed out: %+v", due)
	}
	if err := s.Finish(j.ID, at3, "error: model unreachable"); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.List(); len(jobs) != 0 {
		t.Fatalf("a one-shot that failed %d times should be dropped: %+v", MaxAttempts, jobs)
	}
}
