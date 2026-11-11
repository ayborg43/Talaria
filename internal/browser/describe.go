package browser

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// What follows lets the app say, in words, what the user clicked or typed into while they drive the agent's browser, so
// a demonstration can be written down as steps ("click the button Sign in") instead of pixel positions. It reads only
// what a person would read off the screen: a visible label, never what is in a field.

const describeAtJS = `((x, y) => {
  const el = document.elementFromPoint(x, y);
  if (!el) return null;
  const hit = el.closest('a,button,input,select,textarea,summary,label,[role=button],[role=link],[role=tab],[role=menuitem],[role=checkbox],[role=option]') || el;
  const tag = (hit.tagName || '').toLowerCase();
  const role = (hit.getAttribute('role') || '').toLowerCase();
  const ty = (hit.type || '').toLowerCase();
  const name = (hit.getAttribute('aria-label') || '').trim();
  let label = name;
  if (!label && (tag === 'input' || tag === 'textarea' || tag === 'select')) {
    const id = hit.id && document.querySelector('label[for="' + CSS.escape(hit.id) + '"]');
    label = ((id && id.innerText) || (hit.closest('label') && hit.closest('label').innerText) || hit.placeholder || hit.name || '').trim();
  }
  if (!label && ty !== 'password') label = (hit.innerText || hit.alt || hit.title || hit.value || '').toString().trim();
  if (!label) label = (hit.getAttribute('title') || '').trim();
  return { tag, role, type: ty, label: label.slice(0, 120), ac: (hit.autocomplete || '').toLowerCase(), name: (hit.name || '').toLowerCase() };
})`

const describeFocusJS = `(() => {
  const e = document.activeElement;
  if (!e) return null;
  const tag = (e.tagName || '').toLowerCase();
  const ty = (e.type || '').toLowerCase();
  let label = (e.getAttribute('aria-label') || '').trim();
  if (!label) {
    const id = e.id && document.querySelector('label[for="' + CSS.escape(e.id) + '"]');
    label = ((id && id.innerText) || (e.closest('label') && e.closest('label').innerText) || e.placeholder || e.name || '').trim();
  }
  return { tag, role: '', type: ty, label: label.slice(0, 120), ac: (e.autocomplete || '').toLowerCase(), name: (e.name || '').toLowerCase() };
})()`

// Described is what a click landed on, or what has focus, as a person would name it.
type Described struct {
	Tag, Role, Type, Label, AC, Name string
}

type describedJSON struct {
	Tag   string `json:"tag"`
	Role  string `json:"role"`
	Type  string `json:"type"`
	Label string `json:"label"`
	AC    string `json:"ac"`
	Name  string `json:"name"`
}

func (p *Page) described(ctx context.Context, expr string) (Described, bool) {
	var e *describedJSON
	if err := p.eval(ctx, expr, &e); err != nil || e == nil {
		return Described{}, false
	}
	return Described{Tag: e.Tag, Role: e.Role, Type: e.Type, Label: e.Label, AC: e.AC, Name: e.Name}, true
}

// DescribeAt says what is at a point on the page, before it is clicked.
func (p *Page) DescribeAt(ctx context.Context, x, y float64) (Described, bool) {
	return p.described(ctx, fmt.Sprintf("%s(%f, %f)", describeAtJS, x, y))
}

// DescribeFocus says what field has focus, before something is typed into it.
func (p *Page) DescribeFocus(ctx context.Context) (Described, bool) {
	return p.described(ctx, describeFocusJS)
}

var privateField = regexp.MustCompile(`(?i)(pass(word|code|wd)?|pin\b|cvv|cvc|card|ssn|social.?security|otp|one.?time|secret|token|2fa|verification|security.?code|iban|routing|account.?number)`)

// Private says whether what goes into this field is private: a password, a card, a code that proves who you are. A
// recording keeps only that such a field was filled, never the value.
func (e Described) Private() bool {
	if e.Type == "password" {
		return true
	}
	if strings.HasPrefix(e.AC, "cc-") || e.AC == "one-time-code" || strings.Contains(e.AC, "password") {
		return true
	}
	return privateField.MatchString(e.Label) || privateField.MatchString(e.Name)
}

// Words names the element the way a person says it: the button "Sign in", the field "Search".
func (e Described) Words() string {
	kind := "element"
	switch {
	case e.Type == "checkbox" || e.Role == "checkbox":
		kind = "checkbox"
	case e.Type == "radio":
		kind = "option"
	case e.Tag == "select":
		kind = "drop-down"
	case e.Tag == "input" || e.Tag == "textarea":
		kind = "field"
		if e.Type == "submit" || e.Type == "button" {
			kind = "button"
		}
	case e.Tag == "button" || e.Role == "button" || e.Role == "menuitem":
		kind = "button"
	case e.Tag == "a" || e.Role == "link":
		kind = "link"
	case e.Role == "tab":
		kind = "tab"
	case e.Tag == "summary":
		kind = "section heading"
	}
	label := strings.Join(strings.Fields(e.Label), " ")
	if label == "" {
		return "a " + kind + " with no label"
	}
	if kind == "element" {
		return fmt.Sprintf("the text %q", label)
	}
	return fmt.Sprintf("the %s %q", kind, label)
}
