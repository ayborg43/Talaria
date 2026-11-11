'use strict';
/* Talaria's web app, second part: the things you do with your own hands. The agent's computer (its browser,
   a terminal, its files), the document viewer, the cards that show mail, pages, plans and spending for what
   they are, the conversations sheet, and your own profile and notes.

   This file is loaded before app.js and only declares functions, so everything here that app.js calls exists
   by the time it runs; in turn it uses app.js's helpers (h, $, api, toast, S) only from inside functions, which
   run later. As there, everything from outside goes on the page as text, never as markup. */

// ---------- a full-screen panel over the app ----------
const PANEL = { onClose: null, timer: 0 };
function openPanel(title, sub) {
  closePanel();
  $('#panel-title').textContent = title;
  $('#panel-sub').textContent = sub || '';
  $('#panel-acts').replaceChildren();
  $('#panel-seg').replaceChildren(); $('#panel-seg').hidden = true;
  $('#panel-body').replaceChildren();
  $('#panel-body').className = 'panel-body';
  $('#panel').hidden = false;
  return $('#panel-body');
}
function closePanel() {
  if (PANEL.onClose) { try { PANEL.onClose(); } catch { /* closing anyway */ } }
  PANEL.onClose = null;
  clearTimeout(PANEL.timer);
  const p = document.getElementById('panel');
  if (p) p.hidden = true;
}
document.getElementById('panel-close').addEventListener('click', closePanel);
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && !document.getElementById('panel').hidden && !e.target.closest('input,textarea')) closePanel(); });
document.getElementById('computer-btn').addEventListener('click', () => openComputer());
function segments(items, current, pick) {
  const seg = $('#panel-seg');
  seg.hidden = false;
  seg.replaceChildren(...items.map(([id, label]) => h('button', { type: 'button', role: 'tab', 'aria-selected': String(id === current), text: label, onclick: () => pick(id) })));
}
const failed = (e) => { if (!(e instanceof AuthError)) toast(e.message); };

// ---------- the agent's computer ----------
const COMPUTER = { seg: 'browser', path: '.', tab: '' };
function openComputer(seg) {
  if (seg) COMPUTER.seg = seg;
  openPanel('Computer', 'The agent’s browser, terminal and files');
  showComputer();
}
function showComputer() {
  if (PANEL.onClose) { PANEL.onClose(); PANEL.onClose = null; }
  clearTimeout(PANEL.timer);
  segments([['browser', 'Browser'], ['terminal', 'Terminal'], ['files', 'Files']], COMPUTER.seg, (id) => { COMPUTER.seg = id; showComputer(); });
  const body = $('#panel-body');
  body.replaceChildren(h('div', { class: 'skeleton' }));
  ({ browser: showBrowser, terminal: showTerminal, files: showFiles })[COMPUTER.seg](body);
}
function computerCard() {
  return h('div', { class: 'card' }, h('h2', null, 'The agent’s computer'),
    h('div', { class: 'field-note', text: 'Watch the page it has open and take over when a site needs you, run a command yourself, or look through its files.' }),
    h('div', { class: 'btn-row' },
      h('button', { class: 'btn', type: 'button', text: 'Browser', onclick: () => openComputer('browser') }),
      h('button', { class: 'btn', type: 'button', text: 'Terminal', onclick: () => openComputer('terminal') }),
      h('button', { class: 'btn', type: 'button', text: 'Files', onclick: () => openComputer('files') })));
}

// The browser: a live view of the tab the agent has open, shown only while this is on screen. The server sends a picture
// whenever the page changes and the browser draws it in a plain image; if that cannot be had, a picture is fetched about
// every second and a half instead. Pointing at the picture clicks the page. What is typed here goes to the page as it is
// typed, in a masked box for a password (which a password manager can fill), and nowhere else.
async function showBrowser(body) {
  let d;
  await loadRoutines();
  try { d = await api('/api/browser'); } catch (e) { failed(e); body.replaceChildren(h('div', { class: 'card empty', text: e.message })); return; }
  if (COMPUTER.seg !== 'browser' || $('#panel').hidden) return;
  const tabs = d.tabs || [];
  if (!tabs.length) {
    body.replaceChildren(h('div', { class: 'card' }, h('h2', null, 'No page is open'),
      h('div', { class: 'field-note', text: 'When the agent opens a page, it shows here as it sees it, and you can take control: sign in, pass a check, or finish something by hand. Ask it to open a site to begin.' }),
      h('div', { class: 'btn-row' }, h('button', { class: 'btn', type: 'button', text: 'Look again', onclick: showComputer }))));
    return;
  }
  if (COMPUTER.want) { const wanted = tabs.find((t) => hostOf(t.url) === COMPUTER.want); if (wanted) COMPUTER.tab = wanted.id; COMPUTER.want = ''; }
  if (!tabs.some((t) => t.id === COMPUTER.tab)) COMPUTER.tab = tabs[0].id;
  const W = d.width || 1280, H = d.height || 800;
  const addr = h('input', { type: 'text', class: 'addr', 'aria-label': 'Address', autocomplete: 'off', autocapitalize: 'none', spellcheck: 'false', enterkeyhint: 'go' });
  addr.value = (tabs.find((t) => t.id === COMPUTER.tab) || {}).url || '';
  const img = h('img', { class: 'shot', alt: 'The page the agent has open', draggable: 'false' });
  const stage = h('div', { class: 'stage', tabindex: '0', 'aria-label': 'Page. Click a field here, then type: on a keyboard, type directly; on a phone, use the box under the page.' }, img);
  // Signing in without aiming at a picture: a real form (so a password manager can fill it) whose values are
  // typed into the page's own username and password fields and then cleared here. Shown only on a page that
  // is asking for a password; the watcher below keeps that up to date as the page moves.
  const cur = tabs.find((t) => t.id === COMPUTER.tab) || {};
  // The boxes are whatever this page is asking for: only an email on the first page of a two-step sign-in, only a password on
  // the second, a code on a third, the page's own word for each. They are rebuilt when the page's questions change.
  let signKey = '', signInputs = [], signFields = [];
  const loginForm = h('form', { class: 'login-form', hidden: true });
  const words = (fs) => {
    const w = [...new Set(fs.map((f) => (f.kind === 'password' ? (f.new ? 'a new password' : 'your password') : f.kind === 'code' ? 'a code' : 'your username or email')))];
    return w.length > 1 ? w.slice(0, -1).join(', ') + ' and ' + w[w.length - 1] : w[0];
  };
  const drawSignIn = (fields) => {
    fields = fields || [];
    const key = JSON.stringify(fields);
    if (key === signKey) return;
    signKey = key; signFields = fields;
    if (!fields.length) { signInputs = []; loginForm.hidden = true; loginForm.replaceChildren(); return; }
    signInputs = fields.map((f, i) => h('input', {
      type: f.kind === 'password' ? 'password' : 'text', name: f.kind + i, 'aria-label': f.label, placeholder: f.label,
      autocomplete: f.kind === 'password' ? (f.new ? 'new-password' : 'current-password') : f.kind === 'code' ? 'one-time-code' : 'username',
      inputmode: f.kind === 'code' ? 'numeric' : null, autocapitalize: 'none', spellcheck: 'false', enterkeyhint: i === fields.length - 1 ? 'go' : 'next' }));
    loginForm.replaceChildren(h('div', { class: 'login-title', text: 'This page asks for ' + words(fields) }), ...signInputs,
      h('button', { class: 'btn primary', type: 'submit', text: fields.length === 1 ? 'Fill ' + fields[0].label.toLowerCase() : 'Fill these in' }),
      h('div', { class: 'field-note', text: 'These go into the page’s own fields and nowhere else: not to the agent, not saved here. Then tap the page’s own button (Next, Sign in) in the picture, or press Enter below.' }));
    loginForm.hidden = false;
  };
  drawSignIn(cur.signin || (cur.login ? [{ kind: 'password', label: 'Password' }] : []));
  const livePill = h('span', { class: 'pill ok live-pill', text: 'LIVE', hidden: '' });
  let alive = true, busy = false, last = '', live = false;
  // one picture, fetched now: the fallback when no live view can be had, and a refresh after a tap
  const shot = async () => {
    if (!alive || busy || document.hidden || live) return;
    busy = true;
    try {
      const r = await fetch('/api/browser/shot?id=' + encodeURIComponent(COMPUTER.tab), { credentials: 'same-origin' });
      if (r.status === 401) { showLogin(); return; }
      if (r.status === 404) { alive = false; showComputer(); return; } // the tab closed: show what is open now
      if (r.ok) {
        const url = URL.createObjectURL(await r.blob());
        if (last) URL.revokeObjectURL(last); // one picture is held at a time
        last = url; img.src = url;
        const at = r.headers.get('X-Page-URL');
        if (at && document.activeElement !== addr) addr.value = at;
      }
    } catch { /* offline: the next one will try again */ }
    busy = false;
  };
  const poll = () => { clearTimeout(PANEL.timer); if (!alive || live) return; PANEL.timer = setTimeout(async () => { await shot(); poll(); }, 1500); };
  // The live view is one WebSocket: pictures arrive when the page paints, and taps, drags, keys and typed text go back on
  // the same connection, in order, with no request for each. If a WebSocket cannot be had (a proxy that blocks it), the
  // older way is used: a stream of pictures, and a request per action.
  let ws = null, takenOver = false, wsBroken = false, shown = '', focusWanted = 0;
  const wsOpen = () => !!ws && ws.readyState === 1;
  const emit = (m) => { if (!wsOpen()) return false; ws.send(JSON.stringify(m)); return true; };
  const drawPicture = (blob) => {
    const u = URL.createObjectURL(blob), old = shown; shown = u; img.src = u;
    if (old) setTimeout(() => URL.revokeObjectURL(old), 1500);
  };
  const fromServer = (m) => {
    if (m.t === 'focus') {
      if (m.url && document.activeElement !== addr) addr.value = m.url;
      if (m.signin) drawSignIn(m.signin);
      if (Date.now() - focusWanted < 2500) { focusWanted = 0; setMode(m.kind || 'none'); } // a tap (or Tab) on a field opens the right box to type into
    } else if (m.t === 'taken') {
      takenOver = true; toast('This view was opened on another screen. Tap reload to bring it back here.');
    } else if (m.t === 'error') {
      wsBroken = true;
    }
  };
  const startStream = () => {
    live = true; livePill.hidden = true;
    img.src = '/api/browser/stream?id=' + encodeURIComponent(COMPUTER.tab) + '&t=' + Date.now();
  };
  const startLive = () => {
    if (!alive || document.hidden || takenOver) return;
    if (wsBroken || !('WebSocket' in window)) { startStream(); return; }
    let sock;
    try { sock = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/browser/live?id=' + encodeURIComponent(COMPUTER.tab)); }
    catch { wsBroken = true; startStream(); return; }
    ws = sock; sock.binaryType = 'blob';
    let got = false;
    sock.onmessage = (e) => {
      if (sock !== ws) return;
      if (typeof e.data === 'string') { try { fromServer(JSON.parse(e.data)); } catch { /* not for us */ } return; }
      if (!got) { got = true; live = true; livePill.hidden = false; }
      drawPicture(e.data);
    };
    sock.onclose = () => {
      if (sock !== ws) return;
      ws = null; livePill.hidden = true;
      if (!alive || document.hidden || takenOver) { live = false; return; }
      if (!got) { wsBroken = true; startStream(); poll(); return; } // it never worked here: use the older way
      live = false; setTimeout(() => { if (alive && !ws && !document.hidden && !takenOver) startLive(); }, 700); // it dropped: come back
    };
    sock.onerror = () => {};
  };
  const stopLive = () => {
    live = false; livePill.hidden = true;
    if (ws) { const w = ws; ws = null; try { w.close(); } catch { /* already gone */ } }
    img.removeAttribute('src');
  };
  img.addEventListener('load', () => { if (live && !wsOpen()) livePill.hidden = false; });
  img.addEventListener('error', () => { if (live && !wsOpen()) { live = false; livePill.hidden = true; shot(); poll(); } }); // no live view: fall back to pictures
  // the address and whether the tab is still there, now and then: a live view carries no headers
  const watch = async () => {
    if (!alive || document.hidden) return;
    try {
      const t = ((await api('/api/browser')).tabs || []).find((x) => x.id === COMPUTER.tab);
      if (!t) { alive = false; showComputer(); return; }
      if (t.url && document.activeElement !== addr) addr.value = t.url;
      drawSignIn(t.signin); // a page that starts (or stops) asking for something gets (or loses) the right boxes
    } catch { /* the next one will try again */ }
  };
  const watcher = setInterval(watch, 3000);
  const vis = () => { if (document.hidden) stopLive(); else if (alive) { startLive(); poll(); } };
  document.addEventListener('visibilitychange', vis);
  PANEL.onClose = () => { alive = false; stopLive(); clearInterval(watcher); document.removeEventListener('visibilitychange', vis); if (last) URL.revokeObjectURL(last); if (shown) URL.revokeObjectURL(shown); };

  const post = (a) => api('/api/browser/act', { method: 'POST', body: { id: COMPUTER.tab, ...a } }).catch((e) => { if (!(e instanceof AuthError)) failed(e); return null; });
  // Typing: what is in the box is sent to the page as it changes, as only the part that changed, in order. A password
  // manager filling the box in one go arrives as a replacement. Nothing typed here is kept.
  const typed = h('input', { type: 'text', class: 'typed', 'aria-label': 'Type into the page', placeholder: 'Type into the page…', autocomplete: 'off', autocapitalize: 'none', spellcheck: 'false', enterkeyhint: 'go' });
  let prev = '', chain = Promise.resolve(), timer = 0;
  const setMode = (kind) => {
    typed.value = ''; prev = '';
    typed.type = kind === 'password' ? 'password' : 'text';
    typed.setAttribute('autocomplete', kind === 'password' ? 'current-password' : kind === 'text' ? 'username' : 'off');
    typed.placeholder = kind === 'password' ? 'Password — tap here and type (manager can fill)' : kind === 'text' ? 'Tap here and type into the field you tapped' : 'Tap a field in the page, then type here…';
    modeBtn.textContent = kind === 'password' ? 'Show text' : 'Password';
    typed.classList.toggle('secret', kind === 'password');
    if (kind !== 'none') { try { typed.focus({ preventScroll: false }); typed.scrollIntoView({ block: 'nearest' }); } catch { /* a phone may want the box tapped */ } }
  };
  const modeBtn = h('button', { type: 'button', class: 'key', text: 'Password', title: 'Type a password into a masked box', onclick: () => setMode(typed.type === 'password' ? 'text' : 'password') });
  const flush = () => {
    const now = typed.value; if (now === prev) return;
    let i = 0; while (i < prev.length && i < now.length && prev[i] === now[i]) i++;
    const del = prev.length - i, add = now.slice(i); prev = now;
    if (wsOpen()) { // sent the moment it is typed, in order, on the open connection
      if (del === 0) emit({ t: 'text', text: add });
      else if (del <= 3 && !add) for (let k = 0; k < del; k++) emit({ t: 'key', key: 'Backspace' });
      else emit({ t: 'replace', text: now });
      return;
    }
    chain = chain.then(async () => {
      if (del === 0) await post({ kind: 'type', text: add });
      else if (del <= 3 && !add) for (let k = 0; k < del; k++) await post({ kind: 'key', key: 'Backspace' });
      else await post({ kind: 'replace', text: now });
      if (!live) shot();
    });
  };
  typed.addEventListener('input', (e) => { if (e.isComposing) return; if (wsOpen()) flush(); else { clearTimeout(timer); timer = setTimeout(flush, 60); } });
  typed.addEventListener('compositionend', () => flush());
  typed.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    e.preventDefault(); clearTimeout(timer); flush();
    if (!emit({ t: 'key', key: 'Enter' })) chain = chain.then(async () => { await post({ kind: 'key', key: 'Enter' }); if (!live) shot(); });
  });
  // Nothing else may reach the page before what is still in the typing box: a tap on another field (or on
  // Sign in) used to be able to overtake the last letters, which then landed in the wrong field.
  const flushNow = async () => { clearTimeout(timer); flush(); await chain; };

  const act = async (a) => {
    if (wsOpen() && (a.kind === 'click' || a.kind === 'key' || a.kind === 'wheel')) { // one connection, in order: nothing to wait for
      flush();
      if (a.kind === 'click') { focusWanted = Date.now(); emit({ t: 'mouse', k: 'down', x: a.x, y: a.y, b: 'left', n: 1 }); emit({ t: 'mouse', k: 'up', x: a.x, y: a.y, b: 'left', n: 1 }); }
      else if (a.kind === 'key') { if (a.key === 'Tab') focusWanted = Date.now(); emit({ t: 'key', key: a.key, mod: a.shift ? 8 : 0 }); }
      else emit({ t: 'wheel', x: a.x, y: a.y, dy: a.dy });
      return;
    }
    await flushNow();
    const r = await post(a);
    if (r && r.url && document.activeElement !== addr) addr.value = r.url;
    if (r && (a.kind === 'click' || a.kind === 'key')) setMode(r.focus || 'none'); // a tap (or Tab) on a field opens the right box to type into
    if (!live) await shot();
  };
  loginForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const values = signInputs.map((inp, i) => ({ index: i, kind: signFields[i].kind, value: inp.value })).filter((v) => v.value);
    if (!values.length) { (signInputs[0] || {}).focus && signInputs[0].focus(); toast('Type what the page asks for first.'); return; }
    const btn = loginForm.querySelector('button[type=submit]'); if (btn) btn.disabled = true;
    const r = await post({ kind: 'login', fields: values });
    if (btn) btn.disabled = false;
    if (!r) return;
    signInputs.forEach((inp) => { inp.value = ''; }); // filled into the page; kept nowhere here
    toast('Filled in the page. Now tap its own button in the picture.');
    if (!live) await shot();
    stage.scrollIntoView({ block: 'nearest' });
  });
  // With a real keyboard, typing straight at the picture works too: click a field, then just type. Each key goes to the
  // page as it is pressed.
  const typedKeys = { Enter: 'Enter', Tab: 'Tab', Escape: 'Escape', Backspace: 'Backspace', Delete: 'Delete', ArrowUp: 'ArrowUp', ArrowDown: 'ArrowDown', ArrowLeft: 'ArrowLeft', ArrowRight: 'ArrowRight', Home: 'Home', End: 'End', PageUp: 'PageUp', PageDown: 'PageDown' };
  const sendText = (text) => { if (!emit({ t: 'text', text })) chain = chain.then(async () => { await post({ kind: 'type', text }); if (!live) shot(); }); };
  stage.addEventListener('keydown', (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (typedKeys[e.key]) { e.preventDefault(); act({ kind: 'key', key: typedKeys[e.key], shift: e.shiftKey }); }
    else if (e.key.length === 1) { e.preventDefault(); sendText(e.key); }
  });
  stage.addEventListener('paste', (e) => { const text = (e.clipboardData || {}).getData ? e.clipboardData.getData('text') : ''; if (text) { e.preventDefault(); sendText(text); } });
  const point = (e) => { const b = img.getBoundingClientRect(); return { x: Math.round(((e.clientX - b.left) / b.width) * W), y: Math.round(((e.clientY - b.top) / b.height) * H) }; };
  const scale = () => W / img.getBoundingClientRect().width; // a page pixel for each pixel of the picture on screen
  // What the pointer does on the picture. On the live connection it is the pointer itself: press, drag and release reach
  // the page as they happen, so a drag selects or moves things and a double click is a double click; a finger tap is a
  // click and a finger drag scrolls the page as it moves. Without it, a tap is one click and a swipe is one scroll.
  let drag = null, accScroll = null;
  const sendScroll = (x, y, dx, dy) => {
    if (accScroll) { accScroll.dx += dx; accScroll.dy += dy; accScroll.x = x; accScroll.y = y; return; }
    accScroll = { x, y, dx, dy };
    requestAnimationFrame(() => { const a = accScroll; accScroll = null; if (a && (a.dx || a.dy)) emit({ t: 'wheel', x: a.x, y: a.y, dx: Math.round(a.dx), dy: Math.round(a.dy) }); });
  };
  let swipe = null, ateClick = false, moveQueued = false, lastMove = null;
  img.addEventListener('contextmenu', (e) => { if (wsOpen()) e.preventDefault(); });
  img.addEventListener('pointerdown', (e) => {
    if (!wsOpen()) { swipe = { y: e.clientY, x: e.clientX }; return; }
    if (stage.classList.contains('zoomed') && e.pointerType !== 'mouse') return; // a finger on the enlarged picture moves the picture
    e.preventDefault();
    try { img.setPointerCapture(e.pointerId); stage.focus({ preventScroll: true }); } catch { /* not everywhere */ }
    const touch = e.pointerType !== 'mouse', p = point(e), b = e.button === 2 ? 'right' : e.button === 1 ? 'middle' : 'left';
    drag = { id: e.pointerId, touch, sx: e.clientX, sy: e.clientY, lx: e.clientX, ly: e.clientY, moved: false, p, b };
    if (!touch) { focusWanted = Date.now(); emit({ t: 'mouse', k: 'down', x: p.x, y: p.y, b, n: Math.max(1, Math.min(3, e.detail || 1)) }); }
  });
  img.addEventListener('pointermove', (e) => {
    if (!drag || e.pointerId !== drag.id || !wsOpen()) return;
    if (drag.touch) {
      if (!drag.moved && Math.hypot(e.clientX - drag.sx, e.clientY - drag.sy) < 10) return;
      drag.moved = true;
      const k = scale(), p = point(e);
      sendScroll(p.x, p.y, -(e.clientX - drag.lx) * k, -(e.clientY - drag.ly) * k); // the page follows the finger
      drag.lx = e.clientX; drag.ly = e.clientY;
    } else if (e.buttons) {
      lastMove = point(e);
      if (!moveQueued) { moveQueued = true; requestAnimationFrame(() => { moveQueued = false; if (lastMove) emit({ t: 'mouse', k: 'move', x: lastMove.x, y: lastMove.y, b: drag ? drag.b : 'left' }); }); }
    }
  });
  const release = (e) => {
    if (!drag || e.pointerId !== drag.id) return;
    const d = drag; drag = null;
    if (!wsOpen()) return;
    const p = point(e);
    if (!d.touch) { emit({ t: 'mouse', k: 'up', x: p.x, y: p.y, b: d.b, n: Math.max(1, Math.min(3, e.detail || 1)) }); return; }
    if (!d.moved) { focusWanted = Date.now(); emit({ t: 'mouse', k: 'down', x: d.p.x, y: d.p.y, b: 'left', n: 1 }); emit({ t: 'mouse', k: 'up', x: d.p.x, y: d.p.y, b: 'left', n: 1 }); }
  };
  img.addEventListener('pointerup', (e) => {
    if (wsOpen()) { release(e); return; }
    if (!swipe) return;
    const dy = e.clientY - swipe.y, at = point(e); swipe = null;
    if (Math.abs(dy) < 24) return; // a tap, not a swipe: the click below handles it
    ateClick = true; // the tap this swipe ends with must not also click what it lands on
    act({ kind: 'wheel', ...at, dy: dy < 0 ? 400 : -400 }); // swipe up scrolls the page down, as a finger expects
  });
  img.addEventListener('pointercancel', () => { drag = null; });
  img.addEventListener('click', (e) => { if (wsOpen()) return; if (ateClick) { ateClick = false; return; } try { stage.focus({ preventScroll: true }); } catch { /* not focusable everywhere */ } act({ kind: 'click', ...point(e) }); });
  let wheelAt = 0;
  img.addEventListener('wheel', (e) => {
    e.preventDefault();
    if (wsOpen()) { const p = point(e), k = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? H : 1; sendScroll(p.x, p.y, e.deltaX * k, e.deltaY * k); return; }
    if (Date.now() - wheelAt < 250) return; // a wheel sends dozens of events: a few a second are enough
    wheelAt = Date.now();
    act({ kind: 'wheel', ...point(e), dy: Math.sign(e.deltaY) * 400 });
  }, { passive: false });
  const go = () => { if (addr.value.trim()) { addr.blur(); act({ kind: 'open', url: addr.value.trim() }); } };
  addr.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); go(); } });
  const key = (label, name) => h('button', { type: 'button', class: 'key', text: label, 'aria-label': name, onclick: () => act({ kind: 'key', key: name }) });
  const ikey = (name, label, fn) => h('button', { type: 'button', class: 'key', 'aria-label': label, title: label, onclick: fn }, icon(name));
  const picker = tabs.length > 1 ? h('div', { class: 'seg inner' }, tabs.map((t, i) => h('button', { type: 'button', 'aria-selected': String(t.id === COMPUTER.tab), text: 'Tab ' + (i + 1), onclick: () => { COMPUTER.tab = t.id; showComputer(); } }))) : null;
  // While the agent is using the tab itself and nobody has asked for the user, a tap can collide with what it is doing.
  const busyBar = working() && !pendingHandoffs().length
    ? h('div', { class: 'handoff-bar busy' }, h('span', { class: 'grow' }, h('b', { text: 'The agent is using this tab' }), h('span', { class: 's', text: 'Your taps work, but may collide with what it is doing.' })),
      h('button', { class: 'btn', type: 'button', text: 'Stop it', onclick: () => $('#stop').click() })) : null;
  body.className = 'panel-body browser';
  body.replaceChildren(...[handoffBar(), recordBar(), busyBar, picker,
    h('div', { class: 'toolbar' },
      ikey('back', 'Back a page', () => act({ kind: 'back' })),
      addr, livePill, h('button', { type: 'button', class: 'key go', text: 'Go', onclick: go })),
    loginForm,
    stage,
    h('div', { class: 'toolbar typing' }, typed, modeBtn),
    h('div', { class: 'toolbar keys' }, key('Enter', 'Enter'), key('Tab', 'Tab'), key('Esc', 'Escape'), ikey('del', 'Backspace', () => act({ kind: 'key', key: 'Backspace' })),
      ikey('up', 'Up arrow', () => act({ kind: 'key', key: 'ArrowUp' })), ikey('down', 'Down arrow', () => act({ kind: 'key', key: 'ArrowDown' })),
      ikey('refresh', 'Reload the view', () => { takenOver = false; if (live || ws) { stopLive(); startLive(); } else { startLive(); shot(); } }),
      ikey('zoom', 'Make the picture bigger or smaller', () => stage.classList.toggle('zoomed'))),
    h('div', { class: 'field-note', text: 'You are working the same tab the agent uses. For a sign-in, use the username and password boxes above. For anything else: tap a field in the picture, then type in the box under it (or just type, with a keyboard). Swipe the picture to scroll. It is told you took control, and reads the page again before it carries on. What you type here is not shown to it.' })].filter(Boolean));
  startLive(); poll();
}

// The terminal: one command at a time, thirty seconds each, on the agent's computer and never on the server.
function runBlock(rc) {
  const meta = rc.timed_out ? 'stopped after 30s' : 'exit ' + rc.exit + ' · ' + (rc.ms / 1000).toFixed(1) + 's';
  return h('div', { class: 'run' },
    h('div', { class: 'run-head' }, h('span', { class: 'prompt', text: '$' }), h('span', { class: 'grow cmd', text: rc.command }), h('span', { class: 'when ' + (rc.exit === 0 && !rc.timed_out ? 'state-ok' : 'state-bad'), text: meta })),
    rc.output ? h('pre', { class: 'out', text: rc.output }) : null);
}
async function showTerminal(body) {
  let d;
  try { d = await api('/api/computer'); } catch (e) { failed(e); body.replaceChildren(h('div', { class: 'card empty', text: e.message })); return; }
  if (COMPUTER.seg !== 'terminal' || $('#panel').hidden) return;
  if (!d.available) { body.replaceChildren(h('div', { class: 'card' }, h('h2', null, 'No terminal here'), h('div', { class: 'field-note', text: d.reason }))); return; }
  const log = h('div', { class: 'runs', role: 'log', 'aria-live': 'polite' }, (d.receipts || []).slice().reverse().map(runBlock));
  if (!log.children.length) log.append(h('div', { class: 'empty', text: 'Commands you run are listed here, with what they printed. The last 30 are kept.' }));
  const cmd = h('input', { type: 'text', class: 'typed mono', 'aria-label': 'Command', placeholder: 'ls -la', autocomplete: 'off', autocapitalize: 'none', autocorrect: 'off', spellcheck: 'false', enterkeyhint: 'send' });
  const btn = h('button', { type: 'submit', class: 'key go', text: 'Run' });
  const form = h('form', { class: 'toolbar' }, h('span', { class: 'prompt', text: '$' }), cmd, btn);
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const text = cmd.value.trim();
    if (!text) return;
    btn.disabled = true; cmd.disabled = true;
    try {
      const rc = await api('/api/computer/run', { method: 'POST', body: { command: text } });
      if (log.querySelector('.empty')) log.replaceChildren();
      log.append(runBlock(rc)); cmd.value = '';
      log.scrollTop = log.scrollHeight;
    } catch (x) { failed(x); }
    btn.disabled = false; cmd.disabled = false; cmd.focus();
  });
  body.className = 'panel-body terminal';
  body.replaceChildren(h('div', { class: 'field-note', text: 'Runs in ' + d.where + '. Each command may take up to 30 seconds.' }), log, form);
  log.scrollTop = log.scrollHeight;
}

// Files: the folders of the agent's computer, a plain editor for text, and a way to move a PDF or picture
// between there and Documents.
const DOC_NAME = /\.(pdf|png|jpe?g|gif|webp)$/i;
const joinPath = (dir, name) => (dir === '.' || dir === '' ? name : dir.replace(/\/$/, '') + '/' + name);
const parentPath = (p) => { const i = p.replace(/\/$/, '').lastIndexOf('/'); return i < 0 ? '.' : i === 0 ? '/' : p.slice(0, i); };
async function showFiles(body) {
  let d;
  try { d = await api('/api/computer/files?path=' + encodeURIComponent(COMPUTER.path)); }
  catch (e) {
    if (e instanceof AuthError) return;
    if (COMPUTER.path !== '.') { toast(e.message); COMPUTER.path = '.'; return showFiles(body); }
    body.replaceChildren(h('div', { class: 'card' }, h('h2', null, 'No files here'), h('div', { class: 'field-note', text: e.message }))); return;
  }
  if (COMPUTER.seg !== 'files' || $('#panel').hidden) return;
  const here = d.path;
  const rows = h('div', { class: 'rows' });
  if (here !== '.' && here !== '/') rows.append(h('button', { class: 'row', type: 'button', onclick: () => { COMPUTER.path = parentPath(here); showComputer(); } }, h('span', { class: 'tag' }, icon('up')), h('span', { class: 'grow' }, h('span', { class: 't', text: 'Up a folder' }))));
  for (const e of d.entries || []) {
    const full = joinPath(here, e.name);
    const open = e.dir ? () => { COMPUTER.path = full; showComputer(); } : () => editFile(full);
    const row = h('div', { class: 'row' },
      h('button', { class: 'rowbtn', type: 'button', onclick: open }, h('span', { class: 'tag' + (e.dir ? ' goal' : ''), text: e.dir ? 'DIR' : (e.name.split('.').pop() || '').slice(0, 4).toUpperCase() || 'FILE' }), h('span', { class: 'grow' }, h('span', { class: 't', text: e.name }))));
    if (!e.dir && DOC_NAME.test(e.name)) row.append(h('button', { class: 'btn small', type: 'button', text: 'To Documents', onclick: () => keepFile(full) }));
    rows.append(row);
  }
  if (!(d.entries || []).length) rows.append(h('div', { class: 'empty', text: 'This folder is empty.' }));
  if (d.more) rows.append(h('div', { class: 'field-note', text: '+' + d.more + ' more not listed.' }));
  const name = h('input', { type: 'text', 'aria-label': 'Name for a new file or folder', placeholder: 'notes.txt', autocomplete: 'off', autocapitalize: 'none', spellcheck: 'false' });
  const make = async (dir) => {
    const n = name.value.trim();
    if (!n) { toast('Give it a name first.'); return; }
    if (!dir) { editFile(joinPath(here, n), true); return; }
    try { await api('/api/computer/file', { method: 'POST', body: { path: joinPath(here, n), dir: true } }); showComputer(); } catch (e) { failed(e); }
  };
  body.className = 'panel-body';
  body.replaceChildren(
    h('div', { class: 'card' }, h('h2', null, h('span', { class: 'mono path', text: here === '.' ? 'Working folder' : here })), rows),
    h('div', { class: 'card' }, h('h2', null, 'New here'), h('div', { class: 'field' }, name),
      h('div', { class: 'btn-row' }, h('button', { class: 'btn', type: 'button', text: 'New file', onclick: () => make(false) }), h('button', { class: 'btn', type: 'button', text: 'New folder', onclick: () => make(true) }),
        h('button', { class: 'btn', type: 'button', text: 'From Documents', onclick: () => putDocument(here) }))));
}
async function keepFile(path) {
  try {
    const f = await api('/api/computer/transfer', { method: 'POST', body: { keep: path } });
    toast('Copied to Documents: ' + f.name);
    openDoc(f.id, { name: f.name });
  } catch (e) { failed(e); }
}
async function putDocument(dir) {
  const body = $('#panel-body');
  let d;
  try { d = await api('/api/files'); } catch (e) { failed(e); return; }
  const rows = h('div', { class: 'rows' });
  for (const f of d.files || []) {
    rows.append(h('button', { class: 'row', type: 'button', onclick: async () => {
      try { const r = await api('/api/computer/transfer', { method: 'POST', body: { put: f.id, path: dir } }); toast('Copied to ' + r.path); } catch (e) { failed(e); }
      showComputer();
    } }, h('span', { class: 'grow' }, h('span', { class: 't', text: f.name }), h('span', { class: 's', text: fmtSize(f.size) }))));
  }
  if (!(d.files || []).length) rows.append(h('div', { class: 'empty', text: 'Nothing is kept in Documents right now.' }));
  body.replaceChildren(h('div', { class: 'card' }, h('h2', null, 'Copy a document here'), rows, h('div', { class: 'btn-row' }, h('button', { class: 'btn', type: 'button', text: 'Cancel', onclick: showComputer }))));
}
async function editFile(path, fresh) {
  const body = $('#panel-body');
  let text = '', cut = false;
  if (!fresh) {
    try { const d = await api('/api/computer/file?path=' + encodeURIComponent(path)); text = d.text; cut = d.truncated; }
    catch (e) { failed(e); return; }
  }
  const area = h('textarea', { class: 'editor mono', 'aria-label': 'Contents of ' + path, spellcheck: 'false', autocapitalize: 'none', autocorrect: 'off' });
  area.value = text;
  const save = h('button', { class: 'btn primary', type: 'button', text: 'Save' });
  save.disabled = cut;
  save.addEventListener('click', async () => {
    save.disabled = true;
    try { await api('/api/computer/file', { method: 'POST', body: { path, text: area.value } }); toast('Saved ' + path); } catch (e) { failed(e); }
    save.disabled = false;
  });
  body.className = 'panel-body editing';
  body.replaceChildren(h('div', { class: 'field-note mono', text: path + (cut ? ' — too long to edit here: only its start is shown, and saving is off so the rest is not lost.' : '') }), area,
    h('div', { class: 'btn-row' }, h('button', { class: 'btn', type: 'button', text: 'Back to files', onclick: showComputer }), save));
}

// ---------- documents ----------
// A kept PDF or picture opens inside the app, in the browser's own viewer: nothing is rendered on the server
// and no viewer is shipped with the page. A PDF form's fields can be filled here into a copy.
async function openDoc(id, f) {
  let meta = f;
  if (!meta || !meta.mime) {
    try { meta = ((await api('/api/files')).files || []).find((x) => x.id === id) || meta; } catch (e) { if (e instanceof AuthError) return; }
  }
  if (!meta || !meta.mime) { toast('That file is no longer kept: it may have expired.'); return; }
  const src = '/api/files/' + id, isPDF = meta.mime === 'application/pdf', isPage = meta.mime === 'text/html';
  const body = openPanel(meta.name || 'Document', (isPDF ? 'PDF' : isPage ? 'Page' : 'Image') + (meta.size ? ' · ' + fmtSize(meta.size) : ''));
  body.className = 'panel-body doc';
  let page = 1, zoom = 100;
  const view = h('div', { class: 'docview' });
  const draw = () => {
    // A page the agent made is shown with every script, form and popup switched off. allow-same-origin is only so
    // the browser will frame it at all: the server's own policy (sandbox, no script, no network) still gives the
    // page an origin of its own, and with no script allowed there is nothing that could use the app's.
    if (isPage) view.replaceChildren(h('iframe', { class: 'docframe', title: meta.name || 'Page', sandbox: 'allow-same-origin', referrerpolicy: 'no-referrer', src: src + '?inline=1' }));
    else if (isPDF) view.replaceChildren(h('iframe', { class: 'docframe', title: meta.name || 'Document', src: src + '?inline=1#page=' + page + '&zoom=' + zoom }));
    else { const im = h('img', { class: 'docimg', alt: meta.name || 'Image', src: src + '?inline=1' }); im.style.width = zoom + '%'; view.replaceChildren(im); }
    label.textContent = isPage ? '' : (isPDF ? 'Page ' + page + ' · ' : '') + zoom + '%';
  };
  const label = h('span', { class: 'doclabel mono' });
  const btn = (name, label, fn) => h('button', { type: 'button', class: 'key', 'aria-label': label, title: label, onclick: () => { fn(); draw(); } }, icon(name));
  const bar = h('div', { class: 'toolbar' },
    isPDF ? btn('left', 'Previous page', () => { page = Math.max(1, page - 1); }) : null,
    isPDF ? btn('right', 'Next page', () => { page++; }) : null,
    isPage ? null : btn('minus', 'Zoom out', () => { zoom = Math.max(25, zoom - 25); }), isPage ? null : btn('plus', 'Zoom in', () => { zoom = Math.min(400, zoom + 25); }), label, h('span', { class: 'grow' }),
    h('a', { class: 'key go', href: src + '?inline=1', target: '_blank', rel: 'noopener', text: 'Open' }),
    h('a', { class: 'key go', href: src, download: '', text: 'Save' }));
  if (navigator.share && window.File) {
    bar.append(h('button', { type: 'button', class: 'key go', text: 'Share', onclick: async () => {
      try {
        const blob = await (await fetch(src, { credentials: 'same-origin' })).blob();
        await navigator.share({ files: [new File([blob], meta.name || 'document', { type: meta.mime })], title: meta.name });
      } catch { /* the share sheet was closed, or this file cannot be shared from here */ }
    } }));
  }
  const form = h('div');
  body.replaceChildren(bar, view, form);
  draw();
  if (!isPDF) return;
  let d;
  try { d = await api(src + '/form'); } catch { return; }
  const fields = (d.fields || []).filter((x) => x.Name);
  if (!fields.length) return;
  const inputs = fields.map((fl) => {
    const box = fl.Type === 'checkbox';
    const input = h('input', box ? { type: 'checkbox' } : { type: fl.Type === 'date' ? 'date' : 'text', autocomplete: 'off' });
    if (box) input.checked = /^(true|yes|on|1)$/i.test(fl.Value || ''); else input.value = fl.Value || '';
    return { fl, input, was: box ? String(input.checked) : input.value, box };
  });
  const save = h('button', { class: 'btn primary', type: 'button', text: 'Save a filled copy' });
  save.addEventListener('click', async () => {
    const values = {};
    for (const it of inputs) { const v = it.box ? String(it.input.checked) : it.input.value; if (v !== it.was) values[it.fl.Name] = v; }
    if (!Object.keys(values).length) { toast('Nothing was changed.'); return; }
    save.disabled = true;
    try { const n = await api(src + '/form', { method: 'POST', body: { values } }); toast('Kept as ' + n.name + '. The original is untouched.'); openDoc(n.id, { name: n.name }); }
    catch (e) { failed(e); save.disabled = false; }
  });
  form.replaceChildren(h('div', { class: 'card' }, h('h2', null, 'Fill in the form', h('small', { text: fields.length + (fields.length === 1 ? ' field' : ' fields') })),
    inputs.map((it) => it.box ? h('label', { class: 'check formcheck' }, it.input, ' ' + it.fl.Name) : h('div', { class: 'field' }, h('span', { class: 'field-label', text: it.fl.Name }), it.input)),
    h('div', { class: 'btn-row' }, save)));
}

// ---------- results shown as what they are ----------
// A tool that knows what its result is says so in a typed record beside the text (m.artifact: a kind and its
// fields), and the card is drawn from that. A result with no record (an older conversation, a tool that gives
// none) is read out of the text instead, by the readers at the end of this section, into the same shape.
function docCard(d) {
  return h('div', { class: 'rich doc' }, h('span', { class: 'tag', text: /image/.test(d.mime || '') ? 'IMG' : d.mime === 'text/html' ? 'PAGE' : 'PDF' }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: d.name || 'Document' }), h('span', { class: 's', text: d.size ? fmtSize(d.size) : 'Kept in Documents' })),
    h('button', { class: 'btn small', type: 'button', text: 'View', onclick: () => openDoc(d.id) }),
    h('a', { class: 'btn small', href: '/api/files/' + d.id, download: '', text: 'Save' }));
}
function mailTriageCard(d) {
  const needs = d.needs || [], wait = d.wait || [];
  if (!needs.length && !wait.length) return null;
  const rows = (items, tint, actions) => {
    const box = h('div', { class: 'rich rows' });
    items.slice(0, 10).forEach((r, i) => {
      const from = r.from || '(unknown)', name = from.replace(/\s*<.*>$/, '') || from;
      const av = h('span', { class: 'avatar', text: (name.trim()[0] || '?').toUpperCase() });
      av.style.background = TINTS[(i + tint) % 4][0]; av.style.color = TINTS[(i + tint) % 4][1];
      box.append(h('div', { class: 'row convo' },
        h('button', { class: 'rowbtn', type: 'button', onclick: () => sendText('Read message ' + r.id + ' and tell me what it says.') }, av,
          h('span', { class: 'grow' }, h('span', { class: 't', text: name + ' · ' + (r.subject || '(no subject)') }), h('span', { class: 's', text: r.why || '' })),
          r.unread ? h('span', { class: 'pill info', text: 'NEW' }) : null),
        actions ? h('span', { class: 'acts' },
          h('button', { class: 'btn small primary', type: 'button', text: 'Start', title: 'Have it deal with this', onclick: () => sendText('Handle message ' + r.id + ': read it, work out what it needs from me, and do what you can (draft a reply, add to my calendar). Ask me before anything is sent.') }),
          h('button', { class: 'btn small', type: 'button', text: 'Reply', onclick: () => sendText('Draft a reply to message ' + r.id + ' and save it in my Drafts. Do not send it.') })) : null));
    });
    if (items.length > 10) box.append(h('div', { class: 'field-note', text: '+' + (items.length - 10) + ' more.' }));
    return box;
  };
  const card = h('div', { class: 'rich triage' });
  card.append(h('div', { class: 'triage-h' }, h('b', { text: 'Needs you' }), h('span', { class: 'pill ' + (needs.length ? 'brass' : 'ok'), text: String(needs.length) })));
  card.append(needs.length ? rows(needs, 0, true) : h('div', { class: 'field-note', text: 'Nothing needs you.' }));
  if (wait.length) {
    const more = h('details', { class: 'triage-wait' }, h('summary', null, 'Can wait ', h('span', { class: 'pill', text: String(wait.length) })), rows(wait, 2, false));
    card.append(more);
  }
  return card;
}
function mailListCard(d) {
  const items = d.items || [];
  if (!items.length) return null;
  const box = h('div', { class: 'rich rows' });
  items.slice(0, 8).forEach((r, i) => {
    const from = r.from || '(unknown)', name = from.replace(/\s*<.*>$/, '') || from;
    const av = h('span', { class: 'avatar', text: (name.trim()[0] || '?').toUpperCase() });
    av.style.background = TINTS[i % 4][0]; av.style.color = TINTS[i % 4][1];
    box.append(h('button', { class: 'row', type: 'button', onclick: () => sendText('Read message ' + r.id + ' and tell me what it says.') }, av,
      h('span', { class: 'grow' }, h('span', { class: 't', text: name }), h('span', { class: 's', text: r.subject || '(no subject)' })),
      h('span', { class: 'when' + (r.unread ? ' state-info' : ''), text: r.when || '' })));
  });
  if (items.length > 8) box.append(h('div', { class: 'field-note', text: '+' + (items.length - 8) + ' more in the full result.' }));
  return box;
}
function mailCard(d) {
  if (!d.from && !d.subject) return null;
  const from = d.from || '(unknown)', name = from.replace(/\s*<.*>$/, '') || from;
  const card = h('div', { class: 'rich mail' }, h('div', { class: 'row' },
    h('span', { class: 'avatar tint', text: (name.trim()[0] || '?').toUpperCase() }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: d.subject || '(no subject)' }), h('span', { class: 's', text: from }))),
  d.excerpt ? h('div', { class: 'mailbody', text: d.excerpt }) : null,
  (d.attachments || []).length ? h('div', { class: 'field-note', text: 'Attached: ' + d.attachments.join(', ') }) : null);
  if (d.id) card.append(h('div', { class: 'btn-row' },
    h('button', { class: 'btn small', type: 'button', text: 'Draft a reply', onclick: () => sendText('Draft a reply to message ' + d.id + ' and show it to me before anything is sent.') }),
    h('button', { class: 'btn small', type: 'button', text: 'Whole thread', onclick: () => sendText('Show me the whole conversation of message ' + d.id + '.') })));
  return card;
}
function pageCard(d) {
  if (!d.url) return null;
  let host = d.url;
  try { host = new URL(d.url).host || d.url; } catch { /* keep what was given */ }
  return h('div', { class: 'rich page' }, h('span', { class: 'tag', text: 'WEB' }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: d.title || host }), h('span', { class: 's mono', text: d.url })),
    h('button', { class: 'btn small', type: 'button', text: 'Take control', onclick: () => openComputer('browser') }));
}
function planCard(d) {
  if (!d.title) return null;
  const steps = d.steps || [], done = steps.filter((s) => s.done).length, status = d.status || 'open';
  const card = h('div', { class: 'rich plan' }, h('div', { class: 'row' }, h('span', { class: 'tag goal', text: 'GOAL' }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: d.title }), h('span', { class: 's', text: steps.length ? done + ' of ' + steps.length + ' steps done' : status })),
    h('span', { class: 'pill ' + (status === 'open' ? 'ok' : 'brass'), text: status.toUpperCase() })));
  if (steps.length) {
    const bar = h('i'); bar.style.width = Math.round((done / steps.length) * 100) + '%';
    card.append(h('div', { class: 'progress' }, bar), h('ul', { class: 'plansteps' }, steps.map((s) => h('li', { class: s.done ? 'done' : '' }, h('span', { class: 'box', 'aria-hidden': 'true' }, s.done ? icon('check') : null), s.text))));
  }
  if (d.next) card.append(h('div', { class: 'field-note', text: 'Next: ' + d.next }));
  return card;
}
function spendingCard(d) {
  if (d.spending == null) return null;
  const cats = d.categories || [];
  const amount = (n) => Number(n).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  const stat = (label, value, cls) => h('div', { class: 'stat' }, h('span', { text: label }), h('b', { class: cls || '', text: amount(value) }));
  const card = h('div', { class: 'rich spend' },
    h('div', { class: 'row' }, h('span', { class: 'tag time', text: '$' }), h('span', { class: 'grow' }, h('span', { class: 't', text: 'Spending summary' }), h('span', { class: 's', text: d.count + ' transactions · ' + d.from + ' to ' + d.to }))),
    h('div', { class: 'stats' }, stat('Spent', d.spending), stat('Income', d.income), stat('Saved', d.saved, Number(d.saved) >= 0 ? 'state-ok' : 'state-bad')));
  const max = Math.max(1, ...cats.map((c) => Number(c.amount) || 0));
  for (const c of cats.slice(0, 8)) {
    const fill = h('i'); fill.style.width = Math.max(2, Math.round(((Number(c.amount) || 0) / max) * 100)) + '%';
    card.append(h('div', { class: 'cat' }, h('span', { class: 'catname', text: c.name }), h('div', { class: 'progress' }, fill), h('span', { class: 'mono catamt', text: amount(c.amount) })));
  }
  const goal = h('input', { type: 'number', min: '1', step: '1', inputmode: 'decimal', 'aria-label': 'Amount to save each month', placeholder: 'Save each month' });
  card.append(h('div', { class: 'toolbar' }, goal, h('button', { class: 'key go', type: 'button', text: 'Set a savings goal', onclick: () => {
    const v = Number(goal.value);
    if (!(v > 0)) { toast('Say how much to save each month.'); return; }
    sendText('Set up a goal to save ' + v + ' a month, using the spending summary above: say which categories to cut and by how much, and check in on it.');
  } })));
  return card;
}
const CARDS = { mail_triage: mailTriageCard, mail_list: mailListCard, mail: mailCard, page: pageCard, plan: planCard, spending: spendingCard, document: (d) => (d.id ? docCard(d) : null) };

// The readers: the same records, taken from a result's text when no record came with it.
function readArtifact(tool, text) {
  let m;
  if (tool === 'email_list') {
    const items = [];
    for (const line of text.split('\n')) {
      if ((m = /^id (\d+) ([* ]) (.+?) {2}From: (.*?) {2}Subject: (.*)$/.exec(line))) items.push({ id: m[1], unread: m[2] === '*', when: m[3], from: m[4], subject: m[5] });
    }
    return { kind: 'mail_list', items };
  }
  if (tool === 'email_read') {
    const from = /^From: (.*)$/m.exec(text), subject = /^Subject: (.*)$/m.exec(text), id = /^Message (\d+)$/m.exec(text);
    const start = text.indexOf('---\n'), end = text.indexOf('\n--- end of message ---');
    let excerpt = start >= 0 && end > start ? text.slice(start + 4, end).trim() : '';
    if (excerpt.length > 420) excerpt = excerpt.slice(0, 420) + '…';
    return { kind: 'mail', id: id && id[1], from: from && from[1], subject: subject && subject[1], excerpt };
  }
  if (/^browser_(open|snapshot|click|type|press|select|scroll|back|wait)$/.test(tool)) {
    const title = /^Page: (.*)$/m.exec(text), url = /^URL: (.*)$/m.exec(text);
    return { kind: 'page', title: title && title[1], url: url && url[1] };
  }
  if (/^goal_(create|get|update)$/.test(tool)) {
    if (!(m = /^(?:updated\.\n)?Goal (\S+): (.*) \[(\w+)\]$/m.exec(text))) return null;
    const next = /^Next: (.*)$/m.exec(text);
    return { kind: 'plan', id: m[1], title: m[2], status: m[3], next: next && next[1], steps: [...text.matchAll(/^ {2}\d+\. \[([ x])\] (.*)$/gm)].map((x) => ({ done: x[1] === 'x', text: x[2] })) };
  }
  if (tool === 'finance_import') {
    if (!(m = /^Imported (\d+) transactions \((.*?) to (.*?)\)\.\nSpending ([\d.]+), income ([\d.]+), saved (-?[\d.]+)\./.exec(text))) return null;
    const categories = [...text.slice(m[0].length).matchAll(/([^.\d][^.]*?) (\d+\.\d\d)\.(?: |$)/g)].map((x) => ({ name: x[1].trim(), amount: Number(x[2]) })).filter((c) => !/^\+\d+ more/.test(c.name));
    return { kind: 'spending', count: m[1], from: m[2], to: m[3], spending: m[4], income: m[5], saved: m[6], categories };
  }
  if (/^(files_make_pdf|pdf_form_fill|email_attachment)$/.test(tool)) {
    if (!(m = /\/api\/files\/([0-9a-fA-F]+)/.exec(text) || /kept the copy as file ([0-9a-fA-F]+)/.exec(text) || /\bfile ([0-9a-f]{8,64})\b/.exec(text))) return null;
    return { kind: 'document', id: m[1], name: tool === 'pdf_form_fill' ? 'Filled form' : tool === 'email_attachment' ? 'Attachment' : 'Document' };
  }
  return null;
}
function richResult(t) {
  try {
    const d = t.artifact && t.artifact.kind ? t.artifact : readArtifact(t.tool, t.result || '');
    const draw = d && CARDS[d.kind];
    return draw ? draw(d) : null;
  } catch { return null; } // a record that does not fit is shown as text, as before
}

// ---------- conversations ----------
function conversationsCard(chats, archived) {
  const rows = h('div', { class: 'rows' });
  const reload = () => openSheet();
  const change = async (body, done) => {
    try { await api('/api/chats', { method: 'POST', body }); if (done) toast(done); } catch (e) { failed(e); }
    reload();
  };
  if (!archived) {
    rows.append(h('div', { class: 'btn-row sheet-new' },
      h('button', { class: 'btn', type: 'button', text: '+ New chat', onclick: newConversation }),
      h('button', { class: 'btn', type: 'button', text: '+ Side chat', title: 'A blank conversation beside this one, for a tangent', onclick: sideConversation }),
      h('button', { class: 'btn', type: 'button', text: '+ Channel', title: 'A conversation kept for one piece of work', onclick: channelForm })));
  }
  const ordered = chats.slice().sort((a, b) => (b.main ? 1 : 0) - (a.main ? 1 : 0)); // the main one first; the rest stay newest first
  for (const c of ordered) {
    const sub = [c.main ? 'Main conversation' : c.side ? 'Side chat of ' + c.side : c.purpose || '', (c.messages || 0) + (c.messages === 1 ? ' message' : ' messages')].filter(Boolean).join(' · ');
    const acts = h('span', { class: 'acts' });
    acts.append(h('button', { class: 'btn small', type: 'button', text: 'Rename', onclick: () => {
      const title = prompt('Name this conversation', c.title || '');
      if (title && title.trim()) change({ rename: c.id, title: title.trim() });
    } }));
    const del = () => {
      if (!confirm('Delete "' + (c.title || 'New chat') + '"? This cannot be undone.')) return;
      // Deleting the open one drops back to the main conversation, so the page has to reload it.
      change({ delete: c.id }, 'Deleted.').then(() => { if (c.active) { closeSheet(); loadState().then(() => { S.pinned = true; toBottom(); }); } });
    };
    if (archived) {
      acts.append(h('button', { class: 'btn small', type: 'button', text: 'Restore', onclick: () => change({ unarchive: c.id }, 'Restored.') }));
      acts.append(h('button', { class: 'btn small danger', type: 'button', text: 'Delete', onclick: del }));
    } else if (!c.main) {
      acts.append(h('button', { class: 'btn small', type: 'button', text: 'Archive', onclick: () => change({ archive: c.id }, 'Archived. Nothing was deleted.') }));
      acts.append(h('button', { class: 'btn small', type: 'button', text: 'Make main', onclick: () => change({ main: c.id }) }));
      acts.append(h('button', { class: 'btn small danger', type: 'button', text: 'Delete', onclick: del }));
    }
    rows.append(h('div', { class: 'row convo', 'aria-current': c.active ? 'true' : null },
      h('button', { class: 'rowbtn', type: 'button', onclick: () => (archived ? change({ unarchive: c.id }).then(() => openConversation(c.id)) : openConversation(c.id)) },
        c.main ? h('span', { class: 'pill brass', text: 'MAIN' }) : c.side ? h('span', { class: 'pill info', text: 'SIDE' }) : c.color ? h('span', { class: 'chan-ico chan ' + c.color, text: c.icon || (c.title || '?').trim()[0].toUpperCase() }) : null,
        h('span', { class: 'grow' }, h('span', { class: 't', text: c.title || 'New chat' }), h('span', { class: 's', text: sub })),
        h('span', { class: 'when', text: c.updated_at ? fmtWhen(new Date(c.updated_at)) : '' })), acts));
  }
  if (!chats.length) rows.append(h('div', { class: 'empty', text: archived ? 'Nothing is archived.' : 'No conversations yet.' }));
  const foot = h('div', { class: 'btn-row' },
    h('button', { class: 'btn small', type: 'button', text: archived ? 'Back to conversations' : 'Archived', onclick: () => { sheetArchived = !archived; reload(); } }),
    !archived && S.order.length > 1 ? h('button', { class: 'btn small', type: 'button', text: 'Replay this one', onclick: replayConversation }) : null);
  return h('div', { class: 'card' }, h('h2', null, archived ? 'Archived' : 'Conversations'), rows, foot);
}
async function sideConversation() {
  closeSheet();
  try { await api('/api/chats', { method: 'POST', body: { side: true } }); } catch (e) { failed(e); return; }
  await loadState();
  S.pinned = true; toBottom();
  toast('A side chat. The conversation you came from is kept as it was.');
}
// Replay shows the open conversation again from its start, a step at a time. It is the page revealing what it
// already holds: nothing is sent, and nothing runs again.
let replaying = 0;
function replayConversation() {
  closeSheet();
  const els = [...$('#thread').children].filter((el) => el.dataset.key);
  if (els.length < 2) return;
  const run = ++replaying;
  els.forEach((el) => el.classList.add('unplayed'));
  S.pinned = true;
  let i = 0;
  const step = () => {
    if (run !== replaying || i >= els.length) { els.forEach((el) => el.classList.remove('unplayed')); return; }
    els[i].classList.remove('unplayed'); i++;
    toBottom();
    setTimeout(step, 450);
  };
  step();
}

// ---------- finished tasks ----------
// ---------- you: profile and notes ----------
async function saveMe(body, done) {
  try { S.me = await api('/api/profile', { method: 'POST', body }); if (done) toast(done); }
  catch (e) { failed(e); return false; }
  if (body.profile) { S.profile = { name: S.me.profile.name || '', avatar: S.me.profile.avatar || '', theme: S.me.profile.theme || '', onboarded: !!S.me.profile.onboarded }; applyTheme(S.profile.theme); S.blocks.clear(); $('#thread').replaceChildren(); renderChat(); }
  renderSettings();
  return true;
}
// Light is how the app looks unless you choose otherwise. The choice is kept on the server with your profile,
// not on the phone, so it follows you to every device you sign in on.
const THEME_COLOR = { light: '#F6F3EC', dark: '#100E0A' };
function applyTheme(theme) {
  const t = theme === 'dark' || theme === 'auto' ? theme : 'light';
  const root = document.documentElement;
  if (t === 'light') root.removeAttribute('data-theme'); else root.setAttribute('data-theme', t);
  const dark = t === 'dark' || (t === 'auto' && window.matchMedia('(prefers-color-scheme: dark)').matches);
  const bar = document.querySelector('meta[name="theme-color"]');
  if (bar) bar.setAttribute('content', dark ? THEME_COLOR.dark : THEME_COLOR.light);
}
window.matchMedia('(prefers-color-scheme: dark)').addEventListener?.('change', () => applyTheme(document.documentElement.getAttribute('data-theme') || 'light'));
function themeCard(p) {
  const cur = p.theme === 'dark' || p.theme === 'auto' ? p.theme : 'light';
  const opt = (id, title, text) => h('label', { class: 'opt' + (cur === id ? ' on' : '') },
    h('input', { type: 'radio', name: 'theme', value: id, checked: cur === id, onchange: () => saveMe({ profile: { name: p.name || '', avatar: p.avatar || '', tone: p.tone || '', theme: id } }) }),
    h('span', { class: 'grow' }, h('b', { text: title }), h('span', { class: 's', text })));
  return h('div', { class: 'card' }, h('h2', null, 'Appearance'),
    opt('light', 'Light', 'Warm paper and ink. The default.'),
    opt('dark', 'Dark', 'Always dark, whatever the device is set to.'),
    opt('auto', 'Match my device', 'Light by day and dark by night, if that is how your phone is set.'));
}
const TONES = ['Warm and brief', 'Plain and direct', 'Formal', 'Playful', 'Detailed, with reasons'];
function notesCard(target, title, hint, view) {
  const card = h('div', { class: 'card' }, h('h2', null, title, h('small', { text: view.used + ' of ' + view.limit + ' characters' })), h('div', { class: 'field-note', text: hint }));
  view.entries.forEach((text, index) => {
    const area = h('textarea', { class: 'notearea', rows: '2', 'aria-label': 'Note ' + (index + 1) });
    area.value = text;
    card.append(h('div', { class: 'noteitem' }, area, h('div', { class: 'btn-row' },
      h('button', { class: 'btn small', type: 'button', text: 'Save', onclick: () => { if (area.value.trim() && area.value.trim() !== text) saveMe({ note: { target, index, old: text, text: area.value } }, 'Saved.'); } }),
      h('button', { class: 'btn small danger', type: 'button', text: 'Forget', onclick: () => saveMe({ note: { target, index, old: text, text: '' } }, 'Forgotten.') }))));
  });
  if (!view.entries.length) card.append(h('div', { class: 'empty', text: 'Nothing yet.' }));
  const fresh = h('textarea', { class: 'notearea', rows: '2', 'aria-label': 'A new note', placeholder: target === 'user' ? 'Something it should know about you' : 'Something it should remember' });
  card.append(h('div', { class: 'noteitem' }, fresh, h('div', { class: 'btn-row' },
    h('button', { class: 'btn small primary', type: 'button', text: 'Add', onclick: async () => { if (fresh.value.trim() && await saveMe({ note: { target, text: fresh.value } }, 'Added. It is used from the next conversation.')) fresh.value = ''; } }))));
  return card;
}
function paneYou() {
  const me = S.me;
  if (!me) return [h('div', { class: 'card empty', text: 'Your profile is not available here.' })];
  const p = me.profile || {};
  const name = h('input', { id: 'me-name', type: 'text', autocomplete: 'off', maxlength: '60', placeholder: 'What should it call you?' }); name.value = p.name || '';
  const avatar = h('input', { id: 'me-avatar', type: 'text', autocomplete: 'off', maxlength: '8', placeholder: 'An emoji or your initials' }); avatar.value = p.avatar || '';
  const tone = h('input', { id: 'me-tone', type: 'text', autocomplete: 'off', maxlength: '200', placeholder: 'How should replies sound?' }); tone.value = p.tone || '';
  const presets = h('div', { class: 'presets' }, TONES.map((t) => h('button', { type: 'button', class: p.tone === t ? 'on' : '', text: t, onclick: () => { tone.value = t; } })));
  return [
    h('div', { class: 'card' }, h('h2', null, 'About you'),
      h('div', { class: 'field' }, h('label', { class: 'field-label', for: 'me-name', text: 'Name' }), name),
      h('div', { class: 'field' }, h('label', { class: 'field-label', for: 'me-avatar', text: 'Avatar, shown beside what you write' }), avatar),
      h('div', { class: 'field' }, h('label', { class: 'field-label', for: 'me-tone', text: 'Tone' }), tone), presets,
      h('div', { class: 'field-note', text: 'The name and tone are given to the agent with its notes, from the next conversation on.' }),
      h('div', { class: 'btn-row' }, h('button', { class: 'btn primary', type: 'button', text: 'Save', onclick: () => saveMe({ profile: { name: name.value, avatar: avatar.value, tone: tone.value, theme: p.theme || '' } }, 'Saved.') }))),
    briefCard(),
    themeCard(p),
    notesCard('user', 'What it knows about you', 'The agent writes these as it learns; you can correct or remove any of them.', me.user),
    notesCard('memory', 'Its own notes', 'What it has noted about its work and this setup.', me.memory),
  ];
}

// ---------- notifications kept in the app ----------
function noticeCards() {
  const n = S.notices;
  if (!n) return [];
  const prefs = n.prefs || { approvals: true, replies: true };
  const set = async (next) => { try { S.notices = await api('/api/notices', { method: 'POST', body: next }); } catch (e) { failed(e); } renderSettings(); };
  const sw = (label, sub, key) => h('label', { class: 'opt' + (prefs[key] ? ' on' : '') },
    h('input', { type: 'checkbox', checked: prefs[key], onchange: (e) => set({ prefs: { ...prefs, [key]: e.target.checked } }) }),
    h('span', { class: 'grow' }, h('b', { text: label }), h('span', { class: 's', text: sub })));
  const kinds = h('div', { class: 'card' }, h('h2', null, 'What reaches your phone'),
    sw('When something needs your OK', 'A question that is waiting for your answer.', 'approvals'),
    sw('Replies and finished work', 'An answer, a reminder, or a background task that ended while the app was closed.', 'replies'),
    h('div', { class: 'field-note', text: 'A kind you turn off still appears in the list below: it only stops buzzing.' }));
  // Quiet hours: replies and finished work are kept in the list and not pushed; a request for you always is.
  const qf = h('input', { id: 'quiet-from', type: 'time', value: prefs.quiet_from || '22:00', step: '300' });
  const qt = h('input', { id: 'quiet-to', type: 'time', value: prefs.quiet_to || '07:00', step: '300' });
  const qon = h('input', { type: 'checkbox', checked: !!(prefs.quiet_from && prefs.quiet_to) });
  const quiet = h('div', { class: 'card' }, h('h2', null, 'Quiet hours'),
    h('label', { class: 'opt' + (qon.checked ? ' on' : '') }, qon, h('span', { class: 'grow' }, h('b', { text: 'Keep replies quiet at night' }),
      h('span', { class: 's', text: 'Replies, reminders and finished work wait in the list below instead of buzzing. Anything that needs your OK or your hands still comes through.' }))),
    h('div', { class: 'field' }, h('label', { class: 'field-label', for: 'quiet-from', text: 'From' }), qf),
    h('div', { class: 'field' }, h('label', { class: 'field-label', for: 'quiet-to', text: 'Until' }), qt),
    h('div', { class: 'btn-row' }, h('button', { class: 'btn primary', type: 'button', text: 'Save', onclick: () => set({ prefs: { ...prefs, quiet_from: qon.checked ? qf.value : '', quiet_to: qon.checked ? qt.value : '' } }) })));
  const list = h('div', { class: 'card' }, h('h2', null, 'Lately', h('small', { text: (n.items || []).length ? 'the last ' + n.items.length : '' })));
  if (!(n.items || []).length) list.append(h('div', { class: 'empty', text: 'Notifications are listed here, so one you swiped away is not lost.' }));
  else {
    const rows = h('div', { class: 'rows' });
    for (const it of n.items.slice(0, 20)) {
      rows.append(h('a', { class: 'row', href: it.tag === 'approval' ? '#agents' : '#chat' }, h('span', { class: 'mark ' + (it.tag === 'approval' ? 'quiet' : 'ok') }, icon(it.tag === 'approval' ? 'shield' : 'bell')),
        h('span', { class: 'grow' }, h('span', { class: 't', text: it.title }), h('span', { class: 's', text: it.body || '' })), h('span', { class: 'when', text: ago(it.at) })));
    }
    list.append(rows, h('div', { class: 'btn-row' }, h('button', { class: 'btn small', type: 'button', text: 'Clear the list', onclick: () => set({ clear: true }) })));
  }
  return [kinds, quiet, list];
}


// ---------- setup, the morning brief and the first day ----------
// The checklist is read from what Settings already knows, plus a live check of the mailbox and calendars when
// asked: "Ready" for those means it answered just now, not only that a setting is saved.
function setupItems() {
  const s = S.settings || {}, checks = S.checks || {}, out = [];
  const add = (id, title, state, detail, go, optional) => out.push({ id, title, state, detail, go, optional });
  const profiles = s.profiles || [];
  const usable = profiles.filter((p) => p.available);
  const cur = usable.find((p) => p.active) || usable.find((p) => p.default) || usable[0];
  if (usable.length || s.has_default_key) add('model', 'A model that answers', 'ready', 'Using ' + ((cur && cur.model) || S.model || 'a model') + '.', 'models');
  else if (S.model) add('model', 'A model that answers', 'unchecked', 'Using ' + S.model + '. Its key comes from the server environment, which this page cannot see.', 'models');
  else add('model', 'A model that answers', 'todo', 'No model is set up yet.', 'models');
  const em = s.email || {}, ec = checks.email;
  add('email', 'Your email', ec ? (ec.ok ? 'ready' : 'todo') : em.available ? 'unchecked' : 'todo', ec ? ec.detail : em.available ? (em.status || 'Set up. Press Check again to try it.') : 'Not connected: it cannot read your mail, sort it or write the brief from it.', 'connections');
  // A calendar may be set in Settings (listed in s.calendars) or by the server's environment (TALARIA_CALENDARS), which
  // Settings cannot see: the server's own list of connections (ctl.links) names those too, with the calendars it reads.
  const own = (s.calendars || []).filter((x) => !x.builtin), cc = checks.calendar;
  const linked = ((typeof ctl !== 'undefined' && ctl && ctl.links) || []).some((l) => l.name === 'Calendar' && l.detail);
  const have = own.length > 0 || linked;
  add('calendar', 'Your calendar', cc ? (cc.ok ? 'ready' : 'todo') : have ? 'unchecked' : 'todo', cc ? cc.detail : have ? 'Set up. Press Check again to try it.' : 'Only its own calendar is there. Add yours to see your day in the brief.', 'connections');
  const brief = S.brief || {};
  if (brief.available) add('brief', 'Morning brief', brief.enabled ? 'ready' : 'todo', brief.enabled ? 'Arrives at ' + brief.at + ' every day.' : 'Off. Turn it on under You to get one message each morning.', 'you');
  add('voice', 'Voice notes', S.voice && S.voice.available ? 'ready' : 'todo', S.voice && S.voice.available ? 'Voice notes are typed out by ' + S.voice.model + '.' : 'Optional: record a note and have it typed out. Until a service is set up the button uses the browser’s own dictation, where there is one.', 'voice', true);
  add('push', 'Notifications on this device', S.push === 'on' ? 'ready' : S.push === 'unsupported' ? 'unchecked' : 'todo', S.push === 'on' ? 'On.' : S.push === 'needs-install' ? 'Add Talaria to your Home Screen first.' : S.push === 'denied' ? 'Blocked in the browser.' : 'Off: it cannot reach you when the app is closed.', 'notifications', true);
  const tg = (s.chats || []).find((c) => c.service === 'telegram');
  add('telegram', 'Telegram', tg && tg.connected ? 'ready' : 'todo', tg && tg.connected ? 'Connected.' : 'Not connected. Optional: lets it message you away from the app.', 'connections', true);
  return out;
}
const SETUP_LABEL = { ready: 'READY', unchecked: 'NOT CHECKED', todo: 'NEEDS SETUP' };
function paneSetup() {
  const items = setupItems();
  const open = items.filter((x) => !x.optional && x.state !== 'ready').length;
  const recheck = h('button', { class: 'btn', type: 'button', text: 'Check again' });
  recheck.addEventListener('click', async () => {
    recheck.disabled = true; recheck.textContent = 'Checking…';
    try { S.checks = await api('/api/setup/check', { method: 'POST', body: {} }); await loadSettings(); }
    catch (e) { failed(e); }
    renderSettings();
  });
  const rows = h('div', { class: 'rows' });
  for (const it of items) {
    rows.append(h('a', { class: 'row setup-row', href: '#settings/' + it.go },
      h('span', { class: 'pill ' + (it.state === 'ready' ? 'ok' : it.state === 'todo' ? 'bad' : 'info'), text: SETUP_LABEL[it.state] }),
      h('span', { class: 'grow' }, h('span', { class: 't', text: it.title + (it.optional ? ' (optional)' : '') }), h('span', { class: 's', text: it.detail })), icon('chev')));
  }
  return [h('div', { class: 'card' }, h('h2', null, 'What it depends on'),
    h('div', { class: 'field-note', text: open ? open + (open === 1 ? ' thing is' : ' things are') + ' not ready yet.' : 'Everything it needs is ready.' }), rows,
    h('div', { class: 'btn-row' }, recheck)),
    h('div', { class: 'field-note', text: 'Check again signs in to your mailbox and reads your calendars just now. It does not send a message to the model, so it costs nothing.' })];
}
function briefCard() {
  const b = S.brief;
  if (!b || !b.available) return null;
  const at = h('input', { id: 'brief-at', type: 'time', value: b.at || '07:30', step: '300' });
  const on = h('input', { type: 'checkbox', checked: !!b.enabled });
  const save = async () => {
    try { S.brief = await api('/api/brief', { method: 'POST', body: { enabled: on.checked, at: at.value || '07:30' } }); toast(S.brief.enabled ? 'The brief will arrive at ' + S.brief.at + '.' : 'The morning brief is off.'); }
    catch (e) { failed(e); }
    renderSettings();
  };
  const next = b.enabled && b.next ? 'Next: ' + fmtWhen(new Date(b.next)) + (b.status && b.status !== 'ok' ? ' · last run: ' + b.status : '') : '';
  return h('div', { class: 'card' }, h('h2', null, 'Morning brief'),
    h('label', { class: 'opt' + (b.enabled ? ' on' : '') }, on, h('span', { class: 'grow' }, h('b', { text: 'Send me a brief each morning' }),
      h('span', { class: 's', text: 'What is on today, what needs you in your mail and why, what can wait, and anything it is tracking. It is written fresh each morning from your calendar and mail.' }))),
    h('div', { class: 'field' }, h('label', { class: 'field-label', for: 'brief-at', text: 'Time, in your time zone' }), at),
    next ? h('div', { class: 'field-note', text: next }) : null,
    h('div', { class: 'btn-row' }, h('button', { class: 'btn primary', type: 'button', text: 'Save', onclick: save }), h('button', { class: 'btn', type: 'button', text: 'Brief me now', onclick: () => { location.hash = '#chat'; sendText('Give me my morning brief'); } })));
}
// The first day: until it has been done or waved away, an empty chat offers to set up and to be introduced.
const ONBOARD_PROMPT = `This is my first time here. Introduce yourself in three short paragraphs, plain text:
1. Who I seem to be, from my recent mail and this week's calendar if they are connected. If they are not, say what you cannot see rather than guessing, and where to connect them (Settings, then Connections).
2. Three concrete things you can do for me, picked from what you can actually reach.
3. Ask me to correct anything that is off.
Then call choice_present with the options "Sounds right" and "Something's off". Do not claim to know anything you did not read.`;
async function markOnboarded() {
  try { S.me = await api('/api/profile', { method: 'POST', body: { onboarded: true } }); } catch (e) { if (e instanceof AuthError) return; }
  S.profile = { ...S.profile, onboarded: true };
  renderWelcome();
}
function renderWelcome() {
  const el = $('#welcome'); if (!el) return;
  const show = S.profile && S.profile.onboarded === false && S.order.length === 0 && !working() && S.tab === 'chat';
  if (el.hidden === !show && (!show || el.firstChild)) return;
  el.hidden = !show;
  if (!show) { el.replaceChildren(); return; }
  el.replaceChildren(h('div', { class: 'card welcome-card' }, h('h2', null, 'Welcome' + (S.profile.name ? ', ' + S.profile.name : '')),
    h('p', { class: 'field-note', text: 'It can read your mail and calendar, keep working on a computer of its own, and ask before it sends, pays or deletes anything. Two ways to begin:' }),
    h('div', { class: 'btn-row' },
      h('button', { class: 'btn primary', type: 'button', text: 'Introduce yourself', onclick: async () => { await markOnboarded(); sendText(ONBOARD_PROMPT); } }),
      h('button', { class: 'btn', type: 'button', text: 'Set things up', onclick: async () => { await markOnboarded(); location.hash = '#settings/setup'; } }),
      h('button', { class: 'btn small', type: 'button', text: 'Not now', onclick: markOnboarded }))));
}

// ---------- make: what it made ----------
// A gallery of the documents, pages, slide decks and dashboards it has made (and PDFs). Each tile is drawn from
// its kind alone, so nothing is rendered until you open one: a page of its own is never loaded just to be a thumbnail.
const MAKE_KINDS = [['all', 'All'], ['doc', 'Docs'], ['page', 'Pages'], ['slides', 'Slides'], ['dashboard', 'Dashboards'], ['pdf', 'PDFs']];
const MAKE_NAME = { doc: 'Document', page: 'Web page', slides: 'Slides', dashboard: 'Dashboard', pdf: 'PDF' };
const MAKE_ASK = { doc: 'Make a document about ', page: 'Make a web page for ', slides: 'Make a slide deck about ', dashboard: 'Make a dashboard of ' };
// Tile art is built with DOM calls from path lists (no markup is parsed): each entry is a path, or a rectangle as [x, y, w, h, r].
const MAKE_ART = {
  doc: ['M12 6h17l8 8v28H12z', 'M29 6v8h8M18 22h14M18 28h14M18 34h9'],
  page: [[5, 9, 38, 30, 4], 'M5 17h38M11 13h.01M16 13h.01M12 25h14M12 31h22'],
  slides: [[5, 9, 38, 26, 3], 'M24 35v6M16 41h16M13 18h14M13 24h22M13 29h9'],
  dashboard: [[6, 6, 36, 36, 4], 'M14 34V26M22 34V16M30 34V22'],
  pdf: ['M12 6h17l8 8v28H12z', 'M29 6v8h8', 'M17 31h14M17 36h8'],
};
function artEl(kind) {
  const NS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(NS, 'svg');
  for (const [k, v] of Object.entries({ viewBox: '0 0 48 48', fill: 'none', stroke: 'currentColor', 'stroke-width': '2.2', 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true' })) svg.setAttribute(k, v);
  for (const shape of MAKE_ART[kind] || MAKE_ART.doc) {
    const el = document.createElementNS(NS, Array.isArray(shape) ? 'rect' : 'path');
    if (Array.isArray(shape)) for (const [k, v] of [['x', shape[0]], ['y', shape[1]], ['width', shape[2]], ['height', shape[3]], ['rx', shape[4]]]) el.setAttribute(k, String(v));
    else el.setAttribute('d', shape);
    svg.append(el);
  }
  return svg;
}
function makeKindOf(f) { return f.kind || (f.mime === 'application/pdf' ? 'pdf' : ''); }
function makeStarters() {
  return h('div', { class: 'make-start' }, Object.keys(MAKE_ASK).map((k) => h('button', { type: 'button', onclick: () => {
    location.hash = '#chat';
    const box = $('#input'); box.value = MAKE_ASK[k]; autosize(); renderChrome(); box.focus(); box.setSelectionRange(box.value.length, box.value.length);
  } }, artEl(k), MAKE_NAME[k])));
}
async function loadMake() {
  const body = $('#make-body');
  if (!S.made) body.replaceChildren(h('div', { class: 'skeleton' }));
  try {
    const d = await api('/api/files');
    S.made = { files: (d.files || []).filter((f) => f.origin === 'generated' && makeKindOf(f)), hours: d.retention_hours || 12 };
  } catch (e) { if (e instanceof AuthError) return; if (!S.made) { body.replaceChildren(h('div', { class: 'card empty', text: e.message })); return; } toast(e.message); }
  renderMake();
}
function renderMake() {
  const body = $('#make-body'); if (!body || !S.made || S.tab !== 'make') return;
  const filter = S.makeFilter || 'all';
  const all = S.made.files, list = all.filter((f) => filter === 'all' || makeKindOf(f) === filter);
  const chips = h('div', { class: 'chips', role: 'group', 'aria-label': 'Kind' }, MAKE_KINDS.filter(([k]) => k === 'all' || all.some((f) => makeKindOf(f) === k) || k === filter).map(([k, label]) =>
    h('button', { type: 'button', 'aria-pressed': k === filter ? 'true' : 'false', text: label, onclick: () => { S.makeFilter = k; renderMake(); } })));
  const left = (at) => {
    const m = Math.floor((new Date(at).getTime() + S.made.hours * 3600000 - Date.now()) / 60000);
    return m <= 0 ? 'expiring' : m < 60 ? 'expires in ' + m + 'm' : m < 2880 ? 'expires in ' + Math.floor(m / 60) + 'h' : 'expires in ' + Math.floor(m / 1440) + 'd';
  };
  const out = [h('div', { class: 'card' }, h('h2', null, 'Start something'), makeStarters())];
  if (!all.length) out.push(h('div', { class: 'card empty', text: 'Nothing made yet. Ask in chat for a document, a web page, a slide deck or a dashboard, or tap one above.' }));
  else {
    out.push(chips);
    const grid = h('div', { class: 'make-grid' });
    for (const f of list) {
      const k = makeKindOf(f);
      grid.append(h('button', { class: 'make-card k-' + k, type: 'button', onclick: () => openDoc(f.id, f) },
        h('span', { class: 'make-art' }, artEl(k)),
        h('span', { class: 't', text: f.name.replace(/\.(html|pdf)$/i, '') }),
        h('span', { class: 's', text: MAKE_NAME[k] + ' · ' + fmtWhen(new Date(f.created)) + ' · ' + left(f.created) })));
    }
    out.push(list.length ? grid : h('div', { class: 'card empty', text: 'None of that kind yet.' }));
    out.push(h('div', { class: 'field-note', text: 'Made files are kept for ' + S.made.hours + ' hours, then deleted: open one and Save it to keep it.' }));
  }
  keepScroll(body, () => body.replaceChildren(...out));
}

// ---------- tasks: what needs you, what is working, what is ready ----------
// "Needs input" holds what waits on an answer (a request for your OK, or a task that failed); "Working" is what is
// running; "Ready for review" is finished work you have not looked at yet, checked off with Done and then filed
// under Resolved in History. Up next is what is set to run on its own.
function reviewTask(t, done) {
  return async () => {
    try { const d = await api('/api/tasks', { method: 'POST', body: { review: t.id } }); doneTasks = (d.tasks || []); if (done) toast(done); renderAgentsPage(); }
    catch (e) { failed(e); }
  };
}
function runAgain(t, label) {
  const b = h('button', { class: 'btn small', type: 'button', text: label });
  b.addEventListener('click', async () => {
    b.disabled = true;
    try { const d = await api('/api/tasks', { method: 'POST', body: { retry: t.id } }); doneTasks = d.tasks || []; toast('Started again: ' + t.name); renderAgentsPage(); }
    catch (e) { failed(e); b.disabled = false; }
  });
  return b;
}
function taskRow(t, extra) {
  // With a record kept, the name opens what the task did.
  const text = [h('span', { class: 't', text: t.name }), h('span', { class: 's', text: t.failed ? t.status.replace(/^error: /, 'Failed: ') : 'Finished ' + ago(t.at) })];
  return h('div', { class: 'row task' }, h('span', { class: 'mark ' + (t.failed ? 'bad' : 'ok') }, icon(t.failed ? 'x' : 'check')),
    RUNS.on ? h('button', { type: 'button', class: 'grow rowlink', title: 'See what it did', onclick: () => openRun(t.id) }, text) : h('span', { class: 'grow' }, text),
    h('span', { class: 'acts keep' }, extra));
}
function failedTasksRows() {
  return doneTasks.filter((t) => t.failed && !t.reviewed).slice(0, 10).map((t) => taskRow(t, [runAgain(t, 'Retry'), h('button', { class: 'btn small', type: 'button', text: 'Dismiss', onclick: reviewTask(t) })]));
}
function readyCard() {
  const ready = doneTasks.filter((t) => !t.failed && !t.reviewed).slice(0, 10);
  const card = h('div', { class: 'card' }, h('h2', null, 'Ready for review', h('small', { text: ready.length ? ready.length + ' finished' : '' })));
  if (!ready.length) { card.append(h('div', { class: 'empty', text: 'Finished background work waits here until you check it off.' })); return card; }
  const rows = h('div', { class: 'rows' });
  for (const t of ready) rows.append(taskRow(t, [runAgain(t, 'Run again'), h('button', { class: 'btn small primary', type: 'button', text: 'Done', onclick: reviewTask(t, 'Filed under Resolved.') })]));
  card.append(rows);
  return card;
}
function resolvedCard() {
  const done = doneTasks.filter((t) => t.reviewed).slice(0, 15);
  if (!done.length) return null;
  const rows = h('div', { class: 'rows' });
  for (const t of done) rows.append(taskRow(t, [runAgain(t, t.failed ? 'Retry' : 'Run again')]));
  const d = h('details', { class: 'card fold' }, h('summary', null, h('span', { class: 'display', text: 'Resolved' }), h('small', { text: String(done.length) })), rows);
  return d;
}
function needsInputCard() {
  const pend = pendingAsks(), bad = failedTasksRows(), hand = pendingHandoffs();
  const n = pend.length + bad.length + hand.length;
  const card = h('div', { class: 'card wait' + (n ? ' has' : '') }, h('h2', null, 'Needs input', h('small', { text: n ? n + ' to look at' : '' })));
  if (!n) { card.append(h('div', { class: 'empty', text: 'Nothing is waiting for you.' }), h('div', { class: 'note center', text: ASK_POLICY[ctl && ctl.permissions] || ASK_POLICY.deletes_and_posts })); return card; }
  if (pend.length) card.append(h('div', { class: 'note', text: 'These are saved. Answer whenever you like: the work carries on from where it stopped.' }));
  hand.forEach((m) => card.append(handoffCard(m)));
  pend.forEach((m) => card.append(askCard(m)));
  if (bad.length) card.append(h('div', { class: 'rows' }, bad));
  return card;
}
function tasksCounts() {
  const needs = pendingAsks().length + pendingHandoffs().length + doneTasks.filter((t) => t.failed && !t.reviewed).length;
  const ready = doneTasks.filter((t) => !t.failed && !t.reviewed).length;
  return { active: needs + S.agents.length + ready, next: standing.length + (S.suggestions || []).length };
}
function tasksSeg() {
  const c = tasksCounts();
  const seg = h('div', { class: 'seg tasks', role: 'tablist' }, [['active', 'Active', c.active], ['next', 'Up next', c.next], ['history', 'History', 0]].map(([id, label, n]) =>
    h('button', { type: 'button', role: 'tab', 'aria-selected': (S.tasksView || 'active') === id ? 'true' : 'false', onclick: () => { S.tasksView = id; renderAgentsPage(); } }, label, n ? h('small', { text: String(n) }) : null)));
  return seg;
}

// ---------- activity: what a task did, and a word with it ----------
// Each background run leaves a short record (the tools it used and what they were asked, the files it made, what it
// reported). Open one to read it, tell a task that is still working what to do differently, or write to a finished one
// to carry on from where it stopped. The page shows what the server kept and nothing it did not.
const RUNS = { list: [], on: false, timer: 0 };
const RUN_STATE = { running: 'Working', done: 'Finished', failed: 'Failed', cancelled: 'Stopped' };
async function loadRuns() {
  try { const d = await api('/api/runs'); RUNS.list = d.runs || []; RUNS.on = !!d.available; } catch (e) { if (e instanceof AuthError) return; }
  const el = document.getElementById('runs-card');
  if (S.tab !== 'agents' || (S.tasksView || 'active') !== 'active') return;
  const c = runsCard();
  if (el) keepScroll(el, () => { if (c) el.replaceWith(c); else el.remove(); });
  else if (c && document.getElementById('now')) keepScroll(document.getElementById('now'), () => document.getElementById('now').after(c));
}
function runClock(iso) {
  return new Intl.DateTimeFormat([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23', timeZone: S.zone }).format(new Date(iso));
}
function runTook(r) {
  if (!r.ended) return '';
  return duration(Math.max(0, Math.round((new Date(r.ended) - new Date(r.started)) / 1000)));
}
function runLine(r) {
  if (r.status === 'running') return r.doing || 'Starting';
  return (RUN_STATE[r.status] || r.status) + ' ' + ago(r.ended || r.started) + (runTook(r) ? ' · took ' + runTook(r) : '') + (r.steps ? ' · ' + r.steps + (r.steps === 1 ? ' step' : ' steps') : '');
}
function runsCard() {
  if (!RUNS.on || !RUNS.list.length) return null;
  const rows = h('div', { class: 'rows' });
  for (const r of RUNS.list.slice(0, 8)) {
    rows.append(h('button', { type: 'button', class: 'row runrow', onclick: () => openRun(r.id) },
      h('span', { class: 'k ' + (r.kind || 'job'), text: KINDS[r.kind || 'job'] || 'JOB' }),
      h('span', { class: 'grow' }, h('span', { class: 't', text: r.name }), h('span', { class: 's mono', text: runLine(r) })),
      r.status === 'running' ? h('span', { class: 'pulse', 'aria-hidden': 'true' }) : h('span', { class: 'state ' + (r.status === 'done' ? 'state-ok' : r.status === 'failed' ? 'state-bad' : 'state-quiet'), text: RUN_STATE[r.status] || r.status })));
  }
  const n = RUNS.list.filter((r) => r.status === 'running').length;
  return h('div', { class: 'card', id: 'runs-card' }, h('h2', null, 'What it did', h('small', { text: n ? n + ' working' : 'latest runs' })),
    h('div', { class: 'note', text: 'Open one to see each step, what it made and what it reported, or to tell it what to do differently.' }), rows);
}
// A light refresh while something is working, so its latest step moves on the Tasks screen.
setInterval(() => { if (S.tab === 'agents' && !document.hidden && RUNS.list.some((r) => r.status === 'running')) loadRuns(); }, 4000);

async function openRun(id) {
  const body = openPanel('Activity', '');
  body.className = 'panel-body activity';
  const top = h('div', { class: 'run-top' }), detail = h('div', { class: 'run-detail' }), steer = h('div', { class: 'run-steer' });
  body.replaceChildren(top, detail, steer);
  let drawnFor = '';
  const load = async () => {
    let r;
    try { r = await api('/api/runs/' + encodeURIComponent(id)); }
    catch (e) { if (e instanceof AuthError) return; detail.replaceChildren(h('div', { class: 'card' }, h('div', { class: 'empty', text: e.message }))); return; }
    drawRun(r, top, detail);
    if (drawnFor !== r.id + r.status) { drawnFor = r.id + r.status; drawSteer(r, steer, () => { id = r.id; load(); }); }
    clearTimeout(PANEL.timer);
    if (r.status === 'running') PANEL.timer = setTimeout(load, 2500);
    else loadRuns();
  };
  await load();
}
function drawRun(r, top, detail) {
  document.getElementById('panel-title').textContent = r.name;
  document.getElementById('panel-sub').textContent = (RUN_STATE[r.status] || r.status) + (r.ended ? ' · took ' + runTook(r) : ' · started ' + runClock(r.started)) + (r.model ? ' · ' + r.model : '');
  const acts = [];
  if (r.status === 'running') {
    const stop = h('button', { class: 'btn small', type: 'button', text: 'Stop it' });
    stop.addEventListener('click', async () => {
      stop.disabled = true;
      try { await api('/api/runs/' + encodeURIComponent(r.id) + '/stop', { method: 'POST' }); toast('Stopping it.'); } catch (e) { failed(e); stop.disabled = false; }
    });
    acts.push(stop);
  }
  const cards = [];
  const mark = h('div', { class: 'run-state ' + r.status }, h('span', { class: 'mark ' + (r.status === 'failed' ? 'bad' : r.status === 'running' ? 'quiet' : 'ok') }, icon(r.status === 'failed' ? 'x' : r.status === 'running' ? 'info' : 'check')),
    h('span', { class: 'grow' }, h('b', { text: RUN_STATE[r.status] || r.status }), h('span', { class: 's', text: r.ended ? 'Ended ' + ago(r.ended) : 'Started ' + ago(r.started) })), acts);
  top.replaceChildren(...[mark, r.parent ? h('div', { class: 'note', text: 'This carries on from an earlier run.' }) : null].filter(Boolean));

  if (r.prompt) cards.push(h('details', { class: 'card fold' }, h('summary', null, h('span', { class: 'display', text: 'What it was asked' })), h('div', { class: 'run-prompt', text: r.prompt })));
  const steps = h('div', { class: 'timeline' });
  (r.steps || []).forEach((st, i) => {
    if (r.skipped && i === 5) steps.append(h('div', { class: 'run-gap', text: r.skipped + ' steps in between are not shown' }));
    steps.append(h('div', { class: 'run-step' }, h('span', { class: 'at mono', text: runClock(st.at) }), h('span', { class: 'what' }, h('b', { text: st.tool }), st.detail ? h('span', { class: 'mono d', text: st.detail }) : null)));
  });
  cards.push(h('div', { class: 'card' }, h('h2', null, 'Steps', h('small', { text: String((r.steps || []).length + (r.skipped || 0)) })),
    (r.steps || []).length ? steps : h('div', { class: 'empty', text: r.status === 'running' ? 'Nothing yet: it is getting started.' : 'It did not use any tools.' })));
  if ((r.files || []).length) {
    const rows = h('div', { class: 'rows' });
    for (const f of r.files) rows.append(h('div', { class: 'row' }, h('span', { class: 'tag', text: /image/.test(f.mime || '') ? 'IMG' : f.mime === 'text/html' ? 'PAGE' : 'DOC' }), h('span', { class: 'grow' }, h('span', { class: 't', text: f.name || 'Document' })),
      h('button', { class: 'btn small', type: 'button', text: 'View', onclick: () => openDoc(f.id, { name: f.name, mime: f.mime }) })));
    cards.push(h('div', { class: 'card' }, h('h2', null, 'What it made'), rows));
  }
  if ((r.steers || []).length) {
    cards.push(h('div', { class: 'card' }, h('h2', null, 'What you told it'), h('div', { class: 'rows' }, r.steers.map((x) =>
      h('div', { class: 'row' }, h('span', { class: 'grow' }, h('span', { class: 't wrap', text: x.text }), h('span', { class: 's', text: runClock(x.at) + (x.applied ? ' · it has read this' : ' · it will read this at its next step') })))))));
  }
  if (r.result) cards.push(h('div', { class: 'card' }, h('h2', null, r.status === 'failed' ? 'What went wrong' : 'What it reported'), h('div', { class: 'run-result' }, markdown(r.result))));
  keepScroll(detail, () => detail.replaceChildren(...cards));
}
function drawSteer(r, box, again) {
  const live = r.status === 'running';
  const area = h('textarea', { maxlength: '1000', rows: '2', placeholder: live ? 'Tell it something while it works. For example: skip the second site, or look at prices in euros.' : 'Write to carry on from here. For example: now compare delivery times.' });
  const go = h('button', { class: 'btn primary', type: 'button', text: live ? 'Tell it' : 'Carry on from here' });
  go.addEventListener('click', async () => {
    const text = area.value.trim();
    if (!text) { toast('Write what you want it to do.'); return; }
    go.disabled = true;
    try {
      const d = await api('/api/runs/' + encodeURIComponent(r.id) + '/steer', { method: 'POST', body: { text } });
      area.value = '';
      if (d.mode === 'follow_up') { toast('Started: ' + d.started + '. Its result comes to your chat.'); closePanel(); loadAgentsPage(); return; }
      toast('Noted. It reads this at its next step.');
      again();
    } catch (e) { failed(e); }
    go.disabled = false;
  });
  box.replaceChildren(h('div', { class: 'card' }, h('h2', null, live ? 'Steer it' : 'Follow up'),
    live ? null : h('div', { class: 'note', text: 'This starts a new background task that knows what this one was asked and what it reported. Like any background task it cannot send, post, pay or delete.' }),
    area, h('div', { class: 'btn-row' }, go)));
}

// ---------- routines: show it once, and it repeats it ----------
// In the browser view, Record writes down what you do by hand as plain steps ("click the link Orders"). Passwords, codes
// and card numbers are never kept, only that something private was entered. The routine can then be run now or put on a
// schedule; it repeats the steps in the agent's browser, may click and type there without asking, and never deletes or posts.
const ROUT = { on: false, recording: false, count: 0, steps: [], routines: [], canRun: false };
async function loadRoutines() {
  try {
    const d = await api('/api/routines');
    ROUT.on = !!d.available; ROUT.recording = !!d.recording; ROUT.count = d.count || 0; ROUT.steps = d.steps || []; ROUT.routines = d.routines || []; ROUT.canRun = !!d.can_run;
  } catch (e) { if (e instanceof AuthError) return; }
}
async function routineAct(body) {
  const d = await api('/api/routines', { method: 'POST', body });
  ROUT.on = true; ROUT.recording = !!d.recording; ROUT.count = d.count || 0; ROUT.steps = d.steps || []; ROUT.routines = d.routines || []; ROUT.canRun = !!d.can_run;
  return d;
}
function recordBar() {
  if (!ROUT.on) return null;
  const bar = h('div', { class: 'rec-bar' });
  const count = h('b', { class: 'rec-count' });
  const draw = () => {
    if (ROUT.recording) {
      count.textContent = ROUT.count + (ROUT.count === 1 ? ' step' : ' steps');
      bar.replaceChildren(h('span', { class: 'pulse', 'aria-hidden': 'true' }), h('span', { class: 'grow' }, h('b', { text: 'Recording ' }), count, h('span', { class: 's', text: 'Do it by hand in the page below. Passwords and codes are not kept.' })),
        h('button', { class: 'btn small primary', type: 'button', text: 'Stop', onclick: async () => { try { await routineAct({ action: 'stop' }); } catch (e) { failed(e); } draw(); } }));
    } else if (ROUT.steps.length) {
      bar.replaceChildren(saveRoutineForm(draw));
    } else {
      bar.replaceChildren(h('span', { class: 'grow' }, h('b', { text: 'Teach it a routine' }), h('span', { class: 's', text: 'Press Record, do something by hand here, then save it to run again.' })),
        h('button', { class: 'btn small', type: 'button', text: 'Record', onclick: async () => { try { await routineAct({ action: 'start' }); } catch (e) { failed(e); } draw(); } }));
    }
  };
  draw();
  const tick = async () => {
    if (!bar.isConnected) return;
    if (ROUT.recording) { await loadRoutines(); if (ROUT.recording) count.textContent = ROUT.count + (ROUT.count === 1 ? ' step' : ' steps'); else draw(); }
    setTimeout(tick, 1500);
  };
  setTimeout(tick, 1500);
  return bar;
}
function saveRoutineForm(redraw) {
  const keep = ROUT.steps.map(() => true);
  const list = h('div', { class: 'rec-steps' }, ROUT.steps.map((st, i) => h('label', { class: 'check rec-step' + (st.secret ? ' private' : '') },
    h('input', { type: 'checkbox', checked: true, onchange: (e) => { keep[i] = e.target.checked; } }), h('span', { text: (i + 1) + '. ' + st.line }))));
  const name = h('input', { type: 'text', maxlength: '60', placeholder: 'Check my orders', autocomplete: 'off', 'aria-label': 'Name of the routine' });
  const save = h('button', { class: 'btn primary', type: 'button', text: 'Save routine' });
  save.addEventListener('click', async () => {
    if (!name.value.trim()) { toast('Give the routine a name.'); return; }
    save.disabled = true;
    try { const d = await routineAct({ action: 'save', name: name.value, keep: keep.map((k, i) => (k ? i : -1)).filter((i) => i >= 0) }); toast('Saved “' + d.saved + '”. Find it under Tasks, Up next.'); redraw(); }
    catch (e) { failed(e); save.disabled = false; }
  });
  return h('div', { class: 'rec-save' }, h('b', { text: 'Here is what you did' }), h('div', { class: 's', text: 'Untick anything that should not be part of it. A step marked private was done by you and is not kept.' }), list,
    h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'Name' }), name),
    h('div', { class: 'btn-row' }, save, h('button', { class: 'btn', type: 'button', text: 'Throw it away', onclick: async () => { try { await routineAct({ action: 'discard' }); } catch (e) { failed(e); } redraw(); } })));
}
const DAY_CHOICES = [['daily', 'Every day'], ['weekdays', 'Every weekday'], ['weekly:mon', 'Every Monday'], ['weekly:tue', 'Every Tuesday'], ['weekly:wed', 'Every Wednesday'], ['weekly:thu', 'Every Thursday'], ['weekly:fri', 'Every Friday'], ['weekly:sat', 'Every Saturday'], ['weekly:sun', 'Every Sunday']];
function dayWords(days) { const f = DAY_CHOICES.find((d) => d[0] === days); return f ? f[1].toLowerCase() : ''; }
function routinesCard() {
  if (!ROUT.on) return null;
  const card = h('div', { class: 'card' }, h('h2', null, 'Routines', h('small', { text: ROUT.routines.length ? ROUT.routines.length + ' saved' : '' })));
  if (!ROUT.routines.length) {
    card.append(h('div', { class: 'field-note', text: 'Show it something once and it does it again. In the agent’s browser, press Record, do it by hand, and save it as a routine you can run or schedule.' }),
      h('div', { class: 'btn-row' }, h('button', { class: 'btn primary', type: 'button', text: 'Open the browser to record', onclick: () => openComputer('browser') })));
    return card;
  }
  const rows = h('div', { class: 'rows' });
  for (const r of ROUT.routines) {
    const sc = r.schedule;
    rows.append(h('button', { type: 'button', class: 'row runrow', onclick: () => openRoutine(r.id) },
      h('span', { class: 'k job', text: 'ROUTINE' }),
      h('span', { class: 'grow' }, h('span', { class: 't', text: r.name }), h('span', { class: 's', text: r.steps.length + (r.steps.length === 1 ? ' step' : ' steps') + (sc ? ' · ' + dayWords(sc.days) + ' at ' + sc.at : ' · not scheduled') }))));
  }
  card.append(rows, h('div', { class: 'btn-row' }, h('button', { class: 'btn', type: 'button', text: 'Record another', onclick: () => openComputer('browser') })));
  return card;
}
function openRoutine(id) {
  const r = ROUT.routines.find((x) => x.id === id);
  if (!r) return;
  const body = openPanel(r.name, r.steps.length + (r.steps.length === 1 ? ' step' : ' steps') + ' you showed it');
  body.className = 'panel-body routine';
  const steps = h('ol', { class: 'routine-steps' }, r.steps.map((st) => h('li', { class: st.secret ? 'private' : '', text: st.line })));
  const run = h('button', { class: 'btn primary', type: 'button', text: 'Run it now', disabled: ROUT.canRun ? null : true });
  run.addEventListener('click', async () => {
    run.disabled = true;
    try { const d = await routineAct({ action: 'run', id: r.id }); toast(d.message || 'Started.'); closePanel(); loadAgentsPage(); }
    catch (e) { failed(e); run.disabled = false; }
  });
  const days = h('select', { class: 'rule-select', 'aria-label': 'How often' }, DAY_CHOICES.map(([v, l]) => h('option', { value: v, text: l })));
  const at = h('input', { type: 'time', step: '300', 'aria-label': 'Time of day' });
  days.value = r.schedule ? r.schedule.days : 'weekdays'; at.value = r.schedule ? r.schedule.at : '08:00';
  const set = h('button', { class: 'btn', type: 'button', text: r.schedule ? 'Change schedule' : 'Run it on a schedule' });
  set.addEventListener('click', async () => {
    set.disabled = true;
    try { const d = await routineAct({ action: 'schedule', id: r.id, days: days.value, at: at.value }); toast(d.message || 'Scheduled.'); closePanel(); loadAgentsPage(); }
    catch (e) { failed(e); set.disabled = false; }
  });
  const sched = h('div', { class: 'card' }, h('h2', null, 'Schedule'), r.schedule ? h('div', { class: 'field-note', text: 'Runs ' + dayWords(r.schedule.days) + ' at ' + r.schedule.at + (r.schedule.next ? '. Next: ' + fmtWhen(new Date(r.schedule.next)) : '') + (r.schedule.last ? '. Last: ' + r.schedule.last : '') + '.' }) : null,
    h('div', { class: 'row-inline' }, days, at), h('div', { class: 'btn-row' }, set,
      r.schedule ? h('button', { class: 'btn', type: 'button', text: 'Stop the schedule', onclick: async () => { try { await routineAct({ action: 'unschedule', id: r.id }); toast('Taken off the schedule.'); closePanel(); loadAgentsPage(); } catch (e) { failed(e); } } }) : null));
  const del = h('button', { class: 'btn', type: 'button', text: 'Delete this routine' });
  del.addEventListener('click', async () => {
    if (!confirm('Delete “' + r.name + '”? Its schedule goes with it.')) return;
    try { await routineAct({ action: 'remove', id: r.id }); toast('Deleted.'); closePanel(); loadAgentsPage(); } catch (e) { failed(e); }
  });
  body.replaceChildren(h('div', { class: 'card' }, h('h2', null, 'Steps'), steps),
    h('div', { class: 'card' }, h('h2', null, 'What it may do'), h('div', { class: 'field-note', text: 'It follows these steps in the agent’s browser, clicking and typing as you did, without asking. It reads each page and adapts if it looks different. It cannot use your files, shell or mail, and it never deletes or posts. If a page needs a password or a code, it asks you to take over. Your rules and Pause all still apply.' }),
      h('div', { class: 'btn-row' }, run)), sched, h('div', { class: 'btn-row' }, del));
}

// ---------- named agents ----------
// An agent is a conversation with its own name, instructions and, if you like, its own model, and a switch that lets it
// look and not touch. It is the same Talaria with those added: it can never do more than you have allowed.
const AG = { available: false, agents: [], profiles: [], default_profile: '', colors: [], max: 8 };
async function loadPersonas() {
  try { const d = await api('/api/personas'); Object.assign(AG, { available: !!d.available, agents: d.agents || [], profiles: d.profiles || [], default_profile: d.default_profile || '', colors: d.colors || CHAN_COLORS, max: d.max || 8 }); }
  catch (e) { if (e instanceof AuthError) return; }
}
function agentsCard() {
  if (!AG.available) return null;
  const card = h('div', { class: 'card' }, h('h2', null, 'Your agents', h('small', { text: AG.agents.length ? AG.agents.length + ' of ' + AG.max : '' })));
  if (!AG.agents.length) card.append(h('div', { class: 'field-note', text: 'Give a job its own agent: a name, what it is for, how it should behave, and optionally its own model. For example “Inbox”, which only reads and sorts your mail. Each keeps its own conversation.' }));
  const rows = h('div', { class: 'rows' });
  for (const a of AG.agents) {
    rows.append(h('div', { class: 'row' }, h('span', { class: 'chan-ico chan ' + a.color, text: a.icon || a.name.slice(0, 1).toUpperCase() }),
      h('span', { class: 'grow' }, h('span', { class: 't', text: a.name + (a.active ? ' · open now' : '') }), h('span', { class: 's', text: [a.read_only ? 'Looks only' : 'Can act as you allow', a.profile ? 'Model ' + a.profile : ''].filter(Boolean).join(' · ') })),
      h('span', { class: 'acts keep' }, h('button', { class: 'btn small primary', type: 'button', text: 'Chat', onclick: () => chatWithAgent(a) }), h('button', { class: 'btn small', type: 'button', text: 'Edit', onclick: () => openAgentForm(a) }))));
  }
  if (AG.agents.length) card.append(rows);
  if (AG.agents.length < AG.max) card.append(h('div', { class: 'btn-row' }, h('button', { class: 'btn', type: 'button', text: 'New agent', onclick: () => openAgentForm(null) })));
  return card;
}
async function chatWithAgent(a) {
  try { await api('/api/personas', { method: 'POST', body: { action: 'chat', id: a.id } }); } catch (e) { failed(e); return; }
  closePanel(); location.hash = '#chat';
  await loadState(); S.pinned = true; toBottom(); loadPersonas();
}
function openAgentForm(a) {
  const body = openPanel(a ? a.name : 'New agent', 'A name, what it is for, and how it should behave');
  body.className = 'panel-body';
  let color = a ? a.color : 'brass';
  const name = h('input', { type: 'text', maxlength: '30', placeholder: 'Inbox', autocomplete: 'off' });
  const iconIn = h('input', { type: 'text', maxlength: '8', placeholder: '📥', autocomplete: 'off' });
  const how = h('textarea', { maxlength: '1500', placeholder: 'For example: You look after my inbox. Sort what needs me from what can wait, draft replies but never send them, and keep answers to a few lines.' });
  const model = h('select', { class: 'rule-select', 'aria-label': 'Model' }, h('option', { value: '', text: 'Same as the chat' }), AG.profiles.map((p) => h('option', { value: p, text: p + (p === AG.default_profile ? ' (default)' : '') })));
  const look = h('input', { type: 'checkbox' });
  if (a) { name.value = a.name; iconIn.value = a.icon || ''; how.value = a.instructions; model.value = a.profile || ''; look.checked = !!a.read_only; }
  const sw = h('div', { class: 'swatches' }, (AG.colors.length ? AG.colors : CHAN_COLORS).map((c) => h('button', { type: 'button', class: 'chan ' + c, 'aria-label': c, 'aria-pressed': c === color ? 'true' : 'false', onclick: () => { color = c; for (const b of sw.children) b.setAttribute('aria-pressed', b.classList.contains(c) ? 'true' : 'false'); } })));
  const save = h('button', { class: 'btn primary', type: 'button', text: a ? 'Save changes' : 'Create agent' });
  save.addEventListener('click', async () => {
    save.disabled = true;
    try {
      const d = await api('/api/personas', { method: 'POST', body: { action: 'save', id: a ? a.id : '', name: name.value, icon: iconIn.value, color, instructions: how.value, profile: model.value, read_only: look.checked } });
      Object.assign(AG, { agents: d.agents || [] }); toast('Saved “' + d.saved + '”.'); closePanel(); if (S.tab === 'agents') renderAgentsPage();
    } catch (e) { failed(e); save.disabled = false; }
  });
  const acts = [save];
  if (a) {
    acts.push(h('button', { class: 'btn', type: 'button', text: 'Chat with it', onclick: () => chatWithAgent(a) }));
    acts.push(h('button', { class: 'btn', type: 'button', text: 'Remove', onclick: async () => {
      if (!confirm('Remove “' + a.name + '”? Its conversations stay, as ordinary ones.')) return;
      try { const d = await api('/api/personas', { method: 'POST', body: { action: 'remove', id: a.id } }); AG.agents = d.agents || []; toast('Removed.'); closePanel(); if (S.tab === 'agents') renderAgentsPage(); loadState(); } catch (e) { failed(e); }
    } }));
  }
  body.replaceChildren(h('div', { class: 'card chan-form' },
    h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'Name' }), name),
    h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'Icon (an emoji or a couple of letters)' }), iconIn),
    h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'Colour' }), sw),
    h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'What it is for and how it should behave' }), how),
    AG.profiles.length > 1 ? h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'Model' }), model) : null,
    h('label', { class: 'check' }, look, ' Looks only: it can read and search, and nothing that asks for approval is open to it'),
    h('div', { class: 'field-note', text: 'It follows your instructions inside the same safety rules as the rest of Talaria: it can never do more than you have allowed.' }),
    h('div', { class: 'btn-row' }, acts)));
  name.focus();
}

// ---------- the greeting an empty chat opens with ----------
function greetLine() {
  const now = new Date();
  const hr = Number(new Intl.DateTimeFormat('en-GB', { hour: '2-digit', hourCycle: 'h23', timeZone: S.zone }).format(now));
  return (hr < 12 ? 'Good morning' : hr < 18 ? 'Good afternoon' : 'Good evening') + (S.profile && S.profile.name ? ', ' + S.profile.name : '') + '.';
}
function renderGreet() {
  const el = $('#greet'); if (!el) return;
  const show = S.tab === 'chat' && S.order.length === 0 && !!S.profile;
  if (!show) { if (!el.hidden) { el.hidden = true; el.replaceChildren(); } return; }
  const date = new Intl.DateTimeFormat([], { weekday: 'long', day: 'numeric', month: 'long', timeZone: S.zone }).format(new Date());
  const ch = S.channel;
  el.hidden = false;
  el.replaceChildren(h('div', { class: 'd', text: ch ? (ch.icon ? ch.icon + ' ' : '') + ch.title : date }), h('h2', { text: ch ? (ch.purpose || 'A channel for ' + ch.title) : greetLine() }));
}

// ---------- channels ----------
// A channel is a conversation kept for one piece of work: a name, an icon, a colour and what it is for.
const CHAN_COLORS = ['brass', 'info', 'ok', 'coral', 'plum', 'slate'];
function channelForm() {
  closeSheetBody();
  const body = $('#sheet-body');
  let color = 'brass';
  const name = h('input', { type: 'text', maxlength: '40', placeholder: 'Wholesale orders', autocomplete: 'off' });
  const iconIn = h('input', { type: 'text', maxlength: '8', placeholder: '📦', autocomplete: 'off' });
  const why = h('textarea', { maxlength: '240', placeholder: 'What is it for? For example: quotes, stock and shipping for shop orders.' });
  const sw = h('div', { class: 'swatches' }, CHAN_COLORS.map((c) => h('button', { type: 'button', class: 'chan ' + c, 'aria-label': c, 'aria-pressed': c === color ? 'true' : 'false', onclick: () => { color = c; for (const b of sw.children) b.setAttribute('aria-pressed', b.classList.contains(c) ? 'true' : 'false'); } })));
  const create = h('button', { class: 'btn primary', type: 'button', text: 'Create channel' });
  create.addEventListener('click', async () => {
    if (!name.value.trim()) { toast('Give the channel a name.'); return; }
    create.disabled = true;
    try { await api('/api/chats', { method: 'POST', body: { channel: { name: name.value, icon: iconIn.value, color, purpose: why.value } } }); }
    catch (e) { failed(e); create.disabled = false; return; }
    closeSheet();
    await loadState(); S.pinned = true; toBottom();
    const purpose = why.value.trim();
    sendText((purpose ? 'This channel is for: ' + purpose : 'This channel is called "' + name.value.trim() + '".') + ' Keep the work here about that. Say hello in a line or two, and offer three things you could start on.');
  });
  body.replaceChildren(h('div', { class: 'card chan-form' }, h('h2', null, 'New channel'),
    h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'Name' }), name),
    h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'Icon (an emoji or a couple of letters)' }), iconIn),
    h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'Colour' }), sw),
    h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'What it is for' }), why),
    h('div', { class: 'btn-row' }, create, h('button', { class: 'btn', type: 'button', text: 'Back', onclick: openSheet }))));
  name.focus();
}
function closeSheetBody() { $('#sheet-body').replaceChildren(); }


// ---------- suggestions: what it proposes ----------
// The agent leaves suggestions (the morning brief does, daily) and each waits for a tap: Start runs it as a background
// task, which can read, draft and write but cannot send, pay or delete, and Hide drops it for good. The newest one
// waits above the message box; all of them are under Up next on the Tasks screen.
function sourceNote(sources) {
  const src = (sources || [])[0];
  if (!src) return null;
  const u = String(src.url || '');
  if (u.startsWith('mail:')) return 'From mail #' + u.slice(5);
  if (u.startsWith('calendar:')) return 'From calendar ' + u.slice(9);
  if (/^https:\/\//.test(u)) { try { return 'From ' + new URL(u).hostname; } catch { return null; } }
  return null;
}
async function actOnSuggestion(body, done) {
  try {
    const d = await api('/api/suggestions', { method: 'POST', body });
    S.suggestions = d.suggestions || []; S.suggestion = S.suggestions[0] || null;
    if (d.started) toast('Started: ' + d.started + '. It shows under Tasks, and its result will come to this chat.'); else if (done) toast(done);
  } catch (e) { failed(e); }
  renderSuggest(); if (S.tab === 'agents') renderAgentsPage();
}
function suggestButtons(sg) {
  return [sg.startable ? h('button', { class: 'btn small primary', type: 'button', text: 'Start', title: 'Run it as a background task. It cannot send, pay or delete.', onclick: () => actOnSuggestion({ start: sg.id }) }) : null,
    h('button', { class: 'btn small', type: 'button', text: 'Hide', onclick: () => actOnSuggestion({ hide: sg.id }, 'Hidden.') })];
}
function renderSuggest() {
  const el = $('#suggest'); if (!el) return;
  const sg = S.suggestion;
  const show = !!sg && S.tab === 'chat' && !working() && !$('#input').value.trim();
  if (!show) { if (!el.hidden) { el.hidden = true; el.replaceChildren(); } return; }
  const sig = sg.id + '|' + sg.title;
  if (!el.hidden && el.dataset.sig === sig) return;
  el.hidden = false; el.dataset.sig = sig;
  el.replaceChildren(h('div', { class: 'suggest-card' }, h('span', { class: 'sg-tag', text: 'SUGGESTED' }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: sg.title }), sg.body ? h('span', { class: 's', text: sg.body }) : null),
    h('span', { class: 'acts' }, suggestButtons(sg))));
}
function suggestionsCard() {
  const list = S.suggestions || [];
  if (!list.length) return null;
  const rows = h('div', { class: 'rows' });
  for (const sg of list) {
    const note = sourceNote(sg.sources);
    rows.append(h('div', { class: 'row sug-row' },
      h('span', { class: 'grow' }, h('span', { class: 't', text: sg.title }), sg.body ? h('span', { class: 's', text: sg.body }) : null, note ? h('span', { class: 's why', text: note + ' · ' + ago(sg.at) }) : null),
      h('span', { class: 'acts keep' }, suggestButtons(sg))));
  }
  return h('div', { class: 'card' }, h('h2', null, 'Suggested for you', h('small', { text: list.length + (list.length === 1 ? ' idea' : ' ideas') })),
    h('div', { class: 'note', text: 'Start runs one as a background task. It can read, draft and write, but not send, pay or delete. Hide drops it for good.' }), rows);
}
async function loadSuggestions() {
  try { const d = await api('/api/suggestions'); S.suggestions = d.suggestions || []; S.suggestion = S.suggestions[0] || null; } catch (e) { if (e instanceof AuthError) return; }
  renderSuggest(); if (S.tab === 'agents') renderAgentsPage();
}


// ---------- taking over the browser, when the agent asks ----------
// The agent calls browser_handoff when a site needs a person: a sign-in, a code sent to a phone, a human check. A card
// says why with a button that opens the tab, and while the tab is open a bar stays on it with the way back: when the
// user taps "I'm done" the agent carries on at once, with no message to write.
const HANDOFF_STATE = { done: ['ok', 'You did it'], skipped: ['quiet', 'You skipped it'], replied: ['quiet', 'You answered in the chat'], expired: ['quiet', 'No longer waiting'] };
async function answerHandoff(m, action, btns) {
  btns.forEach((b) => { b.disabled = true; });
  try { await api('/api/handoff', { method: 'POST', body: { token: m.token, action } }); return true; }
  catch (e) { if (!(e instanceof AuthError)) toast(e.message); btns.forEach((b) => { b.disabled = false; }); return false; }
}
function openHandoffTab(m) { COMPUTER.want = m && m.url ? hostOf(m.url) : ''; openComputer('browser'); }
function handoffCard(m) {
  const pending = m.state === 'pending';
  const browserPage = !!m.url; // a page to take over; without one it is an action the user's rules say they do themselves
  const host = m.url ? hostOf(m.url) : '';
  const card = h('div', { class: 'ask handoff' + (pending ? '' : ' done') });
  card.append(h('div', { class: 'ask-head' }, h('span', { class: 'ask-icon' }, icon(browserPage ? 'eye' : 'shield')), h('span', { class: 'ask-title', text: browserPage ? 'Needs you in the browser' : 'Needs you' }), pending && h('span', { class: 'pill brass', text: 'WAITING' })));
  card.append(h('div', { class: 'ask-why', text: m.text + (host ? ' · ' + host : '') }));
  if (pending) {
    card.append(h('div', { class: 'note', text: browserPage ? 'Open the page, do it yourself, then say you are done. What you type there is not shown to the agent.' : 'Your rules say you do this kind of thing yourself. Do it, then tell the agent. Change the rule under Settings, Permissions.' }));
    const btns = [];
    const mk = (label, cls, fn) => { const b = h('button', { class: 'btn ' + cls, type: 'button', text: label, onclick: fn }); btns.push(b); return b; };
    card.append(h('div', { class: 'btn-row' },
      mk('Skip', '', () => answerHandoff(m, 'skip', btns)),
      browserPage ? mk('Open browser', 'primary', () => openHandoffTab(m)) : null,
      mk(browserPage ? 'I’m done' : 'I did it', browserPage ? '' : 'primary', async () => { if (await answerHandoff(m, 'done', btns)) { toast('Carrying on.'); } })));
  } else {
    const v = HANDOFF_STATE[m.state] || ['quiet', m.state];
    card.append(h('div', { class: 'verdict ' + v[0] }, icon(v[0] === 'ok' ? 'check' : 'info'), v[1]));
    if (m.state === 'done' && browserPage) card.append(h('div', { class: 'field-note', text: 'The sign-in stays in the agent’s browser, so it can use this site until the site ends it. To let it sign in again by itself, save the login under Settings, Connections.' }));
  }
  return card;
}
// The bar kept on the browser panel while the agent waits: where it is, and the one tap that sends the user back.
function handoffBar() {
  const m = pendingHandoffs().find((x) => x.url); // only a request about a page belongs on the browser panel
  if (!m) return null;
  const btns = [];
  const done = h('button', { class: 'btn primary', type: 'button', text: 'I’m done, continue', onclick: async () => { if (await answerHandoff(m, 'done', btns)) { closePanel(); location.hash = '#chat'; toast('Carrying on.'); } } });
  const skip = h('button', { class: 'btn', type: 'button', text: 'Skip', onclick: async () => { if (await answerHandoff(m, 'skip', btns)) { closePanel(); location.hash = '#chat'; } } });
  btns.push(done, skip);
  return h('div', { class: 'handoff-bar' }, h('span', { class: 'grow' }, h('b', { text: 'The agent is waiting for you' }), h('span', { class: 's', text: m.text })), skip, done);
}


// ---------- rules: what the agent may do without asking ----------
// One setting per kind of action. "Default" is how Talaria always behaved; the others are rules that apply to the next
// thing the agent does, at once, with no restart. "I'll do it myself" makes the agent ask you to do it instead of
// doing it, and wait.
const MODE_LABEL = { default: 'Default', allow: 'Just do it', ask: 'Ask me first', handoff: 'I’ll do it myself', deny: 'Never' };
const MODE_HELP = {
  default: 'How Talaria behaves without a rule: it asks for what is risky and does the rest.',
  allow: 'It does this without asking, every time, including when you are away.',
  ask: 'It asks you first, every time, and waits for your answer.',
  handoff: 'It does not do this. It asks you to, with what to do, and waits until you say you have.',
  deny: 'It is never allowed to do this, whoever asks.',
};
function rulesCard() {
  const r = S.rules;
  if (!r || !(r.categories || []).length) return null;
  const rows = h('div', { class: 'rows' });
  for (const c of r.categories) {
    const sel = h('select', { class: 'rule-select', 'aria-label': c.title });
    for (const m of r.modes) sel.append(h('option', { value: m, text: MODE_LABEL[m] || m }));
    if (c.mixed) { const o = h('option', { value: '', text: 'Mixed (set by hand)', disabled: '' }); sel.prepend(o); sel.value = ''; } else sel.value = c.mode;
    const help = !c.mixed && c.mode === 'default' ? null : h('span', { class: 's', text: c.mixed ? 'Its tools have different rules in the config file. Pick a mode to make them all the same.' : MODE_HELP[c.mode] });
    sel.addEventListener('change', async () => {
      const mode = sel.value;
      if (mode === 'allow' && c.risky && !confirm('"' + c.title + '" would happen without asking, even when you are away. Are you sure?')) { sel.value = c.mixed ? '' : c.mode; return; }
      sel.disabled = true;
      try {
        const d = await api('/api/rules', { method: 'POST', body: { category: c.id, mode } });
        S.rules = d.rules;
        toast(d.restart_needed ? 'Saved. It applies after Talaria restarts.' : 'Saved. It applies from the next thing the agent does.');
      } catch (e) { failed(e); }
      renderSettings();
    });
    rows.append(h('div', { class: 'row rule-row' }, h('span', { class: 'grow' }, h('span', { class: 't', text: c.title }), h('span', { class: 's', text: c.detail }), help), sel));
  }
  const other = Object.entries(r.other || {});
  return h('div', { class: 'card' }, h('h2', null, 'Rules for each kind of action'),
    h('div', { class: 'field-note', text: 'These apply to every chat, background task and scheduled job. "Never" and "I’ll do it myself" cannot be undone by the agent, and a rule you set here is kept even after a restart.' }), rows,
    other.length ? h('div', { class: 'field-note', text: 'Also set by hand in the config file: ' + other.map(([k, v]) => k + ' is ' + v).join('; ') + '.' }) : null);
}


// ---------- recipes: ready-made jobs ----------
// A recipe is a stored instruction with a time and a blank or two to fill in. Adding one makes an ordinary scheduled job
// (or a page watch), shown under Up next like any other, and what it will ask the agent is shown in full before it is
// added. It reads, drafts and writes pages, and cannot send, post, pay or delete: nobody is there to approve it.
async function loadRecipes() {
  try { S.recipes = await api('/api/recipes'); } catch (e) { if (!(e instanceof AuthError)) toast(e.message); }
  return S.recipes;
}
function recipesEntryCard() {
  const r = S.recipes;
  if (!r || !r.available) return null;
  const added = (r.recipes || []).filter((x) => x.added).length;
  return h('div', { class: 'card' }, h('h2', null, 'Recipes', h('small', { text: added ? added + ' set up' : (r.recipes || []).length + ' ready-made' })),
    h('div', { class: 'field-note', text: 'Ready-made jobs: an evening wrap-up, meeting prep, a receipts table, a price watch. Each is a scheduled job you can see, move and remove.' }),
    h('div', { class: 'btn-row' }, h('button', { class: 'btn primary', type: 'button', text: 'Browse recipes', onclick: openRecipes })));
}
async function openRecipes() {
  const body = openPanel('Recipes', 'Ready-made jobs you can add with a tap');
  body.className = 'panel-body recipes';
  body.replaceChildren(h('div', { class: 'skeleton' }));
  await loadRecipes();
  S.recipeGroup = S.recipeGroup || 'All';
  recipeList(body);
}
function recipeStatus(x) {
  if (x.added) return 'Set up' + (x.when ? ' · ' + x.days + ' at ' + x.added.at : '');
  if (!x.ready) return 'Needs ' + x.missing.join(' and ');
  return x.kind === 'watch' ? 'Watches a page' : x.days + ' at ' + x.when;
}
function recipeList(body) {
  const r = S.recipes;
  if (!r || !r.available) { body.replaceChildren(h('div', { class: 'card empty', text: 'Recipes are not set up here.' })); return; }
  const groups = ['All', ...(r.groups || [])];
  const chips = h('div', { class: 'chips', role: 'group', 'aria-label': 'Group' }, groups.map((g) =>
    h('button', { type: 'button', 'aria-pressed': g === S.recipeGroup ? 'true' : 'false', text: g, onclick: () => { S.recipeGroup = g; recipeList(body); } })));
  const rows = h('div', { class: 'rows' });
  for (const x of (r.recipes || []).filter((y) => S.recipeGroup === 'All' || y.group === S.recipeGroup)) {
    rows.append(h('button', { class: 'row recipe-row', type: 'button', onclick: () => recipeDetail(body, x.id) },
      h('span', { class: 'grow' }, h('span', { class: 't', text: x.title }), h('span', { class: 's', text: x.summary }), h('span', { class: 's recipe-status' + (x.added ? ' on' : x.ready ? '' : ' off'), text: recipeStatus(x) })),
      x.added ? h('span', { class: 'pill ok', text: 'ADDED' }) : icon('chev')));
  }
  body.replaceChildren(chips, rows, h('div', { class: 'field-note', text: 'A recipe runs by itself, with no one to approve anything, so it can read, draft and make pages but cannot send, post, pay or delete. You see what it will do before you add it.' }));
}
function recipeDetail(body, id, done) {
  const x = ((S.recipes || {}).recipes || []).find((y) => y.id === id);
  if (!x) { recipeList(body); return; }
  const values = { ...((x.added && x.added.values) || {}) }; // what was filled in when it was added, shown again
  const fields = (x.params || []).map((p) => {
    const input = h('input', { type: p.kind === 'number' ? 'text' : p.kind === 'url' ? 'url' : 'text', inputmode: p.kind === 'number' ? 'decimal' : null, placeholder: p.placeholder || '', autocomplete: 'off', autocapitalize: 'none', spellcheck: 'false' });
    input.value = values[p.id] || '';
    input.addEventListener('input', () => { values[p.id] = input.value; });
    return h('div', { class: 'field' }, h('label', { class: 'field-label', text: p.label }), input);
  });
  const at = x.kind === 'schedule' ? h('input', { type: 'time', value: (x.added && x.added.at) || x.when, step: '300' }) : null;
  const note = h('div', { class: 'recipe-note', hidden: '' });
  const say = (text, bad) => { note.hidden = false; note.className = 'recipe-note' + (bad ? ' bad' : ' ok'); note.textContent = text; };
  const send = async (action) => {
    const btns = [...body.querySelectorAll('button.act')]; btns.forEach((b) => { b.disabled = true; });
    try {
      const d = await api('/api/recipes', { method: 'POST', body: { action, id: x.id, values, at: at ? at.value : '' } });
      S.recipes = { ...S.recipes, recipes: d.recipes, groups: d.groups, available: d.available };
      if (action === 'remove') { toast('Removed.'); recipeList(body); return; }
      recipeDetail(body, x.id, d.message);
    } catch (e) { failed(e); say(e.message, true); btns.forEach((b) => { b.disabled = !x.ready; }); }
  };
  const act = (label, cls, action, always) => h('button', { class: 'btn act ' + cls, type: 'button', text: label, disabled: x.ready || always ? null : '', onclick: () => send(action) });
  const buttons = [x.added ? act('Save changes', 'primary', 'add') : act('Add', 'primary', 'add'),
    x.kind === 'schedule' ? act('Try it now', '', 'now') : null,
    x.added ? act('Remove', '', 'remove', true) : null];
  body.replaceChildren(
    h('button', { class: 'btn small', type: 'button', text: '‹ All recipes', onclick: () => recipeList(body) }),
    h('div', { class: 'card' }, h('h2', null, x.title, h('small', { text: x.group })), h('div', { class: 'field-note', text: x.summary }),
      x.ready ? null : h('div', { class: 'recipe-note bad', text: 'This needs ' + x.missing.join(' and ') + ' first.' }, ' ', h('a', { href: '#settings/setup', onclick: () => closePanel(), text: 'Open Setup' })),
      at ? h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Runs ' + x.days + ' at' }), at) : null,
      ...fields,
      h('details', { class: 'recipe-prompt' }, h('summary', { text: 'What it will do, in full' }), h('pre', { text: x.prompt })),
      done ? h('div', { class: 'recipe-note ok', text: done }) : null, note,
      h('div', { class: 'btn-row' }, ...buttons.filter(Boolean))));
}


// ---------- the model routine jobs use ----------
// Scheduled prompts (reminders, the morning brief, recipes) that name no model run on this one, so routine work uses a
// cheaper model than the chat does. Automatic picks a profile marked cheap, if there is one.
function routineCard(s) {
  const ps = (s.profiles || []).filter((p) => p.available);
  if (ps.length < 2) return null; // with one model there is nothing to choose between
  const sel = h('select', { class: 'rule-select', 'aria-label': 'Model for routine jobs' },
    h('option', { value: '', text: 'Automatic' }), h('option', { value: 'default', text: 'Same as the chat' }), ps.map((p) => h('option', { value: p.name, text: p.name + (p.model ? ' (' + p.model + ')' : '') })));
  sel.value = s.routine_profile || '';
  sel.addEventListener('change', async () => {
    sel.disabled = true;
    try { await api('/api/settings', { method: 'POST', body: { routine_profile: sel.value } }); toast('Saved. Routine jobs use it from their next run.'); }
    catch (e) { failed(e); }
    loadSettings();
  });
  const using = s.routine_using ? 'Routine jobs run on ' + s.routine_using + '.' : 'Routine jobs run on the same model as the chat.';
  return h('div', { class: 'card' }, h('h2', null, 'Routine jobs'),
    h('div', { class: 'field-note', text: 'Reminders, the morning brief and recipes run by themselves, and rarely need your strongest model. ' + using + ' A job that names its own model keeps it, and background tasks you start use the chat’s model.' }), sel);
}

// ---------- voice notes ----------
// The microphone button records, turns the recording into a small mono 16 kHz WAV in the browser (every speech service
// takes that, whatever the phone records in), and the server turns it into words with the speech service chosen in
// Settings, Voice. The words land in the message box to check, or are sent at once if you chose that. Nothing is
// stored: not on the phone, not on the server. With no speech service the button uses the browser's own dictation,
// where it has one, as before.
const VOICE = { cur: null, busy: false, wired: false };
const SpeechRec = window.SpeechRecognition || window.webkitSpeechRecognition;
function voiceServer() { return !!(S.voice && S.voice.available && navigator.mediaDevices && navigator.mediaDevices.getUserMedia && window.MediaRecorder && (window.AudioContext || window.webkitAudioContext)); }
async function loadVoice() {
  try { S.voice = await api('/api/voice'); } catch (e) { if (e instanceof AuthError) return; }
  initMic();
}
function initMic() {
  const mic = $('#mic'); if (!mic) return;
  mic.hidden = !(voiceServer() || SpeechRec);
  mic.setAttribute('aria-label', voiceServer() ? 'Record a voice note' : 'Dictate');
  if (VOICE.wired) return;
  VOICE.wired = true;
  let dictating = null;
  mic.addEventListener('click', () => {
    if (voiceServer()) { if (VOICE.busy) return; if (VOICE.cur) stopVoice(); else startVoice(); return; }
    if (!SpeechRec) return;
    if (dictating) { dictating.stop(); return; }
    const rec = dictating = new SpeechRec(); rec.lang = navigator.language; rec.interimResults = false; rec.continuous = false;
    mic.classList.add('live');
    rec.onresult = (e) => { input.value = (input.value + ' ' + [...e.results].map((r) => r[0].transcript).join(' ')).trim(); autosize(); renderChrome(); };
    rec.onend = rec.onerror = () => { dictating = null; mic.classList.remove('live'); };
    try { rec.start(); } catch { dictating = null; mic.classList.remove('live'); }
  });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && VOICE.cur) cancelVoice(); });
}
function voiceClock(ms) { const s = Math.floor(ms / 1000); return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0'); }
function voiceBar(content) {
  const bar = $('#voicebar'); if (!bar) return;
  if (!content) { bar.hidden = true; bar.replaceChildren(); return; }
  bar.hidden = false; bar.replaceChildren(content);
}
async function startVoice() {
  let stream;
  try { stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } }); }
  catch (e) { toast(e && e.name === 'NotAllowedError' ? 'The microphone is blocked. Allow it for this site in the browser or phone settings.' : 'No microphone could be opened.'); return; }
  const chunks = [];
  const rec = new MediaRecorder(stream);
  const cur = VOICE.cur = { rec, stream, chunks, t0: Date.now(), cancelled: false, timer: 0 };
  rec.ondataavailable = (e) => { if (e.data && e.data.size) chunks.push(e.data); };
  rec.onstop = () => finishVoice(cur);
  const label = h('span', { class: 'grow', text: 'Recording 0:00' });
  voiceBar(h('div', { class: 'voice-row' }, h('span', { class: 'rec-dot', 'aria-hidden': 'true' }), label,
    h('button', { class: 'btn small', type: 'button', text: 'Cancel', onclick: cancelVoice }), h('button', { class: 'btn small primary', type: 'button', text: 'Done', onclick: stopVoice })));
  const max = ((S.voice && S.voice.max_seconds) || 120) * 1000;
  cur.timer = setInterval(() => {
    const ms = Date.now() - cur.t0;
    label.textContent = 'Recording ' + voiceClock(ms) + ' of ' + voiceClock(max);
    if (ms >= max) { toast('That is the longest a voice note can be.'); stopVoice(); }
  }, 250);
  $('#mic').classList.add('live'); $('#mic').setAttribute('aria-label', 'Stop recording');
  rec.start();
}
function endRecording(cur) {
  clearInterval(cur.timer);
  for (const t of cur.stream.getTracks()) t.stop(); // the microphone is released the moment recording ends
  $('#mic').classList.remove('live'); $('#mic').setAttribute('aria-label', 'Record a voice note');
}
function stopVoice() { const cur = VOICE.cur; if (!cur || cur.rec.state === 'inactive') return; cur.rec.stop(); }
function cancelVoice() { const cur = VOICE.cur; if (!cur) return; cur.cancelled = true; if (cur.rec.state !== 'inactive') cur.rec.stop(); else finishVoice(cur); }
async function finishVoice(cur) {
  if (VOICE.cur !== cur) return;
  endRecording(cur);
  VOICE.cur = null;
  if (cur.cancelled) { voiceBar(null); return; }
  VOICE.busy = true;
  voiceBar(h('div', { class: 'voice-row' }, icon('spin'), h('span', { class: 'grow', text: 'Turning your voice into text…' })));
  try {
    if (!cur.chunks.length) { toast('Nothing was recorded. Check that the microphone is working.'); return; }
    const wav = await blobToWav(new Blob(cur.chunks, { type: cur.rec.mimeType || 'audio/webm' }));
    if (wav.seconds < 0.4) { toast('That was too short to hear.'); return; }
    if (wav.peak < 0.01) { toast('I did not hear anything. Check that the microphone is working.'); return; }
    const d = await api('/api/transcribe', { method: 'POST', raw: true, body: wav.blob });
    const text = (d.text || '').trim();
    if (!text) { toast('No words were heard.'); return; }
    input.value = (input.value + ' ' + text).trim(); autosize(); renderChrome(); input.focus();
    if (S.voice && S.voice.auto_send) $('#composer').requestSubmit();
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message || 'The voice note could not be turned into text.'); }
  finally { VOICE.busy = false; voiceBar(null); }
}
// A recording in whatever the browser makes (webm, mp4) becomes mono 16-bit 16 kHz PCM in a WAV: small, and the one
// format every speech service reads. peak is how loud it got, 0 to 1, so silence is not sent anywhere.
async function blobToWav(blob) {
  const AC = window.AudioContext || window.webkitAudioContext, OAC = window.OfflineAudioContext || window.webkitOfflineAudioContext;
  const ctx = new AC();
  let buf;
  try { const ab = await blob.arrayBuffer(); buf = await new Promise((ok, no) => ctx.decodeAudioData(ab, ok, no)); }
  catch { throw new Error('This browser could not read the recording.'); }
  finally { ctx.close && ctx.close(); }
  const rate = 16000, off = new OAC(1, Math.max(1, Math.ceil(buf.duration * rate)), rate);
  const src = off.createBufferSource(); src.buffer = buf; src.connect(off.destination); src.start();
  const out = await off.startRendering();
  return encodeWav(out.getChannelData(0), rate);
}
function encodeWav(samples, rate) {
  const n = samples.length, view = new DataView(new ArrayBuffer(44 + n * 2));
  const str = (o, t) => { for (let i = 0; i < t.length; i++) view.setUint8(o + i, t.charCodeAt(i)); };
  str(0, 'RIFF'); view.setUint32(4, 36 + n * 2, true); str(8, 'WAVE'); str(12, 'fmt ');
  view.setUint32(16, 16, true); view.setUint16(20, 1, true); view.setUint16(22, 1, true); view.setUint32(24, rate, true);
  view.setUint32(28, rate * 2, true); view.setUint16(32, 2, true); view.setUint16(34, 16, true); str(36, 'data'); view.setUint32(40, n * 2, true);
  let peak = 0;
  for (let i = 0; i < n; i++) {
    const v = Math.max(-1, Math.min(1, samples[i]));
    peak = Math.max(peak, Math.abs(v));
    view.setInt16(44 + i * 2, v < 0 ? v * 0x8000 : v * 0x7fff, true);
  }
  return { blob: new Blob([view], { type: 'audio/wav' }), peak, seconds: n / rate };
}

// ---------- settings: voice ----------
async function saveVoice(body, done) {
  try { S.voice = await api('/api/voice', { method: 'POST', body }); if (done) toast(done); }
  catch (e) { failed(e); return false; }
  initMic(); renderSettings();
  return true;
}
function paneVoice() {
  const v = S.voice;
  if (!v) return [h('div', { class: 'card empty', text: 'Voice settings are not available here.' })];
  const presets = v.presets || [];
  const text = (id, label, value, ph, type) => h('div', { class: 'field' }, h('label', { class: 'field-label', for: id, text: label }), h('input', { id, type: type || 'text', value: value || '', placeholder: ph || '', autocomplete: 'off', autocapitalize: 'none', spellcheck: 'false' }));
  const status = v.available
    ? h('div', { class: 'verdict ok' }, icon('check'), 'On: ' + (v.provider === 'muse' ? 'Meta Muse' : 'speech service') + ', model ' + v.model + (v.has_key ? ' · key from ' + v.key_source : ''))
    : h('div', { class: 'note', text: SpeechRec ? 'Not set up. The microphone button uses your browser’s own dictation until you add a service.' : 'Not set up. Your browser has no dictation of its own, so the microphone button is hidden until you add a service.' });
  const pick = h('select', { id: 'voice-preset', class: 'rule-select', 'aria-label': 'Speech service' }, h('option', { value: '', text: 'Choose a service…' }), presets.map((p) => h('option', { value: p.id, text: p.label })));
  const note = h('div', { class: 'field-note', text: '' });
  const set = (id, val) => { const el = $('#' + id); if (el) el.value = val || ''; };
  pick.addEventListener('change', () => {
    const p = presets.find((x) => x.id === pick.value); if (!p) return;
    voiceProvider = p.provider; set('voice-base', p.base_url); set('voice-model', p.model); set('voice-keyenv', p.key_env);
    note.textContent = p.note || 'Your voice goes to this service, with your key. Talaria stores neither.';
  });
  let voiceProvider = v.provider;
  const auto = h('input', { type: 'checkbox', checked: !!v.auto_send });
  const read = () => ({
    provider: voiceProvider, base_url: $('#voice-base').value.trim(), model: $('#voice-model').value.trim(), api_key: $('#voice-key').value,
    api_key_env: $('#voice-keyenv').value.trim(), language: $('#voice-lang').value.trim(), vocabulary: $('#voice-vocab').value.trim(),
    auto_send: auto.checked, max_seconds: Number($('#voice-max').value) || 0,
  });
  const out = [h('div', { class: 'card' }, h('h2', null, 'Speech to text'), status,
    h('div', { class: 'field-note', text: 'Record a voice note with the microphone button and it is typed out for you. Talaria stores no audio and no words: the recording goes to the service you choose, with your key, and is dropped when it is done.' }),
    h('div', { class: 'field' }, h('label', { class: 'field-label', for: 'voice-preset', text: 'Service' }), pick), note,
    text('voice-base', 'Address', v.base_url, 'https://…'), text('voice-model', 'Model', v.model, 'whisper-1'),
    text('voice-key', 'API key', '', v.has_key ? 'saved: leave blank to keep it' : 'paste it here', 'password'),
    text('voice-keyenv', 'Or the name of a variable that holds it', v.api_key_env, 'OPENAI_API_KEY'),
    text('voice-lang', 'Language (optional)', v.language, 'en  (Muse: English)'), text('voice-vocab', 'Names and terms to listen for (optional)', v.vocabulary, 'Talaria, Ayo, Lagos'),
    text('voice-max', 'Longest voice note, in seconds', String(v.max_seconds || 120), '120', 'number'),
    h('label', { class: 'opt' + (auto.checked ? ' on' : '') }, auto, h('span', { class: 'grow' }, h('b', { text: 'Send it as soon as it is typed out' }), h('span', { class: 's', text: 'Off by default: the words wait in the message box so you can check them first.' }))),
    h('div', { class: 'field-note', text: 'Save first, then Test: the test sends a second of silence to the saved service and checks that it answers.' }),
    h('div', { class: 'btn-row' },
      h('button', { class: 'btn primary', type: 'button', text: 'Save', onclick: () => saveVoice(read(), 'Saved. It applies to your next voice note.') }),
      h('button', { class: 'btn', type: 'button', text: 'Test', onclick: async (e) => {
        const b = e.currentTarget; b.disabled = true; b.textContent = 'Testing…';
        try { const r = await api('/api/voice/test', { method: 'POST', body: {} }); toast(r.ok ? 'It works.' : r.detail); const n = $('#voice-result'); n.textContent = r.detail; n.className = 'recipe-note ' + (r.ok ? 'ok' : 'bad'); n.hidden = false; }
        catch (err) { failed(err); }
        b.disabled = false; b.textContent = 'Test';
      } }),
      v.available || v.base_url ? h('button', { class: 'btn', type: 'button', text: 'Turn off', onclick: () => saveVoice({ disconnect: true }, 'Voice notes are off.') }) : null),
    h('div', { id: 'voice-result', class: 'recipe-note', hidden: '' }))];
  return out;
}
