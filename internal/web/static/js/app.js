// App shell: session bootstrap, hash router, sidebar layout.

import { get, post, setCsrf } from './api.js';
import { h, clear, toast, icon } from './ui.js';
import { renderLogin, renderSetup } from './views/login.js';
import { renderDashboard } from './views/dashboard.js';
import { renderServer } from './views/server.js';
import { renderAdmin } from './views/admin.js';
import { renderAccount } from './views/account.js';

export const state = {
  me: null,          // /api/me payload
  cleanup: null,     // view teardown fn (SSE close etc.)
};

const app = document.getElementById('app');

async function loadMe() {
  const me = await get('/api/me');
  setCsrf(me.csrf);
  state.me = me;
  return me;
}

export async function refreshMe() { return loadMe(); }

export async function logout() {
  try { await post('/api/auth/logout'); } catch { /* ignore */ }
  state.me = null;
  location.hash = '#/login';
}

function route() {
  const hash = location.hash.replace(/^#\/?/, '');
  const parts = hash.split('/').filter(Boolean);
  return { parts, hash };
}

function navLink(href, label, active, ic) {
  return h('a', { href, class: active ? 'active' : '' }, ic ? icon(ic) : null, label);
}

function themeToggleBtn() {
  const isLight = () => document.documentElement.getAttribute('data-theme') === 'light';
  const btn = h('button', {
    class: 'icon-btn', title: 'Toggle light/dark', 'aria-label': 'Toggle light/dark theme',
    onclick: () => {
      window.BPTheme.set('theme', isLight() ? 'dark' : 'light');
      clear(btn);
      btn.append(icon(isLight() ? 'moon' : 'sun'));
    },
  }, icon(isLight() ? 'moon' : 'sun'));
  return btn;
}

function brand() {
  return h('div', { class: 'brand' }, h('span', { class: 'cube' }, icon('cube', 18)), 'BlockPanel');
}

function layout(contentEl, activePath) {
  const me = state.me;
  const g = me.global || {};
  const adminLinks = [];
  if (g['users.manage']) adminLinks.push(navLink('#/admin/users', 'Users', activePath === 'admin/users', 'users'));
  if (g['roles.manage']) adminLinks.push(navLink('#/admin/roles', 'Roles', activePath === 'admin/roles', 'shield'));
  if (me.is_admin) {
    adminLinks.push(navLink('#/admin/ai', 'AI Settings', activePath === 'admin/ai', 'spark'));
    adminLinks.push(navLink('#/admin/settings', 'Panel Settings', activePath === 'admin/settings', 'sliders'));
  }
  if (g['apikeys.manage']) adminLinks.push(navLink('#/admin/apikeys', 'API Keys', activePath === 'admin/apikeys', 'key'));
  if (g['audit.view']) adminLinks.push(navLink('#/admin/audit', 'Audit Log', activePath === 'admin/audit', 'list'));

  const sidebar = h('div', { class: 'sidebar' },
    brand(),
    h('div', { class: 'nav' },
      navLink('#/', 'Servers', activePath === 'home' || activePath.startsWith('server'), 'cube'),
      adminLinks.length ? h('div', { class: 'nav-label' }, 'Administration') : null,
      ...adminLinks,
      h('div', { class: 'nav-label' }, 'You'),
      navLink('#/account', 'Account', activePath === 'account', 'user'),
    ),
    h('div', { class: 'sidebar-foot' },
      h('div', { class: 'who' },
        h('span', { class: 'u' }, me.username),
        h('span', { class: 'r' }, me.is_admin ? 'administrator' : 'user'),
      ),
      themeToggleBtn(),
      h('button', { class: 'icon-btn', onclick: logout, title: 'Sign out', 'aria-label': 'Sign out' }, icon('logout')),
    ),
  );

  // Mobile: sidebar becomes a drawer behind a hamburger topbar.
  const scrim = h('div', { class: 'scrim', style: 'display:none', onclick: closeNav });
  function openNav() { sidebar.classList.add('open'); scrim.style.cssText = ''; }
  function closeNav() { sidebar.classList.remove('open'); scrim.style.cssText = 'display:none'; }
  sidebar.addEventListener('click', (e) => { if (e.target.closest('a')) closeNav(); });
  const topbar = h('div', { class: 'topbar' },
    h('button', { class: 'icon-btn', 'aria-label': 'Open menu', onclick: openNav }, icon('menu', 20)),
    brand(),
  );

  clear(app);
  app.append(topbar, scrim, sidebar, h('div', { class: 'main' }, contentEl));
}

async function render() {
  // tear down previous view (SSE streams etc.)
  if (state.cleanup) { try { state.cleanup(); } catch { /* ignore */ } state.cleanup = null; }

  const { parts } = route();
  const page = h('div');

  // unauthenticated routes
  if (parts[0] === 'login') { clear(app); app.append(renderLogin()); return; }
  if (parts[0] === 'setup') {
    const st = await get('/api/setup/status');
    if (!st.needed) { location.hash = '#/login'; return; }
    clear(app); app.append(renderSetup()); return;
  }

  if (!state.me) {
    try {
      const st = await get('/api/setup/status');
      if (st.needed) { location.hash = '#/setup'; return; }
      await loadMe();
    } catch {
      location.hash = '#/login';
      return;
    }
  }

  // force password change before anything else
  if (state.me.must_change_pw && parts[0] !== 'account') {
    location.hash = '#/account';
    return;
  }

  try {
    if (parts.length === 0) {
      layout(page, 'home');
      await renderDashboard(page);
    } else if (parts[0] === 'server' && parts[1]) {
      layout(page, 'server');
      await renderServer(page, parts[1], parts[2] || 'console');
    } else if (parts[0] === 'admin' && parts[1]) {
      layout(page, 'admin/' + parts[1]);
      await renderAdmin(page, parts[1]);
    } else if (parts[0] === 'account') {
      layout(page, 'account');
      await renderAccount(page);
    } else {
      location.hash = '#/';
    }
  } catch (e) {
    if (e.status === 401) { location.hash = '#/login'; return; }
    toast(e.message, 'error');
    page.append(h('div', { class: 'card' }, 'Failed to load: ' + e.message));
  }
}

window.addEventListener('hashchange', render);
window.addEventListener('DOMContentLoaded', render);
render();
