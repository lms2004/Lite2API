/* Native account status controls.
   The stable account renderer keeps credentials redacted and uses the existing
   account PUT endpoint. This layer adds direct, reversible enable/disable,
   refresh, priority, pool strategy, and deletion controls. */
(() => {
  'use strict';

  const byId = id => document.getElementById(id);
  const later = fn => requestAnimationFrame(() => requestAnimationFrame(fn));
  let oauthRoutingAttempted = false;
  let oauthRoutingBusy = false;

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

  async function setOAuthAccountPriority(id, priority, button) {
    const account = (state.oauth_accounts || []).find(item => item.id === id);
    if (!account) {
      say('未找到该认证账号，请刷新后重试', true);
      return;
    }
    if (!Number.isInteger(priority) || priority < 0 || priority > 1000) {
      say('账号优先级必须是 0 到 1000 的整数', true);
      return;
    }
    if (button) button.disabled = true;
    try {
      await api('/oauth/accounts/priority', {
        method: 'POST',
        body: JSON.stringify({ id, priority })
      });
      say(`${account.identity || id} 的优先级已设为 ${priority}；数值越大越优先，失败后自动切换`);
      await load();
    } catch (error) {
      say(error.message || '账号优先级更新失败', true);
    } finally {
      if (button) button.disabled = false;
    }
  }

  function editOAuthAccountPriority(id, button) {
    const account = (state.oauth_accounts || []).find(item => item.id === id);
    if (!account) {
      say('未找到该认证账号，请刷新后重试', true);
      return;
    }
    const current = Number.isInteger(Number(account.priority)) ? Number(account.priority) : 0;
    const raw = window.prompt('设置认证账号优先级（0–1000）\n\n数值越大越优先；同优先级按上方选号策略分配；账号冷却或失败时自动切换。', String(current));
    if (raw === null) return;
    const trimmed = String(raw).trim();
    const priority = Number(trimmed);
    if (!/^\d+$/.test(trimmed) || !Number.isInteger(priority) || priority < 0 || priority > 1000) {
      say('请输入 0 到 1000 的整数', true);
      return;
    }
    setOAuthAccountPriority(id, priority, button);
  }

  function syncOAuthRouting(response) {
    const select = byId('oauthRoutingStrategy');
    const hint = byId('oauthRoutingHint');
    if (!select || !hint) return;
    const strategy = response?.strategy === 'fill-first' ? 'fill-first' : 'round-robin';
    select.value = strategy;
    select.disabled = false;
    hint.innerHTML = strategy === 'fill-first'
      ? '<strong>固定首选：</strong>先选数值最高的优先级；同级固定使用首个可用账号，额度、鉴权或上游故障时自动换号。'
      : '<strong>同级轮询：</strong>先选数值最高的优先级；同级账号轮询分配，会话粘滞仍可生效，故障时自动换号。';
  }

  async function loadOAuthRouting(force = false) {
    if (oauthRoutingBusy || (oauthRoutingAttempted && !force)) return;
    oauthRoutingAttempted = true;
    oauthRoutingBusy = true;
    const select = byId('oauthRoutingStrategy');
    try {
      const response = await api('/oauth/routing');
      syncOAuthRouting(response);
    } catch (error) {
      // A transient adapter/session failure must not permanently suppress every
      // later read. The next normal render may retry without a page reload.
      oauthRoutingAttempted = false;
      if (select) select.disabled = true;
      const hint = byId('oauthRoutingHint');
      if (hint) hint.textContent = `账号选择策略暂不可读：${error.message || '适配器未连接'}`;
    } finally {
      oauthRoutingBusy = false;
    }
  }

  async function setOAuthRoutingStrategy(strategy, select) {
    if (!['round-robin', 'fill-first'].includes(strategy)) return;
    if (select) select.disabled = true;
    try {
      const response = await api('/oauth/routing', {
        method: 'PUT',
        body: JSON.stringify({ strategy })
      });
      syncOAuthRouting(response);
      say(strategy === 'fill-first' ? '认证池已改为同级固定首选；故障切换仍保持开启' : '认证池已改为同级轮询；故障切换仍保持开启');
    } catch (error) {
      oauthRoutingAttempted = false;
      say(error.message || '账号选择策略更新失败', true);
      await loadOAuthRouting(true);
    } finally {
      if (select) select.disabled = false;
    }
  }

  async function deleteOAuthAccount(id, button) {
    const account = (state.oauth_accounts || []).find(item => item.id === id);
    if (!account) {
      say('未找到该认证账号，请刷新后重试', true);
      return;
    }
    const label = account.identity || id;
    if (!confirm(`确认删除认证账号“${label}”？\n\n删除后 OAuth 凭据将从认证池移除，无法恢复。`)) return;
    const card = button?.closest('.channel-account');
    const controls = card ? card.querySelectorAll('button') : [];
    controls.forEach(control => { control.disabled = true; });
    try {
      await api('/oauth/accounts', {
        method: 'DELETE',
        body: JSON.stringify({ id })
      });
      say(`${label} 已删除，认证池立即生效`);
      await load();
    } catch (error) {
      say(error.message || '认证账号删除失败', true);
    } finally {
      controls.forEach(control => { control.disabled = false; });
    }
  }

  async function refreshOAuthAccounts(id = '', button) {
    const account = id ? (state.oauth_accounts || []).find(item => item.id === id) : null;
    if (id && !account) {
      say('未找到该认证账号，请刷新后重试', true);
      return;
    }
    const card = button?.closest('.channel-account');
    const controls = card ? card.querySelectorAll('button') : [button || byId('oauthRefreshBtn')].filter(Boolean);
    controls.forEach(control => { control.disabled = true; });
    try {
      const response = await api('/oauth/accounts/refresh', {
        method: 'POST',
        body: JSON.stringify(id ? { id } : { all: true })
      });
      const refreshed = Number(response.refreshed || 0);
      const skipped = Number(response.skipped || 0);
      const failed = Array.isArray(response.failed) ? response.failed.length : 0;
      if (failed > 0) {
        say(`渠道刷新部分失败：${refreshed} 个成功 / ${failed} 个失败`, true);
      } else {
        say(`渠道刷新完成：${refreshed} 个账号已刷新，正在重新读取额度快照${skipped ? `，${skipped} 个已跳过` : ''}`);
      }
      await load();
    } catch (error) {
      say(error.message || '渠道刷新失败', true);
    } finally {
      controls.forEach(control => { control.disabled = false; });
    }
  }

  function routeAccountID(row) {
    if (row?.dataset?.uiKey) return row.dataset.uiKey;
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
    button.dataset.uiAction = 'toggle';
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
    if (card?.dataset?.uiKey) return card.dataset.uiKey;
    const text = card.querySelector('.channel-account-id span')?.textContent || '';
    return text.split(' · ')[0].trim();
  }

  function addOAuthControl(card) {
    const id = oauthAccountID(card);
    const account = (state.oauth_accounts || []).find(item => item.id === id);
    const status = card.querySelector('.channel-account-status');
    if (!account || !status) return;

    let priorityButton = status.querySelector('.account-priority');
    if (!priorityButton) {
      priorityButton = document.createElement('button');
      priorityButton.type = 'button';
      priorityButton.className = 'text-action account-priority';
      priorityButton.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        editOAuthAccountPriority(id, priorityButton);
      });
      status.append(priorityButton);
    }
    const priority = Number.isInteger(Number(account.priority)) ? Number(account.priority) : 0;
    priorityButton.dataset.uiAction = 'priority';
    priorityButton.textContent = `优先级 ${priority}`;
    priorityButton.title = '设置认证池选号优先级；数值越大越优先';
    priorityButton.setAttribute('aria-label', `${priorityButton.title}：${account.identity || id}，当前 ${priority}`);

    let refreshButton = status.querySelector('.account-refresh');
    if (!refreshButton) {
      refreshButton = document.createElement('button');
      refreshButton.type = 'button';
      refreshButton.className = 'text-action account-refresh';
      refreshButton.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        refreshOAuthAccounts(id, refreshButton);
      });
      status.append(refreshButton);
    }
    refreshButton.textContent = '刷新';
    refreshButton.dataset.uiAction = 'refresh';
    refreshButton.title = '刷新此认证账号的凭据状态并重新读取已观测额度';
    refreshButton.setAttribute('aria-label', `${refreshButton.title}：${account.identity || id}`);

    let button = status.querySelector('.account-toggle:not(.account-delete)');
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
    button.dataset.uiAction = 'toggle';
    button.textContent = account.disabled ? '启用' : '停用';
    button.title = account.disabled ? '启用此认证账号' : '停用此认证账号';
    button.setAttribute('aria-label', `${button.title}：${account.identity || id}`);
    button.setAttribute('aria-pressed', String(!account.disabled));

    let deleteButton = status.querySelector('.account-delete');
    if (!deleteButton) {
      deleteButton = document.createElement('button');
      deleteButton.type = 'button';
      deleteButton.className = 'text-action account-delete';
      deleteButton.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        deleteOAuthAccount(id, deleteButton);
      });
      status.append(deleteButton);
    }
    deleteButton.textContent = '删除';
    deleteButton.dataset.uiAction = 'delete';
    deleteButton.title = '删除此认证账号及其 OAuth 凭据';
    deleteButton.setAttribute('aria-label', `${deleteButton.title}：${account.identity || id}`);

    const detail = card.querySelector('.channel-account-detail');
    if (detail) {
      let priorityFact = detail.querySelector('.account-priority-fact');
      if (!priorityFact) {
        priorityFact = document.createElement('div');
        priorityFact.className = 'account-fact account-priority-fact';
        detail.append(priorityFact);
      }
      priorityFact.innerHTML = `<span>选号优先级</span><strong>${priority} · 数值越大越优先</strong>`;

      let recentFact = detail.querySelector('.account-recent-fact');
      if (!recentFact) {
        recentFact = document.createElement('div');
        recentFact.className = 'account-fact account-recent-fact';
        detail.append(recentFact);
      }
      recentFact.innerHTML = `<span>最近约 200 分钟</span><strong>${Number(account.recent_success || 0).toLocaleString('zh-CN')} 成功 / ${Number(account.recent_failed || 0).toLocaleString('zh-CN')} 失败</strong>`;
    }
  }

  function syncOAuthControls() {
    byId('oauthAccounts')?.querySelectorAll('.channel-account').forEach(addOAuthControl);
    if (typeof activeViewName === 'undefined' || activeViewName === 'accounts') loadOAuthRouting();
  }

  function installOAuthObserver() {
    const list = byId('oauthAccounts');
    if (!list || list.dataset.accountStatusObserved === '1') return;
    list.dataset.accountStatusObserved = '1';
    let scheduled = false;
    new MutationObserver(() => {
      if (scheduled) return;
      scheduled = true;
      later(() => {
        scheduled = false;
        syncOAuthControls();
      });
    }).observe(list, { childList: true, subtree: true });
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
    wrap('showView', () => {
      syncOAuthControls();
      if (typeof activeViewName === 'undefined' || activeViewName === 'accounts') loadOAuthRouting();
    });
    installOAuthObserver();
    syncRouteControls();
    syncOAuthControls();
  }

  window.refreshOAuthAccounts = refreshOAuthAccounts;
  window.setOAuthRoutingStrategy = setOAuthRoutingStrategy;
  window.Lite2APIAccountStatus = Object.freeze({ syncRouteControls, syncOAuthControls, refreshOAuthAccounts, setOAuthAccountPriority, setOAuthRoutingStrategy, loadOAuthRouting });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();
