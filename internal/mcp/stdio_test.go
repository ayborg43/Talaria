package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// fakeProc describes the test binary re-run as a server.
func fakeProc(mode string, env map[string]string) Server {
	e := map[string]string{"MCP_FAKE_SERVER": mode}
	for k, v := range env {
		e[k] = v
	}
	return Server{Command: os.Args[0], Env: e}
}

func connect(t *testing.T, s Server) *Client {
	t.Helper()
	c, err := Connect(testCtx(t), "fake", s, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func call(t *testing.T, c *Client, tool string, args string) (string, error) {
	t.Helper()
	return c.Call(testCtx(t), tool, json.RawMessage(args))
}

func TestStdioLegacyEndToEnd(t *testing.T) {
	c := connect(t, fakeProc("legacy", nil))
	if c.Protocol() != "2025-11-25" {
		t.Fatalf("protocol %q", c.Protocol())
	}
	names := map[string]bool{}
	for _, tool := range c.Tools() {
		names[tool.Name] = true
	}
	if len(c.Tools()) != 9 || !names["echo"] || !names["slow"] {
		t.Fatalf("tools: %+v", c.Tools())
	}

	out, err := call(t, c, "echo", `{"text":"hi"}`)
	if err != nil || out != "echo: hi" {
		t.Fatalf("echo: %q %v", out, err)
	}
	if _, err := call(t, c, "fail", `{"reason":"because"}`); err == nil || err.Error() != "boom: because" {
		t.Fatalf("a tool's own failure message should come through, got %v", err)
	}
	if out, _ := call(t, c, "image", `{}`); out != "here\n[image image/png, 8 bytes, not shown]" {
		t.Fatalf("image: %q", out)
	}
	if out, _ := call(t, c, "structured", `{}`); out != `{"n":1}` {
		t.Fatalf("structured content should be used when there is no text: %q", out)
	}
	if out, _ := call(t, c, "resource", `{}`); out != "[resource file:///a.txt]\nfile body\n[resource link: file:///b (b)]" {
		t.Fatalf("resource: %q", out)
	}
	if _, err := call(t, c, "nope", `{}`); err == nil || !strings.Contains(err.Error(), "Unknown tool") {
		t.Fatalf("protocol errors should surface: %v", err)
	}
	if _, err := call(t, c, "echo", `[1,2]`); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("non-object arguments: %v", err)
	}
}

func TestStdioModernServerIsDetected(t *testing.T) {
	c := connect(t, fakeProc("modern", nil))
	if c.Protocol() != "2026-07-28 (stateless)" {
		t.Fatalf("protocol %q", c.Protocol())
	}
	if out, err := call(t, c, "echo", `{"text":"stateless"}`); err != nil || out != "echo: stateless" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := call(t, c, "needs_input", `{}`); err == nil || !strings.Contains(err.Error(), "interactive input") {
		t.Fatalf("input_required cannot be served and should say so: %v", err)
	}
}

func TestStdioNegotiatesOlderAndRejectsUnknownVersions(t *testing.T) {
	if c := connect(t, fakeProc("legacy", map[string]string{"MCP_FAKE_VERSION": "2024-11-05"})); c.Protocol() != "2024-11-05" {
		t.Fatalf("protocol %q", c.Protocol())
	}
	_, err := Connect(testCtx(t), "fake", fakeProc("legacy", map[string]string{"MCP_FAKE_VERSION": "1999-01-01"}), os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("got %v", err)
	}
}

func TestStdioPaginationAndStdoutNoise(t *testing.T) {
	c := connect(t, fakeProc("legacy", map[string]string{"MCP_FAKE_PAGE": "2", "MCP_FAKE_NOISE": "1"}))
	if len(c.Tools()) != 9 {
		t.Fatalf("all pages should be read, got %d tools", len(c.Tools()))
	}
	if out, err := call(t, c, "echo", `{"text":"x"}`); err != nil || out != "echo: x" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestStdioEnvironmentIsIsolated(t *testing.T) {
	t.Setenv("SECRET_TOKEN", "do-not-leak")
	t.Setenv("FROM_PARENT", "configured-value")
	s := fakeProc("legacy", map[string]string{"CONFIGURED_VAR": "${FROM_PARENT}"})
	c := connect(t, s)

	out, err := call(t, c, "env", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "SECRET= ") {
		t.Fatalf("the parent's secrets must not reach the server: %q", out)
	}
	if !strings.Contains(out, "CONFIGURED=configured-value") || !strings.Contains(out, "HOME_SET=true") {
		t.Fatalf("configured and basic variables should arrive: %q", out)
	}
}

func TestStdioMissingVariableAndMissingCommand(t *testing.T) {
	s := fakeProc("legacy", map[string]string{"X": "${DEFINITELY_NOT_SET_12345}"})
	if _, err := Connect(testCtx(t), "fake", s, os.Getenv); err == nil || !strings.Contains(err.Error(), "DEFINITELY_NOT_SET_12345") {
		t.Fatalf("got %v", err)
	}
	if _, err := Connect(testCtx(t), "fake", Server{Command: "/no/such/binary"}, os.Getenv); err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("got %v", err)
	}
}

func TestStdioCrashReportsStderr(t *testing.T) {
	_, err := Connect(testCtx(t), "fake", fakeProc("crashstart", nil), os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "cannot open database") || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("a server that dies at startup should explain why: %v", err)
	}

	c := connect(t, fakeProc("legacy", nil))
	_, err = call(t, c, "crash", `{}`)
	if err == nil || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("a server that dies mid-call should explain why: %v", err)
	}
	if _, err := call(t, c, "echo", `{"text":"x"}`); err == nil {
		t.Fatal("calls after the server died must fail, not hang")
	}
}

func TestStdioCancellationReachesTheServer(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "cancelled")
	c := connect(t, fakeProc("legacy", map[string]string{"MCP_FAKE_CANCEL_FILE": marker}))

	ctx, cancel := context.WithTimeout(testCtx(t), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Call(ctx, "slow", json.RawMessage(`{}`))
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("a cancelled call must return promptly, err=%v after %v", err, time.Since(start))
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			// The connection is still healthy afterwards.
			if out, err := call(t, c, "echo", `{"text":"still here"}`); err != nil || out != "echo: still here" {
				t.Fatalf("%q %v", out, err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the server never received notifications/cancelled")
}

func TestStdioConcurrentCalls(t *testing.T) {
	c := connect(t, fakeProc("legacy", nil))
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := c.Call(testCtx(t), "echo", json.RawMessage(fmt.Sprintf(`{"text":"n%d"}`, i)))
			if err != nil || out != fmt.Sprintf("echo: n%d", i) {
				errs <- fmt.Errorf("call %d: %q %v", i, out, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestStdioCloseStopsServers(t *testing.T) {
	old := stopGrace
	defer func() { stopGrace = old }()

	// A server that honours the closing of stdin exits on its own, long before it would be killed.
	// (The generous bound allows for the race detector's one-second pause at process exit.)
	stopGrace = 5 * time.Second
	c, err := Connect(testCtx(t), "fake", fakeProc("legacy", nil), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	c.Close()
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("a well-behaved server should exit when stdin closes, not wait for the kill (took %v)", d)
	}

	// One that ignores it is killed once the grace period runs out.
	stopGrace = 150 * time.Millisecond
	stubborn, err := Connect(testCtx(t), "fake", fakeProc("stubborn", nil), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	stubborn.Close()
	if d := time.Since(start); d < 100*time.Millisecond || d > 3*time.Second {
		t.Fatalf("expected a kill after the grace period, took %v", d)
	}
}
