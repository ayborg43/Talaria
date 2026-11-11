package web

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"talaria/internal/browser"
)

// The live browser view over one WebSocket: pictures come as the page paints and key presses, taps and drags go in as
// they happen, in order, so the page answers as fast as the network allows. It replaces the older way (a picture
// polled every third of a second, and a request per tap) wherever a WebSocket can be had; the older way remains for
// connections that cannot.

// liveTab is what the live view needs of a tab. A *browser.Page is one.
type liveTab interface {
	probe
	Screencast(ctx context.Context, quality, maxWidth int, fn func(jpeg []byte, done func())) (func(), error)
	Snap(ctx context.Context, quality int) ([]byte, error)
	Pointer(ctx context.Context, kind string, x, y float64, button string, clicks int) error
	WheelNow(ctx context.Context, x, y, dx, dy float64) error
	KeyNow(ctx context.Context, name string, modifiers int) error
	TypeNow(ctx context.Context, text string) error
	ReplaceNow(ctx context.Context, text string) error
}

// liveLevels are how big and how sharp a picture is, from the best a good connection gets to what a poor one is given.
var liveLevels = []struct{ quality, width int }{{62, 1280}, {52, 1024}, {42, 800}, {36, 640}}

// liveMsg is what the page sends: a tap, a drag, a wheel, a key or some text.
type liveMsg struct {
	T    string  `json:"t"`    // mouse, wheel, key, text, replace
	K    string  `json:"k"`    // mouse: down, up or move
	X    float64 `json:"x"`    //
	Y    float64 `json:"y"`    //
	DX   float64 `json:"dx"`   // wheel
	DY   float64 `json:"dy"`   // wheel
	B    string  `json:"b"`    // mouse button: left, right or middle
	N    int     `json:"n"`    // mouse: 2 for the second press of a double click
	Key  string  `json:"key"`  // key: a name from browser.KeyNames
	Mod  int     `json:"mod"`  // key: modifiers (8 is shift)
	Text string  `json:"text"` // text or replace
}

// liveHold is one open view of a tab. A newer view of the same tab takes it over.
type liveHold struct {
	taken chan struct{}
	once  sync.Once
}

func (h *liveHold) take() { h.once.Do(func() { close(h.taken) }) }

var (
	liveMu    sync.Mutex
	liveByTab = map[string]*liveHold{}
)

// liveOf finds the tab a request names as something the live view can drive.
func (s *Server) liveOf(id string) liveTab {
	if s.liveFor != nil {
		return s.liveFor(id)
	}
	if _, p := s.tab(id); p != nil {
		return p
	}
	return nil
}

// wsOrigin says whether a WebSocket handshake came from this app's own page. A page cannot add the usual header to
// one, so the Origin the browser attaches (and a page on another site cannot forge) is what is checked.
func (s *Server) wsOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	if a := s.origin(); a != "" {
		return o == a
	}
	return sameHost(o, r.Host)
}

func (s *Server) browserLive(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	tab := s.liveOf(id)
	if tab == nil {
		fail(w, http.StatusNotFound, "that tab is no longer open")
		return
	}
	if !s.wsOrigin(r) {
		fail(w, http.StatusForbidden, "that connection did not come from the app")
		return
	}
	if streams.Add(1) > maxStreams {
		streams.Add(-1)
		fail(w, http.StatusTooManyRequests, "too many live views are open")
		return
	}
	defer streams.Add(-1)
	c, err := upgradeWS(w, r)
	if err != nil {
		return // the handshake already answered, or could not be
	}
	defer c.close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// One view of a tab at a time: a newer one (the same person on another screen) takes over from the older.
	mine := &liveHold{taken: make(chan struct{})}
	liveMu.Lock()
	if old := liveByTab[id]; old != nil {
		old.take()
	}
	liveByTab[id] = mine
	liveMu.Unlock()
	defer func() {
		liveMu.Lock()
		if liveByTab[id] == mine {
			delete(liveByTab, id)
		}
		liveMu.Unlock()
	}()
	go func() {
		select {
		case <-mine.taken:
			c.writeText(`{"t":"taken"}`)
			c.close()
		case <-ctx.Done():
		}
	}()

	// Pictures: the newest waits in a slot of one, and the browser is told it may send another only once this one is
	// on the wire, so a slow connection gets newer pictures, never a queue of old ones.
	type frame struct {
		img  []byte
		done func()
	}
	slot := make(chan frame, 1)
	push := func(img []byte, done func()) {
		for {
			select {
			case slot <- frame{img, done}:
				return
			default:
			}
			select {
			case old := <-slot:
				old.done()
			default:
			}
		}
	}
	level := 0
	var stop func()
	start := func() error {
		if stop != nil {
			stop()
		}
		var err error
		stop, err = tab.Screencast(ctx, liveLevels[level].quality, liveLevels[level].width, push)
		return err
	}
	if err := start(); err != nil {
		c.writeText(`{"t":"error","msg":"the live view could not start"}`)
		return
	}
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	c.writeText(`{"t":"ready","w":` + strconv.Itoa(browser.Width) + `,"h":` + strconv.Itoa(browser.Height) + `}`)
	if img, err := tab.Snap(ctx, liveLevels[level].quality); err == nil { // the page as it is now, before it next changes
		c.writeBinary(img)
	}

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		slow, fast := 0, 0
		for {
			select {
			case f := <-slot:
				began := time.Now()
				err := c.writeBinary(f.img)
				f.done()
				if err != nil {
					c.close()
					return
				}
				// A connection that takes long over a picture gets smaller ones; one that is quick gets sharper ones.
				switch took := time.Since(began); {
				case took > 220*time.Millisecond:
					slow, fast = slow+1, 0
				case took < 30*time.Millisecond:
					fast, slow = fast+1, 0
				default:
					slow, fast = 0, 0
				}
				switch {
				case slow >= 4 && level < len(liveLevels)-1:
					level, slow = level+1, 0
					start()
				case fast >= 40 && level > 0:
					level, fast = level-1, 0
					start()
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Keeps an idle connection alive through proxies, and finds a viewer that has gone.
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
			case <-ctx.Done():
				return
			}
		}
	}()

	var pending atomic.Bool // a look at what has focus is already waiting
	afterwards := func() {  // what has focus and where the page is now, once a tap or key has had its effect
		if !pending.CompareAndSwap(false, true) {
			return
		}
		time.AfterFunc(140*time.Millisecond, func() {
			pending.Store(false)
			cctx, cc := context.WithTimeout(ctx, 3*time.Second)
			defer cc()
			b, _ := json.Marshal(map[string]string{"t": "focus", "kind": tab.FocusKind(cctx), "url": tab.Where(cctx)})
			c.write(wsText, b)
		})
	}

	for {
		op, msg, err := c.read(70 * time.Second)
		if err != nil {
			break
		}
		if op != wsText {
			continue
		}
		var m liveMsg
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		s.liveInput(ctx, tab, m, afterwards)
	}
	cancel()
	<-writerDone
}

// liveInput hands one message from the page to the browser, and notes it as a step if a routine is being recorded.
func (s *Server) liveInput(ctx context.Context, tab liveTab, m liveMsg, afterwards func()) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if s.Rec != nil && s.Rec.Recording() {
		var kind, text, key string
		switch {
		case m.T == "mouse" && m.K == "down" && m.B != "right" && m.N <= 1:
			kind = "click"
		case m.T == "text":
			kind, text = "type", m.Text
		case m.T == "replace":
			kind, text = "replace", m.Text
		case m.T == "key":
			kind, key = "key", m.Key
		case m.T == "wheel":
			kind = "wheel"
		}
		if kind != "" {
			if st := s.recordStep(cctx, tab, kind, m.X, m.Y, m.DY, text, key, ""); st != nil {
				s.Rec.Note(*st)
			}
		}
	}
	switch m.T {
	case "mouse":
		_ = tab.Pointer(cctx, m.K, m.X, m.Y, m.B, m.N)
		if m.K == "up" {
			afterwards()
		}
	case "wheel":
		_ = tab.WheelNow(cctx, m.X, m.Y, m.DX, m.DY)
	case "key":
		_ = tab.KeyNow(cctx, m.Key, m.Mod)
		afterwards()
	case "text":
		_ = tab.TypeNow(cctx, m.Text)
	case "replace":
		_ = tab.ReplaceNow(cctx, m.Text)
	}
}

var hostOK = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]{1,5})?$|^\[[0-9A-Fa-f:]+\](:[0-9]{1,5})?$`)

// wsSources are the WebSocket addresses the page may connect to: this app's own host and no other. Some browsers do not
// count a WebSocket to the same host as "self", so it is spelled out; the host is checked to be only a name and a port.
func wsSources(r *http.Request) string {
	if !hostOK.MatchString(r.Host) {
		return ""
	}
	return "ws://" + r.Host + " wss://" + r.Host
}
