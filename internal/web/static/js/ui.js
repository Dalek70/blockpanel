// Tiny DOM helpers. h() builds elements; strings become text nodes, so user
// data can never inject markup.

export function h(tag, props = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v; // CSSOM: works under strict CSP
    else if (k === 'dataset') Object.assign(el.dataset, v);
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
    else if (k === 'value') el.value = v;
    else if (k === 'checked') el.checked = true;
    else if (k === 'disabled') el.disabled = true;
    else if (k === 'selected') el.selected = true;
    else el.setAttribute(k, v === true ? '' : v);
  }
  append(el, children);
  return el;
}

function append(el, kids) {
  for (const c of kids.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

export function clear(el) { while (el.firstChild) el.removeChild(el.firstChild); }

// Inline SVG icons (stroke-based, currentColor). Built with createElementNS —
// h() can't make SVG elements — and only from this static table, never from
// user data.
const ICONS = {
  cube: [['path', 'M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z'], ['path', 'M3.27 6.96 12 12.01l8.73-5.05'], ['path', 'M12 22.08V12']],
  users: [['path', 'M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2'], ['circle', { cx: 9, cy: 7, r: 4 }], ['path', 'M23 21v-2a4 4 0 0 0-3-3.87'], ['path', 'M16 3.13a4 4 0 0 1 0 7.75']],
  shield: [['path', 'M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z']],
  spark: [['path', 'M13 2 3 14h9l-1 8 10-12h-9l1-8z']],
  sliders: [['path', 'M4 21v-7'], ['path', 'M4 10V3'], ['path', 'M12 21v-9'], ['path', 'M12 8V3'], ['path', 'M20 21v-5'], ['path', 'M20 12V3'], ['path', 'M1 14h6'], ['path', 'M9 8h6'], ['path', 'M17 16h6']],
  key: [['path', 'M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0l3 3L22 7l-3-3m-3.5 3.5L19 4']],
  list: [['path', 'M8 6h13'], ['path', 'M8 12h13'], ['path', 'M8 18h13'], ['path', 'M3 6h.01'], ['path', 'M3 12h.01'], ['path', 'M3 18h.01']],
  user: [['path', 'M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2'], ['circle', { cx: 12, cy: 7, r: 4 }]],
  sun: [['circle', { cx: 12, cy: 12, r: 5 }], ['path', 'M12 1v2'], ['path', 'M12 21v2'], ['path', 'M4.22 4.22l1.42 1.42'], ['path', 'M18.36 18.36l1.42 1.42'], ['path', 'M1 12h2'], ['path', 'M21 12h2'], ['path', 'M4.22 19.78l1.42-1.42'], ['path', 'M18.36 5.64l1.42-1.42']],
  moon: [['path', 'M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z']],
  logout: [['path', 'M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4'], ['path', 'M16 17l5-5-5-5'], ['path', 'M21 12H9']],
  menu: [['path', 'M3 12h18'], ['path', 'M3 6h18'], ['path', 'M3 18h18']],
};

const SVGNS = 'http://www.w3.org/2000/svg';

export function icon(name, size = 16) {
  const svg = document.createElementNS(SVGNS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '2');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('class', 'icon');
  svg.setAttribute('aria-hidden', 'true');
  if (size !== 16) svg.style.cssText = `width:${size}px;height:${size}px`;
  for (const [tag, attrs] of ICONS[name] || []) {
    const el = document.createElementNS(SVGNS, tag);
    if (typeof attrs === 'string') el.setAttribute('d', attrs);
    else for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    svg.append(el);
  }
  return svg;
}

export function fmtBytes(n) {
  if (n < 1024) return n + ' B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return n.toFixed(n >= 100 ? 0 : 1) + ' ' + units[i];
}

export function fmtDur(sec) {
  sec = Math.floor(sec);
  if (sec <= 0) return '—';
  const d = Math.floor(sec / 86400), hh = Math.floor(sec % 86400 / 3600),
        mm = Math.floor(sec % 3600 / 60), ss = sec % 60;
  if (d) return `${d}d ${hh}h ${mm}m`;
  if (hh) return `${hh}h ${mm}m`;
  if (mm) return `${mm}m ${ss}s`;
  return `${ss}s`;
}

export function fmtTime(iso) {
  if (!iso || iso.startsWith('0001')) return '—';
  const d = new Date(iso);
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

export function toast(msg, type = 'ok') {
  const t = h('div', { class: 'toast' + (type === 'error' ? ' error' : '') }, msg);
  document.getElementById('toasts').append(t);
  const ttl = type === 'error' ? 6500 : 3500;
  setTimeout(() => t.classList.add('bye'), ttl - 220);
  setTimeout(() => t.remove(), ttl);
}

export function badge(state) {
  return h('span', { class: 'badge ' + state }, state);
}

export function modal(title, contentEl, { wide = false } = {}) {
  const box = h('div', { class: 'modal' + (wide ? ' wide' : '') }, h('h3', {}, title), contentEl);
  const ov = h('div', { class: 'overlay' }, box);
  ov.addEventListener('mousedown', (e) => { if (e.target === ov) close(); });
  const esc = (e) => { if (e.key === 'Escape') close(); };
  document.addEventListener('keydown', esc);
  function close() { ov.remove(); document.removeEventListener('keydown', esc); }
  document.body.append(ov);
  return close;
}

export function confirmDialog(text, { danger = false, okLabel = 'Confirm' } = {}) {
  return new Promise((resolve) => {
    const content = h('div', {},
      h('p', { class: 'mt0' }, text),
      h('div', { class: 'form-actions' },
        h('button', { onclick: () => { close(); resolve(false); } }, 'Cancel'),
        h('button', { class: danger ? 'btn-danger' : 'btn-acc', onclick: () => { close(); resolve(true); } }, okLabel),
      ));
    const close = modal('Are you sure?', content);
  });
}

// promptDialog for single text input flows (rename, mkdir…)
export function promptDialog(title, label, initial = '') {
  return new Promise((resolve) => {
    const input = h('input', { type: 'text', value: initial });
    const form = h('form', {
      onsubmit: (e) => { e.preventDefault(); close(); resolve(input.value); },
    },
      h('label', { class: 'f' }, h('span', {}, label), input),
      h('div', { class: 'form-actions' },
        h('button', { type: 'button', onclick: () => { close(); resolve(null); } }, 'Cancel'),
        h('button', { class: 'btn-acc', type: 'submit' }, 'OK'),
      ));
    const close = modal(title, form);
    setTimeout(() => input.focus(), 30);
  });
}
