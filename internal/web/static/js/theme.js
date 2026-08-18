// Theme bootstrap. Loaded as a classic (non-module) script in <head> so the
// chosen theme is on <html> before first paint — no flash of the wrong theme.
// Preferences live in localStorage only; they are per-browser, not per-account.
(function () {
  'use strict';

  var mqLight = window.matchMedia ? window.matchMedia('(prefers-color-scheme: light)') : null;

  function stored(key, dflt) {
    try { return localStorage.getItem('bp-' + key) || dflt; } catch (e) { return dflt; }
  }

  function apply() {
    var theme = stored('theme', 'auto');
    if (theme === 'auto') theme = (mqLight && mqLight.matches) ? 'light' : 'dark';
    var root = document.documentElement;
    root.setAttribute('data-theme', theme);
    root.setAttribute('data-accent', stored('accent', 'green'));
    root.setAttribute('data-density', stored('density', 'comfortable'));
  }

  window.BPTheme = {
    apply: apply,
    get: function (key, dflt) { return stored(key, dflt); },
    set: function (key, value) {
      try { localStorage.setItem('bp-' + key, value); } catch (e) { /* private mode */ }
      apply();
    },
  };

  if (mqLight && mqLight.addEventListener) mqLight.addEventListener('change', apply);
  apply();
})();
