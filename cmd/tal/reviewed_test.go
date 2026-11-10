package main

import (
	"path/filepath"
	"testing"
)

func TestCheckedOffTasksAreRememberedAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	r := newReviewedSet(path)
	if r.Has("t1") {
		t.Fatal("a task is checked off before anyone did")
	}
	if err := r.Add("t1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Add("t1"); err != nil { // twice changes nothing
		t.Fatal(err)
	}
	if !newReviewedSet(path).Has("t1") {
		t.Fatal("a restart forgot it")
	}
	for i := 0; i < maxReviewed+10; i++ {
		r.Add(string(rune('A'+i%26)) + string(rune('a'+i/26)))
	}
	if len(r.ids) != maxReviewed {
		t.Fatalf("kept %d ids, want %d", len(r.ids), maxReviewed)
	}
}
