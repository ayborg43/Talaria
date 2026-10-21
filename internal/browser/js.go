package browser

// The scripts run in the page. They are plain JavaScript functions evaluated
// with their arguments written in as JSON.

// snapshotJS lists the page's interactive elements, giving each a number that
// the click, type and select tools refer to (kept in a data-tal-ref attribute),
// and returns the page's text. Passwords and card numbers are never read out.
const snapshotJS = `(() => {
  const MAX = 120;
  const sel = 'a[href], button, input, select, textarea, summary, [role="button"], [role="link"], [role="checkbox"], ' +
    '[role="radio"], [role="tab"], [role="menuitem"], [role="switch"], [role="option"], [role="combobox"], [role="textbox"], ' +
    '[contenteditable=""], [contenteditable="true"], [onclick], [tabindex]:not([tabindex="-1"])';
  document.querySelectorAll('[data-tal-ref]').forEach(e => e.removeAttribute('data-tal-ref'));
  const clean = s => (s || '').replace(/\s+/g, ' ').trim().slice(0, 80);
  const visible = el => {
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return false;
    const s = getComputedStyle(el);
    return s.visibility !== 'hidden' && s.display !== 'none' && parseFloat(s.opacity) !== 0;
  };
  // A label's own words, not the options of the drop-down or the text of the field inside it.
  const own = l => {
    const c = l.cloneNode(true);
    c.querySelectorAll('select, input, textarea, button, option, script, style').forEach(n => n.remove());
    return c.textContent;
  };
  const label = el => {
    let t = el.getAttribute('aria-label');
    if (!t && el.id) { const l = document.querySelector('label[for="' + CSS.escape(el.id) + '"]'); if (l) t = own(l); }
    if (!t) { const l = el.closest('label'); if (l) t = own(l); }
    const by = el.getAttribute('aria-labelledby');
    if (!t && by) t = by.split(' ').map(i => (document.getElementById(i) || {}).innerText || '').join(' ');
    if (!t && el.tagName === 'INPUT' && ['button', 'submit', 'reset'].includes(el.type)) t = el.value;
    if (!t && !['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName)) t = el.innerText || el.textContent;
    if (!t) t = el.getAttribute('title') || el.getAttribute('alt') || el.getAttribute('placeholder') || el.getAttribute('name') || '';
    if (!t) { const img = el.querySelector('img[alt]'); if (img) t = img.alt; }
    return clean(t);
  };
  const secret = el => {
    const hint = (el.type + ' ' + (el.autocomplete || '') + ' ' + (el.name || '') + ' ' + (el.id || '')).toLowerCase();
    return /password|passwd|secret|token|cc-|card|cvv|cvc|otp|ssn/.test(hint);
  };
  const seen = new Set(), out = [];
  let total = 0;
  for (const el of document.querySelectorAll(sel)) {
    if (seen.has(el) || !visible(el)) continue;
    seen.add(el);
    total++;
    if (out.length >= MAX) continue;
    const n = out.length + 1;
    el.setAttribute('data-tal-ref', n);
    const tag = el.tagName.toLowerCase(), type = (el.getAttribute('type') || '').toLowerCase();
    const o = { ref: n, tag, type, role: el.getAttribute('role') || '', label: label(el), disabled: !!el.disabled };
    if (tag === 'a') o.href = el.href;
    if (tag === 'input' || tag === 'textarea') {
      o.placeholder = clean(el.placeholder);
      o.value = secret(el) ? (el.value ? '(hidden)' : '') : clean(el.value);
      if (type === 'checkbox' || type === 'radio') o.checked = el.checked;
    }
    if (tag === 'select') {
      o.options = [...el.options].slice(0, 12).map(x => clean(x.text));
      o.value = el.selectedOptions[0] ? clean(el.selectedOptions[0].text) : '';
    }
    out.push(o);
  }
  let text = ((document.body && document.body.innerText) || '').replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim();
  return {
    url: location.href, title: document.title, elements: out, more: total - out.length,
    text: text.slice(0, 6000), textLength: text.length,
    scrollY: Math.round(scrollY), scrollMax: Math.max(0, Math.round(document.documentElement.scrollHeight - innerHeight)),
  };
})()`

// locateJS scrolls the numbered element into view and says where its centre is,
// and what is actually at that spot (something may cover it).
const locateJS = `((ref) => {
  const el = document.querySelector('[data-tal-ref="' + ref + '"]');
  if (!el) return null;
  el.scrollIntoView({ block: 'center', inline: 'center' });
  const r = el.getBoundingClientRect();
  const x = r.left + r.width / 2, y = r.top + r.height / 2;
  const hit = document.elementFromPoint(x, y);
  const name = e => e ? (e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + ((e.innerText || '').trim() ? ' "' + e.innerText.trim().replace(/\s+/g, ' ').slice(0, 40) + '"' : '')) : 'nothing';
  return { x, y, covered: !(hit && (el === hit || el.contains(hit) || hit.contains(el))), hit: name(hit), tag: el.tagName.toLowerCase() };
})`

// focusJS focuses the numbered element and, if asked, selects what is in it so
// that typing replaces it; otherwise the caret goes to the end.
const focusJS = `((ref, clear) => {
  const el = document.querySelector('[data-tal-ref="' + ref + '"]');
  if (!el) return null;
  el.scrollIntoView({ block: 'center', inline: 'center' });
  el.focus();
  const editable = el.isContentEditable;
  try {
    if (clear) {
      if (editable) { const r = document.createRange(); r.selectNodeContents(el); const s = getSelection(); s.removeAllRanges(); s.addRange(r); }
      else el.select();
    } else if (editable) {
      const r = document.createRange(); r.selectNodeContents(el); r.collapse(false); const s = getSelection(); s.removeAllRanges(); s.addRange(r);
    } else if (typeof el.value === 'string') {
      el.setSelectionRange(el.value.length, el.value.length);
    }
  } catch (e) { /* some input types cannot be selected; focus is enough */ }
  const tag = el.tagName.toLowerCase(), type = (el.getAttribute('type') || '').toLowerCase();
  const typable = editable || tag === 'textarea' || (tag === 'input' && !['checkbox', 'radio', 'button', 'submit', 'reset', 'file', 'image', 'range', 'color'].includes(type));
  return { typable, tag, type, focused: document.activeElement === el };
})`

// selectJS chooses an option of a <select> by its value or its text.
const selectJS = `((ref, want) => {
  const el = document.querySelector('[data-tal-ref="' + ref + '"]');
  if (!el) return null;
  if (el.tagName !== 'SELECT') return { error: 'not a drop-down list' };
  const w = want.toLowerCase().trim();
  const opts = [...el.options];
  const opt = opts.find(o => o.value === want) || opts.find(o => o.text.trim().toLowerCase() === w) || opts.find(o => o.text.toLowerCase().includes(w));
  if (!opt) return { error: 'no such option', options: opts.slice(0, 20).map(o => o.text.trim()) };
  el.value = opt.value;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
  return { chosen: opt.text.trim() };
})`

// prepareJS runs in every new document: links that would open a new window open
// in this one instead, since the agent drives a single tab.
const prepareJS = `(() => {
  const open = window.open;
  window.open = function (u) { if (u) location.href = u; return window; };
  document.addEventListener('click', e => {
    const a = e.target && e.target.closest && e.target.closest('a[target]');
    if (a && a.target !== '_self' && a.target !== '') a.target = '_self';
  }, true);
  document.addEventListener('submit', e => { if (e.target && e.target.target) e.target.target = '_self'; }, true);
})();`
