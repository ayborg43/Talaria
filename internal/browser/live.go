package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"
)

// This file is what makes working a tab by hand feel immediate. The console's other calls (ClickAt, PressKey, Shot) are
// built for taking one careful step at a time: each takes the tab's lock, and a click waits for the page to settle. That
// is right for the agent and wrong for a person typing, who must not wait behind a picture being taken or a page still
// loading. Here the browser pushes pictures when the page changes (a screencast), and input is handed to the browser
// as it arrives, in order, with no waiting for the page.

// ErrNoFrames is what Screencast says when the browser will not give pictures.
var ErrNoFrames = errors.New("the browser did not start a live view")

// Screencast starts pushing pictures of the tab and calls fn with each one: a JPEG, and a function that says it has
// been dealt with, which lets the browser send the next. A picture that is not acknowledged holds the browser back, so a
// slow viewer gets fewer, newer pictures rather than a growing queue. fn runs in the browser connection's reader, so it
// must only hand the picture on. Only one screencast of a tab runs at a time: starting another ends the first.
func (p *Page) Screencast(ctx context.Context, quality, maxWidth int, fn func(jpeg []byte, done func())) (stop func(), err error) {
	p.castMu.Lock()
	defer p.castMu.Unlock()
	p.stopCastLocked()
	stopListening := p.c.listen(p.sessionID, func(ev event) {
		if ev.Method != "Page.screencastFrame" {
			return
		}
		var f struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
		}
		if json.Unmarshal(ev.Params, &f) != nil {
			return
		}
		img, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return
		}
		var once sync.Once
		fn(img, func() {
			once.Do(func() {
				go p.call(context.Background(), "Page.screencastFrameAck", map[string]any{"sessionId": f.SessionID})
			})
		})
	})
	maxWidth = min(max(maxWidth, 320), Width) // a smaller picture is cheaper to send; the page's own size is the most there is
	if _, err := p.call(ctx, "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": min(max(quality, 1), 100), "maxWidth": maxWidth, "maxHeight": maxWidth * Height / Width, "everyNthFrame": 1,
	}); err != nil {
		stopListening()
		return nil, err
	}
	p.castGen++
	gen := p.castGen
	var stopOnce sync.Once
	stop = func() {
		stopOnce.Do(func() {
			p.castMu.Lock()
			defer p.castMu.Unlock()
			stopListening()
			if p.castGen == gen { // a view that was replaced must not switch off the one that replaced it
				p.call(context.Background(), "Page.stopScreencast", nil)
				p.castStop = nil
			}
		})
	}
	p.castStop = stop
	return stop, nil
}

// stopCastLocked ends a screencast already running; the caller holds castMu.
func (p *Page) stopCastLocked() {
	if p.castStop != nil {
		old := p.castStop
		p.castStop = nil
		p.castMu.Unlock()
		old()
		p.castMu.Lock()
	}
}

// Snap takes one picture without waiting for anything else the tab is doing: the first sight of the page for a live
// view, before the browser has had a reason to push one.
func (p *Page) Snap(ctx context.Context, quality int) ([]byte, error) {
	raw, err := p.call(ctx, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": min(max(quality, 1), 100)})
	if err != nil {
		return nil, err
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.Data)
}

// Mouse buttons for Pointer.
const (
	Left   = "left"
	Right  = "right"
	Middle = "middle"
)

// Pointer sends one mouse event to the page now: kind is "move", "down" or "up", clicks is 2 for the second press of a
// double click. It takes no lock and waits for nothing, so a drag or a click is as quick as the browser can take it.
func (p *Page) Pointer(ctx context.Context, kind string, x, y float64, button string, clicks int) error {
	if x < 0 || y < 0 || x > Width || y > Height {
		return errors.New("that point is outside the page")
	}
	typ := map[string]string{"move": "mouseMoved", "down": "mousePressed", "up": "mouseReleased"}[kind]
	if typ == "" {
		return errors.New("unknown pointer event")
	}
	if button != Left && button != Right && button != Middle {
		button = Left
	}
	p.touch()
	p.took()
	ev := map[string]any{"type": typ, "x": x, "y": y}
	if kind != "move" {
		ev["button"], ev["clickCount"] = button, max(clicks, 1)
	}
	if kind == "move" {
		ev["button"] = "none"
	}
	_, err := p.call(ctx, "Input.dispatchMouseEvent", ev)
	return err
}

// WheelNow scrolls at a point, at once and by any amount the person's finger or wheel asked for (within sense).
func (p *Page) WheelNow(ctx context.Context, x, y, dx, dy float64) error {
	p.touch()
	_, err := p.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": min(max(x, 0), Width), "y": min(max(y, 0), Height),
		"deltaX": min(max(dx, -2000), 2000), "deltaY": min(max(dy, -2000), 2000)})
	return err
}

// KeyNow presses a named key (Enter, Backspace, an arrow) now. With ctrl or meta held it is a shortcut (select all,
// copy, paste), which the page is given as a key press with that modifier.
func (p *Page) KeyNow(ctx context.Context, name string, modifiers int) error {
	k, ok := keys[name]
	if !ok {
		return errors.New("unknown key")
	}
	p.touch()
	p.took()
	for _, typ := range []string{"keyDown", "keyUp"} {
		ev := map[string]any{"type": typ, "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "nativeVirtualKeyCode": k.vk, "modifiers": modifiers}
		if typ == "keyDown" && k.text != "" && modifiers == 0 {
			ev["text"] = k.text
		}
		if _, err := p.call(ctx, "Input.dispatchKeyEvent", ev); err != nil {
			return err
		}
	}
	return nil
}

// TypeNow types text into whatever has focus, now. One ordinary character goes in as a key press, which is what pages
// that watch the keyboard expect; anything else (a pasted run, an accent, an emoji, a word the phone's keyboard
// completed) is inserted in one go. It is the person's own typing, so it is never logged or shown to the model.
func (p *Page) TypeNow(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	if len(text) > 4000 {
		return errors.New("that is too much to type at once")
	}
	p.touch()
	p.took()
	if r, size := utf8.DecodeRuneInString(text); size == len(text) && r >= ' ' && r < 0x7f {
		s := string(r)
		upper := strings.ToUpper(s)
		vk := int(upper[0])
		for _, typ := range []string{"keyDown", "keyUp"} {
			ev := map[string]any{"type": typ, "key": s, "windowsVirtualKeyCode": vk, "nativeVirtualKeyCode": vk}
			if typ == "keyDown" {
				ev["text"], ev["unmodifiedText"] = s, s
			}
			if _, err := p.call(ctx, "Input.dispatchKeyEvent", ev); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := p.call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}

// ReplaceNow replaces what is in the focused field with text, at once.
func (p *Page) ReplaceNow(ctx context.Context, text string) error {
	if len(text) > 4000 {
		return errors.New("that is too much to type at once")
	}
	p.touch()
	p.took()
	var selected bool
	if err := p.eval(ctx, selectFocusedJS, &selected); err != nil {
		return err
	}
	if text == "" {
		return p.key(ctx, "Backspace")
	}
	_, err := p.call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}
