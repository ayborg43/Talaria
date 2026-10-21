package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGateway is a Discord Gateway that plays a script on each connection.
type fakeGateway struct {
	t   *testing.T
	srv *httptest.Server
	url string // ws URL of this server

	mu     sync.Mutex
	conns  int
	script func(n int, c *serverConn) // called per connection, n from 1
}

func newFakeGateway(t *testing.T, script func(n int, c *serverConn)) *fakeGateway {
	g := &fakeGateway{t: t, script: script}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrade(w, r)
		if err != nil {
			return
		}
		g.mu.Lock()
		g.conns++
		n := g.conns
		g.mu.Unlock()
		defer c.drop()
		g.script(n, c)
	}))
	t.Cleanup(g.srv.Close)
	g.url = "ws" + strings.TrimPrefix(g.srv.URL, "http")
	return g
}

func (g *fakeGateway) connections() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.conns
}

func hello(c *serverConn, interval int) {
	c.send(opHello, map[string]any{"heartbeat_interval": interval}, 0, "")
}

// serve answers heartbeats until the client goes away, optionally calling onMsg
// for everything else.
func serve(c *serverConn, onMsg func(map[string]any)) {
	for {
		m, err := c.recv()
		if err != nil {
			return
		}
		if m["op"] == float64(opHeartbeat) {
			c.send(opHeartbeatACK, nil, 0, "")
			continue
		}
		if onMsg != nil {
			onMsg(m)
		}
	}
}

func ready(c *serverConn, seq int64, resumeURL string) {
	c.send(opDispatch, map[string]any{"session_id": "sess-1", "resume_gateway_url": resumeURL, "user": map[string]any{"id": "900", "username": "talbot"}}, seq, "READY")
}

type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (l *eventLog) add(name string, _ json.RawMessage) {
	l.mu.Lock()
	l.ev = append(l.ev, name)
	l.mu.Unlock()
}
func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ev...)
}

func newSession(g *fakeGateway, handle func(string, json.RawMessage)) *Session {
	return &Session{
		API: NewAPI("TOKEN"), URL: g.url, Handle: handle,
		Backoff:       func(int) time.Duration { return 5 * time.Millisecond },
		InvalidWait:   func() time.Duration { return 5 * time.Millisecond },
		HeartbeatSkew: func() float64 { return 0.1 },
		Logf:          g.t.Logf,
	}
}

func runSession(t *testing.T, s *Session) (stop func(), result chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	result = make(chan error, 1)
	done := make(chan struct{})
	go func() { result <- s.Run(ctx); close(done) }()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("the session did not stop")
		}
	}
	t.Cleanup(cancel)
	return stop, result
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestIdentifiesWithOnlyTheDirectMessageIntent(t *testing.T) {
	got := make(chan map[string]any, 1)
	g := newFakeGateway(t, func(n int, c *serverConn) {
		hello(c, 40)
		m, err := c.recv()
		if err != nil {
			return
		}
		got <- m
		ready(c, 1, "")
		c.send(opDispatch, map[string]any{"content": "hi"}, 2, "MESSAGE_CREATE")
		serve(c, nil)
	})
	log := &eventLog{}
	stop, _ := runSession(t, newSession(g, log.add))
	defer stop()

	select {
	case m := <-got:
		d, _ := m["d"].(map[string]any)
		props, _ := d["properties"].(map[string]any)
		if m["op"] != float64(opIdentify) || d["token"] != "TOKEN" || d["intents"] != float64(4096) || props["browser"] != "talaria" {
			t.Fatalf("identify: %v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no identify")
	}
	waitUntil(t, "events", func() bool { return len(log.all()) == 2 })
	if ev := log.all(); ev[0] != "READY" || ev[1] != "MESSAGE_CREATE" {
		t.Fatalf("%v", ev)
	}
}

func TestHeartbeatsCarryTheLastSequence(t *testing.T) {
	beats := make(chan any, 16)
	g := newFakeGateway(t, func(n int, c *serverConn) {
		hello(c, 30)
		c.recv() // identify
		ready(c, 1, "")
		c.send(opDispatch, map[string]any{}, 7, "MESSAGE_CREATE")
		for {
			m, err := c.recv()
			if err != nil {
				return
			}
			if m["op"] == float64(opHeartbeat) {
				beats <- m["d"]
				c.send(opHeartbeatACK, nil, 0, "")
			}
		}
	})
	stop, _ := runSession(t, newSession(g, nil))
	defer stop()
	seen7 := false
	deadline := time.After(3 * time.Second)
	for i := 0; i < 4 && !seen7; i++ {
		select {
		case d := <-beats:
			seen7 = d == float64(7)
		case <-deadline:
			t.Fatal("no heartbeat")
		}
	}
	if !seen7 {
		t.Fatal("heartbeats should carry the sequence number of the last event, 7")
	}
}

func TestServerAskedHeartbeatIsAnsweredAtOnce(t *testing.T) {
	got := make(chan struct{}, 1)
	g := newFakeGateway(t, func(n int, c *serverConn) {
		hello(c, 600000) // regular heartbeats are far off
		c.recv()
		ready(c, 1, "")
		c.send(opHeartbeat, nil, 0, "")
		for {
			m, err := c.recv()
			if err != nil {
				return
			}
			if m["op"] == float64(opHeartbeat) {
				got <- struct{}{}
				c.send(opHeartbeatACK, nil, 0, "")
			}
		}
	})
	s := newSession(g, nil)
	s.HeartbeatSkew = func() float64 { return 1 }
	stop, _ := runSession(t, s)
	defer stop()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("a heartbeat request must be answered immediately")
	}
}

// After a dropped connection the session resumes, at the address Discord gave,
// with the session ID and the last sequence number, and does not identify again.
func TestResumesAfterADroppedConnection(t *testing.T) {
	second := make(chan map[string]any, 1)
	var resumeSrv *fakeGateway
	first := newFakeGateway(t, func(n int, c *serverConn) {
		hello(c, 40)
		c.recv()
		ready(c, 1, resumeSrv.url) // tells the client to come back to the other server
		c.send(opDispatch, map[string]any{}, 2, "MESSAGE_CREATE")
		c.send(opDispatch, map[string]any{}, 3, "MESSAGE_CREATE")
		time.Sleep(50 * time.Millisecond)
		c.drop()
	})
	resumeSrv = newFakeGateway(t, func(n int, c *serverConn) {
		hello(c, 40)
		m, err := c.recv()
		if err != nil {
			return
		}
		second <- m
		c.send(opDispatch, map[string]any{}, 4, "RESUMED")
		serve(c, nil)
	})
	log := &eventLog{}
	stop, _ := runSession(t, newSession(first, log.add))
	defer stop()

	select {
	case m := <-second:
		d, _ := m["d"].(map[string]any)
		if m["op"] != float64(opResume) || d["session_id"] != "sess-1" || d["seq"] != float64(3) || d["token"] != "TOKEN" {
			t.Fatalf("resume: %v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the session did not resume")
	}
	waitUntil(t, "RESUMED", func() bool { return strings.Contains(strings.Join(log.all(), " "), "RESUMED") })
	if first.connections() != 1 {
		t.Fatalf("the original address was used %d times; resuming goes to the resume URL", first.connections())
	}
}

func TestReconnectRequestResumes(t *testing.T) {
	ops := make(chan float64, 4)
	g := newFakeGateway(t, func(n int, c *serverConn) {
		hello(c, 40)
		m, err := c.recv()
		if err != nil {
			return
		}
		ops <- m["op"].(float64)
		if n == 1 {
			ready(c, 1, "")
			c.send(opReconnect, nil, 0, "")
			time.Sleep(500 * time.Millisecond)
			return
		}
		c.send(opDispatch, map[string]any{}, 2, "RESUMED")
		serve(c, nil)
	})
	// "" resume URL from READY: the client must fall back to the usual address.
	s := newSession(g, nil)
	stop, _ := runSession(t, s)
	defer stop()
	for i, want := range []float64{opIdentify, opResume} {
		select {
		case got := <-ops:
			if got != want {
				t.Fatalf("connection %d sent op %v, want %v", i+1, got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("connection %d never sent its handshake", i+1)
		}
	}
}

func TestInvalidSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		resumable bool
		want      float64
	}{
		{"not resumable: identify afresh", false, opIdentify},
		{"resumable: resume", true, opResume},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := make(chan float64, 4)
			g := newFakeGateway(t, func(n int, c *serverConn) {
				hello(c, 40)
				m, err := c.recv()
				if err != nil {
					return
				}
				ops <- m["op"].(float64)
				if n == 1 {
					ready(c, 1, "")
					c.send(opInvalidSession, tc.resumable, 0, "")
					time.Sleep(300 * time.Millisecond)
					return
				}
				serve(c, nil)
			})
			stop, _ := runSession(t, newSession(g, nil))
			defer stop()
			<-ops
			select {
			case got := <-ops:
				if got != tc.want {
					t.Fatalf("after invalid session (resumable=%v) the client sent op %v, want %v", tc.resumable, got, tc.want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no second handshake")
			}
		})
	}
}

// A bad token or bad intents will never work however often we retry, so the
// session stops and says why instead of hammering Discord.
func TestFatalCloseCodesStopTheSession(t *testing.T) {
	for code, want := range map[int]string{4004: "token", 4013: "intents", 4014: "intent"} {
		g := newFakeGateway(t, func(n int, c *serverConn) {
			hello(c, 40)
			c.recv()
			c.close(code)
		})
		stop, result := runSession(t, newSession(g, nil))
		select {
		case err := <-result:
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("code %d: %v", code, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("code %d: the session kept trying", code)
		}
		time.Sleep(50 * time.Millisecond)
		if n := g.connections(); n != 1 {
			t.Fatalf("code %d: connected %d times", code, n)
		}
		stop()
	}
}

// A session Discord has dropped (timed out, bad sequence) cannot be resumed.
func TestCloseCodesThatEndTheSessionIdentifyAgain(t *testing.T) {
	for _, code := range []int{4007, 4009} {
		ops := make(chan float64, 4)
		g := newFakeGateway(t, func(n int, c *serverConn) {
			hello(c, 40)
			m, err := c.recv()
			if err != nil {
				return
			}
			ops <- m["op"].(float64)
			if n == 1 {
				ready(c, 1, "")
				c.send(opDispatch, map[string]any{}, 2, "MESSAGE_CREATE")
				time.Sleep(30 * time.Millisecond)
				c.close(code)
				return
			}
			serve(c, nil)
		})
		stop, _ := runSession(t, newSession(g, nil))
		<-ops
		select {
		case got := <-ops:
			if got != opIdentify {
				t.Fatalf("code %d: sent op %v, want a fresh Identify", code, got)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("code %d: no reconnect", code)
		}
		stop()
	}
}

// A connection that stops acknowledging heartbeats is dead even if the socket
// still looks open, so it is dropped and replaced.
func TestUnacknowledgedHeartbeatsForceAReconnect(t *testing.T) {
	g := newFakeGateway(t, func(n int, c *serverConn) {
		hello(c, 25)
		c.recv()
		ready(c, 1, "")
		if n == 1 {
			for { // never acknowledges anything
				if _, err := c.recv(); err != nil {
					return
				}
			}
		}
		serve(c, nil)
	})
	stop, _ := runSession(t, newSession(g, nil))
	defer stop()
	waitUntil(t, "a second connection", func() bool { return g.connections() >= 2 })
}

// Shutting down while connected ends the session promptly, with a normal close.
func TestStopsPromptlyWhenTheContextEnds(t *testing.T) {
	closed := make(chan int, 1)
	g := newFakeGateway(t, func(n int, c *serverConn) {
		hello(c, 30000)
		c.recv()
		ready(c, 1, "")
		_, err := c.recv()
		if ce, ok := err.(*CloseError); ok {
			closed <- ce.Code
		}
	})
	stop, result := runSession(t, newSession(g, nil))
	waitUntil(t, "a connection", func() bool { return g.connections() == 1 })
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	stop()
	if err := <-result; err != context.Canceled && err != nil {
		t.Logf("result: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took %s to stop", time.Since(start))
	}
	select {
	case code := <-closed:
		if code != 1000 {
			t.Fatalf("closed with %d; a deliberate shutdown is 1000, which also ends the session on Discord's side", code)
		}
	case <-time.After(time.Second):
		t.Fatal("no close frame")
	}
}

// Failing to learn the gateway address (Discord's REST API down) is retried;
// a rejected token is not.
func TestGatewayURLLookup(t *testing.T) {
	var mu sync.Mutex
	var calls int
	var gw *fakeGateway
	gw = newFakeGateway(t, func(n int, c *serverConn) {
		hello(c, 40)
		c.recv()
		ready(c, 1, "")
		serve(c, nil)
	})
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"url":"` + gw.url + `"}`))
	}))
	defer rest.Close()
	s := newSession(gw, nil)
	s.URL = ""
	s.API.Base = rest.URL
	stop, _ := runSession(t, s)
	defer stop()
	waitUntil(t, "a connection after the REST API recovers", func() bool { return gw.connections() == 1 })

	// A 401 is final.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"401: Unauthorized","code":0}`))
	}))
	defer bad.Close()
	s2 := newSession(gw, nil)
	s2.URL = ""
	s2.API.Base = bad.URL
	_, result := runSession(t, s2)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "token") {
			t.Fatalf("%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a rejected token must stop the session")
	}
}
