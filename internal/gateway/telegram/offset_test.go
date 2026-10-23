package telegram

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"talaria/internal/provider"
)

func TestOffsetRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offset.json")
	logged := 0
	logf := func(string, ...any) { logged++ }
	if got := loadOffset(path, logf); got != 0 {
		t.Fatalf("a missing file reads as %d", got)
	}
	saveOffset(path, 42, logf)
	if got := loadOffset(path, logf); got != 42 {
		t.Fatalf("round trip gave %d", got)
	}
	if logged != 0 {
		t.Fatalf("logged %d times on the happy path", logged)
	}
	raw, _ := os.ReadFile(path)
	if len(raw) == 0 || raw[0] != '{' {
		t.Fatalf("not JSON: %q", raw)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestCorruptOffsetStartsFromZeroLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offset.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	logged := 0
	if got := loadOffset(path, func(string, ...any) { logged++ }); got != 0 {
		t.Fatalf("a corrupt file reads as %d", got)
	}
	if logged == 0 {
		t.Fatal("a corrupt offset was swallowed silently")
	}
	// And an empty path disables persistence without touching the disk.
	saveOffset("", 7, func(string, ...any) { t.Fatal("logged with no path") })
	if got := loadOffset("", func(string, ...any) { t.Fatal("logged with no path") }); got != 0 {
		t.Fatalf("no path reads as %d", got)
	}
}

// A restart resumes polling where the last run left off, so handled updates
// are not replayed (and re-run) again.
func TestRestartResumesThePollCursor(t *testing.T) {
	f := newFake(t)
	path := filepath.Join(t.TempDir(), "offset.json")
	var calls atomic.Int32
	ag := echoAgent(&scripted{replies: []provider.Message{{Role: provider.RoleAssistant, Content: "hello back"}}}, &calls)
	withOffset := func(gw *Gateway) { gw.OffsetPath = path }

	stop := start(t, f, []int64{1}, ag, withOffset)
	f.updates <- []Update{userMsg(41, 1, 1, "private", "hi")}
	f.waitFor("reply", func() bool { return f.sawText("hello back") })
	stop()
	if got := loadOffset(path, t.Logf); got != 42 {
		t.Fatalf("saved offset is %d, want 42", got)
	}

	f.mu.Lock()
	polls := len(f.offsets)
	f.mu.Unlock()
	start(t, f, []int64{1}, ag, withOffset)
	f.waitFor("resumed poll", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.offsets) > polls
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.offsets[polls:] {
		if o != 42 {
			t.Fatalf("a restarted gateway polled from %d, want 42", o)
		}
	}
}
