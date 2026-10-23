package telegram

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// offsetFile is the persisted poll cursor: the update_id the next getUpdates
// must start after. Without it a restart replays every unacknowledged update,
// running the agent twice on each.
type offsetFile struct {
	Offset int `json:"offset"`
}

// loadOffset reads the saved cursor. A missing path means no persistence (the
// gateway starts from zero, as before); a missing or corrupt file also starts
// from zero, loudly.
func loadOffset(path string, logf func(string, ...any)) int {
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		logf("telegram: cannot read the saved poll offset: %v; updates may replay", err)
		return 0
	}
	var f offsetFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Offset < 0 {
		logf("telegram: %s is corrupt (fix or delete it): updates may replay", path)
		return 0
	}
	return f.Offset
}

// saveOffset persists the cursor after an update is handled, via a temp file
// and rename so a crash never leaves half a file. A failure is logged, not
// fatal: polling carries on, and the next update saves again.
func saveOffset(path string, offset int, logf func(string, ...any)) {
	if path == "" {
		return
	}
	raw, err := json.Marshal(offsetFile{Offset: offset})
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logf("telegram: cannot save the poll offset: %v", err)
		return
	}
	tmp, err := os.CreateTemp(dir, "offset-*.tmp")
	if err != nil {
		logf("telegram: cannot save the poll offset: %v", err)
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		logf("telegram: cannot save the poll offset: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		logf("telegram: cannot save the poll offset: %v", err)
		return
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		logf("telegram: cannot save the poll offset: %v", err)
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		logf("telegram: cannot save the poll offset: %v", err)
		return
	}
}
