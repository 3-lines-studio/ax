'use strict';

/* ===========================================================================
   ax UI — vanilla JS, no build step, no dependencies.
   =========================================================================== */

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const S = {
  state: null,
  bootBuild: null,
  newBuild: '',
  channelId: null,
  threadId: null,
  thread: null,
  messages: [],
  running: false,
  live: new Map(),
  liveFrame: 0,
  pending: [],
  uploads: [],
  drafts: {},
  vaultData: null,
  vaultPin: '',
  vaultOpen: null,
  vaultFilter: '',
  vaultNeedPIN: false,
  vaultShown: {},
  es: null,
  connected: false,
  stick: true,
  stateTimer: 0,
  keyWarned: false,
};

/* ------------------------------------------------------------------ utils */

const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
function esc(v) {
  return String(v == null ? '' : v).replace(/[&<>"']/g, (c) => ESC[c]);
}

function fmtTime(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return '';
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function relTime(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return '';
  const s = (Date.now() - d.getTime()) / 1000;
  if (s < 60) return 'now';
  if (s < 3600) return Math.floor(s / 60) + 'm';
  if (s < 86400) return Math.floor(s / 3600) + 'h';
  if (s < 86400 * 7) return Math.floor(s / 86400) + 'd';
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
}

function shortBuild(b) {
  if (!b || b === 'dev') return 'dev';
  return String(b).replace(/^\d{8}-/, '');
}

/* Phosphor Icons, duotone weight (https://phosphoricons.com), MIT.
   Only the glyphs this UI uses are vendored, so the app stays dependency-free;
   the secondary path keeps Phosphor's 0.2 fill and inherits currentColor. */
const ICON_PATHS = {
  'list': '<path d="M216,64V192H40V64Z" opacity="0.2"/><path d="M224,128a8,8,0,0,1-8,8H40a8,8,0,0,1,0-16H216A8,8,0,0,1,224,128ZM40,72H216a8,8,0,0,0,0-16H40a8,8,0,0,0,0,16ZM216,184H40a8,8,0,0,0,0,16H216a8,8,0,0,0,0-16Z"/>',
  'arrow-left': '<path d="M112,56V200L40,128Z" opacity="0.2"/><path d="M216,120H120V56a8,8,0,0,0-13.66-5.66l-72,72a8,8,0,0,0,0,11.32l72,72A8,8,0,0,0,120,200V136h96a8,8,0,0,0,0-16ZM104,180.69,51.31,128,104,75.31Z"/>',
  'arrow-down': '<path d="M200,144l-72,72L56,144Z" opacity="0.2"/><path d="M207.39,140.94A8,8,0,0,0,200,136H136V40a8,8,0,0,0-16,0v96H56a8,8,0,0,0-5.66,13.66l72,72a8,8,0,0,0,11.32,0l72-72A8,8,0,0,0,207.39,140.94ZM128,204.69,75.31,152H180.69Z"/>',
  'x': '<path d="M216,56V200a16,16,0,0,1-16,16H56a16,16,0,0,1-16-16V56A16,16,0,0,1,56,40H200A16,16,0,0,1,216,56Z" opacity="0.2"/><path d="M205.66,194.34a8,8,0,0,1-11.32,11.32L128,139.31,61.66,205.66a8,8,0,0,1-11.32-11.32L116.69,128,50.34,61.66A8,8,0,0,1,61.66,50.34L128,116.69l66.34-66.35a8,8,0,0,1,11.32,11.32L139.31,128Z"/>',
  'plus': '<path d="M216,56V200a16,16,0,0,1-16,16H56a16,16,0,0,1-16-16V56A16,16,0,0,1,56,40H200A16,16,0,0,1,216,56Z" opacity="0.2"/><path d="M224,128a8,8,0,0,1-8,8H136v80a8,8,0,0,1-16,0V136H40a8,8,0,0,1,0-16h80V40a8,8,0,0,1,16,0v80h80A8,8,0,0,1,224,128Z"/>',
  'plus-circle': '<path d="M224,128a96,96,0,1,1-96-96A96,96,0,0,1,224,128Z" opacity="0.2"/><path d="M128,24A104,104,0,1,0,232,128,104.11,104.11,0,0,0,128,24Zm0,192a88,88,0,1,1,88-88A88.1,88.1,0,0,1,128,216Zm48-88a8,8,0,0,1-8,8H136v32a8,8,0,0,1-16,0V136H88a8,8,0,0,1,0-16h32V88a8,8,0,0,1,16,0v32h32A8,8,0,0,1,176,128Z"/>',
  'gear-six': '<path d="M230.1,108.76,198.25,90.62c-.64-1.16-1.31-2.29-2-3.41l-.12-36A104.61,104.61,0,0,0,162,32L130,49.89c-1.34,0-2.69,0-4,0L94,32A104.58,104.58,0,0,0,59.89,51.25l-.16,36c-.7,1.12-1.37,2.26-2,3.41l-31.84,18.1a99.15,99.15,0,0,0,0,38.46l31.85,18.14c.64,1.16,1.31,2.29,2,3.41l.12,36A104.61,104.61,0,0,0,94,224l32-17.87c1.34,0,2.69,0,4,0L162,224a104.58,104.58,0,0,0,34.08-19.25l.16-36c.7-1.12,1.37-2.26,2-3.41l31.84-18.1A99.15,99.15,0,0,0,230.1,108.76ZM128,168a40,40,0,1,1,40-40A40,40,0,0,1,128,168Z" opacity="0.2"/><path d="M128,80a48,48,0,1,0,48,48A48.05,48.05,0,0,0,128,80Zm0,80a32,32,0,1,1,32-32A32,32,0,0,1,128,160Zm109.94-52.79a8,8,0,0,0-3.89-5.4l-29.83-17-.12-33.62a8,8,0,0,0-2.83-6.08,111.91,111.91,0,0,0-36.72-20.67,8,8,0,0,0-6.46.59L128,41.85,97.88,25a8,8,0,0,0-6.47-.6A111.92,111.92,0,0,0,54.73,45.15a8,8,0,0,0-2.83,6.07l-.15,33.65-29.83,17a8,8,0,0,0-3.89,5.4,106.47,106.47,0,0,0,0,41.56,8,8,0,0,0,3.89,5.4l29.83,17,.12,33.63a8,8,0,0,0,2.83,6.08,111.91,111.91,0,0,0,36.72,20.67,8,8,0,0,0,6.46-.59L128,214.15,158.12,231a7.91,7.91,0,0,0,3.9,1,8.09,8.09,0,0,0,2.57-.42,112.1,112.1,0,0,0,36.68-20.73,8,8,0,0,0,2.83-6.07l.15-33.65,29.83-17a8,8,0,0,0,3.89-5.4A106.47,106.47,0,0,0,237.94,107.21Zm-15,34.91-28.57,16.25a8,8,0,0,0-3,3c-.58,1-1.19,2.06-1.81,3.06a7.94,7.94,0,0,0-1.22,4.21l-.15,32.25a95.89,95.89,0,0,1-25.37,14.3L134,199.13a8,8,0,0,0-3.91-1h-.19c-1.21,0-2.43,0-3.64,0a8.1,8.1,0,0,0-4.1,1l-28.84,16.1A96,96,0,0,1,67.88,201l-.11-32.2a8,8,0,0,0-1.22-4.22c-.62-1-1.23-2-1.8-3.06a8.09,8.09,0,0,0-3-3.06l-28.6-16.29a90.49,90.49,0,0,1,0-28.26L61.67,97.63a8,8,0,0,0,3-3c.58-1,1.19-2.06,1.81-3.06a7.94,7.94,0,0,0,1.22-4.21l.15-32.25a95.89,95.89,0,0,1,25.37-14.3L122,56.87a8,8,0,0,0,4.1,1c1.21,0,2.43,0,3.64,0a8,8,0,0,0,4.1-1l28.84-16.1A96,96,0,0,1,188.12,55l.11,32.2a8,8,0,0,0,1.22,4.22c.62,1,1.23,2,1.8,3.06a8.09,8.09,0,0,0,3,3.06l28.6,16.29A90.49,90.49,0,0,1,222.9,142.12Z"/>',
  'lock-simple': '<path d="M216,96V208a8,8,0,0,1-8,8H48a8,8,0,0,1-8-8V96a8,8,0,0,1,8-8H208A8,8,0,0,1,216,96Z" opacity="0.2"/><path d="M208,80H176V56a48,48,0,0,0-96,0V80H48A16,16,0,0,0,32,96V208a16,16,0,0,0,16,16H208a16,16,0,0,0,16-16V96A16,16,0,0,0,208,80ZM96,56a32,32,0,0,1,64,0V80H96ZM208,208H48V96H208V208Z"/>',
  'bell': '<path d="M208,192H48a8,8,0,0,1-6.88-12C47.71,168.6,56,139.81,56,104a72,72,0,0,1,144,0c0,35.82,8.3,64.6,14.9,76A8,8,0,0,1,208,192Z" opacity="0.2"/><path d="M221.8,175.94C216.25,166.38,208,139.33,208,104a80,80,0,1,0-160,0c0,35.34-8.26,62.38-13.81,71.94A16,16,0,0,0,48,200H88.81a40,40,0,0,0,78.38,0H208a16,16,0,0,0,13.8-24.06ZM128,216a24,24,0,0,1-22.62-16h45.24A24,24,0,0,1,128,216ZM48,184c7.7-13.24,16-43.92,16-80a64,64,0,1,1,128,0c0,36.05,8.28,66.73,16,80Z"/>',
  'paperclip': '<path d="M180.75,104.75,204,128l-82.06,81.94a48,48,0,0,1-67.88-67.88L153.37,41.37a32,32,0,0,1,45.26,45.26Z" opacity="0.2"/><path d="M209.66,122.34a8,8,0,0,1,0,11.32l-82.05,82a56,56,0,0,1-79.2-79.21L147.67,35.73a40,40,0,1,1,56.61,56.55L105,193A24,24,0,1,1,71,159L154.3,74.38A8,8,0,1,1,165.7,85.6L82.39,170.31a8,8,0,1,0,11.27,11.36L192.93,81A24,24,0,1,0,159,47L59.76,147.68a40,40,0,1,0,56.53,56.62l82.06-82A8,8,0,0,1,209.66,122.34Z"/>',
  'paper-plane-tilt': '<path d="M223.69,42.18l-58.22,192a8,8,0,0,1-14.92,1.25L108,148,20.58,105.45a8,8,0,0,1,1.25-14.92l192-58.22A8,8,0,0,1,223.69,42.18Z" opacity="0.2"/><path d="M227.32,28.68a16,16,0,0,0-15.66-4.08l-.15,0L19.57,82.84a16,16,0,0,0-2.49,29.8L102,154l41.3,84.87A15.86,15.86,0,0,0,157.74,248q.69,0,1.38-.06a15.88,15.88,0,0,0,14-11.51l58.2-191.94c0-.05,0-.1,0-.15A16,16,0,0,0,227.32,28.68ZM157.83,231.85l-.05.14,0-.07-40.06-82.3,48-48a8,8,0,0,0-11.31-11.31l-48,48L24.08,98.25l-.07,0,.14,0L216,40Z"/>',
  'stop': '<path d="M208,56V200a8,8,0,0,1-8,8H56a8,8,0,0,1-8-8V56a8,8,0,0,1,8-8H200A8,8,0,0,1,208,56Z" opacity="0.2"/><path d="M200,40H56A16,16,0,0,0,40,56V200a16,16,0,0,0,16,16H200a16,16,0,0,0,16-16V56A16,16,0,0,0,200,40Zm0,160H56V56H200V200Z"/>',
  'caret-down': '<path d="M208,96l-80,80L48,96Z" opacity="0.2"/><path d="M215.39,92.94A8,8,0,0,0,208,88H48a8,8,0,0,0-5.66,13.66l80,80a8,8,0,0,0,11.32,0l80-80A8,8,0,0,0,215.39,92.94ZM128,164.69,67.31,104H188.69Z"/>',
  'caret-right': '<path d="M176,128,96,208V48Z" opacity="0.2"/><path d="M181.66,122.34l-80-80A8,8,0,0,0,88,48V208a8,8,0,0,0,13.66,5.66l80-80A8,8,0,0,0,181.66,122.34ZM104,188.69V67.31L164.69,128Z"/>',
  'magnifying-glass': '<path d="M192,112a80,80,0,1,1-80-80A80,80,0,0,1,192,112Z" opacity="0.2"/><path d="M229.66,218.34,179.6,168.28a88.21,88.21,0,1,0-11.32,11.31l50.06,50.07a8,8,0,0,0,11.32-11.32ZM40,112a72,72,0,1,1,72,72A72.08,72.08,0,0,1,40,112Z"/>',
  'push-pin': '<path d="M216,168h-9.29L185.54,48H192a8,8,0,0,0,0-16H64a8,8,0,0,0,0,16h6.46L49.29,168H40a8,8,0,0,0,0,16h80v56a8,8,0,0,0,16,0V184h80a8,8,0,0,0,0-16ZM86.71,48h82.58l21.17,120H65.54Z"/>',
};

function ico(name, cls) {
  const paths = ICON_PATHS[name];
  if (!paths) return '';
  return '<svg class="ico' + (cls ? ' ' + cls : '') + '" viewBox="0 0 256 256" fill="currentColor" aria-hidden="true" focusable="false">' + paths + '</svg>';
}

/* A promotion replaces the server under a page that is still running old JS.
   When the build we booted with disappears (state refresh or SSE hello), reload
   so the next tap runs the new code. Never eat a draft or a live run; the
   check repeats on the next state refresh instead. */
function noteBuild(build) {
  if (!build || build === 'dev') return;
  if (!S.bootBuild) { S.bootBuild = build; return; }
  if (build === S.bootBuild) return;
  S.newBuild = build;
  maybeReloadCode();
}

function maybeReloadCode() {
  if (!S.newBuild) return;
  const ta = $('#input');
  if (S.running || (ta && ta.value.trim())) return;
  // Never reload under an open keyboard: the reloaded page can keep a shrunken
  // layout and draw the composer behind it. The viewport resize retries later.
  const ae = document.activeElement;
  if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA' || ae.isContentEditable)) return;
  let seen = '';
  try { seen = sessionStorage.getItem('ax.reload') || ''; } catch (e) {}
  if (seen === S.newBuild) return;
  try { sessionStorage.setItem('ax.reload', S.newBuild); } catch (e) {}
  location.reload();
}

/* Touch-first UI: no autofocus (it would pop the Android keyboard on every
   thread open) and Enter inserts a newline instead of sending. */
function coarsePointer() {
  return !!(window.matchMedia && window.matchMedia('(pointer: coarse)').matches);
}

async function api(path, opts = {}) {
  const headers = Object.assign({}, opts.headers || {});
  if (opts.body && typeof opts.body === 'string') headers['Content-Type'] = 'application/json';
  const res = await fetch(path, Object.assign({}, opts, { headers }));
  const ct = res.headers.get('content-type') || '';
  let body = null;
  if (ct.includes('application/json')) body = await res.json();
  else body = await res.text();
  if (!res.ok) {
    const err = new Error((body && body.error) || res.statusText || ('HTTP ' + res.status));
    err.status = res.status;
    throw err;
  }
  return body;
}

function toast(msg, isErr) {
  const box = $('#toasts');
  const el = document.createElement('div');
  el.className = 'toast' + (isErr ? ' err' : '');
  el.textContent = msg;
  box.appendChild(el);
  setTimeout(() => {
    el.style.transition = 'opacity .2s';
    el.style.opacity = '0';
    setTimeout(() => el.remove(), 220);
  }, isErr ? 6500 : 2600);
}

function findChannel(id) {
  return (S.state && S.state.channels || []).find((c) => c.id === id) || null;
}
function findThread(id) {
  for (const c of (S.state && S.state.channels) || []) {
    const t = (c.threads || []).find((x) => x.id === id);
    if (t) return Object.assign({}, t, { channelId: c.id });
  }
  return null;
}

/* ------------------------------------------------------------------- boot */

document.addEventListener('DOMContentLoaded', () => {
  wireViewport();
  wireEvents();
  boot();
});

async function boot() {
  restoreRoute();
  syncNotifyButton();
  try {
    await refreshState();
  } catch (e) {
    $('#main').innerHTML = '<div class="boot">cannot reach ax: ' + esc(e.message) + '</div>';
  }
  await route();
  connectSSE();
  registerSW();
}

/* ------------------------------------------------------------------ state */

// Pinned channels first, then case-insensitive name, so "zeta" and "Zeta" do
// not end up in separate blocks. Creation time breaks ties between equal names.
function byChannelOrder(a, b) {
  if (!!a.pinned !== !!b.pinned) return a.pinned ? -1 : 1;
  return String(a.name).localeCompare(String(b.name), undefined, { sensitivity: 'base' }) ||
    String(a.created || '').localeCompare(String(b.created || '')) ||
    String(a.id).localeCompare(String(b.id));
}

async function refreshState() {
  const st = await api('/api/state');
  const prevBuild = S.state && S.state.build;
  // The API returns channels in creation order; the sidebar reads better with
  // pinned ones on top and the rest alphabetically, and every view reads the
  // state in this order.
  st.channels = (st.channels || []).slice().sort(byChannelOrder);
  S.state = st;
  noteBuild(st.build);
  // A run that is still going must be allowed to notify when it ends, even
  // if we never saw its running:true event (e.g. after a reconnect).
  for (const c of st.channels || []) {
    for (const t of c.threads || []) if (t.running) NOTIFY.done.delete(t.id);
  }
  $('#build-badge').textContent = shortBuild(st.build);
  $('#build-badge').title = 'Reload app · ' + (st.build || 'dev');
  syncKeyBanner();
  syncVaultBanner();
  syncSecretBanner();
  renderSidebar();
  if (S.channelId && !findChannel(S.channelId)) {
    S.channelId = null;
    S.threadId = null;
    S.thread = null;
  }
  if (S.threadId) {
    const t = findThread(S.threadId);
    if (!t) {
      location.hash = S.channelId ? '#/c/' + S.channelId : '#/';
      return;
    }
    const h = $('#thread-title');
    if (h && S.thread && t.title !== S.thread.title) { S.thread.title = t.title; h.textContent = t.title; }
    if (t.running !== S.running) { S.running = t.running; renderWorking(); setComposerMode(); }
  } else if (S.channelId) {
    renderChannelView();
  } else if (onVaultRoute()) {
    // keep the vault view
  } else {
    renderHome();
  }
  if (prevBuild && st.build && prevBuild !== st.build) {
    if (S.threadId) loadThread(S.threadId);
  }
}

function scheduleStateRefresh() {
  clearTimeout(S.stateTimer);
  S.stateTimer = setTimeout(() => refreshState().catch(() => {}), 220);
}

/* ---------------------------------------------------------------- routing */

const ROUTE_KEY = 'ax.route';

/* A killed and relaunched PWA starts at "/" and would forget the open thread.
   Remember the last hash and put it back before the first route, without
   adding a history entry. Explicit deep links still win. */
function rememberRoute() {
  try { localStorage.setItem(ROUTE_KEY, location.hash); } catch (e) {}
}

function restoreRoute() {
  if (location.hash && location.hash !== '#') return;
  let last = '';
  try { last = localStorage.getItem(ROUTE_KEY) || ''; } catch (e) {}
  if (!last) return;
  try { history.replaceState(history.state, '', last); } catch (e) {}
}

async function route() {
  closeMenu(false);
  const hash = location.hash.replace(/^#/, '') || '/';
  if (hash === '/vault') {
    S.channelId = null;
    S.threadId = null;
    S.thread = null;
    rememberRoute();
    await loadVault();
    closeDrawer(true);
    return;
  }
  const m = hash.match(/^\/c\/([a-zA-Z0-9_]+)(?:\/t\/([a-zA-Z0-9_]+))?$/);
  if (!m) {
    const first = S.state && S.state.channels && S.state.channels[0];
    if (first) { location.replace('#/c/' + first.id); return; }
    S.channelId = null; S.threadId = null; S.thread = null;
    renderHome();
    closeDrawer(true);
    return;
  }
  const chId = m[1];
  const thId = m[2] || null;
  // A restored hash can point at a channel deleted since last launch.
  if (S.state && !findChannel(chId)) { location.replace('#/'); return; }
  S.channelId = chId;
  rememberRoute();
  if (thId) {
    if (S.threadId !== thId) {
      S.threadId = thId;
      S.thread = null;
      S.messages = [];
      S.live.clear();
      S.running = false;
      S.stick = true;
      renderThreadShell();
      await loadThread(thId);
    }
  } else {
    S.threadId = null;
    S.thread = null;
    renderChannelView();
  }
  renderSidebar();
  closeDrawer(true);
}

/* -------------------------------------------------------------- rendering */

function renderSidebar() {
  const nav = $('#channels');
  const chans = (S.state && S.state.channels) || [];
  nav.innerHTML = chans.map((c) => {
    const running = (c.threads || []).some((t) => t.running);
    return '<a class="channel' + (c.id === S.channelId ? ' active' : '') + '" href="#/c/' + c.id + '">' +
      '<span class="hash">#</span><span class="cname">' + esc(c.name) + '</span>' +
      (c.pinned ? '<span class="pin" title="Pinned">' + ico('push-pin') + '</span>' : '') +
      (running ? '<span class="dot" title="running"></span>' : '') +
      '</a>';
  }).join('') || '<div style="padding:12px 10px;color:var(--faint);font-size:12px">No channels yet</div>';
  const vb = document.querySelector('.side-btn[data-action="vault"]');
  if (vb) vb.classList.toggle('active', onVaultRoute());
}

function syncKeyBanner() {
  if (onVaultRoute()) return;
  const existing = document.querySelector('#main .banner.key');
  const v = S.state && S.state.vault;
  // Only after the vault exists and is open: before that, the vault banner is
  // the one that knows what to do.
  const need = S.state && S.state.config && !S.state.config.has_key && !!(v && v.exists && !v.locked);
  if (need && !existing) {
    const header = $('#main .topbar');
    if (header) header.insertAdjacentHTML('afterend', keyBannerHTML());
  } else if (!need && existing) {
    existing.remove();
  }
}

function keyBannerHTML() {
  if (!S.state || S.state.config.has_key) return '';
  // Without a vault, or with it locked, the key is out of reach and the vault
  // banner already explains how to fix that.
  const v = S.state.vault;
  if (!v || !v.exists || v.locked) return '';
  return '<div class="banner key"><div class="grow">Add your DeepSeek API key to start chatting. It stays in the vault. <a href="https://platform.deepseek.com/api_keys" target="_blank" rel="noopener">Get a key</a>.</div>' +
    '<button class="btn" data-action="add-key">Add key</button></div>';
}

function emptyHTML(title, sub) {
  return '<div class="empty"><strong>' + esc(title) + '</strong>' + esc(sub || '') + '</div>';
}

function emptyThreadHTML() {
  return '<div class="empty thread-empty">' +
    '<span class="logo empty-logo" aria-hidden="true"></span>' +
    '<strong>Ask ax anything</strong>' +
    '<span>Message below, or attach an image.</span></div>';
}

// The vault banner is the only UI the vault has: when it exists and is
// locked, one PIN line appears above the chat. Nothing else changes.
function vaultBannerHTML() {
  const v = S.state && S.state.vault;
  if (!v || !v.exists) {
    return '<div class="banner vault"><div class="grow">Create a vault to keep your API key encrypted on disk. You choose a PIN of 8+ characters.</div>' +
      '<button class="btn" data-action="vault-create">Create vault</button></div>';
  }
  if (!v.locked) return '';
  return '<div class="banner vault"><div class="grow">Vault locked. Secrets are off.</div>' +
    '<input id="vault-pin" type="password" autocomplete="current-password" placeholder="PIN" aria-label="Vault PIN">' +
    '<button class="btn" data-action="vault-unlock">Unlock</button></div>';
}

function currentSecret() {
  const pending = (S.state && S.state.pending_secret) || [];
  return pending.find((p) => p.thread === S.threadId) || null;
}

function secretBannerHTML() {
  const s = currentSecret();
  if (!s) return '';
  const what = s.description ? ' — ' + esc(s.description) : '';
  return '<div class="banner secret"><div class="grow">ax needs <strong>' + esc(s.name) + '</strong> (' + esc(s.scope) + ')' + what + '</div>' +
    '<textarea id="secret-value" rows="2" spellcheck="false" autocomplete="off" autocapitalize="off" autocorrect="off" placeholder="value or file content" aria-label="' + esc(s.name) + '" title="Ctrl+Enter saves"></textarea>' +
    '<button class="btn primary" data-action="secret-save">Save</button>' +
    '<button class="btn" data-action="secret-skip">Skip</button></div>';
}

function syncSecretBanner() {
  if (onVaultRoute()) return;
  const want = !!currentSecret();
  const existing = document.querySelector('#main .banner.secret');
  if (want && !existing) {
    const header = $('#main .topbar');
    if (header) header.insertAdjacentHTML('afterend', secretBannerHTML());
  } else if (!want && existing) {
    existing.remove();
  }
}

async function saveSecret() {
  const s = currentSecret();
  const input = $('#secret-value');
  if (!s || !input) return;
  const value = input.value;
  if (!value) { input.focus(); return; }
  try {
    await api('/api/vault/secret', { method: 'POST', body: JSON.stringify({ id: s.id, value: value }) });
    input.value = '';
    await refreshState();
  } catch (e) {
    toast(e.message, true);
    input.select();
  }
}

async function skipSecret() {
  const s = currentSecret();
  if (!s) return;
  try {
    await api('/api/vault/secret', { method: 'POST', body: JSON.stringify({ id: s.id, skip: true }) });
    await refreshState();
  } catch (e) { toast(e.message, true); }
}

/* ------------------------------------------------------------------ vault */

const VAULT_COLORS = ['#2f5d43', '#34506e', '#5f4a2e', '#4f3968', '#2f5d5d', '#5d3346'];

function onVaultRoute() {
  return location.hash === '#/vault';
}

function vaultColor(scope) {
  let h = 0;
  for (let i = 0; i < scope.length; i++) h = (h * 31 + scope.charCodeAt(i)) >>> 0;
  return VAULT_COLORS[h % VAULT_COLORS.length];
}

function vaultTotal() {
  const vars = (S.vaultData && S.vaultData.vars) || {};
  return Object.keys(vars).reduce((n, k) => n + (vars[k] || []).length, 0);
}

// Every channel is a scope; the vault lists them all, even empty ones, so the
// map of "what lives where" is visible without opening anything.
function vaultGroups() {
  const vars = (S.vaultData && S.vaultData.vars) || {};
  const groups = [{ scope: 'global', label: 'Global', note: 'every channel', names: vars['global'] || [] }];
  const byScope = new Map();
  for (const c of (S.state && S.state.channels) || []) {
    if (!c.vault_scope) continue;
    if (!byScope.has(c.vault_scope)) byScope.set(c.vault_scope, []);
    byScope.get(c.vault_scope).push(c.name);
  }
  for (const scope of Object.keys(vars)) {
    if (scope !== 'global' && !byScope.has(scope)) byScope.set(scope, []);
  }
  for (const [scope, chans] of byScope) {
    groups.push({
      scope: scope,
      label: chans[0] || scope,
      note: chans.length > 1 ? 'shared by ' + chans.join(', ') : (chans.length ? 'channel' : 'no channel'),
      names: vars[scope] || [],
    });
  }
  return groups;
}

function vaultItemHTML(scope, name) {
  const key = scope + '/' + name;
  const open = S.vaultOpen === key;
  const shown = S.vaultShown[key];
  const used = ((S.vaultData && S.vaultData.last_used) || {})[key];
  return '<div class="vi' + (open ? ' open' : '') + '" data-scope="' + esc(scope) + '" data-name="' + esc(name) + '">' +
    '<div class="vi-head" data-action="vault-item">' +
    '<span class="vi-name">' + esc(name) + '</span>' +
    '<span class="vi-mask">••••••••</span>' +
    '<span class="vi-chev">' + ico(open ? 'caret-down' : 'caret-right') + '</span></div>' +
    (open ? '<div class="vi-detail">' +
      (S.vaultNeedPIN && !shown ? '<div class="vi-pin"><input class="vpin" type="password" placeholder="PIN to show values" autocomplete="current-password"><button class="btn" data-action="vault-pin-ok">OK</button></div>' : '') +
      '<div class="vi-value">' + (shown ? esc(shown) : '••••••••••••••••') + '</div>' +
      '<div class="vi-meta">' + (used ? 'last used ' + esc(relTime(used)) : 'never used') + '</div>' +
      '<div class="vi-actions">' +
      '<button data-action="vault-show">Show</button>' +
      '<button data-action="vault-copy">Copy</button>' +
      '<button data-action="vault-edit">Edit</button>' +
      '<button class="danger" data-action="vault-del">Delete</button>' +
      '</div></div>' : '') +
    '</div>';
}

function vaultGroupsHTML() {
  const q = (S.vaultFilter || '').trim().toLowerCase();
  let out = '';
  for (const g of vaultGroups()) {
    const names = g.names.filter((n) => !q || n.toLowerCase().includes(q) || g.label.toLowerCase().includes(q) || g.scope.toLowerCase().includes(q));
    if (!names.length && q) continue;
    out += '<section class="vg"><div class="vg-head">' +
      '<span class="vg-avatar" style="background:' + vaultColor(g.scope) + '">' + esc((g.label || '?').charAt(0).toUpperCase()) + '</span>' +
      '<div class="vg-name">' + esc(g.label) + '<em>' + esc(g.note) + '</em></div>' +
      '<span class="vg-count">' + g.names.length + '</span>' +
      '<button type="button" class="vg-add" data-action="vault-new" data-scope="' + esc(g.scope) + '" title="New secret in ' + esc(g.label) + '" aria-label="New secret in ' + esc(g.label) + '">' + ico('plus') + '</button>' +
      '</div><div class="vg-items">' +
      (names.length ? names.map((n) => vaultItemHTML(g.scope, n)).join('') : '<div class="vi-empty">No secrets yet.</div>') +
      '</div></section>';
  }
  return out || '<div class="vault-none">No matches.</div>';
}

function vaultGateHTML(title, text, actions) {
  return '<div class="vault-gate"><div class="glyph">' + ico('lock-simple') + '</div>' +
    '<h2>' + esc(title) + '</h2><p>' + esc(text) + '</p>' + actions + '</div>';
}

function paintVault() {
  const d = S.vaultData || { exists: false, locked: true };
  const total = vaultTotal();
  const sub = !d.exists ? 'not created yet'
    : d.locked ? 'locked'
    : total + (total === 1 ? ' secret · unlocked' : ' secrets · unlocked');
  let body;
  if (!d.exists) {
    body = vaultGateHTML('No vault yet', 'Create a PIN to encrypt your secrets on disk. There is no recovery if you lose it.',
      '<button type="button" class="btn primary" data-action="vault-create">Create vault</button>');
  } else if (d.locked) {
    body = vaultGateHTML('Vault locked', 'Your secrets are encrypted on disk. Unlock to use them.',
      '<div class="vault-pin"><input id="vault-pin" type="password" placeholder="PIN" autocomplete="current-password" aria-label="Vault PIN">' +
      '<button type="button" class="btn primary" data-action="vault-unlock">Unlock</button></div>');
  } else {
    body = '<div class="vault-tools"><input id="vault-search" placeholder="Search secrets" autocomplete="off" aria-label="Search secrets"></div>' +
      '<div id="vault-groups">' + vaultGroupsHTML() + '</div>';
  }
  $('#main').innerHTML = '<header class="topbar">' +
    '<button class="icon-btn menu-btn" data-action="drawer" aria-label="Channels" aria-expanded="false">' + ico('list') + '</button>' +
    '<div class="titles"><h1>Vault</h1><p>' + esc(sub) + '</p></div>' +
    '<div class="actions">' + (d.exists && !d.locked ? '<button type="button" class="btn" data-action="vault-import" title="Import a .env file">Import .env</button>' +
      '<button type="button" class="btn primary" data-action="vault-new">New secret</button>' : '') + '</div>' +
    '</header><div class="scroll"><div class="vault-shell">' + body + '</div></div>';
  const search = $('#vault-search');
  if (search) {
    search.value = S.vaultFilter || '';
    search.addEventListener('input', () => {
      S.vaultFilter = search.value;
      repaintVaultGroups();
    });
  }
  const pin = $('#vault-pin');
  if (pin) {
    pin.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); vaultUnlock(); }
    });
    setTimeout(() => pin.focus(), 40);
  }
  renderSidebar();
}

function repaintVaultGroups() {
  const box = $('#vault-groups');
  if (box) box.innerHTML = vaultGroupsHTML();
}

async function loadVault() {
  try {
    S.vaultData = await api('/api/vault');
  } catch (e) {
    toast(e.message, true);
    S.vaultData = { exists: false, locked: true };
  }
  paintVault();
}

async function vaultUnlock() {
  const input = $('#vault-pin');
  const pin = input ? input.value : '';
  if (!pin) { if (input) input.focus(); return; }
  try {
    await api('/api/vault/unlock', { method: 'POST', body: JSON.stringify({ pin: pin }) });
    S.vaultPin = pin;
    S.vaultNeedPIN = false;
    await loadVault();
    await refreshState();
    toast('Vault unlocked');
  } catch (e) {
    toast(e.message, true);
    if (input) { input.value = ''; input.focus(); }
  }
}

async function vaultLock() {
  try {
    await api('/api/vault/lock', { method: 'POST' });
    S.vaultPin = '';
    S.vaultNeedPIN = false;
    S.vaultOpen = null;
    S.vaultShown = {};
    await loadVault();
  } catch (e) { toast(e.message, true); }
  await refreshState();
}

async function vaultPIN() {
  const input = $('.vpin');
  const pin = input ? input.value : '';
  if (!pin) { if (input) input.focus(); return; }
  try {
    await api('/api/vault/check', { method: 'POST', body: JSON.stringify({ pin: pin }) });
    S.vaultPin = pin;
    S.vaultNeedPIN = false;
    repaintVaultGroups();
  } catch (e) {
    toast(e.message, true);
    if (input) { input.value = ''; input.focus(); }
  }
}

function defaultVaultScope() {
  const ch = S.channelId ? findChannel(S.channelId) : null;
  return (ch && ch.vault_scope) || 'global';
}

function vaultKey(scope, name) {
  return scope + '/' + name;
}

// The only way a value reaches the page. The PIN is asked again because
// anything running as you can reach loopback.
async function vaultReveal(scope, name) {
  if (!S.vaultPin) {
    S.vaultNeedPIN = true;
    repaintVaultGroups();
    return null;
  }
  try {
    const r = await api('/api/vault/reveal', { method: 'POST', body: JSON.stringify({ scope: scope, name: name, pin: S.vaultPin }) });
    return r.value;
  } catch (e) {
    if (e.message === 'wrong PIN') {
      S.vaultPin = '';
      S.vaultNeedPIN = true;
      repaintVaultGroups();
      toast('Wrong PIN', true);
      return null;
    }
    toast(e.message, true);
    return null;
  }
}

async function vaultShow(scope, name) {
  const value = await vaultReveal(scope, name);
  if (value == null) return;
  S.vaultShown[vaultKey(scope, name)] = value;
  repaintVaultGroups();
}

async function vaultCopy(scope, name) {
  const value = await vaultReveal(scope, name);
  if (value == null) return;
  try {
    await navigator.clipboard.writeText(value);
    toast('Copied');
  } catch (e) {
    toast('Clipboard is not available', true);
  }
}

async function vaultNew(scope, initialName) {
  const scopes = vaultGroups().map((g) => g.scope);
  const current = scope || defaultVaultScope();
  if (!scopes.includes(current)) scopes.push(current);
  const v = await openModal({
    title: 'New secret',
    submit: 'Add',
    fields: [
      { name: 'name', label: 'Variable', value: initialName || '', placeholder: 'GH_TOKEN', note: 'A-Z, 0-9 and underscore. For a file (PEM, kubeconfig), name it @file:KUBECONFIG: the variable will point at a private temp file for each command.' },
      { name: 'value', label: 'Value', textarea: true, placeholder: 'secret value or file content' },
      { name: 'scope', label: 'Scope', value: current, options: scopes.map((s) => ({ value: s, label: s })) },
    ],
  });
  if (!v) return;
  const name = (v.name || '').trim();
  if (!name) { toast('The variable name is required', true); return; }
  try {
    await api('/api/vault/item', { method: 'POST', body: JSON.stringify({ scope: v.scope, name: name, value: v.value || '' }) });
    S.vaultOpen = v.scope + '/' + name;
    toast('Saved');
    await loadVault();
    // has_key comes from /api/state: without this the banner survives and the
    // composer keeps refusing to send.
    await refreshState();
  } catch (e) { toast(e.message, true); }
}

// countEnvKeys is a hint under the textarea, not the real parser: it counts
// KEY=value lines and block headers so an 80-key paste shows its size before
// submitting.
function countEnvKeys(text) {
  let n = 0;
  for (const line of String(text || '').split('\n')) {
    const s = line.trim();
    if (!s || s.startsWith('#')) continue;
    if (/^(?:export\s+)?(?:@file:)?[A-Za-z_][A-Za-z0-9_]*(?:\s*=|<<)/.test(s)) n++;
  }
  return n;
}

async function vaultImport(scope) {
  const scopes = vaultGroups().map((g) => g.scope);
  const current = scope || defaultVaultScope();
  if (!scopes.includes(current)) scopes.push(current);
  const v = await openModal({
    title: 'Import .env',
    submit: 'Import',
    wide: true,
    body: 'Paste a .env file. Keys already in the scope are overwritten; comments and keys the file does not mention stay.' +
      '<div class="import-count" id="import-count">Paste KEY=value lines</div>',
    fields: [
      { name: 'scope', label: 'Scope', value: current, options: scopes.map((s) => ({ value: s, label: s })) },
      { name: 'text', label: 'Content', textarea: true, mono: true, grow: true, focus: true },
    ],
    onMount: (dlg) => {
      const ta = dlg.querySelector('#f_text');
      const note = dlg.querySelector('#import-count');
      if (!ta || !note) return;
      const update = () => {
        const n = countEnvKeys(ta.value);
        note.textContent = n ? n + (n === 1 ? ' variable detected' : ' variables detected') : 'Paste KEY=value lines';
        note.classList.toggle('ok', n > 0);
      };
      ta.addEventListener('input', update);
      update();
    },
  });
  if (!v) return;
  const text = v.text || '';
  if (!text.trim()) { toast('Paste the .env content first', true); return; }
  try {
    const r = await api('/api/vault/import', { method: 'POST', body: JSON.stringify({ scope: v.scope, text: text }) });
    const n = r.imported || 0;
    toast('Imported ' + n + (n === 1 ? ' variable' : ' variables'));
    S.vaultOpen = null;
    await loadVault();
    await refreshState();
  } catch (e) { toast(e.message, true); }
}

async function vaultEdit(scope, name) {
  const scopes = vaultGroups().map((g) => g.scope);
  if (!scopes.includes(scope)) scopes.push(scope);
  const v = await openModal({
    title: 'Edit secret',
    submit: 'Save',
    body: 'The current value is not shown here. Leave the value empty to keep it or just rename the variable.',
    fields: [
      { name: 'name', label: 'Variable', value: name },
      { name: 'value', label: 'New value', textarea: true, placeholder: 'keep current value' },
      { name: 'scope', label: 'Scope', value: scope, options: scopes.map((s) => ({ value: s, label: s })) },
    ],
  });
  if (!v) return;
  const newName = (v.name || '').trim();
  if (!newName) { toast('The variable name is required', true); return; }
  try {
    await api('/api/vault/item', {
      method: 'POST',
      body: JSON.stringify({ scope: v.scope, name: newName, old: name, value: v.value || '' }),
    });
    S.vaultShown = {};
    S.vaultOpen = v.scope + '/' + newName;
    toast('Saved');
    await loadVault();
    await refreshState();
  } catch (e) { toast(e.message, true); }
}

async function vaultDelete(scope, name) {
  const yes = await confirmBox('Delete secret', 'Delete <strong>' + esc(name) + '</strong> from ' + esc(scope) + '? This cannot be undone.', 'Delete');
  if (!yes) return;
  try {
    await api('/api/vault/delete', { method: 'POST', body: JSON.stringify({ scope: scope, name: name }) });
    delete S.vaultShown[vaultKey(scope, name)];
    S.vaultOpen = null;
    toast('Deleted');
    await loadVault();
    await refreshState();
  } catch (e) { toast(e.message, true); }
}

async function createVault() {
  const hasKey = !!(S.state && S.state.config && S.state.config.has_key);
  const v = await openModal({
    title: 'Create vault',
    submit: 'Create',
    body: 'A PIN of at least 8 characters encrypts your secrets on disk. There is no recovery if you lose it.' +
      (hasKey ? ' Your DeepSeek API key will move from config.json into the vault.' : ''),
    fields: [
      { name: 'pin', label: 'PIN', type: 'password' },
      { name: 'confirm', label: 'Repeat PIN', type: 'password' },
    ],
  });
  if (!v) return;
  try {
    await api('/api/vault/init', { method: 'POST', body: JSON.stringify({ pin: v.pin, confirm: v.confirm, move_key: hasKey }) });
    S.vaultPin = v.pin || '';
    S.vaultNeedPIN = false;
    toast('Vault created');
    await refreshState();
    if (onVaultRoute()) await loadVault();
    // The key is the reason the vault exists: ask for it right away.
    if (!(S.state && S.state.config && S.state.config.has_key)) await vaultNew('global', 'DEEPSEEK_API_KEY');
  } catch (e) { toast(e.message, true); }
}

function syncVaultBanner() {
  if (onVaultRoute()) return;
  const v = S.state && S.state.vault;
  const need = !!(v && (!v.exists || v.locked));
  const existing = document.querySelector('#main .banner.vault');
  if (need && !existing) {
    const header = $('#main .topbar');
    if (header) header.insertAdjacentHTML('afterend', vaultBannerHTML());
  } else if (!need && existing) {
    existing.remove();
  }
}

async function unlockVault() {
  const input = $('#vault-pin');
  const pin = input ? input.value : '';
  if (!pin) { if (input) input.focus(); return; }
  try {
    await api('/api/vault/unlock', { method: 'POST', body: JSON.stringify({ pin: pin }) });
    S.vaultPin = pin;
    toast('Vault unlocked');
    await refreshState();
  } catch (e) {
    toast(e.message, true);
    if (input) { input.value = ''; input.focus(); }
  }
}

function renderHome() {
  const chans = (S.state && S.state.channels) || [];
  $('#main').innerHTML =
    '<header class="topbar"><button class="icon-btn menu-btn" data-action="drawer" aria-label="Channels" aria-expanded="false">' + ico('list') + '</button>' +
    '<div class="titles"><h1>ax</h1><p>a small self-hosted assistant</p></div>' +
    '<div class="actions"><button class="btn primary" data-action="new-channel">New channel</button></div></header>' +
    keyBannerHTML() + vaultBannerHTML() +
    '<div class="scroll"><div class="pad">' +
    (chans.length
      ? '<div class="thread-list">' + chans.map((c) =>
          '<a class="thread-item" href="#/c/' + c.id + '"><div class="ti-main">' +
          '<div class="ti-title"># ' + esc(c.name) + '</div>' +
          '<div class="ti-preview">' + (c.threads || []).length + ((c.threads || []).length === 1 ? ' thread' : ' threads') + '</div></div>' +
          '<div class="ti-meta"><span>' + (c.threads || []).length + ' threads</span></div></a>').join('') + '</div>'
      : emptyHTML('Nothing here yet', 'Create a channel to get started.')) +
    '</div></div>';
}

function threadItemHTML(t) {
  return '<a class="thread-item" href="#/c/' + esc(t.channel || S.channelId) + '/t/' + t.id + '">' +
    '<div class="ti-main"><div class="ti-title">' + esc(t.title) + '</div>' +
    '<div class="ti-preview">' + esc(t.preview || 'No messages yet') + '</div></div>' +
    '<div class="ti-meta">' + (t.running ? '<span class="pulse-dot"></span>' : '') +
    '<span>' + relTime(t.updated) + '</span></div></a>';
}

function renderChannelView() {
  const ch = findChannel(S.channelId);
  if (!ch) { renderHome(); return; }
  const threads = ch.threads || [];
  $('#main').innerHTML =
    '<header class="topbar">' +
    '<button class="icon-btn menu-btn" data-action="drawer" aria-label="Channels" aria-expanded="false">' + ico('list') + '</button>' +
    '<div class="titles" role="button" tabindex="0" aria-haspopup="menu" data-action="channel-menu" data-id="' + ch.id + '"><h1># ' + esc(ch.name) + '</h1><p>' + threads.length + (threads.length === 1 ? ' thread' : ' threads') + '</p></div>' +
    '<div class="actions">' +
    '<button class="btn primary" data-action="new-thread">New thread</button>' +
    '</div></header>' +
    keyBannerHTML() + vaultBannerHTML() +
    '<div class="scroll"><div class="pad">' +
    (threads.length
      ? '<div class="thread-list">' + threads.map(threadItemHTML).join('') + '</div>'
      : '<div class="empty"><strong>No threads yet</strong><span>Ask ax anything to start.</span>' +
        '<button class="btn primary" data-action="new-thread">Start chatting</button></div>') +
    '</div></div>';
}

function renderThreadShell() {
  $('#main').innerHTML =
    '<header class="topbar"><button class="icon-btn menu-btn" data-action="drawer" aria-label="Channels" aria-expanded="false">' + ico('list') + '</button>' +
    '<button class="icon-btn back" data-action="back" aria-label="Back">' + ico('arrow-left') + '</button>' +
    '<div class="titles"><h1 id="thread-title">Thread</h1><p id="thread-sub"></p></div>' +
    '<div class="actions"></div></header>' +
    '<div class="scroll" id="messages-scroll"><div class="messages" id="messages"></div></div>' +
    '<div class="composer-wrap">' + jumpLatestHTML() + '<div class="pending" id="pending"></div>' +
    '<div class="composer" id="composer" data-thread="">' +
    '<button class="icon-btn" data-action="attach" title="Attach image" aria-label="Attach image">' + ico('paperclip') + '</button>' +
    '<textarea id="input" rows="1" placeholder="Message ax…" autocomplete="off" autocapitalize="sentences" aria-label="Message"></textarea>' +
    '<button class="send" id="send" data-action="send" title="Send" aria-label="Send message">' + ico('paper-plane-tilt') + '</button>' +
    '<button class="stop" id="stop" data-action="stop" title="Stop" aria-label="Stop" hidden>' + ico('stop') + '</button>' +
    '</div><div class="hint">Enter to send · Shift+Enter for a new line · Ctrl+K to search</div></div>';
  wireComposer();
  renderPending();
  setComposerMode();
}

function jumpLatestHTML() {
  return '<button class="jump-latest" id="jump-latest" data-action="jump-latest" aria-label="Jump to latest messages" hidden>' +
    ico('arrow-down') + '<span>Latest</span><span class="jump-dot" aria-hidden="true"></span></button>';
}

function renderThreadView() {
  const th = S.thread;
  if (!th) return;
  const ch = findChannel(th.channel);
  // A run ending reloads the thread. Keep whatever the user already typed and
  // the keyboard open instead of replacing the composer under their fingers.
  const prevInput = (() => {
    const c = $('#composer');
    return c && c.dataset.thread === S.threadId ? $('#input') : null;
  })();
  const draft = prevInput ? prevInput.value : getDraft(S.threadId);
  const prevSecret = $('#secret-value');
  const secretDraft = prevSecret ? prevSecret.value : '';
  const hadFocus = !!prevInput && document.activeElement === prevInput;
  const selStart = prevInput ? prevInput.selectionStart : 0;
  const selEnd = prevInput ? prevInput.selectionEnd : 0;
  const prevScroll = $('#messages-scroll');
  const fromBottom = prevScroll ? prevScroll.scrollHeight - prevScroll.scrollTop - prevScroll.clientHeight : 0;
  $('#main').innerHTML =
    '<header class="topbar">' +
    '<button class="icon-btn back" data-action="back" aria-label="Back">' + ico('arrow-left') + '</button>' +
    '<div class="titles" role="button" tabindex="0" aria-haspopup="menu" data-action="thread-menu" data-id="' + th.id + '"><h1 id="thread-title">' + esc(th.title) + '</h1>' +
    '<p id="thread-sub">#' + esc(ch ? ch.name : '') + ctxLabel() + '</p></div>' +
    '</header>' + keyBannerHTML() + vaultBannerHTML() + secretBannerHTML() +
    '<div class="scroll" id="messages-scroll"><div class="messages" id="messages"></div></div>' +
    '<div class="composer-wrap">' + jumpLatestHTML() + '<div class="pending" id="pending"></div>' +
    '<div class="composer" id="composer" data-thread="' + esc(th.id) + '">' +
    '<button class="icon-btn" data-action="attach" title="Attach image" aria-label="Attach image">' + ico('paperclip') + '</button>' +
    '<textarea id="input" rows="1" placeholder="Message ax…" autocomplete="off" autocapitalize="sentences" aria-label="Message"></textarea>' +
    '<button class="send" id="send" data-action="send" title="Send" aria-label="Send message">' + ico('paper-plane-tilt') + '</button>' +
    '<button class="stop" id="stop" data-action="stop" title="Stop" aria-label="Stop" hidden>' + ico('stop') + '</button>' +
    '</div><div class="hint">Enter to send · Shift+Enter for a new line · Ctrl+K to search</div></div>';
  wireComposer();
  renderPending();
  renderMessages(false);
  renderWorking();
  setComposerMode();
  const input = $('#input');
  if (input && draft) { input.value = draft; autosize(input); }
  updateSendState();
  const sec = $('#secret-value');
  if (sec && secretDraft) sec.value = secretDraft;
  if (input && hadFocus) {
    input.focus();
    try { input.setSelectionRange(selStart, selEnd); } catch (e) {}
  } else if (!coarsePointer()) {
    setTimeout(() => { const i = $('#input'); if (i) i.focus(); }, 40);
  }
  if (S.stick) scrollToBottom(true);
  else {
    const sc = $('#messages-scroll');
    if (sc) sc.scrollTop = Math.max(0, sc.scrollHeight - fromBottom - sc.clientHeight);
  }
  updateJumpLatest();
}

function ctxLabel() {
  const cfg = (S.state && S.state.config) || {};
  let last = null;
  for (const m of S.messages) {
    if (m.t === 'assistant' && m.usage && m.usage.prompt_tokens) last = m.usage.prompt_tokens;
  }
  if (!last || !cfg.max_context) return '';
  return ' · ctx ' + Math.min(100, Math.round((last / cfg.max_context) * 100)) + '%';
}

function avatarLetter(name) {
  const n = String(name || '?').trim();
  return n ? n[0].toUpperCase() : '?';
}

function bubbleShell(kind, name, iso, bodyHTML, extraClass) {
  return '<div class="msg ' + kind + (extraClass ? ' ' + extraClass : '') + '">' +
    '<div class="avatar">' + esc(avatarLetter(name)) + '</div>' +
    '<div class="body-wrap"><div class="head"><span class="who">' + esc(name) + '</span>' +
    '<span class="time">' + fmtTime(iso) + '</span></div>' +
    '<div class="body">' + bodyHTML + '</div></div></div>';
}

function imagesHTML(ids) {
  if (!ids || !ids.length) return '';
  return '<div class="images">' + ids.map((id) =>
    '<a href="/api/blobs/' + esc(id) + '" target="_blank" rel="noopener">' +
    '<img src="/api/blobs/' + esc(id) + '" alt="image" loading="lazy"></a>').join('') + '</div>';
}

function messageHTML(m) {
  if (m.t === 'compaction') return '<div class="divider">context compacted</div>';
  if (m.t === 'resume') return '<div class="divider">run resumed after an interruption</div>';
  if (m.t === 'error') {
    return '<div class="msg error"><div class="avatar">!</div><div class="body-wrap">' +
      '<div class="head"><span class="who">error</span><span class="time">' + fmtTime(m.ts) + '</span></div>' +
      '<div class="body">' + esc(m.text) + '</div></div></div>';
  }
  const isUser = m.t === 'user';
  const name = isUser ? (m.author || (S.state ? S.state.user : 'you')) : 'ax';
  const body = (isUser ? '<p>' + esc(m.text).replace(/\n/g, '<br>') + '</p>' : md(m.text));
  return bubbleShell(isUser ? 'user' : 'assistant', name, m.ts, imagesHTML(m.images) + body);
}

function liveHTML(id, text) {
  return '<div class="msg assistant live" id="live-' + esc(id) + '">' +
    '<div class="avatar">p</div><div class="body-wrap">' +
    '<div class="head"><span class="who">ax</span><span class="time"></span></div>' +
    '<div class="body">' + md(text) + '<span class="caret"></span></div></div></div>';
}

function renderMessages(forceScroll) {
  const box = $('#messages');
  if (!box) return;
  const stick = forceScroll || S.stick;
  let html = S.messages.map(messageHTML).join('');
  for (const [id, l] of S.live) html += liveHTML(id, l.text);
  const empty = !html;
  if (empty) html = emptyThreadHTML();
  box.classList.toggle('is-empty', empty);
  box.innerHTML = html;
  const sub = $('#thread-sub');
  if (sub && S.thread) {
    const ch = findChannel(S.thread.channel);
    sub.textContent = '#' + (ch ? ch.name : '') + ctxLabel();
  }
  if (stick) scrollToBottom(true);
  updateJumpLatest();
}

function renderWorking() {
  const box = $('#messages');
  if (!box) return;
  let el = $('#working');
  if (S.running) {
    if (!el) {
      el = document.createElement('div');
      el.id = 'working';
      el.className = 'working';
      el.innerHTML = '<span class="pulse-dot"></span><span>ax is working…</span>';
      box.appendChild(el);
    }
  } else if (el) {
    el.remove();
  }
}

function renderPending() {
  const box = $('#pending');
  if (!box) return;
  const done = S.pending.map((p) =>
    '<div class="thumb"><img src="' + esc(p.url) + '" alt="">' +
    '<button class="x" data-remove-img="' + esc(p.id) + '" title="Remove">' + ico('x') + '</button></div>').join('');
  const busy = S.uploads.map((u) =>
    '<div class="thumb uploading" role="status" aria-label="Uploading image">' +
    '<img src="' + esc(u.url) + '" alt=""><span class="upload-spin" aria-hidden="true"></span></div>').join('');
  box.innerHTML = done + busy;
  updateSendState();
}

/* --------------------------------------------------------------- drafts */

const DRAFT_PREFIX = 'ax.draft.';
const draftTimers = {};

function getDraft(id) {
  if (!id) return '';
  if (Object.prototype.hasOwnProperty.call(S.drafts, id)) return S.drafts[id];
  try { return localStorage.getItem(DRAFT_PREFIX + id) || ''; } catch (e) { return ''; }
}

function rememberDraft(id, text) {
  if (!id) return;
  S.drafts[id] = text;
  clearTimeout(draftTimers[id]);
  draftTimers[id] = setTimeout(() => persistDraft(id), 250);
}

function persistDraft(id) {
  if (!id) return;
  const text = S.drafts[id] || '';
  try {
    if (text.trim()) localStorage.setItem(DRAFT_PREFIX + id, text);
    else localStorage.removeItem(DRAFT_PREFIX + id);
  } catch (e) {}
}

function clearDraft(id) {
  if (!id) return;
  delete S.drafts[id];
  clearTimeout(draftTimers[id]);
  delete draftTimers[id];
  try { localStorage.removeItem(DRAFT_PREFIX + id); } catch (e) {}
}

function hasComposerContent() {
  const ta = $('#input');
  return !!((ta && ta.value.trim()) || S.pending.length || S.uploads.length);
}

function updateSendState() {
  const send = $('#send');
  if (!send) return;
  // While a run is going the same button steers it: ax applies the message at
  // the next turn boundary, so the composer never goes mute mid-run.
  send.disabled = S.uploads.length > 0 || !hasComposerContent();
  send.title = S.uploads.length ? 'Uploading image…' : (S.running ? 'Steer this run' : 'Send message');
}

function setComposerMode() {
  const send = $('#send');
  const stop = $('#stop');
  if (!send || !stop) return;
  send.hidden = false;
  stop.hidden = !S.running;
  const input = $('#input');
  if (input) input.placeholder = S.running ? 'Steer this run…' : 'Message ax…';
  updateSendState();
}

function updateJumpLatest() {
  const b = $('#jump-latest');
  if (!b) return;
  b.hidden = !(!S.stick && (S.messages.length > 0 || S.live.size > 0));
  b.classList.toggle('running', S.running);
}

function scrollToBottom(force) {
  const el = $('#messages-scroll');
  if (!el) return;
  if (force || S.stick) el.scrollTop = el.scrollHeight;
}

function isNearBottom() {
  const el = $('#messages-scroll');
  if (!el) return true;
  return el.scrollHeight - el.scrollTop - el.clientHeight < 140;
}

/* --------------------------------------------------------------- markdown */

function inline(s) {
  s = s.replace(/`([^`\n]+)`/g, (m, c) => '<code>' + c + '</code>');
  s = s.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g,
    '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
  s = s.replace(/(^|[\s(])(https?:\/\/[^\s<)]+)/g,
    '$1<a href="$2" target="_blank" rel="noopener noreferrer">$2</a>');
  s = s.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
  s = s.replace(/~~([^~\n]+)~~/g, '<del>$1</del>');
  s = s.replace(/(^|[\s(])\*([^*\n]+)\*(?=[\s).,;:!?]|$)/g, '$1<em>$2</em>');
  s = s.replace(/(^|[\s(])_([^_\n]+)_(?=[\s).,;:!?]|$)/g, '$1<em>$2</em>');
  return s;
}

function fenceHTML(f) {
  const lang = (f.lang || 'text').toLowerCase();
  const code = esc(f.code.replace(/\n$/, ''));
  return '<div class="codeblock"><div class="codehead"><span>' + esc(lang) + '</span>' +
    '<button class="copy" data-copy="1">copy</button></div><pre><code>' + code + '</code></pre></div>';
}

function tableCells(line) {
  let t = line.trim().replace(/\\\|/g, '\u0001');
  if (t.startsWith('|')) t = t.slice(1);
  if (t.endsWith('|')) t = t.slice(0, -1);
  return t.split('|').map((c) => c.trim().replace(/\u0001/g, '|'));
}

function tableAlign(cell) {
  const left = cell.startsWith(':'), right = cell.endsWith(':');
  return left && right ? 'center' : right ? 'right' : left ? 'left' : '';
}

function tableHTML(head, aligns, rows) {
  const cell = (tag, text, i) => '<' + tag + (aligns[i] ? ' style="text-align:' + aligns[i] + '"' : '') + '>' +
    inline(text || '') + '</' + tag + '>';
  const thead = '<thead><tr>' + head.map((c, i) => cell('th', c, i)).join('') + '</tr></thead>';
  const tbody = rows.length
    ? '<tbody>' + rows.map((r) => '<tr>' + head.map((_, i) => cell('td', r[i] || '', i)).join('') + '</tr>').join('') + '</tbody>'
    : '';
  return '<div class="table-wrap"><table>' + thead + tbody + '</table></div>';
}

function md(src) {
  if (!src) return '';
  const fences = [];
  let s = String(src).replace(/```([^\n`]*)\n?([\s\S]*?)```/g, (m, lang, code) => {
    fences.push({ lang: lang, code: code });
    return '\n\u0000' + (fences.length - 1) + '\u0000\n';
  });
  s = esc(s);
  const out = [];
  let para = [];
  let list = null;
  const flushPara = () => {
    if (!para.length) return;
    out.push('<p>' + inline(para.join('\n')).replace(/\n/g, '<br>') + '</p>');
    para = [];
  };
  const flushList = () => {
    if (!list) return;
    out.push('<' + list.type + '>' + list.items.map((i) => '<li>' + inline(i) + '</li>').join('') + '</' + list.type + '>');
    list = null;
  };
  const lines = s.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const f = line.match(/^\u0000(\d+)\u0000$/);
    if (f) { flushPara(); flushList(); out.push(fenceHTML(fences[Number(f[1])])); continue; }
    if (!line.trim()) { flushPara(); flushList(); continue; }
    if (line.includes('|') && i + 1 < lines.length) {
      const head = tableCells(line);
      const delims = tableCells(lines[i + 1]);
      if (head.length >= 2 && delims.length === head.length && delims.every((c) => /^:?-{3,}:?$/.test(c))) {
        flushPara(); flushList();
        const aligns = delims.map(tableAlign);
        const rows = [];
        let j = i + 2;
        while (j < lines.length && lines[j].trim() && lines[j].includes('|')) {
          rows.push(tableCells(lines[j]));
          j++;
        }
        out.push(tableHTML(head, aligns, rows));
        i = j - 1;
        continue;
      }
    }
    const h = line.match(/^(#{1,4})\s+(.+)$/);
    if (h) {
      flushPara(); flushList();
      const lvl = Math.min(4, h[1].length + 1);
      out.push('<h' + lvl + '>' + inline(h[2]) + '</h' + lvl + '>');
      continue;
    }
    const q = line.match(/^&gt;\s?(.*)$/);
    if (q) { flushPara(); flushList(); out.push('<blockquote>' + inline(q[1]) + '</blockquote>'); continue; }
    const ul = line.match(/^\s*[-*•]\s+(.+)$/);
    const ol = line.match(/^\s*\d{1,3}[.)]\s+(.+)$/);
    if (ul || ol) {
      flushPara();
      const type = ul ? 'ul' : 'ol';
      if (!list || list.type !== type) { flushList(); list = { type: type, items: [] }; }
      list.items.push((ul || ol)[1]);
      continue;
    }
    flushList();
    para.push(line);
  }
  flushPara();
  flushList();
  return out.join('');
}

/* ------------------------------------------------------------- thread data */

async function loadThread(id) {
  try {
    const data = await api('/api/threads/' + encodeURIComponent(id));
    if (S.threadId !== id) return;
    S.thread = data.thread;
    S.messages = data.messages || [];
    S.running = !!data.running;
    if (S.running) NOTIFY.done.delete(id);
    S.live.clear();
    renderThreadView();
    renderSidebar();
  } catch (e) {
    toast(e.message, true);
    if (S.channelId) location.hash = '#/c/' + S.channelId;
  }
}

/* -------------------------------------------------------------------- SSE */

let connTimer = 0;

function setConnected(up) {
  S.connected = !!up;
  const el = $('#conn');
  if (!el) return;
  if (up) {
    clearTimeout(connTimer);
    connTimer = 0;
    el.hidden = true;
    return;
  }
  if (connTimer || !el.hidden) return;
  connTimer = setTimeout(() => {
    connTimer = 0;
    el.hidden = false;
  }, 1200);
}

function connectSSE() {
  if (S.es) S.es.close();
  const es = new EventSource('/api/stream');
  S.es = es;
  es.onopen = () => {
    const was = S.connected;
    setConnected(true);
    if (was === false || was === undefined) {
      refreshState().catch(() => {});
      if (S.threadId) loadThread(S.threadId);
    }
  };
  es.onerror = () => {
    setConnected(false);
  };
  es.onmessage = (e) => {
    let ev;
    try { ev = JSON.parse(e.data); } catch { return; }
    handleEvent(ev);
  };
}

function handleEvent(ev) {
  switch (ev.type) {
    case 'hello':
      if (ev.text && S.state && S.state.build && ev.text !== S.state.build) {
        refreshState().catch(() => {});
      }
      noteBuild(ev.text);
      break;
    case 'delta':
      if (ev.thread === S.threadId) onDelta(ev);
      break;
    case 'message':
      if (ev.thread === S.threadId) onMessage(ev);
      scheduleStateRefresh();
      break;
    case 'run':
      if (ev.thread === S.threadId) onRun(ev);
      else scheduleStateRefresh();
      trackRun(ev);
      break;
    case 'state':
      scheduleStateRefresh();
      break;
    case 'compact':
      if (ev.thread === S.threadId) loadThread(S.threadId);
      break;
    case 'secret':
      if (ev.thread !== S.threadId && ev.secret) toast('ax needs ' + ev.secret.name);
      refreshState().catch(() => {});
      break;
    case 'step':
      break;
  }
}

function onDelta(ev) {
  const cur = S.live.get(ev.id) || { text: '' };
  cur.text += ev.text;
  S.live.set(ev.id, cur);
  if (S.liveFrame) return;
  S.liveFrame = requestAnimationFrame(() => {
    S.liveFrame = 0;
    for (const [id, l] of S.live) {
      let node = document.getElementById('live-' + id);
      if (!node) {
        const box = $('#messages');
        if (!box) continue;
        const empty = box.querySelector('.thread-empty');
        if (empty) { empty.remove(); box.classList.remove('is-empty'); }
        const wrap = document.createElement('div');
        wrap.innerHTML = liveHTML(id, l.text);
        node = wrap.firstChild;
        box.appendChild(node);
        renderWorking();
      }
      const body = node.querySelector('.body');
      if (body) body.innerHTML = md(l.text) + '<span class="caret"></span>';
    }
    if (S.stick) scrollToBottom(true);
    updateJumpLatest();
  });
}

function onMessage(ev) {
  const m = ev.message;
  if (!m) return;
  if (S.messages.some((x) => x.id === m.id)) return;
  S.live.delete(m.id);
  const live = document.getElementById('live-' + m.id);
  if (live) live.remove();
  S.messages.push(m);
  renderMessages(false);
  renderWorking();
  updateJumpLatest();
}

function onRun(ev) {
  S.running = !!ev.running;
  renderWorking();
  setComposerMode();
  updateJumpLatest();
  if (!ev.running) {
    if (ev.error) toast(ev.error, true);
    const id = ev.thread;
    setTimeout(() => {
      if (S.threadId === id && !S.running) loadThread(id);
    }, 120);
    maybeReloadCode();
  }
}

/* --------------------------------------------------------------- composer */

function wireComposer() {
  const ta = $('#input');
  if (!ta) return;
  ta.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' || e.isComposing) return;
    // Phones: Enter is a newline and the send button sends. Desktop: Enter
    // sends and Shift+Enter breaks the line. Ctrl/Cmd+Enter always sends.
    const force = e.ctrlKey || e.metaKey;
    if (!force && (coarsePointer() || e.shiftKey)) return;
    e.preventDefault();
    sendMessage();
  });
  ta.addEventListener('input', () => {
    autosize(ta);
    updateSendState();
    if (S.threadId) rememberDraft(S.threadId, ta.value);
  });
  ta.addEventListener('focus', () => {
    // The Android keyboard shrinks the viewport; keep the last message visible.
    setTimeout(() => { if (S.stick) scrollToBottom(true); }, 120);
  });
  ta.addEventListener('paste', (e) => {
    const items = (e.clipboardData && e.clipboardData.items) || [];
    const files = [];
    for (const it of items) {
      if (it.kind === 'file') { const f = it.getAsFile(); if (f) files.push(f); }
    }
    if (files.length) { e.preventDefault(); uploadFiles(files); }
  });
  const comp = $('#composer');
  comp.addEventListener('dragover', (e) => { e.preventDefault(); comp.classList.add('drag'); });
  comp.addEventListener('dragleave', () => comp.classList.remove('drag'));
  comp.addEventListener('drop', (e) => {
    e.preventDefault();
    comp.classList.remove('drag');
    if (e.dataTransfer && e.dataTransfer.files.length) uploadFiles(e.dataTransfer.files);
  });
  autosize(ta);
  updateSendState();
}

function autosize(ta) {
  ta.style.height = 'auto';
  const cap = Math.max(96, Math.round(window.innerHeight * 0.38));
  ta.style.height = Math.min(ta.scrollHeight, cap) + 'px';
  // A one-row composer reads as a pill; as soon as it wraps it grows into the
  // normal rounded box, so a half-height radius would look wrong there.
  const cs = getComputedStyle(ta);
  const one = parseFloat(cs.lineHeight) + parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
  const single = Math.max(one, parseFloat(cs.minHeight) || 0);
  const comp = ta.closest('.composer');
  if (comp) comp.classList.toggle('single', ta.offsetHeight <= single + 1);
}

async function uploadFiles(files) {
  const jobs = [];
  for (const f of files) {
    if (!/^image\//.test(f.type || '')) continue;
    if (f.size > 32 * 1024 * 1024) { toast('image too large (max 32MB)', true); continue; }
    jobs.push({ file: f, tmp: { url: URL.createObjectURL(f) } });
  }
  if (!jobs.length) return;
  for (const j of jobs) S.uploads.push(j.tmp);
  renderPending();
  for (const job of jobs) {
    try {
      const res = await fetch('/api/blobs', { method: 'POST', body: job.file, headers: { 'Content-Type': job.file.type } });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || 'upload failed');
      S.pending.push({ id: data.id, url: data.url });
    } catch (e) {
      toast(e.message, true);
    } finally {
      S.uploads = S.uploads.filter((u) => u !== job.tmp);
      URL.revokeObjectURL(job.tmp.url);
      renderPending();
    }
  }
}

async function sendMessage() {
  const ta = $('#input');
  const threadId = S.threadId;
  if (!ta || !threadId) return;
  if (S.uploads.length) { toast('Wait for the image to finish uploading', true); return; }
  const text = ta.value.trim();
  if (!text && !S.pending.length) return;
  if (!S.state || !S.state.config.has_key) {
    toast('Add your DeepSeek API key first', true);
    openSettings();
    return;
  }
  const pending = S.pending.slice();
  const images = pending.map((p) => p.id);
  ta.value = '';
  autosize(ta);
  S.pending = [];
  renderPending();
  clearDraft(threadId);
  S.stick = true;
  scrollToBottom(true);
  updateJumpLatest();
  try {
    await api('/api/threads/' + threadId + '/messages', {
      method: 'POST',
      body: JSON.stringify({ text: text, images: images }),
    });
  } catch (e) {
    toast(e.message, true);
    if (S.threadId === threadId) {
      if (!ta.value.trim()) { ta.value = text; autosize(ta); }
      S.pending = pending;
      renderPending();
      rememberDraft(threadId, ta.value);
    } else {
      // The user moved on while the request was in flight; keep the text for
      // the thread it belonged to instead of dropping it.
      rememberDraft(threadId, text);
    }
    updateSendState();
  }
}

/* -------------------------------------------------------------------- menus */

let menuEl = null;
let menuCloser = null;
let menuReturnFocus = null;

function closeMenu(restoreFocus) {
  const hadFocus = !!(menuEl && menuEl.contains(document.activeElement));
  if (menuEl) { menuEl.remove(); menuEl = null; }
  if (menuCloser) { document.removeEventListener('click', menuCloser, true); menuCloser = null; }
  const target = menuReturnFocus;
  menuReturnFocus = null;
  if (restoreFocus !== false && hadFocus && target && target.isConnected) {
    try { target.focus({ preventScroll: true }); } catch (e) { target.focus(); }
  }
}

function openMenu(anchor, items) {
  closeMenu(false);
  const m = document.createElement('div');
  const sheet = window.matchMedia('(max-width: 640px)').matches;
  m.className = 'menu' + (sheet ? ' sheet' : '');
  m.setAttribute('role', 'menu');
  m.innerHTML = items.map((i, idx) =>
    i.hr ? '<hr role="separator">' : '<button role="menuitem" data-idx="' + idx + '" class="' + (i.danger ? 'danger' : '') + '">' + esc(i.label) + '</button>'
  ).join('');
  document.body.appendChild(m);
  menuReturnFocus = anchor;
  if (!sheet) {
    const r = anchor.getBoundingClientRect();
    const w = m.offsetWidth;
    const h = m.offsetHeight;
    let left = r.right - w;
    left = Math.max(8, Math.min(window.innerWidth - w - 8, left));
    let top = r.bottom + 6;
    if (top + h > window.innerHeight - 8) top = Math.max(8, r.top - h - 6);
    m.style.left = left + 'px';
    m.style.top = top + 'px';
  }
  const first = m.querySelector('button');
  if (first) first.focus({ preventScroll: true });
  m.addEventListener('keydown', (e) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    const buttons = Array.from(m.querySelectorAll('button'));
    if (!buttons.length) return;
    e.preventDefault();
    const cur = buttons.indexOf(document.activeElement);
    const next = e.key === 'ArrowDown'
      ? (cur + 1) % buttons.length
      : (cur - 1 + buttons.length) % buttons.length;
    buttons[next].focus();
  });
  m.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-idx]');
    if (!b) return;
    e.stopPropagation();
    const item = items[Number(b.dataset.idx)];
    closeMenu();
    if (item && item.run) item.run();
  });
  menuEl = m;
  menuCloser = (e) => { if (!m.contains(e.target)) closeMenu(false); };
  setTimeout(() => document.addEventListener('click', menuCloser, true), 0);
}

/* --------------------------------------------------------- quick switcher */

let swItems = [];
let swIndex = 0;
let swReturnFocus = null;
let swSuppressFocus = false;

function switcherEntries() {
  const chans = (S.state && S.state.channels) || [];
  const channels = chans.map((c) => ({
    kind: 'channel', id: c.id, channel: c.id,
    label: c.name,
    sub: 'channel',
    hay: (c.name + ' ' + (c.workdir || '') + ' channel').toLowerCase(),
  }));
  const threads = [];
  for (const c of chans) {
    for (const t of c.threads || []) {
      const label = t.title || 'Untitled thread';
      const sub = '#' + c.name + ' · ' + (t.preview || 'No messages yet');
      threads.push({
        kind: 'thread', id: t.id, channel: c.id,
        label: label, sub: sub,
        hay: (label + ' ' + sub + ' ' + c.name + ' thread').toLowerCase(),
        updated: t.updated || '',
      });
    }
  }
  threads.sort((a, b) => String(b.updated).localeCompare(String(a.updated)));
  return channels.concat(threads);
}

function renderSwitcher() {
  const input = $('#switcher-input');
  const box = $('#switcher-results');
  if (!input || !box) return;
  const q = input.value.trim().toLowerCase();
  swItems = switcherEntries().filter((e) => !q || e.hay.includes(q)).slice(0, 40);
  if (swIndex >= swItems.length) swIndex = Math.max(0, swItems.length - 1);
  if (!swItems.length) {
    box.innerHTML = '<div class="sw-empty">No matches</div>';
    input.removeAttribute('aria-activedescendant');
    return;
  }
  box.innerHTML = swItems.map((e, i) => {
    const active = i === swIndex;
    return '<button type="button" class="sw-item' + (active ? ' active' : '') + '" id="sw-opt-' + i + '" data-sw="' + i + '" role="option" aria-selected="' + active + '">' +
      '<span class="sw-kind" aria-hidden="true">' + (e.kind === 'channel' ? '#' : '·') + '</span>' +
      '<span class="sw-main"><span class="sw-label">' + esc(e.label) + '</span>' +
      '<span class="sw-sub">' + esc(e.sub) + '</span></span></button>';
  }).join('');
  input.setAttribute('aria-activedescendant', 'sw-opt-' + swIndex);
  const active = box.querySelector('.sw-item.active');
  if (active) active.scrollIntoView({ block: 'nearest' });
}

function openSwitcher() {
  const dlg = $('#switcher');
  if (!dlg) return;
  const modal = $('#modal');
  if (modal && modal.open) return;
  if (dlg.open) { $('#switcher-input').focus(); return; }
  swReturnFocus = document.activeElement;
  closeMenu(false);
  closeDrawer(true);
  dlg.showModal();
  const input = $('#switcher-input');
  input.value = '';
  swIndex = 0;
  swSuppressFocus = false;
  renderSwitcher();
  setTimeout(() => input.focus({ preventScroll: true }), 30);
}

function closeSwitcher() {
  const dlg = $('#switcher');
  if (dlg && dlg.open) dlg.close();
}

function sidebarVisible() {
  const sb = $('#sidebar');
  return !!sb && getComputedStyle(sb).visibility !== 'hidden';
}

function selectSwitcher(i) {
  const e = swItems[i];
  if (!e) return;
  swSuppressFocus = true;
  closeSwitcher();
  location.hash = e.kind === 'channel' ? '#/c/' + e.channel : '#/c/' + e.channel + '/t/' + e.id;
  closeDrawer(true);
}

function wireSwitcher() {
  const dlg = $('#switcher');
  if (!dlg) return;
  const input = $('#switcher-input');
  const results = $('#switcher-results');
  input.addEventListener('input', () => { swIndex = 0; renderSwitcher(); });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!swItems.length) return;
      swIndex = e.key === 'ArrowDown'
        ? (swIndex + 1) % swItems.length
        : (swIndex - 1 + swItems.length) % swItems.length;
      renderSwitcher();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      selectSwitcher(swIndex);
    }
  });
  results.addEventListener('click', (e) => {
    const b = e.target.closest('[data-sw]');
    if (b) selectSwitcher(Number(b.dataset.sw));
  });
  dlg.addEventListener('click', (e) => { if (e.target === dlg) closeSwitcher(); });
  dlg.addEventListener('close', () => {
    const target = swReturnFocus;
    swReturnFocus = null;
    if (swSuppressFocus) {
      swSuppressFocus = false;
      setTimeout(() => {
        const next = $('#input') || $('.menu-btn');
        if (next) next.focus({ preventScroll: true });
      }, 80);
      return;
    }
    const inSidebar = !!(target && target.closest && target.closest('#sidebar'));
    if (target && target !== document.body && target !== document.documentElement &&
        typeof target.focus === 'function' && target.isConnected &&
        (!inSidebar || sidebarVisible())) {
      try { target.focus({ preventScroll: true }); } catch (e) { target.focus(); }
      return;
    }
    const fallback = $('#input') || $('.menu-btn');
    if (fallback) fallback.focus({ preventScroll: true });
  });
}


function openLightbox(src) {
  const dlg = $('#lightbox');
  if (!dlg || !src) return;
  dlg.querySelector('img').src = src;
  dlg.showModal();
}

function openModal(opts) {
  const dlg = $('#modal');
  dlg.classList.remove('vault-dialog');
  dlg.classList.toggle('wide-dialog', !!opts.wide);
  const fields = opts.fields || [];
  let settled = false;
  dlg.innerHTML =
    '<form method="dialog">' +
    '<div class="modal-head">' + esc(opts.title || '') + '</div>' +
    '<div class="modal-body">' +
    (opts.body ? '<div style="font-size:13px;color:var(--muted);line-height:1.5">' + opts.body + '</div>' : '') +
    fields.map(fieldHTML).join('') +
    '</div>' +
    '<div class="modal-foot">' +
    '<button type="button" class="btn" data-cancel>Cancel</button>' +
    '<button type="submit" class="btn ' + (opts.danger ? 'danger' : 'primary') + '">' + esc(opts.submit || 'Save') + '</button>' +
    '</div></form>';
  return new Promise((resolve) => {
    const form = dlg.querySelector('form');
    const done = (v) => {
      if (settled) return;
      settled = true;
      if (dlg.open) dlg.close();
      dlg.innerHTML = ''; // never leave a PIN sitting in the DOM
      resolve(v);
    };
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      done(Object.fromEntries(new FormData(form).entries()));
    });
    dlg.querySelector('[data-cancel]').addEventListener('click', () => done(null));
    dlg.addEventListener('cancel', (e) => { e.preventDefault(); done(null); });
    dlg.showModal();
    if (opts.onMount) opts.onMount(dlg);
    dlg.querySelectorAll('[data-browse]').forEach((btn) => {
      btn.addEventListener('click', async () => {
        const input = dlg.querySelector('#' + btn.dataset.browse);
        const dir = await pickDir(input ? input.value.trim() : '');
        if (!dir) return;
        const target = dlg.querySelector('#' + btn.dataset.browse);
        if (target) {
          target.value = dir;
          target.focus();
        }
      });
    });
    const first = dlg.querySelector('[data-focus]') || dlg.querySelector('input, textarea, select');
    if (first) setTimeout(() => first.focus(), 30);
  });
}

/* A folder browser for the channel form. It talks to /api/dirs, so the UI only
   ever sees directory names. Rows open a folder; the footer picks the one on
   screen. */
function pickDir(start) {
  const dlg = $('#dirpicker');
  if (dlg.open) return Promise.resolve(null);
  return new Promise((resolve) => {
    let settled = false;
    const done = (v) => {
      if (settled) return;
      settled = true;
      dlg.oncancel = null;
      if (dlg.open) dlg.close();
      dlg.innerHTML = '';
      resolve(v);
    };
    const rowsHTML = (res) => {
      let html = '';
      if (res.parent) html += dirRowHTML(res.parent, '..', 'arrow-left');
      for (const d of res.dirs || []) html += dirRowHTML(d.path, d.name, 'caret-right');
      if (res.more) html += '<div class="picker-empty">Too many folders to list. Type a path above.</div>';
      return html || '<div class="picker-empty">No subfolders.</div>';
    };
    dlg.innerHTML =
      '<div class="modal-head">Choose a directory</div>' +
      '<form class="picker-path">' +
      '<input id="picker-input" type="text" placeholder="/home/me/code/project" autocomplete="off" autocorrect="off" autocapitalize="off" spellcheck="false" aria-label="Directory path">' +
      '<button type="submit" class="btn">Go</button>' +
      '</form>' +
      '<form class="picker-path picker-new" hidden>' +
      '<input id="picker-new-name" type="text" placeholder="New folder name" autocomplete="off" autocorrect="off" autocapitalize="off" spellcheck="false" aria-label="New folder name">' +
      '<button type="submit" class="btn primary">Create</button>' +
      '</form>' +
      '<div class="picker-list" id="picker-list"></div>' +
      '<div class="modal-foot">' +
      '<button type="button" class="btn" data-picker-new>New folder</button>' +
      '<button type="button" class="btn" data-picker-cancel>Cancel</button>' +
      '<button type="button" class="btn primary" data-picker-use disabled>Use this folder</button>' +
      '</div>';
    const pathInput = $('#picker-input', dlg);
    const list = $('#picker-list', dlg);
    const useBtn = dlg.querySelector('[data-picker-use]');
    const newForm = dlg.querySelector('.picker-new');
    const newInput = $('#picker-new-name', dlg);
    let current = '';
    const makeDir = async (target, pick) => {
      try {
        const res = await api('/api/dirs', { method: 'POST', body: JSON.stringify({ path: target }) });
        newInput.value = '';
        newForm.hidden = true;
        if (pick) { done(res.path); return true; }
        return await load(res.path);
      } catch (e) {
        toast(e.message, true);
        return false;
      }
    };
    const load = async (target) => {
      try {
        const res = await api('/api/dirs?path=' + encodeURIComponent(target || ''));
        current = res.path;
        pathInput.value = res.path;
        list.innerHTML = rowsHTML(res);
        list.scrollTop = 0;
        const base = res.path.split('/').filter(Boolean).pop();
        useBtn.disabled = false;
        useBtn.textContent = 'Use ' + (base || '/');
        return true;
      } catch (e) {
        // A typed path that does not exist yet is the usual way to start a new
        // project, so offer to create it right there instead of only failing.
        if (e.status === 404) {
          const typed = (target || '').trim();
          pathInput.value = typed;
          useBtn.disabled = true;
          list.innerHTML = '<div class="picker-empty">' + esc(e.message) +
            (typed ? '<div class="picker-create"><button type="button" class="btn" data-create="' +
              esc(typed) + '">Create ' + esc(typed) + '</button></div>' : '') + '</div>';
        } else {
          toast(e.message, true);
        }
        return false;
      }
    };
    list.addEventListener('click', (e) => {
      const mk = e.target.closest('[data-create]');
      if (mk) { makeDir(mk.dataset.create, true); return; }
      const row = e.target.closest('[data-path]');
      if (row) load(row.dataset.path);
    });
    dlg.querySelector('form').addEventListener('submit', (e) => {
      e.preventDefault();
      load(pathInput.value.trim());
    });
    dlg.querySelector('[data-picker-new]').addEventListener('click', () => {
      newForm.hidden = !newForm.hidden;
      if (!newForm.hidden) newInput.focus();
    });
    newForm.addEventListener('submit', (e) => {
      e.preventDefault();
      const name = newInput.value.trim();
      if (!name) { newInput.focus(); return; }
      // An absolute path creates exactly that folder; a plain name creates a
      // child of the folder on screen, which is what the footer button means.
      makeDir(name.startsWith('/') ? name : current.replace(/\/+$/, '') + '/' + name);
    });
    dlg.querySelector('[data-picker-cancel]').addEventListener('click', () => done(null));
    dlg.querySelector('[data-picker-use]').addEventListener('click', () => done(pathInput.value.trim() || null));
    dlg.oncancel = (e) => { e.preventDefault(); done(null); };
    dlg.showModal();
    load(start).then((ok) => { if (!ok && start) load(''); });
    if (!coarsePointer()) setTimeout(() => pathInput.focus(), 30);
  });
}

function dirRowHTML(path, label, icon) {
  return '<button type="button" class="picker-row" data-path="' + esc(path) + '">' +
    ico(icon) + '<span class="picker-name">' + esc(label) + '</span></button>';
}

function fieldHTML(f) {
  const id = 'f_' + f.name;
  let input;
  if (f.options) {
    input = '<select id="' + id + '" name="' + esc(f.name) + '">' + f.options.map((o) =>
      '<option value="' + esc(o.value) + '"' + (String(o.value) === String(f.value) ? ' selected' : '') + '>' +
      esc(o.label) + '</option>').join('') + '</select>';
  } else if (f.textarea) {
    input = '<textarea id="' + id + '" name="' + esc(f.name) + '"' +
      (f.mono ? ' class="mono" wrap="off" spellcheck="false" autocomplete="off" autocapitalize="off" autocorrect="off"' : '') +
      (f.focus ? ' data-focus' : '') +
      ' placeholder="' + esc(f.placeholder || '') + '">' + esc(f.value || '') + '</textarea>';
  } else {
    input = '<input id="' + id + '" name="' + esc(f.name) + '" type="' + esc(f.type || 'text') + '"' +
      (f.step ? ' step="' + esc(f.step) + '"' : '') +
      (f.min != null ? ' min="' + esc(f.min) + '"' : '') +
      (f.max != null ? ' max="' + esc(f.max) + '"' : '') +
      ' value="' + esc(f.value == null ? '' : f.value) + '" placeholder="' + esc(f.placeholder || '') + '" autocomplete="off">';
  }
  if (f.dir) {
    input = '<div class="input-row">' + input +
      '<button type="button" class="btn" data-browse="' + id + '">Browse</button></div>';
  }
  return '<div class="field' + (f.grow ? ' grow' : '') + '"><label for="' + id + '">' + esc(f.label) + '</label>' + input +
    (f.note ? '<div class="note">' + esc(f.note) + '</div>' : '') + '</div>';
}

function confirmBox(title, message, submit) {
  return openModal({ title: title, body: message, submit: submit || 'Delete', danger: true, fields: [] })
    .then((v) => v !== null);
}

/* ----------------------------------------------------------------- actions */

async function newChannel() {
  const v = await openModal({
    title: 'New channel',
    submit: 'Create',
    fields: [
      { name: 'name', label: 'Name', placeholder: 'my-project' },
      { name: 'workdir', label: 'Working directory', dir: true, placeholder: '/home/me/code/project', note: 'Where ax runs tools in this channel.' },
      { name: 'prompt', label: 'Channel instructions (optional)', textarea: true, placeholder: 'Extra instructions for ax in this channel…' },
    ],
  });
  if (!v || !v.name.trim()) return;
  try {
    const c = await api('/api/channels', {
      method: 'POST',
      body: JSON.stringify({ name: v.name.trim(), workdir: (v.workdir || '').trim(), prompt: (v.prompt || '').trim() }),
    });
    await refreshState();
    location.hash = '#/c/' + c.id;
  } catch (e) { toast(e.message, true); }
}

async function editChannel(id) {
  const ch = findChannel(id);
  if (!ch) return;
  const v = await openModal({
    title: 'Edit channel',
    fields: [
      { name: 'name', label: 'Name', value: ch.name },
      { name: 'workdir', label: 'Working directory', dir: true, value: ch.workdir },
      { name: 'prompt', label: 'Channel instructions', textarea: true, value: ch.prompt || '' },
    ],
  });
  if (!v) return;
  try {
    await api('/api/channels/' + id, {
      method: 'PATCH',
      body: JSON.stringify({ name: v.name.trim() || ch.name, workdir: (v.workdir || '').trim(), prompt: (v.prompt || '').trim() }),
    });
    await refreshState();
    toast('Channel updated');
  } catch (e) { toast(e.message, true); }
}

async function togglePin(id) {
  const ch = findChannel(id);
  if (!ch) return;
  try {
    await api('/api/channels/' + id, { method: 'PATCH', body: JSON.stringify({ pinned: !ch.pinned }) });
    await refreshState();
    toast((ch.pinned ? 'Unpinned #' : 'Pinned #') + ch.name);
  } catch (e) { toast(e.message, true); }
}

function channelMenuItems(id) {
  const ch = findChannel(id);
  if (!ch) return [];
  return [
    { label: ch.pinned ? 'Unpin channel' : 'Pin channel', run: () => togglePin(id) },
    { label: 'New thread', run: newThread },
    { label: 'Edit channel', run: () => editChannel(id) },
    { hr: true },
    { label: 'Delete channel', danger: true, run: () => deleteChannel(id) },
  ];
}

async function deleteChannel(id) {
  const ch = findChannel(id);
  if (!ch) return;
  const ok = await confirmBox('Delete #' + ch.name + '?', 'This deletes the channel and all of its threads. It cannot be undone.');
  if (!ok) return;
  try {
    await api('/api/channels/' + id, { method: 'DELETE' });
    for (const t of ch.threads || []) clearDraft(t.id);
    S.channelId = null;
    S.threadId = null;
    await refreshState();
    location.hash = '#/';
  } catch (e) { toast(e.message, true); }
}

async function newThread() {
  if (!S.channelId) return;
  try {
    const th = await api('/api/channels/' + S.channelId + '/threads', { method: 'POST', body: '{}' });
    await refreshState();
    location.hash = '#/c/' + S.channelId + '/t/' + th.id;
  } catch (e) { toast(e.message, true); }
}

async function renameThread(id) {
  const th = findThread(id) || S.thread;
  if (!th) return;
  const v = await openModal({ title: 'Rename thread', fields: [{ name: 'title', label: 'Title', value: th.title }] });
  if (!v || !v.title.trim()) return;
  try {
    await api('/api/threads/' + id, { method: 'PATCH', body: JSON.stringify({ title: v.title.trim() }) });
    await refreshState();
    if (S.thread) { S.thread.title = v.title.trim(); const h = $('#thread-title'); if (h) h.textContent = v.title.trim(); }
  } catch (e) { toast(e.message, true); }
}

async function deleteThread(id) {
  const th = findThread(id) || S.thread;
  const ok = await confirmBox('Delete thread?', esc(th ? th.title : 'This thread') + ' will be deleted permanently.');
  if (!ok) return;
  try {
    await api('/api/threads/' + id, { method: 'DELETE' });
    clearDraft(id);
    await refreshState();
    location.hash = '#/c/' + S.channelId;
  } catch (e) { toast(e.message, true); }
}

async function compactThread(id) {
  toast('Compacting context…');
  try {
    await api('/api/threads/' + id + '/compact', { method: 'POST' });
    toast('Context compacted');
    if (S.threadId === id) loadThread(id);
  } catch (e) { toast(e.message, true); }
}

async function stopRun() {
  if (!S.threadId) return;
  try { await api('/api/threads/' + S.threadId + '/stop', { method: 'POST' }); }
  catch (e) { toast(e.message, true); }
}

async function openSettings() {
  const cfg = (S.state && S.state.config) || {};
  const v = await openModal({
    title: 'Settings',
    submit: 'Save',
    body: 'The DeepSeek API key lives in the vault, not here.',
    fields: [
      { name: 'model', label: 'Model', value: cfg.model, options: [
        { value: 'deepseek-flash', label: 'deepseek-flash (1M ctx, vision)' },
        { value: 'deepseek-v4-pro', label: 'deepseek-v4-pro (stronger, slower)' },
      ] },
      { name: 'effort', label: 'Thinking effort', value: cfg.effort, options: [
        { value: 'none', label: 'none (fastest)' },
        { value: 'low', label: 'low' },
        { value: 'high', label: 'high' },
        { value: 'max', label: 'max (default)' },
      ] },
      { name: 'compact_at', label: 'Auto-compact at', value: cfg.compact_at, type: 'number', step: '0.05', min: '0.1', max: '0.95', note: 'Fraction of the context window. 0.5 = 50%.' },
      { name: 'max_context', label: 'Context window (tokens)', value: cfg.max_context, type: 'number', note: 'deepseek-flash supports 1,000,000.' },
    ],
  });
  if (!v) return;
  const payload = {
    model: v.model,
    effort: v.effort,
    compact_at: Number(v.compact_at),
    max_context: Number(v.max_context),
  };
  try {
    await api('/api/settings', { method: 'POST', body: JSON.stringify(payload) });
    await refreshState();
    toast('Settings saved');
  } catch (e) { toast(e.message, true); }
}

/* ------------------------------------------------------------------- drawer */
/* On phones the drawer is an overlay. Opening it pushes a history entry so the
   Android back gesture closes the drawer instead of leaving the app. */

let drawerOnHistory = false;

function drawerOpen() { return document.body.classList.contains('drawer'); }

function openDrawer() {
  if (drawerOpen() || window.matchMedia('(min-width: 900px)').matches) return;
  document.body.classList.add('drawer');
  document.querySelectorAll('[data-action="drawer"]').forEach((b) => b.setAttribute('aria-expanded', 'true'));
  try {
    history.pushState({ axDrawer: true }, '');
    drawerOnHistory = true;
  } catch (e) { /* history can be unavailable in some webviews */ }
}

function closeDrawer(fromHistory) {
  if (!drawerOpen()) return;
  document.body.classList.remove('drawer');
  document.querySelectorAll('[data-action="drawer"]').forEach((b) => b.setAttribute('aria-expanded', 'false'));
  if (!drawerOnHistory) return;
  drawerOnHistory = false;
  // Consume our own entry so back keeps working after the drawer closes.
  if (!fromHistory) {
    try { history.back(); } catch (e) {}
  }
}

function wireDrawerGestures() {
  let start = null;
  document.addEventListener('touchstart', (e) => {
    if (!drawerOpen() || e.touches.length !== 1) return;
    const sb = $('#sidebar');
    if (!sb) return;
    const r = sb.getBoundingClientRect();
    const t = e.touches[0];
    if (t.clientX > r.right) return;
    start = { x: t.clientX, y: t.clientY };
  }, { passive: true });
  document.addEventListener('touchmove', (e) => {
    if (!start) return;
    const t = e.touches[0];
    const dx = t.clientX - start.x;
    const dy = t.clientY - start.y;
    // A vertical drag is the channel list scrolling, not a drawer gesture.
    if (Math.abs(dy) > 14 && Math.abs(dy) > Math.abs(dx)) start = null;
  }, { passive: true });
  document.addEventListener('touchend', (e) => {
    if (!start) return;
    const t = e.changedTouches[0];
    const dx = t.clientX - start.x;
    const dy = t.clientY - start.y;
    start = null;
    if (dx < -55 && Math.abs(dx) > Math.abs(dy) * 1.3) closeDrawer();
  }, { passive: true });
}

/* Right-click on a sidebar channel, or a long press on Android (which fires
   contextmenu), opens the same menu as its title. */
let channelMenuAt = 0;

function wireChannelMenus() {
  const nav = $('#channels');
  if (!nav) return;
  nav.addEventListener('contextmenu', (e) => {
    const a = e.target.closest('a.channel');
    if (!a) return;
    e.preventDefault();
    channelMenuAt = Date.now();
    openMenu(a, channelMenuItems(a.dataset.id));
  });
}

/* ------------------------------------------------------------- global wiring */

function wireViewport() {
  // Pin the shell to the *visible* viewport instead of trusting 100dvh: after
  // an auto reload with the keyboard still up, dvh can stay at the full screen
  // height and the composer is drawn below the fold. Pinch-zoom keeps 100dvh.
  const app = $('#app');
  const vv = window.visualViewport;
  const syncSize = () => {
    if (!app || !vv) return;
    if (!vv.height || (vv.scale && vv.scale !== 1)) {
      app.classList.remove('vv');
      return;
    }
    app.style.setProperty('--app-h', vv.height + 'px');
    app.classList.add('vv');
  };
  if (vv) {
    syncSize();
    let timer = 0;
    vv.addEventListener('resize', () => {
      syncSize();
      clearTimeout(timer);
      timer = setTimeout(() => {
        closeMenu(false);
        if (S.stick) scrollToBottom(true);
        // A reload deferred because the keyboard was open can run now.
        maybeReloadCode();
      }, 100);
    });
    vv.addEventListener('scroll', syncSize);
  }
  const desktop = window.matchMedia('(min-width: 900px)');
  const onDesktop = (e) => { if (e.matches) closeDrawer(true); };
  if (desktop.addEventListener) desktop.addEventListener('change', onDesktop);
  else if (desktop.addListener) desktop.addListener(onDesktop);
  window.addEventListener('orientationchange', () => {
    setTimeout(() => { closeMenu(false); if (S.stick) scrollToBottom(true); }, 250);
  });
}

function wireEvents() {
  window.addEventListener('hashchange', route);
  window.addEventListener('popstate', () => { if (drawerOpen()) closeDrawer(true); });
  window.addEventListener('pagehide', () => {
    for (const id of Object.keys(S.drafts)) persistDraft(id);
  });
  wireDrawerGestures();
  wireSwitcher();
  wireChannelMenus();

  document.addEventListener('scroll', (e) => {
    if (e.target && e.target.id === 'messages-scroll') {
      S.stick = isNearBottom();
      updateJumpLatest();
    }
  }, true);

  window.addEventListener('resize', () => closeMenu(false));

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { closeMenu(); closeDrawer(); }
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      if (e.shiftKey) {
        closeMenu(false);
        closeDrawer(true);
        const ta = $('#input');
        if (ta) ta.focus();
      } else {
        openSwitcher();
      }
    }
    // Header titles act as buttons but are not native <button> elements.
    const ae = document.activeElement;
    if ((e.key === 'Enter' || e.key === ' ') && ae && ae.dataset && ae.dataset.action &&
        ae.getAttribute('role') === 'button') {
      e.preventDefault();
      ae.click();
    }
  });

  const lb = $('#lightbox');
  if (lb) {
    lb.addEventListener('click', () => lb.close());
    lb.addEventListener('close', () => lb.querySelector('img').removeAttribute('src'));
  }

  document.addEventListener('click', (e) => {
    // A long press that opened the channel menu must not also open the channel.
    if (channelMenuAt && Date.now() - channelMenuAt < 700 && e.target.closest('#channels a.channel')) {
      e.preventDefault();
      return;
    }

    const imgLink = e.target.closest('.msg .images a');
    if (imgLink) {
      e.preventDefault();
      openLightbox(imgLink.getAttribute('href'));
      return;
    }

    const copy = e.target.closest('[data-copy]');
    if (copy) {
      const block = copy.closest('.codeblock');
      const code = block && block.querySelector('code');
      if (code) {
        navigator.clipboard.writeText(code.innerText).then(() => {
          copy.textContent = 'copied';
          setTimeout(() => { copy.textContent = 'copy'; }, 1200);
        }).catch(() => {});
      }
      return;
    }

    const rm = e.target.closest('[data-remove-img]');
    if (rm) {
      S.pending = S.pending.filter((p) => p.id !== rm.dataset.removeImg);
      renderPending();
      return;
    }

    const a = e.target.closest('[data-action]');
    if (!a) return;
    const action = a.dataset.action;
    switch (action) {
      case 'drawer': e.preventDefault(); openDrawer(); break;
      case 'close-drawer': closeDrawer(); break;
      case 'back': location.hash = S.channelId ? '#/c/' + S.channelId : '#/'; break;
      case 'settings': openSettings(); break;
      case 'add-key': vaultNew('global', 'DEEPSEEK_API_KEY'); break;
      case 'search': openSwitcher(); break;
      case 'vault': location.hash = '#/vault'; break;
      case 'vault-new': vaultNew(a.dataset.scope || ''); break;
      case 'vault-import': vaultImport(a.dataset.scope || ''); break;
      case 'vault-lock': vaultLock(); break;
      case 'vault-unlock': vaultUnlock(); break;
      case 'vault-pin-ok': vaultPIN(); break;
      case 'vault-show': { const row = a.closest('.vi'); if (row) vaultShow(row.dataset.scope, row.dataset.name); break; }
      case 'vault-copy': { const row = a.closest('.vi'); if (row) vaultCopy(row.dataset.scope, row.dataset.name); break; }
      case 'vault-edit': { const row = a.closest('.vi'); if (row) vaultEdit(row.dataset.scope, row.dataset.name); break; }
      case 'vault-del': { const row = a.closest('.vi'); if (row) vaultDelete(row.dataset.scope, row.dataset.name); break; }
      case 'vault-item': {
        const row = a.closest('.vi');
        if (!row) break;
        const key = row.dataset.scope + '/' + row.dataset.name;
        S.vaultOpen = S.vaultOpen === key ? null : key;
        repaintVaultGroups();
        break;
      }
      case 'reload': location.reload(); break;
      case 'notify': toggleNotify(); break;
      case 'new-channel': newChannel(); break;
      case 'new-thread': newThread(); break;
      case 'send': sendMessage(); break;
      case 'jump-latest': S.stick = true; scrollToBottom(true); updateJumpLatest(); break;
      case 'stop': stopRun(); break;
      case 'attach': $('#file-input').click(); break;
      case 'vault-unlock': unlockVault(); break;
      case 'vault-create': createVault(); break;
      case 'secret-save': saveSecret(); break;
      case 'secret-skip': skipSecret(); break;
      case 'channel-menu':
        openMenu(a, channelMenuItems(a.dataset.id));
        break;
      case 'thread-menu':
        openMenu(a, [
          { label: 'Rename thread', run: () => renameThread(a.dataset.id) },
          { label: 'Compact context', run: () => compactThread(a.dataset.id) },
          { label: 'Copy link', run: () => {
              const url = location.origin + '/#/c/' + S.channelId + '/t/' + a.dataset.id;
              navigator.clipboard.writeText(url).then(() => toast('Link copied')).catch(() => {});
            } },
          { hr: true },
          { label: 'Delete thread', danger: true, run: () => deleteThread(a.dataset.id) },
        ]);
        break;
    }
  });

  document.addEventListener('keydown', (e) => {
    if (e.target && e.target.id === 'vault-pin' && e.key === 'Enter') {
      e.preventDefault();
      unlockVault();
    }
    if (e.target && e.target.id === 'secret-value' && e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      saveSecret();
    }
  });

  $('#file-input').addEventListener('change', (e) => {
    const files = Array.from(e.target.files || []);
    e.target.value = '';
    if (files.length) uploadFiles(files);
  });
}

/* ---------------------------------------------------------- notifications */

/* Local notifications only: they fire while the app is alive (foreground or
   backgrounded tab/PWA). No push server, no VAPID, nothing to wake a closed
   app. Permission is requested from an explicit click, never on load. */
const NOTIFY_PREF = 'ax.notify';
const NOTIFY_LONG_RUN = 10000;
const NOTIFY = { started: new Map(), done: new Set() };

function notifySupported() {
  return typeof window !== 'undefined' && 'Notification' in window;
}

function notifyPermission() {
  return notifySupported() ? (Notification.permission || 'default') : 'unsupported';
}

function notifyPref() {
  try { return localStorage.getItem(NOTIFY_PREF) === 'on'; } catch (e) { return false; }
}

function setNotifyPref(on) {
  try { localStorage.setItem(NOTIFY_PREF, on ? 'on' : 'off'); } catch (e) {}
}

function notifyActive() {
  return notifyPref() && notifyPermission() === 'granted';
}

function syncNotifyButton() {
  const btn = $('#notify-btn');
  if (!btn) return;
  if (!notifySupported()) { btn.hidden = true; return; }
  btn.hidden = false;
  const perm = notifyPermission();
  const on = notifyActive();
  btn.classList.toggle('on', on);
  const label = $('#notify-state');
  if (label) label.textContent = perm === 'denied' ? 'blocked' : (on ? 'on' : 'off');
  btn.title = on ? 'Notifications on (click to turn off)' : 'Notify me when a run finishes';
}

async function toggleNotify() {
  if (!notifySupported()) { toast('this browser cannot show notifications'); return; }
  if (notifyPermission() === 'denied') {
    // Never ask again once blocked; the browser would ignore it anyway.
    toast('notifications are blocked for ax. Allow them in the site settings (Android: app info → Notifications), then reload.', true);
    return;
  }
  if (notifyActive()) {
    setNotifyPref(false);
    syncNotifyButton();
    toast('notifications off');
    return;
  }
  let perm = notifyPermission();
  if (perm === 'default') {
    // Called straight from the click handler so the user gesture is preserved.
    try { perm = await Notification.requestPermission(); } catch (e) { perm = 'denied'; }
  }
  if (perm !== 'granted') {
    setNotifyPref(false);
    syncNotifyButton();
    toast('notifications stay off: allow them in the site settings to turn them on', true);
    return;
  }
  setNotifyPref(true);
  syncNotifyButton();
  toast('notifications on');
}

function clipText(s, n) {
  const t = String(s == null ? '' : s).replace(/\s+/g, ' ').trim();
  if (t.length <= n) return t;
  return t.slice(0, n).replace(/[\s,.;:!?–—-]+$/, '') + '…';
}

function lastAssistantText(msgs) {
  for (let i = (msgs || []).length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (m && m.t === 'assistant' && m.text && m.text.trim()) return m.text;
  }
  return '';
}

// running:true starts a run; a repeated running:false for the same run is
// dropped so a thread never notifies twice.
function trackRun(ev) {
  const id = ev.thread;
  if (!id) return;
  if (ev.running) {
    if (!NOTIFY.started.has(id)) NOTIFY.started.set(id, Date.now());
    NOTIFY.done.delete(id);
    return;
  }
  const started = NOTIFY.started.get(id);
  NOTIFY.started.delete(id);
  if (NOTIFY.done.has(id)) return;
  NOTIFY.done.add(id);
  if (!notifyActive()) return;
  const took = started ? Date.now() - started : 0;
  if (document.hidden || S.threadId !== id || !document.hasFocus() || took >= NOTIFY_LONG_RUN) {
    notifyRunDone(id, ev.error || '').catch(() => {});
  }
}

async function notifyRunDone(threadId, err) {
  const known = findThread(threadId);
  let title = known ? known.title : '';
  let channel = known ? known.channelId : '';
  let text = '';
  try {
    const data = await api('/api/threads/' + encodeURIComponent(threadId));
    if (data && data.thread) {
      title = data.thread.title || title;
      channel = data.thread.channel || channel;
    }
    text = lastAssistantText((data && data.messages) || []);
  } catch (e) {
    // offline or deleted thread: fall back to what this page already has
  }
  if (!text && S.threadId === threadId) {
    for (const [, live] of S.live) if (live.text && live.text.trim()) text = live.text;
    if (!text) text = lastAssistantText(S.messages);
  }
  const body = err ? 'falló: ' + clipText(err, 110) : (clipText(text, 120) || 'terminó');
  await showRunNotification(title || 'ax', body, threadId, channel);
}

async function showRunNotification(title, body, threadId, channel) {
  const opts = {
    body: body,
    tag: threadId || 'ax-run',
    renotify: true,
    icon: '/static/icon-192.png',
    badge: '/static/icon-192.png',
    data: { thread: threadId || '', channel: channel || '' },
  };
  try {
    if ('serviceWorker' in navigator) {
      const reg = await navigator.serviceWorker.getRegistration();
      if (reg && typeof reg.showNotification === 'function') {
        await reg.showNotification(title, opts);
        return true;
      }
    }
  } catch (e) {
    // fall through: some browsers only allow the page-level constructor
  }
  try {
    const n = new Notification(title, opts);
    n.onclick = () => {
      try { window.focus(); } catch (e) {}
      openFromNotification(opts.data);
      n.close();
    };
    return true;
  } catch (e) {
    return false; // Android Chrome has no page-level constructor
  }
}

async function openFromNotification(data) {
  const threadId = (data && data.thread) || '';
  let channel = (data && data.channel) || '';
  if (!threadId) {
    if (channel) location.hash = '#/c/' + channel;
    return;
  }
  const known = findThread(threadId);
  if (known) channel = known.channelId;
  if (!channel) {
    try { await refreshState(); } catch (e) {}
    const t = findThread(threadId);
    if (t) channel = t.channelId;
  }
  if (!channel) return;
  const hash = '#/c/' + channel + '/t/' + threadId;
  if (location.hash === hash) route();
  else location.hash = hash;
}

/* --------------------------------------------------------------------- PWA */

function registerSW() {
  if (!('serviceWorker' in navigator)) return;
  navigator.serviceWorker.addEventListener('message', (e) => {
    const d = e.data;
    if (!d || d.type !== 'ax-open') return;
    try { window.focus(); } catch (err) {}
    openFromNotification(d);
  });
  const ok = location.protocol === 'https:' || location.hostname === 'localhost' || location.hostname === '127.0.0.1';
  if (!ok) return;
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}
