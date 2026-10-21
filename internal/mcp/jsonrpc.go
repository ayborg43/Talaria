// Package mcp is a client for Model Context Protocol tool servers.
//
// It speaks both protocol eras: the original handshake-based revisions
// (2024-11-05 through 2025-11-25) and the stateless 2026-07-28 revision, over
// stdio subprocesses and Streamable HTTP. It uses tools only: it declares no
// client capabilities, so servers never ask it for sampling, roots or input.
package mcp

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// rpcMsg is any JSON-RPC 2.0 message: request, notification or response.
type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

func (m *rpcMsg) isResponse() bool {
	return m.Method == "" && len(m.ID) > 0 && (m.Result != nil || m.Error != nil)
}

// isServerRequest reports whether the server is asking us something.
func (m *rpcMsg) isServerRequest() bool { return m.Method != "" && len(m.ID) > 0 }

// idMatches reports whether the message carries the numeric id we sent.
func (m *rpcMsg) idMatches(id int64) bool {
	n, err := strconv.ParseInt(string(m.ID), 10, 64)
	return err == nil && n == id
}

func newRequest(id int64, method string, params json.RawMessage) *rpcMsg {
	return &rpcMsg{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: params}
}

// answerServerRequest builds our reply to a request from the server. We
// declare no capabilities, so the only request we expect is ping.
func answerServerRequest(req *rpcMsg) *rpcMsg {
	if req.Method == "ping" {
		return &rpcMsg{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{}`)}
	}
	return &rpcMsg{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not supported by this client"}}
}
