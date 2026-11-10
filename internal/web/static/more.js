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

// The browser: a picture of the tab the agent has open, taken about once a second while this is on screen and
// never otherwise. Pointing at the picture clicks the page; what is typed here goes to the page and nowhere else.
async function showBrowser(body) {
  let d;
  try { d = await api('/api/browser'); } catch (e) { failed(e); body.replaceChildren(h('div', { class: 'card empty', text: e.message })); return; }
  if (COMPUTER.seg !== 'browser' || $('#panel').hidden) return;
  const tabs = d.tabs || [];
  if (!tabs.length) {
    body.replaceChildren(h('div', { class: 'card' }, h('h2', null, 'No page is open'),
      h('div', { class: 'field-note', text: 'When the agent opens a page, it shows here as it sees it, and you can take control: sign in, pass a check, or finish something by hand. Ask it to open a site to begin.' }),
      h('div', { class: 'btn-row' }, h('button', { class: 'btn', type: 'button', text: 'Look again', onclick: showComputer }))));
    return;
  }
  if (!tabs.some((t) => t.id === COMPUTER.tab)) COMPUTER.tab = tabs[0].id;
  const W = d.width || 1280, H = d.height || 800;
  const addr = h('input', { type: 'text', class: 'addr', 'aria-label': 'Address', autocomplete: 'off', autocapitalize: 'none', spellcheck: 'false', enterkeyhint: 'go' });
  addr.value = (tabs.find((t) => t.id === COMPUTER.tab) || {}).url || '';
  const img = h('img', { class: 'shot', alt: 'The page the agent has open', draggable: 'false' });
  const stage = h('div', { class: 'stage' }, img);
  let alive = true, busy = false, last = '';
  const shot = async () => {
    if (!alive || busy || document.hidden) return;
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
  const loop = () => { clearTimeout(PANEL.timer); if (!alive) return; PANEL.timer = setTimeout(async () => { await shot(); loop(); }, 1500); };
  PANEL.onClose = () => { alive = false; if (last) URL.revokeObjectURL(last); };
  const act = async (a) => {
    try {
      const r = await api('/api/browser/act', { method: 'POST', body: { id: COMPUTER.tab, ...a } });
      if (r && r.url && document.activeElement !== addr) addr.value = r.url;
    } catch (e) { failed(e); }
    await shot();
  };
  const point = (e) => { const b = img.getBoundingClientRect(); return { x: Math.round(((e.clientX - b.left) / b.width) * W), y: Math.round(((e.clientY - b.top) / b.height) * H) }; };
  img.addEventListener('click', (e) => act({ kind: 'click', ...point(e) }));
  let wheelAt = 0;
  img.addEventListener('wheel', (e) => {
    e.preventDefault();
    if (Date.now() - wheelAt < 250) return; // a wheel sends dozens of events: a few a second are enough
    wheelAt = Date.now();
    act({ kind: 'wheel', ...point(e), dy: Math.sign(e.deltaY) * 400 });
  }, { passive: false });
  const typed = h('input', { type: 'text', class: 'typed', 'aria-label': 'Type into the page', placeholder: 'Type into the page…', autocomplete: 'off', autocapitalize: 'none', spellcheck: 'false' });
  const send = async () => { const v = typed.value; if (!v) return; typed.value = ''; await act({ kind: 'type', text: v }); };
  typed.addEventListener('keydown', async (e) => { if (e.key === 'Enter') { e.preventDefault(); await send(); await act({ kind: 'key', key: 'Enter' }); } });
  const go = () => { if (addr.value.trim()) { addr.blur(); act({ kind: 'open', url: addr.value.trim() }); } };
  addr.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); go(); } });
  const key = (label, name) => h('button', { type: 'button', class: 'key', text: label, 'aria-label': name, onclick: () => act({ kind: 'key', key: name }) });
  const ikey = (name, label, fn) => h('button', { type: 'button', class: 'key', 'aria-label': label, title: label, onclick: fn }, icon(name));
  const picker = tabs.length > 1 ? h('div', { class: 'seg inner' }, tabs.map((t, i) => h('button', { type: 'button', 'aria-selected': String(t.id === COMPUTER.tab), text: 'Tab ' + (i + 1), onclick: () => { COMPUTER.tab = t.id; showComputer(); } }))) : null;
  body.className = 'panel-body browser';
  body.replaceChildren(picker,
    h('div', { class: 'toolbar' },
      ikey('back', 'Back a page', () => act({ kind: 'back' })),
      addr, h('button', { type: 'button', class: 'key go', text: 'Go', onclick: go })),
    stage,
    h('div', { class: 'toolbar' }, typed, h('button', { type: 'button', class: 'key go', text: 'Type', onclick: send })),
    h('div', { class: 'toolbar keys' }, key('Enter', 'Enter'), key('Tab', 'Tab'), key('Esc', 'Escape'), ikey('del', 'Backspace', () => act({ kind: 'key', key: 'Backspace' })),
      ikey('up', 'Up arrow', () => act({ kind: 'key', key: 'ArrowUp' })), ikey('down', 'Down arrow', () => act({ kind: 'key', key: 'ArrowDown' })),
      ikey('refresh', 'Refresh the picture', shot),
      ikey('zoom', 'Make the picture bigger or smaller', () => stage.classList.toggle('zoomed'))),
    h('div', { class: 'field-note', text: 'You are working the same tab the agent uses. It is told you took control, and reads the page again before it carries on. What you type here is not shown to it.' }));
  await shot(); loop();
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
  const rows = (items, tint) => {
    const box = h('div', { class: 'rich rows' });
    items.slice(0, 10).forEach((r, i) => {
      const from = r.from || '(unknown)', name = from.replace(/\s*<.*>$/, '') || from;
      const av = h('span', { class: 'avatar', text: (name.trim()[0] || '?').toUpperCase() });
      av.style.background = TINTS[(i + tint) % 4][0]; av.style.color = TINTS[(i + tint) % 4][1];
      box.append(h('button', { class: 'row', type: 'button', onclick: () => sendText('Read message ' + r.id + ' and tell me what it says.') }, av,
        h('span', { class: 'grow' }, h('span', { class: 't', text: name + ' · ' + (r.subject || '(no subject)') }), h('span', { class: 's', text: r.why || '' })),
        r.unread ? h('span', { class: 'pill info', text: 'NEW' }) : null));
    });
    if (items.length > 10) box.append(h('div', { class: 'field-note', text: '+' + (items.length - 10) + ' more.' }));
    return box;
  };
  const card = h('div', { class: 'rich triage' });
  card.append(h('div', { class: 'triage-h' }, h('b', { text: 'Needs you' }), h('span', { class: 'pill ' + (needs.length ? 'brass' : 'ok'), text: String(needs.length) })));
  card.append(needs.length ? rows(needs, 0) : h('div', { class: 'field-note', text: 'Nothing needs you.' }));
  if (wait.length) {
    const more = h('details', { class: 'triage-wait' }, h('summary', null, 'Can wait ', h('span', { class: 'pill', text: String(wait.length) })), rows(wait, 2));
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
      h('button', { class: 'btn', type: 'button', text: '+ Side chat', title: 'A blank conversation beside this one, for a tangent', onclick: sideConversation })));
  }
  const ordered = chats.slice().sort((a, b) => (b.main ? 1 : 0) - (a.main ? 1 : 0)); // the main one first; the rest stay newest first
  for (const c of ordered) {
    const sub = [c.main ? 'Main conversation' : c.side ? 'Side chat of ' + c.side : '', (c.messages || 0) + (c.messages === 1 ? ' message' : ' messages')].filter(Boolean).join(' · ');
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
        c.main ? h('span', { class: 'pill brass', text: 'MAIN' }) : c.side ? h('span', { class: 'pill info', text: 'SIDE' }) : null,
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
function tasksCard() {
  if (!doneTasks.length) return null;
  const rows = h('div', { class: 'rows' });
  for (const t of doneTasks.slice(0, 10)) {
    const again = h('button', { class: 'btn small', type: 'button', text: t.failed ? 'Retry' : 'Run again' });
    again.addEventListener('click', async () => {
      again.disabled = true;
      try { const d = await api('/api/tasks', { method: 'POST', body: { retry: t.id } }); doneTasks = d.tasks || []; toast('Started again: ' + t.name); renderAgentsPage(); }
      catch (e) { failed(e); again.disabled = false; }
    });
    rows.append(h('div', { class: 'row' }, h('span', { class: 'mark ' + (t.failed ? 'bad' : 'ok') }, icon(t.failed ? 'x' : 'check')),
      h('span', { class: 'grow' }, h('span', { class: 't', text: t.name }), h('span', { class: 's', text: t.failed ? t.status.replace(/^error: /, 'Failed: ') : 'Finished' })),
      h('span', { class: 'when', text: ago(t.at) }), again));
  }
  return h('div', { class: 'card' }, h('h2', null, 'Background tasks', h('small', { text: 'run one again as it was' })), rows);
}

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
  return [kinds, list];
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
  const own = (s.calendars || []).filter((x) => !x.builtin), cc = checks.calendar;
  add('calendar', 'Your calendar', cc ? (cc.ok ? 'ready' : 'todo') : own.length ? 'unchecked' : 'todo', cc ? cc.detail : own.length ? 'Set up. Press Check again to try it.' : 'Only its own calendar is there. Add yours to see your day in the brief.', 'connections');
  const brief = S.brief || {};
  if (brief.available) add('brief', 'Morning brief', brief.enabled ? 'ready' : 'todo', brief.enabled ? 'Arrives at ' + brief.at + ' every day.' : 'Off. Turn it on under You to get one message each morning.', 'you');
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
