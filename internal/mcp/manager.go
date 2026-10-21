package mcp

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"talaria/internal/tool"
)

// startupTimeout bounds connecting to one server. A variable so tests can shrink it.
var startupTimeout = 60 * time.Second

const maxToolName = 64 // the limit both model APIs put on tool names

var badNameChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// ToolInfo describes a tool as Talaria exposes it.
type ToolInfo struct {
	Name          string // the server's name for it
	Exposed       string // the name the model sees
	Description   string
	NeedsApproval bool
	Class         Class // what the tool does, as far as its name and the server's hints say
}

// ServerStatus is the outcome of connecting to one configured server.
type ServerStatus struct {
	Name      string
	Transport string
	Protocol  string
	Disabled  bool
	Err       error
	Tools     []ToolInfo
	Warnings  []string
}

type exposedTool struct {
	exposed string
	tool    Tool
	approve bool
	class   Class
}

type liveServer struct {
	name   string
	spec   Server
	client *Client
	tools  []exposedTool
}

// Manager owns the connections to all configured servers.
type Manager struct {
	live   []*liveServer
	status []ServerStatus
}

// Start connects to every enabled server in parallel. A server that fails is
// reported in Status and skipped; the others still work.
func Start(ctx context.Context, servers map[string]Server, getenv func(string) string) *Manager {
	return StartAuth(ctx, servers, getenv, nil)
}

// StartAuth is Start with the sign-ins of the servers that use OAuth, by name.
// A server that uses OAuth and has none here cannot connect, and says to sign in.
func StartAuth(ctx context.Context, servers map[string]Server, getenv func(string) string, auth map[string]TokenSource) *Manager {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)

	type outcome struct {
		client *Client
		err    error
	}
	results := make([]outcome, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		if servers[n].Disabled {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, startupTimeout)
			defer cancel()
			var ts TokenSource
			if servers[n].OAuth != nil {
				ts = auth[n]
				if ts == nil {
					results[i] = outcome{nil, fmt.Errorf("it signs in with OAuth, but there is no vault to keep the sign-in in: create one with `tal vault init`, then run `tal connect %s`", n)}
					return
				}
			}
			c, err := ConnectAuth(cctx, n, servers[n], getenv, ts)
			if err != nil && cctx.Err() == context.DeadlineExceeded {
				err = fmt.Errorf("did not finish starting within %s: %w", startupTimeout, err)
			}
			results[i] = outcome{c, err}
		}()
	}
	wg.Wait()

	m := &Manager{}
	taken := map[string]bool{}
	for i, n := range names {
		spec := servers[n]
		st := ServerStatus{Name: n, Transport: spec.label(), Disabled: spec.Disabled, Err: results[i].err}
		if c := results[i].client; c != nil {
			st.Protocol, st.Warnings = c.Protocol(), c.Warnings
			ls := &liveServer{name: n, spec: spec, client: c}
			for _, t := range c.Tools() {
				class := Classify(t.Name, t.Annotations)
				et := exposedTool{exposed: exposedName(n, t.Name, taken), tool: t, approve: !servers[n].approves(t.Name, class), class: class}
				ls.tools = append(ls.tools, et)
				st.Tools = append(st.Tools, ToolInfo{Name: t.Name, Exposed: et.exposed, Description: t.Description, NeedsApproval: et.approve, Class: class})
			}
			m.live = append(m.live, ls)
		}
		m.status = append(m.status, st)
	}
	return m
}

// exposedName builds the model-facing name mcp_<server>_<tool>, made safe for
// both model APIs (letters, digits, _ and -, at most 64) and unique.
func exposedName(server, tool string, taken map[string]bool) string {
	full := "mcp_" + server + "_" + badNameChars.ReplaceAllString(tool, "_")
	name := full
	if len(name) > maxToolName {
		sum := sha1.Sum([]byte(server + "\x00" + tool))
		name = full[:maxToolName-9] + "_" + hex.EncodeToString(sum[:4])
	}
	for n := 2; taken[name]; n++ {
		suffix := fmt.Sprintf("_%d", n)
		base := name
		if len(base)+len(suffix) > maxToolName {
			base = base[:maxToolName-len(suffix)]
		}
		name = base + suffix
	}
	taken[name] = true
	return name
}

// Status reports how each configured server started, sorted by name.
func (m *Manager) Status() []ServerStatus {
	if m == nil {
		return nil
	}
	return m.status
}

// HasTools reports whether any connected server offers a tool.
func (m *Manager) HasTools() bool {
	if m == nil {
		return false
	}
	for _, s := range m.live {
		if len(s.tools) > 0 {
			return true
		}
	}
	return false
}

// Sensitive lists the model-facing names of tools that send, post, delete or the
// like. They are always asked about, and the Always button is never offered
// for them: approving one send is not approving the next.
func (m *Manager) Sensitive() []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, s := range m.live {
		for _, et := range s.tools {
			if et.class.Sensitive() {
				out = append(out, et.exposed)
			}
		}
	}
	return out
}

// Register adds every connected server's tools to r. Calls ask for approval
// unless the server is trusted or lists the tool in auto_approve.
func (m *Manager) Register(r *tool.Registry) {
	if m == nil {
		return
	}
	for _, s := range m.live {
		for _, et := range s.tools {
			client, timeout, name := s.client, s.spec.callTimeout(), et.tool.Name
			r.Register(tool.Tool{
				Name:          et.exposed,
				Description:   fmt.Sprintf("[MCP server %q] %s", s.name, et.tool.Description),
				Schema:        et.tool.InputSchema,
				NeedsApproval: et.approve,
				Run: func(ctx context.Context, args json.RawMessage) (string, error) {
					ctx, cancel := context.WithTimeout(ctx, timeout)
					defer cancel()
					out, err := client.Call(ctx, name, args)
					if err != nil && ctx.Err() == context.DeadlineExceeded {
						return "", fmt.Errorf("timed out after %s", timeout)
					}
					return out, err
				},
			})
		}
	}
}

// Close disconnects from every server, stopping subprocesses.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	var wg sync.WaitGroup
	for _, s := range m.live {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.client.Close()
		}()
	}
	wg.Wait()
}

// FirstLine returns the first non-empty line of s, shortened, for listings.
func FirstLine(s string, n int) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > n {
				return string(r[:n]) + "…"
			}
			return line
		}
	}
	return ""
}
