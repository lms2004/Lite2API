/* Lite2API theme preference. The early bootstrap in index.html prevents a
   light/dark flash; this layer owns the control, persistence, and system-mode
   changes after the page has loaded. */
(() => {
  'use strict';

  const STORAGE_KEY = 'lite2api_theme_mode';
  const MODES = new Set(['system', 'light', 'dark']);
  const root = document.documentElement;
  const media = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

  function normalize(mode) {
    return MODES.has(mode) ? mode : 'system';
  }

  function resolvedTheme(mode) {
    if (mode === 'dark') return 'dark';
    if (mode === 'light') return 'light';
    return media?.matches ? 'dark' : 'light';
  }

  function storedMode() {
    try {
      return normalize(localStorage.getItem(STORAGE_KEY));
    } catch (_) {
      return 'system';
    }
  }

  function updateThemeColor(resolved) {
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', resolved === 'dark' ? '#171719' : '#f4f4f6');
  }

  function applyTheme(mode, persist) {
    const next = normalize(mode);
    const resolved = resolvedTheme(next);
    root.dataset.theme = next;
    root.dataset.themeResolved = resolved;
    root.style.colorScheme = resolved;
    const select = document.getElementById('themeMode');
    if (select && select.value !== next) select.value = next;
    updateThemeColor(resolved);
    if (persist) {
      try { localStorage.setItem(STORAGE_KEY, next); } catch (_) {}
    }
  }

  window.setThemeMode = mode => applyTheme(mode, true);
  applyTheme(root.dataset.theme || storedMode(), false);

  if (media) {
    const handleSystemChange = () => {
      if (root.dataset.theme === 'system') applyTheme('system', false);
    };
    if (typeof media.addEventListener === 'function') media.addEventListener('change', handleSystemChange);
    else if (typeof media.addListener === 'function') media.addListener(handleSystemChange);
  }
})();
