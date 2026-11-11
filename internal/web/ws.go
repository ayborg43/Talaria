package web

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A minimal WebSocket server, enough for the live browser view: text and binary messages, ping and pong, close. The
// live view needs one open connection that carries pictures one way and key presses the other, in order, with none of
// the cost of a new request for each. There is no compression, no extensions and no subprotocol.

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	wsText   = 1
	wsBinary = 2
	wsClose  = 8
	wsPing   = 9
	wsPong   = 10

	// wsMaxIn bounds one message from the page: key presses and taps are tiny; a pasted run of text is the most.
	wsMaxIn = 64 << 10
)

var errWSClosed = errors.New("the connection was closed")

type wsConn struct {
	c  net.Conn
	br *bufio.Reader

	wmu sync.Mutex
}

// upgradeWS accepts a WebSocket from the page. The caller has already checked who is asking.
func upgradeWS(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return nil, errors.New("this is for a WebSocket")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if r.Header.Get("Sec-WebSocket-Version") != "13" || key == "" {
		return nil, errors.New("not a WebSocket request")
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if err == nil {
		err = rw.Flush()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{}) // the server's own timeouts were for a request; this connection lives as long as the view
	return &wsConn{c: conn, br: rw.Reader}, nil
}

func (c *wsConn) close() { c.c.Close() }

// write sends one frame. A viewer that stops reading must not hold the browser forever, so a write gets ten seconds.
func (c *wsConn) write(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	hdr := make([]byte, 0, 10)
	hdr = append(hdr, 0x80|op)
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, byte(n))
	case n <= 0xffff:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	c.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.c.Write(hdr); err != nil {
		return err
	}
	_, err := c.c.Write(payload)
	return err
}

func (c *wsConn) writeText(s string) error   { return c.write(wsText, []byte(s)) }
func (c *wsConn) writeBinary(b []byte) error { return c.write(wsBinary, b) }
func (c *wsConn) writeClose(code uint16) error {
	return c.write(wsClose, []byte{byte(code >> 8), byte(code)})
}

// read returns the next text or binary message, answering pings and noticing a close on the way. The page must mask
// what it sends, as the protocol says; an unmasked frame ends the connection.
func (c *wsConn) read(idle time.Duration) (op byte, msg []byte, err error) {
	var whole []byte
	var first byte
	for {
		c.c.SetReadDeadline(time.Now().Add(idle))
		var h [2]byte
		if _, err := io.ReadFull(c.br, h[:]); err != nil {
			return 0, nil, err
		}
		fin, code := h[0]&0x80 != 0, h[0]&0x0f
		masked, n := h[1]&0x80 != 0, uint64(h[1]&0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return 0, nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if !masked || n > wsMaxIn || uint64(len(whole))+n > wsMaxIn || (code >= wsClose && (n > 125 || !fin)) {
			return 0, nil, errors.New("a frame the live view does not accept")
		}
		var mask [4]byte
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, err
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return 0, nil, err
		}
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
		switch code {
		case wsPing:
			c.write(wsPong, payload)
			continue
		case wsPong:
			continue
		case wsClose:
			c.writeClose(1000)
			return 0, nil, errWSClosed
		case 0:
			if first == 0 {
				return 0, nil, errors.New("a continuation with nothing before it")
			}
		case wsText, wsBinary:
			if first != 0 {
				return 0, nil, errors.New("a new message before the last one ended")
			}
			first = code
		default:
			return 0, nil, errors.New("a frame the live view does not accept")
		}
		whole = append(whole, payload...)
		if fin {
			return first, whole, nil
		}
	}
}
