package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// A sign-in is not always one form. Some sites ask for an email first and the password on the next page; some ask for only
// a password; some then ask for a code. These functions look at the page as it is now and say which of those it is asking
// for, so the app can offer exactly those boxes, with the page's own words for them, and fill only those.
//
// They read labels and kinds of fields, never what is in them.

// signInJS finds the fields a sign-in page is asking for, marks each with data-tal-signin (its place in the list) so the
// fill can find it again, and returns them in page order: a username (an email, a phone number, a handle), a password,
// or a code (a one-time code, a verification code). A search box is never one of them, and a lone email box counts only on
// a page that looks like a sign-in.
const signInJS = `(() => {
  document.querySelectorAll('[data-tal-signin]').forEach(e => e.removeAttribute('data-tal-signin'));
  const clean = s => (s || '').replace(/\s+/g, ' ').trim().slice(0, 60);
  const vis = el => {
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return false;
    const s = getComputedStyle(el);
    return s.visibility !== 'hidden' && s.display !== 'none' && parseFloat(s.opacity) !== 0;
  };
  const own = l => { const c = l.cloneNode(true); c.querySelectorAll('select, input, textarea, button, option, script, style').forEach(n => n.remove()); return c.textContent; };
  const label = el => {
    let t = el.getAttribute('aria-label');
    if (!t && el.id) { const l = document.querySelector('label[for="' + CSS.escape(el.id) + '"]'); if (l) t = own(l); }
    if (!t) { const l = el.closest('label'); if (l) t = own(l); }
    const by = el.getAttribute('aria-labelledby');
    if (!t && by) t = by.split(' ').map(i => (document.getElementById(i) || {}).innerText || '').join(' ');
    return clean(t || el.getAttribute('placeholder') || '');
  };
  const searchy = el => el.type === 'search' || el.getAttribute('role') === 'searchbox' || el.getAttribute('role') === 'combobox' ||
    /(^|[^a-z])(search|query|q)([^a-z]|$)/i.test((el.name || '') + ' ' + (el.id || '')) || !!el.closest('[role="search"], form[action*="search" i]');
  const found = [];
  let hasPassword = false, cue = false;
  for (const el of document.querySelectorAll('input')) {
    if (!vis(el) || el.disabled || el.readOnly) continue;
    const ty = (el.getAttribute('type') || 'text').toLowerCase();
    const ac = (el.getAttribute('autocomplete') || '').toLowerCase();
    const lab = label(el);
    const hint = (lab + ' ' + (el.name || '') + ' ' + (el.id || '') + ' ' + (el.placeholder || '')).toLowerCase();
    let kind = '';
    if (ty === 'password') { kind = 'password'; hasPassword = true; }
    else if (!['text', 'email', 'tel', 'number', ''].includes(ty) || searchy(el)) continue;
    else if (ac === 'one-time-code' || /(one.?time|otp|verification|security code|2fa|two.?factor|authenticator|passcode|6.?digit|enter the code)/.test(hint)) { kind = 'code'; cue = true; }
    else if (/^(username|email|tel|webauthn)/.test(ac) || /(user ?name|e-?mail|log ?in|identifier|phone|mobile|account|handle)/.test(hint)) kind = 'username';
    else continue;
    found.push({ el, kind, label: lab, isNew: ac === 'new-password' || /(confirm|repeat|re-?enter|retype|again)/.test(hint) || /(new password|create)/.test(hint) });
  }
  if (!found.length) return [];
  if (!cue) {
    const path = (location.pathname + ' ' + location.search + ' ' + location.hostname).toLowerCase();
    cue = hasPassword || /(log ?-?in|sign ?-?in|signin|auth|account|verify|session|sso|identity|oauth|accounts\.)/.test(path) ||
      [...document.querySelectorAll('button, input[type="submit"], [role="button"]')].some(b => vis(b) && /^(sign ?in|log ?in|continue|next|verify|confirm)\b/i.test(clean(b.innerText || b.value)));
  }
  if (!cue) return [];
  const names = { username: 'Email or username', password: 'Password', code: 'Code' };
  return found.slice(0, 4).map((f, i) => {
    f.el.setAttribute('data-tal-signin', String(i));
    let l = f.label;
    if (!l || l.length > 40 || /^[a-z0-9_\-]+$/.test(l) && l.length > 24) l = names[f.kind];
    return { kind: f.kind, label: l, isNew: f.isNew && f.kind === 'password' };
  });
})()`

// signInFocusJS focuses the marked field and selects what is in it, so typing replaces it. It says what kind of input it is.
const signInFocusJS = `((i) => {
  const e = document.querySelector('[data-tal-signin="' + i + '"]');
  if (!e) return null;
  e.scrollIntoView({ block: 'center' });
  e.focus();
  if (typeof e.select === 'function') e.select();
  return { type: (e.getAttribute('type') || 'text').toLowerCase(), focused: document.activeElement === e };
})`

// SignInField is one thing a page is asking for before it will let you in.
type SignInField struct {
	Kind  string `json:"kind"`          // username, password or code
	Label string `json:"label"`         // the page's own word for it
	New   bool   `json:"new,omitempty"` // a password being made or repeated, not one being given
}

// SignInFields says what this page is asking for to sign in: nothing, or some of a username, a password and a code.
func (p *Page) SignInFields(ctx context.Context) []SignInField {
	var raw []struct {
		Kind  string `json:"kind"`
		Label string `json:"label"`
		IsNew bool   `json:"isNew"`
	}
	if err := p.eval(ctx, signInJS, &raw); err != nil {
		return nil
	}
	out := make([]SignInField, 0, len(raw))
	for _, r := range raw {
		out = append(out, SignInField{Kind: r.Kind, Label: r.Label, New: r.IsNew})
	}
	return out
}

// SignInValue is what the user typed for one of the fields SignInFields listed.
type SignInValue struct {
	Index int    `json:"index"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// FillSignIn types the user's own values into the fields the page is asking for, each in the one it was typed for. The page
// may have moved on since the boxes were drawn, so each field is found again and must still be the kind it was; if not,
// nothing more is typed and the user is asked to look again. Like InsertText, what is typed is the user's, so it is never
// returned, logged or shown to the model. It does not submit anything: the user taps the page's own button.
func (p *Page) FillSignIn(ctx context.Context, values []SignInValue) error {
	filled := false
	for _, v := range values {
		if len(v.Value) > 4000 {
			return errors.New("that is too much to type at once")
		}
		filled = filled || v.Value != ""
	}
	if !filled {
		return errors.New("type something first")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.touch()
	now := p.SignInFields(ctx)
	for _, v := range values {
		if v.Value == "" {
			continue
		}
		if v.Index < 0 || v.Index >= len(now) || now[v.Index].Kind != v.Kind {
			return errors.New("the page has changed since the boxes were drawn: look at them again")
		}
		var f *struct {
			Type    string `json:"type"`
			Focused bool   `json:"focused"`
		}
		if err := p.eval(ctx, fmt.Sprintf("%s(%d)", signInFocusJS, v.Index), &f); err != nil {
			return err
		}
		if f == nil || !f.Focused {
			return errors.New("could not reach that field: tap it in the page and type below instead")
		}
		if (v.Kind == "password") != (strings.EqualFold(f.Type, "password")) { // a password only ever goes into a password field
			return errors.New("that field is not the kind it looked like: tap it in the page and type below instead")
		}
		p.took()
		if _, err := p.call(ctx, "Input.insertText", map[string]any{"text": v.Value}); err != nil {
			return err
		}
	}
	return nil
}
