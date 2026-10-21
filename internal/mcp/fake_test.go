package mcp

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake MCP server that behaves like either protocol era. It runs two ways:
// as a subprocess (this test binary re-executed with MCP_FAKE_SERVER set),
// to test the stdio transport and process handling for real, and behind an
// httptest server, to test Streamable HTTP.

func TestMain(m *testing.M) {
	if mode := os.Getenv("MCP_FAKE_SERVER"); mode != "" {
		runFakeStdio(mode)
		return
	}
	os.Exit(m.Run())
}

type fakeServer struct {
	era        string // "legacy" or "modern"
	version    string // legacy: version to answer initialize with; "" echoes the client's
	supported  []string
	pageSize   int
	extraTools []map[string]any
	cancelFile string

	mu          sync.Mutex
	initialized bool
	methods     map[string]int
	cancels     []string
	slow        map[string]chan struct{}
}

func newFake(era string) *fakeServer {
	return &fakeServer{era: era, methods: map[string]int{}, slow: map[string]chan struct{}{}}
}

func tl(name, desc, schema string) map[string]any {
	return map[string]any{"name": name, "description": desc, "inputSchema": json.RawMessage(schema)}
}

func (f *fakeServer) allTools() []map[string]any {
	base := []map[string]any{
		tl("echo", "Echo text back.\nSecond line.", `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		tl("fail", "Always fails.", `{"type":"object","properties":{"reason":{"type":"string"}}}`),
		tl("image", "Returns an image.", `{"type":"object"}`),
		tl("structured", "Returns only structured content.", `{"type":"object"}`),
		tl("resource", "Returns resources.", `{"type":"object"}`),
		tl("slow", "Takes a long time.", `{"type":"object"}`),
		tl("needs_input", "Asks for more input.", `{"type":"object"}`),
		tl("env", "Reports its environment.", `{"type":"object"}`),
		tl("crash", "Kills the server.", `{"type":"object"}`),
	}
	return append(base, f.extraTools...)
}

func (f *fakeServer) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.methods[method]
}

func (f *fakeServer) handle(m *rpcMsg) *rpcMsg { return f.handleCtx(m, nil) }

// handleCtx is handle for a server that notices when the client has gone: gone closes then.
func (f *fakeServer) handleCtx(m *rpcMsg, gone <-chan struct{}) *rpcMsg {
	f.mu.Lock()
	f.methods[m.Method]++
	f.mu.Unlock()

	ok := func(result map[string]any) *rpcMsg {
		if f.era == "modern" {
			result["resultType"] = "complete"
		}
		raw, _ := json.Marshal(result)
		return &rpcMsg{JSONRPC: "2.0", ID: m.ID, Result: raw}
	}
	fail := func(code int, msg string) *rpcMsg {
		return &rpcMsg{JSONRPC: "2.0", ID: m.ID, Error: &rpcError{Code: code, Message: msg}}
	}

	if m.Method == "notifications/cancelled" {
		var p struct {
			RequestID json.Number `json:"requestId"`
		}
		json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.cancels = append(f.cancels, p.RequestID.String())
		if ch := f.slow[p.RequestID.String()]; ch != nil {
			close(ch)
			delete(f.slow, p.RequestID.String())
		}
		f.mu.Unlock()
		if f.cancelFile != "" {
			os.WriteFile(f.cancelFile, []byte("cancelled "+p.RequestID.String()), 0o600)
		}
		return nil
	}
	if len(m.ID) == 0 { // any other notification
		if m.Method == "notifications/initialized" {
			f.mu.Lock()
			f.initialized = true
			f.mu.Unlock()
		}
		return nil
	}

	switch f.era {
	case "legacy":
		switch m.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(m.Params, &p)
			v := f.version
			if v == "" {
				v = p.ProtocolVersion
			}
			f.mu.Lock()
			f.initialized = false
			f.mu.Unlock()
			return ok(map[string]any{
				"protocolVersion": v,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake", "version": "1"},
			})
		case "server/discover":
			return fail(-32601, "Method not found")
		}
		f.mu.Lock()
		initialized := f.initialized
		f.mu.Unlock()
		if !initialized {
			return fail(-32600, "server not initialized")
		}
	case "modern":
		if m.Method == "initialize" {
			return fail(-32601, "Method not found: initialize (this server requires protocol 2026-07-28)")
		}
		var p struct {
			Meta map[string]any `json:"_meta"`
		}
		json.Unmarshal(m.Params, &p)
		if p.Meta["io.modelcontextprotocol/protocolVersion"] != "2026-07-28" || p.Meta["io.modelcontextprotocol/clientCapabilities"] == nil {
			return fail(-32602, "missing required _meta fields")
		}
		if m.Method == "server/discover" {
			versions := f.supported
			if versions == nil {
				versions = []string{"2026-07-28"}
			}
			return ok(map[string]any{"supportedVersions": versions, "capabilities": map[string]any{"tools": map[string]any{}}})
		}
	}

	switch m.Method {
	case "ping":
		return ok(map[string]any{})
	case "tools/list":
		var p struct{ Cursor string }
		json.Unmarshal(m.Params, &p)
		all := f.allTools()
		start := 0
		if p.Cursor != "" {
			start, _ = strconv.Atoi(p.Cursor)
		}
		end, next := len(all), ""
		if f.pageSize > 0 && start+f.pageSize < len(all) {
			end = start + f.pageSize
			next = strconv.Itoa(end)
		}
		res := map[string]any{"tools": all[start:end]}
		if next != "" {
			res["nextCursor"] = next
		}
		return ok(res)
	case "tools/call":
		return f.call(m, gone, ok, fail)
	}
	return fail(-32601, "Method not found: "+m.Method)
}

func (f *fakeServer) call(m *rpcMsg, gone <-chan struct{}, ok func(map[string]any) *rpcMsg, fail func(int, string) *rpcMsg) *rpcMsg {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	json.Unmarshal(m.Params, &p)
	text := func(s string) []any { return []any{map[string]any{"type": "text", "text": s}} }

	switch p.Name {
	case "echo":
		return ok(map[string]any{"content": text("echo: " + fmt.Sprint(p.Arguments["text"]))})
	case "fail":
		return ok(map[string]any{"content": text("boom: " + fmt.Sprint(p.Arguments["reason"])), "isError": true})
	case "image":
		return ok(map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "here"},
			map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString([]byte("12345678"))},
		}})
	case "structured":
		return ok(map[string]any{"structuredContent": map[string]any{"n": 1}})
	case "resource":
		return ok(map[string]any{"content": []any{
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///a.txt", "text": "file body"}},
			map[string]any{"type": "resource_link", "uri": "file:///b", "name": "b"},
		}})
	case "needs_input":
		raw, _ := json.Marshal(map[string]any{"resultType": "input_required", "inputRequests": map[string]any{}})
		return &rpcMsg{JSONRPC: "2.0", ID: m.ID, Result: raw}
	case "env":
		return ok(map[string]any{"content": text("SECRET=" + os.Getenv("SECRET_TOKEN") + " CONFIGURED=" + os.Getenv("CONFIGURED_VAR") + " HOME_SET=" + strconv.FormatBool(os.Getenv("HOME") != ""))})
	case "regional":
		return ok(map[string]any{"content": text("region=" + fmt.Sprint(p.Arguments["region"]))})
	case "crash":
		fmt.Fprintln(os.Stderr, "fatal: kaboom")
		os.Exit(3)
	case "slow":
		ch := make(chan struct{})
		f.mu.Lock()
		f.slow[string(m.ID)] = ch
		f.mu.Unlock()
		select {
		case <-ch:
			return nil // cancelled: send nothing further
		case <-gone:
			return nil // the connection dropped
		case <-time.After(10 * time.Second):
			return ok(map[string]any{"content": text("finally")})
		}
	}
	return fail(-32602, "Unknown tool: "+p.Name)
}

// runFakeStdio serves the protocol on stdin/stdout until stdin closes.
func runFakeStdio(mode string) {
	if mode == "crashstart" {
		fmt.Fprintln(os.Stderr, "fatal: cannot open database")
		os.Exit(2)
	}
	era := mode
	if era == "stubborn" {
		era = "legacy"
	}
	f := newFake(era)
	f.version = os.Getenv("MCP_FAKE_VERSION")
	f.cancelFile = os.Getenv("MCP_FAKE_CANCEL_FILE")
	if n, err := strconv.Atoi(os.Getenv("MCP_FAKE_PAGE")); err == nil {
		f.pageSize = n
	}
	if os.Getenv("MCP_FAKE_NOISE") != "" {
		fmt.Println("server starting up...") // not JSON: a misbehaving server logging to stdout
	}

	var wmu sync.Mutex
	write := func(m *rpcMsg) {
		raw, _ := json.Marshal(m)
		wmu.Lock()
		os.Stdout.Write(append(raw, '\n'))
		wmu.Unlock()
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var m rpcMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if len(m.ID) == 0 { // notifications are taken in order, as a real server takes them: "initialized" must be seen before the request that follows it
			f.handle(&m)
			continue
		}
		go func() { // concurrent, so a slow call does not block a cancellation
			if reply := f.handle(&m); reply != nil {
				write(reply)
			}
		}()
	}
	if mode == "stubborn" {
		time.Sleep(time.Hour) // ignores the closing of stdin; must be killed
	}
}

// ---- HTTP ----

type recorded struct {
	Method string // HTTP method
	RPC    string // JSON-RPC method
	Header http.Header
	Body   []byte
}

type fakeHTTP struct {
	f *fakeServer

	// behaviour switches
	sse          bool   // answer tools/call with an event stream
	sessions     bool   // legacy: assign Mcp-Session-Id
	auth         string // require this Authorization header
	status       int    // answer every POST with this bare status
	rejectLegacy int    // modern servers: status for requests lacking modern headers (default 400)

	mu       sync.Mutex
	hits     int // every request received, including ones refused before being recorded
	session  string
	nextSess int
	expire   bool
	reqs     []recorded
	deletes  int
	replies  []rpcMsg // responses the client POSTed to our requests
}

func (h *fakeHTTP) requests(rpc string) []recorded {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []recorded
	for _, r := range h.reqs {
		if r.RPC == rpc {
			out = append(out, r)
		}
	}
	return out
}

func decodeHeaderValue(v string) string {
	if strings.HasPrefix(v, "=?base64?") && strings.HasSuffix(v, "?=") {
		b, _ := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(v, "=?base64?"), "?="))
		return string(b)
	}
	return v
}

func (h *fakeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.hits++
	h.mu.Unlock()
	if h.status != 0 {
		w.WriteHeader(h.status)
		return
	}
	if h.auth != "" && r.Header.Get("Authorization") != h.auth {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodDelete {
		h.mu.Lock()
		h.deletes++
		h.mu.Unlock()
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, _ := io.ReadAll(r.Body)
	var m rpcMsg
	json.Unmarshal(body, &m)
	h.mu.Lock()
	h.reqs = append(h.reqs, recorded{Method: r.Method, RPC: m.Method, Header: r.Header.Clone(), Body: body})
	h.mu.Unlock()

	if m.isResponse() { // the client answering a request we sent in a stream
		h.mu.Lock()
		h.replies = append(h.replies, m)
		h.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		return
	}

	jsonErr := func(status, code int, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(rpcMsg{JSONRPC: "2.0", Error: &rpcError{Code: code, Message: msg}})
	}

	if h.f.era == "legacy" && h.sessions {
		h.mu.Lock()
		switch sid := r.Header.Get("Mcp-Session-Id"); {
		case m.Method == "initialize":
			h.nextSess++
			h.session = fmt.Sprintf("sess-%d", h.nextSess)
			w.Header().Set("Mcp-Session-Id", h.session)
		case h.expire && sid != "":
			h.expire, h.session = false, ""
			h.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		case sid == "":
			h.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			return
		case sid != h.session:
			h.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h.mu.Unlock()
	}

	if h.f.era == "modern" {
		if r.Header.Get("MCP-Protocol-Version") != "2026-07-28" || r.Header.Get("Mcp-Method") != m.Method {
			if h.rejectLegacy != 0 {
				w.WriteHeader(h.rejectLegacy)
				return
			}
			jsonErr(400, -32020, "Header mismatch: required MCP headers are missing")
			return
		}
		if m.Method == "tools/call" {
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			json.Unmarshal(m.Params, &p)
			if decodeHeaderValue(r.Header.Get("Mcp-Name")) != p.Name {
				jsonErr(400, -32020, "Header mismatch: Mcp-Name")
				return
			}
			if p.Name == "regional" && decodeHeaderValue(r.Header.Get("Mcp-Param-Region")) != fmt.Sprint(p.Arguments["region"]) {
				jsonErr(400, -32020, "Header mismatch: Mcp-Param-Region")
				return
			}
		}
	}

	reply := h.f.handleCtx(&m, r.Context().Done())
	if reply == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if h.sse && m.Method == "tools/call" {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, `data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`+"\n\n")
		io.WriteString(w, `data: {"jsonrpc":"2.0","id":"srv-ping","method":"ping"}`+"\n\n")
		fl.Flush()
		time.Sleep(100 * time.Millisecond) // let the client answer the ping
		raw, _ := json.Marshal(reply)
		io.WriteString(w, "data: "+string(raw)+"\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reply)
}
