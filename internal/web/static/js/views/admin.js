import { get, post, patch, put, del } from '../api.js';
import { h, clear, toast, fmtTime, confirmDialog, modal, promptDialog } from '../ui.js';
import { state } from '../app.js';

export async function renderAdmin(page, section) {
  switch (section) {
    case 'users': return usersPage(page);
    case 'roles': return rolesPage(page);
    case 'ai': return aiPage(page);
    case 'settings': return settingsPage(page);
    case 'audit': return auditPage(page);
    case 'apikeys': return apiKeysPage(page);
    default: location.hash = '#/';
  }
}

// ---------- API keys ----------

async function apiKeysPage(page) {
  const [keys, users] = await Promise.all([
    get('/api/apikeys'),
    state.me.is_admin ? get('/api/users').catch(() => []) : Promise.resolve([]),
  ]);
  const table = h('div');

  page.append(
    h('div', { class: 'page-title spread row' },
      h('span', {}, 'API Keys'),
      h('button', { class: 'btn-acc', onclick: createKey }, '+ New key')),
    h('div', { class: 'card hint', style: 'margin-bottom:16px' },
      'API keys let scripts and monitoring call the panel without a browser session. Send the key as an ',
      h('code', {}, 'X-API-Key'), ' header. A key can never do more than the user it belongs to, and read-only keys are restricted to GET requests.'),
    table);

  render(keys);

  function render(list) {
    clear(table);
    if (!list.length) { table.append(h('div', { class: 'card empty' }, 'No API keys yet.')); return; }
    table.append(h('div', { class: 'card', style: 'padding:0' },
      h('table', { class: 'tbl' },
        h('thead', {}, h('tr', {},
          h('th', {}, 'Name'), h('th', {}, 'Owner'), h('th', {}, 'Key'),
          h('th', {}, 'Mode'), h('th', { class: 'num' }, 'Last used'), h('th', {}, ''))),
        h('tbody', {}, list.map((k) => h('tr', {},
          h('td', {}, k.name),
          h('td', {}, k.username),
          h('td', { class: 'mono small muted' }, k.prefix + '…'),
          h('td', {}, k.read_only ? 'read-only' : 'full'),
          h('td', { class: 'num small muted' }, fmtTime(k.last_used)),
          h('td', { class: 'num' }, h('button', {
            class: 'btn-sm btn-danger', onclick: async () => {
              if (!await confirmDialog(`Delete API key "${k.name}"? Anything using it stops working immediately.`, { danger: true, okLabel: 'Delete' })) return;
              try { render(await del(`/api/apikeys/${k.id}`)); toast('Key deleted'); }
              catch (e) { toast(e.message, 'error'); }
            },
          }, 'Delete'))))))));
  }

  function createKey() {
    const name = h('input', { type: 'text', placeholder: 'e.g. uptime monitor' });
    const readOnly = h('input', { type: 'checkbox', checked: true });
    const owner = h('select', {}, h('option', { value: '' }, `${state.me.username} (you)`),
      users.filter((u) => u.id !== state.me.id).map((u) => h('option', { value: u.id }, u.username)));
    const content = h('div', {},
      h('label', { class: 'f' }, h('span', {}, 'Name'), name),
      state.me.is_admin && users.length
        ? h('label', { class: 'f' }, h('span', {}, 'Acts as user'), owner) : null,
      h('label', { class: 'chk' }, readOnly, 'Read-only (GET requests only — recommended for monitoring)'),
      h('div', { class: 'form-actions' },
        h('button', { onclick: () => close() }, 'Cancel'),
        h('button', {
          class: 'btn-acc', onclick: async () => {
            try {
              const res = await post('/api/apikeys', {
                name: name.value, read_only: readOnly.checked,
                user_id: owner.value || undefined,
              });
              close();
              render(res.key);
              showSecret(res.secret);
            } catch (e) { toast(e.message, 'error'); }
          },
        }, 'Create key')));
    const close = modal('New API key', content);
  }

  function showSecret(secret) {
    modal('Copy your API key now', h('div', {},
      h('p', { class: 'mt0' }, 'This is the only time the key is shown. Store it somewhere safe.'),
      h('div', { class: 'secret' }, secret),
      h('p', { class: 'small muted' }, 'Use it as a header: ', h('code', {}, 'X-API-Key: ' + secret.slice(0, 12) + '…')),
      h('div', { class: 'form-actions' },
        h('button', {
          class: 'btn-acc', onclick: (e) => {
            navigator.clipboard?.writeText(secret).then(
              () => toast('Copied to clipboard'),
              () => toast('Select the key and copy it manually', 'error'));
          },
        }, 'Copy'))));
  }
}

// ---------- shared: permission editors ----------

// Tri-state select: inherit (unset) / allow / deny — for user overrides.
function triSelect(current) {
  const sel = h('select', {},
    h('option', { value: 'inherit' }, 'inherit'),
    h('option', { value: 'allow' }, 'allow'),
    h('option', { value: 'deny' }, 'deny'));
  sel.value = current === true ? 'allow' : current === false ? 'deny' : 'inherit';
  return sel;
}
function triValue(sel) {
  return sel.value === 'inherit' ? undefined : sel.value === 'allow';
}

// Editor for a map[perm] -> tri-state; returns {el, read()}
function triGrid(keys, existing) {
  const sels = {};
  const el = h('div', { class: 'perm-grid' },
    keys.map((k) => {
      sels[k] = triSelect(existing ? existing[k] : undefined);
      return h('div', { class: 'tri' }, sels[k], h('span', {}, k));
    }));
  return {
    el,
    read() {
      const out = {};
      for (const [k, sel] of Object.entries(sels)) {
        const v = triValue(sel);
        if (v !== undefined) out[k] = v;
      }
      return out;
    },
  };
}

// Checkbox grid for roles (perm present=true, absent=deny).
function checkGrid(keys, existing) {
  const checks = {};
  const el = h('div', { class: 'perm-grid' },
    keys.map((k) => {
      checks[k] = h('input', { type: 'checkbox', checked: existing?.[k] === true || null });
      return h('label', { class: 'chk', style: 'margin:2px 0' }, checks[k], k);
    }));
  return {
    el,
    read() {
      const out = {};
      for (const [k, c] of Object.entries(checks)) if (c.checked) out[k] = true;
      return out;
    },
  };
}

// Per-server override sections (tri-state or checkbox based on `tri`).
function serverPermEditor(servers, serverKeys, existing, tri) {
  const container = h('div');
  const sections = new Map(); // serverId -> grid
  const existingMap = existing || {};

  const options = [{ id: '*', name: 'All servers (*)' }, ...servers];
  const addSel = h('select', {}, h('option', { value: '' }, '— add server rules —'),
    options.map((s) => h('option', { value: s.id }, s.name)));
  addSel.addEventListener('change', () => {
    if (addSel.value) addSection(addSel.value);
    addSel.value = '';
  });

  function nameFor(sid) {
    if (sid === '*') return 'All servers (*)';
    return servers.find((s) => s.id === sid)?.name || sid;
  }

  function addSection(sid) {
    if (sections.has(sid)) return;
    const grid = tri ? triGrid(serverKeys, existingMap[sid]) : checkGrid(serverKeys, existingMap[sid]);
    sections.set(sid, grid);
    const sec = h('div', { style: 'margin:10px 0 4px' },
      h('div', { class: 'row spread' },
        h('b', { class: 'small' }, nameFor(sid)),
        h('button', {
          class: 'btn-sm btn-ghost', type: 'button',
          onclick: () => { sections.delete(sid); sec.remove(); },
        }, 'remove')),
      grid.el);
    container.append(sec);
  }
  for (const sid of Object.keys(existingMap)) {
    if (Object.keys(existingMap[sid] || {}).length) addSection(sid);
  }

  return {
    el: h('div', {}, addSel, container),
    read() {
      const out = {};
      for (const [sid, grid] of sections) {
        const v = grid.read();
        if (Object.keys(v).length) out[sid] = v;
      }
      return out;
    },
  };
}

// ---------- Users ----------

async function usersPage(page) {
  const [users, roles, perms, servers] = await Promise.all([
    get('/api/users'), get('/api/roles'), get('/api/perms'), get('/api/servers'),
  ]);
  const roleName = (id) => roles.find((r) => r.id === id)?.name || (id ? id : '—');

  const table = h('div');
  page.append(
    h('div', { class: 'page-title spread row' },
      h('span', {}, 'Users'),
      h('button', { class: 'btn-acc', onclick: () => userForm(null) }, '+ New user')),
    h('div', { class: 'card hint mb0', style: 'margin-bottom:16px' },
      'Users sign in with the username + password you set here. A user\'s explicit permission overrides whatever their role says — allow or deny. Admins bypass all checks.'),
    table);

  render();

  function render() {
    clear(table);
    table.append(h('div', { class: 'card', style: 'padding:0' },
      h('table', { class: 'tbl' },
        h('thead', {}, h('tr', {},
          h('th', {}, 'Username'), h('th', {}, 'Role'), h('th', {}, 'Flags'),
          h('th', {}, '2FA'), h('th', { class: 'num' }, 'Last login'), h('th', {}, ''))),
        h('tbody', {}, users.map((u) => h('tr', {},
          h('td', {}, u.username),
          h('td', {}, roleName(u.role_id)),
          h('td', {},
            u.is_admin ? h('span', { class: 'badge admin' }, 'admin') : null, ' ',
            u.disabled ? h('span', { class: 'badge stopped' }, 'disabled') : null, ' ',
            u.must_change_pw ? h('span', { class: 'badge starting' }, 'temp pw') : null),
          h('td', {}, u.totp_enabled ? 'on' : 'off'),
          h('td', { class: 'num' }, fmtTime(u.last_login)),
          h('td', { class: 'num' },
            h('button', { class: 'btn-sm', onclick: () => userForm(u) }, 'Edit'), ' ',
            h('button', { class: 'btn-sm', onclick: () => resetPw(u) }, 'Set password'), ' ',
            u.totp_enabled ? h('button', {
              class: 'btn-sm', onclick: async () => {
                if (!await confirmDialog(`Remove 2FA from ${u.username}? They can sign in with password only until re-enrolled.`, { danger: true, okLabel: 'Remove 2FA' })) return;
                try { await post(`/api/users/${u.id}/totp/reset`); toast('2FA removed'); reload(); }
                catch (e) { toast(e.message, 'error'); }
              },
            }, 'Reset 2FA') : null, ' ',
            u.id !== state.me.id ? h('button', {
              class: 'btn-sm btn-danger', onclick: async () => {
                if (!await confirmDialog(`Delete user ${u.username}?`, { danger: true, okLabel: 'Delete' })) return;
                try { await del(`/api/users/${u.id}`); toast('Deleted'); reload(); }
                catch (e) { toast(e.message, 'error'); }
              },
            }, 'Delete') : null,
          ),
        ))),
      )));
  }

  async function reload() {
    const fresh = await get('/api/users');
    users.length = 0; users.push(...fresh);
    render();
  }

  async function resetPw(u) {
    const pw = await promptDialog(`Set password for ${u.username}`, 'Temporary password (they must change it at next sign-in)');
    if (!pw) return;
    try { await post(`/api/users/${u.id}/password`, { password: pw }); toast('Password set'); reload(); }
    catch (e) { toast(e.message, 'error'); }
  }

  function userForm(existing) {
    const uname = h('input', { type: 'text', value: existing?.username || '', disabled: !!existing });
    const pw = existing ? null : h('input', { type: 'password', autocomplete: 'new-password' });
    const roleSel = h('select', {}, h('option', { value: '' }, '— no role —'),
      roles.map((r) => h('option', { value: r.id, selected: existing?.role_id === r.id || null }, r.name)));
    const isAdmin = h('input', { type: 'checkbox', checked: existing?.is_admin || null, disabled: !state.me.is_admin });
    const disabled = h('input', { type: 'checkbox', checked: existing?.disabled || null });
    const mustChange = existing ? null : h('input', { type: 'checkbox', checked: true });

    const gGrid = triGrid(perms.global, existing?.overrides);
    const sEditor = serverPermEditor(servers, perms.server, existing?.server_overrides, true);

    const form = h('div', {},
      h('div', { class: 'form-grid' },
        h('label', { class: 'f' }, h('span', {}, 'Username'), uname),
        pw ? h('label', { class: 'f' }, h('span', {}, 'Initial password'), pw) : null,
        h('label', { class: 'f' }, h('span', {}, 'Role'), roleSel),
      ),
      h('label', { class: 'chk' }, isAdmin, 'Administrator (bypasses all permissions — admin only can set)'),
      existing ? h('label', { class: 'chk' }, disabled, 'Disabled (cannot sign in)') : null,
      mustChange ? h('label', { class: 'chk' }, mustChange, 'Require password change on first sign-in') : null,
      h('h3', { style: 'margin-top:14px' }, 'Global permission overrides'),
      h('div', { class: 'hint' }, 'inherit = use the role\'s setting. allow/deny always win over the role.'),
      gGrid.el,
      h('h3', { style: 'margin-top:14px' }, 'Per-server permission overrides'),
      sEditor.el,
      h('div', { class: 'form-actions' },
        h('button', { onclick: () => close() }, 'Cancel'),
        h('button', {
          class: 'btn-acc', onclick: async () => {
            try {
              if (existing) {
                await patch(`/api/users/${existing.id}`, {
                  role_id: roleSel.value,
                  is_admin: state.me.is_admin ? isAdmin.checked : undefined,
                  disabled: disabled.checked,
                  overrides: gGrid.read(),
                  server_overrides: sEditor.read(),
                });
              } else {
                await post('/api/users', {
                  username: uname.value, password: pw.value,
                  role_id: roleSel.value, is_admin: isAdmin.checked,
                  must_change_pw: mustChange.checked,
                  overrides: gGrid.read(), server_overrides: sEditor.read(),
                });
              }
              toast('Saved'); close(); reload();
            } catch (e) { toast(e.message, 'error'); }
          },
        }, existing ? 'Save' : 'Create user'),
      ));
    const close = modal(existing ? `Edit ${existing.username}` : 'New user', form, { wide: true });
  }
}

// ---------- Roles ----------

async function rolesPage(page) {
  const [roles, perms, servers] = await Promise.all([get('/api/roles'), get('/api/perms'), get('/api/servers')]);
  const table = h('div');
  page.append(
    h('div', { class: 'page-title spread row' },
      h('span', {}, 'Roles'),
      h('button', { class: 'btn-acc', onclick: () => roleForm(null) }, '+ New role')),
    h('div', { class: 'card hint', style: 'margin-bottom:16px' },
      'A role is a reusable permission set. Assign per-server permissions to specific servers or to "All servers (*)". Users inherit their role and can be overridden individually.'),
    table);

  render();
  function render() {
    clear(table);
    if (!roles.length) { table.append(h('div', { class: 'card empty' }, 'No roles yet.')); return; }
    table.append(h('div', { class: 'card', style: 'padding:0' },
      h('table', { class: 'tbl' },
        h('thead', {}, h('tr', {}, h('th', {}, 'Role'), h('th', {}, 'Global perms'), h('th', {}, 'Server rules'), h('th', {}, ''))),
        h('tbody', {}, roles.map((r) => h('tr', {},
          h('td', {}, r.name),
          h('td', { class: 'small muted' }, Object.keys(r.global || {}).filter((k) => r.global[k]).join(', ') || '—'),
          h('td', { class: 'small muted' }, Object.keys(r.servers || {}).length + ' server(s)'),
          h('td', { class: 'num' },
            h('button', { class: 'btn-sm', onclick: () => roleForm(r) }, 'Edit'), ' ',
            h('button', {
              class: 'btn-sm btn-danger', onclick: async () => {
                if (!await confirmDialog(`Delete role "${r.name}"? Users keep their personal overrides only.`, { danger: true, okLabel: 'Delete' })) return;
                try { await del(`/api/roles/${r.id}`); toast('Deleted'); reload(); }
                catch (e) { toast(e.message, 'error'); }
              },
            }, 'Delete')),
        ))),
      )));
  }
  async function reload() {
    const fresh = await get('/api/roles');
    roles.length = 0; roles.push(...fresh);
    render();
  }

  function roleForm(existing) {
    const name = h('input', { type: 'text', value: existing?.name || '' });
    const gGrid = checkGrid(perms.global, existing?.global);
    const sEditor = serverPermEditor(servers, perms.server, existing?.servers, false);
    const form = h('div', {},
      h('label', { class: 'f' }, h('span', {}, 'Role name'), name),
      h('h3', {}, 'Global permissions'),
      gGrid.el,
      h('h3', { style: 'margin-top:14px' }, 'Per-server permissions'),
      sEditor.el,
      h('div', { class: 'form-actions' },
        h('button', { onclick: () => close() }, 'Cancel'),
        h('button', {
          class: 'btn-acc', onclick: async () => {
            const body = { name: name.value, global: gGrid.read(), servers: sEditor.read() };
            try {
              if (existing) await patch(`/api/roles/${existing.id}`, body);
              else await post('/api/roles', body);
              toast('Saved'); close(); reload();
            } catch (e) { toast(e.message, 'error'); }
          },
        }, 'Save'),
      ));
    const close = modal(existing ? `Edit role: ${existing.name}` : 'New role', form, { wide: true });
  }
}

// ---------- AI settings ----------

async function aiPage(page) {
  const cfg = await get('/api/ai/settings');
  const PROVIDERS = [
    ['sglang', 'SGLang'], ['vllm', 'vLLM'], ['openrouter', 'OpenRouter'],
    ['lmstudio', 'LM Studio'], ['llamacpp', 'llama.cpp'], ['custom', 'Custom (OpenAI-compatible)'],
  ];
  const DEFAULT_URLS = {
    sglang: 'http://127.0.0.1:30000/v1', vllm: 'http://127.0.0.1:8000/v1',
    openrouter: 'https://openrouter.ai/api/v1', lmstudio: 'http://127.0.0.1:1234/v1',
    llamacpp: 'http://127.0.0.1:8080/v1', custom: '',
  };

  const enabled = h('input', { type: 'checkbox', checked: cfg.enabled || null });
  const provider = h('select', {}, PROVIDERS.map(([v, l]) => h('option', { value: v, selected: cfg.provider === v || null }, l)));
  const baseURL = h('input', { type: 'text', value: cfg.base_url || '' });
  provider.addEventListener('change', () => { baseURL.value = DEFAULT_URLS[provider.value] ?? ''; });
  const apiKey = h('input', { type: 'password', placeholder: cfg.api_key_set ? '(key saved — leave empty to keep)' : '(only OpenRouter needs one)' });
  const modelList = h('datalist', { id: 'model-list' });
  const model = h('input', { type: 'text', value: cfg.model || '', list: 'model-list', placeholder: 'e.g. qwen3-14b / deepseek/deepseek-r1' });
  const temp = h('input', { type: 'number', step: '0.05', min: '0', max: '2', value: cfg.temperature });
  const maxTok = h('input', { type: 'number', value: cfg.max_tokens });
  const effort = h('select', {},
    ['', 'low', 'medium', 'high'].map((v) => h('option', { value: v, selected: cfg.reasoning_effort === v || null }, v || '(off)')));
  const extra = h('textarea', { rows: '3', placeholder: '{"chat_template_kwargs": {"enable_thinking": true}}' });
  extra.value = cfg.extra_body || '';
  const webSearch = h('input', { type: 'checkbox', checked: cfg.web_search_enabled || null });
  const ctxLines = h('input', { type: 'number', value: cfg.context_lines });
  const maxIter = h('input', { type: 'number', value: cfg.agent_max_iterations });
  const testOut = h('div', { class: 'small muted', style: 'margin-top:8px' });

  page.append(
    h('div', { class: 'page-title' }, 'AI Settings'),
    h('div', { class: 'card' },
      h('div', { class: 'hint' },
        'Only admins see this page. Users additionally need the global "ai.use" permission plus per-server "ai.ask" / "ai.agent". All five providers speak the OpenAI-compatible API; reasoning models (DeepSeek-R1, QwQ, Qwen3, o-series via OpenRouter) are supported — thinking is shown collapsed.'),
      h('label', { class: 'chk' }, enabled, 'Enable AI features'),
      h('div', { class: 'form-grid' },
        h('label', { class: 'f' }, h('span', {}, 'Provider'), provider),
        h('label', { class: 'f' }, h('span', {}, 'Base URL'), baseURL),
        h('label', { class: 'f' }, h('span', {}, 'API key'), apiKey),
        h('label', { class: 'f' }, h('span', {}, 'Model'), model, modelList),
        h('label', { class: 'f' }, h('span', {}, 'Temperature'), temp),
        h('label', { class: 'f' }, h('span', {}, 'Max tokens'), maxTok),
        h('label', { class: 'f' }, h('span', {}, 'Reasoning effort (OpenRouter only)'), effort),
        h('label', { class: 'f' }, h('span', {}, 'Console lines for "ask" context'), ctxLines),
        h('label', { class: 'f' }, h('span', {}, 'Agent max tool iterations'), maxIter),
      ),
      h('label', { class: 'f' }, h('span', {}, 'Extra request body JSON (advanced — merged into every request)'), extra),
      h('label', { class: 'chk' }, webSearch, 'Allow the agent to search the web (DuckDuckGo — free, no API key)'),
      h('div', { class: 'form-actions' },
        h('button', { class: 'btn-acc', onclick: save }, 'Save'),
        h('button', { onclick: test }, 'Test connection'),
        cfg.api_key_set ? h('button', {
          class: 'btn-ghost', onclick: async () => {
            if (await confirmDialog('Clear the saved API key?')) { await put('/api/ai/settings', { api_key: '-' }); toast('Key cleared'); location.reload(); }
          },
        }, 'Clear key') : null,
      ),
      testOut,
    ),
  );

  async function save() {
    const body = {
      enabled: enabled.checked, provider: provider.value, base_url: baseURL.value,
      model: model.value, temperature: +temp.value, max_tokens: +maxTok.value,
      reasoning_effort: effort.value, extra_body: extra.value,
      web_search_enabled: webSearch.checked, context_lines: +ctxLines.value,
      agent_max_iterations: +maxIter.value,
    };
    if (apiKey.value) body.api_key = apiKey.value;
    try { await put('/api/ai/settings', body); toast('AI settings saved'); }
    catch (e) { toast(e.message, 'error'); }
  }

  async function test() {
    testOut.textContent = 'Testing…';
    try {
      await save();
      const res = await post('/api/ai/test');
      testOut.textContent = `Connected. ${res.models.length} model(s): ${res.models.slice(0, 8).join(', ')}${res.models.length > 8 ? '…' : ''}`;
      clear(modelList);
      modelList.append(...res.models.map((m) => h('option', { value: m })));
    } catch (e) { testOut.textContent = 'Failed: ' + e.message; }
  }
}

// ---------- Panel settings ----------

async function settingsPage(page) {
  const cfg = await get('/api/settings');
  const bind = h('input', { type: 'text', value: cfg.bind });
  const port = h('input', { type: 'number', value: cfg.port });
  const tlsMode = h('select', {},
    [['self-signed', 'HTTPS — self-signed (default)'], ['custom', 'HTTPS — own certificate'], ['http', 'HTTP (local testing only)']]
      .map(([v, l]) => h('option', { value: v, selected: cfg.tls.mode === v || null }, l)));
  const cert = h('input', { type: 'text', value: cfg.tls.cert_file || '', placeholder: '/path/fullchain.pem' });
  const key = h('input', { type: 'text', value: cfg.tls.key_file || '', placeholder: '/path/privkey.pem' });
  const hosts = h('input', { type: 'text', value: (cfg.tls.extra_hosts || []).join(', '), placeholder: 'panel.example.com, 192.168.1.10' });
  const ttl = h('input', { type: 'number', value: cfg.session_ttl_hours });
  const upl = h('input', { type: 'number', value: cfg.max_upload_mb });
  const proxy = h('input', { type: 'checkbox', checked: cfg.trust_proxy || null });
  const tlsProxy = h('input', { type: 'checkbox', checked: cfg.behind_tls_proxy || null });

  page.append(
    h('div', { class: 'page-title' }, 'Panel Settings'),
    h('div', { class: 'card' },
      h('div', { class: 'form-grid' },
        h('label', { class: 'f' }, h('span', {}, 'Bind address'), bind),
        h('label', { class: 'f' }, h('span', {}, 'Port'), port),
        h('label', { class: 'f' }, h('span', {}, 'TLS'), tlsMode),
        h('label', { class: 'f' }, h('span', {}, 'Certificate file (custom TLS)'), cert),
        h('label', { class: 'f' }, h('span', {}, 'Key file (custom TLS)'), key),
        h('label', { class: 'f' }, h('span', {}, 'Extra hostnames/IPs for self-signed cert'), hosts),
        h('label', { class: 'f' }, h('span', {}, 'Session lifetime (hours)'), ttl),
        h('label', { class: 'f' }, h('span', {}, 'Max upload size (MB)'), upl),
      ),
      h('label', { class: 'chk' }, proxy, 'Behind a reverse proxy — trust X-Forwarded-For for audit logs'),
      h('label', { class: 'chk' }, tlsProxy, 'Reverse proxy terminates HTTPS — mark session cookies Secure and send HSTS (required when TLS is set to HTTP behind nginx/Caddy)'),
      h('div', { class: 'form-actions' },
        h('button', {
          class: 'btn-acc', onclick: async () => {
            try {
              await put('/api/settings', {
                bind: bind.value, port: +port.value, tls_mode: tlsMode.value,
                cert_file: cert.value, key_file: key.value,
                extra_hosts: hosts.value.split(',').map((s) => s.trim()).filter(Boolean),
                session_ttl_hours: +ttl.value, max_upload_mb: +upl.value,
                trust_proxy: proxy.checked, behind_tls_proxy: tlsProxy.checked,
              });
              toast('Saved — restart the panel to apply');
            } catch (e) { toast(e.message, 'error'); }
          },
        }, 'Save'),
        h('button', {
          class: 'btn-danger', onclick: async () => {
            if (!await confirmDialog('Restart the panel now? Running Minecraft servers are stopped gracefully first. If the panel is not running as a service it will stay stopped.', { danger: true, okLabel: 'Restart panel' })) return;
            try { await post('/api/restart'); toast('Restarting…'); } catch (e) { toast(e.message, 'error'); }
          },
        }, 'Restart panel'),
      ),
      h('div', { class: 'hint' },
        'HTTPS with a self-signed certificate shows a one-time browser warning — expected for a LAN panel. For a public domain, use "own certificate" with certbot-issued files.'),
    ),
  );
  updatesCard(page);
}

// ---------- Updates ----------

async function updatesCard(page) {
  const statusLine = h('div', { class: 'small muted' }, 'Checking…');
  const detail = h('div');
  const autoBox = h('input', { type: 'checkbox' });
  const checkBtn = h('button', { onclick: () => refresh(true) }, 'Check for updates');
  const updateBtn = h('button', { class: 'btn-acc', style: 'display:none' }, 'Update now');

  page.append(h('div', { class: 'card' },
    h('div', { class: 'spread row' }, h('h3', {}, 'Updates'), statusLine),
    detail,
    h('label', { class: 'chk' }, autoBox,
      'Update automatically when a new release is published (checks every 6 hours, restarts the panel unattended)'),
    h('div', { class: 'form-actions' }, checkBtn, updateBtn),
    h('div', { class: 'hint' },
      'Updates come from github.com/Dalek70/blockpanel releases. During an update running Minecraft servers are stopped; ones with auto-start come back after the restart. The replaced binary is kept next to the new one as "blockpanel.previous" for manual rollback.'),
  ));

  autoBox.addEventListener('change', async () => {
    try {
      await put('/api/update/settings', { auto_update: autoBox.checked });
      toast(autoBox.checked ? 'Auto-update on' : 'Auto-update off');
    } catch (e) { autoBox.checked = !autoBox.checked; toast(e.message, 'error'); }
  });

  updateBtn.addEventListener('click', async () => {
    const ok = await confirmDialog(
      'Download and install the update now? The panel restarts and running Minecraft servers are stopped (auto-start ones come back).',
      { okLabel: 'Update & restart' });
    if (!ok) return;
    try {
      const from = (await get('/api/update/status')).current;
      await post('/api/update/apply');
      statusLine.textContent = 'Updating — downloading and verifying…';
      updateBtn.disabled = true;
      // The old process keeps answering while it downloads, and goes silent
      // during the exec swap — so poll until the reported version CHANGES
      // (new build up → reload), an apply error appears, or we time out.
      const woke = Date.now();
      const timer = setInterval(async () => {
        try {
          const st = await get('/api/update/status');
          if (st.current && st.current !== from) {
            clearInterval(timer);
            location.reload();
            return;
          }
          if (st.apply_error) {
            clearInterval(timer);
            updateBtn.disabled = false;
            statusLine.textContent = 'BlockPanel v' + from;
            toast('Update failed: ' + st.apply_error, 'error');
            refresh(false);
            return;
          }
          statusLine.textContent = st.applying
            ? 'Updating — downloading and verifying…'
            : 'Updating — restarting…';
        } catch {
          // Connection gap = the exec swap in progress; keep polling.
          statusLine.textContent = 'Updating — restarting…';
        }
        if (Date.now() - woke > 600000) {
          clearInterval(timer);
          updateBtn.disabled = false;
          toast('Update is taking too long — check the panel log', 'error');
        }
      }, 3000);
    } catch (e) { toast(e.message, 'error'); }
  });

  async function refresh(force) {
    try {
      const st = force
        ? await post('/api/update/check')
        : await get('/api/update/status');
      render(st);
    } catch (e) {
      statusLine.textContent = '';
      clear(detail);
      detail.append(h('div', { class: 'small', style: 'color:var(--danger);margin-bottom:8px' },
        'Update check failed: ' + e.message));
    }
  }

  function render(st) {
    autoBox.checked = st.auto_update;
    statusLine.textContent = 'BlockPanel v' + st.current;
    clear(detail);
    if (st.applying) {
      detail.append(h('div', { class: 'small', style: 'margin-bottom:8px' }, 'An update is being installed…'));
      updateBtn.style.display = 'none';
      setTimeout(() => refresh(false), 5000); // keep following it
      return;
    }
    if (st.apply_error) {
      detail.append(h('div', { class: 'small', style: 'color:var(--danger);margin-bottom:8px' },
        'Last update attempt failed: ' + st.apply_error));
    }
    if (st.update_available) {
      const link = h('a', { href: st.release_url || '#', target: '_blank', rel: 'noopener noreferrer' }, 'release notes');
      detail.append(h('div', { class: 'small', style: 'margin-bottom:8px' },
        h('span', { class: 'badge' }, 'update available'), ' v', st.current, ' → v', st.latest, ' — ', link));
      updateBtn.style.display = '';
    } else {
      updateBtn.style.display = 'none';
      let txt = 'Up to date.';
      if (st.check_error) txt = 'Last check failed: ' + st.check_error;
      else if (!st.checked_at || st.checked_at.startsWith('0001')) txt = 'Not checked yet — first check runs a couple of minutes after start.';
      detail.append(h('div', { class: 'small muted', style: 'margin-bottom:8px' }, txt));
    }
  }

  refresh(false);
}

// ---------- Audit ----------

async function auditPage(page) {
  const userF = h('input', { type: 'text', placeholder: 'filter user' });
  const actionF = h('input', { type: 'text', placeholder: 'filter action (e.g. files.)' });
  const limitF = h('input', { type: 'number', value: 200, style: 'width:90px' });
  const table = h('div');

  page.append(
    h('div', { class: 'page-title' }, 'Audit Log'),
    h('div', { class: 'row', style: 'margin-bottom:12px' },
      userF, actionF, limitF,
      h('button', { class: 'btn-acc', onclick: load }, 'Apply')),
    table);

  async function load() {
    const q = new URLSearchParams({ user: userF.value, action: actionF.value, limit: limitF.value });
    let entries;
    try { entries = await get('/api/audit?' + q); }
    catch (e) { toast(e.message, 'error'); return; }
    clear(table);
    if (!entries.length) { table.append(h('div', { class: 'card empty' }, 'No matching entries.')); return; }
    table.append(h('div', { class: 'card', style: 'padding:0' },
      h('table', { class: 'tbl' },
        h('thead', {}, h('tr', {},
          h('th', {}, 'Time'), h('th', {}, 'User'), h('th', {}, 'IP'),
          h('th', {}, 'Action'), h('th', {}, 'Target / detail'), h('th', {}, 'Server'))),
        h('tbody', {}, entries.map((e) => h('tr', {},
          h('td', { class: 'num small' }, fmtTime(e.time)),
          h('td', {}, e.user),
          h('td', { class: 'mono small' }, e.ip),
          h('td', { class: 'mono small' }, e.action),
          h('td', { class: 'small muted' }, [e.target, e.detail].filter(Boolean).join(' — ')),
          h('td', { class: 'small muted' }, e.server_id || ''),
        ))),
      )));
  }
  await load();
}
