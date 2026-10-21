// Package browser lets the agent use a web browser. It drives Chromium over the
// Chrome DevTools Protocol, spoken through a pipe to a process started in an
// execution environment (usually the workspace container), so no network port
// is opened and nothing on this machine can reach the browser but Talaria.
package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"talaria/internal/execenv"
)

// maxMessage bounds one message from the browser. A full-page screenshot is a
// few megabytes of base64.
const maxMessage = 64 << 20

// event is a notification from the browser.
type event struct {
	Method    string
	SessionID string
	Params    json.RawMessage
}

// cdpError is an error the browser answered a command with.
type cdpError struct {
	Method  string
	Code    int
	Message string
}

func (e *cdpError) Error() string {
	return fmt.Sprintf("browser: %s: %s (code %d)", e.Method, e.Message, e.Code)
}

type response struct {
	result json.RawMessage
	err    error
}

// client speaks the protocol over a pipe: JSON messages, each ended by a NUL byte.
type client struct {
	proc *execenv.Process

	wmu sync.Mutex // one writer at a time

	mu      sync.Mutex
	nextID  int
	pending map[int]chan response
	subs    map[*listener]struct{}
	dead    error // why the connection ended, once it has
}

// A listener is called, in the reader's own goroutine, for each event of its
// session ("" means every session). Because events and answers are read in the
// order they arrive, everything the browser said before it answered a command
// has been handled by the time that command returns. A listener must be quick.
type listener struct {
	sessionID string
	fn        func(event)
}

func newClient(p *execenv.Process) *client {
	c := &client{proc: p, pending: map[int]chan response{}, subs: map[*listener]struct{}{}}
	go c.readLoop()
	return c
}

func (c *client) readLoop() {
	r := bufio.NewReaderSize(c.proc.Out, 1<<20)
	var err error
	for {
		var msg []byte
		msg, err = readMessage(r)
		if err != nil {
			break
		}
		var m struct {
			ID        int             `json:"id"`
			Method    string          `json:"method"`
			SessionID string          `json:"sessionId"`
			Params    json.RawMessage `json:"params"`
			Result    json.RawMessage `json:"result"`
			Error     *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		if m.Method != "" && m.ID == 0 {
			c.publish(event{Method: m.Method, SessionID: m.SessionID, Params: m.Params})
			continue
		}
		c.mu.Lock()
		ch := c.pending[m.ID]
		delete(c.pending, m.ID)
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		if m.Error != nil {
			ch <- response{err: &cdpError{Code: m.Error.Code, Message: m.Error.Message}}
		} else {
			ch <- response{result: m.Result}
		}
	}

	// The browser has gone: say why to everyone still waiting.
	if errors.Is(err, io.EOF) {
		err = errors.New("the browser exited")
		if s := c.proc.Stderr(); s != "" {
			err = fmt.Errorf("the browser exited: %s", s)
		}
	}
	c.mu.Lock()
	c.dead = err
	for id, ch := range c.pending {
		ch <- response{err: err}
		delete(c.pending, id)
	}
	c.subs = map[*listener]struct{}{}
	c.mu.Unlock()
}

// readMessage reads up to the next NUL.
func readMessage(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice(0)
		buf = append(buf, chunk...)
		if len(buf) > maxMessage {
			return nil, errors.New("the browser sent a message that is too large")
		}
		switch {
		case err == nil:
			return buf[:len(buf)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

func (c *client) publish(ev event) {
	c.mu.Lock()
	var to []*listener
	for l := range c.subs {
		if l.sessionID == "" || l.sessionID == ev.SessionID {
			to = append(to, l)
		}
	}
	c.mu.Unlock()
	for _, l := range to {
		l.fn(ev)
	}
}

// listen calls fn for the events of a session, until the returned function is called.
func (c *client) listen(sessionID string, fn func(event)) (stop func()) {
	l := &listener{sessionID: sessionID, fn: fn}
	c.mu.Lock()
	if c.dead == nil {
		c.subs[l] = struct{}{}
	}
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.subs, l)
		c.mu.Unlock()
	}
}

// call sends a command and waits for its answer.
func (c *client) call(ctx context.Context, method string, params any, sessionID string) (json.RawMessage, error) {
	c.mu.Lock()
	if c.dead != nil {
		err := c.dead
		c.mu.Unlock()
		return nil, err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if sessionID != "" {
		msg["sessionId"] = sessionID
	}
	raw, err := json.Marshal(msg)
	if err == nil {
		c.wmu.Lock()
		_, err = c.proc.In.Write(append(raw, 0))
		c.wmu.Unlock()
	}
	if err != nil {
		c.forget(id)
		return nil, fmt.Errorf("browser: %s: %w", method, err)
	}

	select {
	case r := <-ch:
		var ce *cdpError
		if errors.As(r.err, &ce) {
			ce.Method = method
		}
		return r.result, r.err
	case <-ctx.Done():
		c.forget(id)
		return nil, ctx.Err()
	}
}

func (c *client) forget(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *client) close() { c.proc.Close() }

// alive reports whether the connection to the browser is still good. It goes
// false the moment the browser's output ends, which is before the process
// itself has been reaped.
func (c *client) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead == nil
}
