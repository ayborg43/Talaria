package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Timing, variables so tests can shorten them.
var (
	loadTimeout   = 30 * time.Second
	settleStart   = 300 * time.Millisecond // how long after an action to watch for a page load beginning
	settleTimeout = 15 * time.Second
	afterLoad     = 200 * time.Millisecond // for scripts that run once the page has loaded
)

// ErrStale means an element number does not refer to anything now.
var ErrStale = errors.New("that element number is out of date: take a new snapshot (the page changed), then use its numbers")

// Element is one interactive thing on the page, as the last snapshot saw it.
type Element struct {
	Ref         int      `json:"ref"`
	Tag         string   `json:"tag"`
	Type        string   `json:"type"`
	Role        string   `json:"role"`
	Label       string   `json:"label"`
	Disabled    bool     `json:"disabled"`
	Href        string   `json:"href"`
	Placeholder string   `json:"placeholder"`
	Value       string   `json:"value"`
	Checked     *bool    `json:"checked"`
	Options     []string `json:"options"`
}

// Snapshot is what the page looks like to the agent.
type Snapshot struct {
	URL        string    `json:"url"`
	Title      string    `json:"title"`
	Elements   []Element `json:"elements"`
	More       int       `json:"more"`
	Text       string    `json:"text"`
	TextLength int       `json:"textLength"`
	ScrollY    int       `json:"scrollY"`
	ScrollMax  int       `json:"scrollMax"`
}

// Page is one browser tab, used by one conversation.
type Page struct {
	c         *client
	targetID  string
	sessionID string
	stop      func()

	mu       sync.Mutex // one action at a time
	refs     map[int]Element
	lastURL  string
	lastUsed atomic.Int64 // unix nanoseconds

	stateMu sync.Mutex
	notes   []string // things that happened that the next result should mention
	loading bool
	starts  int // main-frame loads begun, counted so a wait can tell one began
}

func newPage(ctx context.Context, c *client, userAgent string) (*Page, error) {
	raw, err := c.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, "")
	if err != nil {
		return nil, err
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.TargetID == "" {
		return nil, errors.New("browser: could not open a tab")
	}
	raw, err = c.call(ctx, "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, "")
	if err != nil {
		return nil, err
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &att); err != nil || att.SessionID == "" {
		return nil, errors.New("browser: could not attach to the tab")
	}

	p := &Page{c: c, targetID: created.TargetID, sessionID: att.SessionID, refs: map[int]Element{}}
	p.stop = c.listen(att.SessionID, p.onEvent)

	setup := []struct {
		method string
		params any
	}{
		{"Page.enable", nil},
		{"Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 800, "deviceScaleFactor": 1, "mobile": false}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": prepareJS}},
	}
	if userAgent != "" {
		setup = append(setup, struct {
			method string
			params any
		}{"Emulation.setUserAgentOverride", map[string]any{"userAgent": userAgent}})
	}
	for _, s := range setup {
		if _, err := c.call(ctx, s.method, s.params, att.SessionID); err != nil {
			p.close()
			return nil, err
		}
	}
	p.touch()
	return p, nil
}

func (p *Page) touch() { p.lastUsed.Store(time.Now().UnixNano()) }

// idleAt says how long the page had been unused at a given time.
func (p *Page) idleAt(now time.Time) time.Duration { return now.Sub(time.Unix(0, p.lastUsed.Load())) }

func (p *Page) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return p.c.call(ctx, method, params, p.sessionID)
}

// onEvent follows the tab's events: loads, and dialogs that would otherwise
// block it. It runs in the browser connection's reader, so it only records and
// starts work; it never waits on the browser itself.
func (p *Page) onEvent(ev event) {
	switch ev.Method {
	case "Page.frameStartedLoading":
		p.stateMu.Lock()
		p.loading = true
		p.starts++
		p.stateMu.Unlock()
	case "Page.frameStoppedLoading", "Page.loadEventFired":
		p.stateMu.Lock()
		p.loading = false
		p.stateMu.Unlock()
	case "Page.javascriptDialogOpening":
		var d struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		json.Unmarshal(ev.Params, &d)
		// An alert is only information, so it is accepted. A question (confirm,
		// prompt, leaving the page) is refused: the agent did not choose to answer it.
		accept := d.Type == "alert"
		// Detached on purpose: events carry no request context, and the call
		// fails fast if the connection is already dead.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			p.call(ctx, "Page.handleJavaScriptDialog", map[string]any{"accept": accept})
		}()
		verb := "dismissed"
		if accept {
			verb = "accepted"
		}
		p.note(fmt.Sprintf("A %s dialog appeared saying %q; it was %s.", d.Type, clip(d.Message, 200), verb))
	}
}

func (p *Page) note(s string) {
	p.stateMu.Lock()
	p.notes = append(p.notes, s)
	p.stateMu.Unlock()
}

// takeNotes returns, and forgets, what happened since the last result.
func (p *Page) takeNotes() []string {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	n := p.notes
	p.notes = nil
	return n
}

func (p *Page) loadState() (loading bool, starts int) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.loading, p.starts
}

// settle waits for whatever an action set off to finish: if a page load begins
// shortly after it, until that load is done.
func (p *Page) settle(ctx context.Context, startedBefore int) {
	if !sleep(ctx, settleStart) {
		return
	}
	deadline := time.Now().Add(settleTimeout)
	for time.Now().Before(deadline) {
		loading, starts := p.loadState()
		if !loading && starts == startedBefore {
			return // nothing began
		}
		if !loading {
			sleep(ctx, afterLoad)
			return
		}
		if !sleep(ctx, 50*time.Millisecond) {
			return
		}
	}
	p.note("The page was still loading after " + settleTimeout.String() + "; what follows may be incomplete.")
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// eval runs an expression in the page and decodes its result into out.
func (p *Page) eval(ctx context.Context, expr string, out any) error {
	raw, err := p.call(ctx, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return err
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.Exception != nil {
		msg := r.Exception.Exception.Description
		if msg == "" {
			msg = r.Exception.Text
		}
		return fmt.Errorf("the page raised an error: %s", clip(msg, 300))
	}
	if out == nil || len(r.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(r.Result.Value, out)
}

// Navigate goes to url and waits for the page to load.
func (p *Page) Navigate(ctx context.Context, url string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	raw, err := p.call(ctx, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return err
	}
	var r struct {
		ErrorText string `json:"errorText"`
	}
	json.Unmarshal(raw, &r)
	if r.ErrorText != "" {
		return fmt.Errorf("could not load %s: %s", url, strings.TrimPrefix(r.ErrorText, "net::"))
	}
	p.settleLoad(ctx)
	return nil
}

// settleLoad waits for a navigation that has been started to finish.
func (p *Page) settleLoad(ctx context.Context) {
	deadline := time.Now().Add(loadTimeout)
	for {
		loading, _ := p.loadState()
		if !loading {
			sleep(ctx, afterLoad)
			return
		}
		if time.Now().After(deadline) {
			p.note("The page was still loading after " + loadTimeout.String() + "; what follows may be incomplete.")
			return
		}
		if !sleep(ctx, 50*time.Millisecond) {
			return
		}
	}
}

// Snapshot reads the page. The element numbers it assigns are good until the next one.
func (p *Page) Snapshot(ctx context.Context) (*Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshot(ctx)
}

func (p *Page) snapshot(ctx context.Context) (*Snapshot, error) {
	p.touch()
	var s Snapshot
	if err := p.eval(ctx, snapshotJS, &s); err != nil {
		return nil, err
	}
	p.refs = make(map[int]Element, len(s.Elements))
	for _, e := range s.Elements {
		p.refs[e.Ref] = e
	}
	p.lastURL = s.URL
	return &s, nil
}

// Element returns what the last snapshot said about an element.
func (p *Page) Element(ref int) (Element, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.refs[ref]
	return e, p.lastURL, ok
}

type location struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Covered bool    `json:"covered"`
	Hit     string  `json:"hit"`
	Tag     string  `json:"tag"`
}

func (p *Page) locate(ctx context.Context, ref int) (*location, error) {
	var loc *location
	if err := p.eval(ctx, fmt.Sprintf("%s(%d)", locateJS, ref), &loc); err != nil {
		return nil, err
	}
	if loc == nil {
		return nil, ErrStale
	}
	return loc, nil
}

// Click clicks an element with the mouse, as a person would.
func (p *Page) Click(ctx context.Context, ref int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	loc, err := p.locate(ctx, ref)
	if err != nil {
		return err
	}
	if loc.Covered {
		// The user approved a click on this element. Something else is drawn over it, so a click would
		// land on that instead: a transparent frame over a harmless-looking button is how a page gets
		// a click it was not given. Not clicking is the only safe answer.
		return fmt.Errorf("something else is on top of that element (%s), so a click would land on that instead of what you approved. Nothing was clicked. Take a snapshot to see what is in the way (a pop-up or banner?)", loc.Hit)
	}
	_, before := p.loadState()
	for _, ev := range []map[string]any{
		{"type": "mouseMoved", "x": loc.X, "y": loc.Y},
		{"type": "mousePressed", "x": loc.X, "y": loc.Y, "button": "left", "clickCount": 1},
		{"type": "mouseReleased", "x": loc.X, "y": loc.Y, "button": "left", "clickCount": 1},
	} {
		if _, err := p.call(ctx, "Input.dispatchMouseEvent", ev); err != nil {
			return err
		}
	}
	p.settle(ctx, before)
	return nil
}

// Type puts text into an element: a field, a text area, or editable content.
func (p *Page) Type(ctx context.Context, ref int, text string, clear, submit bool) error {
	return p.TypeInto(ctx, ref, text, clear, submit, "")
}

// TypeInto is Type, but refuses unless the element is an input of type mustBe
// (when mustBe is not empty). The check is made on the element as it is when
// the text goes in, not as an earlier listing described it: a page can change
// a field's type between the two.
func (p *Page) TypeInto(ctx context.Context, ref int, text string, clear, submit bool, mustBe string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	var f *struct {
		Typable bool   `json:"typable"`
		Tag     string `json:"tag"`
		Type    string `json:"type"`
		Focused bool   `json:"focused"`
	}
	if err := p.eval(ctx, fmt.Sprintf("%s(%d, %t)", focusJS, ref, clear), &f); err != nil {
		return err
	}
	switch {
	case f == nil:
		return ErrStale
	case !f.Typable:
		return fmt.Errorf("that element (a %s) cannot be typed into", f.Tag)
	case mustBe != "" && (f.Tag != "input" || f.Type != mustBe):
		return fmt.Errorf("that field is not a %s field now, so nothing was typed", mustBe)
	}
	switch {
	case text != "":
		if _, err := p.call(ctx, "Input.insertText", map[string]any{"text": text}); err != nil {
			return err
		}
	case clear:
		if err := p.key(ctx, "Backspace"); err != nil {
			return err
		}
	}
	if submit {
		_, before := p.loadState()
		if err := p.key(ctx, "Enter"); err != nil {
			return err
		}
		p.settle(ctx, before)
	}
	return nil
}

type keyDef struct {
	key, code string
	vk        int
	text      string
}

var keys = map[string]keyDef{
	"Enter":      {"Enter", "Enter", 13, "\r"},
	"Tab":        {"Tab", "Tab", 9, ""},
	"Escape":     {"Escape", "Escape", 27, ""},
	"Backspace":  {"Backspace", "Backspace", 8, ""},
	"Delete":     {"Delete", "Delete", 46, ""},
	"ArrowUp":    {"ArrowUp", "ArrowUp", 38, ""},
	"ArrowDown":  {"ArrowDown", "ArrowDown", 40, ""},
	"ArrowLeft":  {"ArrowLeft", "ArrowLeft", 37, ""},
	"ArrowRight": {"ArrowRight", "ArrowRight", 39, ""},
	"PageUp":     {"PageUp", "PageUp", 33, ""},
	"PageDown":   {"PageDown", "PageDown", 34, ""},
	"Home":       {"Home", "Home", 36, ""},
	"End":        {"End", "End", 35, ""},
	"Space":      {" ", "Space", 32, " "},
}

// KeyNames lists the keys Press understands.
func KeyNames() []string {
	return []string{"Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "PageUp", "PageDown", "Home", "End", "Space"}
}

func (p *Page) key(ctx context.Context, name string) error {
	k, ok := keys[name]
	if !ok {
		return fmt.Errorf("unknown key %q (use one of: %s)", name, strings.Join(KeyNames(), ", "))
	}
	down := map[string]any{"type": "keyDown", "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "nativeVirtualKeyCode": k.vk}
	if k.text != "" {
		down["type"], down["text"] = "keyDown", k.text
	}
	up := map[string]any{"type": "keyUp", "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "nativeVirtualKeyCode": k.vk}
	for _, ev := range []map[string]any{down, up} {
		if _, err := p.call(ctx, "Input.dispatchKeyEvent", ev); err != nil {
			return err
		}
	}
	return nil
}

// Press presses a key, such as Enter or Escape, on whatever has focus.
func (p *Page) Press(ctx context.Context, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	_, before := p.loadState()
	if err := p.key(ctx, name); err != nil {
		return err
	}
	p.settle(ctx, before)
	return nil
}

// Select chooses an option in a drop-down list, by its value or its text.
func (p *Page) Select(ctx context.Context, ref int, want string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	var r *struct {
		Error   string   `json:"error"`
		Options []string `json:"options"`
		Chosen  string   `json:"chosen"`
	}
	if err := p.eval(ctx, fmt.Sprintf("%s(%d, %s)", selectJS, ref, jsString(want)), &r); err != nil {
		return "", err
	}
	switch {
	case r == nil:
		return "", ErrStale
	case r.Error != "" && len(r.Options) > 0:
		return "", fmt.Errorf("%s; the options are: %s", r.Error, strings.Join(r.Options, " | "))
	case r.Error != "":
		return "", errors.New(r.Error)
	}
	_, before := p.loadState()
	p.settle(ctx, before)
	return r.Chosen, nil
}

// Scroll moves the page up or down by most of a screen, or to its top or bottom.
func (p *Page) Scroll(ctx context.Context, direction string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	var js string
	switch direction {
	case "down":
		js = "window.scrollBy(0, innerHeight * 0.8)"
	case "up":
		js = "window.scrollBy(0, -innerHeight * 0.8)"
	case "top":
		js = "window.scrollTo(0, 0)"
	case "bottom":
		js = "window.scrollTo(0, document.documentElement.scrollHeight)"
	default:
		return fmt.Errorf("scroll up, down, top or bottom, not %q", direction)
	}
	return p.eval(ctx, js, nil)
}

// Back goes to the previous page in the tab's history.
func (p *Page) Back(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	raw, err := p.call(ctx, "Page.getNavigationHistory", nil)
	if err != nil {
		return err
	}
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return err
	}
	if h.CurrentIndex <= 0 {
		return errors.New("there is no earlier page to go back to")
	}
	_, before := p.loadState()
	if _, err := p.call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[h.CurrentIndex-1].ID}); err != nil {
		return err
	}
	p.settle(ctx, before)
	return nil
}

// WaitForText waits until the page shows text, or for the time to pass, and
// says whether the text appeared.
func (p *Page) WaitForText(ctx context.Context, text string, timeout time.Duration) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	deadline := time.Now().Add(timeout)
	for {
		var found bool
		if err := p.eval(ctx, fmt.Sprintf("(document.body ? document.body.innerText : '').includes(%s)", jsString(text)), &found); err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
		if time.Now().After(deadline) || !sleep(ctx, 250*time.Millisecond) {
			return false, nil
		}
	}
}

// Screenshot returns a JPEG of what is in the window.
func (p *Page) Screenshot(ctx context.Context) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	raw, err := p.call(ctx, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 60})
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

// URL returns the address the tab is on now.
func (p *Page) URL(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var u string
	err := p.eval(ctx, "location.href", &u)
	return u, err
}

// Reset sends the tab to a blank page.
func (p *Page) Reset(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.call(ctx, "Page.navigate", map[string]any{"url": "about:blank"})
	p.refs = map[int]Element{}
}

// close closes the tab. Cleanup runs detached: it must go out even when the
// request that owned the tab is already done.
func (p *Page) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.c.call(ctx, "Target.closeTarget", map[string]any{"targetId": p.targetID}, "")
	p.stop()
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
