package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Gateway opcodes.
const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatACK   = 11
)

// intentDirectMessages is the only intent the gateway asks for. Message
// content in direct messages needs no privileged intent.
const intentDirectMessages = 1 << 12

// Close codes after which reconnecting cannot help: the token, the intents or
// the API version are wrong.
var fatalCloseCodes = map[int]string{
	4004: "Discord rejected the bot token",
	4010: "invalid shard",
	4011: "sharding required",
	4012: "invalid API version",
	4013: "invalid intents",
	4014: "a privileged intent is not enabled for this bot",
}

// closeNewSession is a close code after which the session cannot be resumed.
func closeNewSession(code int) bool { return code == 4007 || code == 4009 }

// closeReconnect is what we close with to reconnect and resume; any code other
// than 1000 or 1001 keeps the session alive on Discord's side.
const closeReconnect = 4000

// Session keeps one bot connected to the Gateway: it identifies, heartbeats,
// resumes after a drop, and hands dispatched events to Handle.
type Session struct {
	API     *API
	Intents int
	Logf    func(format string, args ...any)
	// Handle receives each event by name. It runs on the connection's reading
	// goroutine and so must not block.
	Handle func(event string, data json.RawMessage)

	// URL, if set, is where to connect instead of asking Discord.
	URL string

	// Timing, replaceable by tests.
	Backoff       func(attempt int) time.Duration // wait before reconnect attempt n, from 1
	InvalidWait   func() time.Duration            // wait after Invalid Session
	HeartbeatSkew func() float64                  // 0..1: how much of the first interval to wait

	mu        sync.Mutex
	sessionID string
	resumeURL string
	seq       atomic.Int64 // 0 until the first event
	userID    string
}

func (s *Session) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Session) backoff(attempt int) time.Duration {
	if s.Backoff != nil {
		return s.Backoff(attempt)
	}
	d := time.Second << min(attempt-1, 6)
	return min(d, 60*time.Second)
}

// Run stays connected until ctx ends, or until Discord says that reconnecting is
// pointless (a bad token, say), which it returns as an error.
func (s *Session) Run(ctx context.Context) error {
	attempt := 0
	for ctx.Err() == nil {
		ready, err := s.connect(ctx)
		if ready {
			attempt = 0 // it worked for a while: start the backoff over
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var ce *CloseError
		var ae *APIError
		switch {
		case errors.As(err, &ce) && fatalCloseCodes[ce.Code] != "":
			return fmt.Errorf("%s (close code %d)", fatalCloseCodes[ce.Code], ce.Code)
		case errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden):
			return fmt.Errorf("Discord rejected the bot token: %w", err)
		case errors.As(err, &ce) && closeNewSession(ce.Code):
			s.forgetSession()
		}

		attempt++
		wait := s.backoff(attempt)
		s.logf("discord: disconnected (%v); reconnecting in %s", err, wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	return ctx.Err()
}

func (s *Session) forgetSession() {
	s.mu.Lock()
	s.sessionID, s.resumeURL = "", ""
	s.mu.Unlock()
	s.seq.Store(0)
}

type frame struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

// connect runs one connection until it ends. ready says whether the Gateway
// accepted us (READY or RESUMED) at some point.
func (s *Session) connect(ctx context.Context) (ready bool, err error) {
	s.mu.Lock()
	sessionID, base := s.sessionID, s.resumeURL
	s.mu.Unlock()
	if base == "" {
		if base = s.URL; base == "" {
			if base, err = s.API.GatewayURL(ctx); err != nil {
				return false, err
			}
		}
	}
	conn, err := Dial(ctx, base+"/?v=10&encoding=json", http.Header{"User-Agent": {userAgent}})
	if err != nil {
		return false, err
	}
	defer conn.Close(closeReconnect, "")
	// Ending ctx must unblock the read below.
	stop := context.AfterFunc(ctx, func() { conn.Close(1000, "shutting down") })
	defer stop()

	// First comes Hello, saying how often to heartbeat.
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	f, err := readFrame(conn)
	if err != nil {
		return false, err
	}
	if f.Op != opHello {
		return false, fmt.Errorf("expected Hello from the gateway, got opcode %d", f.Op)
	}
	var hello struct {
		HeartbeatInterval float64 `json:"heartbeat_interval"`
	}
	if json.Unmarshal(f.D, &hello) != nil || hello.HeartbeatInterval < 1 {
		return false, errors.New("the gateway's Hello had no heartbeat interval")
	}
	interval := time.Duration(hello.HeartbeatInterval * float64(time.Millisecond))

	hb := &heartbeater{conn: conn, seq: &s.seq, interval: interval, skew: s.HeartbeatSkew}
	hb.start()
	defer hb.stop()

	if sessionID != "" {
		err = send(conn, opResume, map[string]any{"token": s.API.Token, "session_id": sessionID, "seq": s.seq.Load()})
	} else {
		err = send(conn, opIdentify, map[string]any{
			"token":      s.API.Token,
			"intents":    s.intents(),
			"properties": map[string]string{"os": runtime.GOOS, "browser": "talaria", "device": "talaria"},
		})
	}
	if err != nil {
		return false, err
	}

	for {
		// Heartbeats are acknowledged, so a healthy connection is never silent for long.
		conn.SetReadDeadline(time.Now().Add(3*interval + 30*time.Second))
		f, err := readFrame(conn)
		if err != nil {
			return ready, err
		}
		if f.S != nil {
			s.seq.Store(*f.S)
		}
		switch f.Op {
		case opHeartbeatACK:
			hb.acked.Store(true)
		case opHeartbeat: // the gateway wants one now
			hb.beat()
		case opReconnect:
			return ready, errors.New("the gateway asked us to reconnect")
		case opInvalidSession:
			var resumable bool
			json.Unmarshal(f.D, &resumable)
			if !resumable {
				s.forgetSession()
			}
			wait := time.Duration(1+rand.IntN(5)) * time.Second // the docs ask for 1 to 5 seconds
			if s.InvalidWait != nil {
				wait = s.InvalidWait()
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
			return ready, errors.New("the gateway invalidated the session")
		case opDispatch:
			switch f.T {
			case "READY":
				var r struct {
					SessionID string `json:"session_id"`
					ResumeURL string `json:"resume_gateway_url"`
					User      User   `json:"user"`
				}
				if err := json.Unmarshal(f.D, &r); err != nil || r.SessionID == "" {
					return ready, errors.New("the gateway's READY could not be understood")
				}
				s.mu.Lock()
				s.sessionID, s.resumeURL, s.userID = r.SessionID, r.ResumeURL, r.User.ID
				s.mu.Unlock()
				ready = true
				s.logf("discord: connected as %s", r.User.Username)
			case "RESUMED":
				ready = true
				s.logf("discord: resumed")
			}
			if s.Handle != nil {
				s.Handle(f.T, f.D)
			}
		}
	}
}

func (s *Session) intents() int {
	if s.Intents != 0 {
		return s.Intents
	}
	return intentDirectMessages
}

// UserID is the bot's own user ID, known once the gateway has said READY.
func (s *Session) UserID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userID
}

func readFrame(c *Conn) (frame, error) {
	for {
		raw, err := c.ReadMessage()
		if err != nil {
			return frame{}, err
		}
		var f frame
		if err := json.Unmarshal(raw, &f); err != nil {
			continue // not something we know how to read; the next one may be
		}
		return f, nil
	}
}

func send(c *Conn, op int, d any) error {
	raw, err := json.Marshal(map[string]any{"op": op, "d": d})
	if err != nil {
		return err
	}
	return c.WriteText(raw)
}

// heartbeater sends a heartbeat every interval, and gives up on a connection
// that stops acknowledging them: the link may be dead without either end
// having noticed.
type heartbeater struct {
	conn     *Conn
	seq      *atomic.Int64
	interval time.Duration
	skew     func() float64

	acked atomic.Bool
	quit  chan struct{}
	wg    sync.WaitGroup
}

func (h *heartbeater) start() {
	h.quit = make(chan struct{})
	h.acked.Store(true) // nothing is owed yet
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		jitter := rand.Float64()
		if h.skew != nil {
			jitter = h.skew()
		}
		wait := time.Duration(float64(h.interval) * jitter)
		for {
			select {
			case <-h.quit:
				return
			case <-time.After(wait):
			}
			if !h.acked.Load() {
				h.conn.Close(closeReconnect, "no heartbeat acknowledgement")
				return
			}
			h.acked.Store(false)
			h.beat()
			wait = h.interval
		}
	}()
}

// beat sends one heartbeat now.
func (h *heartbeater) beat() {
	var d any // null until an event has been received
	if n := h.seq.Load(); n != 0 {
		d = n
	}
	send(h.conn, opHeartbeat, d)
}

func (h *heartbeater) stop() {
	close(h.quit)
	h.wg.Wait()
}
