package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerWritesPlainLinesAndFiltersByLevel(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(newPlainHandler(&buf, slog.LevelWarn))
	log.Info("mcp: mail ready")
	log.Warn("mcp: db failed")
	log.Error("cannot start")
	out := buf.String()
	if strings.Contains(out, "mcp: mail ready") {
		t.Fatalf("info shown at warn level: %q", out)
	}
	if !strings.Contains(out, "warning: mcp: db failed\n") || !strings.Contains(out, "error: cannot start\n") {
		t.Fatalf("prefixes wrong: %q", out)
	}
}

func TestLoggerLevelComesFromTheEnvironment(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf, func(k string) string { return map[string]string{"TALARIA_LOG_LEVEL": "error"}[k] })
	log.Warn("hidden")
	log.Error("shown")
	if out := buf.String(); strings.Contains(out, "hidden") || !strings.Contains(out, "shown") {
		t.Fatalf("%q", out)
	}
	var buf2 bytes.Buffer
	newLogger(&buf2, func(string) string { return "verbose" })
	if !strings.Contains(buf2.String(), "TALARIA_LOG_LEVEL") {
		t.Fatalf("a bad level should warn and carry on: %q", buf2.String())
	}
}
