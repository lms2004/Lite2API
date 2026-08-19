/* Native account status controls.
   The stable account renderer keeps credentials redacted and uses the existing
   account PUT endpoint. This layer adds direct, reversible enable/disable
   actions for both Lite2API route connections and OAuth pool credentials. */
(() => {
  'use strict';

  const byId = id => document.getElementById(id);
  const later = fn => requestAnimationFrame(() => requestAnimationFrame(fn));

  function accountConfig(id) {
    return (state.config?.accounts || []).find(account => account.id === id);
  }

  function routeAccountPayload(account, enabled) {
    return {
      ...account,
      api_key: '',
      headers: { ...(account.headers || {}) },
      enabled: Boolean(enabled)
    };
  }

  async function setRouteAccountStatus(id, enabled, button) {
    const account = accountConfig(id);
    if (!account) {
      say('未找到该路由连接，请刷新后重试', true);
      return;
    }
    if (button) button.disabled = true;
    try {
      await api('/accounts', { method: 'PUT', body: JSON.stringify(routeAccountPayload(account, enabled)) });
      say(`${account.name || id} 已${enabled ? '启用' : '停用'}，新请求立即生效`);
      await load();
    } catch (error) {
      say(error.message || '账号状态更新失败', true);
    } finally {
      if (button) button.disabled = false;
    }
  }

  async function setOAuthAccountStatus(id, disabled, button) {
    const account = (state.oauth_accounts || []).find(item => item.id === id);
    if (!account) {
      say('未找到该认证账号，请刷新后重试', true);
      return;
    }
    if (button) button.disabled = true;
    try {
      await api('/oauth/accounts/status', {
        method: 'POST',
        body: JSON.stringify({ id, disabled: Boolean(disabled) })
      });
      say(`${account.identity || id} 已${disabled ? '停用' : '启用'}，认证池立即生效`);
      await load();
    } catch (error) {
      say(error.message || '认证账号状态更新失败', true);
    } finally {
      if (button) button.disabled = false;
    }
  }

  function routeAccountID(row) {
    const input = row.querySelector('input[type="checkbox"][onchange*="toggleAccount("]');
    const match = input?.getAttribute('onchange')?.match(/toggleAccount\('([^']+)'/);
    if (!match) return '';
    try {
      return decodeURIComponent(match[1]);
    } catch (_) {
      return match[1];
    }
  }

  function addRouteControl(row) {
    const id = routeAccountID(row);
    const account = id && accountConfig(id);
    const cell = row.children[3];
    if (!account || !cell) return;

    let button = cell.querySelector('.account-toggle');
    if (!button) {
      button = document.createElement('button');
      button.type = 'button';
      button.className = 'text-action account-toggle';
      button.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        setRouteAccountStatus(id, !account.enabled, button);
      });
      cell.append(button);
    }
    button.classList.toggle('enable', !account.enabled);
    button.textContent = account.enabled ? '停用' : '启用';
    button.title = account.enabled ? '停用此路由连接' : '启用此路由连接';
    button.setAttribute('aria-label', `${button.title}：${account.name || id}`);
    button.setAttribute('aria-pressed', String(Boolean(account.enabled)));
  }

  function syncRouteControls() {
    const tbody = byId('accountRows') || byId('accounts');
    if (!tbody) return;
    tbody.querySelectorAll(':scope > tr').forEach(addRouteControl);
  }

  function oauthAccountID(card) {
    const text = card.querySelector('.channel-account-id span')?.textContent || '';
    return text.split(' · ')[0].trim();
  }

  function addOAuthControl(card) {
    const id = oauthAccountID(card);
    const account = (state.oauth_accounts || []).find(item => item.id === id);
    const status = card.querySelector('.channel-account-status');
    if (!account || !status) return;

    let button = status.querySelector('.account-toggle');
    if (!button) {
      button = document.createElement('button');
      button.type = 'button';
      button.className = 'text-action account-toggle';
      button.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        setOAuthAccountStatus(id, !account.disabled, button);
      });
      status.append(button);
    }
    button.classList.toggle('enable', Boolean(account.disabled));
    button.textContent = account.disabled ? '启用' : '停用';
    button.title = account.disabled ? '启用此认证账号' : '停用此认证账号';
    button.setAttribute('aria-label', `${button.title}：${account.identity || id}`);
    button.setAttribute('aria-pressed', String(!account.disabled));
  }

  function syncOAuthControls() {
    byId('oauthAccounts')?.querySelectorAll('.channel-account').forEach(addOAuthControl);
  }

  function wrap(name, after) {
    const original = window[name];
    if (typeof original !== 'function' || original.__nativeAccountStatusWrapped) return;
    const wrapped = function (...args) {
      const result = original.apply(this, args);
      if (result && typeof result.finally === 'function') result.finally(() => later(after));
      else later(after);
      return result;
    };
    Object.defineProperty(wrapped, '__nativeAccountStatusWrapped', { value: true });
    window[name] = wrapped;
  }

  function init() {
    wrap('renderAccounts', syncRouteControls);
    wrap('renderOAuthAccounts', syncOAuthControls);
    syncRouteControls();
    syncOAuthControls();
  }

  window.Lite2APIAccountStatus = Object.freeze({ syncRouteControls, syncOAuthControls });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();
