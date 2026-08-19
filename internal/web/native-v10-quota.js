/* Native v10 quota loader — the usage page must not depend on visiting Accounts first. */
(() => {
  'use strict';

  let busy = false;
  let loadedAt = 0;

  async function loadUsageQuota(force = false) {
    if (busy) return;
    if (!force && Date.now() - loadedAt < 60000) return;
    if (typeof activeViewName !== 'undefined' && activeViewName !== 'monitor') return;
    busy = true;
    try {
      const response = await api('/oauth/accounts');
      state.oauth_accounts = response.data || [];
      state.oauth_error = response.warning || '';
    } catch (error) {
      state.oauth_accounts = state.oauth_accounts || [];
      state.oauth_error = error.message || '认证适配器当前不可读';
    } finally {
      loadedAt = Date.now();
      busy = false;
      window.Lite2APINativeV10?.sync?.();
    }
  }

  function wrap(name) {
    const original = window[name];
    if (typeof original !== 'function' || original.__nativeV10QuotaWrapped) return;
    const wrapped = function (...args) {
      const result = original.apply(this, args);
      const done = () => void loadUsageQuota(false);
      if (result && typeof result.finally === 'function') result.finally(done);
      else done();
      return result;
    };
    Object.defineProperty(wrapped, '__nativeV10QuotaWrapped', { value: true });
    window[name] = wrapped;
  }

  function init() {
    wrap('showView');
    wrap('load');
    void loadUsageQuota(true);
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden) void loadUsageQuota(false);
    });
  }

  window.Lite2APINativeV10Quota = Object.freeze({ load: loadUsageQuota });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();
