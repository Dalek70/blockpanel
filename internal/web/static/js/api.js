// Fetch wrapper: JSON in/out, CSRF header, 401 redirect, SSE-over-POST reader.

let csrfToken = null;
export function setCsrf(t) { csrfToken = t; }

export class ApiError extends Error {
  constructor(msg, status) { super(msg); this.status = status; }
}

async function parseError(resp) {
  let msg = resp.statusText;
  try {
    const j = await resp.json();
    if (j && j.error) msg = j.error;
  } catch { /* not json */ }
  return new ApiError(msg, resp.status);
}

export async function api(method, path, body) {
  const headers = {};
  if (csrfToken) headers['X-CSRF-Token'] = csrfToken;
  const opts = { method, headers, credentials: 'same-origin' };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch(path, opts);
  if (resp.status === 401 && !path.startsWith('/api/auth') && path !== '/api/setup') {
    location.hash = '#/login';
    throw new ApiError('not authenticated', 401);
  }
  if (!resp.ok) throw await parseError(resp);
  return resp.json();
}

export const get = (p) => api('GET', p);
export const post = (p, b) => api('POST', p, b ?? {});
export const put = (p, b) => api('PUT', p, b);
export const patch = (p, b) => api('PATCH', p, b);
export const del = (p) => api('DELETE', p);

// Upload files via XHR so we get progress events for big jars/worlds.
export function upload(path, files, onProgress) {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    for (const f of files) fd.append('file', f, f.name);
    const xhr = new XMLHttpRequest();
    xhr.open('POST', path);
    if (csrfToken) xhr.setRequestHeader('X-CSRF-Token', csrfToken);
    xhr.upload.onprogress = (e) => {
      if (onProgress && e.lengthComputable) onProgress(e.loaded / e.total);
    };
    xhr.onload = () => {
      let j = null;
      try { j = JSON.parse(xhr.responseText); } catch { /* ignore */ }
      if (xhr.status >= 200 && xhr.status < 300) resolve(j);
      else reject(new ApiError(j?.error || xhr.statusText, xhr.status));
    };
    xhr.onerror = () => reject(new ApiError('upload failed', 0));
    xhr.send(fd);
  });
}

// POST that answers with an SSE stream (AI ask/agent). Calls onEvent for each
// `data:` JSON object. Returns an AbortController.
export function ssePost(path, body, onEvent, onError, onDone) {
  const ctrl = new AbortController();
  (async () => {
    let resp;
    try {
      resp = await fetch(path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        credentials: 'same-origin',
        body: JSON.stringify(body),
        signal: ctrl.signal,
      });
    } catch (e) {
      if (e.name !== 'AbortError') onError?.(e.message);
      return;
    }
    if (!resp.ok || !resp.headers.get('content-type')?.includes('event-stream')) {
      onError?.((await parseError(resp)).message);
      return;
    }
    const reader = resp.body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          for (const line of block.split('\n')) {
            if (line.startsWith('data:')) {
              try { onEvent(JSON.parse(line.slice(5).trim())); } catch { /* skip */ }
            }
          }
        }
      }
    } catch (e) {
      if (e.name !== 'AbortError') onError?.(e.message);
      return;
    }
    onDone?.();
  })();
  return ctrl;
}
