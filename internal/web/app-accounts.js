/* Accounts controller. Dependencies are supplied by the composition root. */
globalThis.Lite2APIAccounts = function createAccounts(context) {
  'use strict';
  const {
    UI,
    accountRouteUsage,
    all,
    Runtime,
    startOAuth,
    openManualAccount,
    openChannelChat,
    openRouteCreate,
    testExistingConnection,
    configuredAccount,
    deleteConnection,
    $,
    resourcesState,
    accountsState,
    Core,
    number,
    accountStatus,
    setHTML,
    quotaWindows,
    quotaPercentage,
    providerKey,
    escapeHTML,
    providerIconHTML,
    PROVIDER_LABEL,
    quotaWindowHTML,
    relativeTime,
    connectionStats,
    connectionProviderKey,
    modelLabelHTML,
    request,
    toast,
    refreshAll,
    one,
  } = context;
  let routingSaving = false;
  function oauthErrorPresentation(raw) {
    const text = String(raw || ''),
      lower = text.toLowerCase();
    if (/bann?ed|blocked|too many failed|rate.?limit/.test(lower))
      return {
        title: '认证服务暂时限制访问',
        message: '认证适配器因连续失败进入保护状态。请稍后重新读取；API 连接、路由与客户端功能仍可使用。',
      };
    if (/timeout|timed out|请求超时/.test(lower))
      return {
        title: '认证服务响应超时',
        message: '认证账号和额度本次未能读取。系统会继续自动重试，也可以立即重新读取。',
      };
    if (/refused|unavailable|not reachable|connection reset|无法连接/.test(lower))
      return {
        title: '认证服务暂时不可用',
        message: '认证账号和额度暂时不可读。其他控制台功能仍可继续使用。',
      };
    return {
      title: '认证账号暂时不可读',
      message: '认证服务返回异常。其他控制台功能仍可继续使用，稍后可重新读取。',
    };
  }

  function renderOAuthServiceState() {
    const state = $('oauthServiceState');
    state.hidden = !resourcesState.oauthError;
    if (!resourcesState.oauthError) return;
    const presentation = oauthErrorPresentation(resourcesState.oauthError);
    $('oauthErrorTitle').textContent = presentation.title;
    $('oauthErrorMessage').textContent = presentation.message;
    $('oauthErrorTechnical').textContent = resourcesState.oauthError;
  }

  function renderAccounts() {
    renderSourceOverview();
    renderOAuthServiceState();
    renderOAuthRouting();
    renderAuthAccounts();
    renderConnections();
  }

  function renderSourceOverview() {
    const connections = resourcesState.data?.config?.accounts || [],
      routes = resourcesState.data?.config?.routes || {},
      usedConnections = connections.filter((account) => accountRouteUsage(account.id).length).length,
      accountSummary = resourcesState.oauthError
        ? '<span><strong>—</strong> 认证账号暂时不可读</span>'
        : `<span><strong>${number(resourcesState.oauth.filter((account) => accountStatus(account).label === '可用').length)}</strong> / ${number(resourcesState.oauth.length)} 个认证账号已实测可用</span>`;
    setHTML(
      $('sourceOverview'),
      `${accountSummary}<i></i><span><strong>${number(usedConnections)}</strong> / ${number(connections.length)} 条连接已进入路由</span><i></i><span><strong>${number(Object.keys(routes).length)}</strong> 个对外模型名</span>`,
    );
  }

  function renderOAuthRouting() {
    const select = $('oauthRoutingStrategy'),
      routing = resourcesState.oauthRouting;
    if (routingSaving) return;
    if (!routing) {
      select.disabled = true;
      $('oauthRoutingHint').textContent = resourcesState.oauthError
        ? '认证适配器不可读，暂时无法管理池内选号。'
        : '正在读取认证池选号策略…';
      return;
    }
    select.disabled = false;
    select.value = routing.strategy === 'fill-first' ? 'fill-first' : 'round-robin';
    $('oauthRoutingHint').textContent =
      select.value === 'fill-first'
        ? '先选择账号池数值更大的账号；同值时固定使用首个可用账号，失败或冷却后换号。'
        : '先选择账号池数值更大的账号；同值时轮询分配，失败或冷却后自动换号。';
  }

  function renderAuthAccounts() {
    const search = $('authAccountSearch').value.trim().toLowerCase(),
      statusFilter = $('authAccountStatus').value,
      rows = resourcesState.oauth.filter((account) => {
        const status = accountStatus(account),
          text = [account.identity, account.provider, account.plan, account.id].join(' ').toLowerCase(),
          quotaWarn = quotaWindows(account).some((window) => {
            const value = quotaPercentage(window);
            return value !== null && value >= 82;
          });
        return (
          (!search || text.includes(search)) &&
          (!statusFilter ||
            (statusFilter === 'ready' && status.label === '可用') ||
            (statusFilter === 'attention' && status.tone) ||
            (statusFilter === 'quota' && quotaWarn))
        );
      });
    $('authAccountToolbar').hidden = resourcesState.oauth.length < 8;
    if (!rows.length) {
      UI.patchHTML(
        $('authAccounts'),
        `<div class="account-list-empty">${resourcesState.oauthError ? '认证账号数据暂时不可用' : '没有匹配的认证账号'}</div>`,
      );
      return;
    }
    UI.patchHTML(
      $('authAccounts'),
      rows
        .map((account) => {
          const key = providerKey(account.provider),
            status = accountStatus(account),
            windows = quotaWindows(account),
            priority = Number.isFinite(Number(account.priority)) ? Number(account.priority) : 0;
          return `<article class="auth-account" data-oauth-id="${escapeHTML(account.id)}" data-ui-key="account-${escapeHTML(account.id)}"><div class="quota-identity">${providerIconHTML(key)}<div><strong>${escapeHTML(account.identity || '已保存凭据')}</strong><span>${escapeHTML(PROVIDER_LABEL[key] || account.provider)} · ${escapeHTML(account.plan || '套餐未知')}</span></div></div><div class="quota-windows">${windows.length ? windows.slice(0, 4).map(quotaWindowHTML).join('') : '<div class="quota-unknown">暂无真实额度观测</div>'}</div><div class="numeric">${number(account.success || 0)}<br><small>${number(account.failed || 0)} 失败</small></div><div><span class="status-pill ${status.tone}">${status.label}</span><small>${account.updated_at ? relativeTime(account.updated_at) : '未更新'}</small></div><details class="compact-row-actions"><summary>管理</summary><div class="oauth-controls"><label>账号池选择值 <span>大值优先</span><input data-oauth-priority type="number" min="0" max="1000" value="${priority}"></label><button data-oauth-action="priority" type="button">保存</button><div class="oauth-actions"><button data-oauth-action="refresh" type="button">刷新</button><button data-oauth-action="toggle" type="button">${account.disabled ? '启用' : '停用'}</button><button data-oauth-action="delete" class="danger" type="button">移除</button></div></div></details></article>`;
        })
        .join(''),
    );
  }

  function renderConnections() {
    const search = $('connectionSearch').value.trim().toLowerCase(),
      status = $('connectionStatus').value,
      allRows = connectionStats(),
      rows = allRows.filter((item) => {
        const text = [
            item.id,
            item.name,
            item.config.base_url,
            ...(item.config.models || []),
            ...Core.logicalModels([item.config]),
          ]
            .join(' ')
            .toLowerCase(),
          configuredEnabled = item.config.enabled !== false;
        return (
          (!search || text.includes(search)) &&
          (!status || (status === 'enabled' ? configuredEnabled : !configuredEnabled))
        );
      });
    $('connectionToolbar').hidden = allRows.length < 8;
    UI.patchHTML(
      $('connectionRows'),
      rows
        .map((item) => {
          const key = connectionProviderKey(item.config),
            enabled = item.config.enabled !== false && !item.live.circuit_open_until,
            direct = Core.directModels(item.config),
            logical = Core.logicalModels([item.config]),
            models = Core.uniqueStrings([...logical, ...direct]),
            usage = accountRouteUsage(item.id),
            chatAvailable = item.config.enabled !== false && Core.channelChatSupported(item.config),
            preview = models.slice(0, 6),
            catalog = preview.length
              ? `<div class="model-chip-list">${preview.map((model) => modelLabelHTML(model)).join('')}${models.length > preview.length ? `<span class="model-chip-more">+${models.length - preview.length}</span>` : ''}</div>`
              : '<span class="model-list">能力未知</span>';
          return `<tr data-ui-key="connection-${escapeHTML(item.id)}"><td data-label="连接"><div class="quota-identity">${providerIconHTML(key)}<div><strong>${escapeHTML(item.name)}</strong><span>${escapeHTML(item.id)}</span></div></div></td><td data-label="地址 / 协议"><div class="cell-main"><strong>${escapeHTML(item.config.type === 'anthropic' ? 'Anthropic' : 'OpenAI 兼容')}</strong><span class="base-url" title="${escapeHTML(item.config.base_url)}">${escapeHTML(item.config.base_url)}</span></div></td><td data-label="模型能力" title="${escapeHTML(models.join(', '))}">${catalog}<small class="model-catalog-meta">${logical.length ? `${logical.length} 个能力映射 · ` : ''}${direct.length ? `${direct.length} 个直连模型` : '直连目录未知'}</small></td><td data-label="路由使用"><div class="route-usage">${
            usage.length
              ? usage
                  .slice(0, 4)
                  .map((alias) => `<span>${escapeHTML(alias)}</span>`)
                  .join('')
              : '<small>尚未使用</small>'
          }</div></td><td data-label="调用" class="numeric">${number(item.live.total_requests || item.samples)}<br><small>${number(item.live.consecutive_failures || item.failures)} 连续失败</small></td><td data-label="速度" class="numeric">${Number.isFinite(item.avg) ? number(Math.round(item.avg)) + ' ms' : '—'}<br><small>P95 ${item.p95 === null ? '—' : number(Math.round(item.p95)) + ' ms'}</small></td><td data-label="状态"><span class="status-pill ${enabled ? '' : 'bad'}">${enabled ? '已启用' : item.live.circuit_open_until ? '熔断中' : '已停用'}</span></td><td data-label="操作"><div class="row-actions"><button type="button" data-action="connection-chat" data-id="${escapeHTML(item.id)}" ${chatAvailable ? '' : 'disabled'}>聊天</button><button type="button" data-action="connection-route" data-id="${escapeHTML(item.id)}">建路由</button><details class="compact-row-actions"><summary>更多</summary><div><button type="button" data-action="connection-test" data-id="${escapeHTML(item.id)}">连接测试</button><button type="button" data-action="connection-edit" data-id="${escapeHTML(item.id)}">编辑</button><button type="button" data-action="connection-delete" data-id="${escapeHTML(item.id)}" class="danger">删除</button></div></details></div></td></tr>`;
        })
        .join('') || '<tr><td colspan="8" class="empty-copy">没有匹配的 API 连接</td></tr>',
    );
  }

  function setAccountTab(tab) {
    accountsState.accountTab = tab;
    $('authAccountPane').hidden = false;
    $('connectionPane').hidden = false;
    const pane = $(tab === 'connections' ? 'connectionPane' : 'authAccountPane');
    const heading = pane.querySelector('h2');
    heading.tabIndex = -1;
    heading.focus({ preventScroll: true });
    pane.scrollIntoView({
      block: 'start',
      behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth',
    });
  }

  async function setOAuthRoutingStrategy(strategy) {
    if (routingSaving) return;
    routingSaving = true;
    const select = $('oauthRoutingStrategy');
    select.disabled = true;
    try {
      resourcesState.oauthRouting = await request('/oauth/routing', {
        method: 'PUT',
        body: JSON.stringify({ strategy }),
      });
      toast('认证池选号策略已更新');
    } catch (error) {
      toast(error.message, true);
    } finally {
      routingSaving = false;
      renderOAuthRouting();
    }
  }

  async function refreshOAuthAccount(id = '', button = null) {
    if (button) button.disabled = true;
    try {
      const result = await request('/oauth/accounts/refresh', {
        method: 'POST',
        body: JSON.stringify({ id }),
      });
      toast(
        result.ok === false ? '部分认证账号刷新失败' : `已刷新 ${result.refreshed || 0} 个认证账号`,
        result.ok === false,
      );
      const menu = button?.closest('details');
      if (menu) menu.open = false;
      await refreshAll(true, true);
    } catch (error) {
      toast(error.message, true);
    } finally {
      if (button) button.disabled = false;
    }
  }

  async function retryOAuthAccounts() {
    const button = $('retryOAuthAccounts'),
      label = button.textContent;
    button.disabled = true;
    button.textContent = '正在读取…';
    try {
      await refreshAll(true, true);
      if (!resourcesState.oauthError) toast('认证账号已恢复');
    } finally {
      button.disabled = false;
      button.textContent = label;
    }
  }

  async function handleOAuthAction(button) {
    const card = button.closest('[data-oauth-id]'),
      id = card?.dataset.oauthId,
      account = resourcesState.oauth.find((item) => item.id === id);
    if (!account) return;
    button.disabled = true;
    try {
      if (button.dataset.oauthAction === 'priority') {
        const value = Number(one('[data-oauth-priority]', card)?.value);
        if (!Number.isInteger(value) || value < 0 || value > 1000)
          throw new Error('账号池选择值必须是 0–1000 的整数');
        await request('/oauth/accounts/priority', {
          method: 'POST',
          body: JSON.stringify({ id, priority: value }),
        });
        toast(`账号池选择值已设为 ${value}；大值优先`);
      } else if (button.dataset.oauthAction === 'refresh') {
        await refreshOAuthAccount(id, button);
        return;
      } else if (button.dataset.oauthAction === 'toggle') {
        await request('/oauth/accounts/status', {
          method: 'POST',
          body: JSON.stringify({ id, disabled: !account.disabled }),
        });
        toast(account.disabled ? '认证账号已启用' : '认证账号已停用');
      } else if (button.dataset.oauthAction === 'delete') {
        if (
          !(await UI.confirm({
            title: '移除认证账号',
            message: `将“${account.identity || id}”移出认证池。API 连接和其他账号会保留。`,
            confirmLabel: '移除账号',
            destructive: true,
          }))
        )
          return;
        await request('/oauth/accounts', { method: 'DELETE', body: JSON.stringify({ id }) });
        toast('认证账号已从账号池移除');
      }
      const menu = button.closest('details');
      if (menu) menu.open = false;
      await refreshAll(true, true);
    } catch (error) {
      toast(error.message, true);
    } finally {
      button.disabled = false;
    }
  }

  function bind() {
    all('[data-account-tab]').forEach((button) =>
      button.addEventListener('click', () => setAccountTab(button.dataset.accountTab)),
    );
    all('[data-quick-oauth]').forEach((button) =>
      button.addEventListener('click', () => startOAuth(button.dataset.quickOauth)),
    );
    all('[data-quick-manual]').forEach((button) =>
      button.addEventListener('click', () => openManualAccount(button.dataset.quickManual)),
    );
    $('authAccountSearch').addEventListener('input', Runtime.debounce(renderAuthAccounts));
    $('authAccountStatus').addEventListener('change', renderAuthAccounts);
    $('authAccounts').addEventListener('click', (event) => {
      const button = event.target.closest('[data-oauth-action]');
      if (button) UI.runAction(button, () => handleOAuthAction(button));
    });
    $('retryOAuthAccounts').addEventListener('click', retryOAuthAccounts);
    $('oauthRoutingStrategy').addEventListener('change', (event) =>
      setOAuthRoutingStrategy(event.target.value),
    );
    $('refreshOAuthAccounts').addEventListener('click', (event) =>
      refreshOAuthAccount('', event.currentTarget),
    );
    $('connectionSearch').addEventListener('input', Runtime.debounce(renderConnections));
    $('connectionStatus').addEventListener('change', renderConnections);
    $('connectionRows').addEventListener('click', (event) => {
      const button = event.target.closest('[data-action]');
      if (!button) return;
      if (button.dataset.action === 'connection-chat') openChannelChat(button.dataset.id);
      if (button.dataset.action === 'connection-route') openRouteCreate(button.dataset.id);
      if (button.dataset.action === 'connection-test') {
        UI.runAction(button, () => testExistingConnection(button.dataset.id), '测试中…');
      }
      if (button.dataset.action === 'connection-edit')
        openManualAccount(
          providerKey(
            configuredAccount(button.dataset.id)?.name,
            configuredAccount(button.dataset.id)?.base_url,
          ),
          button.dataset.id,
        );
      if (button.dataset.action === 'connection-delete')
        UI.runAction(button, () => deleteConnection(button.dataset.id));
    });
  }

  return Object.freeze({ bind, renderAccounts, setAccountTab, renderConnections });
};
