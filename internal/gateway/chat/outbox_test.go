package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func shrinkRetry(t *testing.T) {
	t.Helper()
	old := sendRetryWait
	sendRetryWait = time.Millisecond
	t.Cleanup(func() { sendRetryWait = old })
}

// A message the service refused is kept on disk, and goes out once the
// service works again — even for an outbox opened later, as after a restart.
func TestOutboxKeepsFailedPartsAndSendsThemLater(t *testing.T) {
	shrinkRetry(t)
	path := filepath.Join(t.TempDir(), "outbox.json")
	f := newFakeService(4000, false)
	f.failSends = 1 << 30 // the service is down

	DeliverQueue(context.Background(), f, 7, "job output", nil, &Outbox{Path: path})
	if ev := f.log(); len(ev) != 0 {
		t.Fatalf("nothing should have gone out: %+v", ev)
	}

	// A fresh outbox on the same file, as after a restart, still has it.
	box := &Outbox{Path: path}
	entry, ok, err := box.Claim()
	if err != nil || !ok || entry.Chat != 7 || entry.Text != "job output" || entry.Attempts != 1 {
		t.Fatalf("%+v %v %v", entry, ok, err)
	}

	f.failSends = 0 // the service is back
	e := &Engine{T: f, Outbox: box}
	e.drainOutbox(context.Background())
	if ev := f.log(); len(ev) != 1 || ev[0].Text != "job output" {
		t.Fatalf("%+v", ev)
	}
	if _, ok, err := box.Claim(); err != nil || ok {
		t.Fatalf("delivered means forgotten: %v %v", ok, err)
	}
}

// Queued messages go out oldest first, and past the cap the oldest is dropped
// to make room.
func TestOutboxIsFirstInFirstOutWithACap(t *testing.T) {
	box := &Outbox{Path: filepath.Join(t.TempDir(), "outbox.json")}
	for i := 0; i < maxOutboxEntries+1; i++ {
		if err := box.Enqueue(1, string(rune('a'+i%26))+strings.Repeat("x", i)); err != nil {
			t.Fatal(err)
		}
	}
	var texts []string
	for {
		e, ok, err := box.Claim()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		texts = append(texts, e.Text)
		if err := box.Done(e.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(texts) != maxOutboxEntries {
		t.Fatalf("%d entries left, want %d", len(texts), maxOutboxEntries)
	}
	if texts[0] == "a" {
		t.Fatal("the oldest entry should have been dropped past the cap")
	}
	for k, text := range texts { // lengths grow with i, so increasing length proves FIFO order
		if len(text) != k+2 {
			t.Fatalf("entry %d has length %d, want %d", k, len(text), k+2)
		}
	}
}

// A message tried too often is dropped with a log line, not retried forever.
func TestOutboxDropsAfterMaxAttempts(t *testing.T) {
	shrinkRetry(t)
	path := filepath.Join(t.TempDir(), "outbox.json")
	raw, _ := json.Marshal(map[string]any{"entries": []map[string]any{{
		"id": "old", "chat": 3, "text": "stale", "queued_at": time.Now().Add(-time.Hour).Format(time.RFC3339), "attempts": maxOutboxAttempts,
	}}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var logged []string
	f := newFakeService(4000, false)
	f.failSends = 1 << 30
	e := &Engine{T: f, Outbox: &Outbox{Path: path}, Logf: func(format string, args ...any) { logged = append(logged, format) }}
	e.drainOutbox(context.Background())
	if _, ok, err := e.Outbox.Claim(); err != nil || ok {
		t.Fatalf("an exhausted entry should be gone: %v %v", ok, err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "dropping a queued message") {
		t.Fatalf("%v", logged)
	}
}

// Concurrent senders do not lose or corrupt queued messages.
func TestOutboxConcurrentEnqueues(t *testing.T) {
	box := &Outbox{Path: filepath.Join(t.TempDir(), "outbox.json")}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if err := box.Enqueue(int64(w), "m"); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	n := 0
	for {
		e, ok, err := box.Claim()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		n++
		if err := box.Done(e.ID); err != nil {
			t.Fatal(err)
		}
	}
	if n != 80 {
		t.Fatalf("%d entries, want 80", n)
	}
}

// Without an outbox, a failed send is still logged and dropped, as before.
func TestSendWithoutAnOutboxDropsAfterRetries(t *testing.T) {
	shrinkRetry(t)
	f := newFakeService(4000, false)
	f.failSends = 1 << 30
	var logged []string
	e := &Engine{T: f, Logf: func(format string, args ...any) { logged = append(logged, format) }}
	e.Send(context.Background(), 1, "hi")
	if len(logged) != 1 || !strings.Contains(logged[0], "dropping a message after retries") {
		t.Fatalf("%v", logged)
	}
}
