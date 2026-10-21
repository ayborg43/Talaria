package discord

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeConn returns a client Conn whose peer is the other end of a pipe.
func pipeConn() (*Conn, net.Conn) {
	a, b := net.Pipe()
	return &Conn{c: a, br: bufio.NewReader(a), maxMessage: maxMessage}, b
}

// The examples are the ones in RFC 6455 section 5.7 and 1.3.
func TestAcceptKeyMatchesRFC(t *testing.T) {
	if got := acceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteIsMaskedLikeTheRFCExample(t *testing.T) {
	c, peer := pipeConn()
	c.maskFn = func(k []byte) { copy(k, []byte{0x37, 0xfa, 0x21, 0x3d}) }
	go c.WriteText([]byte("Hello"))
	want := []byte{0x81, 0x85, 0x37, 0xfa, 0x21, 0x3d, 0x7f, 0x9f, 0x4d, 0x51, 0x58}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(peer, got); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got % x (%v), want % x", got, err, want)
	}
}

func TestWriteLengthForms(t *testing.T) {
	for _, n := range []int{0, 125, 126, 65535, 65536, 70000} {
		c, peer := pipeConn()
		payload := bytes.Repeat([]byte("x"), n)
		go c.WriteText(payload)
		s := &serverConn{c: peer, br: bufio.NewReader(peer)}
		op, got, err := s.frame()
		if err != nil || op != opText || !bytes.Equal(got, payload) {
			t.Fatalf("length %d: op=%d len=%d err=%v", n, op, len(got), err)
		}
	}
}

func read(t *testing.T, c *Conn, peer net.Conn, bytesIn []byte) ([]byte, error) {
	t.Helper()
	go peer.Write(bytesIn)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	return c.ReadMessage()
}

func TestReadRFCExamples(t *testing.T) {
	c, peer := pipeConn()
	if got, err := read(t, c, peer, []byte{0x81, 0x05, 0x48, 0x65, 0x6c, 0x6c, 0x6f}); err != nil || string(got) != "Hello" {
		t.Fatalf("unmasked: %q %v", got, err)
	}
	// "Hel" + "lo" in two fragments.
	if got, err := read(t, c, peer, []byte{0x01, 0x03, 0x48, 0x65, 0x6c, 0x80, 0x02, 0x6c, 0x6f}); err != nil || string(got) != "Hello" {
		t.Fatalf("fragmented: %q %v", got, err)
	}
	// 256 bytes, which needs the 16-bit length form.
	body := bytes.Repeat([]byte("a"), 256)
	if got, err := read(t, c, peer, append([]byte{0x82, 0x7e, 0x01, 0x00}, body...)); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("16-bit length: %d bytes, %v", len(got), err)
	}
}

// A ping may arrive in the middle of a fragmented message; the answer is a pong
// carrying the same bytes, and the message is not disturbed.
func TestReadAnswersPingInTheMiddleOfAMessage(t *testing.T) {
	c, peer := pipeConn()
	c.maskFn = func(k []byte) { copy(k, []byte{1, 2, 3, 4}) }
	frames := append([]byte{0x01, 0x03, 0x48, 0x65, 0x6c}, 0x89, 0x05, 0x48, 0x65, 0x6c, 0x6c, 0x6f)
	frames = append(frames, 0x80, 0x02, 0x6c, 0x6f)
	pong := make(chan []byte, 1)
	go func() {
		peer.Write(frames)
		buf := make([]byte, 11)
		io.ReadFull(peer, buf)
		pong <- buf
	}()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := c.ReadMessage()
	if err != nil || string(got) != "Hello" {
		t.Fatalf("%q %v", got, err)
	}
	want := []byte{0x8a, 0x85, 1, 2, 3, 4, 0x48 ^ 1, 0x65 ^ 2, 0x6c ^ 3, 0x6c ^ 4, 0x6f ^ 1}
	select {
	case p := <-pong:
		if !bytes.Equal(p, want) {
			t.Fatalf("pong % x, want % x", p, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no pong")
	}
}

func TestReadCloseFrame(t *testing.T) {
	c, peer := pipeConn()
	echo := make(chan []byte, 1)
	go func() {
		peer.Write([]byte{0x88, 0x08, 0x0f, 0xb4, 'n', 'o', 'p', 'e', '!', '!'}) // 4020
		buf := make([]byte, 8)
		io.ReadFull(peer, buf)
		echo <- buf
	}()
	c.maskFn = func(k []byte) {}
	_, err := c.ReadMessage()
	ce, ok := err.(*CloseError)
	if !ok || ce.Code != 4020 || ce.Reason != "nope!!" {
		t.Fatalf("got %#v", err)
	}
	select {
	case e := <-echo:
		if e[0] != 0x88 || e[1] != 0x82 || e[6] != 0x0f || e[7] != 0xb4 {
			t.Fatalf("the close was not echoed with its code: % x", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close frame not answered")
	}
}

func TestReadRejectsProtocolViolations(t *testing.T) {
	for name, frames := range map[string][]byte{
		"masked from the server": {0x81, 0x85, 1, 2, 3, 4, 0, 0, 0, 0, 0},
		"reserved bits":          {0xc1, 0x00},
		"fragmented ping":        {0x09, 0x00},
		"oversized ping":         append([]byte{0x89, 126, 0, 126}, make([]byte, 126)...),
		"continuation first":     {0x80, 0x01, 'x'},
		"unknown opcode":         {0x83, 0x00},
		"invalid utf-8":          {0x81, 0x02, 0xff, 0xfe},
		"new message mid-way":    {0x01, 0x01, 'a', 0x01, 0x01, 'b'},
	} {
		c, peer := pipeConn()
		go func() { // the client answers with a close frame, which must be drained
			io.Copy(io.Discard, peer)
		}()
		if _, err := read(t, c, peer, frames); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A message over the limit is skipped, not buffered and not fatal.
func TestReadSkipsOversizedMessages(t *testing.T) {
	c, peer := pipeConn()
	c.maxMessage = 100
	big := bytes.Repeat([]byte("z"), 300)
	var frames []byte
	frames = append(frames, 0x01, 0x7e, 0x00, 0x96) // first 150 bytes, not final
	frames = append(frames, big[:150]...)
	frames = append(frames, 0x80, 0x7e, 0x00, 0x96) // the other 150
	frames = append(frames, big[150:]...)
	frames = append(frames, 0x81, 0x02, 'o', 'k')
	got, err := read(t, c, peer, frames)
	if err != nil || string(got) != "ok" {
		t.Fatalf("the oversized message should be skipped and the next one read: %q %v", got, err)
	}
}

// A dial against a server in the test: the handshake must check what the server says.
func TestDialHandshake(t *testing.T) {
	var gotReq *http.Request
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotReq = r
		mu.Unlock()
		s, err := upgrade(w, r)
		if err != nil {
			return
		}
		s.write(opText, []byte(`{"hello":1}`))
		s.recv()
	}))
	defer srv.Close()

	ctx := context.Background()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/path?v=10", http.Header{"User-Agent": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "")
	msg, err := c.ReadMessage()
	if err != nil || string(msg) != `{"hello":1}` {
		t.Fatalf("%q %v", msg, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotReq.URL.RequestURI() != "/path?v=10" || gotReq.Header.Get("User-Agent") != "x" ||
		gotReq.Header.Get("Sec-WebSocket-Version") != "13" || len(gotReq.Header.Get("Sec-WebSocket-Key")) != 24 {
		t.Fatalf("bad handshake request: %+v", gotReq.Header)
	}
}

func TestDialRejectsABadHandshake(t *testing.T) {
	for name, reply := range map[string]string{
		"not an upgrade":      "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n",
		"wrong accept":        "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: AAAA\r\n\r\n",
		"no accept":           "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n",
		"unoffered extension": "EXT",
	} {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			br := bufio.NewReader(c)
			req, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			out := reply
			if reply == "EXT" {
				out = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Extensions: permessage-deflate\r\n" +
					"Sec-WebSocket-Accept: " + acceptKey(req.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n"
			}
			c.Write([]byte(out))
			time.Sleep(200 * time.Millisecond)
			c.Close()
		}()
		if c, err := Dial(context.Background(), "ws://"+ln.Addr().String(), nil); err == nil {
			c.Close(1000, "")
			t.Errorf("%s: accepted", name)
		}
		ln.Close()
	}
}

func TestDialStopsWhenTheContextEnds(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() { // accepts and says nothing
		c, err := ln.Accept()
		if err == nil {
			time.Sleep(3 * time.Second)
			c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Dial(ctx, "ws://"+ln.Addr().String(), nil); err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s to give up", time.Since(start))
	}
}
