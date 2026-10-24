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
// Rebuilding a page must not move the reader: run fn, then put the scroll back where it was.
function keepScroll(body, fn) {
  const sc = body.closest('.scroll');
  const top = sc ? sc.scrollTop : 0;
  fn();
  if (sc) sc.scrollTop = top;
}
const ICONS = {
  check: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>',
  x: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>',
  spin: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" aria-hidden="true"><path d="M12 3a9 9 0 1 0 9 9"/></svg>',
  shield: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z"/><path d="M9 12l2 2 4-4"/></svg>',
  info: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 8v5M12 16.5v.01"/></svg>',
  eye: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.8"/></svg>',
  cpu: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="3"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/><rect x="10" y="10" width="4" height="4" rx="1"/></svg>',
  lock: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="11" width="14" height="9" rx="3"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/></svg>',
  link: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3A4 4 0 0 0 11 18.7l1-1"/></svg>',
  bell: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 9a6 6 0 0 1 12 0c0 6 2 7 2 7H4s2-1 2-7z"/><path d="M10 20a2 2 0 0 0 4 0"/></svg>',
  user: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="8" r="3.5"/><path d="M5 20a7 7 0 0 1 14 0"/></svg>',
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
  model: '', paused: false, zone: undefined, version: '', tab: 'chat', sub: '', telegram: false, tgOn: false,
  today: null, blocks: new Map(), retry: 1000, installEvent: null,
  pinned: true, ownTop: 0, smoothUntil: 0, lastUserScroll: 0, dirty: false, flushTimer: 0,
  agents: [], agentsAt: 0,
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
  const before = fence ? text.slice(first.length, fence.index).trim() : '';
  const after = fence ? text.slice(fence.index + fence[0].length).trim() : '';
  const code = fence ? fence[1] : text.slice(first.length).trim();
  return { title: first, code, why: before, note: after };
}

function askCard(m, opts = {}) {
  const { title, code, why, note } = parseAsk(m.text);
  const pending = m.state === 'pending';
  const card = h('div', { class: 'ask' + (pending ? '' : ' done') });
  card.append(h('div', { class: 'ask-head' },
    h('span', { class: 'ask-icon' }, icon('shield')),
    h('span', { class: 'ask-title', text: title }),
    pending && h('span', { class: 'pill brass', text: 'WAITING' })));
  if (why) card.append(h('div', { class: 'ask-why', text: why }));
  if (code) card.append(h('div', { class: 'code', text: code }));
  if (pending) {
    if (note) card.append(h('div', { class: 'note', text: note }));
    const answer = async (choice, btns) => {
      btns.forEach((b) => (b.disabled = true));
      try { await api('/api/approve', { method: 'POST', body: { token: m.token, choice } }); }
      catch (e) { if (!(e instanceof AuthError)) toast(e.message); btns.forEach((b) => (b.disabled = false)); }
    };
    const btns = [];
    const mk = (label, cls, choice) => { const b = h('button', { class: 'btn ' + cls, type: 'button', text: label, onclick: () => answer(choice, btns) }); btns.push(b); return b; };
    card.append(h('div', { class: 'btn-row' }, mk('Deny', '', 'deny'), m.always && mk('Always', '', 'always'), mk('Allow', 'primary', 'allow')));
  } else {
    const v = { allowed: ['ok', 'Allowed'], always: ['ok', 'Allowed, and always allowed'], denied: ['bad', 'Denied'], expired: ['quiet', 'No longer waiting'] }[m.state] || ['quiet', m.state];
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

// ---------- keeping the reader's place ----------
// The thread follows the newest message only while the reader is "pinned" to the bottom. Pinned changes
// only when the reader scrolls, and never because something above or below changed size, so:
//  - an update can never take a reader who scrolled up back down, however small the scroll was;
//  - a reader at the bottom stays there while an answer streams in, a tool chip appears, or the keyboard opens;
//  - a reader higher up keeps looking at the same message while the thread changes around it;
//  - while a finger or wheel is moving the thread, updates wait until it stops, so they cannot interrupt it.
const threadEl = () => $('#thread');
const gapBelow = (t) => t.scrollHeight - t.scrollTop - t.clientHeight;
// Every scroll the page does itself goes through here, which records exactly where it left the thread. A scroll event
// that finds the thread anywhere else is the reader's; one that finds it where the page put it is not.
function setTop(v) {
  const t = threadEl();
  t.scrollTop = v;
  S.ownTop = t.scrollTop; // the position actually reached, after the browser has limited it
}
function toBottom() { setTop(threadEl().scrollHeight); }
// The reader may have scrolled in the same frame as an update, before the page has heard about it. Whenever the thread is
// not where the page last put it, that is the reader's doing, and it settles whether they are still at the bottom.
function noticeReader() {
  const t = threadEl();
  if (performance.now() >= S.smoothUntil && Math.abs(t.scrollTop - S.ownTop) >= 2) {
    S.lastUserScroll = performance.now();
    S.pinned = gapBelow(t) < 24;
  }
}
const interacting = () => performance.now() - S.lastUserScroll < 140;
function flushLater() {
  clearTimeout(S.flushTimer);
  S.flushTimer = setTimeout(() => { if (interacting()) flushLater(); else if (S.dirty) renderChat(); }, 160);
}
function updateJump() { const t = threadEl(); $('#jump').hidden = S.pinned || gapBelow(t) < 200; }

let renderQueued = false;
function renderChat() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => {
    renderQueued = false;
    if (interacting()) { S.dirty = true; flushLater(); return; } // the reader is mid-scroll: do it when they stop
    S.dirty = false;
    doRenderChat();
  });
}
// Stay at the bottom, but only if the reader is still there: used by everything that moves the thread by itself.
function followBottom() {
  noticeReader();
  if (S.pinned) toBottom();
}
function doRenderChat() {
  const thread = threadEl();
  noticeReader();

  // If the reader is not at the bottom, remember which message is at the top of their view, and where.
  let anchorKey = null, anchorGap = 0;
  if (!S.pinned) {
    for (const el of thread.children) {
      if (el.dataset.key && el.offsetTop + el.offsetHeight > thread.scrollTop + thread.offsetTop) {
        anchorKey = el.dataset.key; anchorGap = el.offsetTop - thread.scrollTop; break;
      }
    }
  }

  const blocks = groupBlocks();
  const keep = new Set();
  let prev = null;
  blocks.forEach((b, i) => {
    const last = i === blocks.length - 1;
    const sig = signature(b, last);
    let rec = S.blocks.get(b.key);
    if (!rec || rec.sig !== sig) {
      const el = buildBlock(b, last);
      el.dataset.key = b.key;
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

  if (S.pinned) toBottom();
  else if (anchorKey) {
    const el = S.blocks.get(anchorKey)?.el;
    if (el && el.isConnected) {
      const want = el.offsetTop - anchorGap;
      if (Math.abs(thread.scrollTop - want) > 1) setTop(want);
    }
  }
  updateJump();
  renderChrome();
}
// What the reader does with the thread, and nothing else, decides whether it follows the newest message.
$('#thread').addEventListener('scroll', () => {
  const t = threadEl();
  noticeReader();
  if (S.dirty) flushLater();
  updateJump();
}, { passive: true });
for (const type of ['touchmove', 'wheel']) {
  $('#thread').addEventListener(type, () => { S.lastUserScroll = performance.now(); }, { passive: true });
}
$('#jump').addEventListener('click', () => {
  const t = threadEl();
  S.pinned = true;
  S.smoothUntil = performance.now() + 900; // the smooth scroll passes through places that must not unpin
  t.scrollTo({ top: t.scrollHeight, behavior: 'smooth' });
  setTimeout(() => { if (S.pinned) toBottom(); updateJump(); }, 900);
  updateJump();
});
// A change in the thread's own size (the agents strip appearing, the keyboard opening, the window turning) must not
// move a reader who is at the bottom; and text that settles into its real font afterwards must not either.
if ('ResizeObserver' in window) new ResizeObserver(() => { if (!$('#screen-chat').hidden) followBottom(); }).observe($('#thread'));
document.fonts?.addEventListener?.('loadingdone', () => { if (!$('#screen-chat').hidden) followBottom(); });

// header, badge, composer buttons
function renderChrome() {
  const run = working();
  const busy = run || S.agents.length > 0; // the agent, or something it started that is still going
  $('#logo').classList.toggle('work', busy);
  const dot = $('#dot');
  dot.className = 'dot ' + (!S.connected ? 'off' : busy ? 'work' : 'on');
  const count = S.agents.length;
  $('#status-text').textContent = !S.connected ? 'Reconnecting…' : S.paused ? 'Paused · chat still works'
    : count > 1 ? `${count} agents working` : busy ? 'Working…' : 'Online' + (S.model ? ' · ' + S.model : '');
  const n = pendingAsks().length;
  const ab = $('#agent-badge');
  ab.hidden = n === 0 && S.agents.length === 0; ab.textContent = String(n || S.agents.length); ab.classList.toggle('work', n === 0);
  const hasText = $('#input').value.trim().length > 0;
  $('#send').hidden = run && !hasText;
  $('#stop').hidden = !(run && !hasText);
  $('#send').disabled = !hasText;
  $('#quick').hidden = run || hasText;
  document.title = n ? `(${n}) Talaria` : 'Talaria';
  if (S.tab === 'agents') { const sig = n + '|' + S.agents.length; if (sig !== S.agentsSig) { S.agentsSig = sig; renderAgentsPage(); } }
}

// ---------- who is working ----------
const KINDS = { main: 'MAIN', sub: 'HELPER', task: 'TASK', goal: 'GOAL', watch: 'WATCH', job: 'JOB' };
function setAgents(list) {
  const before = S.agents.length;
  S.agents = Array.isArray(list) ? list : [];
  if (S.agents.length < before && S.tab === 'agents') loadAgentsPage(); // something ended: it is now in the finished list
  S.agentsAt = Date.now();
  renderAgents(); renderChrome(); updateNow();
}
const elapsed = (w) => {
  const s = Math.max(0, Math.round(((w.ageMs || 0) + (Date.now() - S.agentsAt)) / 1000));
  return s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + String(s % 60).padStart(2, '0') + 's';
};
const doingText = (w) => !w.doing ? 'thinking…' : /^waiting/.test(w.doing) ? w.doing : 'using ' + w.doing;
function renderAgents() {
  const box = $('#agents');
  box.hidden = S.agents.length === 0;
  box.replaceChildren(...S.agents.map((w) => h('div', { class: 'agent', 'data-id': w.id },
    h('span', { class: 'pulse', 'aria-hidden': 'true' }),
    h('span', { class: 'k ' + w.kind, text: KINDS[w.kind] || w.kind.toUpperCase() }),
    h('span', { class: 'l', text: w.label }),
    w.kind !== 'main' && w.model ? h('span', { class: 'm', text: w.model }) : null,
    h('span', { class: 'd', text: doingText(w) }),
    h('span', { class: 'tick', text: elapsed(w) }))));
}
// Times tick on the page, without asking the server.
setInterval(() => {
  if (!S.agents.length || document.hidden) return;
  for (const w of S.agents) for (const t of $$(`[data-id="${w.id}"] .tick`)) t.textContent = elapsed(w);
}, 1000);

const who = (w) => (w.kind === 'main' ? 'Main agent' : 'Agent #' + w.id);
function nowCard() {
  const card = h('div', { class: 'card', id: 'now' }, h('h2', null, 'Working now', h('small', { text: S.agents.length ? S.agents.length + ' running' : '' })));
  if (!S.agents.length) { card.append(h('div', { class: 'empty', text: 'Nothing is working right now.' })); return card; }
  const rows = h('div', { class: 'rows' });
  for (const w of S.agents) {
    rows.append(h('div', { class: 'row', 'data-id': w.id }, h('span', { class: 'k ' + w.kind, text: KINDS[w.kind] || w.kind.toUpperCase() }),
      h('span', { class: 'grow' }, h('span', { class: 't', text: w.label }),
        h('span', { class: 's mono', text: [who(w), w.model].filter(Boolean).join(' · ') }),
        h('span', { class: 's mono', text: doingText(w) })),
      h('span', { class: 'when tick', text: elapsed(w) })));
  }
  card.append(rows);
  return card;
}
function updateNow() { const el = $('#now'); if (el && S.tab === 'agents') keepScroll(el, () => el.replaceWith(nowCard())); }

// The Agents screen: what waits for you, who is working, what is set up to run on its own, what has finished, and
// the switch that stops it all.
let recentAgents = [], standing = [], ctl = null;
async function loadAgentsPage() {
  const body = $('#agents-body');
  if (!body.children.length) body.replaceChildren(h('div', { class: 'skeleton' }));
  try {
    const [d, c] = await Promise.all([api('/api/agents'), api('/api/control')]);
    recentAgents = d.recent || []; standing = d.jobs || []; ctl = c; S.paused = c.paused;
  } catch (e) { if (e instanceof AuthError) return; toast(e.message); }
  renderAgentsPage();
}
function ago(iso) {
  const s = Math.max(0, Math.round((Date.now() - new Date(iso)) / 1000));
  return s < 60 ? 'just now' : s < 3600 ? Math.floor(s / 60) + ' min ago' : s < 86400 ? Math.floor(s / 3600) + ' h ago' : fmtDay(new Date(iso));
}
function duration(sec) { return sec < 60 ? sec + 's' : Math.floor(sec / 60) + 'm ' + (sec % 60) + 's'; }
const ASK_POLICY = {
  deletes_and_posts: 'Talaria asks before deleting anything or posting on social media. Everything else it just does.',
  strict: 'Talaria asks before running commands, changing files or sending anything out.',
};
function waitingSection() {
  const pend = pendingAsks();
  const card = h('div', { class: 'card wait' + (pend.length ? ' has' : '') }, h('h2', null, 'Waiting for you', h('small', { text: pend.length ? pend.length + ' to answer' : '' })));
  if (!pend.length) {
    card.append(h('div', { class: 'empty', text: 'Nothing is waiting for you.' }), h('div', { class: 'note center', text: ASK_POLICY[ctl && ctl.permissions] || ASK_POLICY.deletes_and_posts }));
    return card;
  }
  card.append(h('div', { class: 'note', text: 'These are saved. Answer whenever you like: the work carries on from where it stopped.' }));
  pend.forEach((m) => card.append(askCard(m)));
  return card;
}
function historyCard() {
  const done = S.order.map((id) => S.msgs.get(id)).filter((m) => m.role === 'approval' && m.state !== 'pending').reverse().slice(0, 40);
  if (!done.length) return null;
  const rows = h('div', { class: 'rows' });
  for (const m of done) {
    const bad = m.state === 'denied', expired = m.state === 'expired';
    const mark = h('span', { class: 'mark ' + (bad ? 'bad' : expired ? 'quiet' : 'ok') }, icon(bad ? 'x' : expired ? 'info' : 'check'));
    rows.append(h('div', { class: 'row' }, mark,
      h('span', { class: 'grow' }, h('span', { class: 't', text: parseAsk(m.text).title }), h('span', { class: 's', text: { allowed: 'You allowed it', always: 'You always allow it', denied: 'You denied it', expired: 'It ran out of time' }[m.state] })),
      h('span', { class: 'when', text: fmtWhen(new Date(m.at)) })));
  }
  const d = h('details', { class: 'card fold' }, h('summary', null, h('span', { class: 'display', text: 'Answered lately' }), h('small', { text: String(done.length) })), rows);
  if (S.historyOpen) d.setAttribute('open', '');
  d.addEventListener('toggle', () => { S.historyOpen = d.open; });
  return d;
}
function activityCard() {
  if (!ctl || !ctl.activity) return null;
  const max = Math.max(1, ...ctl.activity), total = ctl.activity.reduce((a, b) => a + b, 0);
  const bars = h('div', { class: 'bars', role: 'img', 'aria-label': `Activity over the last 24 hours: ${total} actions` });
  ctl.activity.forEach((v, i) => { const b = h('i', { class: i === ctl.activity.length - 1 ? 'now' : '' }); b.style.height = Math.max(5, (v / max) * 100) + '%'; bars.append(b); });
  return h('div', { class: 'card' }, h('h2', null, 'Activity', h('small', { text: total + ' actions in 24 h' })), bars, h('div', { class: 'chart-f' }, h('span', { text: '24 h ago' }), h('span', { text: 'now' })));
}
function renderAgentsPage() {
  if (S.tab !== 'agents') return;
  renderPause();
  const out = [waitingSection(), nowCard()];
  out.push(standing.length ? jobsCard(standing, 'Set up to run on their own')
    : h('div', { class: 'card' }, h('h2', null, 'Set up to run on their own'), h('div', { class: 'empty', text: 'Nothing yet. Ask for a reminder, a watch on your mail, or a goal to work on, and it will show here.' })));
  const fin = h('div', { class: 'card' }, h('h2', null, 'Finished lately'));
  if (!recentAgents.length) fin.append(h('div', { class: 'empty', text: 'Helpers and background work that finish will be listed here.' }));
  else {
    const rows = h('div', { class: 'rows' });
    for (const f of recentAgents.slice(0, 15)) {
      rows.append(h('div', { class: 'row' }, h('span', { class: 'k ' + f.kind, text: KINDS[f.kind] || f.kind.toUpperCase() }),
        h('span', { class: 'grow' }, h('span', { class: 't', text: f.label }), h('span', { class: 's', text: (f.stopped ? 'Stopped after ' : 'Took ') + duration(f.seconds) + (f.model ? ' · ' + f.model : '') })),
        h('span', { class: 'when', text: ago(f.at) })));
    }
    fin.append(rows);
  }
  out.push(fin);
  const hist = historyCard(); if (hist) out.push(hist);
  const act = activityCard(); if (act) out.push(act);
  const agentsBody = $('#agents-body');
  keepScroll(agentsBody, () => agentsBody.replaceChildren(...out));
}
let pauseArmed = false, pauseTimer;
function renderPause() {
  const b = $('#pause-btn');
  if (!ctl) { b.hidden = true; return; }
  b.hidden = false;
  b.className = 'pause' + (S.paused ? ' on' : pauseArmed ? ' armed' : '');
  b.textContent = S.paused ? 'Resume' : pauseArmed ? 'Tap again to pause' : 'Pause all';
  $('#agents-sub').textContent = S.paused ? 'Paused: goals, watches and reminders are held until you resume. Chat still works.' : 'Who is working for you, and what is waiting for your OK.';
}
$('#pause-btn').addEventListener('click', async () => {
  if (!S.paused && !pauseArmed) { pauseArmed = true; clearTimeout(pauseTimer); pauseTimer = setTimeout(() => { pauseArmed = false; renderPause(); }, 4000); renderPause(); return; }
  pauseArmed = false; $('#pause-btn').disabled = true;
  try { const r = await api('/api/pause', { method: 'POST', body: { paused: !S.paused } }); S.paused = r.paused; }
  catch (e) { if (!(e instanceof AuthError)) toast(e.message); }
  $('#pause-btn').disabled = false;
  loadAgentsPage(); renderChrome();
});

// ---------- composer ----------
const input = $('#input');
function autosize() { input.style.height = 'auto'; input.style.height = Math.min(input.scrollHeight, 140) + 'px'; }
input.addEventListener('input', () => { autosize(); renderChrome(); });
input.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing && !matchMedia('(pointer: coarse)').matches) { e.preventDefault(); $('#composer').requestSubmit(); }
});
// Messages written while unreachable wait here, in memory only — the page
// keeps nothing on the phone — and go out in order once the server answers.
let outbox = []; // {text, telegram}
let flushing = false, flushAgain = false;
async function flushOutbox() {
  if (flushing) { flushAgain = true; return; } // a trigger mid-flush buys one more pass, so nothing waits on a lost wakeup
  if (!outbox.length || !navigator.onLine) return;
  flushing = true;
  do {
    flushAgain = false;
    let sent = 0;
    while (outbox.length) {
      try { await api('/api/send', { method: 'POST', body: outbox[0] }); }
      catch { break; } // still unreachable (a 401 also shows the sign-in); the rest waits
      outbox.shift(); sent++;
    }
    if (sent) toast(sent === 1 ? 'Sent 1 queued message.' : `Sent ${sent} queued messages.`);
  } while (flushAgain && outbox.length && navigator.onLine);
  flushing = false;
}

async function sendText(text, telegram = false) {
  text = text.trim();
  if (!text) return;
  try { await api('/api/send', { method: 'POST', body: { text, telegram: telegram || undefined } }); }
  catch (e) {
    if (e instanceof AuthError) return false;
    if (!navigator.onLine || e.message === 'You appear to be offline.') {
      outbox.push({ text, telegram: telegram || undefined });
      toast(outbox.length === 1 ? 'It will send when you are back online.' : `${outbox.length} messages will send when you are back online.`);
      return true; // queued, so the composer does not put it back
    }
    toast(e.message); return false;
  }
  flushOutbox();
  return true;
}
$('#composer').addEventListener('submit', async (e) => {
  e.preventDefault();
  const text = input.value;
  if (!text.trim()) return;
  const viaTelegram = S.tgOn;
  input.value = ''; autosize(); renderChrome();
  if (await sendText(text, viaTelegram)) { S.tgOn = false; renderTg(); }
  else { input.value = text; autosize(); renderChrome(); }
});
// "Also send the result to Telegram": for the next message only, where the agent can reach Telegram.
const tg = $('#tg');
function renderTg() {
  tg.hidden = !S.telegram;
  tg.setAttribute('aria-pressed', String(S.tgOn)); tg.classList.toggle('on', S.tgOn);
  input.placeholder = S.tgOn ? 'Also to Telegram…' : 'Message Talaria';
}
tg.addEventListener('click', () => { S.tgOn = !S.tgOn; renderTg(); input.focus(); });
$('#stop').addEventListener('click', () => sendText('/stop'));
async function openSheet() {
  const body = $('#sheet-body');
  $('#sheet').hidden = false;
  body.replaceChildren(h('div', { class: 'card empty', text: 'Loading…' }));
  try {
    const d = await api('/api/chats');
    const rows = h('div', { class: 'rows' });
    rows.append(h('button', { class: 'row new', type: 'button', onclick: newConversation },
      h('span', { class: 'grow' }, h('span', { class: 't', text: '+ New chat' }), h('span', { class: 's', text: 'What is open is kept first' }))));
    for (const c of d.chats || []) {
      rows.append(h('button', { class: 'row', type: 'button', 'aria-current': c.active ? 'true' : null, onclick: () => openConversation(c.id) },
        h('span', { class: 'grow' }, h('span', { class: 't', text: c.title || 'New chat' }),
          h('span', { class: 's', text: (c.messages || 0) + (c.messages === 1 ? ' message' : ' messages') })),
        h('span', { class: 'when', text: c.updated ? fmtWhen(new Date(c.updated)) : '' })));
    }
    body.replaceChildren(h('div', { class: 'card' }, h('h2', null, 'Conversations'), rows));
  } catch (e) { if (!(e instanceof AuthError)) body.replaceChildren(h('div', { class: 'card empty', text: e.message })); }
}
function closeSheet() { $('#sheet').hidden = true; }
// A new chat never loses anything: what was open is kept first, and the new one starts blank.
async function newConversation() {
  closeSheet();
  try { await api('/api/chats', { method: 'POST', body: { new: true } }); }
  catch (e) { if (!(e instanceof AuthError)) toast(e.message); return; }
  await loadState();
  S.pinned = true; toBottom(); // the blank page starts at its bottom
}
async function openConversation(id) {
  closeSheet();
  try { await api('/api/chats', { method: 'POST', body: { open: id } }); }
  catch (e) { if (!(e instanceof AuthError)) toast(e.message); return; }
  await loadState();
  S.pinned = true; toBottom(); // an old conversation opens where it left off: with its latest
}
$('#chats').addEventListener('click', openSheet);
$('#new-chat').addEventListener('click', newConversation);
$('#sheet').addEventListener('click', (e) => { if (e.target.closest('.scrim')) closeSheet(); });
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
  setAgents(d.agents);
  S.epoch = d.epoch; S.seq = d.seq; S.running = d.running; S.model = d.model; S.paused = d.paused; S.version = d.version;
  S.telegram = !!d.telegram; renderTg();
  S.zone = d.zone || S.zone;
  if (!S.order.length) S.pinned = true; // a conversation that starts again starts at its bottom
  // Nothing is torn down: messages that are unchanged keep their elements, so the reader's place survives a reconnect.
  renderChat();
}
function connect() {
  if (S.es) S.es.close();
  const es = new EventSource(`/api/events?epoch=${S.epoch}&after=${S.seq}`);
  S.es = es;
  es.onopen = () => { S.connected = true; S.retry = 1000; renderChrome(); flushOutbox(); };
  es.onmessage = (e) => {
    const ev = JSON.parse(e.data);
    if (ev.seq > S.seq) S.seq = ev.seq;
    if (ev.type === 'msg') { upsert(ev.msg); renderChat(); }
    else if (ev.type === 'run') { S.running = !!ev.on; if (ev.model) S.model = ev.model; renderChat(); }
    else if (ev.type === 'agents') { setAgents(ev.agents); }
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
const TABS = ['chat', 'today', 'agents', 'settings'];
function route() {
  let [tab, sub = ''] = location.hash.slice(1).split('/');
  if (tab === 'needs' || tab === 'control') tab = 'agents'; // those screens live there now
  if (!TABS.includes(tab)) tab = 'chat';
  S.tab = tab;
  S.sub = tab === 'settings' && SETTINGS_PAGES.some((p) => p.id === sub) ? sub : '';
  for (const t of TABS) $('#screen-' + t).hidden = t !== tab;
  $$('#tabs a').forEach((a) => (a.getAttribute('data-tab') === tab ? a.setAttribute('aria-current', 'page') : a.removeAttribute('aria-current')));
  if (tab === 'today') loadToday();
  if (tab === 'agents') loadAgentsPage();
  if (tab === 'settings') { if (S.settings) renderSettings(); loadSettings(); }
  if (tab === 'chat') { S.pinned = true; toBottom(); }
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
  if (n) out.push(h('a', { class: 'alert-card', href: '#agents' }, h('span', { class: 'ask-icon' }, icon('shield')),
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

  const todayBody = $('#today-body');
  keepScroll(todayBody, () => todayBody.replaceChildren(...out));
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

function jobsCard(jobs, title = 'Working for You') {
  const card = h('div', { class: 'card' }, h('h2', null, title));
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

// ---------- settings ----------
let settingsEditing = null;

async function loadSettings() {
  try {
    const [st, c] = await Promise.all([api('/api/settings'), api('/api/control')]);
    S.settings = st; ctl = c; S.paused = c.paused;
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message); return; }
  renderSettings();
}

function profileNameCard(p) {
  const saved = p.has_key ? h('span', { class: 'pill info', text: 'key set via ' + (p.key_source || p.api_key_env || 'config') }) : h('span', { class: 'pill', text: 'no key' });
  const card = h('div', { class: 'card' });
  const head = h('div', { class: 'row' });
  head.append(h('span', { class: 'grow', text: p.name }), saved);
  if (p.active) head.append(h('span', { class: 'pill ok', text: 'IN USE' }));
  else if (p.default) head.append(h('span', { class: 'pill brass', text: 'DEFAULT' }));
  card.append(head);
  card.append(h('div', { class: 'field-note', text: [p.type, p.base_url, p.model && 'model ' + p.model].filter(Boolean).join(' · ') || 'No address or model set.' }));
  if (p.status) card.append(h('div', { class: 'field-note', text: p.available ? p.status : 'Unavailable: ' + p.status }));
  if (p.traits && p.traits.length) card.append(h('div', { class: 'field-note', text: 'Also: ' + p.traits.map((t) => t === 'vision' ? 'sees images' : 'reasons deeply').join(', ') }));
  const btns = h('div', { class: 'btn-row' });
  if (S.settings.active_profile && !p.active) {
    const use = h('button', { class: 'btn primary', type: 'button', text: 'Use', onclick: () => useProfile(p.name) });
    if (!p.available) use.disabled = true;
    btns.append(use);
  }
  if (S.settings.active_profile) {
    const test = h('button', { class: 'btn', type: 'button', text: 'Test', onclick: () => testProfile(p.name) });
    if (!p.available) test.disabled = true;
    btns.append(test);
  }
  btns.append(h('button', { class: 'btn', type: 'button', text: 'Edit', onclick: () => editProfile(p.name) }));
  btns.append(h('button', { class: 'btn danger', type: 'button', text: 'Delete', onclick: () => deleteProfile(p.name) }));
  card.append(btns);
  return card;
}

async function testProfile(name) {
  toast('Asking ' + name + ' to say ok…');
  const t0 = Date.now();
  try {
    const d = await api('/api/settings', { method: 'POST', body: { test_profile: name } });
    toast(name + ' answers (' + d.model + ', ' + ((Date.now() - t0) / 1000).toFixed(1) + 's): ' + d.reply);
  } catch (e) { if (!(e instanceof AuthError)) toast('Test failed: ' + e.message); return; }
  loadSettings();
}

async function useProfile(name) {
  try {
    await api('/api/settings', { method: 'POST', body: { use_profile: name } });
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message); return; }
  toast('Now using ' + name + '. The conversation continues with the new model.');
  loadSettings();
  api('/api/state').then((d) => { S.model = d.model; renderChrome(); }).catch(() => {});
}

function strengthsRow(best, strengths) {
  const names = { code: 'Code', cheap: 'Cheap & fast' };
  const row = h('div', { class: 'checks' });
  for (const s of strengths || []) {
    const props = { type: 'checkbox', name: 'best-for', value: s };
    if ((best || []).includes(s)) props.checked = true;
    row.append(h('label', { class: 'check' }, h('input', props), ' ' + (names[s] || s)));
  }
  return h('div', { class: 'field' }, h('span', { class: 'field-label', text: 'Good at (for auto-routing)' }), row);
}

function editProfile(name) {
  settingsEditing = name;
  const p = S.settings.profiles.find(x => x.name === name) || { name };
  const form = h('div', { class: 'card' },
    h('h2', null, 'Edit Profile'),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Profile name', for: 'prof-name' }), h('input', { id: 'prof-name', name: 'prof-name', autocomplete: 'off', type: 'text', value: p.name })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Type', for: 'prof-type' }), h('input', { id: 'prof-type', name: 'prof-type', autocomplete: 'off', type: 'text', value: p.type || 'openai' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Base URL', for: 'prof-baseurl' }), h('input', { id: 'prof-baseurl', name: 'prof-baseurl', autocomplete: 'off', type: 'text', value: p.base_url || '' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Model', for: 'prof-model' }), h('input', { id: 'prof-model', name: 'prof-model', autocomplete: 'off', type: 'text', value: p.model || '' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'API key env var', for: 'prof-apikeyenv' }), h('input', { id: 'prof-apikeyenv', name: 'prof-apikeyenv', autocomplete: 'off', type: 'text', value: p.api_key_env || '' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'API key', for: 'prof-apikey' }), h('input', { id: 'prof-apikey', name: 'prof-apikey', autocomplete: 'off', type: 'password', placeholder: 'leave blank to keep current' })),
    strengthsRow(p.best, S.settings.strengths),
    h('div', { class: 'field-note', text: 'The key is saved to config.json (mode 0600). Prefer api_key_env over pasting the key directly.' }),
    h('div', { class: 'btn-row' },
      h('button', { class: 'btn primary big', type: 'button', text: 'Save', onclick: saveProfile }),
      h('button', { class: 'btn big', type: 'button', text: 'Cancel', onclick: () => { settingsEditing = null; renderSettings(); } })));
  const editBody = $('#settings-pane');
  keepScroll(editBody, () => editBody.replaceChildren(form));
}

function presetBar() {
  const hint = h('div', { class: 'field-note', text: 'Pick a provider to fill in the address and model, or fill it by hand.' });
  const bar = h('div', { class: 'presets' });
  for (const pr of S.settings.presets || []) {
    bar.append(h('button', {
      type: 'button', text: pr.label, onclick: (e) => {
        $$('#settings-pane .presets button').forEach((b) => b.classList.toggle('on', b === e.currentTarget));
        const fill = (id, v) => { if (v) $('#' + id).value = v; };
        fill('prof-type', pr.type); fill('prof-baseurl', pr.base_url); fill('prof-model', pr.model); fill('prof-apikeyenv', pr.key_env);
        const name = $('#prof-name');
        if (!name.value.trim()) name.value = pr.label.toLowerCase().replace(/[^a-z0-9]+/g, '-');
        hint.textContent = pr.hint;
      }
    }));
  }
  return h('div', null, bar, hint);
}

function newProfileForm() {
  settingsEditing = 'new';
  const form = h('div', { class: 'card' },
    h('h2', null, 'Add AI Profile'),
    presetBar(),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Profile name', for: 'prof-name' }), h('input', { id: 'prof-name', name: 'prof-name', autocomplete: 'off', type: 'text', placeholder: 'e.g. openrouter' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Type', for: 'prof-type' }), h('input', { id: 'prof-type', name: 'prof-type', autocomplete: 'off', type: 'text', value: 'openai' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Base URL', for: 'prof-baseurl' }), h('input', { id: 'prof-baseurl', name: 'prof-baseurl', autocomplete: 'off', type: 'text', placeholder: 'https://api.openai.com/v1' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Model', for: 'prof-model' }), h('input', { id: 'prof-model', name: 'prof-model', autocomplete: 'off', type: 'text', placeholder: 'gpt-4o-mini' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'API key env var', for: 'prof-apikeyenv' }), h('input', { id: 'prof-apikeyenv', name: 'prof-apikeyenv', autocomplete: 'off', type: 'text', placeholder: 'OPENROUTER_API_KEY' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'API key', for: 'prof-apikey' }), h('input', { id: 'prof-apikey', name: 'prof-apikey', autocomplete: 'off', type: 'password', placeholder: 'sk-…' })),
    strengthsRow([], S.settings.strengths),
    h('div', { class: 'field-note', text: 'Pick provider type openai, anthropic or deepseek. Keys are stored in config.json.' }),
    h('div', { class: 'btn-row' },
      h('button', { class: 'btn primary big', type: 'button', text: 'Add', onclick: saveProfile }),
      h('button', { class: 'btn big', type: 'button', text: 'Cancel', onclick: () => { settingsEditing = null; renderSettings(); } })));
  const newBody = $('#settings-pane');
  keepScroll(newBody, () => newBody.replaceChildren(form));
}

async function saveProfile() {
  const name = $('#prof-name').value.trim();
  const body = { // keys must match the Go struct tags: snake_case, not camelCase
    name,
    type: $('#prof-type').value.trim() || undefined,
    base_url: $('#prof-baseurl').value.trim() || undefined,
    model: $('#prof-model').value.trim() || undefined,
    api_key_env: $('#prof-apikeyenv').value.trim() || undefined,
    api_key: $('#prof-apikey').value.trim() || undefined,
    best_for: $$('#settings-pane input[name="best-for"]:checked').map((el) => el.value),
  };
  try {
    await api('/api/settings', { method: 'POST', body });
    settingsEditing = null;
    loadSettings();
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message); }
}

async function deleteProfile(name) {
  if (!confirm('Delete profile "' + name + '"? This cannot be undone.')) return;
  try {
    await api('/api/settings', { method: 'POST', body: { Name: name, _delete: true } });
    loadSettings();
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message); }
}

function emailCard() {
  const e = S.settings.email || {};
  const head = h('div', { class: 'row' });
  head.append(h('span', { class: 'grow', text: 'Email' }));
  head.append(e.available ? h('span', { class: 'pill ok', text: 'connected' }) : h('span', { class: 'pill', text: e.configured ? 'needs attention' : 'not connected' }));
  const card = h('div', { class: 'card' }, h('h2', null, 'Email'), head);
  if (e.status) card.append(h('div', { class: 'field-note', text: e.status + (e.password_source ? ' (key from ' + e.password_source + ')' : '') }));
  card.append(
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Address', for: 'mail-address' }), h('input', { id: 'mail-address', autocomplete: 'off', type: 'text', value: e.address || '' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Sign-in name, if not the address', for: 'mail-user' }), h('input', { id: 'mail-user', autocomplete: 'off', type: 'text', value: e.user || '' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Server (found automatically for common providers)', for: 'mail-server' }), h('input', { id: 'mail-server', autocomplete: 'off', type: 'text', value: e.server || '' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'App password', for: 'mail-password' }), h('input', { id: 'mail-password', autocomplete: 'off', type: 'password', placeholder: e.has_password ? 'leave blank to keep' : 'an app password, not your normal password' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Or a variable holding it', for: 'mail-passwordenv' }), h('input', { id: 'mail-passwordenv', autocomplete: 'off', type: 'text', placeholder: 'leave blank' })),
    h('div', { class: 'field-note', text: 'Clear everything to disconnect. Read-only: mail is never sent, deleted or marked.' }));
  const btns = h('div', { class: 'btn-row' });
  btns.append(h('button', { class: 'btn primary', type: 'button', text: 'Save', onclick: saveEmail }));
  if (e.configured) btns.append(h('button', { class: 'btn', type: 'button', text: 'Test', onclick: testEmail }));
  card.append(btns);
  return card;
}

async function saveEmail() {
  const val = (id) => $('#' + id).value.trim() || undefined;
  const body = { address: val('mail-address'), user: val('mail-user'), server: val('mail-server'), password: $('#mail-password').value || undefined, password_env: val('mail-passwordenv') };
  try {
    const d = await api('/api/settings', { method: 'POST', body: { email: body } });
    toast(d.restart_needed ? 'Saved. Restart the server to use the new mailbox.' : 'Mailbox saved.');
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message); return; }
  loadSettings();
}

async function testEmail() {
  toast('Checking the mailbox…');
  try {
    const d = await api('/api/settings', { method: 'POST', body: { test_email: true } });
    toast('The mailbox answers (' + d.folders + ' folders).');
  } catch (e) { if (!(e instanceof AuthError)) toast('Test failed: ' + e.message); }
}

function calendarsCard() {
  const list = S.settings.calendars || [];
  const card = h('div', { class: 'card' }, h('h2', null, 'Calendars'));
  if (!list.length) card.append(h('div', { class: 'empty', text: 'No calendars connected yet.' }));
  for (const c of list) {
    const head = h('div', { class: 'row' });
    head.append(h('span', { class: 'grow', text: c.name }));
    if (c.builtin) head.append(h('span', { class: 'pill info', text: 'built in' }));
    else if (c.source) head.append(h('span', { class: 'pill info', text: 'from ' + c.source }));
    else head.append(h('span', { class: 'pill', text: 'not set up' }));
    card.append(head);
    if (c.status) card.append(h('div', { class: 'field-note', text: c.status }));
    if (!c.builtin && c.source === 'settings') {
      const btns = h('div', { class: 'btn-row' });
      btns.append(h('button', { class: 'btn', type: 'button', text: 'Test', onclick: () => testCalendar(c.name) }));
      btns.append(h('button', { class: 'btn', type: 'button', text: 'Change address', onclick: () => editCalendar(c.name) }));
      btns.append(h('button', { class: 'btn danger', type: 'button', text: 'Remove', onclick: () => deleteCalendar(c.name) }));
      card.append(btns);
    } else if (!c.builtin && c.source === 'environment') {
      card.append(h('div', { class: 'field-note', text: 'Managed in the environment.' }));
    }
  }
  card.append(calendarForm());
  return card;
}

function calendarForm() {
  return h('div', null,
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Name', for: 'cal-name' }), h('input', { id: 'cal-name', autocomplete: 'off', type: 'text', placeholder: 'family' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Secret iCal address', for: 'cal-url' }), h('input', { id: 'cal-url', autocomplete: 'off', type: 'text', placeholder: 'https://…' })),
    h('div', { class: 'field' }, h('label', { class: 'check' }, h('input', { type: 'checkbox', id: 'cal-private' }), ' On this machine or a private network')),
    h('div', { class: 'btn-row' }, h('button', { class: 'btn primary', type: 'button', text: 'Add calendar', onclick: () => saveCalendar('add') })));
}

function editCalendar(name) {
  const form = h('div', { class: 'card' },
    h('h2', null, 'Change address'),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Name' }), h('input', { type: 'text', value: name, disabled: true })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Secret iCal address', for: 'cal-url' }), h('input', { id: 'cal-url', autocomplete: 'off', type: 'text', placeholder: 'https://…' })),
    h('div', { class: 'field' }, h('label', { class: 'check' }, h('input', { type: 'checkbox', id: 'cal-private' }), ' On this machine or a private network')),
    h('div', { class: 'btn-row' },
      h('button', { class: 'btn primary', type: 'button', text: 'Save', onclick: () => saveCalendar('edit', name) }),
      h('button', { class: 'btn', type: 'button', text: 'Cancel', onclick: renderSettings })));
  const body = $('#settings-pane');
  keepScroll(body, () => { body.replaceChildren(form); });
}

async function saveCalendar(op, name) {
  name = name || $('#cal-name').value.trim();
  const body = { op, name };
  if (op !== 'delete') {
    body.url = $('#cal-url').value.trim();
    body.allow_private = $('#cal-private').checked;
  }
  try {
    const d = await api('/api/settings', { method: 'POST', body: { calendar: body } });
    toast(d.restart_needed ? 'Saved. Restart the server to apply it.' : 'Calendar saved.');
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message); return; }
  loadSettings();
}

async function deleteCalendar(name) {
  if (!confirm('Remove the calendar ' + name + '?')) return;
  saveCalendar('delete', name);
}

async function testCalendar(name) {
  toast('Reading ' + name + '…');
  try {
    const d = await api('/api/settings', { method: 'POST', body: { test_calendar: name } });
    toast(name + ' answers (' + d.events + ' events today).');
  } catch (e) { if (!(e instanceof AuthError)) toast('Test failed: ' + e.message); }
}

function chatName(service) {
  return service === 'telegram' ? 'Telegram' : service === 'discord' ? 'Discord' : 'X';
}

async function testX() {
  toast('Checking the X sign-in…');
  try {
    const d = await api('/api/settings', { method: 'POST', body: { test_x: true } });
    toast(d.account);
  } catch (e) { if (!(e instanceof AuthError)) toast('Test failed: ' + e.message); }
}

function chatsCard() {
  const list = S.settings.chats || [];
  const card = h('div', { class: 'card' }, h('h2', null, 'Chat services'));
  for (const c of list) {
    const head = h('div', { class: 'row' });
    head.append(h('span', { class: 'grow', text: chatName(c.service) }));
    head.append(c.connected ? h('span', { class: 'pill ok', text: 'connected' + (c.source ? ' via ' + c.source : '') }) : h('span', { class: 'pill', text: 'not connected' }));
    card.append(head);
    if (c.service === 'x' && c.connected) {
      const btns = h('div', { class: 'btn-row' });
      btns.append(h('button', { class: 'btn', type: 'button', text: 'Test', onclick: testX }));
      card.append(btns);
    }
  }
  card.append(
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Service', for: 'chat-service' }), h('input', { id: 'chat-service', autocomplete: 'off', type: 'text', placeholder: 'telegram, discord or x' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Bot token', for: 'chat-token' }), h('input', { id: 'chat-token', autocomplete: 'off', type: 'password', placeholder: 'leave blank to disconnect' })),
    h('div', { class: 'field' }, h('label', { class: 'field-label', text: 'Or a variable holding it', for: 'chat-tokenenv' }), h('input', { id: 'chat-tokenenv', autocomplete: 'off', type: 'text', placeholder: 'leave blank' })),
    h('div', { class: 'field-note', text: 'Takes effect after a restart.' }),
    h('div', { class: 'btn-row' }, h('button', { class: 'btn primary', type: 'button', text: 'Save', onclick: saveChatToken })));
  return card;
}

async function saveChatToken() {
  const val = (id) => $('#' + id).value.trim() || undefined;
  try {
    await api('/api/settings', { method: 'POST', body: { chat_token: { service: (val('chat-service') || '').toLowerCase(), token: $('#chat-token').value || undefined, token_env: val('chat-tokenenv') } } });
    toast('Saved. Restart the server to apply it.');
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message); return; }
  loadSettings();
}

function uptime(sec) {
  const d = Math.floor(sec / 86400), hh = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  return d ? `${d} d ${hh} h` : hh ? `${hh} h ${m} min` : `${m} min`;
}

const SETTINGS_PAGES = [
  { id: 'models', title: 'AI models', icon: 'cpu', sub: 'Which models work for you, and when each is used.' },
  { id: 'permissions', title: 'Permissions', icon: 'lock', sub: 'When Talaria has to ask you first.' },
  { id: 'connections', title: 'Connections', icon: 'link', sub: 'Email, calendars and chat apps.' },
  { id: 'notifications', title: 'Notifications', icon: 'bell', sub: 'Hear from it when the app is closed.' },
  { id: 'about', title: 'About & account', icon: 'user', sub: 'Status, installing, and signing out.' },
];
function settingsSummary(id) {
  const s = S.settings || {}, c = ctl || {};
  switch (id) {
    case 'models': {
      const ps = s.profiles || [], cur = ps.find((p) => p.active) || ps.find((p) => p.default);
      return (cur && cur.model ? cur.model : S.model || 'Not set') + (ps.length > 1 ? ' · ' + ps.length + ' profiles' : '');
    }
    case 'permissions': return s.permissions === 'strict' ? 'Asks before anything risky' : 'Asks only to delete or post';
    case 'connections': {
      const on = [];
      if (s.email && s.email.available) on.push('Email');
      if ((s.calendars || []).some((x) => !x.builtin)) on.push('Calendars');
      for (const ch of s.chats || []) if (ch.connected) on.push(chatName(ch.service));
      return on.length ? on.join(', ') : 'Nothing connected yet';
    }
    case 'notifications': return { on: 'On for this device', off: 'Off', denied: 'Blocked in the browser', unsupported: 'Not supported here', 'needs-install': 'Add the app to your Home Screen first' }[S.push] || '';
    case 'about': return 'Talaria ' + (c.version || S.version || '');
  }
  return '';
}

function paneModels(s) {
  const out = [], ps = s.profiles || [];
  const cur = ps.find((p) => p.active) || ps.find((p) => p.default);
  const usable = ps.filter((p) => p.available);
  out.push(h('div', { class: 'card' }, h('h2', null, 'How models are used'),
    h('div', { class: 'field-note', text: 'Main agent: ' + (cur ? cur.name + (cur.model ? ' (' + cur.model + ')' : '') : S.model || 'the default model') + '.' }),
    h('div', { class: 'field-note', text: usable.length > 1
      ? 'Each message can go to the profile that suits it: one marked Code for programming, one marked Cheap & fast for quick questions. Helpers can be given any profile too, so a cheap model does the lookups while a strong one does the thinking. The Agents screen shows which model each is using.'
      : 'Add a second profile and Talaria can use a cheap model for quick jobs and a strong one for hard ones, and hand lookups to helpers on a cheaper model.' })));
  for (const p of ps) out.push(profileNameCard(p));
  if (!ps.length) out.push(h('div', { class: 'card' }, h('h2', null, 'Profiles'), h('div', { class: 'empty', text: (S.model ? S.model + ' comes from the server settings. ' : '') + 'Add a profile to keep more than one model and switch between them.' })));
  out.push(h('button', { class: 'btn big', type: 'button', text: 'Add an AI profile', onclick: newProfileForm }));
  out.push(h('div', { class: 'field-note', text: s.has_default_key ? s.default_key_env + ' is set, so the default profile works without a config file.' : 'Set TALARIA_API_KEY in your server environment to use the default profile without a config file.' }));
  return out;
}

async function savePermissions(mode) {
  try {
    const d = await api('/api/settings', { method: 'POST', body: { permissions: mode } });
    toast(d.restart_needed ? 'Saved. It applies after Talaria restarts.' : 'Saved.');
  } catch (e) { if (!(e instanceof AuthError)) toast(e.message); }
  loadSettings();
}
function panePermissions(s) {
  const opt = (id, title, text) => h('label', { class: 'opt' + (s.permissions === id ? ' on' : '') },
    h('input', { type: 'radio', name: 'perm', value: id, checked: s.permissions === id, onchange: () => savePermissions(id) }),
    h('span', { class: 'grow' }, h('b', { text: title }), h('span', { class: 's', text })));
  const out = [h('div', { class: 'card' }, h('h2', null, 'When Talaria asks you first'),
    opt('deletes_and_posts', 'Only to delete or to post', 'It runs commands, edits files, browses and sends you messages without asking. It asks before it deletes anything, and before it posts on social media.'),
    opt('strict', 'Before anything risky', 'It also asks before running commands, writing files, changing schedules, and sending anything out after it has read mail or the web.'))];
  if (s.permissions_env) out[0].append(h('div', { class: 'field-note', text: 'TALARIA_PERMISSIONS is set on the server, and it wins over this choice.' }));
  else if (s.permissions !== s.permissions_running) out[0].append(h('div', { class: 'field-note', text: 'Saved. It applies after Talaria restarts: it is still working the old way for now.' }));
  out.push(h('div', { class: 'card' }, h('h2', null, 'What counts'),
    h('ul', { class: 'steps' },
      h('li', { text: 'Deleting: removing files or folders, git clean and force-pushes, dropping databases, deleting skills, schedules or goals.' }),
      h('li', { text: 'Posting: anything sent in public on X, Facebook, Instagram, LinkedIn, Reddit, TikTok, Bluesky and similar sites, by the X tool or by clicking and typing in the browser there.' })),
    h('div', { class: 'field-note', text: 'A command that deletes is recognised by what it says, so an unusual one can slip past. Turn on the stricter choice if you want a second pair of eyes on every command.' })));
  out.push(h('div', { class: 'card' }, h('h2', null, 'While it waits'),
    h('div', { class: 'field-note', text: 'The question is saved, the agent carries on with anything else, and you will find it under Agents and in your notifications. Answer whenever you like, even after a restart: the saved step is then done and the work picks up where it stopped. Unanswered questions lapse after a week.' })));
  return out;
}

function paneConnections(s) {
  const out = [];
  if ((s.chats || []).some((c) => c.service === 'telegram' && c.connected)) {
    out.push(h('div', { class: 'banner' }, icon('info'), h('span', { text: 'Telegram is connected. Ask for the result to be sent there ("research this and send me the summary on Telegram"), or use the Telegram button next to the message box.' })));
  }
  out.push(emailCard(), calendarsCard(), chatsCard());
  return out;
}

function paneNotifications() { return [notifyCard(ctl || { push: {} })]; }

function paneAbout() {
  const c = ctl || {};
  const rows = h('div', { class: 'rows' });
  for (const [k, v] of [['Version', c.version || S.version], ['Running for', c.uptimeSec != null ? uptime(c.uptimeSec) : ''], ['Model', c.model || S.model]]) {
    if (v) rows.append(h('div', { class: 'row' }, h('span', { class: 'grow', text: k }), h('span', { class: 'mono s', text: v })));
  }
  const out = [h('div', { class: 'card' }, h('h2', null, 'Status'), rows)];
  if ((c.links || []).length) {
    const lr = h('div', { class: 'rows' });
    for (const l of c.links) lr.append(h('div', { class: 'row' }, h('span', { class: 'link-dot' }), h('span', { class: 'grow' }, h('span', { class: 't', text: l.name }), h('span', { class: 's mono', text: l.detail })), h('span', { class: 'pill info', text: l.tag })));
    out.push(h('div', { class: 'card' }, h('h2', null, 'Connected'), lr));
  }
  const inst = installCard(); if (inst) out.push(inst);
  out.push(h('button', { class: 'btn big', type: 'button', text: 'Sign out', onclick: async () => { try { await api('/api/logout', { method: 'POST' }); } catch { /* signed out anyway */ } showLogin(); } }));
  return out;
}

const PANES = { models: paneModels, permissions: panePermissions, connections: paneConnections, notifications: paneNotifications, about: paneAbout };
const wideScreen = () => window.matchMedia('(min-width: 900px)').matches;
window.matchMedia('(min-width: 900px)').addEventListener?.('change', () => { if (S.tab === 'settings') renderSettings(); });
$('#settings-back').addEventListener('click', () => { location.hash = '#settings'; });

async function renderSettings() {
  const s = S.settings; if (!s || S.tab !== 'settings') return;
  S.push = await pushState();
  if (S.tab !== 'settings') return;
  const wide = wideScreen();
  const sub = S.sub || (wide ? 'models' : '');
  const page = SETTINGS_PAGES.find((p) => p.id === sub);
  $('#settings-title').textContent = page && !wide ? page.title : 'Settings';
  $('#settings-sub').textContent = page && !wide ? page.sub : 'Models, permissions and connections.';
  $('#settings-back').hidden = !page || wide;
  const nav = h('nav', { class: 'set-nav', 'aria-label': 'Settings sections' }, SETTINGS_PAGES.map((p) => h('a', { class: 'set-item', href: '#settings/' + p.id, 'aria-current': p.id === sub ? 'page' : null },
    h('span', { class: 'set-icon' }, icon(p.icon)),
    h('span', { class: 'grow' }, h('span', { class: 't', text: p.title }), h('span', { class: 's', text: settingsSummary(p.id) })),
    icon('chev'))));
  const pane = h('div', { id: 'settings-pane', class: 'set-pane stack' }, page && wide ? h('h2', { class: 'pane-title', text: page.title }) : null, ...(page ? PANES[page.id](s) : []));
  const body = $('#settings-body');
  keepScroll(body, () => body.replaceChildren(h('div', { class: 'set-grid' + (page ? ' has-sub' : '') }, nav, pane)));
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
function notifyCard(c) {
  const st = S.push;
  const card = h('div', { class: 'card' }, h('h2', null, 'Notifications'));
  const done = () => renderSettings();
  if (!(c.push && c.push.available)) card.append(h('div', { class: 'note', text: 'Notifications are not set up on the server.' }));
  else if (st === 'on') card.append(h('div', { class: 'verdict ok' }, icon('check'), 'On for this device'), h('p', { class: 'field-note', text: 'I will notify you when something needs your OK, and when I have something to tell you while the app is closed.' }), h('p', null, h('button', { class: 'btn', type: 'button', text: 'Turn off', onclick: async () => { await disablePush(); done(); } })));
  else if (st === 'needs-install') card.append(h('div', { class: 'note', text: 'On iPhone, notifications work once Talaria is on your Home Screen:' }), h('ol', { class: 'steps' }, h('li', { text: 'Tap the Share button in Safari.' }), h('li', { text: 'Choose “Add to Home Screen”.' }), h('li', { text: 'Open Talaria from there, and come back to this screen.' })));
  else if (st === 'denied') card.append(h('div', { class: 'note', text: 'Notifications are blocked for this site. Allow them in your browser or phone settings, then come back.' }));
  else if (st === 'unsupported') card.append(h('div', { class: 'note', text: 'This browser cannot show notifications.' }));
  else card.append(h('div', { class: 'note', text: 'Get a notification when something needs your OK, even when the app is closed.' }), h('p', null, h('button', { class: 'btn primary big', type: 'button', text: 'Turn on notifications', onclick: async (e) => { e.target.disabled = true; await enablePush(); done(); } })));
  return card;
}
window.addEventListener('beforeinstallprompt', (e) => { e.preventDefault(); S.installEvent = e; renderInstall(); if (S.tab === 'settings') renderSettings(); });
window.addEventListener('appinstalled', () => { S.installEvent = null; renderInstall(); if (S.tab === 'settings') renderSettings(); });

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
  S.installEvent = null; renderInstall(); if (S.tab === 'settings') renderSettings();
});
$('#install-close').addEventListener('click', () => { installDismissed = true; renderInstall(); });
function installCard() {
  if (standalone()) return null;
  const card = h('div', { class: 'card' }, h('h2', null, 'Install the App'));
  if (S.installEvent) return (card.append(h('div', { class: 'note', text: 'Add Talaria to your home screen to open it like any app.' }), h('p', null, h('button', { class: 'btn primary big', type: 'button', text: 'Install', onclick: async () => { S.installEvent.prompt(); await S.installEvent.userChoice; S.installEvent = null; renderSettings(); } }))), card);
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
  connect(); route(); renderChrome(); flushOutbox();
}
function presence() { if (!document.hidden && S.connected) api('/api/presence', { method: 'POST', body: { visible: true } }).catch(() => {}); }
setInterval(presence, 20000);
document.addEventListener('visibilitychange', () => {
  if (document.hidden) return;
  presence(); flushOutbox();
  if (!S.es) { loadState().then(connect).catch(() => {}); }
  if (S.tab === 'today') loadToday(); if (S.tab === 'agents') loadAgentsPage();
});
window.addEventListener('online', () => { $('#offline').hidden = true; flushOutbox(); });
window.addEventListener('offline', () => { $('#offline').hidden = false; });
if (!navigator.onLine) $('#offline').hidden = false;
if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
navigator.serviceWorker?.addEventListener('message', (e) => { if (e.data && e.data.go) { location.hash = e.data.go; } });
start();
