package web

import (
	"net/http"
	"time"

	phonelink "talaria/internal/phone"
)

// phoneStatus says whether the user's phone is linked and what it has agreed to share, for Settings.
func (s *Server) phoneStatus(w http.ResponseWriter, r *http.Request) {
	if s.Phone == nil {
		writeJSON(w, http.StatusOK, phonelink.Status{})
		return
	}
	st := s.Phone.Status()
	if st.Caps == nil {
		st.Caps = []string{}
	}
	writeJSON(w, http.StatusOK, st)
}

// phoneConn lets the phone hub write to a WebSocket.
type phoneConn struct{ c *wsConn }

func (p phoneConn) Send(msg []byte) error { return p.c.writeText(string(msg)) }
func (p phoneConn) Close()                { p.c.close() }

// phoneLink is the phone's standing connection. The phone says what it shares, then answers the agent's requests as
// they come; the connection is for that and nothing else. Only the signed-in app, sending its own Origin, may open it.
func (s *Server) phoneLink(w http.ResponseWriter, r *http.Request) {
	if s.Phone == nil {
		fail(w, http.StatusNotFound, "this server has no phone link")
		return
	}
	if !s.wsOrigin(r) {
		fail(w, http.StatusForbidden, "that connection did not come from the app")
		return
	}
	c, err := upgradeWS(w, r)
	if err != nil {
		return
	}
	defer c.close()
	link := s.Phone.Attach(phoneConn{c})
	defer link.Detach()
	done := make(chan struct{})
	defer close(done)
	// Keeps an idle connection alive through proxies, and finds a phone that has gone.
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if c.write(wsPing, nil) != nil {
					c.close()
					return
				}
			case <-done:
				return
			}
		}
	}()
	for {
		op, msg, err := c.read(70 * time.Second)
		if err != nil {
			return
		}
		if op == wsText {
			link.Receive(msg)
		}
	}
}
