import { get, post, patch, del, put, upload, ssePost } from '../api.js';
import { h, clear, toast, badge, fmtBytes, fmtDur, fmtTime, confirmDialog, promptDialog, modal } from '../ui.js';
import { state } from '../app.js';
import { renderPlayers, renderProperties, renderSchedules, renderGraphs } from './servertabs.js';
import { renderMarkdown } from '../md.js';

export async function renderServer(page, id, tab) {
  let srv = await get(`/api/servers/${id}`);
  const p = srv.perms || {};

  // ---- header ----
  const stateBadge = h('span', {}, badge(srv.state));
  const statBar = h('div', { class: 'statbar' });
  const buttons = h('div', { class: 'row' });

  function renderHeader() {
    clear(statBar);
    statBar.append(
      h('span', {}, 'uptime ', h('b', {}, fmtDur(srv.uptime_sec))),
      h('span', {}, 'cpu ', h('b', {}, srv.state === 'stopped' ? '—' : srv.cpu_percent.toFixed(0) + '%')),
      h('span', {}, 'mem ', h('b', {}, srv.state === 'stopped' ? '—' : srv.rss_mb.toFixed(0) + ' MB')),
      h('span', {}, 'port ', h('b', {}, srv.port)),
      srv.players_max ? h('span', {}, 'players ', h('b', {}, `${srv.players_now}/${srv.players_max}`)) : null,
      srv.mc_version ? h('span', {}, 'version ', h('b', {}, srv.mc_version)) : null,
      srv.motd ? h('span', { class: 'muted', title: srv.motd }, '“' + srv.motd.split('\n')[0].slice(0, 48) + '”') : null,
    );
    clear(stateBadge);
    stateBadge.append(badge(srv.state));
    clear(buttons);
    const stopped = srv.state === 'stopped';
    if (p.start) buttons.append(h('button', { class: 'btn-acc btn-sm', disabled: !stopped, onclick: () => act('start') }, 'Start'));
    if (p.restart) buttons.append(h('button', { class: 'btn-sm', disabled: stopped, onclick: () => act('restart') }, 'Restart'));
    if (p.stop) buttons.append(h('button', { class: 'btn-sm', disabled: stopped, onclick: () => act('stop') }, 'Stop'));
    if (p.kill) buttons.append(h('button', { class: 'btn-sm btn-danger', disabled: stopped, onclick: killConfirm }, 'Kill'));
  }

  async function act(verb) {
    try { await post(`/api/servers/${id}/${verb}`); toast(verb + ' requested'); }
    catch (e) { toast(e.message, 'error'); }
  }
  async function killConfirm() {
    if (await confirmDialog('Force-kill the process? Unsaved world data may be lost.', { danger: true, okLabel: 'Kill' })) act('kill');
  }

  async function refreshSrv() {
    try { srv = await get(`/api/servers/${id}`); renderHeader(); } catch { /* ignore */ }
  }

  // ---- tabs ----
  const tabs = [];
  const addTab = (key, label, cond) => { if (cond) tabs.push([key, label]); };
  addTab('console', 'Console', p['console.view']);
  addTab('files', 'Files', p['files.view']);
  addTab('players', 'Players', p['view']);
  addTab('properties', 'Properties', p['config.edit']);
  addTab('schedules', 'Schedules', p['schedules.manage']);
  addTab('settings', 'Settings', p['config.edit'] || state.me.is_admin);
  addTab('backups', 'Backups', p['backups.create'] || p['backups.restore'] || p['backups.download'] || p['backups.delete'] || state.me.is_admin);
  addTab('webhooks', 'Webhooks', p['webhooks.manage']);
  addTab('ai', 'AI', (p['ai.ask'] || p['ai.agent']) && state.me.global['ai.use']);
  if (!tabs.some(([k]) => k === tab)) tab = tabs.length ? tabs[0][0] : 'none';

  const body = h('div');
  page.append(
    h('div', { class: 'page-title row' },
      h('span', {}, srv.name), stateBadge,
      h('span', { class: 'grow' }), buttons),
    statBar,
    h('div', { style: 'height:14px' }),
    h('div', { class: 'tabs' },
      tabs.map(([key, label]) =>
        h('a', { href: `#/server/${id}/${key}`, class: key === tab ? 'active' : '' }, label))),
    body,
  );
  renderHeader();

  const cleanups = [];
  state.cleanup = () => cleanups.forEach((f) => { try { f(); } catch { /* ignore */ } });
  const headerIv = setInterval(refreshSrv, 5000);
  cleanups.push(() => clearInterval(headerIv));

  switch (tab) {
    case 'console': renderConsole(); break;
    case 'files': renderFiles(); break;
    case 'players': renderPlayers(body, id, p); break;
    case 'properties': renderProperties(body, id); break;
    case 'schedules': renderSchedules(body, id); break;
    case 'settings': renderSettings(); break;
    case 'backups': renderBackups(); break;
    case 'webhooks': renderWebhooks(); break;
    case 'ai': renderAI(); break;
    default: body.append(h('div', { class: 'card empty' }, 'No permissions for any tab on this server.'));
  }

  // ================= Console =================
  function renderConsole() {
    const box = h('div', { class: 'console' });
    let autoscroll = true;

    // Toolbar: live filter, history search, log download, metric graphs.
    const filterInput = h('input', {
      type: 'text', placeholder: 'Filter visible lines…', style: 'max-width:220px',
    });
    const graphs = h('div', { class: 'graphs' });
    let graphsShown = false;
    const graphBtn = h('button', {
      class: 'btn-sm', onclick: async () => {
        graphsShown = !graphsShown;
        graphs.style.display = graphsShown ? '' : 'none';
        graphBtn.textContent = graphsShown ? 'Hide graphs' : 'Show graphs';
        if (graphsShown) await renderGraphs(graphs, id);
      },
    }, 'Show graphs');
    graphs.style.display = 'none';

    filterInput.addEventListener('input', () => {
      const f = filterInput.value.toLowerCase();
      for (const child of box.children) {
        child.style.display = !f || child.textContent.toLowerCase().includes(f) ? '' : 'none';
      }
    });

    body.append(
      h('div', { class: 'row', style: 'margin-bottom:10px' },
        filterInput,
        h('button', {
          class: 'btn-sm', onclick: async () => {
            const q = await promptDialog('Search console history', 'Text to find (searches the full buffer)');
            if (!q) return;
            try {
              const hits = await get(`/api/servers/${id}/console/search?q=${encodeURIComponent(q)}`);
              const pre = h('pre', { style: 'max-height:60vh;overflow:auto;font-size:12px' },
                hits.length ? hits.map((l) => `${new Date(l.t).toTimeString().slice(0, 8)}  ${l.text}\n`).join('') : 'No matches.');
              modal(`${hits.length} match${hits.length === 1 ? '' : 'es'} for "${q}"`, pre, { wide: true });
            } catch (e) { toast(e.message, 'error'); }
          },
        }, 'Search history'),
        graphBtn,
        h('a', { class: 'btn btn-sm', href: `/api/servers/${id}/console/download` }, 'Download log'),
        h('span', { class: 'grow' }),
        h('button', { class: 'btn-sm btn-ghost', onclick: () => clear(box) }, 'Clear view'),
      ),
      graphs);
    box.addEventListener('scroll', () => {
      autoscroll = box.scrollTop + box.clientHeight >= box.scrollHeight - 30;
    });

    function addLine(l) {
      const cls = classify(l.text);
      const row = h('div', { class: cls },
        h('span', { class: 't' }, timePart(l.t) + ' '), l.text);
      const f = filterInput.value.toLowerCase();
      if (f && !row.textContent.toLowerCase().includes(f)) row.style.display = 'none';
      box.append(row);
      while (box.childElementCount > 3000) box.firstElementChild.remove();
      if (autoscroll) box.scrollTop = box.scrollHeight;
    }
    function classify(text) {
      if (text.startsWith('[panel]') || text.startsWith('> ')) return 'panelmsg';
      if (/ERROR|SEVERE|FATAL|Exception|\tat /.test(text)) return 'err';
      if (/WARN/.test(text)) return 'warn';
      return '';
    }
    function timePart(iso) {
      try { return new Date(iso).toTimeString().slice(0, 8); } catch { return ''; }
    }

    const es = new EventSource(`/api/servers/${id}/console/stream`);
    es.addEventListener('line', (e) => addLine(JSON.parse(e.data)));
    es.addEventListener('state', (e) => {
      const st = JSON.parse(e.data);
      if (st !== srv.state) { srv.state = st; renderHeader(); }
    });
    es.onerror = () => { /* EventSource auto-reconnects */ };
    cleanups.push(() => es.close());

    body.append(box);

    if (p['console.send']) {
      const input = h('input', { type: 'text', placeholder: 'server command (e.g. "list", "say hi") — Enter to send' });
      const history = [];
      let hIdx = -1;
      input.addEventListener('keydown', (e) => {
        if (e.key === 'ArrowUp') { if (hIdx < history.length - 1) { hIdx++; input.value = history[history.length - 1 - hIdx] || ''; } e.preventDefault(); }
        if (e.key === 'ArrowDown') { if (hIdx > 0) { hIdx--; input.value = history[history.length - 1 - hIdx] || ''; } else { hIdx = -1; input.value = ''; } e.preventDefault(); }
      });
      body.append(h('form', {
        class: 'console-input',
        onsubmit: async (e) => {
          e.preventDefault();
          const cmd = input.value.trim();
          if (!cmd) return;
          try {
            await post(`/api/servers/${id}/command`, { command: cmd });
            history.push(cmd); hIdx = -1; input.value = '';
          } catch (ex) { toast(ex.message, 'error'); }
        },
      },
        input, h('button', { class: 'btn-acc', type: 'submit' }, 'Send')));
    }
  }

  // ================= Files =================
  function renderFiles() {
    let cwd = '';
    const crumbs = h('div', { class: 'crumbs grow' });
    const listing = h('div');
    const uploadInput = h('input', { type: 'file', multiple: true, style: 'display:none' });
    uploadInput.addEventListener('change', async () => {
      if (!uploadInput.files.length) return;
      const prog = h('span', { class: 'muted small' }, 'uploading… 0%');
      bar.append(prog);
      try {
        await upload(`/api/servers/${id}/files/upload?path=${encodeURIComponent(cwd)}`,
          uploadInput.files, (f) => { prog.textContent = `uploading… ${Math.round(f * 100)}%`; });
        toast('Uploaded');
        load();
      } catch (e) { toast(e.message, 'error'); }
      prog.remove();
      uploadInput.value = '';
    });

    const selected = new Set();
    const selInfo = h('span', { class: 'muted small' });

    function updateSel() {
      selInfo.textContent = selected.size ? `${selected.size} selected` : '';
      selActions.style.display = selected.size ? '' : 'none';
    }

    const selActions = h('span', { class: 'row' },
      p['files.edit'] ? h('button', {
        class: 'btn-sm', onclick: async () => {
          const name = await promptDialog('Create archive', 'Archive filename', 'archive.zip');
          if (!name) return;
          try {
            await post(`/api/servers/${id}/files/archive`, { paths: [...selected], dest: join(cwd, name) });
            toast('Archive created'); selected.clear(); load();
          } catch (e) { toast(e.message, 'error'); }
        },
      }, 'Zip selected') : null,
      p['files.edit'] ? h('button', {
        class: 'btn-sm btn-danger', onclick: async () => {
          if (!await confirmDialog(`Delete ${selected.size} selected item(s)?`, { danger: true, okLabel: 'Delete' })) return;
          try {
            await post(`/api/servers/${id}/files/delete-batch`, { paths: [...selected] });
            toast('Deleted'); selected.clear(); load();
          } catch (e) { toast(e.message, 'error'); }
        },
      }, 'Delete selected') : null);
    selActions.style.display = 'none';

    const searchBox = h('input', { type: 'text', placeholder: 'Search files…', style: 'max-width:170px' });
    searchBox.addEventListener('keydown', async (e) => {
      if (e.key !== 'Enter') return;
      const q = searchBox.value.trim();
      if (!q) { load(); return; }
      try {
        const hits = await get(`/api/servers/${id}/files/search?path=${encodeURIComponent(cwd)}&q=${encodeURIComponent(q)}`);
        showSearch(hits, q);
      } catch (ex) { toast(ex.message, 'error'); }
    });

    const bar = h('div', { class: 'row', style: 'margin-bottom:10px' },
      crumbs, selInfo, selActions, searchBox,
      p['files.edit'] ? h('button', { class: 'btn-sm', onclick: () => uploadInput.click() }, 'Upload') : null,
      p['files.edit'] ? h('button', {
        class: 'btn-sm', onclick: async () => {
          const name = await promptDialog('New folder', 'Folder name');
          if (!name) return;
          try { await post(`/api/servers/${id}/files/mkdir`, { path: join(cwd, name) }); load(); }
          catch (e) { toast(e.message, 'error'); }
        },
      }, 'New folder') : null,
      h('button', { class: 'btn-sm', onclick: () => load() }, 'Refresh'),
      uploadInput,
    );
    body.append(bar, listing);

    function join(a, b) { return a ? a + '/' + b : b; }

    function renderCrumbs() {
      clear(crumbs);
      const parts = cwd ? cwd.split('/') : [];
      crumbs.append(h('a', { href: 'javascript:;', onclick: () => { cwd = ''; load(); } }, srv.name));
      let acc = '';
      for (const part of parts) {
        acc = join(acc, part);
        const target = acc;
        crumbs.append(' / ', h('a', { href: 'javascript:;', onclick: () => { cwd = target; load(); } }, part));
      }
    }

    async function load() {
      renderCrumbs();
      let entries;
      try { entries = await get(`/api/servers/${id}/files?path=${encodeURIComponent(cwd)}`); }
      catch (e) { toast(e.message, 'error'); return; }
      clear(listing);
      const rows = entries.map((en) => {
        const rel = join(cwd, en.name);
        const check = h('input', {
          type: 'checkbox',
          onclick: (e) => {
            e.stopPropagation();
            if (e.target.checked) selected.add(rel); else selected.delete(rel);
            updateSel();
          },
        });
        const isZip = /\.(zip|jar)$/i.test(en.name);
        const actions = h('td', { class: 'num' },
          !en.is_dir && isZip && p['files.edit'] ? h('button', {
            class: 'btn-sm', onclick: async (e) => {
              e.stopPropagation();
              const dest = await promptDialog('Extract archive', 'Extract into folder (relative)', cwd || '.');
              if (dest === null) return;
              try {
                await post(`/api/servers/${id}/files/extract`, { path: rel, dest });
                toast('Extracted'); load();
              } catch (ex) { toast(ex.message, 'error'); }
            },
          }, 'Extract') : null,
          ' ',
          !en.is_dir && p['files.download'] ? h('a', {
            class: 'btn btn-sm', href: `/api/servers/${id}/files/download?path=${encodeURIComponent(rel)}`,
            onclick: (e) => e.stopPropagation(),
          }, 'Download') : null,
          ' ',
          p['files.edit'] ? h('button', {
            class: 'btn-sm', onclick: async (e) => {
              e.stopPropagation();
              const to = await promptDialog('Rename', 'New name', en.name);
              if (!to || to === en.name) return;
              try { await post(`/api/servers/${id}/files/rename`, { from: rel, to: join(cwd, to) }); load(); }
              catch (ex) { toast(ex.message, 'error'); }
            },
          }, 'Rename') : null,
          ' ',
          p['files.edit'] ? h('button', {
            class: 'btn-sm btn-danger', onclick: async (e) => {
              e.stopPropagation();
              if (await confirmDialog(`Delete ${en.name}${en.is_dir ? ' and everything inside it' : ''}?`, { danger: true, okLabel: 'Delete' })) {
                try { await post(`/api/servers/${id}/files/delete`, { path: rel }); load(); }
                catch (ex) { toast(ex.message, 'error'); }
              }
            },
          }, 'Delete') : null,
        );
        return h('tr', {
          class: 'click',
          onclick: () => { en.is_dir ? (cwd = rel, selected.clear(), updateSel(), load()) : openEditor(rel); },
        },
          h('td', { style: 'width:28px' }, check),
          h('td', {}, en.is_dir ? '📁 ' : '📄 ', en.name),
          h('td', { class: 'num' }, en.is_dir ? '' : fmtBytes(en.size)),
          h('td', { class: 'num' }, fmtTime(en.mod_time)),
          actions);
      });
      listing.append(h('div', { class: 'card', style: 'padding:0' },
        h('table', { class: 'tbl' },
          h('thead', {}, h('tr', {}, h('th', {}, ''), h('th', {}, 'Name'), h('th', { class: 'num' }, 'Size'), h('th', { class: 'num' }, 'Modified'), h('th', {}, ''))),
          h('tbody', {}, rows.length ? rows : h('tr', {}, h('td', { colspan: '5', class: 'empty' }, 'Empty directory'))),
        )));
    }

    // Search results view (flat list of matches under the current folder).
    function showSearch(hits, q) {
      clear(listing);
      listing.append(h('div', { class: 'row', style: 'margin-bottom:8px' },
        h('span', { class: 'muted small' }, `${hits.length} match${hits.length === 1 ? '' : 'es'} for "${q}"`),
        h('button', { class: 'btn-sm', onclick: () => { searchBox.value = ''; load(); } }, 'Back to browsing')));
      listing.append(h('div', { class: 'card', style: 'padding:0' },
        h('table', { class: 'tbl' },
          h('thead', {}, h('tr', {}, h('th', {}, 'Path'), h('th', { class: 'num' }, 'Size'), h('th', { class: 'num' }, 'Modified'))),
          h('tbody', {}, hits.length
            ? hits.map((en) => h('tr', {
                class: 'click',
                onclick: () => {
                  if (en.is_dir) { cwd = en.name; searchBox.value = ''; load(); }
                  else openEditor(en.name);
                },
              },
                h('td', { class: 'mono small' }, (en.is_dir ? '📁 ' : '📄 ') + en.name),
                h('td', { class: 'num' }, en.is_dir ? '' : fmtBytes(en.size)),
                h('td', { class: 'num' }, fmtTime(en.mod_time))))
            : h('tr', {}, h('td', { colspan: '3', class: 'empty' }, 'No matches'))))));
    }

    async function openEditor(rel) {
      let content;
      try { content = (await get(`/api/servers/${id}/files/read?path=${encodeURIComponent(rel)}`)).content; }
      catch (e) { toast(e.message, 'error'); return; }
      const ta = h('textarea', { rows: '24', spellcheck: 'false' });
      ta.value = content;
      const readOnly = !p['files.edit'];
      const closeBtn = h('button', { type: 'button', onclick: () => close() }, readOnly ? 'Close' : 'Cancel');
      const saveBtn = readOnly ? null : h('button', {
        class: 'btn-acc', onclick: async () => {
          try {
            await put(`/api/servers/${id}/files/write`, { path: rel, content: ta.value });
            toast('Saved'); close(); load();
          } catch (e) { toast(e.message, 'error'); }
        },
      }, 'Save');
      const close = modal(rel, h('div', {}, ta, h('div', { class: 'form-actions' }, closeBtn, saveBtn)), { wide: true });
    }

    load();
  }

  // ================= Settings =================
  function renderSettings() {
    const isAdmin = state.me.is_admin;
    const f = {
      name: h('input', { type: 'text', value: srv.name }),
      jar: h('input', { type: 'text', value: srv.jar || '', placeholder: 'server.jar' }),
      min_mem_mb: h('input', { type: 'number', value: srv.min_mem_mb || 1024 }),
      max_mem_mb: h('input', { type: 'number', value: srv.max_mem_mb || 2048 }),
      stop_command: h('input', { type: 'text', value: srv.stop_command || 'stop' }),
      stop_grace_secs: h('input', { type: 'number', value: srv.stop_grace_secs || 30 }),
      auto_restart: h('input', { type: 'checkbox', checked: srv.auto_restart || null }),
      auto_start: h('input', { type: 'checkbox', checked: srv.auto_start || null }),
      accept_eula: h('input', { type: 'checkbox', checked: srv.accept_eula || null }),
      backup_keep: h('input', { type: 'number', value: srv.backup_keep || 0, min: '0' }),
    };

    body.append(h('div', { class: 'card' },
      h('h3', {}, 'Launch'),
      h('div', { class: 'form-grid' },
        h('label', { class: 'f' }, h('span', {}, 'Name'), f.name),
        h('label', { class: 'f' }, h('span', {}, 'Server jar (in server folder)'), f.jar),
        h('label', { class: 'f' }, h('span', {}, 'Min memory (MB)'), f.min_mem_mb),
        h('label', { class: 'f' }, h('span', {}, 'Max memory (MB)'), f.max_mem_mb),
        h('label', { class: 'f' }, h('span', {}, 'Stop command'), f.stop_command),
        h('label', { class: 'f' }, h('span', {}, 'Stop grace period (s)'), f.stop_grace_secs),
        h('label', { class: 'f' }, h('span', {}, 'Backups to keep (0 = keep all)'), f.backup_keep),
      ),
      h('label', { class: 'chk' }, f.accept_eula, 'Auto-accept Minecraft EULA (writes eula.txt on start)'),
      h('label', { class: 'chk' }, f.auto_restart, 'Restart automatically if the server crashes'),
      h('label', { class: 'chk' }, f.auto_start, 'Start this server when the panel starts'),
      h('div', { class: 'form-actions' },
        h('button', { class: 'btn-acc', onclick: save }, 'Save'),
        h('span', { class: 'muted small' }, 'Changes apply on next start.')),
    ));

    // One-click installer from the official distribution APIs.
    if (p['config.edit'] || isAdmin) {
      const flavor = h('select', {},
        [['paper', 'Paper'], ['purpur', 'Purpur'], ['vanilla', 'Vanilla'], ['fabric', 'Fabric']]
          .map(([v, l]) => h('option', { value: v }, l)));
      const version = h('select', {}, h('option', {}, 'choose a type first'));
      const installBtn = h('button', { class: 'btn-acc', disabled: true }, 'Install');

      async function loadVersions() {
        clear(version);
        version.append(h('option', {}, 'loading…'));
        installBtn.disabled = true;
        try {
          const res = await get(`/api/versions?type=${flavor.value}`);
          clear(version);
          version.append(...res.versions.map((v) => h('option', { value: v }, v)));
          installBtn.disabled = false;
        } catch (e) {
          clear(version);
          version.append(h('option', {}, 'failed to load'));
          toast(e.message, 'error');
        }
      }
      flavor.addEventListener('change', loadVersions);
      installBtn.addEventListener('click', async () => {
        if (!await confirmDialog(`Install ${flavor.value} ${version.value}? It becomes this server's launch jar.`, { okLabel: 'Install' })) return;
        installBtn.disabled = true;
        const old = installBtn.textContent;
        installBtn.textContent = 'Downloading…';
        try {
          const res = await post(`/api/servers/${id}/jar-install`, { type: flavor.value, version: version.value });
          toast(`Installed ${res.jar} (${res.size})`);
          f.jar.value = res.jar;
        } catch (e) { toast(e.message, 'error'); }
        installBtn.disabled = false;
        installBtn.textContent = old;
      });
      loadVersions();

      body.append(h('div', { class: 'card' },
        h('h3', {}, 'Install a server jar'),
        h('div', { class: 'hint' }, 'Downloads directly from the official Paper, Purpur, Mojang or Fabric APIs and sets it as the launch jar.'),
        h('div', { class: 'row' },
          h('div', {}, flavor), h('div', { class: 'grow' }, version), installBtn)));

      const url = h('input', { type: 'url', placeholder: 'https://…/server.jar' });
      body.append(h('div', { class: 'card' },
        h('h3', {}, 'Or download a jar from a URL'),
        h('div', { class: 'hint' }, 'Fetches any jar over HTTPS into the server folder and sets it as the launch jar.'),
        h('div', { class: 'row' },
          h('div', { class: 'grow' }, url),
          h('button', {
            onclick: async (e) => {
              const btn = e.target;
              btn.disabled = true; btn.textContent = 'Downloading…';
              try {
                const res = await post(`/api/servers/${id}/jar-url`, { url: url.value });
                toast(`Downloaded ${res.jar} (${res.size})`);
              } catch (ex) { toast(ex.message, 'error'); }
              btn.disabled = false; btn.textContent = 'Download';
            },
          }, 'Download')),
      ));
    }

    if (isAdmin) {
      const dl = h('input', { type: 'checkbox', checked: srv.downloads_enabled || null });
      const blocked = h('input', { type: 'text', value: (srv.blocked_extensions || []).join(', '), placeholder: 'jar, zip' });
      const override = h('input', { type: 'text', value: srv.launch_override || '', placeholder: './run.sh (runs via sh -c instead of java -jar)' });
      const javaPath = h('input', { type: 'text', value: srv.java_path || 'java' });
      const jvmArgs = h('input', { type: 'text', value: srv.jvm_args || '', placeholder: '-XX:+UseG1GC …' });
      const serverArgs = h('input', { type: 'text', value: srv.server_args || 'nogui' });
      const javaPick = h('select', {}, h('option', { value: '' }, 'detected runtimes…'));
      javaPick.addEventListener('change', () => { if (javaPick.value) javaPath.value = javaPick.value; });
      get('/api/java').then((list) => {
        clear(javaPick);
        javaPick.append(h('option', { value: '' }, `detected runtimes (${list.length})`));
        javaPick.append(...list.map((j) =>
          h('option', { value: j.path }, `${j.vendor || 'Java'} ${j.version}${j.default ? ' (default)' : ''}`)));
      }).catch(() => { /* detection is best-effort */ });

      body.append(h('div', { class: 'card' },
        h('h3', {}, 'Admin-only'),
        h('div', { class: 'hint' },
          'JVM and server arguments are admin-only: JVM flags such as -javaagent and -XX:OnOutOfMemoryError execute code.'),
        h('div', { class: 'form-grid' },
          h('label', { class: 'f' }, h('span', {}, 'Java executable'), javaPath),
          h('label', { class: 'f' }, h('span', {}, 'Detected Java runtimes'), javaPick),
          h('label', { class: 'f' }, h('span', {}, 'Extra JVM args'), jvmArgs),
          h('label', { class: 'f' }, h('span', {}, 'Server args'), serverArgs),
          h('label', { class: 'f' }, h('span', {}, 'Launch override (advanced)'), override),
        ),
        h('label', { class: 'chk' }, dl, 'Allow file downloads from this server'),
        h('label', { class: 'f' }, h('span', {}, 'Blocked download extensions (comma-separated — e.g. block "jar" so plugins can\'t be taken)'), blocked),
        h('div', { class: 'hint' }, 'Download policy applies to non-admin users, including backup downloads (a backup zip would bypass the block otherwise).'),
        h('div', { class: 'form-actions' },
          h('button', {
            class: 'btn-acc', onclick: async () => {
              try {
                await patch(`/api/servers/${id}`, {
                  java_path: javaPath.value,
                  jvm_args: jvmArgs.value,
                  server_args: serverArgs.value,
                  launch_override: override.value,
                  downloads_enabled: dl.checked,
                  blocked_extensions: blocked.value.split(',').map((s) => s.trim()).filter(Boolean),
                });
                toast('Saved');
              } catch (e) { toast(e.message, 'error'); }
            },
          }, 'Save admin settings')),
      ));
    }

    if (state.me.global['servers.manage'] || isAdmin) {
      body.append(h('div', { class: 'card danger-zone' },
        h('h3', {}, 'Danger zone'),
        h('div', { class: 'hint' }, srv.imported
          ? 'This server was imported — deleting removes it from the panel but leaves its directory on disk.'
          : 'Deleting removes the server, its world and its backups from disk permanently.'),
        h('button', {
          class: 'btn-danger', onclick: async () => {
            if (!await confirmDialog(`Delete server "${srv.name}"? ${srv.imported ? '' : 'World data and backups are erased.'}`, { danger: true, okLabel: 'Delete server' })) return;
            try { await del(`/api/servers/${id}`); toast('Server deleted'); location.hash = '#/'; }
            catch (e) { toast(e.message, 'error'); }
          },
        }, 'Delete server'),
      ));
    }

    async function save() {
      try {
        await patch(`/api/servers/${id}`, {
          name: f.name.value,
          jar: f.jar.value,
          min_mem_mb: +f.min_mem_mb.value,
          max_mem_mb: +f.max_mem_mb.value,
          stop_command: f.stop_command.value,
          stop_grace_secs: +f.stop_grace_secs.value,
          auto_restart: f.auto_restart.checked,
          auto_start: f.auto_start.checked,
          accept_eula: f.accept_eula.checked,
          backup_keep: +f.backup_keep.value,
        });
        toast('Saved');
      } catch (e) { toast(e.message, 'error'); }
    }
  }

  // ================= Backups =================
  function renderBackups() {
    const list = h('div');
    body.append(
      h('div', { class: 'row', style: 'margin-bottom:10px' },
        p['backups.create'] ? h('button', {
          class: 'btn-acc', onclick: async (e) => {
            const btn = e.target;
            btn.disabled = true; btn.textContent = 'Creating…';
            try { const res = await post(`/api/servers/${id}/backups`); toast('Backup created: ' + res.name); load(); }
            catch (ex) { toast(ex.message, 'error'); }
            btn.disabled = false; btn.textContent = 'Create backup';
          },
        }, 'Create backup') : null,
        h('span', { class: 'muted small' }, 'Best taken while the server is stopped (or after save-all).'),
      ),
      list);

    async function load() {
      let backups;
      try { backups = await get(`/api/servers/${id}/backups`); }
      catch (e) { toast(e.message, 'error'); return; }
      clear(list);
      if (!backups.length) { list.append(h('div', { class: 'card empty' }, 'No backups yet.')); return; }
      list.append(h('div', { class: 'card', style: 'padding:0' },
        h('table', { class: 'tbl' },
          h('thead', {}, h('tr', {}, h('th', {}, 'Backup'), h('th', { class: 'num' }, 'Size'), h('th', { class: 'num' }, 'Created'), h('th', {}, ''))),
          h('tbody', {}, backups.map((b) => h('tr', {},
            h('td', { class: 'mono' }, b.name),
            h('td', { class: 'num' }, fmtBytes(b.size)),
            h('td', { class: 'num' }, fmtTime(b.mod_time)),
            h('td', { class: 'num' },
              p['backups.restore'] ? h('button', {
                class: 'btn-sm', onclick: async () => {
                  if (!await confirmDialog(`Restore ${b.name}? Current server files are WIPED and replaced. Server must be stopped.`, { danger: true, okLabel: 'Restore' })) return;
                  try { await post(`/api/servers/${id}/backups/${b.name}/restore`); toast('Restored'); }
                  catch (e) { toast(e.message, 'error'); }
                },
              }, 'Restore') : null, ' ',
              p['backups.download'] ? h('a', { class: 'btn btn-sm', href: `/api/servers/${id}/backups/${b.name}/download` }, 'Download') : null, ' ',
              p['backups.delete'] ? h('button', {
                class: 'btn-sm btn-danger', onclick: async () => {
                  if (!await confirmDialog(`Delete backup ${b.name}?`, { danger: true, okLabel: 'Delete' })) return;
                  try { await del(`/api/servers/${id}/backups/${b.name}`); load(); }
                  catch (e) { toast(e.message, 'error'); }
                },
              }, 'Delete') : null,
            ),
          ))),
        )));
    }
    load();
  }

  // ================= Webhooks =================
  function renderWebhooks() {
    const list = h('div');
    const EVENTS = ['start', 'stop', 'crash', 'backup'];

    function whForm(existing) {
      const name = h('input', { type: 'text', value: existing?.name || '', placeholder: 'e.g. #server-status' });
      const url = h('input', { type: 'url', value: '', placeholder: existing ? '(unchanged)' : 'https://discord.com/api/webhooks/…' });
      const enabled = h('input', { type: 'checkbox', checked: (existing ? existing.enabled : true) || null });
      const evChecks = Object.fromEntries(EVENTS.map((ev) => [ev,
        h('input', { type: 'checkbox', checked: (existing ? existing.events?.includes(ev) : true) || null })]));
      const form = h('div', {},
        h('label', { class: 'f' }, h('span', {}, 'Name'), name),
        h('label', { class: 'f' }, h('span', {}, 'Discord webhook URL'), url),
        h('div', { class: 'row' }, EVENTS.map((ev) => h('label', { class: 'chk', style: 'margin:0' }, evChecks[ev], ev))),
        h('label', { class: 'chk', style: 'margin-top:8px' }, enabled, 'Enabled'),
        h('div', { class: 'form-actions' },
          h('button', { onclick: () => close() }, 'Cancel'),
          h('button', {
            class: 'btn-acc', onclick: async () => {
              const events = EVENTS.filter((ev) => evChecks[ev].checked);
              try {
                if (existing) {
                  const bodyPatch = { name: name.value, events, enabled: enabled.checked };
                  if (url.value) bodyPatch.url = url.value;
                  await patch(`/api/servers/${id}/webhooks/${existing.id}`, bodyPatch);
                } else {
                  await post(`/api/servers/${id}/webhooks`, { name: name.value, url: url.value, events, enabled: enabled.checked });
                }
                toast('Saved'); close(); load();
              } catch (e) { toast(e.message, 'error'); }
            },
          }, 'Save'),
        ));
      const close = modal(existing ? 'Edit webhook' : 'Add Discord webhook', form);
    }

    body.append(
      h('div', { class: 'row', style: 'margin-bottom:10px' },
        h('button', { class: 'btn-acc', onclick: () => whForm(null) }, '+ Add webhook'),
        h('span', { class: 'muted small' }, 'Discord notifications for start / stop / crash / backup events.')),
      list);

    async function load() {
      let hooks;
      try { hooks = await get(`/api/servers/${id}/webhooks`); }
      catch (e) { toast(e.message, 'error'); return; }
      clear(list);
      if (!hooks.length) { list.append(h('div', { class: 'card empty' }, 'No webhooks configured.')); return; }
      list.append(h('div', { class: 'card', style: 'padding:0' },
        h('table', { class: 'tbl' },
          h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', {}, 'URL'), h('th', {}, 'Events'), h('th', {}, 'Enabled'), h('th', {}, ''))),
          h('tbody', {}, hooks.map((wh) => h('tr', {},
            h('td', {}, wh.name),
            h('td', { class: 'mono small muted' }, wh.url_masked),
            h('td', { class: 'small' }, (wh.events || []).join(', ') || '—'),
            h('td', {}, wh.enabled ? 'yes' : 'no'),
            h('td', { class: 'num' },
              h('button', {
                class: 'btn-sm', onclick: async () => {
                  try { await post(`/api/servers/${id}/webhooks/${wh.id}/test`); toast('Test sent'); }
                  catch (e) { toast(e.message, 'error'); }
                },
              }, 'Test'), ' ',
              h('button', { class: 'btn-sm', onclick: () => whForm(wh) }, 'Edit'), ' ',
              h('button', {
                class: 'btn-sm btn-danger', onclick: async () => {
                  if (!await confirmDialog(`Delete webhook "${wh.name}"?`, { danger: true, okLabel: 'Delete' })) return;
                  try { await del(`/api/servers/${id}/webhooks/${wh.id}`); load(); }
                  catch (e) { toast(e.message, 'error'); }
                },
              }, 'Delete'),
            ),
          ))),
        )));
    }
    load();
  }

  // ================= AI =================
  function renderAI() {
    let mode = p['ai.ask'] ? 'ask' : 'agent';
    let running = null; // AbortController while streaming

    const chat = h('div', { class: 'chat' });
    const input = h('textarea', { rows: '2', placeholder: 'Ask about the logs…' });
    const sendBtn = h('button', { class: 'btn-acc', type: 'submit' }, 'Send');
    const stopBtn = h('button', { type: 'button', style: 'display:none', onclick: () => running?.abort() }, 'Stop');

    const modeAsk = h('button', { class: 'btn-sm', type: 'button', onclick: () => setMode('ask') }, 'Ask about logs');
    const modeAgent = h('button', { class: 'btn-sm', type: 'button', onclick: () => setMode('agent') }, 'Agent');
    const resetBtn = h('button', {
      class: 'btn-sm btn-ghost', type: 'button', style: 'display:none',
      onclick: async () => {
        try { await post(`/api/servers/${id}/ai/agent/reset`); chat.replaceChildren(); toast('Agent conversation reset'); }
        catch (e) { toast(e.message, 'error'); }
      },
    }, 'Reset conversation');

    function setMode(m) {
      mode = m;
      modeAsk.className = 'btn-sm' + (m === 'ask' ? ' btn-acc' : '');
      modeAgent.className = 'btn-sm' + (m === 'agent' ? ' btn-acc' : '');
      resetBtn.style.display = m === 'agent' ? '' : 'none';
      input.placeholder = m === 'ask'
        ? `Ask about the logs — the model gets the question plus the last console lines`
        : 'Tell the agent what to investigate or change — it can read the console and files, and asks before writing';
      chat.replaceChildren();
    }
    if (!p['ai.ask']) modeAsk.style.display = 'none';
    if (!p['ai.agent']) modeAgent.style.display = 'none';
    setMode(mode);

    (async () => {
      try {
        const st = await get('/api/ai/status');
        if (!st.enabled) {
          chat.append(h('div', { class: 'card empty' }, 'AI is not configured. An admin must enable it under AI Settings.'));
          sendBtn.disabled = true;
        }
      } catch { /* ignore */ }
    })();

    body.append(
      h('div', { class: 'row', style: 'margin-bottom:12px' }, modeAsk, modeAgent, h('span', { class: 'grow' }), resetBtn),
      chat,
      h('form', {
        class: 'console-input', style: 'align-items:flex-end',
        onsubmit: (e) => { e.preventDefault(); send(); },
      }, input, h('div', { class: 'row' }, stopBtn, sendBtn)),
    );
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
    });

    function send() {
      const text = input.value.trim();
      if (!text || running) return;
      input.value = '';
      chat.append(h('div', { class: 'msg user' }, text));

      let reasoningBox = null, reasoningBody = null;
      let answer = null;
      const ensureAnswer = () => {
        if (!answer) { answer = h('div', { class: 'msg assistant md' }); answer._raw = ''; chat.append(answer); }
        return answer;
      };

      // Markdown is re-rendered from the raw buffer as tokens stream in, but
      // only once per frame — reparsing on every token thrashes layout.
      const dirty = new Set();
      let frame = 0;
      const paint = () => {
        for (const el of dirty) el.replaceChildren(renderMarkdown(el._raw));
        dirty.clear();
      };
      const scheduleMarkdown = (el) => {
        dirty.add(el);
        if (frame) return;
        frame = requestAnimationFrame(() => { frame = 0; paint(); });
      };
      const flushMarkdown = () => {
        if (frame) { cancelAnimationFrame(frame); frame = 0; }
        paint();
      };
      cleanups.push(() => { if (frame) cancelAnimationFrame(frame); });
      const onEvent = (ev) => {
        switch (ev.type) {
          case 'reasoning': {
            if (!reasoningBox) {
              reasoningBody = h('div', { class: 'rbody' });
              reasoningBox = h('details', { class: 'reasoning' }, h('summary', {}, 'Reasoning'), reasoningBody);
              chat.append(reasoningBox);
            }
            reasoningBody.append(ev.text);
            break;
          }
          case 'content': {
            const el = ensureAnswer(); // same bubble until a tool call splits it
            el._raw += ev.text;
            scheduleMarkdown(el);
            break;
          }
          case 'tool_call': {
            answer = null; reasoningBox = null; // next content = fresh bubble
            let argsText = '';
            try { argsText = JSON.stringify(typeof ev.args === 'string' ? JSON.parse(ev.args) : ev.args ?? {}); }
            catch { argsText = String(ev.args ?? ''); }
            if (argsText.length > 200) argsText = argsText.slice(0, 200) + '…';
            chat.append(h('div', { class: 'msg tool' }, `⚙ ${ev.tool}(${argsText})`));
            break;
          }
          case 'tool_result': {
            const resultEl = h('div', { class: 'tool-result' }, ev.result || '');
            const box = h('div', { class: 'msg tool' },
              h('span', { class: 'toggler', onclick: () => box.classList.toggle('open') },
                `↳ ${ev.tool} result (click to expand)`),
              resultEl);
            chat.append(box);
            break;
          }
          case 'approval_required': chat.append(approvalCard(ev.approval)); break;
          case 'approval_resolved': markApproval(ev.approval_id, ev.approved); break;
          case 'error':
            chat.append(h('div', { class: 'msg assistant', style: 'border-color:var(--danger)' }, 'Error: ' + ev.error));
            break;
          case 'done': flushMarkdown(); break;
        }
        chat.parentElement && (window.scrollTo(0, document.body.scrollHeight));
      };

      const path = mode === 'ask' ? `/api/servers/${id}/ai/ask` : `/api/servers/${id}/ai/agent`;
      const bodyJson = mode === 'ask' ? { question: text } : { message: text };
      sendBtn.disabled = true; stopBtn.style.display = '';
      running = ssePost(path, bodyJson, onEvent,
        (err) => { onEvent({ type: 'error', error: err }); finish(); },
        () => finish());
      function finish() { flushMarkdown(); running = null; sendBtn.disabled = false; stopBtn.style.display = 'none'; }
      cleanups.push(() => running?.abort());
    }

    function approvalCard(ap) {
      const actions = h('div', { class: 'row' },
        h('button', { class: 'btn-acc btn-sm', onclick: () => decide(true) }, 'Approve'),
        h('button', { class: 'btn-danger btn-sm', onclick: () => decide(false) }, 'Deny'),
      );
      async function decide(approve) {
        try { await post(`/api/ai/approvals/${ap.id}`, { approve }); }
        catch (e) { toast(e.message, 'error'); }
      }
      const details = [];
      if (ap.tool === 'write_file') {
        if (ap.exists && ap.old_content !== undefined) {
          details.push(h('div', { class: 'small muted' }, 'Current content:'), h('pre', {}, ap.old_content || '(empty)'));
        }
        details.push(h('div', { class: 'small muted' }, ap.exists ? 'New content:' : 'New file content:'), h('pre', {}, ap.new_content || '(empty)'));
      }
      if (ap.tool === 'send_command') details.push(h('pre', {}, '/' + ap.command));
      const card = h('div', { class: 'approval', dataset: { approval: ap.id } },
        h('h4', {}, '⚠ The agent wants to: ', ap.summary),
        ...details, actions);
      card._actions = actions;
      return card;
    }

    function markApproval(idv, approved) {
      const card = chat.querySelector(`[data-approval="${idv}"]`);
      if (!card) return;
      card._actions?.remove();
      card.append(h('div', { class: 'decided' }, approved ? '✓ Approved — executing' : '✗ Denied'));
    }
  }
}
