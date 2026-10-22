package activity

import (
	"context"
	"sync"
	"testing"
)

type rec struct {
	mu  sync.Mutex
	log []string
}

func (r *rec) add(s string) { r.mu.Lock(); r.log = append(r.log, s); r.mu.Unlock() }
func (r *rec) Start(kind, label string) int64 {
	r.add("start " + kind + " " + label)
	return 7
}
func (r *rec) Doing(id int64, what string) { r.add("doing " + what) }
func (r *rec) Stop(id int64)               { r.add("stop") }

func TestNobodyWatchingIsFine(t *testing.T) {
	id, end := Begin(nil, Sub, "x")
	if id != 0 {
		t.Fatalf("%d", id)
	}
	end() // must not panic
	Note(nil, 5, "anything")
	var s Sink
	Note(s, 5, "anything")
}

func TestAWorkerIsStartedNotedAndEnded(t *testing.T) {
	r := &rec{}
	id, end := Begin(r, Task, "report")
	Note(r, id, "web_fetch")
	Note(r, 0, "ignored: no worker") // a worker number of 0 means there is none
	end()
	if got := r.log; len(got) != 3 || got[0] != "start task report" || got[1] != "doing web_fetch" || got[2] != "stop" {
		t.Fatalf("%v", got)
	}
}

func TestTheWorkersNumberTravelsWithTheContext(t *testing.T) {
	if ID(context.Background()) != 0 {
		t.Fatal("a context with no worker has one")
	}
	if got := ID(With(context.Background(), 42)); got != 42 {
		t.Fatalf("%d", got)
	}
}
