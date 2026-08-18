// Server tabs added in the feature expansion: Players, Properties,
// Schedules, plus the resource graph used on the Console tab.

import { get, post, put, patch, del } from '../api.js';
import { h, clear, toast, fmtBytes, fmtDur, fmtTime, confirmDialog, promptDialog, modal } from '../ui.js';

// ---------- resource graph (inline SVG, no chart library) ----------

export function resourceGraph(samples, { key, label, color, unit = '', max = null }) {
  const w = 600, hgt = 90, pad = 4;
  if (!samples.length) {
    return h('div', { class: 'graph-empty' }, 'No data yet — start the server to collect metrics.');
  }
  const vals = samples.map((s) => s[key] ?? 0);
  const peak = max ?? Math.max(1, ...vals);
  const step = samples.length > 1 ? (w - pad * 2) / (samples.length - 1) : 0;
  const pts = vals.map((v, i) => {
    const x = pad + i * step;
    const y = hgt - pad - (v / peak) * (hgt - pad * 2);
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  });
  const area = `M${pad},${hgt - pad} L${pts.join(' L')} L${(pad + (vals.length - 1) * step).toFixed(1)},${hgt - pad} Z`;

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', `0 0 ${w} ${hgt}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('class', 'graph-svg');
  const mk = (tag, attrs) => {
    const el = document.createElementNS('http://www.w3.org/2000/svg', tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    return el;
  };
  svg.append(mk('path', { d: area, fill: color, 'fill-opacity': '0.15' }));
  svg.append(mk('polyline', { points: pts.join(' '), fill: 'none', stroke: color, 'stroke-width': '1.5' }));

  const latest = vals[vals.length - 1];
  return h('div', { class: 'graph' },
    h('div', { class: 'graph-head' },
      h('span', {}, label),
      h('b', {}, `${latest.toFixed(latest < 10 ? 1 : 0)}${unit}`),
      h('span', { class: 'muted small' }, `peak ${peak.toFixed(peak < 10 ? 1 : 0)}${unit}`)),
    svg);
}

export async function renderGraphs(container, id) {
  let samples = [];
  try { samples = await get(`/api/servers/${id}/history?points=180`); }
  catch { return; }
  clear(container);
  container.append(
    resourceGraph(samples, { key: 'cpu', label: 'CPU', color: 'var(--accent)', unit: '%' }),
    resourceGraph(samples, { key: 'rss', label: 'Memory', color: 'var(--info)', unit: ' MB' }),
    resourceGraph(samples, { key: 'players', label: 'Players', color: 'var(--warn)' }),
  );
}

// ---------- Players ----------

export async function renderPlayers(body, id, perms) {
  const wrap = h('div');
  body.append(wrap);

  async function load() {
    let data;
    try { data = await get(`/api/servers/${id}/players`); }
    catch (e) { toast(e.message, 'error'); return; }
    clear(wrap);

    const canManage = perms['players.manage'];

    const actionBtn = (label, action, player, danger) => h('button', {
      class: 'btn-sm' + (danger ? ' btn-danger' : ''),
      onclick: async () => {
        let reason = '';
        if (action === 'ban' || action === 'kick') {
          reason = await promptDialog(`${action === 'ban' ? 'Ban' : 'Kick'} ${player}`, 'Reason (optional)') ?? '';
        }
        try {
          await post(`/api/servers/${id}/players/action`, { action, player, reason });
          toast(`${action.replace('_', ' ')}: ${player}`);
          setTimeout(load, 600);
        } catch (e) { toast(e.message, 'error'); }
      },
    }, label);

    // Online players
    const online = data.online || [];
    wrap.append(h('div', { class: 'card' },
      h('h3', {}, `Online players (${online.length})`),
      online.length
        ? h('table', { class: 'tbl' },
            h('thead', {}, h('tr', {}, h('th', {}, 'Player'), h('th', {}, 'Online for'), h('th', {}, ''))),
            h('tbody', {}, online.map((p) => h('tr', {},
              h('td', {}, p.name),
              h('td', { class: 'small muted' }, fmtDur(p.since_sec)),
              h('td', { class: 'num' }, canManage ? [
                actionBtn('Kick', 'kick', p.name), ' ',
                actionBtn('Ban', 'ban', p.name, true), ' ',
                actionBtn('Op', 'op', p.name),
              ] : null)))))
        : h('div', { class: 'empty' }, 'Nobody online.')));

    // The three list files
    const listCard = (title, entries, kind) => {
      const addBtn = canManage ? h('button', {
        class: 'btn-sm btn-acc',
        onclick: async () => {
          const name = await promptDialog(`Add to ${title.toLowerCase()}`, 'Player name');
          if (!name) return;
          const action = kind === 'whitelist' ? 'whitelist_add' : kind === 'ops' ? 'op' : 'ban';
          try {
            await post(`/api/servers/${id}/players/action`, { action, player: name });
            toast('Done'); setTimeout(load, 600);
          } catch (e) { toast(e.message, 'error'); }
        },
      }, '+ Add') : null;

      const removeAction = kind === 'whitelist' ? 'whitelist_remove' : kind === 'ops' ? 'deop' : 'pardon';
      return h('div', { class: 'card' },
        h('div', { class: 'row spread' }, h('h3', { class: 'mb0' }, `${title} (${entries.length})`), addBtn),
        entries.length
          ? h('table', { class: 'tbl', style: 'margin-top:10px' },
              h('tbody', {}, entries.map((e) => h('tr', {},
                h('td', {}, e.name || '(unnamed)'),
                h('td', { class: 'small muted mono' }, (e.uuid || '').slice(0, 8)),
                kind === 'bans' ? h('td', { class: 'small muted' }, e.reason || '') : null,
                h('td', { class: 'num' }, canManage
                  ? actionBtn('Remove', removeAction, e.name, true) : null)))))
          : h('div', { class: 'empty small' }, 'Empty.'));
    };

    wrap.append(
      listCard('Whitelist', data.whitelist || [], 'whitelist'),
      listCard('Operators', data.ops || [], 'ops'),
      listCard('Banned players', data.bans || [], 'bans'),
    );
    if (canManage) {
      wrap.append(h('div', { class: 'hint' },
        'Adding entries runs the matching server command, so the server must be running. Removals also work while it is stopped.'));
    }
  }
  await load();
}

// ---------- server.properties ----------

// Keys worth surfacing with friendly labels and input types; anything else
// still shows up as a plain text field.
const PROP_META = {
  'motd': { label: 'MOTD (server description)' },
  'max-players': { label: 'Max players', type: 'number' },
  'difficulty': { label: 'Difficulty', options: ['peaceful', 'easy', 'normal', 'hard'] },
  'gamemode': { label: 'Default gamemode', options: ['survival', 'creative', 'adventure', 'spectator'] },
  'pvp': { label: 'PvP enabled', type: 'bool' },
  'online-mode': { label: 'Online mode (Mojang auth)', type: 'bool' },
  'white-list': { label: 'Whitelist enabled', type: 'bool' },
  'enforce-whitelist': { label: 'Enforce whitelist', type: 'bool' },
  'server-port': { label: 'Server port', type: 'number' },
  'view-distance': { label: 'View distance', type: 'number' },
  'simulation-distance': { label: 'Simulation distance', type: 'number' },
  'spawn-protection': { label: 'Spawn protection radius', type: 'number' },
  'level-name': { label: 'World folder name' },
  'level-seed': { label: 'World seed' },
  'level-type': { label: 'Level type' },
  'hardcore': { label: 'Hardcore', type: 'bool' },
  'allow-flight': { label: 'Allow flight', type: 'bool' },
  'allow-nether': { label: 'Allow Nether', type: 'bool' },
  'spawn-monsters': { label: 'Spawn monsters', type: 'bool' },
  'spawn-animals': { label: 'Spawn animals', type: 'bool' },
  'spawn-npcs': { label: 'Spawn villagers', type: 'bool' },
  'enable-command-block': { label: 'Enable command blocks', type: 'bool' },
  'max-world-size': { label: 'Max world size', type: 'number' },
  'enable-rcon': { label: 'Enable RCON', type: 'bool' },
  'require-resource-pack': { label: 'Require resource pack', type: 'bool' },
};

export async function renderProperties(body, id) {
  let props;
  try { props = await get(`/api/servers/${id}/properties`); }
  catch (e) { body.append(h('div', { class: 'card empty' }, e.message)); return; }

  if (!props.length) {
    body.append(h('div', { class: 'card empty' },
      'No server.properties yet — start the server once to generate it.'));
    return;
  }

  const inputs = {};
  const search = h('input', { type: 'text', placeholder: 'Filter properties…', style: 'max-width:260px' });
  const grid = h('div', { class: 'form-grid' });

  function field(p) {
    const meta = PROP_META[p.key] || {};
    let input;
    if (meta.type === 'bool') {
      input = h('select', {},
        h('option', { value: 'true', selected: p.value === 'true' || null }, 'true'),
        h('option', { value: 'false', selected: p.value !== 'true' || null }, 'false'));
    } else if (meta.options) {
      input = h('select', {}, meta.options.map((o) =>
        h('option', { value: o, selected: p.value === o || null }, o)));
    } else {
      input = h('input', { type: meta.type === 'number' ? 'number' : 'text', value: p.value });
    }
    inputs[p.key] = { el: input, original: p.value };
    return h('label', { class: 'f', dataset: { key: p.key } },
      h('span', {}, meta.label || p.key),
      input,
      meta.label ? h('span', { class: 'muted', style: 'font-size:11px' }, p.key) : null);
  }

  function draw(filter = '') {
    clear(grid);
    const f = filter.toLowerCase();
    for (const p of props) {
      const meta = PROP_META[p.key] || {};
      const hay = (p.key + ' ' + (meta.label || '')).toLowerCase();
      if (f && !hay.includes(f)) continue;
      grid.append(field(p));
    }
  }
  draw();
  search.addEventListener('input', () => draw(search.value));

  body.append(h('div', { class: 'card' },
    h('div', { class: 'row spread', style: 'margin-bottom:12px' },
      h('h3', { class: 'mb0' }, 'server.properties'),
      search),
    h('div', { class: 'hint' }, 'Changes take effect the next time the server starts. Comments and unknown keys in the file are preserved.'),
    grid,
    h('div', { class: 'form-actions' },
      h('button', {
        class: 'btn-acc',
        onclick: async () => {
          const updates = {};
          for (const [k, v] of Object.entries(inputs)) {
            if (v.el.value !== v.original) updates[k] = v.el.value;
          }
          if (!Object.keys(updates).length) { toast('No changes'); return; }
          try {
            await put(`/api/servers/${id}/properties`, { updates });
            toast(`Saved ${Object.keys(updates).length} propert${Object.keys(updates).length === 1 ? 'y' : 'ies'}`);
            for (const k of Object.keys(updates)) inputs[k].original = inputs[k].el.value;
          } catch (e) { toast(e.message, 'error'); }
        },
      }, 'Save changes'))));
}

// ---------- Schedules ----------

const ACTIONS = [
  ['restart', 'Restart server'],
  ['start', 'Start server'],
  ['stop', 'Stop server'],
  ['backup', 'Create backup'],
  ['command', 'Run console command'],
];
const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

export async function renderSchedules(body, id) {
  const list = h('div');
  body.append(
    h('div', { class: 'row', style: 'margin-bottom:10px' },
      h('button', { class: 'btn-acc', onclick: () => form(null) }, '+ New schedule'),
      h('span', { class: 'muted small' }, 'Automate restarts, backups and commands.')),
    list);

  async function load() {
    let items;
    try { items = await get(`/api/servers/${id}/schedules`); }
    catch (e) { toast(e.message, 'error'); return; }
    clear(list);
    if (!items.length) {
      list.append(h('div', { class: 'card empty' }, 'No schedules yet.'));
      return;
    }
    list.append(h('div', { class: 'card', style: 'padding:0' },
      h('table', { class: 'tbl' },
        h('thead', {}, h('tr', {},
          h('th', {}, 'Name'), h('th', {}, 'Action'), h('th', {}, 'When'),
          h('th', {}, 'Last run'), h('th', {}, 'Enabled'), h('th', {}, ''))),
        h('tbody', {}, items.map((sc) => h('tr', {},
          h('td', {}, sc.name),
          h('td', { class: 'small' }, sc.action === 'command' ? `command: ${sc.command}` : sc.action),
          h('td', { class: 'small muted' }, describe(sc)),
          h('td', { class: 'small muted' },
            sc.last_run && !sc.last_run.startsWith('0001')
              ? [h('span', { class: sc.last_ok ? '' : 'err-text' }, sc.last_ok ? '✓ ' : '✗ '),
                 fmtTime(sc.last_run), sc.last_note ? h('div', { class: 'small muted' }, sc.last_note) : null]
              : '—'),
          h('td', {}, sc.enabled ? 'yes' : 'no'),
          h('td', { class: 'num' },
            h('button', {
              class: 'btn-sm', onclick: async () => {
                try { await post(`/api/servers/${id}/schedules/${sc.id}/run`); toast('Running now…'); setTimeout(load, 2500); }
                catch (e) { toast(e.message, 'error'); }
              },
            }, 'Run now'), ' ',
            h('button', { class: 'btn-sm', onclick: () => form(sc) }, 'Edit'), ' ',
            h('button', {
              class: 'btn-sm btn-danger', onclick: async () => {
                if (!await confirmDialog(`Delete schedule "${sc.name}"?`, { danger: true, okLabel: 'Delete' })) return;
                try { await del(`/api/servers/${id}/schedules/${sc.id}`); load(); }
                catch (e) { toast(e.message, 'error'); }
              },
            }, 'Delete'))))))));
  }

  function describe(sc) {
    if (sc.mode === 'interval') return `every ${sc.interval_min} min`;
    const days = (sc.weekdays && sc.weekdays.length)
      ? sc.weekdays.map((d) => DAYS[d]).join(', ') : 'every day';
    return `${sc.time_of_day} — ${days}`;
  }

  function form(existing) {
    const name = h('input', { type: 'text', value: existing?.name || '' });
    const action = h('select', {}, ACTIONS.map(([v, l]) =>
      h('option', { value: v, selected: existing?.action === v || null }, l)));
    const command = h('input', { type: 'text', value: existing?.command || '', placeholder: 'say Restarting in 5 minutes' });
    const mode = h('select', {},
      h('option', { value: 'interval', selected: existing?.mode === 'interval' || null }, 'Every N minutes'),
      h('option', { value: 'daily', selected: existing?.mode === 'daily' || null }, 'Daily at a set time'));
    const interval = h('input', { type: 'number', value: existing?.interval_min || 60, min: '1' });
    const timeOfDay = h('input', { type: 'text', value: existing?.time_of_day || '04:00', placeholder: 'HH:MM' });
    const enabled = h('input', { type: 'checkbox', checked: (existing ? existing.enabled : true) || null });
    const dayChecks = DAYS.map((d, i) =>
      h('input', { type: 'checkbox', checked: existing?.weekdays?.includes(i) || null }));

    const cmdRow = h('label', { class: 'f' }, h('span', {}, 'Console command'), command);
    const intervalRow = h('label', { class: 'f' }, h('span', {}, 'Interval (minutes)'), interval);
    const dailyRow = h('div', {},
      h('label', { class: 'f' }, h('span', {}, 'Time of day (24h, server local time)'), timeOfDay),
      h('div', { class: 'row' }, DAYS.map((d, i) =>
        h('label', { class: 'chk', style: 'margin:0 8px 0 0' }, dayChecks[i], d))),
      h('div', { class: 'hint' }, 'No days selected = every day.'));

    function sync() {
      cmdRow.style.display = action.value === 'command' ? '' : 'none';
      intervalRow.style.display = mode.value === 'interval' ? '' : 'none';
      dailyRow.style.display = mode.value === 'daily' ? '' : 'none';
    }
    action.addEventListener('change', sync);
    mode.addEventListener('change', sync);

    const content = h('div', {},
      h('div', { class: 'form-grid' },
        h('label', { class: 'f' }, h('span', {}, 'Name'), name),
        h('label', { class: 'f' }, h('span', {}, 'Action'), action),
        h('label', { class: 'f' }, h('span', {}, 'Schedule type'), mode)),
      cmdRow, intervalRow, dailyRow,
      h('label', { class: 'chk' }, enabled, 'Enabled'),
      h('div', { class: 'form-actions' },
        h('button', { onclick: () => close() }, 'Cancel'),
        h('button', {
          class: 'btn-acc',
          onclick: async () => {
            const payload = {
              name: name.value, action: action.value, command: command.value,
              mode: mode.value, interval_min: +interval.value,
              time_of_day: timeOfDay.value,
              weekdays: dayChecks.map((c, i) => c.checked ? i : -1).filter((i) => i >= 0),
              enabled: enabled.checked,
            };
            try {
              if (existing) await patch(`/api/servers/${id}/schedules/${existing.id}`, payload);
              else await post(`/api/servers/${id}/schedules`, payload);
              toast('Saved'); close(); load();
            } catch (e) { toast(e.message, 'error'); }
          },
        }, 'Save')));

    const close = modal(existing ? `Edit schedule: ${existing.name}` : 'New schedule', content);
    sync();
  }

  await load();
}
