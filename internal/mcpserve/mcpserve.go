// Package mcpserve is a small Model Context Protocol server over stdio: another program (Claude Desktop, Cursor, a
// script) starts `tal mcp serve` and uses what Talaria offers as tools. It speaks JSON-RPC 2.0, one message per line,
// and offers tools only: no resources, prompts or sampling, and nothing that asks the client for anything.
//
// It is deliberately bounded. The tools it serves are chosen by the caller of Serve, so what a client can do is a list
// in one place, not a side effect of what Talaria can do.
package mcpserve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Versions are the protocol revisions the server will agree to, newest first. A client that asks for another gets the newest.
var Versions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// maxLine bounds one incoming message.
const maxLine = 4 << 20

// Tool is one tool on offer.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON Schema of its arguments (an object). Nil means no arguments.
	Schema json.RawMessage
	// ReadOnly says it changes nothing: the client may run it without asking.
	ReadOnly bool
	// Run does the work. Its text is the tool's result; an error is returned to the client as a failed result, not as a
	// protocol error, so the model can read what went wrong.
	Run func(ctx context.Context, args json.RawMessage) (string, error)
}

// Server serves a set of tools.
type Server struct {
	Name    string
	Version string
	// Instructions is shown to the client's model: what these tools are for.
	Instructions string
	Tools        []Tool
	// Logf, if set, is told about problems (never about a tool's arguments or results).
	Logf func(string, ...any)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

// Serve reads messages from in and writes answers to out until in ends or ctx is done. Requests are handled one at a
// time, in order: a client that wants two things at once opens two servers.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	var wmu sync.Mutex
	send := func(r response) {
		r.JSONRPC = "2.0"
		b, err := json.Marshal(r)
		if err != nil {
			return
		}
		wmu.Lock()
		defer wmu.Unlock()
		out.Write(append(b, '\n'))
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	lines := make(chan []byte)
	go func() {
		defer close(lines)
		for sc.Scan() {
			b := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- b:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				return nil
			}
			if len(line) == 0 {
				continue
			}
			var req request
			if err := json.Unmarshal(line, &req); err != nil {
				send(response{ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
				continue
			}
			if len(req.ID) == 0 { // a notification: nothing is answered
				continue
			}
			send(s.handle(ctx, req))
		}
	}
}

func (s *Server) handle(ctx context.Context, req request) response {
	res := response{ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := Versions[0]
		for _, known := range Versions {
			if known == p.ProtocolVersion {
				v = known
			}
		}
		info := map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]string{"name": s.Name, "version": s.Version}}
		if s.Instructions != "" {
			info["instructions"] = s.Instructions
		}
		res.Result = info
	case "ping":
		res.Result = map[string]any{}
	case "tools/list":
		tools := make([]map[string]any, 0, len(s.Tools))
		for _, t := range s.Tools {
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema,
				"annotations": map[string]any{"readOnlyHint": t.ReadOnly, "destructiveHint": false, "openWorldHint": false}})
		}
		res.Result = map[string]any{"tools": tools}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			res.Error = &rpcError{Code: -32602, Message: "invalid params"}
			return res
		}
		var tool *Tool
		for i := range s.Tools {
			if s.Tools[i].Name == p.Name {
				tool = &s.Tools[i]
			}
		}
		if tool == nil {
			res.Error = &rpcError{Code: -32602, Message: fmt.Sprintf("no tool called %q", p.Name)}
			return res
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage("{}")
		}
		text, err := s.call(ctx, tool, p.Arguments)
		out := map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
		if err != nil {
			out["content"] = []map[string]string{{"type": "text", "text": err.Error()}}
			out["isError"] = true
		}
		res.Result = out
	default:
		res.Error = &rpcError{Code: -32601, Message: "method not found"}
	}
	return res
}

// call runs a tool, turning a panic into a failed result so one bad call cannot end the session.
func (s *Server) call(ctx context.Context, t *Tool, args json.RawMessage) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("mcp serve: tool %s panicked: %v", t.Name, r)
			text, err = "", errors.New("that tool failed unexpectedly")
		}
	}()
	return t.Run(ctx, args)
}
