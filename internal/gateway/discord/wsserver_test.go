package discord

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// serverConn is the server end of a WebSocket, written separately from the
// client in ws.go so the two check each other: it reads masked frames and
// writes unmasked ones.
type serverConn struct {
	c  net.Conn
	br *bufio.Reader
	mu sync.Mutex
}

// upgrade completes a WebSocket handshake on an HTTP request.
func upgrade(w http.ResponseWriter, r *http.Request) (*serverConn, error) {
	if r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, "not a websocket", http.StatusBadRequest)
		return nil, errors.New("not a websocket request")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("no hijack")
	}
	c, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + acceptKey(r.Header.Get("Sec-WebSocket-Key")) + "\r\n\r\n"
	if _, err := c.Write([]byte(resp)); err != nil {
		return nil, err
	}
	return &serverConn{c: c, br: brw.Reader}, nil
}

// frame reads one frame from the client, which must be masked.
func (s *serverConn) frame() (op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(s.br, h[:]); err != nil {
		return
	}
	op = h[0] & 0x0f
	if h[0]&0x80 == 0 {
		return 0, nil, errors.New("client sent a fragment; this server does not expect them")
	}
	if h[1]&0x80 == 0 {
		return 0, nil, errors.New("client frame was not masked")
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		io.ReadFull(s.br, b[:])
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		io.ReadFull(s.br, b[:])
		n = binary.BigEndian.Uint64(b[:])
	}
	var key [4]byte
	if _, err = io.ReadFull(s.br, key[:]); err != nil {
		return
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(s.br, payload); err != nil {
		return
	}
	for i := range payload {
		payload[i] ^= key[i%4]
	}
	return
}

// recv returns the next text message from the client, answering nothing else.
func (s *serverConn) recv() (map[string]any, error) {
	for {
		op, payload, err := s.frame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opText:
			var m map[string]any
			if err := json.Unmarshal(payload, &m); err != nil {
				return nil, err
			}
			return m, nil
		case opClose:
			code := 1005
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload))
			}
			return nil, &CloseError{Code: code}
		}
	}
}

func (s *serverConn) write(op byte, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hdr := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n < 1<<16:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	_, err := s.c.Write(append(hdr, payload...))
	return err
}

// send writes a gateway payload.
func (s *serverConn) send(op int, d any, seq int64, event string) error {
	m := map[string]any{"op": op, "d": d}
	if event != "" {
		m["t"], m["s"] = event, seq
	}
	raw, _ := json.Marshal(m)
	return s.write(opText, raw)
}

// close sends a close frame with a status code and hangs up.
func (s *serverConn) close(code int) {
	s.write(opClose, binary.BigEndian.AppendUint16(nil, uint16(code)))
	time.Sleep(10 * time.Millisecond)
	s.c.Close()
}

func (s *serverConn) drop() { s.c.Close() }
