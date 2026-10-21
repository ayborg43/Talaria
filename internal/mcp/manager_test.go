package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"talaria/internal/tool"
)

func TestExposedName(t *testing.T) {
	taken := map[string]bool{}

	if got := exposedName("fs", "read_file", taken); got != "mcp_fs_read_file" {
		t.Fatal(got)
	}
	// Characters the model APIs reject become underscores.
	if got := exposedName("fs", "list dir/v2.0", taken); got != "mcp_fs_list_dir_v2_0" {
		t.Fatal(got)
	}
	// Two tools that sanitize to the same name stay distinct.
	a, b := exposedName("fs", "a.b", taken), exposedName("fs", "a b", taken)
	if a == b || a != "mcp_fs_a_b" || b != "mcp_fs_a_b_2" {
		t.Fatalf("%q %q", a, b)
	}

	// Long names are shortened to the 64 characters both APIs allow, stably and distinctly.
	long1 := exposedName("server", strings.Repeat("x", 100)+"1", map[string]bool{})
	long1again := exposedName("server", strings.Repeat("x", 100)+"1", map[string]bool{})
	long2 := exposedName("server", strings.Repeat("x", 100)+"2", map[string]bool{})
	if len(long1) != 64 || long1 != long1again || long1 == long2 {
		t.Fatalf("%q (%d) %q %q", long1, len(long1), long1again, long2)
	}
	for _, n := range []string{long1, long2, a, b} {
		if strings.Trim(n, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
			t.Errorf("%q contains characters the model APIs reject", n)
		}
	}
}

func TestManagerStartRegisterAndFailureIsolation(t *testing.T) {
	servers := map[string]Server{
		"good":     fakeProc("legacy", nil),
		"trusting": withTrust(fakeProc("legacy", nil)),
		"picky":    withAutoApprove(fakeProc("modern", nil), "echo"),
		"broken":   {Command: "/no/such/binary"},
		"off":      {Command: "/no/such/binary", Disabled: true},
	}
	m := Start(testCtx(t), servers, os.Getenv)
	defer m.Close()

	status := map[string]ServerStatus{}
	var order []string
	for _, st := range m.Status() {
		status[st.Name] = st
		order = append(order, st.Name)
	}
	if strings.Join(order, ",") != "broken,good,off,picky,trusting" {
		t.Fatalf("statuses should be sorted by name: %v", order)
	}
	if status["broken"].Err == nil || !strings.Contains(status["broken"].Err.Error(), "cannot start") {
		t.Fatalf("a broken server should report why: %+v", status["broken"])
	}
	if !status["off"].Disabled || status["off"].Err != nil {
		t.Fatalf("a disabled server is skipped without error: %+v", status["off"])
	}
	if status["good"].Err != nil || len(status["good"].Tools) != 9 || status["good"].Protocol != "2025-11-25" {
		t.Fatalf("one server failing must not affect the others: %+v", status["good"])
	}
	if !strings.Contains(status["picky"].Protocol, "2026-07-28") {
		t.Fatalf("%+v", status["picky"])
	}

	reg := tool.NewRegistry()
	m.Register(reg)
	approval := func(name string) bool {
		tl, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		return tl.NeedsApproval
	}
	if !approval("mcp_good_echo") {
		t.Error("tools of an untrusted server must ask first")
	}
	if approval("mcp_trusting_echo") || approval("mcp_trusting_fail") {
		t.Error("a trusted server's tools should not ask")
	}
	if approval("mcp_picky_echo") || !approval("mcp_picky_fail") {
		t.Error("auto_approve applies to the named tools only")
	}
	if _, ok := reg.Get("mcp_broken_echo"); ok {
		t.Error("a failed server registers nothing")
	}

	echo, _ := reg.Get("mcp_good_echo")
	if !strings.HasPrefix(echo.Description, `[MCP server "good"] Echo text back.`) || !strings.Contains(string(echo.Schema), `"text"`) {
		t.Fatalf("description %q schema %s", echo.Description, echo.Schema)
	}
	out, err := echo.Run(testCtx(t), json.RawMessage(`{"text":"through the registry"}`))
	if err != nil || out != "echo: through the registry" {
		t.Fatalf("%q %v", out, err)
	}
	fail, _ := reg.Get("mcp_picky_fail")
	if _, err := fail.Run(testCtx(t), json.RawMessage(`{"reason":"x"}`)); err == nil || err.Error() != "boom: x" {
		t.Fatalf("a tool's failure should reach the model verbatim: %v", err)
	}
}

func withTrust(s Server) Server { s.Trusted = true; return s }

func withAutoApprove(s Server, tools ...string) Server { s.AutoApprove = tools; return s }

func TestManagerCallTimeout(t *testing.T) {
	s := fakeProc("legacy", nil)
	s.TimeoutSeconds = 1
	m := Start(testCtx(t), map[string]Server{"slowpoke": s}, os.Getenv)
	defer m.Close()

	reg := tool.NewRegistry()
	m.Register(reg)
	slow, _ := reg.Get("mcp_slowpoke_slow")

	start := time.Now()
	_, err := slow.Run(testCtx(t), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "timed out after 1s") || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestManagerStartupTimeout(t *testing.T) {
	old := startupTimeout
	startupTimeout = 300 * time.Millisecond
	defer func() { startupTimeout = old }()

	// A command that starts fine but never speaks.
	m := Start(testCtx(t), map[string]Server{"mute": {Command: "sleep", Args: []string{"30"}}}, os.Getenv)
	defer m.Close()
	st := m.Status()[0]
	if st.Err == nil || !strings.Contains(st.Err.Error(), "did not finish starting") {
		t.Fatalf("a server that never answers should time out, got %v", st.Err)
	}
}

func TestNilManagerIsHarmless(t *testing.T) {
	var m *Manager
	m.Register(tool.NewRegistry())
	m.Close()
	if m.Status() != nil {
		t.Fatal("nil manager has no status")
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("\n  \nfirst line here\nsecond", 100); got != "first line here" {
		t.Fatal(got)
	}
	if got := FirstLine("abcdefghij", 4); got != "abcd…" {
		t.Fatal(got)
	}
	if FirstLine("", 10) != "" {
		t.Fatal("empty")
	}
}
