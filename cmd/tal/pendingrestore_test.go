package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talaria/internal/backup"
)

func TestPendingRestoreIsAppliedOnceAndKeepsTheOldState(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	os.MkdirAll(src, 0o700)
	os.WriteFile(filepath.Join(src, "config.json"), []byte(`{"from":"old server"}`), 0o600)
	var buf bytes.Buffer
	if _, err := backup.Create(src, &buf, backup.Options{}); err != nil {
		t.Fatal(err)
	}

	home := filepath.Join(t.TempDir(), "data")
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"from":"new server"}`), 0o600)
	os.WriteFile(filepath.Join(home, backup.PendingName), buf.Bytes(), 0o600)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	applyPendingRestore(home, log)
	if b, _ := os.ReadFile(filepath.Join(home, "config.json")); string(b) != `{"from":"old server"}` {
		t.Fatalf("config %s", b)
	}
	if _, err := os.Stat(filepath.Join(home, backup.PendingName)); !os.IsNotExist(err) {
		t.Fatal("the pending file should be gone, or the next start would restore again")
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".before-restore-*", "config.json"))
	if len(matches) != 1 {
		t.Fatalf("the old state should be kept aside: %v", matches)
	}
}

func TestBadPendingRestoreChangesNothingAndIsSetAside(t *testing.T) {
	home := filepath.Join(t.TempDir(), "data")
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "config.json"), []byte("keep"), 0o600)
	os.WriteFile(filepath.Join(home, backup.PendingName), []byte("junk"), 0o600)
	applyPendingRestore(home, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if b, _ := os.ReadFile(filepath.Join(home, "config.json")); !strings.Contains(string(b), "keep") {
		t.Fatal("state changed")
	}
	if _, err := os.Stat(filepath.Join(home, backup.PendingName+".failed")); err != nil {
		t.Fatal("a bad backup should be set aside, not retried at every start")
	}
}
