// Package discord is a Discord gateway: a WebSocket session with Discord's
// Gateway for events, its REST API for sending, an allowlist, and
// button-approvals for risky tools. It serves direct messages only.
package discord

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// A WebSocket client (RFC 6455) with just what the Discord Gateway needs: text
// messages, ping/pong, close, fragmentation. No extensions are negotiated, so
// there is no compression to handle.

const (
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	// maxMessage bounds what one message may take in memory. Bigger messages
	// (a huge server's description at connect time) are read and thrown away:
	// nothing this gateway acts on is that large.
	maxMessage = 16 << 20
)

// CloseError is the peer closing the connection with a status code.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("websocket closed (code %d)", e.Code)
	}
	return fmt.Sprintf("websocket closed (code %d: %s)", e.Code, e.Reason)
}

// Conn is a WebSocket connection. ReadMessage is for one goroutine at a time;
// the write methods may be used from several.
type Conn struct {
	c  net.Conn
	br *bufio.Reader

	wmu    sync.Mutex
	closed bool

	maxMessage int          // tests lower it
	maskFn     func([]byte) // fills a masking key; tests fix it
}

// Dial opens a WebSocket to a ws:// or wss:// URL. A tight deadline on ctx
// bounds the connect and handshake.
func Dial(ctx context.Context, rawURL string, header http.Header) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	var secure bool
	switch u.Scheme {
	case "wss", "https":
		secure = true
	case "ws", "http":
	default:
		return nil, fmt.Errorf("not a websocket URL: %q", rawURL)
	}
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if secure {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}

	d := net.Dialer{Timeout: 20 * time.Second}
	var nc net.Conn
	if secure {
		nc, err = (&tls.Dialer{NetDialer: &d, Config: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", host)
	} else {
		nc, err = d.DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, err
	}

	// The handshake must not hang forever on a server that accepts and goes quiet.
	deadline := time.Now().Add(20 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	nc.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()

	var key [16]byte
	rand.Read(key[:])
	nonce := base64.StdEncoding.EncodeToString(key[:])

	req := (&http.Request{
		Method: "GET", URL: u, Host: u.Host, Header: http.Header{}, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
	}).WithContext(ctx)
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", nonce)
	req.Header.Set("Sec-WebSocket-Version", "13")
	if err := req.Write(nc); err != nil {
		nc.Close()
		return nil, err
	}

	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		nc.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("websocket handshake: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode != http.StatusSwitchingProtocols:
		nc.Close()
		return nil, fmt.Errorf("websocket handshake: server answered %s", resp.Status)
	case !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || !headerHasToken(resp.Header, "Connection", "upgrade"):
		nc.Close()
		return nil, errors.New("websocket handshake: the server did not agree to upgrade")
	case resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(nonce):
		nc.Close()
		return nil, errors.New("websocket handshake: wrong Sec-WebSocket-Accept")
	case resp.Header.Get("Sec-WebSocket-Extensions") != "":
		nc.Close()
		return nil, errors.New("websocket handshake: the server chose an extension that was not offered")
	}
	nc.SetDeadline(time.Time{})
	return &Conn{c: nc, br: br, maxMessage: maxMessage}, nil
}

func acceptKey(nonce string) string {
	sum := sha1.Sum([]byte(nonce + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// SetReadDeadline bounds the next ReadMessage; a zero time clears it.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.c.SetReadDeadline(t) }

// ReadMessage returns the next text or binary message, answering pings on the
// way. A close frame is answered and returned as a *CloseError.
func (c *Conn) ReadMessage() ([]byte, error) {
	var msg []byte
	var textMsg, inMessage, discarding bool
	for {
		fin, op, payload, err := c.readFrame(&discarding, len(msg))
		if err != nil {
			return nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			ce := &CloseError{Code: 1005}
			if len(payload) >= 2 {
				ce.Code = int(binary.BigEndian.Uint16(payload))
				ce.Reason = string(payload[2:])
			}
			// Echo the code, as the protocol asks, then let the caller see why.
			c.writeFrame(opClose, payload[:min(len(payload), 2)])
			c.c.Close()
			return nil, ce
		case opText, opBinary:
			if inMessage {
				return nil, c.fail(1002, "a new message began before the last one ended")
			}
			inMessage, textMsg = true, op == opText
		case opContinuation:
			if !inMessage {
				return nil, c.fail(1002, "continuation without a message")
			}
		default:
			return nil, c.fail(1002, "unknown opcode")
		}
		if !discarding {
			msg = append(msg, payload...)
		}
		if !fin {
			continue
		}
		if discarding {
			msg, textMsg, inMessage, discarding = nil, false, false, false
			continue // too big to use: skipped whole
		}
		if textMsg && !utf8.Valid(msg) {
			return nil, c.fail(1007, "text that is not UTF-8")
		}
		return msg, nil
	}
}

// readFrame reads one frame. A data frame that would push the message over the
// limit is not kept: discarding is set, and the rest of the message is skipped.
func (c *Conn) readFrame(discarding *bool, have int) (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0f
	if h[0]&0x70 != 0 {
		return false, 0, nil, c.fail(1002, "reserved bits set")
	}
	if h[1]&0x80 != 0 {
		return false, 0, nil, c.fail(1002, "the server must not mask frames")
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.br, b[:]); err != nil {
			return
		}
		if n = binary.BigEndian.Uint64(b[:]); n>>63 != 0 {
			return false, 0, nil, c.fail(1002, "bad frame length")
		}
	}
	if op >= opClose && (!fin || n > 125) {
		return false, 0, nil, c.fail(1002, "bad control frame")
	}
	limit := c.maxMessage
	if limit == 0 {
		limit = maxMessage
	}
	if op < opClose && (*discarding || uint64(have)+n > uint64(limit)) {
		*discarding = true
		if _, err = io.CopyN(io.Discard, c.br, int64(n)); err != nil {
			return
		}
		return fin, op, nil, nil
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(c.br, payload)
	return
}

// fail closes the connection over a protocol violation by the server.
func (c *Conn) fail(code int, reason string) error {
	c.Close(code, reason)
	return fmt.Errorf("websocket protocol error: %s", reason)
}

// WriteText sends one text message.
func (c *Conn) WriteText(b []byte) error { return c.writeFrame(opText, b) }

func (c *Conn) writeFrame(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	hdr := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 1<<16:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	var key [4]byte
	if c.maskFn != nil {
		c.maskFn(key[:])
	} else {
		rand.Read(key[:])
	}
	hdr = append(hdr, key[:]...)
	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ key[i%4]
	}
	c.c.SetWriteDeadline(time.Now().Add(20 * time.Second))
	_, err := c.c.Write(append(hdr, masked...))
	return err
}

// Close says goodbye with a status code and drops the connection. It does not
// wait for the peer's reply. Closing twice is harmless.
func (c *Conn) Close(code int, reason string) error {
	c.wmu.Lock()
	if c.closed {
		c.wmu.Unlock()
		return nil
	}
	c.wmu.Unlock()
	if len(reason) > 100 {
		reason = reason[:100]
	}
	body := append(binary.BigEndian.AppendUint16(nil, uint16(code)), reason...)
	c.writeFrame(opClose, body)
	c.wmu.Lock()
	c.closed = true
	c.wmu.Unlock()
	return c.c.Close()
}
