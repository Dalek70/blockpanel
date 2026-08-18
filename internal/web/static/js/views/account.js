import { post } from '../api.js';
import { h, clear, toast } from '../ui.js';
import { state, refreshMe } from '../app.js';

export async function renderAccount(page) {
  const me = state.me;

  if (me.must_change_pw) {
    page.append(h('div', { class: 'banner' },
      'Your account has a temporary password. Set your own password below before using the panel.'));
  }

  page.append(h('div', { class: 'page-title' }, 'Account'),
    passwordCard(), totpCard(), appearanceCard());

  function appearanceCard() {
    const T = window.BPTheme;
    const THEMES = [
      ['auto', 'Auto', 'tt-auto'], ['dark', 'Dark', 'tt-dark'], ['light', 'Light', 'tt-light'],
      ['midnight', 'Midnight', 'tt-midnight'], ['slate', 'Slate', 'tt-slate'],
    ];
    const ACCENTS = [
      ['green', '#46a86e'], ['blue', '#4a9eff'], ['purple', '#a78bfa'],
      ['amber', '#d9a13b'], ['red', '#e0665f'], ['teal', '#2dbcaf'],
    ];
    const body = h('div');
    function draw() {
      const curT = T.get('theme', 'auto'), curA = T.get('accent', 'green'), curD = T.get('density', 'comfortable');
      clear(body);
      body.append(
        h('label', { class: 'f' }, h('span', {}, 'Theme')),
        h('div', { class: 'row', style: 'margin-bottom:16px' },
          THEMES.map(([id, label, cls]) => h('button', {
            type: 'button', class: 'theme-opt' + (curT === id ? ' on' : ''),
            onclick: () => { T.set('theme', id); draw(); },
          }, h('span', { class: 'theme-thumb ' + cls }), label))),
        h('label', { class: 'f' }, h('span', {}, 'Accent color')),
        h('div', { class: 'row', style: 'margin-bottom:16px' },
          ACCENTS.map(([id, color]) => h('button', {
            type: 'button', class: 'swatch' + (curA === id ? ' on' : ''),
            title: id, 'aria-label': 'Accent: ' + id, style: 'background:' + color,
            onclick: () => { T.set('accent', id); draw(); },
          }))),
        h('label', { class: 'f' }, h('span', {}, 'Density')),
        h('div', { class: 'seg' },
          [['comfortable', 'Comfortable'], ['compact', 'Compact']].map(([id, label]) => h('button', {
            type: 'button', class: curD === id ? 'on' : '',
            onclick: () => { T.set('density', id); draw(); },
          }, label))),
      );
    }
    draw();
    return h('div', { class: 'card' },
      h('h3', {}, 'Appearance'),
      h('div', { class: 'hint' }, 'Stored in this browser only — each device keeps its own choice.'),
      body);
  }

  function passwordCard() {
    const cur = h('input', { type: 'password', autocomplete: 'current-password' });
    const nw = h('input', { type: 'password', autocomplete: 'new-password' });
    const nw2 = h('input', { type: 'password', autocomplete: 'new-password' });
    return h('div', { class: 'card' },
      h('h3', {}, 'Change password'),
      h('div', { class: 'form-grid' },
        h('label', { class: 'f' }, h('span', {}, 'Current password'), cur),
        h('label', { class: 'f' }, h('span', {}, 'New password (10+ chars, letters + digits/symbols)'), nw),
        h('label', { class: 'f' }, h('span', {}, 'Repeat new password'), nw2),
      ),
      h('div', { class: 'form-actions' },
        h('button', {
          class: 'btn-acc', onclick: async () => {
            if (nw.value !== nw2.value) { toast('passwords do not match', 'error'); return; }
            try {
              await post('/api/me/password', { current: cur.value, new: nw.value });
              toast('Password changed');
              cur.value = nw.value = nw2.value = '';
              await refreshMe();
              if (location.hash !== '#/account') location.hash = '#/';
              else location.reload();
            } catch (e) { toast(e.message, 'error'); }
          },
        }, 'Change password')));
  }

  function totpCard() {
    const body = h('div');
    const card = h('div', { class: 'card' },
      h('h3', {}, 'Two-factor authentication (TOTP)'),
      h('div', { class: 'hint' },
        'Works with Microsoft Authenticator, Google Authenticator, Aegis, 1Password — any TOTP app.'),
      body);

    if (me.totp_enabled) {
      const pw = h('input', { type: 'password', placeholder: 'password' });
      const code = h('input', { type: 'text', placeholder: 'current 6-digit code', maxlength: '6' });
      body.append(
        h('p', { class: 'small' }, '2FA is ', h('b', {}, 'enabled'), ' on your account.'),
        h('div', { class: 'row' },
          pw, code,
          h('button', {
            class: 'btn-danger', onclick: async () => {
              try {
                await post('/api/me/totp/disable', { password: pw.value, code: code.value });
                toast('2FA disabled'); location.reload();
              } catch (e) { toast(e.message, 'error'); }
            },
          }, 'Disable 2FA')));
      return card;
    }

    const pw = h('input', { type: 'password', placeholder: 'confirm your password', style: 'max-width:280px' });
    body.append(
      h('p', { class: 'small' }, '2FA is ', h('b', {}, 'off'), '. Enable it to require a 6-digit code at every sign-in.'),
      h('div', { class: 'row' },
        pw,
        h('button', {
          class: 'btn-acc', onclick: async () => {
            try {
              const res = await post('/api/me/totp/begin', { password: pw.value });
              showEnroll(res);
            } catch (e) { toast(e.message, 'error'); }
          },
        }, 'Set up 2FA')));

    function showEnroll(res) {
      const code = h('input', {
        type: 'text', maxlength: '6', placeholder: '000000',
        style: 'max-width:140px;text-align:center;font-family:var(--mono);letter-spacing:4px',
      });
      body.replaceChildren(
        h('ol', { class: 'small', style: 'padding-left:18px;line-height:2' },
          h('li', {}, 'In Microsoft Authenticator: ', h('b', {}, '+ Add account → Other account → Enter code manually'), '.'),
          h('li', {}, 'Account name: ', h('code', {}, 'BlockPanel (' + me.username + ')'), ' — Secret key:'),
        ),
        h('div', { class: 'secret' }, res.secret),
        h('p', { class: 'small muted' }, 'Or paste this URI into an app that accepts otpauth links: ',
          h('code', { style: 'word-break:break-all' }, res.uri)),
        h('div', { class: 'row', style: 'margin-top:10px' },
          code,
          h('button', {
            class: 'btn-acc', onclick: async () => {
              try {
                await post('/api/me/totp/confirm', { code: code.value });
                toast('2FA enabled'); location.reload();
              } catch (e) { toast(e.message, 'error'); }
            },
          }, 'Verify & enable')),
      );
      code.focus();
    }
    return card;
  }
}
