import { get, post } from '../api.js';
import { h, clear, toast, badge, fmtDur, promptDialog } from '../ui.js';
import { state } from '../app.js';

export async function renderDashboard(page) {
  const stats = h('div');
  const table = h('div');
  const canCreate = state.me.is_admin || state.me.global['servers.manage'];

  page.append(
    h('div', { class: 'page-title spread row' },
      h('span', {}, 'Servers'),
      canCreate ? h('button', { class: 'btn-acc', onclick: createServer }, '+ New server') : null,
    ),
    stats,
    table,
  );

  async function load() {
    const servers = await get('/api/servers');
    clear(stats);
    if (servers.length) {
      const running = servers.filter((s) => s.state !== 'stopped');
      const players = servers.reduce((n, s) => n + (s.players_now || 0), 0);
      const mem = running.reduce((n, s) => n + (s.rss_mb || 0), 0);
      stats.append(h('div', { class: 'statbar', style: 'margin-bottom:12px' },
        h('span', {}, h('b', {}, servers.length), servers.length === 1 ? ' server' : ' servers'),
        h('span', {}, h('b', {}, running.length), ' running'),
        h('span', {}, h('b', {}, players), players === 1 ? ' player online' : ' players online'),
        running.length ? h('span', {}, h('b', {}, mem >= 1024 ? (mem / 1024).toFixed(1) + ' GB' : mem.toFixed(0) + ' MB'), ' memory') : null,
      ));
    }
    clear(table);
    if (!servers.length) {
      table.append(h('div', { class: 'card empty' },
        canCreate ? 'No servers yet. Create one to get started.' : 'No servers are shared with your account.'));
      return;
    }
    table.append(h('div', { class: 'card', style: 'padding:0' },
      h('table', { class: 'tbl' },
        h('thead', {}, h('tr', {},
          h('th', {}, 'Server'), h('th', {}, 'State'), h('th', { class: 'num' }, 'Players'),
          h('th', { class: 'num' }, 'CPU'),
          h('th', { class: 'num' }, 'Memory'), h('th', { class: 'num' }, 'Uptime'),
          h('th', { class: 'num' }, 'Port'), h('th', {}, ''))),
        h('tbody', {}, servers.map(row)),
      )));
  }

  function row(s) {
    const p = s.perms || {};
    const actions = h('td', { class: 'num' },
      s.state === 'stopped' && p.start
        ? h('button', { class: 'btn-sm', onclick: (e) => act(e, s.id, 'start') }, 'Start') : null,
      s.state !== 'stopped' && p.stop
        ? h('button', { class: 'btn-sm', onclick: (e) => act(e, s.id, 'stop') }, 'Stop') : null,
    );
    return h('tr', { class: 'click', onclick: () => { location.hash = `#/server/${s.id}/console`; } },
      h('td', {}, h('span', { class: 'dot ' + s.state }), s.name,
        s.motd ? h('div', { class: 'small muted' }, s.motd.split('\n')[0].slice(0, 60)) : null),
      h('td', {}, badge(s.state)),
      h('td', { class: 'num' }, s.players_max ? `${s.players_now}/${s.players_max}` : '—'),
      h('td', { class: 'num' }, s.state === 'stopped' ? '—' : s.cpu_percent.toFixed(0) + '%'),
      h('td', { class: 'num' }, s.state === 'stopped' ? '—' : s.rss_mb.toFixed(0) + ' MB'),
      h('td', { class: 'num' }, fmtDur(s.uptime_sec)),
      h('td', { class: 'num' }, s.port),
      actions,
    );
  }

  async function act(e, id, verb) {
    e.stopPropagation();
    try {
      await post(`/api/servers/${id}/${verb}`);
      toast(verb + ' requested');
      setTimeout(load, 800);
    } catch (ex) { toast(ex.message, 'error'); }
  }

  async function createServer() {
    const name = await promptDialog('New server', 'Server name (e.g. "survival")');
    if (!name) return;
    try {
      const s = await post('/api/servers', { name });
      toast('Server created');
      location.hash = `#/server/${s.id}/settings`;
    } catch (ex) { toast(ex.message, 'error'); }
  }

  await load();
  const iv = setInterval(() => load().catch(() => { /* transient; retry next tick */ }), 5000);
  state.cleanup = () => clearInterval(iv);
}
