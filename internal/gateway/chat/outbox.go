package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"talaria/internal/filelock"
)

const (
	// maxOutboxEntries is how many unsent messages are kept; past that the
	// oldest is dropped to make room, so a long outage cannot grow the file
	// without bound.
	maxOutboxEntries = 100
	// maxOutboxAttempts is how many times one message is retried before it is
	// dropped: about eight hours of five-minute retries.
	maxOutboxAttempts = 100
	// maxOutboxDrain is how many queued messages one drain pass attempts.
	maxOutboxDrain = 10
)

// outboxDrainInterval is how often the engine retries its queued messages. A
// variable so tests can shrink it.
var outboxDrainInterval = 5 * time.Minute

// outboxEntry is one message that could not be delivered.
type outboxEntry struct {
	ID       string    `json:"id"`
	Chat     int64     `json:"chat"`
	Text     string    `json:"text"`
	QueuedAt time.Time `json:"queued_at"`
	Attempts int       `json:"attempts"`
}

// Outbox keeps messages that could not be delivered, on disk, so a chat
// service being down does not eat a job's output. The zero value is useless;
// make one with a Path. It is safe for concurrent use, across processes too.
type Outbox struct {
	Path string

	mu sync.Mutex
}

func outboxID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Enqueue keeps a message to try again later.
func (o *Outbox) Enqueue(chat int64, text string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	release, err := lockOutbox(o.Path)
	if err != nil {
		return err
	}
	defer release()
	entries, err := loadOutbox(o.Path)
	if err != nil {
		return err
	}
	entries = append(entries, outboxEntry{ID: outboxID(), Chat: chat, Text: text, QueuedAt: time.Now(), Attempts: 0})
	for len(entries) > maxOutboxEntries {
		entries = entries[1:]
	}
	return saveOutbox(o.Path, entries)
}

// Claim returns the oldest queued message and records an attempt on it, so a
// crash between claiming and sending does not retry it forever. ok is false
// when nothing is queued.
func (o *Outbox) Claim() (entry outboxEntry, ok bool, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	release, err := lockOutbox(o.Path)
	if err != nil {
		return outboxEntry{}, false, err
	}
	defer release()
	entries, err := loadOutbox(o.Path)
	if err != nil {
		return outboxEntry{}, false, err
	}
	if len(entries) == 0 {
		return outboxEntry{}, false, nil
	}
	entries[0].Attempts++
	if err := saveOutbox(o.Path, entries); err != nil {
		return outboxEntry{}, false, err
	}
	return entries[0], true, nil
}

// Done forgets a queued message, after it was delivered or dropped.
func (o *Outbox) Done(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	release, err := lockOutbox(o.Path)
	if err != nil {
		return err
	}
	defer release()
	entries, err := loadOutbox(o.Path)
	if err != nil {
		return err
	}
	kept := entries[:0]
	for _, e := range entries {
		if e.ID != id {
			kept = append(kept, e)
		}
	}
	return saveOutbox(o.Path, kept)
}

func lockOutbox(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return filelock.Lock(path)
}

func loadOutbox(path string) ([]outboxEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f struct {
		Entries []outboxEntry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s is corrupt (fix or delete it): %w", path, err)
	}
	return f.Entries, nil
}

// saveOutbox writes via a temp file and rename so a crash never leaves half a file.
func saveOutbox(path string, entries []outboxEntry) error {
	raw, err := json.MarshalIndent(struct {
		Entries []outboxEntry `json:"entries"`
	}{entries}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "outbox-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil { // a job's output may be private
		return err
	}
	return os.Rename(tmp.Name(), path)
}
