package browser

import (
	"context"
	"strings"
	"testing"
	"time"
)

func openShop(t *testing.T) (*rig, *Page) {
	r := newRig(t, Options{})
	if _, err := r.call("browser_open", `{"url":"http://shop.test/"}`); err != nil {
		t.Fatal(err)
	}
	return r, r.m.Peek(r.owner)
}

func TestALiveViewIsPushedAndHeldBackUntilEachPictureIsDealtWith(t *testing.T) {
	r, p := openShop(t)
	got := make(chan []byte, 4)
	acks := make(chan func(), 4)
	stop, err := p.Screencast(context.Background(), 60, 1280, func(img []byte, done func()) { got <- img; acks <- done })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case img := <-got:
		if !strings.HasPrefix(string(img), "\xff\xd8") {
			t.Fatalf("%q", img)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no picture was pushed")
	}
	(<-acks)()
	deadline := time.Now().Add(2 * time.Second)
	for len(r.sent("Page.screencastFrameAck")) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if acked := r.sent("Page.screencastFrameAck"); len(acked) != 1 || acked[0].Params["sessionId"] != float64(7) {
		t.Fatalf("the browser is told which picture was dealt with: %+v", acked)
	}
	stop()
	stop() // twice is harmless
	if len(r.sent("Page.stopScreencast")) != 1 {
		t.Fatalf("%+v", r.sent("Page.stopScreencast"))
	}
}

func TestAReplacedLiveViewDoesNotSwitchOffTheOneThatReplacedIt(t *testing.T) {
	r, p := openShop(t)
	frames := make(chan struct{}, 8)
	stop1, err := p.Screencast(context.Background(), 60, 1280, func([]byte, func()) {})
	if err != nil {
		t.Fatal(err)
	}
	stop2, err := p.Screencast(context.Background(), 60, 1280, func([]byte, func()) { frames <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	stop1() // late: the first was already replaced
	before := len(r.sent("Page.stopScreencast"))
	select {
	case <-frames:
	case <-time.After(2 * time.Second):
		t.Fatal("the second view got no picture")
	}
	stop2()
	if n := len(r.sent("Page.stopScreencast")); n != before+1 {
		t.Fatalf("stop calls: before %d, after %d", before, n)
	}
}

func TestInputGoesStraightToTheBrowserWithNoLockAndInOrder(t *testing.T) {
	r, p := openShop(t)
	ctx := context.Background()
	p.mu.Lock() // the agent, or a picture being taken, is holding the tab: a person's typing must not wait for it
	done := make(chan error, 1)
	go func() {
		for _, text := range []string{"h", "i", " ", "é"} {
			if err := p.TypeNow(ctx, text); err != nil {
				done <- err
				return
			}
		}
		if err := p.KeyNow(ctx, "Enter", 0); err != nil {
			done <- err
			return
		}
		if err := p.Pointer(ctx, "down", 100, 100, Left, 1); err != nil {
			done <- err
			return
		}
		done <- p.Pointer(ctx, "up", 100, 100, Left, 1)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		p.mu.Unlock()
		t.Fatal("input waited for the tab's lock")
	}
	p.mu.Unlock()
	var sent []string
	for _, c := range r.commands() {
		switch c.Method {
		case "Input.dispatchKeyEvent":
			sent = append(sent, "key:"+c.Params["type"].(string)+":"+asString(c.Params["key"]))
		case "Input.insertText":
			sent = append(sent, "insert:"+asString(c.Params["text"]))
		case "Input.dispatchMouseEvent":
			sent = append(sent, "mouse:"+asString(c.Params["type"]))
		}
	}
	got := strings.Join(sent, " ")
	want := "key:keyDown:h key:keyUp:h key:keyDown:i key:keyUp:i key:keyDown:  key:keyUp:  insert:é key:keyDown:Enter key:keyUp:Enter mouse:mousePressed mouse:mouseReleased"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if err := p.Pointer(ctx, "down", -5, 10, Left, 1); err == nil {
		t.Fatal("a point outside the page")
	}
	if err := p.TypeNow(ctx, strings.Repeat("x", 5000)); err == nil {
		t.Fatal("too much at once")
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func TestTheUserTouchingTheTabIsNotedForTheAgent(t *testing.T) {
	r, p := openShop(t)
	if err := p.TypeNow(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	out, err := r.call("browser_snapshot", `{}`)
	if err != nil || !strings.Contains(out, "took control") {
		t.Fatalf("%q %v", out, err)
	}
}
