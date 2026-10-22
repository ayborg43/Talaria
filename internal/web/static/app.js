'use strict';
/* Talaria's web app. Everything shown that came from outside (mail, calendars, the model, the web) is put
   on the page as text, never as markup, so nothing a stranger writes can run or restyle anything. */

const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];

// ---------- small helpers ----------
function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'text') el.textContent = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  add(el, kids);
  return el;
}
function add(el, kids) {
  for (const k of kids.flat(Infinity)) {
    if (k == null || k === false) continue;
    el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  }
}
const ICONS = {
  check: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>',
  x: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>',
  spin: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" aria-hidden="true"><path d="M12 3a9 9 0 1 0 9 9"/></svg>',
  shield: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z"/><path d="M9 12l2 2 4-4"/></svg>',
  info: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 8v5M12 16.5v.01"/></svg>',
  eye: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.8"/></svg>',
  chev: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" width="20" height="20"><path d="M9 6l6 6-6 6"/></svg>',
};
function icon(name) {
  const t = document.createElement('template');
  t.innerHTML = ICONS[name]; // fixed strings above, never anything from outside
  return t.content.firstChild;
}
const wait = (ms) => new Promise((r) => setTimeout(r, ms));
const standalone = () => window.matchMedia('(display-mode: standalone)').matches || navigator.standalone === true;
const isIOS = () => /iphone|ipad|ipod/i.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);

class AuthError extends Error {}
async function api(path, opts = {}) {
  const init = { method: opts.method || 'GET', headers: {}, credentials: 'same-origin' };
  if (init.method !== 'GET') {
    init.headers['X-Talaria'] = '1';
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.body || {});
  }
  let r;
  try { r = await fetch(path, init); } catch { throw new Error('You appear to be offline.'); }
  if (r.status === 401 && path !== '/api/login') { showLogin(); throw new AuthError(); }
  if (!r.ok) {
    let msg = 'Something went wrong.';
    try { msg = (await r.json()).error || msg; } catch { /* keep the generic message */ }
    throw new Error(msg);
  }
  return r.status === 204 ? null : r.json();
}

let toastTimer;
function toast(text) {
  let t = $('#toast');
  if (!t) { t = h('div', { id: 'toast', class: 'toast', role: 'status' }); document.body.append(t); }
  t.textContent = text; t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), 3500);
}

// ---------- state ----------
const S = {
  msgs: new Map(), order: [], epoch: 0, seq: 0, es: null, connected: false, running: false,
  model: '', paused: false, zone: undefined, version: '', tab: 'chat', needs: 'wait',
  today: null, control: null, blocks: new Map(), retry: 1000, installEvent: null,
};

const fmtTime = (d) => new Intl.DateTimeFormat([], { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone: S.zone }).format(d);
const dayKey = (d) => new Intl.DateTimeFormat('en-CA', { timeZone: S.zone }).format(d);
const fmtDay = (d) => new Intl.DateTimeFormat([], { weekday: 'short', day: 'numeric', month: 'short', timeZone: S.zone }).format(d);
function fmtWhen(d) { return dayKey(d) === dayKey(new Date()) ? fmtTime(d) : fmtDay(d); }

// ---------- sign in ----------
function showLogin() {
  S.es && S.es.close(); S.es = null;
  $('#app').hidden = true; $('#login').hidden = false;
  setTimeout(() => $('#password').focus(), 50);
}
$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const err = $('#login-error'); err.hidden = true;
  const btn = e.submitter || $('#login-form button'); btn.disabled = true;
  try {
    await api('/api/login', { method: 'POST', body: { password: $('#password').value } });
    $('#password').value = '';
    await renderInstall();
start();
  } catch (x) { err.textContent = x.message; err.hidden = false; }
  btn.disabled = false;
});

// ---------- markdown (small, and safe: it only ever builds elements and text) ----------
const INLINE = /(`[^`\n]+`|\*\*[^*\n]+\*\*|https?:\/\/[^\s<>]+[^\s<>.,;:!?'")\]])/g;
function inline(text) {
  const out = []; let last = 0, m;
  INLINE.lastIndex = 0;
  while ((m = INLINE.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const t = m[0];
    if (t.startsWith('`')) out.push(h('code', { text: t.slice(1, -1) }));
    else if (t.startsWith('**')) out.push(h('b', { text: t.slice(2, -2) }));
    else out.push(h('a', { href: t, target: '_blank', rel: 'noopener noreferrer', text: t })); // shows the address it goes to
    last = m.index + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
function prose(text, frag) {
  let para = [], list = null;
  const flushPara = () => { if (para.length) { frag.append(h('p', null, ...para.flatMap((l, i) => i ? [document.createElement('br'), ...inline(l)] : inline(l)))); para = []; } };
  const flushList = () => { if (list) { frag.append(list); list = null; } };
  for (const line of text.split('\n')) {
    let m;
    if ((m = /^\s*[-*•]\s+(.*)/.exec(line)) || (m = /^\s*\d+[.)]\s+(.*)/.exec(line))) {
      flushPara();
      const ordered = /^\s*\d/.test(line);
      if (!list || (list.tagName === 'OL') !== ordered) { flushList(); list = h(ordered ? 'ol' : 'ul'); }
      list.append(h('li', null, ...inline(m[1])));
    } else if ((m = /^#{1,4}\s+(.*)/.exec(line))) {
      flushPara(); flushList(); frag.append(h('h4', null, ...inline(m[1])));
    } else if (!line.trim()) { flushPara(); flushList(); }
    else { flushList(); para.push(line); }
  }
  flushPara(); flushList();
}
function markdown(text) {
  const frag = document.createDocumentFragment();
  text.split('```').forEach((part, i) => {
    if (i % 2 === 1) {
      const nl = part.indexOf('\n');
      frag.append(h('pre', null, h('code', { text: (nl >= 0 ? part.slice(nl + 1) : part).replace(/\n$/, '') })));
    } else prose(part, frag);
  });
  return frag;
}

// ---------- the conversation ----------
function upsert(m) {
  if (!S.msgs.has(m.id)) S.order.push(m.id);
  S.msgs.set(m.id, m);
  if (S.order.length > 500) S.msgs.delete(S.order.shift());
}
function pendingAsks() { return S.order.map((id) => S.msgs.get(id)).filter((m) => m.role === 'approval' && m.state === 'pending'); }
function toolRunning() { return S.order.some((id) => { const m = S.msgs.get(id); return m.role === 'tool' && m.state === 'running'; }); }
function working() { return S.running; }

function groupBlocks() {
  const out = [];
  for (const id of S.order) {
    const m = S.msgs.get(id);
    if (m.role === 'tool') {
      const last = out[out.length - 1];
      if (last && last.kind === 'tools') { last.items.push(m); continue; }
      out.push({ kind: 'tools', key: 't' + m.id, items: [m] });
    } else out.push({ kind: m.role, key: 'm' + m.id, m });
  }
  return out;
}

function parseAsk(text) {
  const first = text.split('\n')[0];
  const fence = /```[^\n]*\n([\s\S]*?)\n```/.exec(text);
  let note = fence ? text.slice(fence.index + fence[0].length).trim() : '';
  let code = fence ? fence[1] : text.slice(first.length).trim();
  return { title: first, code, note };
}

function askCard(m, opts = {}) {
  const { title, code, note } = parseAsk(m.text);
  const pending = m.state === 'pending';
  const card = h('div', { class: 'ask' + (pending ? '' : ' done') });
  card.append(h('div', { class: 'ask-head' },
    h('span', { class: 'ask-icon' }, icon('shield')),
    h('span', { class: 'ask-title', text: title }),
    pending && h('span', { class: 'pill brass', text: 'WAITING' })));
  if (code) card.append(h('div', { class: 'code', text: code }));
  if (pending) {
    const answer = async (choice, btns) => {
      btns.forEach((b) => (b.disabled = true));
      try { await api('/api/approve', { method: 'POST', body: { token: m.token, choice } }); }
      catch (e) { if (!(e instanceof AuthError)) toast(e.message); btns.forEach((b) => (b.disabled = false)); }
    };
    const btns = [];
    const mk = (label, cls, choice) => { const b = h('button', { class: 'btn ' + cls, type: 'button', text: label, onclick: () => answer(choice, btns) }); btns.push(b); return b; };
    card.append(h('div', { class: 'btn-row' }, mk('Deny', '', 'deny'), m.always && mk('Always', '', 'always'), mk('Allow', 'primary', 'allow')));
  } else {
    const v = { allowed: ['ok', 'Allowed'], always: ['ok', 'Allowed, and always allowed'], denied: ['bad', 'Denied'], expired: ['quiet', note || 'No longer waiting'] }[m.state] || ['quiet', m.state];
    card.append(h('div', { class: 'verdict ' + v[0] }, icon(v[0] === 'bad' ? 'x' : v[0] === 'ok' ? 'check' : 'info'), v[1]));
  }
  return card;
}

function buildBlock(b, isLast) {
  if (b.kind === 'user') return h('div', { class: 'msg-user', text: b.m.text });
  if (b.kind === 'tools') {
    return h('div', { class: 'chips' }, b.items.map((t) => h('span', { class: 'chip ' + t.state },
      icon(t.state === 'ok' ? 'check' : t.state === 'error' ? 'x' : 'spin'), t.tool,
      t.ms ? h('em', { text: (t.ms / 1000).toFixed(1) + 's' }) : null)));
  }
  if (b.kind === 'approval') return askCard(b.m);
  const streaming = isLast && working();
  const body = h('div', { class: 'msg-bot' + (streaming ? ' cursor' : '') });
  body.append(markdown(b.m.text || ''));
  return body;
}
function signature(b, isLast) {
  if (b.kind === 'tools') return b.items.map((t) => t.state + t.ms).join();
  if (b.kind === 'assistant') return b.m.text + (isLast && working() ? '|' : '');
  return b.m.text + '|' + b.m.state;
}

let renderQueued = false;
function renderChat() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => { renderQueued = false; doRenderChat(); });
}
function doRenderChat() {
  const thread = $('#thread');
  const stick = S.fresh || thread.scrollHeight - thread.scrollTop - thread.clientHeight < 90;
  S.fresh = false;
  const blocks = groupBlocks();
  const keep = new Set();
  let prev = null;
  blocks.forEach((b, i) => {
    const last = i === blocks.length - 1;
    const sig = signature(b, last);
    let rec = S.blocks.get(b.key);
    if (!rec || rec.sig !== sig) {
      const el = buildBlock(b, last);
      if (rec) rec.el.replaceWith(el);
      rec = { el, sig };
      S.blocks.set(b.key, rec);
    }
    keep.add(b.key);
    if (prev) { if (prev.nextSibling !== rec.el) prev.after(rec.el); }
    else if (thread.firstChild !== rec.el) thread.prepend(rec.el);
    prev = rec.el;
  });
  for (const [k, rec] of S.blocks) if (!keep.has(k)) { rec.el.remove(); S.blocks.delete(k); }

  // while it works and nothing is streaming yet, show that it is thinking
  let dots = $('#thinking');
  const lastBlock = blocks[blocks.length - 1];
  const need = working() && (!lastBlock || lastBlock.kind !== 'assistant' || !lastBlock.m.text);
  if (need && !dots) thread.append(h('div', { id: 'thinking', class: 'typing', 'aria-label': 'Working' }, h('i'), h('i'), h('i')));
  else if (!need && dots) dots.remove();
  else if (need && dots) thread.append(dots);

  if (stick) thread.scrollTop = thread.scrollHeight;
  $('#jump').hidden = stick || thread.scrollHeight - thread.scrollTop - thread.clientHeight < 200;
  renderChrome();
}
$('#thread').addEventListener('scroll', () => {
  const t = $('#thread');
  $('#jump').hidden = t.scrollHeight - t.scrollTop - t.clientHeight < 200;
}, { passive: true });
$('#jump').addEventListener('click', () => { const t = $('#thread'); t.scrollTo({ top: t.scrollHeight, behavior: 'smooth' }); });

// header, badge, composer buttons
function renderChrome() {
  const run = working();
  $('#logo').classList.toggle('work', run);
  const dot = $('#dot');
  dot.className = 'dot ' + (!S.connected ? 'off' : run ? 'work' : 'on');
  $('#status-text').textContent = !S.connected ? 'Reconnecting…' : S.paused ? 'Paused · chat still works' : run ? 'Working…' : 'Online' + (S.model ? ' · ' + S.model : '');
  const n = pendingAsks().length;
  const badge = $('#badge'); badge.hidden = n === 0; badge.textContent = String(n);
  const hasText = $('#input').value.trim().length > 0;
  $('#send').hidden = run && !hasText;
  $('#stop').hidden = !(run && !hasText);
  $('#send').disabled = !hasText;
  $('#quick').hidden = run || hasText;
  document.title = n ? `(${n}) Talaria` : 'Talaria';
  if (S.tab === 'needs') renderNeeds();
}

// ---------- composer ----------
const input = $('#input');
function autosize() { input.style.height = 'auto'; input.style.height = Math.min(input.scrollHeight, 140) + 'px'; }
input.addEventListener('input', () => { autosize(); renderChrome(); });
input.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && !matchMedia('(pointer: coarse)').matches) { e.preventDefault(); $('#composer').requestSubmit(); }
});
async function sendText(text) {
  text = text.trim();
  if (!text) return;
  try { await api('/api/send', { method: 'POST', body: { text } }); }
  catch (e) { if (!(e instanceof AuthError)) toast(e.message); return false; }
  return true;
}
$('#composer').addEventListener('submit', async (e) => {
  e.preventDefault();
  const text = input.value;
  if (!text.trim()) return;
  input.value = ''; autosize(); renderChrome();
  if (!(await sendText(text))) { input.value = text; autosize(); renderChrome(); }
});
$('#stop').addEventListener('click', () => sendText('/stop'));
$('#new-chat').addEventListener('click', () => { if (confirm('Start a new conversation? Talaria forgets this one (your notes and history stay).')) sendText('/new'); });
for (const q of ["What's on today?", 'Summarise my inbox', 'What are you watching for me?']) {
  $('#quick').append(h('button', { type: 'button', text: q, onclick: () => sendText(q) }));
}
// dictation, where the phone offers it
const Rec = window.SpeechRecognition || window.webkitSpeechRecognition;
if (Rec) {
  const mic = $('#mic'); mic.hidden = false;
  let rec = null;
  mic.addEventListener('click', () => {
    if (rec) { rec.stop(); return; }
    rec = new Rec(); rec.lang = navigator.language; rec.interimResults = false; rec.continuous = false;
    mic.classList.add('live');
    rec.onresult = (e) => { input.value = (input.value + ' ' + [...e.results].map((r) => r[0].transcript).join(' ')).trim(); autosize(); renderChrome(); };
    rec.onend = rec.onerror = () => { rec = null; mic.classList.remove('live'); };
    try { rec.start(); } catch { rec = null; mic.classList.remove('live'); }
  });
}

// ---------- connection to the server ----------
async function loadState() {
  const d = await api('/api/state');
  S.msgs.clear(); S.order = [];
  for (const m of d.messages) upsert(m);
  S.epoch = d.epoch; S.seq = d.seq; S.running = d.running; S.model = d.model; S.paused = d.paused; S.version = d.version;
  S.zone = d.zone || S.zone;
  for (const rec of S.blocks.values()) rec.el.remove();
  S.blocks.clear();
  S.fresh = true;
  renderChat();
}
function connect() {
  if (S.es) S.es.close();
  const es = new EventSource(`/api/events?epoch=${S.epoch}&after=${S.seq}`);
  S.es = es;
  es.onopen = () => { S.connected = true; S.retry = 1000; renderChrome(); };
  es.onmessage = (e) => {
    const ev = JSON.parse(e.data);
    if (ev.seq > S.seq) S.seq = ev.seq;
    if (ev.type === 'msg') { upsert(ev.msg); renderChat(); }
    else if (ev.type === 'run') { S.running = !!ev.on; renderChat(); }
    else if (ev.type === 'reset') { loadState().then(connect).catch(() => {}); }
  };
  es.onerror = () => { es.close(); if (S.es === es) S.es = null; S.connected = false; renderChrome(); reconnect(); };
}
let reconnecting = false;
async function reconnect() {
  if (reconnecting) return;
  reconnecting = true;
  try {
    for (;;) {
      await wait(S.retry); S.retry = Math.min(S.retry * 2, 15000);
      try { await loadState(); connect(); return; } catch (e) { if (e instanceof AuthError) return; }
    }
  } finally { reconnecting = false; }
}

// ---------- tabs ----------
const TABS = ['chat', 'today', 'needs', 'control'];
function route() {
  const tab = TABS.includes(location.hash.slice(1)) ? location.hash.slice(1) : 'chat';
  S.tab = tab;
  for (const t of TABS) $('#screen-' + t).hidden = t !== tab;
  $$('#tabs a').forEach((a) => (a.getAttribute('data-tab') === tab ? a.setAttribute('aria-current', 'page') : a.removeAttribute('aria-current')));
  if (tab === 'today') loadToday();
  if (tab === 'needs') renderNeeds();
  if (tab === 'control') loadControl();
  if (tab === 'chat') { const t = $('#thread'); t.scrollTop = t.scrollHeight; }
}
window.addEventListener('hashchange', route);

// ---------- today ----------
const TINTS = [['var(--info-tint)', 'var(--info)'], ['var(--brass-tint)', 'var(--brass-text)'], ['var(--ok-tint)', 'var(--ok)'], ['var(--bad-tint)', 'var(--bad)']];
const BARS = ['var(--brass)', 'var(--info)', 'var(--ok)', 'var(--coral)'];
async function loadToday(fresh) {
  const body = $('#today-body');
  if (!S.today) body.replaceChildren(h('div', { class: 'skeleton' }), h('div', { class: 'skeleton' }));
  try { S.today = await api('/api/today' + (fresh ? '?fresh=1' : '')); S.zone = S.today.zone || S.zone; }
  catch (e) { if (e instanceof AuthError) return; if (!S.today) { body.replaceChildren(h('div', { class: 'card empty', text: e.message })); return; } toast(e.message); }
  renderToday();
}
$('#today-refresh').addEventListener('click', () => loadToday(true));

function setup(text, hint) {
  return h('div', { class: 'card' }, h('div', { class: 'row' }, h('span', { class: 'tag' }, icon('info')), h('span', { class: 'grow' }, h('span', { class: 't', text }), h('span', { class: 's', text: hint }))));
}
function renderToday() {
  const d = S.today; if (!d || S.tab !== 'today') return;
  const now = new Date();
  const hr = Number(new Intl.DateTimeFormat('en-GB', { hour: '2-digit', hourCycle: 'h23', timeZone: S.zone }).format(now));
  $('#today-greeting').textContent = hr < 12 ? 'Good morning.' : hr < 18 ? 'Good afternoon.' : 'Good evening.';
  $('#today-date').textContent = new Intl.DateTimeFormat([], { weekday: 'long', day: 'numeric', month: 'long', timeZone: S.zone }).format(now);
  const out = [];
  const n = pendingAsks().length;
  if (n) out.push(h('a', { class: 'alert-card', href: '#needs' }, h('span', { class: 'ask-icon' }, icon('shield')),
    h('span', { class: 'grow' }, h('b', { text: n === 1 ? '1 thing needs your OK' : n + ' things need your OK' })), icon('chev')));

  // calendar
  if (!d.agendaOn) out.push(setup('Your calendar is not connected', 'Add TALARIA_CALENDARS in your server settings, then restart.'));
  else if (d.agendaError) out.push(setup('Your calendar could not be read', d.agendaError));
  else {
    const ev = (d.agenda || []).map((e) => ({ ...e, s: new Date(e.start), e: new Date(e.end) })).sort((a, b) => a.s - b.s);
    const timed = ev.filter((e) => !e.allDay), allDay = ev.filter((e) => e.allDay);
    out.push(hero(timed, now));
    const card = h('div', { class: 'card' }, h('h2', null, 'Today', h('small', { text: `${ev.length} event${ev.length === 1 ? '' : 's'} · read-only` })));
    if (allDay.length) card.append(h('div', { class: 'allday' }, allDay.map((e) => h('span', { class: 'pill brass', text: e.title }))));
    let marked = false;
    const nowRow = () => h('div', { class: 'nowline' }, h('span', { text: fmtTime(now) }), h('i'), h('b', { text: 'NOW' }));
    timed.forEach((e, i) => {
      if (!marked && e.e > now) { if (e.s > now) card.append(nowRow()); marked = true; }
      card.append(h('div', { class: 'ev' + (e.e <= now ? ' past' : '') },
        h('span', { class: 'tm', text: fmtTime(e.s) }), h('span', { class: 'bar-c' }),
        h('span', { class: 'grow' }, h('span', { class: 't', text: e.title }), h('span', { class: 's', text: [fmtTime(e.s) + '–' + fmtTime(e.e), e.where].filter(Boolean).join(' · ') }))));
      card.lastChild.querySelector('.bar-c').style.background = BARS[i % BARS.length];
    });
    if (!marked && timed.length) card.append(nowRow());
    if (!ev.length) card.append(h('div', { class: 'empty', text: 'Nothing on your calendar today.' }));
    out.push(card);
  }

  // mail
  if (!d.mailOn) out.push(setup('Your email is not connected', 'Add TALARIA_EMAIL and TALARIA_EMAIL_PASSWORD (an app password), then restart.'));
  else if (d.mailError) out.push(setup('Your email could not be read', d.mailError));
  else {
    const mail = d.mail || { unread: 0, items: [] };
    const card = h('div', { class: 'card' }, h('h2', null, 'Inbox', h('small', { class: 'state-info', text: mail.unread ? mail.unread + ' unread' : 'All read' })));
    const rows = h('div', { class: 'rows' });
    mail.items.slice(0, 6).forEach((m, i) => {
      const name = m.from.replace(/\s*<.*>$/, '') || m.from;
      const av = h('span', { class: 'avatar', text: (name.trim()[0] || '?').toUpperCase() });
      av.style.background = TINTS[i % 4][0]; av.style.color = TINTS[i % 4][1];
      rows.append(h('div', { class: 'row' }, av,
        h('span', { class: 'grow' }, h('span', { class: 't', text: name }), h('span', { class: 's', text: m.subject || '(no subject)' })),
        h('span', { class: 'when' + (m.unread ? ' state-info' : ''), text: fmtWhen(new Date(m.at)) })));
    });
    card.append(mail.items.length ? rows : h('div', { class: 'empty', text: 'Nothing new in the last few days.' }));
    out.push(card);
  }

  if (d.jobs && d.jobs.length) out.push(jobsCard(d.jobs));
  $('#today-body').replaceChildren(...out);
}

function hero(timed, now) {
  const cur = timed.find((e) => e.s <= now && now < e.e), next = timed.find((e) => e.s > now);
  const C = 2 * Math.PI * 44;
  const gauge = (frac, big, small) => {
    const ns = 'http://www.w3.org/2000/svg';
    const svg = document.createElementNS(ns, 'svg'); svg.setAttribute('viewBox', '0 0 104 104'); svg.setAttribute('fill', 'none'); svg.setAttribute('aria-hidden', 'true');
    const mk = (cls) => { const c = document.createElementNS(ns, 'circle'); c.setAttribute('cx', 52); c.setAttribute('cy', 52); c.setAttribute('r', 44); c.setAttribute('stroke-width', 7); c.setAttribute('class', cls); return c; };
    const arc = mk('arc'); arc.setAttribute('stroke-linecap', 'round'); arc.setAttribute('transform', 'rotate(-90 52 52)');
    arc.setAttribute('stroke-dasharray', C.toFixed(2)); arc.setAttribute('stroke-dashoffset', C.toFixed(2));
    svg.append(mk('track'), arc);
    setTimeout(() => arc.setAttribute('stroke-dashoffset', (C * (1 - Math.max(0.02, Math.min(1, frac)))).toFixed(2)), 60);
    return h('div', { class: 'gauge' }, svg, h('div', { class: 'gauge-in' }, h('b', { text: big }), h('span', { text: small })));
  };
  const orbit = () => { const ns = 'http://www.w3.org/2000/svg'; const s = document.createElementNS(ns, 'svg'); s.setAttribute('class', 'orbit'); s.setAttribute('viewBox', '0 0 260 260'); s.setAttribute('aria-hidden', 'true'); for (const r of [40, 70, 100, 128]) { const c = document.createElementNS(ns, 'circle'); c.setAttribute('cx', 130); c.setAttribute('cy', 130); c.setAttribute('r', r); s.append(c); } return s; };
  let kick, ev, g;
  if (cur) { kick = 'HAPPENING NOW'; ev = cur; const left = Math.max(0, Math.round((cur.e - now) / 60000)); g = gauge((now - cur.s) / (cur.e - cur.s), String(left), 'MIN LEFT'); }
  else if (next) { kick = 'NEXT UP'; ev = next; const mins = Math.max(0, Math.round((next.s - now) / 60000)); g = gauge(1 - Math.min(mins, 120) / 120, mins >= 120 ? Math.round(mins / 60) + 'h' : String(mins), mins >= 120 ? 'HOURS' : 'MIN'); }
  if (!ev) return h('div', { class: 'hero' }, orbit(), h('div', { class: 'hero-in' }, gauge(1, '✓', 'CLEAR'), h('div', null, h('div', { class: 'kick', text: 'ALL CLEAR' }), h('div', { class: 'big', text: 'Nothing else today' }), h('div', { class: 'sub', text: 'Enjoy the quiet.' }))));
  return h('div', { class: 'hero' }, orbit(), h('div', { class: 'hero-in' }, g, h('div', { class: 'grow' }, h('div', { class: 'kick', text: kick }), h('div', { class: 'big', text: ev.title }), h('div', { class: 'sub', text: [fmtTime(ev.s) + ' – ' + fmtTime(ev.e), ev.where].filter(Boolean).join(' · ') }))));
}
setInterval(() => { if (S.tab === 'today' && S.today && !document.hidden) renderToday(); }, 30000);

function jobsCard(jobs) {
  const card = h('div', { class: 'card' }, h('h2', null, 'Working for you'));
  const rows = h('div', { class: 'rows' });
  for (const j of jobs) {
    const kind = j.kind.toLowerCase();
    rows.append(h('div', { class: 'row' }, h('span', { class: 'tag ' + kind, text: j.kind }),
      h('span', { class: 'grow' }, h('span', { class: 't', text: j.title }), h('span', { class: 's', text: j.sub })),
      h('span', { class: 'state ' + (j.state === 'Paused' ? 'state-brass' : j.state === 'Active' || j.state === 'Waiting' ? 'state-ok' : 'state-quiet'), text: j.state })));
  }
  card.append(rows);
  return card;
}

// ---------- needs you ----------
$('#seg-wait').addEventListener('click', () => { S.needs = 'wait'; renderNeeds(); });
$('#seg-hist').addEventListener('click', () => { S.needs = 'hist'; renderNeeds(); });
function renderNeeds() {
  if (S.tab !== 'needs') return;
  const pend = pendingAsks();
  $('#seg-wait').textContent = 'Waiting · ' + pend.length; $('#seg-wait').setAttribute('aria-selected', S.needs === 'wait');
  $('#seg-hist').setAttribute('aria-selected', S.needs === 'hist');
  const out = [];
  if (S.needs === 'wait') {
    out.push(h('div', { class: 'banner' }, icon('info'), h('span', { text: 'Text from mail, calendars and the web is written by other people and can try to give orders. So whatever could send something out, or run a command, asks you first.' })));
    if (!pend.length) out.push(h('div', { class: 'card empty' }, h('div', { text: 'Nothing is waiting for you.' }), h('div', { class: 'note', text: 'I will tell you here, and with a notification if you turn them on, when something needs your OK.' })));
    pend.forEach((m) => out.push(askCard(m)));
  } else {
    const done = S.order.map((id) => S.msgs.get(id)).filter((m) => m.role === 'approval' && m.state !== 'pending').reverse().slice(0, 40);
    if (!done.length) out.push(h('div', { class: 'card empty', text: 'Nothing decided yet.' }));
    else {
      const rows = h('div', { class: 'rows' });
      for (const m of done) {
        const bad = m.state === 'denied', expired = m.state === 'expired';
        const mark = h('span', { class: 'avatar' }, icon(bad ? 'x' : expired ? 'info' : 'check'));
        mark.style.width = mark.style.height = '30px'; mark.style.borderRadius = '99px';
        mark.style.background = bad ? 'var(--bad-tint)' : expired ? 'var(--line2)' : 'var(--ok-tint)'; mark.style.color = bad ? 'var(--bad)' : expired ? 'var(--muted)' : 'var(--ok)';
        rows.append(h('div', { class: 'row' }, mark,
          h('span', { class: 'grow' }, h('span', { class: 't', text: parseAsk(m.text).title }), h('span', { class: 's', text: { allowed: 'You allowed it', always: 'You always allow it', denied: 'You denied it', expired: 'It ran out of time' }[m.state] })),
          h('span', { class: 'when', text: fmtWhen(new Date(m.at)) })));
      }
      out.push(h('div', { class: 'card' }, rows));
    }
  }
  $('#needs-body').replaceChildren(...out);
}

// ---------- control ----------
async function loadControl() {
  try { S.control = await api('/api/control'); S.paused = S.control.paused; }
  catch (e) { if (!(e instanceof AuthError)) toast(e.message); return; }
  renderControl();
}
function uptime(sec) {
  const d = Math.floor(sec / 86400), hh = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  return d ? `${d} d ${hh} h` : hh ? `${hh} h ${m} min` : `${m} min`;
}
let pauseArmed = false, pauseTimer;
async function renderControl() {
  const c = S.control; if (!c || S.tab !== 'control') return;
  const out = [];
  const health = h('div', { class: 'health' + (c.paused ? ' paused' : '') });
  health.append(h('div', { class: 'health-top' }, h('span', { class: 'light' }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: c.paused ? 'Paused' : 'All systems normal' }),
      h('span', { class: 's', text: c.paused ? 'Goals, watches and reminders are held until you resume' : `Up ${uptime(c.uptimeSec)} · ${c.model || 'model'}` }))));
  const max = Math.max(1, ...c.activity), total = c.activity.reduce((a, b) => a + b, 0);
  const bars = h('div', { class: 'bars', role: 'img', 'aria-label': `Activity over the last 24 hours: ${total} actions` });
  c.activity.forEach((v, i) => { const b = h('i', { class: i === c.activity.length - 1 ? 'now' : '' }); b.style.height = Math.max(5, (v / max) * 100) + '%'; b.style.animationDelay = i * 15 + 'ms'; bars.append(b); });
  health.append(h('div', null, h('div', { class: 'chart-h' }, h('span', { text: 'ACTIVITY · LAST 24 H' }), h('span', { text: total + ' actions' })), bars, h('div', { class: 'chart-f' }, h('span', { text: '24 h ago' }), h('span', { text: 'now' }))));
  const btn = h('button', { class: 'btn big ' + (c.paused ? 'primary' : 'danger'), type: 'button', text: c.paused ? 'Resume everything' : pauseArmed ? 'Tap again to pause everything' : 'Pause everything' });
  btn.addEventListener('click', async () => {
    if (!c.paused && !pauseArmed) { pauseArmed = true; clearTimeout(pauseTimer); pauseTimer = setTimeout(() => { pauseArmed = false; renderControl(); }, 4000); renderControl(); return; }
    pauseArmed = false; btn.disabled = true;
    try { const r = await api('/api/pause', { method: 'POST', body: { paused: !c.paused } }); S.paused = r.paused; }
    catch (e) { if (!(e instanceof AuthError)) toast(e.message); }
    loadControl(); renderChrome();
  });
  health.append(btn);
  out.push(health);

  out.push(await notifyCard(c));
  if (c.jobs.length) out.push(jobsCard(c.jobs));
  else out.push(h('div', { class: 'card' }, h('h2', null, 'Working for you'), h('div', { class: 'empty', text: 'No goals, watches or reminders yet. Ask me in chat.' })));
  if (c.links.length) {
    const rows = h('div', { class: 'rows' });
    for (const l of c.links) rows.append(h('div', { class: 'row' }, h('span', { class: 'link-dot' }), h('span', { class: 'grow' }, h('span', { class: 't', text: l.name }), h('span', { class: 's mono', text: l.detail })), h('span', { class: 'pill info', text: l.tag })));
    out.push(h('div', { class: 'card' }, h('h2', null, 'Connected'), rows));
  }
  const inst = installCard(); if (inst) out.push(inst);
  out.push(h('button', { class: 'btn big', type: 'button', text: 'Sign out', onclick: async () => { try { await api('/api/logout', { method: 'POST' }); } catch { /* signed out anyway */ } showLogin(); } }));
  out.push(h('div', { class: 'foot', text: 'Talaria ' + (c.version || '') }));
  $('#control-body').replaceChildren(...out);
}

// ---------- notifications and installing ----------
function b64bytes(s) { s = s.replace(/-/g, '+').replace(/_/g, '/'); const bin = atob(s + '='.repeat((4 - (s.length % 4)) % 4)); return Uint8Array.from(bin, (c) => c.charCodeAt(0)); }
async function pushState() {
  if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) return isIOS() && !standalone() ? 'needs-install' : 'unsupported';
  if (isIOS() && !standalone()) return 'needs-install';
  if (Notification.permission === 'denied') return 'denied';
  try { const reg = await navigator.serviceWorker.ready; return (await reg.pushManager.getSubscription()) ? 'on' : 'off'; } catch { return 'off'; }
}
async function enablePush() {
  if ((await Notification.requestPermission()) !== 'granted') { toast('Notifications were not allowed.'); return; }
  try {
    const { key } = await api('/api/push/key');
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64bytes(key) });
    await api('/api/push/subscribe', { method: 'POST', body: sub.toJSON() });
    toast('Notifications are on.');
  } catch (e) { if (!(e instanceof AuthError)) toast('Could not turn notifications on: ' + e.message); }
}
async function disablePush() {
  try {
    const reg = await navigator.serviceWorker.ready; const sub = await reg.pushManager.getSubscription();
    if (sub) { await api('/api/push/unsubscribe', { method: 'POST', body: { endpoint: sub.endpoint } }); await sub.unsubscribe(); }
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message); }
}
async function notifyCard(c) {
  const st = await pushState();
  const card = h('div', { class: 'card' }, h('h2', null, 'Notifications'));
  const done = () => { loadControl(); };
  if (!c.push.available) card.append(h('div', { class: 'note', text: 'Notifications are not set up on the server.' }));
  else if (st === 'on') card.append(h('div', { class: 'verdict ok' }, icon('check'), 'On for this device'), h('p', { class: 'field-note', text: 'I will notify you when something needs your OK, and when I have something to tell you while the app is closed.' }), h('p', null, h('button', { class: 'btn', type: 'button', text: 'Turn off', onclick: async () => { await disablePush(); done(); } })));
  else if (st === 'needs-install') card.append(h('div', { class: 'note', text: 'On iPhone, notifications work once Talaria is on your Home Screen:' }), h('ol', { class: 'steps' }, h('li', { text: 'Tap the Share button in Safari.' }), h('li', { text: 'Choose “Add to Home Screen”.' }), h('li', { text: 'Open Talaria from there, and come back to this screen.' })));
  else if (st === 'denied') card.append(h('div', { class: 'note', text: 'Notifications are blocked for this site. Allow them in your browser or phone settings, then come back.' }));
  else if (st === 'unsupported') card.append(h('div', { class: 'note', text: 'This browser cannot show notifications.' }));
  else card.append(h('div', { class: 'note', text: 'Get a notification when something needs your OK, even when the app is closed.' }), h('p', null, h('button', { class: 'btn primary big', type: 'button', text: 'Turn on notifications', onclick: async (e) => { e.target.disabled = true; await enablePush(); done(); } })));
  return card;
}
window.addEventListener('beforeinstallprompt', (e) => { e.preventDefault(); S.installEvent = e; renderInstall(); if (S.tab === 'control') renderControl(); });
window.addEventListener('appinstalled', () => { S.installEvent = null; renderInstall(); if (S.tab === 'control') renderControl(); });

// A banner at the top, so installing is never buried: a button where the browser allows it, and where it does not
// (Safari) the two taps that do it. It goes away once the app is installed or for as long as this page stays open.
let installDismissed = false;
function renderInstall() {
  const bar = $('#install');
  const ios = isIOS();
  const show = !standalone() && !installDismissed && (S.installEvent || ios);
  bar.hidden = !show;
  if (!show) return;
  $('#install-go').hidden = !S.installEvent;
  $('#install-hint').textContent = S.installEvent ? 'Add it to your home screen to open it like an app.' : 'Tap Share, then “Add to Home Screen”.';
}
$('#install-go').addEventListener('click', async () => {
  if (!S.installEvent) return;
  S.installEvent.prompt();
  try { await S.installEvent.userChoice; } catch { /* the browser closed it */ }
  S.installEvent = null; renderInstall(); if (S.tab === 'control') renderControl();
});
$('#install-close').addEventListener('click', () => { installDismissed = true; renderInstall(); });
function installCard() {
  if (standalone()) return null;
  const card = h('div', { class: 'card' }, h('h2', null, 'Install the app'));
  if (S.installEvent) return (card.append(h('div', { class: 'note', text: 'Add Talaria to your home screen to open it like any app.' }), h('p', null, h('button', { class: 'btn primary big', type: 'button', text: 'Install', onclick: async () => { S.installEvent.prompt(); await S.installEvent.userChoice; S.installEvent = null; renderControl(); } }))), card);
  if (isIOS()) return (card.append(h('ol', { class: 'steps' }, h('li', { text: 'Tap the Share button in Safari.' }), h('li', { text: 'Choose “Add to Home Screen”.' }), h('li', { text: 'Open Talaria from your Home Screen.' }))), card);
  // Chrome on Android offers installing from its own menu even when it has not offered a button yet.
  if (/android/i.test(navigator.userAgent)) return (card.append(h('div', { class: 'note', text: 'Your browser has not offered an Install button yet. You can still do it by hand:' }),
    h('ol', { class: 'steps' }, h('li', { text: 'Tap the ⋮ menu at the top right of Chrome.' }), h('li', { text: 'Choose “Install app” (or “Add to Home screen”).' }), h('li', { text: 'Open Talaria from your home screen.' })),
    h('div', { class: 'note', text: 'If you opened this link inside another app (Telegram, Gmail, Facebook), open it in Chrome first: that is where installing works.' })), card);
  return null;
}

// ---------- start ----------
async function start() {
  try { await loadState(); } catch (e) { if (e instanceof AuthError) return; $('#status-text').textContent = e.message; return; }
  $('#login').hidden = true; $('#app').hidden = false;
  connect(); route(); renderChrome();
}
function presence() { if (!document.hidden && S.connected) api('/api/presence', { method: 'POST', body: { visible: true } }).catch(() => {}); }
setInterval(presence, 20000);
document.addEventListener('visibilitychange', () => {
  if (document.hidden) return;
  presence();
  if (!S.es) { loadState().then(connect).catch(() => {}); }
  if (S.tab === 'today') loadToday(); if (S.tab === 'control') loadControl();
});
window.addEventListener('online', () => { $('#offline').hidden = true; });
window.addEventListener('offline', () => { $('#offline').hidden = false; });
if (!navigator.onLine) $('#offline').hidden = false;
if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
navigator.serviceWorker?.addEventListener('message', (e) => { if (e.data && e.data.go) { location.hash = e.data.go; } });
start();
