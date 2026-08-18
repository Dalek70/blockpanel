import { post } from '../api.js';
import { h, icon } from '../ui.js';

function shell(...kids) {
  return h('div', { class: 'login-wrap' },
    h('div', { class: 'brand' }, h('span', { class: 'cube' }, icon('cube', 24)), 'BlockPanel'),
    h('div', { class: 'login-card' }, ...kids));
}

export function renderLogin() {
  const err = h('div', { class: 'login-err' });
  const user = h('input', { type: 'text', autocomplete: 'username', autofocus: true });
  const pass = h('input', { type: 'password', autocomplete: 'current-password' });

  const form = h('form', {
    onsubmit: async (e) => {
      e.preventDefault();
      err.textContent = '';
      try {
        const res = await post('/api/auth/login', { username: user.value, password: pass.value });
        if (res.status === 'totp_required') { swapToTotp(res.pending); return; }
        location.hash = '#/';
        location.reload();
      } catch (ex) { err.textContent = ex.message; }
    },
  },
    h('label', { class: 'f' }, h('span', {}, 'Username'), user),
    h('label', { class: 'f' }, h('span', {}, 'Password'), pass),
    h('button', { class: 'btn-acc', style: 'width:100%', type: 'submit' }, 'Sign in'),
    err,
  );

  const card = shell(form);

  function swapToTotp(pending) {
    const code = h('input', {
      type: 'text', inputmode: 'numeric', autocomplete: 'one-time-code',
      maxlength: '6', placeholder: '000000', style: 'text-align:center;font-family:var(--mono);font-size:18px;letter-spacing:6px',
    });
    const err2 = h('div', { class: 'login-err' });
    const totpForm = h('form', {
      onsubmit: async (e) => {
        e.preventDefault();
        err2.textContent = '';
        try {
          await post('/api/auth/totp', { pending, code: code.value });
          location.hash = '#/';
          location.reload();
        } catch (ex) { err2.textContent = ex.message; }
      },
    },
      h('p', { class: 'mt0 muted small' }, 'Enter the 6-digit code from your authenticator app.'),
      h('label', { class: 'f' }, code),
      h('button', { class: 'btn-acc', style: 'width:100%', type: 'submit' }, 'Verify'),
      err2,
    );
    form.replaceWith(totpForm);
    code.focus();
  }

  return card;
}

export function renderSetup() {
  const err = h('div', { class: 'login-err' });
  const user = h('input', { type: 'text', autocomplete: 'username' });
  const pass = h('input', { type: 'password', autocomplete: 'new-password' });
  const pass2 = h('input', { type: 'password', autocomplete: 'new-password' });

  return shell(
    h('p', { class: 'mt0', style: 'font-weight:600' }, 'First run — create the admin account'),
    h('p', { class: 'muted small' },
      'This account has full control of the panel and is the only one that can manage panel, AI and download settings. Further accounts are created by this admin from the Users page.'),
    h('form', {
      onsubmit: async (e) => {
        e.preventDefault();
        err.textContent = '';
        if (pass.value !== pass2.value) { err.textContent = 'passwords do not match'; return; }
        try {
          await post('/api/setup', { username: user.value, password: pass.value });
          location.hash = '#/';
          location.reload();
        } catch (ex) { err.textContent = ex.message; }
      },
    },
      h('label', { class: 'f' }, h('span', {}, 'Admin username'), user),
      h('label', { class: 'f' }, h('span', {}, 'Password (min 10 chars, letters + digits/symbols)'), pass),
      h('label', { class: 'f' }, h('span', {}, 'Repeat password'), pass2),
      h('button', { class: 'btn-acc', style: 'width:100%', type: 'submit' }, 'Create admin account'),
      err,
    ),
  );
}
