/* ax service worker: network-first for the shell so the last good UI is
   still available if a bad build breaks its assets. Never caches the API.
   The cache name stays v1 on purpose: installing the new worker refreshes the
   shell in place and never throws away the last good copy. */
const CACHE = 'ax-shell-v1';
const SHELL = ['/', '/static/app.js', '/static/app.css', '/static/icon-192.png', '/static/icon-512.png', '/static/fonts/geist-latin.woff2', '/static/fonts/geist-mono-latin.woff2'];

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()).catch(() => {}));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname === '/healthz') return;

  e.respondWith((async () => {
    try {
      const res = await fetch(req);
      if (res.ok) {
        const copy = res.clone();
        caches.open(CACHE).then((c) => c.put(req, copy)).catch(() => {});
      }
      return res;
    } catch (err) {
      const hit = await caches.match(req, { ignoreSearch: false });
      if (hit) return hit;
      if (req.mode === 'navigate') {
        const shell = await caches.match('/');
        if (shell) return shell;
      }
      throw err;
    }
  })());
});

/* Local run notifications: the page hands us the thread to open when the
   notification is tapped. Focus an existing window and let app.js route the
   hash; only fall back to opening a new window when nothing is alive. */
function notificationTarget(data) {
  const ch = (data && data.channel) || '';
  const th = (data && data.thread) || '';
  const rt = (data && data.routine) || '';
  if (rt && ch) return '#/routines/' + ch + '/' + rt + (data.run ? '/' + data.run : '');
  if (th && ch) return '#/c/' + ch + '/t/' + th;
  if (ch) return '#/c/' + ch;
  return '#/';
}

self.addEventListener('notificationclick', (event) => {
  const notif = event.notification;
  const data = (notif && notif.data) || {};
  if (notif) notif.close();
  const target = new URL(notificationTarget(data), self.location.origin).href;
  event.waitUntil((async () => {
    let wins = [];
    try {
      wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    } catch (err) {
      wins = [];
    }
    const client = wins.find((c) => {
      try { return new URL(c.url).origin === self.location.origin; } catch (err) { return false; }
    });
    if (client) {
      try { await client.focus(); } catch (err) {}
      client.postMessage({ type: 'ax-open', thread: data.thread || '', channel: data.channel || '', routine: data.routine || '', run: data.run || '' });
      return;
    }
    if (self.clients.openWindow) {
      try { await self.clients.openWindow(target); } catch (err) {}
    }
  })());
});
