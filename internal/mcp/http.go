package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"talaria/internal/sse"
)

// errSessionExpired means a server that assigned us a session no longer knows it.
var errSessionExpired = errors.New("mcp session expired")

// httpStatusError is a non-success HTTP reply.
type httpStatusError struct {
	Status int
	Body   string
	signIn string // the server's name, when it uses an OAuth sign-in
}

func (e *httpStatusError) Error() string {
	msg := fmt.Sprintf("server returned HTTP %d", e.Status)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	if e.Status == 401 || e.Status == 403 {
		if e.signIn != "" {
			msg += " (the server did not accept the sign-in: run `tal connect " + e.signIn + "` to sign in again)"
		} else {
			msg += " (authentication failed: check the headers in the config, or add \"oauth\": {} to sign in with OAuth)"
		}
	}
	return msg
}

// TokenSource supplies the access token for a server that wants an OAuth
// sign-in. Token returns one that is good to send; Refresh renews it after the
// server refused the last one.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	Refresh(ctx context.Context) error
}

// httpConn speaks Streamable HTTP: every message is its own POST, and the
// reply is either one JSON object or an SSE stream ending in the response.
// Servers of the older revisions may assign a session via Mcp-Session-Id,
// which is carried on later requests.
type httpConn struct {
	url     string
	headers map[string]string
	hc      *http.Client
	auth    TokenSource // nil unless the server uses an OAuth sign-in
	name    string      // the server's name in the config

	mu      sync.Mutex
	session string
}

func newHTTPConn(s Server) *httpConn {
	return &httpConn{url: s.URL, headers: s.Headers, hc: &http.Client{}}
}

func (c *httpConn) isHTTP() bool { return true }

func (c *httpConn) sessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

func (c *httpConn) resetSession() {
	c.mu.Lock()
	c.session = ""
	c.mu.Unlock()
}

func (c *httpConn) post(ctx context.Context, msg *rpcMsg, headers map[string]string) (*http.Response, string, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, "", err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
		if err != nil {
			return nil, "", err
		}
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		session := c.sessionID()
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		if c.auth != nil {
			tok, err := c.auth.Token(ctx)
			if err != nil {
				return nil, session, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}

		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, session, err
		}
		// A token the server no longer accepts is renewed, once, and the request sent again.
		if resp.StatusCode == http.StatusUnauthorized && c.auth != nil && attempt == 0 {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if err := c.auth.Refresh(ctx); err != nil {
				return nil, session, err
			}
			continue
		}
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			c.mu.Lock()
			c.session = sid
			c.mu.Unlock()
		}
		return resp, session, nil
	}
}

func (c *httpConn) statusError(resp *http.Response, sentSession string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound && sentSession != "" {
		return errSessionExpired
	}
	e := &httpStatusError{Status: resp.StatusCode, Body: strings.Join(strings.Fields(string(raw)), " ")}
	if c.auth != nil {
		e.signIn = c.name
	}
	return e
}

func (c *httpConn) call(ctx context.Context, id int64, req *rpcMsg, headers map[string]string) (*rpcMsg, error) {
	resp, session, err := c.post(ctx, req, headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, c.statusError(resp, session)
	}

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return c.readStream(ctx, resp.Body, id, headers)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMessage))
	if err != nil {
		return nil, err
	}
	var m rpcMsg
	if err := json.Unmarshal(raw, &m); err != nil || !m.isResponse() || !m.idMatches(id) {
		return nil, fmt.Errorf("server sent an unexpected reply (HTTP %d)", resp.StatusCode)
	}
	return &m, nil
}

// readStream reads events until the response to request id arrives.
func (c *httpConn) readStream(ctx context.Context, body io.Reader, id int64, headers map[string]string) (*rpcMsg, error) {
	var answer *rpcMsg
	err := sse.Read(body, func(data string) error {
		var m rpcMsg
		if json.Unmarshal([]byte(data), &m) != nil {
			return nil
		}
		switch {
		case m.isServerRequest():
			go c.reply(&m, headers) // older revisions allow pings on the stream
		case m.isResponse() && m.idMatches(id):
			answer = &m
			return sse.Stop
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if answer == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("server closed the stream without answering")
	}
	return answer, nil
}

func (c *httpConn) reply(req *rpcMsg, headers map[string]string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if resp, _, err := c.post(ctx, answerServerRequest(req), headers); err == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
	}
}

func (c *httpConn) notify(ctx context.Context, method string, params any, headers map[string]string) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	resp, session, err := c.post(ctx, &rpcMsg{JSONRPC: "2.0", Method: method, Params: raw}, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.statusError(resp, session)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}

// close ends the session if the server gave us one.
func (c *httpConn) close() {
	session := c.sessionID()
	if session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Mcp-Session-Id", session)
	if resp, err := c.hc.Do(req); err == nil { // a 405 just means the server does not support it
		resp.Body.Close()
	}
}
