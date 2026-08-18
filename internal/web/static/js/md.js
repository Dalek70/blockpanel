// Minimal Markdown renderer for model output.
//
// Everything is built as real DOM nodes and all text lands in text nodes, so
// nothing a model emits can turn into markup — that also keeps it working
// under the panel's strict CSP, which forbids inline scripts and styles.
//
// Supported: fenced code, ATX headings, nested ordered/unordered lists,
// blockquotes, GFM tables, thematic breaks, paragraphs, and inline code,
// bold, italic, strikethrough, links and autolinks.

const SAFE_HREF = /^(?:https?:|mailto:)/i;

export function renderMarkdown(src) {
  const frag = document.createDocumentFragment();
  const lines = String(src ?? '').replace(/\r\n?/g, '\n').replace(/\t/g, '    ').split('\n');
  parseBlocks(lines, frag);
  return frag;
}

// ---- blocks ---------------------------------------------------------------

const RE_FENCE = /^ {0,3}(`{3,}|~{3,})\s*([^\s`]*)/;
const RE_HEADING = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
const RE_HR = /^ {0,3}([-*_])[ \t]*(?:\1[ \t]*){2,}$/;
const RE_QUOTE = /^ {0,3}>/;
const RE_ITEM = /^(\s*)([-*+]|\d{1,9}[.)])(\s+)(.*)$/;

const indentOf = (line) => /^\s*/.exec(line)[0].length;
const isItem = (line) => RE_ITEM.test(line) && !RE_HR.test(line);

function isBlockStart(line) {
  return RE_FENCE.test(line) || RE_HEADING.test(line) || RE_HR.test(line)
    || RE_QUOTE.test(line) || isItem(line);
}

function parseBlocks(lines, parent) {
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];

    if (!line.trim()) { i++; continue; }

    const fence = RE_FENCE.exec(line);
    if (fence) { i = parseFence(lines, i, fence, parent); continue; }

    const heading = RE_HEADING.exec(line);
    if (heading) {
      const el = document.createElement('h' + heading[1].length);
      parseInline(heading[2], el);
      parent.append(el);
      i++;
      continue;
    }

    if (RE_HR.test(line)) {
      parent.append(document.createElement('hr'));
      i++;
      continue;
    }

    if (RE_QUOTE.test(line)) { i = parseQuote(lines, i, parent); continue; }
    if (isItem(line)) { i = parseList(lines, i, parent); continue; }
    if (isTable(lines, i)) { i = parseTable(lines, i, parent); continue; }

    const buf = [];
    while (i < lines.length && lines[i].trim() && !isBlockStart(lines[i]) && !isTable(lines, i)) {
      buf.push(lines[i]);
      i++;
    }
    const p = document.createElement('p');
    parseInline(buf.join('\n'), p);
    parent.append(p);
  }
}

function parseFence(lines, i, fence, parent) {
  const closer = new RegExp('^ {0,3}' + fence[1][0] + '{' + fence[1].length + ',}\\s*$');
  const buf = [];
  i++;
  while (i < lines.length && !closer.test(lines[i])) buf.push(lines[i++]);
  if (i < lines.length) i++; // closing fence

  const code = document.createElement('code');
  if (fence[2]) code.className = 'lang-' + fence[2].replace(/[^\w+.-]/g, '');
  code.textContent = buf.join('\n');
  const pre = document.createElement('pre');
  pre.append(code);
  parent.append(pre);
  return i;
}

function parseQuote(lines, i, parent) {
  const buf = [];
  while (i < lines.length) {
    const line = lines[i];
    if (RE_QUOTE.test(line)) { buf.push(line.replace(/^ {0,3}> ?/, '')); i++; continue; }
    // Lazy continuation: an unmarked line still belongs to the quote.
    if (line.trim() && !isBlockStart(line)) { buf.push(line); i++; continue; }
    break;
  }
  const bq = document.createElement('blockquote');
  parseBlocks(buf, bq);
  parent.append(bq);
  return i;
}

function parseList(lines, start, parent) {
  const first = RE_ITEM.exec(lines[start]);
  const base = first[1].length;
  const ordered = /\d/.test(first[2]);
  const list = document.createElement(ordered ? 'ol' : 'ul');
  if (ordered) {
    const n = parseInt(first[2], 10);
    if (n !== 1) list.start = n;
  }

  let i = start;
  let loose = false;
  while (i < lines.length) {
    if (!lines[i].trim()) {
      // A blank line only stays inside the list if list content follows.
      const next = lines[i + 1];
      if (next && next.trim() && (indentOf(next) > base || (isItem(next) && indentOf(next) >= base))) {
        loose = true;
        i++;
        continue;
      }
      break;
    }

    const m = RE_ITEM.exec(lines[i]);
    if (!m || m[1].length < base) break;
    if (m[1].length > base) break; // deeper item — belongs to the previous <li>
    if (/\d/.test(m[2]) !== ordered) break; // marker changed — a new list starts

    const contentIndent = m[1].length + m[2].length + m[3].length;
    const buf = [m[4]];
    i++;

    // Collect this item's continuation lines (including nested lists).
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) {
        const next = lines[i + 1];
        if (next && next.trim() && indentOf(next) >= contentIndent) { buf.push(''); i++; continue; }
        break;
      }
      if (isItem(line) && indentOf(line) <= base) break;
      if (indentOf(line) >= contentIndent || isItem(line)) {
        buf.push(line.slice(Math.min(indentOf(line), contentIndent)));
        i++;
        continue;
      }
      if (!isBlockStart(line)) { buf.push(line.trim()); i++; continue; } // lazy continuation
      break;
    }

    const li = document.createElement('li');
    parseBlocks(buf, li);
    list.append(li);
  }

  // Tight list: unwrap the single paragraph each item would otherwise carry.
  if (!loose) {
    for (const li of list.children) {
      if (li.childNodes.length === 1 && li.firstChild.tagName === 'P') li.replaceChildren(...li.firstChild.childNodes);
    }
  }
  parent.append(list);
  return i;
}

// ---- tables ---------------------------------------------------------------

const RE_DELIM = /^ {0,3}\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$/;

function isTable(lines, i) {
  const head = lines[i], delim = lines[i + 1];
  if (!head || !delim || !head.includes('|')) return false;
  return delim.includes('|') && RE_DELIM.test(delim);
}

function splitRow(row) {
  const s = row.trim().replace(/^\|/, '').replace(/\|\s*$/, '');
  const cells = [];
  let cur = '';
  for (let i = 0; i < s.length; i++) {
    if (s[i] === '\\' && s[i + 1] === '|') { cur += '|'; i++; continue; }
    if (s[i] === '|') { cells.push(cur); cur = ''; continue; }
    cur += s[i];
  }
  cells.push(cur);
  return cells.map((c) => c.trim());
}

function parseTable(lines, i, parent) {
  const headers = splitRow(lines[i]);
  const aligns = splitRow(lines[i + 1]).map((c) => {
    const left = c.startsWith(':'), right = c.endsWith(':');
    return right && left ? 'center' : right ? 'right' : left ? 'left' : '';
  });
  i += 2;

  const table = document.createElement('table');
  const thead = document.createElement('thead');
  const hrow = document.createElement('tr');
  headers.forEach((cell, n) => hrow.append(makeCell('th', cell, aligns[n])));
  thead.append(hrow);
  table.append(thead);

  const tbody = document.createElement('tbody');
  while (i < lines.length && lines[i].trim() && lines[i].includes('|') && !isBlockStart(lines[i])) {
    const cells = splitRow(lines[i]);
    const row = document.createElement('tr');
    for (let n = 0; n < headers.length; n++) row.append(makeCell('td', cells[n] ?? '', aligns[n]));
    tbody.append(row);
    i++;
  }
  table.append(tbody);
  parent.append(table);
  return i;
}

function makeCell(tag, text, align) {
  const cell = document.createElement(tag);
  parseInline(text, cell);
  if (align) cell.style.textAlign = align; // CSSOM — allowed under strict CSP
  return cell;
}

// ---- inline ---------------------------------------------------------------

const RE_INLINE = new RegExp([
  '(`+)([\\s\\S]*?)\\1',                                   // 1,2  code span
  '\\*\\*([\\s\\S]+?)\\*\\*',                              // 3    bold
  '__([\\s\\S]+?)__',                                      // 4    bold
  '~~([\\s\\S]+?)~~',                                      // 5    strikethrough
  '\\*([^\\s*][\\s\\S]*?)\\*',                             // 6    italic
  '(?<![\\w_])_([^\\s_][\\s\\S]*?)_(?![\\w_])',            // 7    italic
  '(!?)\\[([^\\]]*)\\]\\(\\s*<?([^\\s)>]*)>?[^)]*\\)',     // 8,9,10 image flag, text, href
  '<((?:https?:|mailto:)[^>\\s]+)>',                       // 11   autolink
  '(?<![\\w@.])((?:https?:\\/\\/)[^\\s<>()\\[\\]]*[^\\s<>()\\[\\].,;:!?\'"])', // 12 bare URL
].join('|'), 'g');

function parseInline(text, parent) {
  // Fresh instance per call: parseInline recurses (via wrap), and recursion
  // would clobber a shared global regex's lastIndex — an infinite loop.
  const re = new RegExp(RE_INLINE.source, RE_INLINE.flags);
  let last = 0, m;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) appendText(parent, text.slice(last, m.index));

    if (m[2] !== undefined) {
      const code = document.createElement('code');
      code.textContent = m[2].replace(/^ (.*?) $/, '$1');
      parent.append(code);
    } else if (m[3] !== undefined || m[4] !== undefined) {
      parent.append(wrap('strong', m[3] ?? m[4]));
    } else if (m[5] !== undefined) {
      parent.append(wrap('del', m[5]));
    } else if (m[6] !== undefined || m[7] !== undefined) {
      parent.append(wrap('em', m[6] ?? m[7]));
    } else if (m[9] !== undefined) {
      // Images render as links: the CSP blocks remote image loads anyway.
      parent.append(makeLink(m[10], m[9] || m[10], m[8] === '!'));
    } else if (m[11] !== undefined) {
      parent.append(makeLink(m[11], m[11], false));
    } else if (m[12] !== undefined) {
      parent.append(makeLink(m[12], m[12], false));
    }
    last = re.lastIndex;
  }
  if (last < text.length) appendText(parent, text.slice(last));
}

function wrap(tag, inner) {
  const el = document.createElement(tag);
  parseInline(inner, el);
  return el;
}

function makeLink(href, label, isImage) {
  const text = (isImage ? '🖼 ' : '') + label;
  if (!SAFE_HREF.test(href)) {
    // javascript:, data:, anything unexpected — show the text, never link it.
    return document.createTextNode(text);
  }
  const a = document.createElement('a');
  a.href = href;
  a.target = '_blank';
  a.rel = 'noopener noreferrer';
  a.textContent = text;
  return a;
}

// Single newlines become <br> so streamed output keeps the shape the model gave it.
function appendText(parent, text) {
  const parts = text.split('\n');
  parts.forEach((part, i) => {
    if (i) parent.append(document.createElement('br'));
    if (part) parent.append(document.createTextNode(part));
  });
}
