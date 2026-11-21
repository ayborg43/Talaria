package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"talaria/internal/cron"
	"talaria/internal/digest"
	"talaria/internal/guard"
	"talaria/internal/runlog"
	"talaria/internal/web"
)

func TestDigestScheduleRoundTripAndWindow(t *testing.T) {
	store := &cron.Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	zone := time.UTC
	set, get := setDigest(store, zone), webDigest(store, zone)

	if get().Enabled {
		t.Fatal("on before it was asked for")
	}
	if err := set(true, "25:61", false); err == nil {
		t.Fatal("a bad time was accepted")
	}
	if err := set(true, "08:30", false); err != nil {
		t.Fatal(err)
	}
	if d := get(); !d.Enabled || d.At != "08:30" || d.Weekly {
		t.Fatalf("%+v", d)
	}
	// Turning it on again replaces the job: there is never more than one.
	if err := set(true, "07:00", true); err != nil {
		t.Fatal(err)
	}
	jobs, _ := store.List()
	n := 0
	for _, j := range jobs {
		if j.Name == digestName {
			n++
		}
	}
	if d := get(); n != 1 || d.At != "07:00" || !d.Weekly {
		t.Fatalf("%d jobs, %+v", n, d)
	}
	if err := set(false, "", false); err != nil || get().Enabled {
		t.Fatalf("not turned off: %v", err)
	}

	now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	daily := cron.Job{Schedule: "CRON_TZ=UTC 0 8 * * *"}
	weekly := cron.Job{Schedule: "CRON_TZ=UTC 0 8 * * 1"}
	if got := digestWindow(daily, now); got != now.Add(-24*time.Hour) {
		t.Fatalf("a first daily digest covers the day: %v", got)
	}
	if got := digestWindow(weekly, now); got != now.Add(-7*24*time.Hour) {
		t.Fatalf("a first weekly digest covers the week: %v", got)
	}
	last := now.Add(-26 * time.Hour)
	daily.LastRun = last
	if got := digestWindow(daily, now); got != last {
		t.Fatalf("it covers the time since it last ran: %v", got)
	}
	daily.LastRun = now.Add(-90 * 24 * time.Hour) // a long silence is not reported in full
	if got := digestWindow(daily, now); got != now.Add(-24*time.Hour) {
		t.Fatalf("%v", got)
	}
}

func TestDigestIsWrittenFromWhatIsKept(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	// The activity log: one allowed call by a job, one refused.
	log := &guard.Log{Path: filepath.Join(home, "audit.log")}
	log.Append(guard.Record{Time: now.Add(-time.Hour), Origin: "job:a1", Tool: "email_triage", Allowed: true, Via: "default", Status: "ok"})
	log.Append(guard.Record{Time: now.Add(-30 * time.Minute), Origin: "job:a1", Tool: "shell", Allowed: false, Via: "rule", Status: "denied"})
	log.Append(guard.Record{Time: now.Add(-48 * time.Hour), Origin: "chat", Tool: "read_file", Allowed: true, Via: "default", Status: "ok"}) // before the window
	// A finished background run.
	runs := &runlog.Log{Path: filepath.Join(home, "runs.json")}
	id := runs.Begin("j1", "Check the build", "task", "web", web.ChatID, "check it", "m", "")
	runs.End(id, runlog.Done, "The build is green.")

	src := digestSources{home: home, zone: time.UTC, runs: runs, waiting: func() (int, int) { return 2, 1 }}
	d := src.build(now.Add(-6*time.Hour), now.Add(time.Minute))
	for _, want := range []string{"Done: Check the build: The build is green.", "2 tool calls, 2 of them on its own", "email_triage", "1 call was refused", "2 questions are waiting", "1 finished task is waiting"} {
		if !strings.Contains(d.Text, want) {
			t.Errorf("missing %q in:\n%s", want, d.Text)
		}
	}
	if strings.Contains(d.Text, "read_file") {
		t.Error("a call from before the window is in the digest")
	}
	quiet := digestSources{home: t.TempDir(), zone: time.UTC}.build(now.Add(-time.Hour), now)
	if !quiet.Empty {
		t.Fatalf("%+v", quiet)
	}
	var _ digest.Digest = quiet
}
