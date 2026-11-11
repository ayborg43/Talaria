package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"talaria/internal/browser"
	"talaria/internal/routine"
)

// fakeLive is a tab that remembers what it was told, in order.
type fakeLive struct {
	fakePage
	mu     sync.Mutex
	events []string
	push   func([]byte, func())
	done   chan struct{}
	stops  int
}

func (f *fakeLive) note(s string) { f.mu.Lock(); f.events = append(f.events, s); f.mu.Unlock() }
func (f *fakeLive) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}
func (f *fakeLive) Screencast(_ context.Context, q, w int, fn func([]byte, func())) (func(), error) {
	f.mu.Lock()
	f.push = fn
	f.mu.Unlock()
	return func() { f.mu.Lock(); f.stops++; f.mu.Unlock() }, nil
}
func (f *fakeLive) Snap(context.Context, int) ([]byte, error) { return []byte("\xff\xd8FIRST"), nil }
func (f *fakeLive) Pointer(_ context.Context, k string, x, y float64, b string, n int) error {
	f.note("mouse " + k + " " + b)
	return nil
}
func (f *fakeLive) WheelNow(_ context.Context, x, y, dx, dy float64) error {
	f.note("wheel")
	return nil
}
func (f *fakeLive) KeyNow(_ context.Context, name string, mod int) error {
	f.note("key " + name)
	return nil
}
func (f *fakeLive) TypeNow(_ context.Context, t string) error    { f.note("type " + t); return nil }
func (f *fakeLive) ReplaceNow(_ context.Context, t string) error { f.note("replace " + t); return nil }

// wsClient is the page's end of a WebSocket, as small as it can be.
type wsClient struct {
	c  net.Conn
	br *bufio.Reader
}

func dialWS(t *testing.T, r *rig, path, origin string, withCookie bool) (*wsClient, int) {
	t.Helper()
	u, _ := url.Parse(r.ts.URL)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 16)
	rand.Read(key)
	var b strings.Builder
	b.WriteString("GET " + path + " HTTP/1.1\r\nHost: " + u.Host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(key) + "\r\n")
	if origin != "" {
		b.WriteString("Origin: " + origin + "\r\n")
	}
	if withCookie {
		for _, ck := range r.c.Jar.Cookies(u) {
			b.WriteString("Cookie: " + ck.Name + "=" + ck.Value + "\r\n")
		}
	}
	b.WriteString("\r\n")
	c.Write([]byte(b.String()))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &wsClient{c: c, br: br}, resp.StatusCode
}

func (w *wsClient) send(op byte, payload []byte, mask bool) {
	hdr := []byte{0x80 | op}
	m := byte(0)
	if mask {
		m = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, m|byte(n))
	default:
		hdr = append(hdr, m|126, byte(n>>8), byte(n))
	}
	body := append([]byte(nil), payload...)
	if mask {
		key := []byte{1, 2, 3, 4}
		hdr = append(hdr, key...)
		for i := range body {
			body[i] ^= key[i%4]
		}
	}
	w.c.Write(append(hdr, body...))
}

func (w *wsClient) text(s string) { w.send(1, []byte(s), true) }

// next reads one message from the server: its kind and contents.
func (w *wsClient) next(t *testing.T) (byte, []byte, error) {
	w.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var h [2]byte
	if _, err := io.ReadFull(w.br, h[:]); err != nil {
		return 0, nil, err
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		io.ReadFull(w.br, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		io.ReadFull(w.br, b[:])
		n = int(binary.BigEndian.Uint64(b[:]))
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(w.br, p); err != nil {
		return 0, nil, err
	}
	return h[0] & 0x0f, p, nil
}

func liveRig(t *testing.T) (*rig, *fakeLive, string) {
	r := newRig(t)
	r.login()
	f := &fakeLive{}
	f.url = "https://shop.test/"
	f.fields = []browser.SignInField{{Kind: "username", Label: "Email address"}}
	r.srv.liveFor = func(id string) liveTab {
		if id == "t1" {
			return f
		}
		return nil
	}
	u, _ := url.Parse(r.ts.URL)
	return r, f, "http://" + u.Host
}

func TestTheLiveViewChecksWhoIsAskingAndWhatForBeforeOpening(t *testing.T) {
	r, _, origin := liveRig(t)
	for name, c := range map[string]struct {
		path, origin string
		cookie       bool
		want         int
	}{
		"not signed in":      {"/api/browser/live?id=t1", origin, false, 401},
		"another site":       {"/api/browser/live?id=t1", "https://evil.example", true, 403},
		"no origin at all":   {"/api/browser/live?id=t1", "", true, 403},
		"a tab that is gone": {"/api/browser/live?id=nope", origin, true, 404},
	} {
		if _, code := dialWS(t, r, c.path, c.origin, c.cookie); code != c.want {
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
	}
}

func TestPicturesArePushedAndInputArrivesInOrder(t *testing.T) {
	r, f, origin := liveRig(t)
	ws, code := dialWS(t, r, "/api/browser/live?id=t1", origin, true)
	if code != 101 {
		t.Fatalf("%d", code)
	}
	if op, p, err := ws.next(t); err != nil || op != 1 || !strings.Contains(string(p), `"ready"`) || !strings.Contains(string(p), `"w":1280`) {
		t.Fatalf("%d %q %v", op, p, err)
	}
	if op, p, err := ws.next(t); err != nil || op != 2 || string(p) != "\xff\xd8FIRST" {
		t.Fatalf("the page as it is now comes first: %d %q %v", op, p, err)
	}
	// A picture the browser pushes is sent on, and only then is the browser told it may send another.
	acked := make(chan struct{}, 1)
	f.mu.Lock()
	push := f.push
	f.mu.Unlock()
	push([]byte("\xff\xd8NEXT"), func() { acked <- struct{}{} })
	if op, p, err := ws.next(t); err != nil || op != 2 || string(p) != "\xff\xd8NEXT" {
		t.Fatalf("%d %q %v", op, p, err)
	}
	select {
	case <-acked:
	case <-time.After(2 * time.Second):
		t.Fatal("the browser was never told the picture was sent")
	}

	for _, m := range []string{
		`{"t":"mouse","k":"down","x":10,"y":20,"b":"left","n":1}`,
		`{"t":"mouse","k":"move","x":12,"y":22}`,
		`{"t":"mouse","k":"up","x":12,"y":22,"b":"left","n":1}`,
		`{"t":"text","text":"h"}`, `{"t":"text","text":"i"}`,
		`{"t":"key","key":"Enter"}`,
		`{"t":"wheel","x":5,"y":5,"dy":120}`,
		`{"t":"replace","text":"hi there"}`,
		`not json`,
	} {
		ws.text(m)
	}
	// The page is told what has focus and where it is, a moment after a tap or a key.
	var sawFocus bool
	for i := 0; i < 3 && !sawFocus; i++ {
		if op, p, err := ws.next(t); err == nil && op == 1 && strings.Contains(string(p), `"focus"`) {
			sawFocus = true
			if !strings.Contains(string(p), `"signin":[{"kind":"username","label":"Email address"}]`) {
				t.Fatalf("the page is told what it is asking for to sign in: %s", p)
			}
		}
	}
	if !sawFocus {
		t.Fatal("no word of what has focus")
	}
	want := []string{"mouse down left", "mouse move ", "mouse up left", "type h", "type i", "key Enter", "wheel", "replace hi there"}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(f.got()) < len(want) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := f.got(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestAnUnmaskedFrameEndsTheConnectionAndStopsTheView(t *testing.T) {
	r, f, origin := liveRig(t)
	ws, _ := dialWS(t, r, "/api/browser/live?id=t1", origin, true)
	ws.next(t)
	ws.send(1, []byte(`{"t":"text","text":"x"}`), false) // a page always masks; anything else is not the page
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, err := ws.next(t); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the connection stayed open")
		}
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := f.stops
		f.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the live view kept running after its connection ended")
}

func TestANewerViewOfATabTakesOverFromTheOlder(t *testing.T) {
	r, _, origin := liveRig(t)
	first, _ := dialWS(t, r, "/api/browser/live?id=t1", origin, true)
	first.next(t)
	second, code := dialWS(t, r, "/api/browser/live?id=t1", origin, true)
	if code != 101 {
		t.Fatalf("%d", code)
	}
	second.next(t)
	var taken bool
	for i := 0; i < 5 && !taken; i++ {
		_, p, err := first.next(t)
		if err != nil {
			break
		}
		taken = strings.Contains(string(p), `"taken"`)
	}
	if !taken {
		t.Fatal("the older view was not told another screen took over")
	}
}

func TestWhatIsDoneInTheLiveViewIsRecordedLikeAnythingElseAndPrivateTextIsNot(t *testing.T) {
	r, f, origin := liveRig(t)
	r.srv.Rec = &routine.Recorder{}
	r.srv.Rec.Start()
	f.focus = browser.Described{Tag: "input", Type: "password", Label: "Password"}
	f.kind = "password"
	ws, _ := dialWS(t, r, "/api/browser/live?id=t1", origin, true)
	ws.next(t)
	ws.text(`{"t":"text","text":"hunter2"}`)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(r.srv.Rec.Steps()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	steps := r.srv.Rec.Steps()
	if len(steps) != 1 || !steps[0].Secret || strings.Contains(steps[0].Line(), "hunter2") {
		t.Fatalf("%+v", steps)
	}
}

func TestTheContentPolicyAllowsOnlyThisHostsWebSocket(t *testing.T) {
	r := newRig(t)
	resp, _ := r.do("GET", "/", nil)
	csp := resp.Header.Get("Content-Security-Policy")
	u, _ := url.Parse(r.ts.URL)
	if !strings.Contains(csp, "connect-src 'self' ws://"+u.Host+" wss://"+u.Host+";") || strings.Contains(csp, " ws: ") || strings.Contains(csp, " wss: ") || strings.Contains(csp, " *") {
		t.Fatalf("%s", csp)
	}
	if got := wsSources(&http.Request{Host: "evil.com; script-src *"}); got != "" {
		t.Fatalf("a host that is not a plain name and port must add nothing: %q", got)
	}
}

// The page opens the live connection first and keeps the older stream only as a fallback, and nothing it draws from the
// browser's pictures is written as markup.
func TestThePageTriesTheLiveConnectionFirstAndSendsInputOnIt(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("static", "more.js"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"drawSignIn(", "fields: values", "/api/browser/live?id=", "new WebSocket(", "startStream()", "emit({ t: 'text'", "emit({ t: 'mouse'", "emit({ t: 'wheel'", "wsBroken"} {
		if !strings.Contains(src, want) {
			t.Errorf("more.js lacks %s", want)
		}
	}
	if strings.Contains(src, "setTimeout(flush, 60)") && !strings.Contains(src, "if (wsOpen()) flush(); else") {
		t.Error("typing waits to be sent even when the connection is open")
	}
}
