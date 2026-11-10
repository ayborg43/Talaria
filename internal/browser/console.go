package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The console is the browser as the user drives it: the same tab the agent has
// open, seen as a picture and worked by pointing at it. It adds nothing that
// stays running: a picture is taken when one is asked for, and the tab idles
// out as any other once nobody asks.

// Size is the window every tab is given, which is the size of a picture of it.
const (
	Width  = 1280
	Height = 800
)

// Owners lists who has a tab open, in a stable order.
func (m *Manager) Owners() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pages))
	for o := range m.pages {
		out = append(out, o)
	}
	slices.Sort(out)
	return out
}

// Shot is a JPEG of the tab at the given quality (1 to 100).
func (p *Page) Shot(ctx context.Context, quality int) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
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

// took records that the user worked the tab by hand, so the agent's next
// result says so: the page it remembers may not be the page that is there.
func (p *Page) took() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	const msg = "The user took control of this tab and may have changed the page: element numbers from before are stale, so read the page again."
	if !slices.Contains(p.notes, msg) {
		p.notes = append(p.notes, msg)
	}
}

// ClickAt clicks a point of the window, as the user pointing at the picture.
func (p *Page) ClickAt(ctx context.Context, x, y float64) error {
	if x < 0 || y < 0 || x > Width || y > Height {
		return errors.New("that point is outside the page")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	p.took()
	_, before := p.loadState()
	for _, ev := range []map[string]any{
		{"type": "mouseMoved", "x": x, "y": y},
		{"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1},
		{"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1},
	} {
		if _, err := p.call(ctx, "Input.dispatchMouseEvent", ev); err != nil {
			return err
		}
	}
	p.settle(ctx, before)
	return nil
}

// InsertText types text into whatever has focus. It is the user's own typing,
// so it is never returned, logged or shown to the model.
func (p *Page) InsertText(ctx context.Context, text string) error {
	if len(text) > 4000 {
		return errors.New("that is too much to type at once")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	p.took()
	_, err := p.call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}

// PressKey presses one key on whatever has focus, for the user.
func (p *Page) PressKey(ctx context.Context, name string) error {
	p.took()
	return p.Press(ctx, name)
}

// Wheel scrolls the window by dy pixels at a point, as a mouse wheel would,
// so a scrolling panel under the pointer moves rather than the whole page.
func (p *Page) Wheel(ctx context.Context, x, y, dy float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	dy = min(max(dy, -2000), 2000)
	_, err := p.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": min(max(x, 0), Width), "y": min(max(y, 0), Height), "deltaX": 0, "deltaY": dy})
	return err
}

// Go sends the tab to an address the user typed, after the same check the
// agent's own navigation gets.
func (m *Manager) Go(ctx context.Context, p *Page, raw string) error {
	if !strings.Contains(raw, "://") {
		raw = "https://" + strings.TrimSpace(raw)
	}
	u, err := m.CheckURL(ctx, raw)
	if err != nil {
		return err
	}
	p.took()
	return p.Navigate(ctx, u)
}

// Where is the tab's address, made safe to show, for the console's bar.
func (p *Page) Where(ctx context.Context) string {
	u, err := p.URL(ctx)
	if err != nil {
		return ""
	}
	return clip(SafeURL(u), 300)
}

// GoBack goes back a page, for the user.
func (p *Page) GoBack(ctx context.Context) error {
	p.took()
	if err := p.Back(ctx); err != nil {
		return fmt.Errorf("%w", err)
	}
	return nil
}

// focusKindJS says what the page has focused: a password field, some other text field, or nothing that takes typing.
const focusKindJS = `(() => {
  const e = document.activeElement;
  if (!e) return 'none';
  const t = (e.tagName || '').toLowerCase();
  if (t === 'input') {
    const ty = (e.type || 'text').toLowerCase();
    if (['checkbox', 'radio', 'button', 'submit', 'reset', 'image', 'file', 'range', 'color'].includes(ty)) return 'none';
    return ty === 'password' ? 'password' : 'text';
  }
  return t === 'textarea' || e.isContentEditable ? 'text' : 'none';
})()`

// FocusKind says whether the page now has a password field, another text field, or nothing that takes typing in
// focus ("password", "text" or "none"), so the app can offer the right kind of box to type into. It asks only
// what kind of thing has focus, never what is in it.
func (p *Page) FocusKind(ctx context.Context) string {
	var kind string
	if err := p.eval(ctx, focusKindJS, &kind); err != nil {
		return "none"
	}
	switch kind {
	case "password", "text":
		return kind
	}
	return "none"
}

const selectFocusedJS = `(() => {
  const e = document.activeElement;
  if (e && typeof e.select === 'function') { e.select(); return true; }
  if (e && e.isContentEditable) { document.execCommand('selectAll'); return true; }
  return false;
})()`

// ReplaceText replaces what is in the focused field with text, for the user's own typing when a password manager
// fills a field in one go or they change a word in the middle. Empty text clears it. Like InsertText it is never
// returned, logged or shown to the model.
func (p *Page) ReplaceText(ctx context.Context, text string) error {
	if len(text) > 4000 {
		return errors.New("that is too much to type at once")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
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
