'use strict';
/* Talaria's service worker: keeps the app's shell so it opens with no network, and shows notifications.
   It never keeps anything from /api: what is in the conversation, your mail and your calendar are only ever
   fetched live, and are not left in a cache on the phone. */

const CACHE = 'talaria-shell-v1';
const SHELL = ['/', '/app.css', '/app.js', '/manifest.webmanifest', '/icons/icon.svg', '/icons/icon-192.png',
  '/fonts/instrument.woff2', '/fonts/bricolage.woff2', '/fonts/jetbrains-mono.woff2'];

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== 'GET' || url.origin !== location.origin || url.pathname.startsWith('/api/')) return;
  // Fonts and icons never change under the same name: use the copy kept. The rest comes from the network
  // when there is one, so an update shows at once, and from the copy kept when there is not.
  const still = url.pathname.startsWith('/fonts/') || url.pathname.startsWith('/icons/');
  e.respondWith((async () => {
    const cache = await caches.open(CACHE);
    if (still) { const hit = await cache.match(e.request); if (hit) return hit; }
    try {
      const res = await fetch(e.request);
      if (res.ok) cache.put(e.request, res.clone());
      return res;
    } catch (err) {
      const hit = (await cache.match(e.request)) || (e.request.mode === 'navigate' ? await cache.match('/') : null);
      if (hit) return hit;
      throw err;
    }
  })());
});

self.addEventListener('push', (e) => {
  let d = {};
  try { d = e.data ? e.data.json() : {}; } catch { d = { title: 'Talaria', body: e.data ? e.data.text() : '' }; }
  const title = String(d.title || 'Talaria').slice(0, 80);
  e.waitUntil(self.registration.showNotification(title, {
    body: String(d.body || '').slice(0, 200),
    tag: d.tag || 'talaria',
    renotify: d.tag === 'approval',
    icon: '/icons/icon-192.png',
    badge: '/icons/icon-192.png',
    data: { url: typeof d.url === 'string' && d.url.startsWith('/') ? d.url : '/' },
  }));
});

// Tapping a notification opens the app at the right screen. It does not answer anything: a question that could
// run a command is answered in the app, where all of it can be read.
self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const target = (e.notification.data && e.notification.data.url) || '/';
  const hash = target.includes('#') ? target.slice(target.indexOf('#')) : '';
  e.waitUntil((async () => {
    const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const c of all) {
      if (new URL(c.url).origin === location.origin) {
        await c.focus();
        if (hash) c.postMessage({ go: hash });
        return;
      }
    }
    await self.clients.openWindow(target);
  })());
});
