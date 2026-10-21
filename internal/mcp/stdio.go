package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// conn carries JSON-RPC messages to one server.
type conn interface {
	// call sends req (whose id is id) and waits for the matching response.
	// headers are request metadata for HTTP and ignored elsewhere.
	call(ctx context.Context, id int64, req *rpcMsg, headers map[string]string) (*rpcMsg, error)
	notify(ctx context.Context, method string, params any, headers map[string]string) error
	close()
	isHTTP() bool
}

const (
	maxMessage = 32 << 20 // largest single message accepted from a server
	stderrKeep = 2048     // bytes of server stderr kept for error messages
)

// stopGrace is how long a server gets to exit after its stdin closes. A variable so tests can shrink it.
var stopGrace = 2 * time.Second

// stdioConn runs a server as a subprocess and exchanges newline-delimited
// JSON-RPC messages over its stdin and stdout.
type stdioConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailBuffer

	wmu sync.Mutex // serializes writes

	mu      sync.Mutex
	pending map[int64]chan *rpcMsg

	done    chan struct{} // closed once the process's stdout has ended and it has been reaped
	exitErr error
}

func startStdio(s Server, getenv func(string) string) (*stdioConn, error) {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Env = s.environment(getenv)
	cmd.Dir = s.Cwd
	c := &stdioConn{cmd: cmd, stderr: &tailBuffer{max: stderrKeep}, pending: map[int64]chan *rpcMsg{}, done: make(chan struct{})}
	cmd.Stderr = c.stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start %q: %w", s.Command, err)
	}
	c.stdin = stdin
	go c.readLoop(stdout)
	return c, nil
}

func (c *stdioConn) isHTTP() bool { return false }

func (c *stdioConn) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), maxMessage)
	for sc.Scan() {
		c.dispatch(sc.Bytes())
	}
	readErr := sc.Err()

	waitErr := c.cmd.Wait()
	switch {
	case readErr != nil:
		c.exitErr = fmt.Errorf("unreadable output from server: %w", readErr)
	case waitErr != nil:
		c.exitErr = fmt.Errorf("server exited: %v", waitErr)
	default:
		c.exitErr = fmt.Errorf("server exited")
	}
	if tail := c.stderr.String(); tail != "" {
		c.exitErr = fmt.Errorf("%w; its stderr ended with: %s", c.exitErr, tail)
	}
	close(c.done)
}

func (c *stdioConn) dispatch(line []byte) {
	var m rpcMsg
	if len(line) == 0 || json.Unmarshal(line, &m) != nil {
		return // blank lines, or logging a server wrongly wrote to stdout
	}
	switch {
	case m.isServerRequest():
		c.write(answerServerRequest(&m))
	case m.isResponse():
		var id int64
		if _, err := fmt.Sscan(string(m.ID), &id); err != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- &m
		}
	}
	// notifications (progress, logging, list changes) are not used
}

func (c *stdioConn) write(m *rpcMsg) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.stdin.Write(append(raw, '\n'))
	return err
}

func (c *stdioConn) call(ctx context.Context, id int64, req *rpcMsg, _ map[string]string) (*rpcMsg, error) {
	ch := make(chan *rpcMsg, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	if err := c.write(req); err != nil {
		forget()
		select { // a dead process is the more useful explanation of a broken pipe
		case <-c.done:
			return nil, c.exitErr
		case <-time.After(500 * time.Millisecond):
			return nil, err
		}
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	case <-c.done:
		select { // a response may have landed just before the process ended
		case resp := <-ch:
			return resp, nil
		default:
			return nil, c.exitErr
		}
	}
}

func (c *stdioConn) notify(_ context.Context, method string, params any, _ map[string]string) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	return c.write(&rpcMsg{JSONRPC: "2.0", Method: method, Params: raw})
}

// close follows the protocol's shutdown order: close the server's stdin, give
// it a moment to exit, then kill it.
func (c *stdioConn) close() {
	c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(stopGrace):
		c.cmd.Process.Kill()
		<-c.done
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(strings.Fields(string(t.buf)), " ")
}
