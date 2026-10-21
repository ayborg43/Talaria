package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func serveHTTP(t *testing.T, h *fakeHTTP) Server {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return Server{URL: srv.URL}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHTTPLegacySessionFlow(t *testing.T) {
	t.Setenv("TEST_API_KEY", "k-123")
	h := &fakeHTTP{f: newFake("legacy"), sessions: true}
	s := serveHTTP(t, h)
	s.Headers = map[string]string{"X-Api-Key": "${TEST_API_KEY}"}

	c, err := Connect(testCtx(t), "remote", s, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if c.Protocol() != "2025-11-25" || len(c.Tools()) != 9 {
		t.Fatalf("protocol %q, %d tools", c.Protocol(), len(c.Tools()))
	}
	if out, err := call(t, c, "echo", `{"text":"over http"}`); err != nil || out != "echo: over http" {
		t.Fatalf("%q %v", out, err)
	}

	init := h.requests("initialize")[0].Header
	if init.Get("Mcp-Session-Id") != "" || init.Get("MCP-Protocol-Version") != "" {
		t.Fatalf("initialize must not carry a session or version yet: %v", init)
	}
	if a := init.Get("Accept"); !strings.Contains(a, "application/json") || !strings.Contains(a, "text/event-stream") {
		t.Fatalf("Accept must list both content types, got %q", a)
	}
	later := h.requests("tools/call")[0].Header
	if later.Get("Mcp-Session-Id") != "sess-1" || later.Get("MCP-Protocol-Version") != "2025-11-25" || later.Get("X-Api-Key") != "k-123" {
		t.Fatalf("later requests need the session, the version and the configured headers: %v", later)
	}
	if h.requests("notifications/initialized")[0].Header.Get("Mcp-Session-Id") != "sess-1" {
		t.Fatal("the initialized notification belongs to the session")
	}

	c.Close()
	if h.deletes != 1 {
		t.Fatalf("closing should end the session with a DELETE, saw %d", h.deletes)
	}
}

func TestHTTPEventStreamReplies(t *testing.T) {
	h := &fakeHTTP{f: newFake("legacy"), sessions: true, sse: true}
	c, err := Connect(testCtx(t), "remote", serveHTTP(t, h), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if out, err := call(t, c, "echo", `{"text":"streamed"}`); err != nil || out != "echo: streamed" {
		t.Fatalf("a reply that arrives as an event stream: %q %v", out, err)
	}
	// The server pinged us inside the stream; a client must answer.
	waitFor(t, "the ping answer", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.replies) == 1 && string(h.replies[0].ID) == `"srv-ping"` && string(h.replies[0].Result) == `{}`
	})
}

func TestHTTPSessionExpiryIsRecovered(t *testing.T) {
	h := &fakeHTTP{f: newFake("legacy"), sessions: true}
	c, err := Connect(testCtx(t), "remote", serveHTTP(t, h), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	h.mu.Lock()
	h.expire = true // the server forgets us before the next request
	h.mu.Unlock()

	out, err := call(t, c, "echo", `{"text":"after expiry"}`)
	if err != nil || out != "echo: after expiry" {
		t.Fatalf("an expired session should be re-established transparently: %q %v", out, err)
	}
	if n := h.f.count("initialize"); n != 2 {
		t.Fatalf("expected one re-initialization, saw %d initializes", n)
	}
	if got := h.requests("tools/call")[1].Header.Get("Mcp-Session-Id"); got != "sess-2" {
		t.Fatalf("the retry should use the new session, used %q", got)
	}
}

func TestHTTPModernServer(t *testing.T) {
	h := &fakeHTTP{f: newFake("modern")}
	c, err := Connect(testCtx(t), "remote", serveHTTP(t, h), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Protocol() != "2026-07-28 (stateless)" {
		t.Fatalf("protocol %q", c.Protocol())
	}

	if out, err := call(t, c, "echo", `{"text":"hi"}`); err != nil || out != "echo: hi" {
		t.Fatalf("%q %v", out, err)
	}
	r := h.requests("tools/call")[0]
	if r.Header.Get("MCP-Protocol-Version") != "2026-07-28" || r.Header.Get("Mcp-Method") != "tools/call" || r.Header.Get("Mcp-Name") != "echo" {
		t.Fatalf("modern requests must mirror the method and tool name into headers: %v", r.Header)
	}
	if r.Header.Get("Mcp-Session-Id") != "" {
		t.Fatal("the stateless revision has no sessions")
	}
	var body struct {
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	json.Unmarshal(r.Body, &body)
	if body.Params.Meta["io.modelcontextprotocol/protocolVersion"] != "2026-07-28" || body.Params.Meta["io.modelcontextprotocol/clientCapabilities"] == nil {
		t.Fatalf("every modern request carries _meta: %s", r.Body)
	}
	if n := len(h.requests("notifications/initialized")); n != 0 {
		t.Fatalf("no handshake in the stateless revision, saw %d initialized notifications", n)
	}
}

// Whether a modern-only server answers a legacy initialize with a JSON error,
// a bare 400, a 404 or a 405, the client must fall back to server/discover.
func TestHTTPModernFallbackWhateverTheLegacyRejection(t *testing.T) {
	for _, status := range []int{0, 400, 404, 405} {
		h := &fakeHTTP{f: newFake("modern"), rejectLegacy: status}
		c, err := Connect(testCtx(t), "remote", serveHTTP(t, h), os.Getenv)
		if err != nil {
			t.Fatalf("legacy rejection %d: %v", status, err)
		}
		if !strings.Contains(c.Protocol(), "2026-07-28") {
			t.Fatalf("legacy rejection %d: protocol %q", status, c.Protocol())
		}
		c.Close()
	}
}

func TestHTTPMirroredToolParameters(t *testing.T) {
	f := newFake("modern")
	f.extraTools = []map[string]any{
		tl("regional", "Region-aware.", `{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"},"query":{"type":"string"}}}`),
		tl("badnumber", "Bad annotation.", `{"type":"object","properties":{"n":{"type":"number","x-mcp-header":"N"}}}`),
		tl("nested", "Hidden in an array.", `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string","x-mcp-header":"Id"}}}}}}`),
	}
	h := &fakeHTTP{f: f}
	c, err := Connect(testCtx(t), "remote", serveHTTP(t, h), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	have := map[string]bool{}
	for _, tool := range c.Tools() {
		have[tool.Name] = true
	}
	if !have["regional"] || have["badnumber"] || have["nested"] {
		t.Fatalf("tools with invalid x-mcp-header annotations must be dropped: %v", have)
	}
	if len(c.Warnings) != 2 {
		t.Fatalf("each dropped tool should be reported: %v", c.Warnings)
	}

	if out, err := call(t, c, "regional", `{"region":"us-west1","query":"select 1"}`); err != nil || out != "region=us-west1" {
		t.Fatalf("%q %v", out, err)
	}
	if got := h.requests("tools/call")[0].Header.Get("Mcp-Param-Region"); got != "us-west1" {
		t.Fatalf("Mcp-Param-Region = %q", got)
	}
	// A value that is not plain ASCII travels base64-encoded, and the server decodes it back.
	if out, err := call(t, c, "regional", `{"region":"Zürich"}`); err != nil || out != "region=Zürich" {
		t.Fatalf("%q %v", out, err)
	}
	if got := h.requests("tools/call")[1].Header.Get("Mcp-Param-Region"); got != "=?base64?WsO8cmljaA==?=" {
		t.Fatalf("Mcp-Param-Region = %q", got)
	}
	// No value, no header.
	call(t, c, "regional", `{"query":"x"}`)
	if got := h.requests("tools/call")[2].Header.Get("Mcp-Param-Region"); got != "" {
		t.Fatalf("a missing argument must not produce a header, got %q", got)
	}
}

func TestHTTPAuthenticationFailureIsClearAndNotRetried(t *testing.T) {
	h := &fakeHTTP{f: newFake("legacy"), auth: "Bearer right"}
	s := serveHTTP(t, h)
	s.Headers = map[string]string{"Authorization": "Bearer wrong"}

	_, err := Connect(testCtx(t), "remote", s, os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "authentication failed") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("got %v", err)
	}
	if h.hits != 1 {
		t.Fatalf("a 401 must not trigger the modern fallback; saw %d requests", h.hits)
	}

	s.Headers = map[string]string{"Authorization": "Bearer right"}
	c, err := Connect(testCtx(t), "remote", s, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestHTTPOldSSETransportIsReportedClearly(t *testing.T) {
	h := &fakeHTTP{f: newFake("legacy"), status: 405} // a 2024-11-05 HTTP+SSE server has no POST endpoint here
	_, err := Connect(testCtx(t), "remote", serveHTTP(t, h), os.Getenv)
	if err == nil || !strings.Contains(err.Error(), "HTTP+SSE") {
		t.Fatalf("got %v", err)
	}
}

func TestHTTPCancellation(t *testing.T) {
	// Legacy servers are told with a notification.
	hl := &fakeHTTP{f: newFake("legacy"), sessions: true}
	cl, err := Connect(testCtx(t), "remote", serveHTTP(t, hl), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := cl.Call(ctx, "slow", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected the call to be cancelled")
	}
	waitFor(t, "the cancel notification", func() bool { return len(hl.requests("notifications/cancelled")) == 1 })

	// Stateless servers treat the dropped connection as the cancellation: nothing else is sent.
	hm := &fakeHTTP{f: newFake("modern")}
	cm, err := Connect(testCtx(t), "remote", serveHTTP(t, hm), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, err := cm.Call(ctx2, "slow", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected the call to be cancelled")
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(hm.requests("notifications/cancelled")); n != 0 {
		t.Fatalf("the stateless binding sends no cancel notification, saw %d", n)
	}
}
