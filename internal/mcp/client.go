package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	modernVersion  = "2026-07-28" // stateless: no handshake, every request carries _meta
	clientName     = "talaria"
	clientVersion  = "0.1.0"
	maxTools       = 200
	maxPages       = 20
	maxDescription = 2000
)

// legacyVersions are the handshake-based revisions we can speak, newest first.
var legacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// Tool is a tool a server offers.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Annotations Annotations // the server's hints about what it does

	mirrored []paramHeader // modern HTTP only: arguments copied into headers
}

// Client is a connection to one MCP server, safe for concurrent use.
type Client struct {
	name string
	conn conn

	nextID atomic.Int64

	mu         sync.Mutex // guards the protocol state
	modern     bool
	version    string
	hasTools   bool
	generation uint64 // bumped each time a legacy session is re-established

	reinit sync.Mutex

	tools    []Tool
	byName   map[string]*Tool
	Warnings []string // tools skipped and similar
}

// Connect starts or reaches a server, negotiates a protocol version and lists
// its tools. ctx bounds the whole startup. The caller must Close the client.
func Connect(ctx context.Context, name string, s Server, getenv func(string) string) (*Client, error) {
	return ConnectAuth(ctx, name, s, getenv, nil)
}

// ConnectAuth is Connect for a server that wants an OAuth sign-in: auth supplies
// the access token each request carries.
func ConnectAuth(ctx context.Context, name string, s Server, getenv func(string) string, auth TokenSource) (*Client, error) {
	s, err := s.expand(getenv)
	if err != nil {
		return nil, err
	}
	var cn conn
	if s.isHTTP() {
		h := newHTTPConn(s)
		h.auth, h.name = auth, name
		cn = h
	} else {
		sc, err := startStdio(s, getenv)
		if err != nil {
			return nil, err
		}
		cn = sc
	}

	c := &Client{name: name, conn: cn}
	if err := c.handshake(ctx); err != nil {
		cn.close()
		return nil, err
	}
	if err := c.loadTools(ctx); err != nil {
		cn.close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Tools() []Tool { return c.tools }

func (c *Client) Close() { c.conn.close() }

// Protocol describes what was negotiated, for display.
func (c *Client) Protocol() string {
	modern, version := c.proto()
	if modern {
		return version + " (stateless)"
	}
	return version
}

func (c *Client) proto() (modern bool, version string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.modern, c.version
}

func (c *Client) setProto(modern bool, version string, hasTools bool) {
	c.mu.Lock()
	c.modern, c.version, c.hasTools = modern, version, hasTools
	c.mu.Unlock()
}

// handshake works out which era the server speaks. It tries the original
// initialize handshake first, which every older and dual-era server answers,
// and falls back to the 2026-07-28 server/discover if the server rejects it.
func (c *Client) handshake(ctx context.Context) error {
	err := c.initializeLegacy(ctx)
	if err == nil || !rejectedAsUnknown(err) {
		return err
	}
	merr := c.discoverModern(ctx)
	if merr == nil {
		return nil
	}

	msg := fmt.Sprintf("the server rejected both the initialize handshake (%v) and server/discover (%v)", err, merr)
	var he *httpStatusError
	if errors.As(merr, &he) && (he.Status == 404 || he.Status == 405) {
		msg += "; if it uses the deprecated HTTP+SSE transport, that is not supported"
	}
	return errors.New(msg)
}

// rejectedAsUnknown reports whether err means "this server does not speak
// that handshake", as opposed to a failure that retrying another way cannot fix.
func rejectedAsUnknown(err error) bool {
	var re *rpcError
	if errors.As(err, &re) {
		return true
	}
	var he *httpStatusError
	return errors.As(err, &he) && (he.Status == 400 || he.Status == 404 || he.Status == 405)
}

func (c *Client) initializeLegacy(ctx context.Context) error {
	c.setProto(false, "", false)
	res, err := c.rawCall(ctx, "initialize", map[string]any{
		"protocolVersion": legacyVersions[0],
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": clientName, "version": clientVersion},
	}, nil)
	if err != nil {
		return err
	}
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools json.RawMessage `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return fmt.Errorf("unreadable initialize reply: %w", err)
	}
	if !slices.Contains(legacyVersions, r.ProtocolVersion) {
		return fmt.Errorf("the server speaks protocol version %q, which this client does not support", r.ProtocolVersion)
	}
	c.setProto(false, r.ProtocolVersion, len(r.Capabilities.Tools) > 0)
	return c.conn.notify(ctx, "notifications/initialized", nil, c.headers(false, r.ProtocolVersion, "notifications/initialized", nil, nil))
}

func (c *Client) discoverModern(ctx context.Context) error {
	c.setProto(true, modernVersion, false)
	res, err := c.rawCall(ctx, "server/discover", nil, nil)
	if err != nil {
		c.setProto(false, "", false)
		return err
	}
	var r struct {
		SupportedVersions []string `json:"supportedVersions"`
		Capabilities      struct {
			Tools json.RawMessage `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		c.setProto(false, "", false)
		return fmt.Errorf("unreadable server/discover reply: %w", err)
	}
	if !slices.Contains(r.SupportedVersions, modernVersion) {
		c.setProto(false, "", false)
		return fmt.Errorf("the server supports protocol versions %v, not %s", r.SupportedVersions, modernVersion)
	}
	c.setProto(true, modernVersion, len(r.Capabilities.Tools) > 0)
	return nil
}

// meta is the per-request metadata the stateless revision requires.
func meta() map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    modernVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": clientName, "version": clientVersion},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

// headers builds HTTP request metadata (ignored over stdio).
func (c *Client) headers(modern bool, version, method string, params map[string]any, extra map[string]string) map[string]string {
	h := map[string]string{}
	switch {
	case modern:
		h["MCP-Protocol-Version"] = version
		h["Mcp-Method"] = method
		if name, ok := params["name"].(string); ok && method == "tools/call" {
			h["Mcp-Name"] = encodeHeaderValue(name)
		}
	case version >= "2025-06-18" && method != "initialize": // dates sort as strings
		h["MCP-Protocol-Version"] = version
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// rawCall sends one request and returns its result. For a legacy HTTP server
// whose session has expired it starts a new session and retries once.
func (c *Client) rawCall(ctx context.Context, method string, params map[string]any, extra map[string]string) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		modern, version := c.proto()
		c.mu.Lock()
		gen := c.generation
		c.mu.Unlock()

		p := make(map[string]any, len(params)+1)
		for k, v := range params {
			p[k] = v
		}
		if modern {
			p["_meta"] = meta()
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}

		id := c.nextID.Add(1)
		resp, err := c.conn.call(ctx, id, newRequest(id, method, raw), c.headers(modern, version, method, p, extra))
		if err != nil {
			if errors.Is(err, errSessionExpired) && !modern && method != "initialize" && attempt == 0 {
				if rerr := c.reinitialize(ctx, gen); rerr != nil {
					return nil, fmt.Errorf("session expired and starting a new one failed: %w", rerr)
				}
				continue
			}
			if ctx.Err() != nil {
				c.cancel(id, modern, version)
			}
			return nil, err
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (c *Client) reinitialize(ctx context.Context, seen uint64) error {
	c.reinit.Lock()
	defer c.reinit.Unlock()
	c.mu.Lock()
	current := c.generation
	c.mu.Unlock()
	if current != seen { // another caller already re-established the session
		return nil
	}
	if r, ok := c.conn.(interface{ resetSession() }); ok {
		r.resetSession()
	}
	if err := c.initializeLegacy(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.generation++
	c.mu.Unlock()
	return nil
}

// cancel tells the server we gave up on a request. On the stateless HTTP
// binding, dropping the connection already did that.
func (c *Client) cancel(id int64, modern bool, version string) {
	if modern && c.conn.isHTTP() {
		return
	}
	ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
	defer done()
	c.conn.notify(ctx, "notifications/cancelled", map[string]any{"requestId": id, "reason": "the client stopped waiting"},
		c.headers(modern, version, "notifications/cancelled", nil, nil))
}

func (c *Client) loadTools(ctx context.Context) error {
	c.mu.Lock()
	hasTools := c.hasTools
	c.mu.Unlock()
	if !hasTools {
		return nil
	}
	modern, _ := c.proto()
	mirror := modern && c.conn.isHTTP()

	c.byName = map[string]*Tool{}
	cursor := ""
	for page := 0; page < maxPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.rawCall(ctx, "tools/list", params, nil)
		if err != nil {
			return fmt.Errorf("listing tools: %w", err)
		}
		var lr struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
				Annotations Annotations     `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &lr); err != nil {
			return fmt.Errorf("unreadable tool list: %w", err)
		}

		for _, t := range lr.Tools {
			if t.Name == "" {
				continue
			}
			if len(c.tools) >= maxTools {
				c.Warnings = append(c.Warnings, fmt.Sprintf("more than %d tools: the rest are ignored", maxTools))
				return nil
			}
			tool := Tool{Name: t.Name, Description: truncateRunes(t.Description, maxDescription), InputSchema: normalizeSchema(t.InputSchema), Annotations: t.Annotations}
			if mirror {
				m, err := mirroredParams(tool.InputSchema)
				if err != nil { // the spec requires dropping such tools
					c.Warnings = append(c.Warnings, fmt.Sprintf("tool %q skipped: %v", t.Name, err))
					continue
				}
				tool.mirrored = m
			}
			c.tools = append(c.tools, tool)
		}
		if lr.NextCursor == "" {
			break
		}
		cursor = lr.NextCursor
	}
	for i := range c.tools {
		c.byName[c.tools[i].Name] = &c.tools[i]
	}
	return nil
}

func normalizeSchema(s json.RawMessage) json.RawMessage {
	var probe map[string]any
	if json.Unmarshal(s, &probe) != nil || probe == nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return s
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Call runs a tool and returns its output as text. A tool that reports a
// failure comes back as an error carrying the tool's own message.
func (c *Client) Call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	argObj := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(args))
		dec.UseNumber() // keep large integers exact
		if err := dec.Decode(&argObj); err != nil || argObj == nil {
			return "", errors.New("arguments must be a JSON object")
		}
	}

	var extra map[string]string
	if t := c.byName[tool]; t != nil {
		extra = mirroredHeaders(t.mirrored, argObj)
	}
	res, err := c.rawCall(ctx, "tools/call", map[string]any{"name": tool, "arguments": argObj}, extra)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	return renderResult(res)
}
