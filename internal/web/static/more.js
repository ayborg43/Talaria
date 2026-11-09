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
  const picker = tabs.length > 1 ? h('div', { class: 'seg inner' }, tabs.map((t, i) => h('button', { type: 'button', 'aria-selected': String(t.id === COMPUTER.tab), text: 'Tab ' + (i + 1), onclick: () => { COMPUTER.tab = t.id; showComputer(); } }))) : null;
  body.className = 'panel-body browser';
  body.replaceChildren(picker,
    h('div', { class: 'toolbar' },
      h('button', { type: 'button', class: 'key', text: '←', 'aria-label': 'Back', onclick: () => act({ kind: 'back' }) }),
      h('button', { type: 'button', class: 'key', text: '↻', 'aria-label': 'Refresh the picture', onclick: shot }),
      addr, h('button', { type: 'button', class: 'key go', text: 'Go', onclick: go })),
    stage,
    h('div', { class: 'toolbar' }, typed, h('button', { type: 'button', class: 'key go', text: 'Type', onclick: send })),
    h('div', { class: 'toolbar keys' }, key('Enter', 'Enter'), key('Tab', 'Tab'), key('Esc', 'Escape'), key('⌫', 'Backspace'), key('↑', 'ArrowUp'), key('↓', 'ArrowDown'), key('PgDn', 'PageDown'), key('PgUp', 'PageUp')),
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
  if (here !== '.' && here !== '/') rows.append(h('button', { class: 'row', type: 'button', onclick: () => { COMPUTER.path = parentPath(here); showComputer(); } }, h('span', { class: 'tag', text: '↑' }), h('span', { class: 'grow' }, h('span', { class: 't', text: 'Up a folder' }))));
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
  const src = '/api/files/' + id, isPDF = meta.mime === 'application/pdf';
  const body = openPanel(meta.name || 'Document', (isPDF ? 'PDF' : 'Image') + (meta.size ? ' · ' + fmtSize(meta.size) : ''));
  body.className = 'panel-body doc';
  let page = 1, zoom = 100;
  const view = h('div', { class: 'docview' });
  const draw = () => {
    if (isPDF) view.replaceChildren(h('iframe', { class: 'docframe', title: meta.name || 'Document', src: src + '?inline=1#page=' + page + '&zoom=' + zoom }));
    else { const im = h('img', { class: 'docimg', alt: meta.name || 'Image', src: src + '?inline=1' }); im.style.width = zoom + '%'; view.replaceChildren(im); }
    label.textContent = (isPDF ? 'Page ' + page + ' · ' : '') + zoom + '%';
  };
  const label = h('span', { class: 'doclabel mono' });
  const btn = (text, name, fn) => h('button', { type: 'button', class: 'key', text, 'aria-label': name, onclick: () => { fn(); draw(); } });
  const bar = h('div', { class: 'toolbar' },
    isPDF ? btn('‹', 'Previous page', () => { page = Math.max(1, page - 1); }) : null,
    isPDF ? btn('›', 'Next page', () => { page++; }) : null,
    btn('−', 'Zoom out', () => { zoom = Math.max(25, zoom - 25); }), btn('+', 'Zoom in', () => { zoom = Math.min(400, zoom + 25); }), label, h('span', { class: 'grow' }),
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
function fileIdIn(text) { const m = /\/api\/files\/([0-9a-fA-F]+)/.exec(text) || /kept the copy as file ([0-9a-fA-F]+)/.exec(text); return m ? m[1] : ''; }
function docCard(id, title, sub) {
  return h('div', { class: 'rich doc' }, h('span', { class: 'tag', text: 'PDF' }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: title }), sub ? h('span', { class: 's', text: sub }) : null),
    h('button', { class: 'btn small', type: 'button', text: 'View', onclick: () => openDoc(id) }),
    h('a', { class: 'btn small', href: '/api/files/' + id, download: '', text: 'Save' }));
}
function mailRows(text) {
  const rows = [];
  for (const line of text.split('\n')) {
    const m = /^id (\d+) ([* ]) (.+?) {2}From: (.*?) {2}Subject: (.*)$/.exec(line);
    if (m) rows.push({ id: m[1], unread: m[2] === '*', when: m[3], from: m[4], subject: m[5] });
  }
  if (!rows.length) return null;
  const box = h('div', { class: 'rich rows' });
  rows.slice(0, 8).forEach((r, i) => {
    const name = r.from.replace(/\s*<.*>$/, '') || r.from;
    const av = h('span', { class: 'avatar', text: (name.trim()[0] || '?').toUpperCase() });
    av.style.background = TINTS[i % 4][0]; av.style.color = TINTS[i % 4][1];
    box.append(h('button', { class: 'row', type: 'button', onclick: () => sendText('Read message ' + r.id + ' and tell me what it says.') }, av,
      h('span', { class: 'grow' }, h('span', { class: 't', text: name }), h('span', { class: 's', text: r.subject })),
      h('span', { class: 'when' + (r.unread ? ' state-info' : ''), text: r.when })));
  });
  if (rows.length > 8) box.append(h('div', { class: 'field-note', text: '+' + (rows.length - 8) + ' more in the full result.' }));
  return box;
}
function mailCard(text) {
  const from = /^From: (.*)$/m.exec(text), subject = /^Subject: (.*)$/m.exec(text), id = /^Message (\d+)$/m.exec(text);
  if (!from || !subject) return null;
  const start = text.indexOf('---\n'), end = text.indexOf('\n--- end of message ---');
  let bodyText = start >= 0 && end > start ? text.slice(start + 4, end).trim() : '';
  if (bodyText.length > 420) bodyText = bodyText.slice(0, 420) + '…';
  const name = from[1].replace(/\s*<.*>$/, '') || from[1];
  const card = h('div', { class: 'rich mail' }, h('div', { class: 'row' },
    h('span', { class: 'avatar tint', text: (name.trim()[0] || '?').toUpperCase() }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: subject[1] }), h('span', { class: 's', text: from[1] }))),
  bodyText ? h('div', { class: 'mailbody', text: bodyText }) : null);
  if (id) card.append(h('div', { class: 'btn-row' },
    h('button', { class: 'btn small', type: 'button', text: 'Draft a reply', onclick: () => sendText('Draft a reply to message ' + id[1] + ' and show it to me before anything is sent.') }),
    h('button', { class: 'btn small', type: 'button', text: 'Whole thread', onclick: () => sendText('Show me the whole conversation of message ' + id[1] + '.') })));
  return card;
}
function pageCard(text) {
  const title = /^Page: (.*)$/m.exec(text), url = /^URL: (.*)$/m.exec(text);
  if (!url) return null;
  let host = url[1];
  try { host = new URL(url[1]).host || url[1]; } catch { /* keep what was given */ }
  return h('div', { class: 'rich page' }, h('span', { class: 'tag', text: 'WEB' }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: (title && title[1]) || host }), h('span', { class: 's mono', text: url[1] })),
    h('button', { class: 'btn small', type: 'button', text: 'Take control', onclick: () => openComputer('browser') }));
}
function planCard(text) {
  const head = /^(?:updated\.\n)?Goal (\S+): (.*) \[(\w+)\]$/m.exec(text);
  if (!head) return null;
  const steps = [...text.matchAll(/^ {2}\d+\. \[([ x])\] (.*)$/gm)].map((m) => ({ done: m[1] === 'x', text: m[2] }));
  const next = /^Next: (.*)$/m.exec(text);
  const done = steps.filter((s) => s.done).length;
  const card = h('div', { class: 'rich plan' }, h('div', { class: 'row' }, h('span', { class: 'tag goal', text: 'GOAL' }),
    h('span', { class: 'grow' }, h('span', { class: 't', text: head[2] }), h('span', { class: 's', text: steps.length ? done + ' of ' + steps.length + ' steps done' : head[3] })),
    h('span', { class: 'pill ' + (head[3] === 'open' ? 'ok' : 'brass'), text: head[3].toUpperCase() })));
  if (steps.length) {
    const bar = h('i'); bar.style.width = Math.round((done / steps.length) * 100) + '%';
    card.append(h('div', { class: 'progress' }, bar), h('ul', { class: 'plansteps' }, steps.map((s) => h('li', { class: s.done ? 'done' : '' }, h('span', { class: 'box', 'aria-hidden': 'true', text: s.done ? '✓' : '' }), s.text))));
  }
  if (next) card.append(h('div', { class: 'field-note', text: 'Next: ' + next[1] }));
  return card;
}
function spendingCard(text) {
  const head = /^Imported (\d+) transactions \((.*?) to (.*?)\)\.\nSpending ([\d.]+), income ([\d.]+), saved (-?[\d.]+)\./.exec(text);
  if (!head) return null;
  const tail = text.slice(head[0].length);
  const cats = [...tail.matchAll(/([^.\d][^.]*?) (\d+\.\d\d)\.(?: |$)/g)].map((m) => ({ name: m[1].trim(), amount: Number(m[2]) })).filter((c) => !/^\+\d+ more/.test(c.name));
  const amount = (n) => Number(n).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  const stat = (label, value, cls) => h('div', { class: 'stat' }, h('span', { text: label }), h('b', { class: cls || '', text: amount(value) }));
  const card = h('div', { class: 'rich spend' },
    h('div', { class: 'row' }, h('span', { class: 'tag time', text: '$' }), h('span', { class: 'grow' }, h('span', { class: 't', text: 'Spending summary' }), h('span', { class: 's', text: head[1] + ' transactions · ' + head[2] + ' to ' + head[3] }))),
    h('div', { class: 'stats' }, stat('Spent', head[4]), stat('Income', head[5]), stat('Saved', head[6], Number(head[6]) >= 0 ? 'state-ok' : 'state-bad')));
  const max = Math.max(1, ...cats.map((c) => c.amount));
  for (const c of cats) {
    const fill = h('i'); fill.style.width = Math.max(2, Math.round((c.amount / max) * 100)) + '%';
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
function richResult(t) {
  const text = t.result || '';
  try {
    if (t.tool === 'email_list') return mailRows(text);
    if (t.tool === 'email_read') return mailCard(text);
    if (/^browser_(open|snapshot|click|type|press|select|scroll|back|wait)$/.test(t.tool)) return pageCard(text);
    if (/^goal_(create|get|update)$/.test(t.tool)) return planCard(text);
    if (t.tool === 'finance_import') return spendingCard(text);
    if (/^(files_make_pdf|pdf_form_fill|email_attachment)$/.test(t.tool)) {
      const id = fileIdIn(text);
      if (id) return docCard(id, t.tool === 'pdf_form_fill' ? 'Filled form' : t.tool === 'email_attachment' ? 'Attachment' : 'Document', text.split('\n')[0].slice(0, 120));
    }
  } catch { /* a result that does not parse is shown as text, as before */ }
  return null;
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
    if (archived) acts.append(h('button', { class: 'btn small', type: 'button', text: 'Restore', onclick: () => change({ unarchive: c.id }, 'Restored.') }));
    else if (!c.main) {
      acts.append(h('button', { class: 'btn small', type: 'button', text: 'Archive', onclick: () => change({ archive: c.id }, 'Archived. Nothing was deleted.') }));
      acts.append(h('button', { class: 'btn small', type: 'button', text: 'Make main', onclick: () => change({ main: c.id }) }));
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
  if (body.profile) { S.profile = { name: S.me.profile.name || '', avatar: S.me.profile.avatar || '' }; S.blocks.clear(); $('#thread').replaceChildren(); renderChat(); }
  renderSettings();
  return true;
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
      h('div', { class: 'btn-row' }, h('button', { class: 'btn primary', type: 'button', text: 'Save', onclick: () => saveMe({ profile: { name: name.value, avatar: avatar.value, tone: tone.value } }, 'Saved.') }))),
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
