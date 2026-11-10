package main

import (
	"path/filepath"
	"testing"
	"time"

	"talaria/internal/cron"
)

func TestTheMorningBriefIsOneJobThatCanBeMovedAndRemoved(t *testing.T) {
	store := &cron.Store{Path: filepath.Join(t.TempDir(), "jobs.json")}
	lagos, _ := time.LoadLocation("Africa/Lagos")
	get, set := webBrief(store, lagos), setBrief(store, lagos)

	if get().Enabled {
		t.Fatal("the brief is on before anyone asked")
	}
	if err := set(true, "07:30"); err != nil {
		t.Fatal(err)
	}
	if b := get(); !b.Enabled || b.At != "07:30" || b.Next.IsZero() {
		t.Fatalf("after turning it on: %+v", b)
	}
	if err := set(true, "06:05"); err != nil { // moving it replaces the job
		t.Fatal(err)
	}
	jobs, _ := store.List()
	if len(jobs) != 1 || get().At != "06:05" {
		t.Fatalf("moving it left %d jobs, at %q", len(jobs), get().At)
	}
	if err := set(true, "7:3x"); err == nil {
		t.Fatal("a bad time was accepted")
	}
	if err := set(false, ""); err != nil {
		t.Fatal(err)
	}
	if jobs, _ = store.List(); len(jobs) != 0 || get().Enabled {
		t.Fatalf("turning it off left %d jobs", len(jobs))
	}
}
