/* Routes controller. Dependencies are supplied by the composition root. */
globalThis.Lite2APIRoutes = function createRoutes(context) {
  'use strict';
  const {
    UI,
    Runtime,
    resourcesState,
    Core,
    configuredAccounts,
    $,
    setHTML,
    configuredAccount,
    escapeHTML,
    modelIconHTML,
    one,
    toast,
    accountStatus,
    modelLabelHTML,
    connectionProviderKey,
    providerIconHTML,
    openDialog,
    closeDialog,
    showView,
    request,
    refreshAll,
  } = context;
  const routesState = {
    routeDraft: {},
    selectedRoute: '',
    routeBaseline: {},
    routeServerFingerprint: '',
    routeConflict: false,
    routesDirty: false,
    routeCreateAliasTouched: false,
    routePickerAliases: new Set(),
  };
  let saving = false;

  function receiveSnapshot(serverRoutes = {}) {
    const fingerprint = Core.routeFingerprint(serverRoutes);
    if (!routesState.routesDirty && fingerprint !== routesState.routeServerFingerprint) {
      routesState.routeDraft = structuredClone(serverRoutes);
      routesState.routeBaseline = structuredClone(serverRoutes);
      routesState.routeServerFingerprint = fingerprint;
      routesState.routeConflict = false;
    } else if (routesState.routesDirty && fingerprint !== routesState.routeServerFingerprint)
      routesState.routeConflict = true;
  }

  function hasUnsavedChanges() {
    return routesState.routesDirty || saving;
  }
  function routeAliases() {
    return Object.keys(routesState.routeDraft || {}).sort();
  }

  function normalizedRoute(alias) {
    return Core.normalizeRoute(routesState.routeDraft[alias] || {});
  }

  function logicalModels() {
    return Core.logicalModels(configuredAccounts());
  }

  function reasoningOptions(model) {
    const values = new Set(
      configuredAccounts()
        .flatMap((account) =>
          (account.capabilities || [])
            .filter((capability) => capability.model === model)
            .flatMap((capability) => capability.reasoning_efforts || []),
        )
        .map((value) => String(value).toLowerCase()),
    );
    const order = ['auto', 'none', 'minimal', 'low', 'medium', 'high', 'max', 'xhigh', 'ultra'];
    return order.filter((value) => values.has(value));
  }

  function routeHealth(alias) {
    const route = routesState.routeDraft[alias],
      baseline = routesState.routeBaseline[alias];
    if (
      !baseline ||
      Core.routeFingerprint(Core.normalizeRoute(route)) !==
        Core.routeFingerprint(Core.normalizeRoute(baseline))
    )
      return { state: 'draft', reason: '尚未保存的草稿' };
    return (
      (resourcesState.data?.operations?.routes || []).find((item) => item.alias === alias) || {
        state: 'unknown',
        reason: '尚无真实请求样本',
      }
    );
  }

  function routeHealthTone(state) {
    return state === 'ready'
      ? ''
      : state === 'degraded' || state === 'draft'
        ? 'warn'
        : state === 'unavailable'
          ? 'bad'
          : 'unknown';
  }

  function strategyHelp(strategy) {
    if (strategy === 'least_loaded') return '动态选择当前负载最低的可用连接；同负载时连接排序值更小者优先。';
    if (strategy === 'round_robin') return '每次请求轮换首选目标；目标失败后仍会继续尝试其他可用目标。';
    if (strategy === 'priority') return '按连接排序值从小到大选择；同值时保持你编排的目标顺序。';
    if (strategy === 'sticky') return '有稳定会话标识时保持同一目标；无会话标识时按最少负载选择。';
    return '严格保持下面的目标顺序：第 1 顺位先试，失败后才进入下一顺位。';
  }

  function routeValidationFor(alias) {
    return Core.routeValidation({ [alias]: routesState.routeDraft[alias] }, configuredAccounts());
  }

  function renderRouteChangeState(validation) {
    const changes = Core.routeImpact(routesState.routeBaseline, routesState.routeDraft),
      status = $('routeStatus'),
      bar = $('routeChangeBar');
    const errorCount = validation.errors.length;
    status.className =
      'edit-status' +
      (routesState.routeConflict || errorCount ? ' bad' : routesState.routesDirty ? ' dirty' : '');
    status.textContent = routesState.routeConflict
      ? '服务器配置已变化'
      : errorCount
        ? `${errorCount} 项待修复`
        : routesState.routesDirty
          ? `${changes.length} 项未保存`
          : '已同步';
    status.hidden = !routesState.routesDirty && !routesState.routeConflict && !errorCount;
    $('discardRoutesButton').disabled = saving || (!routesState.routesDirty && !routesState.routeConflict);
    $('discardRoutesButton').hidden = !routesState.routesDirty && !routesState.routeConflict;
    $('saveRoutesButton').disabled = saving || !routesState.routesDirty || routesState.routeConflict;
    $('saveRoutesButton').textContent = saving ? '保存中…' : '保存修改';
    $('saveRoutesButton').setAttribute('aria-busy', String(saving));
    $('saveRoutesButton').hidden = !routesState.routesDirty;
    bar.hidden = !routesState.routesDirty && !routesState.routeConflict;
    $('reloadRoutesButton').hidden = !routesState.routeConflict;
    $('reloadRoutesButton').disabled = saving;
    const summary = changes
      .slice(0, 4)
      .map((change) => change.message)
      .join('、');
    $('routeChangeSummary').textContent = routesState.routeConflict
      ? '服务器端路由已被其他操作更新。为避免覆盖，请重新载入后再编辑。'
      : summary + (changes.length > 4 ? `，另有 ${changes.length - 4} 项` : '');
  }

  function validateAndRenderRoutes(focusFirst = false) {
    const validation = Core.routeValidation(routesState.routeDraft, configuredAccounts());
    if (focusFirst && validation.errors[0]?.alias in routesState.routeDraft)
      routesState.selectedRoute = validation.errors[0].alias;
    renderRoutes(validation);
    return validation;
  }

  function renderRoutes(validation = Core.routeValidation(routesState.routeDraft, configuredAccounts())) {
    const aliases = routeAliases();
    if (!routesState.selectedRoute || !routesState.routeDraft[routesState.selectedRoute])
      routesState.selectedRoute = aliases[0] || '';
    const errorsByAlias = new Set(validation.errors.map((item) => item.alias));
    const warningsByAlias = new Set(validation.warnings.map((item) => item.alias));
    const logicalModelCounts = new Map();
    aliases.forEach((alias) => {
      const route = normalizedRoute(alias);
      if (route.model) logicalModelCounts.set(route.model, (logicalModelCounts.get(route.model) || 0) + 1);
    });
    UI.patchHTML(
      $('routeList'),
      aliases
        .filter((alias) => {
          const query = $('routeSearch').value.trim().toLowerCase();
          return (
            !query || [alias, routesState.routeDraft[alias]?.model].join(' ').toLowerCase().includes(query)
          );
        })
        .map((alias) => {
          const route = normalizedRoute(alias),
            health = routeHealth(alias),
            state = errorsByAlias.has(alias)
              ? 'unavailable'
              : warningsByAlias.has(alias)
                ? 'degraded'
                : health.state;
          const firstTarget = route.targets[0],
            firstModel = firstTarget?.model || '',
            firstAccount = firstTarget ? configuredAccount(firstTarget.account) : null,
            firstResolution =
              !route.legacy && firstTarget ? Core.targetResolution(firstAccount, route, firstTarget) : null,
            sameModelCount = route.model ? logicalModelCounts.get(route.model) || 1 : 1,
            modelReuse = sameModelCount > 1 ? ` · 同模型 ${sameModelCount} 条` : '',
            detail =
              (route.legacy
                ? route.upstream_model
                  ? route.upstream_model === alias
                    ? '旧式 · 同名上游'
                    : `旧式 · ${route.upstream_model}`
                  : '旧式 · 继承客户端别名'
                : route.model
                  ? route.model === alias
                    ? `能力映射 · ${route.reasoning_effort}`
                    : `逻辑 ${route.model} · ${route.reasoning_effort}`
                  : firstModel
                    ? firstModel === alias
                      ? '同名上游直连'
                      : firstModel
                    : '尚未设置实际模型') + modelReuse,
            targetCount = route.all_accounts
              ? configuredAccounts().filter((account) => account.enabled !== false).length
              : route.targets.length,
            displayModel = route.legacy
              ? route.upstream_model || firstModel || alias
              : firstResolution?.ok
                ? firstResolution.upstream_model || firstModel || route.model
                : route.model || firstModel || alias;
          const selected = routesState.selectedRoute === alias,
            healthLabel =
              state === 'ready'
                ? '可用'
                : state === 'degraded'
                  ? '性能下降'
                  : state === 'draft'
                    ? '有未保存更改'
                    : state === 'unavailable'
                      ? '不可用'
                      : '状态未知';
          return `<button type="button" class="${selected ? 'active' : ''}" data-route-alias="${escapeHTML(alias)}" data-ui-key="route-${escapeHTML(alias)}" aria-pressed="${selected}" title="${escapeHTML(errorsByAlias.has(alias) ? '配置待修复' : health.reason || '')}"><span class="route-list-main">${modelIconHTML(displayModel, 'route-model-icon')}<span class="route-list-copy"><strong>${escapeHTML(alias)}</strong><span>${escapeHTML(detail)}</span></span></span><i class="route-list-state ${routeHealthTone(state)}" aria-hidden="true"></i><span aria-label="${targetCount} 个上游">${targetCount}</span><span class="sr-only">，${escapeHTML(healthLabel)}</span></button>`;
        })
        .join('') ||
        ($('routeSearch').value.trim()
          ? '<div class="empty-copy">没有匹配的路由</div>'
          : '<div class="empty-copy"><strong>还没有模型路由</strong><br>创建第一条路由，把客户端模型别名连接到真实上游。</div>'),
    );
    renderRouteEditor();
    renderRouteChangeState(validation);
  }

  function routeCandidateTarget(route, account, alias) {
    if (route.model) {
      const resolution = Core.capabilityResolution(account, route.model, route.reasoning_effort);
      return resolution
        ? {
            ok: true,
            target: { account: account.id, model: '', reasoning_effort: '' },
            label: resolution.upstream_model,
          }
        : { ok: false, target: null, label: `不支持 ${route.model} · ${route.reasoning_effort}` };
    }
    const declared = Core.directModels(account),
      preferred = route.targets.find((target) => target.model)?.model || alias;
    const model =
      (account?.models || []).includes('*') || declared.includes(preferred)
        ? preferred
        : declared[0] || preferred;
    const resolution = Core.directResolution(account, model);
    return {
      ok: resolution.ok,
      target: { account: account.id, model, reasoning_effort: '' },
      label: resolution.verified ? model : `${model} · 未声明目录`,
    };
  }

  function routeTargetToolbarHTML(alias, route) {
    const selected = new Set(route.targets.map((target) => target.account).filter(Boolean)),
      candidates = configuredAccounts().map((account) => ({
        account,
        candidate: routeCandidateTarget(route, account, alias),
      })),
      available = candidates.filter(
        (item) => item.account.enabled !== false && item.candidate.ok && !selected.has(item.account.id),
      );
    const rows =
      candidates
        .map(({ account, candidate }) => {
          const checked = selected.has(account.id),
            disabled = !checked && (account.enabled === false || !candidate.ok),
            detail = checked
              ? route.targets.find((target) => target.account === account.id)?.model || candidate.label
              : candidate.label;
          return `<label class="route-picker-option ${disabled ? 'disabled' : ''}" data-ui-key="pick-${escapeHTML(account.id)}"><input type="checkbox" data-route-account-toggle="${escapeHTML(account.id)}" ${checked ? 'checked' : ''} ${disabled ? 'disabled' : ''}><span><strong>${escapeHTML(account.name || account.id)}</strong><small>${escapeHTML(account.id)} · ${escapeHTML(detail)}${account.enabled === false ? ' · 已停用' : ''}</small></span></label>`;
        })
        .join('') || '<div class="empty-copy">还没有可选择的 API 连接</div>';
    const bulk =
      route.model && available.length
        ? `<button type="button" data-route-action="add-all-compatible" class="secondary">加入其余 ${available.length} 个兼容连接</button>`
        : '';
    return `<div class="route-target-toolbar" data-ui-key="target-toolbar"><div><strong>上游选择</strong><span>已选 ${selected.size} / ${candidates.length} 个连接；同一连接不会重复加入。</span></div><div class="route-target-tools">${bulk}<details class="route-target-picker" ${routesState.routePickerAliases.has(alias) ? 'open' : ''}><summary>选择上游</summary><div class="route-target-popover">${rows}</div></details></div></div>`;
  }

  function duplicateSelectedRoute() {
    const alias = routesState.selectedRoute;
    if (!alias || !routesState.routeDraft[alias]) return;
    const nextAlias = uniqueRouteAlias(`${alias}-copy`);
    routesState.routeDraft[nextAlias] = structuredClone(routesState.routeDraft[alias]);
    routesState.selectedRoute = nextAlias;
    routesState.routePickerAliases.delete(alias);
    markRoutesDirty();
    validateAndRenderRoutes();
    requestAnimationFrame(() => $('routeAliasInput')?.select());
    toast(`已复制为 ${nextAlias}；它与原路由可独立编辑`);
  }

  function toggleRouteAccount(accountID, checked) {
    const alias = routesState.selectedRoute,
      route = normalizedRoute(alias);
    if (!alias || route.legacy) return;
    if (checked) {
      if (route.targets.some((target) => target.account === accountID)) return;
      if (route.targets.length >= 64) {
        toast('一条路由最多 64 个目标', true);
        return;
      }
      const account = configuredAccount(accountID);
      if (!account) {
        toast('连接已不存在', true);
        return;
      }
      const candidate = routeCandidateTarget(route, account, alias);
      if (account.enabled === false || !candidate.ok) {
        toast(candidate.label || '该连接当前不可加入', true);
        return;
      }
      route.targets.push(candidate.target);
    } else route.targets = route.targets.filter((target) => target.account !== accountID);
    routesState.routeDraft[alias] = route;
    markRoutesDirty();
    validateAndRenderRoutes();
  }

  function addAllCompatibleTargets() {
    const alias = routesState.selectedRoute,
      route = normalizedRoute(alias);
    if (!alias || route.legacy || !route.model) return;
    const used = new Set(route.targets.map((target) => target.account)),
      targets = configuredAccounts()
        .filter((account) => account.enabled !== false && !used.has(account.id))
        .map((account) => routeCandidateTarget(route, account, alias))
        .filter((candidate) => candidate.ok)
        .map((candidate) => candidate.target);
    if (!targets.length) {
      toast('所有兼容连接都已加入');
      return;
    }
    if (route.targets.length + targets.length > 64) {
      toast('加入后会超过 64 个目标，请手动选择', true);
      return;
    }
    route.targets.push(...targets);
    routesState.routeDraft[alias] = route;
    markRoutesDirty();
    validateAndRenderRoutes();
    toast(`已加入 ${targets.length} 个兼容连接，可继续调整 fallback 顺序`);
  }

  function oauthProviderForConnection(account) {
    const text = [account?.instance_id, account?.id, account?.name].filter(Boolean).join(' ').toLowerCase();
    if (text.includes('antigravity')) return 'antigravity';
    if (/claude|anthropic/.test(text)) return 'anthropic';
    if (/gemini|google/.test(text)) return 'gemini';
    if (/codex|openai/.test(text)) return 'codex';
    return '';
  }

  function oauthCredentialsForConnection(account) {
    if (String(account?.adapter_id || '').toLowerCase() !== 'cli-proxy-api') return [];
    const provider = oauthProviderForConnection(account);
    return resourcesState.oauth.filter((credential) => !provider || credential.provider === provider);
  }

  function credentialDisplay(id) {
    const credential = resourcesState.oauth.find((item) => item.id === id);
    return credential?.identity || (id ? `账号 ${id}` : '自动账号池');
  }

  function routeTargetHTML(route, target, index, accounts) {
    const account = accounts.find((item) => item.id === target.account),
      resolution = target.account
        ? Core.targetResolution(account, route, target)
        : { ok: false, verified: false, reason: '尚未选择连接', upstream_model: '' };
    const disabled = account && account.enabled === false;
    const invalid = !resolution.ok,
      unverified = resolution.ok && (!resolution.verified || disabled);
    const stateClass = invalid ? 'invalid' : unverified ? 'unverified' : '';
    let reason = resolution.ok
      ? route.model
        ? `将解析为 ${resolution.upstream_model}`
        : resolution.verified
          ? '模型已由连接声明'
          : resolution.reason
      : resolution.reason;
    if (disabled) reason = `连接已停用；${reason}`;
    const missing =
      target.account && !account
        ? `<option value="${escapeHTML(target.account)}" selected>${escapeHTML(target.account)}（已不存在）</option>`
        : '';
    const selectedAccounts = new Set(route.targets.map((item) => item.account).filter(Boolean));
    const options = accounts
      .map((item) => {
        const pool = String(item.adapter_id || '').toLowerCase() === 'cli-proxy-api',
          duplicate = item.id !== target.account && selectedAccounts.has(item.id) && !pool;
        return `<option value="${escapeHTML(item.id)}" ${item.id === target.account ? 'selected' : ''} ${duplicate ? 'disabled' : ''}>${escapeHTML(item.name || item.id)}${item.enabled === false ? '（停用）' : ''}${duplicate ? '（已选择）' : ''}</option>`;
      })
      .join('');
    const credentialTarget = String(account?.adapter_id || '').toLowerCase() === 'cli-proxy-api',
      credentials = oauthCredentialsForConnection(account),
      credentialMissing =
        target.credential && !credentials.some((item) => item.id === target.credential)
          ? `<option value="${escapeHTML(target.credential)}" selected>${escapeHTML(credentialDisplay(target.credential))}（当前不可见）</option>`
          : '',
      credentialField = credentialTarget
        ? `<label data-ui-key="credential">认证账号<select data-target-field="credential"><option value="">自动账号池</option>${credentialMissing}${credentials
            .map((item) => {
              const status = accountStatus(item),
                selected = item.id === target.credential;
              return `<option value="${escapeHTML(item.id)}" ${selected ? 'selected' : ''} ${item.disabled ? 'disabled' : ''}>${escapeHTML(item.identity || item.id)} · ${escapeHTML(status.label)}</option>`;
            })
            .join(
              '',
            )}</select><span class="target-resolution ${target.credential ? '' : 'warn'}">${target.credential ? '只使用该账号；失败后进入下一目标' : '由认证池自动选号并在池内重试'}</span></label>`
        : '';
    const directModels = Core.directModels(account),
      listID = `targetModels${index}`;
    const modelField = route.model
      ? `<label data-ui-key="model">解析后的上游模型<div class="resolved-field ${invalid ? 'bad' : ''}">${resolution.upstream_model ? modelLabelHTML(resolution.upstream_model, 'resolved-model-label') : '无法解析'}</div></label>`
      : `<label data-ui-key="model">实际上游模型<input data-target-field="model" list="${listID}" value="${escapeHTML(target.model || '')}" placeholder="必填；可选择或手动填写"><datalist id="${listID}">${directModels.map((model) => `<option value="${escapeHTML(model)}"></option>`).join('')}</datalist></label>`;
    const effortField = route.model
      ? '<label data-ui-key="effort">推理强度<div class="resolved-field">继承路由设置</div></label>'
      : `<label data-ui-key="effort">目标推理强度<select data-target-field="reasoning_effort">${['', 'auto', 'none', 'minimal', 'low', 'medium', 'high', 'max', 'xhigh', 'ultra'].map((value) => `<option value="${value}" ${value === target.reasoning_effort ? 'selected' : ''}>${value || '不指定'}</option>`).join('')}</select></label>`;
    const explanation =
      invalid || unverified
        ? `<span class="target-resolution ${invalid ? 'bad' : 'warn'}">${escapeHTML(reason)}</span>`
        : '';
    return `<div class="target-row ${stateClass} ${credentialTarget ? 'credential-target' : ''}" data-target-index="${index}" data-ui-key="target-${index}"><span class="target-order" title="第 ${index + 1} 顺位">${index + 1}</span><label data-ui-key="connection">连接<select data-target-field="account"><option value="">选择连接</option>${missing}${options}</select>${explanation}</label>${credentialField}${modelField}${effortField}<div class="target-actions"><button type="button" data-target-action="up" aria-label="上移" title="上移" ${index === 0 ? 'disabled' : ''}>↑</button><button type="button" data-target-action="down" aria-label="下移" title="下移" ${index === route.targets.length - 1 ? 'disabled' : ''}>↓</button><button type="button" data-target-action="delete" class="danger" aria-label="移除" title="移除">×</button></div></div>`;
  }

  function renderRoutePreview(route) {
    const accounts = configuredAccounts();
    const dynamic = ['least_loaded', 'round_robin', 'sticky'].includes(route.strategy),
      targets =
        route.strategy === 'priority'
          ? route.targets
              .map((target, index) => ({ target, index }))
              .sort((left, right) => {
                const a = accounts.find((item) => item.id === left.target.account),
                  b = accounts.find((item) => item.id === right.target.account),
                  difference = (Number(a?.priority) || 0) - (Number(b?.priority) || 0);
                return difference || left.index - right.index;
              })
              .map((item) => item.target)
          : route.targets;
    const rows = targets
      .map((target, index) => {
        const account = accounts.find((item) => item.id === target.account),
          resolution = Core.targetResolution(account, route, target),
          key = connectionProviderKey(account || { id: target.account });
        const priority = route.strategy === 'priority' ? ` · 排序值 ${Number(account?.priority) || 0}` : '',
          credential =
            String(account?.adapter_id || '').toLowerCase() === 'cli-proxy-api'
              ? ` · ${credentialDisplay(target.credential)}`
              : '',
          effectiveModel = resolution.ok
            ? resolution.upstream_model || target.model || route.model
            : target.model || route.model,
          detail =
            (resolution.ok ? resolution.upstream_model || target.model : resolution.reason) +
            credential +
            priority;
        return `<li class="${resolution.ok ? '' : 'bad'}"><b>${dynamic ? '·' : index + 1}</b><span><strong class="route-preview-account">${providerIconHTML(key, 'provider-mini')}${escapeHTML(account?.name || target.account || '未选连接')}</strong><small class="route-preview-model">${modelIconHTML(effectiveModel, 'inline-model-icon')}${escapeHTML(detail)}</small></span></li>`;
      })
      .join('');
    const title = !route.strategy
      ? '实际故障链'
      : route.strategy === 'priority'
        ? '按连接排序值计算的链'
        : '动态候选目标';
    return `<details class="route-preview-disclosure" data-ui-key="request-preview"><summary>查看实际请求链</summary><div class="route-preview"><div><span class="eyebrow">Effective routing</span><h3>${title}</h3><p>${escapeHTML(strategyHelp(route.strategy))}</p></div><ol>${rows || '<li class="bad"><b>!</b><span><strong>尚无目标</strong><small>至少添加一个真实上游</small></span></li>'}</ol></div></details>`;
  }

  function renderLegacyRouteEditor(alias, route, validation) {
    const accounts = configuredAccounts(),
      targets = route.all_accounts
        ? accounts
            .filter((account) => account.enabled !== false)
            .map((account) => ({ account: account.id, model: route.upstream_model }))
        : route.targets;
    const errorHTML = validation.errors.length
      ? `<div class="route-editor-error" data-ui-key="editor-errors"><strong>当前旧式配置存在问题</strong><ul>${validation.errors.map((item) => `<li>${escapeHTML(item.message)}</li>`).join('')}</ul></div>`
      : '';
    const warningHTML = validation.warnings.length
      ? `<div class="route-editor-warning" data-ui-key="editor-warnings"><strong>需要注意</strong><ul>${validation.warnings.map((item) => `<li>${escapeHTML(item.message)}</li>`).join('')}</ul></div>`
      : '';
    const rows = targets
      .map((target, index) => {
        const account = accounts.find((item) => item.id === target.account),
          resolved = route.upstream_model || account?.model_map?.[alias] || alias,
          key = connectionProviderKey(account || { id: target.account });
        return `<li class="${account ? '' : 'bad'}"><b>${index + 1}</b><span><strong class="route-preview-account">${providerIconHTML(key, 'provider-mini')}${escapeHTML(account?.name || target.account)}</strong><small class="route-preview-model">${modelIconHTML(resolved, 'inline-model-icon')}${escapeHTML(account ? resolved : '连接已不存在')}</small></span></li>`;
      })
      .join('');
    UI.patchHTML(
      $('routeEditor'),
      `<div class="route-editor-head" data-ui-key="editor-heading"><div><span class="eyebrow">Preserved legacy route</span><h2>${escapeHTML(alias)}</h2><p>${route.all_accounts ? '所有已启用连接' : '固定连接列表'} · ${route.strategy || '最少负载（旧式默认）'}</p></div><button type="button" data-route-action="duplicate" class="secondary">复制路由</button><button type="button" data-route-action="delete" class="secondary danger">删除路由</button></div>${errorHTML}${warningHTML}<div class="legacy-route-card"><strong>这是一条仍受后端支持的旧式路由</strong><p>界面会原样保存，不会在编辑其他路由时偷偷改变它的调度语义。转换后才能使用新的逐目标模型、能力校验和可视化 fallback。</p><dl><div><dt>上游模型</dt><dd>${escapeHTML(route.upstream_model || '继承客户端别名或连接映射')}</dd></div><div><dt>目标范围</dt><dd>${route.all_accounts ? '所有当前及未来启用的连接' : `${route.targets.length} 个固定连接`}</dd></div><div><dt>调度策略</dt><dd>${escapeHTML(route.strategy || 'least_loaded（旧式默认）')}</dd></div></dl><button type="button" data-route-action="convert-legacy" class="primary">转换为显式目标草稿</button></div><div class="route-preview"><div><span class="eyebrow">Current behavior</span><h3>当前候选连接</h3><p>${route.all_accounts ? '转换会把当前启用连接固化为显式目标；以后新增连接不会自动加入。' : '转换会保留当前连接列表，并显式写入每个目标模型。'}</p></div><ol>${rows || '<li class="bad"><b>!</b><span><strong>当前没有候选连接</strong><small>先添加或启用连接</small></span></li>'}</ol></div>`,
    );
  }

  function renderRouteEditor() {
    const alias = routesState.selectedRoute;
    if (!alias) {
      UI.patchHTML(
        $('routeEditor'),
        '<div class="empty-copy"><strong>选择或创建一条路由</strong><br>这里会同时展示配置校验和保存后的实际请求链。</div>',
      );
      return;
    }
    const route = normalizedRoute(alias),
      accounts = configuredAccounts(),
      validation = routeValidationFor(alias);
    if (route.legacy) {
      renderLegacyRouteEditor(alias, route, validation);
      return;
    }
    const models = Core.uniqueStrings([route.model, ...logicalModels()]),
      availableEfforts = reasoningOptions(route.model),
      efforts = Core.uniqueStrings([route.reasoning_effort, ...availableEfforts]),
      logical = !!route.model;
    const errorHTML = validation.errors.length
      ? `<div class="route-editor-error" data-ui-key="editor-errors"><strong>保存前需要修复</strong><ul>${validation.errors.map((item) => `<li>${escapeHTML(item.message)}</li>`).join('')}</ul></div>`
      : '';
    const warningHTML = validation.warnings.length
      ? `<div class="route-editor-warning" data-ui-key="editor-warnings"><strong>需要你确认</strong><ul>${validation.warnings.map((item) => `<li>${escapeHTML(item.message)}</li>`).join('')}</ul></div>`
      : '';
    UI.patchHTML(
      $('routeEditor'),
      `<div class="route-editor-head" data-ui-key="editor-heading"><div><span class="eyebrow">Route</span><h2>${escapeHTML(alias)}</h2><p>${logical ? '能力映射' : '实际模型直连'} · ${route.targets.length} 个上游</p></div><button type="button" data-route-action="duplicate" class="secondary">复制路由</button><button type="button" data-route-action="delete" class="secondary danger">删除路由</button></div>${errorHTML}${warningHTML}<div class="route-intent route-intent-compact" data-ui-key="route-intent"><label>对外模型名<input id="routeAliasInput" value="${escapeHTML(alias)}" maxlength="256" required></label><details class="route-advanced-settings"><summary><span>高级路由设置</span><small>模式、推理强度与调度策略</small></summary><div class="route-settings-grid"><label data-ui-key="mode">路由模式<select id="routeModeInput"><option value="logical" ${logical ? 'selected' : ''}>能力映射（跨供应商）</option><option value="direct" ${!logical ? 'selected' : ''}>实际模型直连</option></select></label>${logical ? `<label data-ui-key="model">逻辑模型<select id="routeModelInput">${models.map((model) => `<option value="${escapeHTML(model)}" ${model === route.model ? 'selected' : ''}>${escapeHTML(model)}</option>`).join('')}</select></label><label data-ui-key="effort">推理强度<select id="routeEffortInput">${efforts.map((effort) => `<option value="${escapeHTML(effort)}" ${effort === route.reasoning_effort ? 'selected' : ''}>${escapeHTML(effort)}</option>`).join('')}</select></label>` : '<div class="route-mode-placeholder"><strong>每个目标独立指定模型</strong><span>适合上游模型名明确、不需要能力映射的路由。</span></div>'}<label data-ui-key="strategy">调度策略<select id="routeStrategyInput"><option value="" ${!route.strategy ? 'selected' : ''}>严格按目标顺序</option><option value="least_loaded" ${route.strategy === 'least_loaded' ? 'selected' : ''}>最少负载</option><option value="round_robin" ${route.strategy === 'round_robin' ? 'selected' : ''}>轮询</option><option value="priority" ${route.strategy === 'priority' ? 'selected' : ''}>连接优先级</option><option value="sticky" ${route.strategy === 'sticky' ? 'selected' : ''}>会话粘滞</option></select></label><p class="strategy-help">${escapeHTML(strategyHelp(route.strategy))}</p></div><div class="route-mode-note"><span>${logical ? '↗' : '→'}</span><div><strong>${logical ? '能力映射模式' : '实际模型直连模式'}</strong>${logical ? '每个连接按能力表自动解析真实模型。' : '每个上游目标独立指定实际模型。'}</div></div></details></div><div class="section-head route-target-head" data-ui-key="targets-heading"><div><h2>上游顺序</h2><p>${route.strategy ? '顺序用于同条件排序和故障接管。' : '首个上游失败后自动尝试下一项。'}</p></div></div><div class="target-chain" data-ui-key="targets">${route.targets.map((target, index) => routeTargetHTML(route, target, index, accounts)).join('') || '<div class="empty-copy">尚无上游；添加后再选择连接和模型。</div>'}</div>${routeTargetToolbarHTML(alias, route)}<button class="add-target" type="button" data-route-action="add-target">＋ 添加备用上游</button>${renderRoutePreview(route)}`,
    );
  }

  async function convertLegacyRoute() {
    const alias = routesState.selectedRoute,
      route = normalizedRoute(alias);
    if (!route.legacy) return;
    const accounts = configuredAccounts(),
      source = route.all_accounts
        ? accounts.filter((account) => account.enabled !== false).map((account) => ({ account: account.id }))
        : route.targets;
    if (!source.length) {
      toast('当前没有可转换的连接目标', true);
      return;
    }
    const scopeWarning = route.all_accounts ? '转换会固定当前已启用连接；以后新增连接不会自动加入。\n\n' : '';
    if (
      !(await UI.confirm({
        title: '转换为显式上游',
        message: `${scopeWarning}转换会创建一份草稿，检查后再保存。`,
        confirmLabel: '创建转换草稿',
      }))
    )
      return;
    routesState.routeDraft[alias] = {
      model: '',
      reasoning_effort: 'auto',
      strategy: route.strategy || 'least_loaded',
      targets: source.map((target) => {
        const account = accounts.find((item) => item.id === target.account),
          model = route.upstream_model || account?.model_map?.[alias] || alias;
        return { account: target.account, model, reasoning_effort: '' };
      }),
    };
    markRoutesDirty();
    validateAndRenderRoutes();
    toast('已转换为显式目标草稿；请检查模型兼容性后保存');
  }

  function markRoutesDirty() {
    routesState.routesDirty = Core.routeImpact(routesState.routeBaseline, routesState.routeDraft).length > 0;
    if (!routesState.routesDirty && routesState.routeConflict) {
      const serverRoutes = structuredClone(resourcesState.data?.config?.routes || {});
      routesState.routeDraft = serverRoutes;
      routesState.routeBaseline = structuredClone(serverRoutes);
      routesState.routeServerFingerprint = Core.routeFingerprint(serverRoutes);
      routesState.routeConflict = false;
    }
  }

  function updateRouteFromEditor(changedField = '') {
    const alias = routesState.selectedRoute;
    if (!alias) return;
    const route = normalizedRoute(alias),
      aliasInput = $('routeAliasInput'),
      nextAlias = aliasInput?.value.trim() || '';
    if (!nextAlias) {
      toast('客户端模型别名不能为空', true);
      aliasInput.value = alias;
      return;
    }
    if (nextAlias !== alias && routesState.routeDraft[nextAlias]) {
      toast('客户端模型别名已存在', true);
      aliasInput.value = alias;
      return;
    }
    if ($('routeModelInput')) route.model = $('routeModelInput').value;
    if (changedField === 'routeModelInput') {
      const choices = reasoningOptions(route.model);
      if (!choices.includes(route.reasoning_effort)) route.reasoning_effort = choices[0] || 'auto';
    } else if ($('routeEffortInput')) route.reasoning_effort = $('routeEffortInput').value || 'auto';
    route.strategy = $('routeStrategyInput')?.value || '';
    if (nextAlias !== alias) {
      delete routesState.routeDraft[alias];
      routesState.routeDraft[nextAlias] = route;
      routesState.selectedRoute = nextAlias;
    } else routesState.routeDraft[alias] = route;
    markRoutesDirty();
    validateAndRenderRoutes();
  }

  function changeRouteMode(mode) {
    const alias = routesState.selectedRoute,
      route = normalizedRoute(alias);
    if (!alias) return;
    if (mode === 'logical') {
      const models = logicalModels();
      if (!models.length) {
        toast('当前连接没有能力映射，暂时不能使用能力路由', true);
        renderRouteEditor();
        return;
      }
      route.model = models[0];
      const choices = reasoningOptions(route.model);
      route.reasoning_effort = choices.includes('auto') ? 'auto' : choices[0] || 'auto';
    } else if (route.model) {
      const previous = structuredClone(route);
      route.targets = route.targets.map((target) => {
        if (target.model) return target;
        const account = configuredAccounts().find((item) => item.id === target.account),
          resolution = Core.targetResolution(account, previous, target);
        return {
          ...target,
          model: resolution.ok ? resolution.upstream_model : '',
          reasoning_effort: target.reasoning_effort || previous.reasoning_effort || '',
        };
      });
      route.model = '';
      route.reasoning_effort = 'auto';
    }
    routesState.routeDraft[alias] = route;
    markRoutesDirty();
    validateAndRenderRoutes();
  }

  function updateTarget(index, field, value) {
    const route = normalizedRoute(routesState.selectedRoute);
    if (!route.targets[index]) return;
    route.targets[index][field] = value;
    if (field === 'account') {
      route.targets[index].credential = '';
      if (!route.model && !route.targets[index].model) {
        const models = Core.directModels(configuredAccount(value));
        if (models.length === 1) route.targets[index].model = models[0];
      }
    }
    routesState.routeDraft[routesState.selectedRoute] = route;
    markRoutesDirty();
    validateAndRenderRoutes();
  }

  function targetAction(index, action) {
    const route = normalizedRoute(routesState.selectedRoute);
    if (action === 'delete') route.targets.splice(index, 1);
    if (action === 'up' && index > 0)
      [route.targets[index - 1], route.targets[index]] = [route.targets[index], route.targets[index - 1]];
    if (action === 'down' && index < route.targets.length - 1)
      [route.targets[index + 1], route.targets[index]] = [route.targets[index], route.targets[index + 1]];
    routesState.routeDraft[routesState.selectedRoute] = route;
    markRoutesDirty();
    validateAndRenderRoutes();
    const nextIndex = Math.max(
      0,
      Math.min(route.targets.length - 1, action === 'up' ? index - 1 : action === 'down' ? index + 1 : index),
    );
    const row = one(`[data-target-index="${nextIndex}"]`, $('routeEditor'));
    const next =
      (action !== 'delete' && row?.querySelector(`[data-target-action="${action}"]:not(:disabled)`)) ||
      row?.querySelector('[data-target-field="account"]') ||
      one('[data-route-action="add-target"]', $('routeEditor'));
    next?.focus({ preventScroll: true });
  }

  function uniqueRouteAlias(base) {
    const root = (base || 'new-model').trim() || 'new-model';
    let alias = root,
      index = 2;
    while (routesState.routeDraft[alias]) alias = `${root}-${index++}`;
    return alias;
  }

  function syncRouteCreateModels(resetModel = false) {
    const account = configuredAccount($('routeCreateConnection').value),
      input = $('routeCreateModel'),
      logical = Core.logicalModels(account ? [account] : []),
      direct = Core.directModels(account),
      models = Core.uniqueStrings([...logical, ...direct]);
    setHTML(
      $('routeCreateModels'),
      models.map((model) => `<option value="${escapeHTML(model)}"></option>`).join(''),
    );
    if (resetModel || !input.value) input.value = logical[0] || direct[0] || '';
    const capability = (account?.capabilities || []).find((item) => item.model === input.value),
      knownDirect = direct.includes(input.value) || (account?.models || []).includes('*');
    setHTML(
      $('routeCreateModeHint'),
      capability
        ? `<strong>能力映射模式</strong><br>${escapeHTML(input.value)} 会按每条连接的能力表解析实际模型。`
        : knownDirect
          ? `<strong>实际模型直连</strong><br>${escapeHTML(input.value)} 已由此连接声明。`
          : `<strong>手动实际模型</strong><br>${account ? '此连接未声明该模型，保存前会要求你确认。' : '请先选择连接。'}`,
    );
    if (!routesState.routeCreateAliasTouched || !$('routeCreateAlias').value)
      $('routeCreateAlias').value = uniqueRouteAlias(input.value || 'new-model');
    one('button[type="submit"]', $('routeCreateForm')).disabled = !account || !input.value.trim();
  }

  function openRouteCreate(connectionID = '', preferredModel = '') {
    const accounts = configuredAccounts(),
      select = $('routeCreateConnection');
    setHTML(
      select,
      accounts
        .map(
          (account) =>
            `<option value="${escapeHTML(account.id)}" ${account.id === connectionID ? 'selected' : ''}>${escapeHTML(account.name || account.id)}${account.enabled === false ? '（停用）' : ''}</option>`,
        )
        .join(''),
    );
    if (connectionID && accounts.some((account) => account.id === connectionID)) select.value = connectionID;
    routesState.routeCreateAliasTouched = false;
    $('routeCreateModel').value = preferredModel;
    $('routeCreateAlias').value = '';
    syncRouteCreateModels(!preferredModel);
    openDialog('routeCreateDialog');
  }

  async function createRouteFromDialog(event) {
    event.preventDefault();
    const account = configuredAccount($('routeCreateConnection').value),
      model = $('routeCreateModel').value.trim(),
      alias = $('routeCreateAlias').value.trim(),
      button = one('button[type="submit"]', $('routeCreateForm')),
      label = button.textContent;
    if (!account || !model || !alias) {
      toast('请选择连接，并填写模型和客户端别名', true);
      return;
    }
    if (routesState.routeDraft[alias]) {
      toast('客户端模型别名已存在', true);
      $('routeCreateAlias').focus();
      return;
    }
    button.disabled = true;
    button.textContent = '正在启用…';
    routesState.routeDraft[alias] = Core.defaultRouteForAccount(account, model);
    routesState.selectedRoute = alias;
    markRoutesDirty();
    closeDialog('routeCreateDialog');
    closeDialog('onboardingResultDialog');
    showView('routes', true);
    validateAndRenderRoutes();
    try {
      await saveRoutes({ successMessage: `${alias} 已创建并启用` });
    } finally {
      button.disabled = false;
      button.textContent = label;
    }
  }

  function addRoute() {
    openRouteCreate();
  }

  function serializeRoute(route) {
    return Core.serializeRoute(route);
  }

  async function deleteSelectedRoute() {
    const alias = routesState.selectedRoute;
    if (!alias || !routesState.routeDraft[alias]) return;
    if (
      !(await UI.confirm({
        title: '删除模型路由',
        message: `将删除“${alias}”。${routesState.routesDirty ? '此项会加入待保存更改，保存后生效。' : '立即生效，客户端将无法继续使用这个模型名。'}`,
        confirmLabel: '删除路由',
        destructive: true,
      }))
    )
      return;
    const hadPendingChanges = routesState.routesDirty;
    delete routesState.routeDraft[alias];
    routesState.selectedRoute = '';
    markRoutesDirty();
    validateAndRenderRoutes();
    if (hadPendingChanges) {
      toast('路由已加入待保存更改');
      return;
    }
    await saveRoutes({ skipCautionConfirm: true, successMessage: `${alias} 已删除并热加载` });
  }

  async function discardRouteChanges(ask = true) {
    if (saving) return;
    if (
      ask &&
      routesState.routesDirty &&
      !(await UI.confirm({
        title: '放弃未保存的更改',
        message: '将恢复服务器上的配置。当前路由草稿会被清除。',
        confirmLabel: '放弃更改',
        destructive: true,
      }))
    )
      return;
    const routes = structuredClone(resourcesState.data?.config?.routes || {});
    routesState.routeDraft = routes;
    routesState.routeBaseline = structuredClone(routes);
    routesState.routeServerFingerprint = Core.routeFingerprint(routes);
    routesState.routeConflict = false;
    routesState.routesDirty = false;
    routesState.selectedRoute =
      routesState.selectedRoute && routes[routesState.selectedRoute]
        ? routesState.selectedRoute
        : Object.keys(routes).sort()[0] || '';
    validateAndRenderRoutes();
    toast('已重新载入服务器路由');
  }

  async function saveRoutes(options = {}) {
    if (saving) return false;
    if (routesState.routeConflict) {
      toast('服务器路由已变化，请先重新载入，避免覆盖其他更改', true);
      return false;
    }
    const validation = Core.routeValidation(routesState.routeDraft, configuredAccounts());
    if (!validation.ok) {
      validateAndRenderRoutes(true);
      toast(`还有 ${validation.errors.length} 项路由配置需要修复`, true);
      return false;
    }
    const changes = Core.routeImpact(routesState.routeBaseline, routesState.routeDraft),
      removed = changes.filter((change) => change.kind === 'removed').length;
    if (!changes.length) {
      routesState.routesDirty = false;
      validateAndRenderRoutes();
      return true;
    }
    const cautions = [];
    const draftFingerprint = Core.routeFingerprint(routesState.routeDraft);
    if (removed) cautions.push(`将删除 ${removed} 条客户端路由`);
    if (validation.warnings.length) cautions.push(`存在 ${validation.warnings.length} 项未验证或停用警告`);
    if (
      cautions.length &&
      !options.skipCautionConfirm &&
      !(await UI.confirm({
        title: '保存路由更改',
        message: `${cautions.join('，')}。\n\n保存后立即生效。`,
        confirmLabel: '保存并生效',
        destructive: removed > 0,
      }))
    )
      return false;
    if (routesState.routeConflict || draftFingerprint !== Core.routeFingerprint(routesState.routeDraft)) {
      toast('路由配置已变化，请检查当前内容后再保存。', true);
      return false;
    }
    const routes = Object.fromEntries(
      routeAliases().map((alias) => [alias, serializeRoute(routesState.routeDraft[alias])]),
    );
    saving = true;
    renderRouteChangeState(validation);
    try {
      await request('/routes', { method: 'PUT', body: JSON.stringify(routes) });
      // The operator can continue editing while the save is in flight. Only
      // acknowledge the submitted snapshot; never erase newer local changes.
      if (draftFingerprint === Core.routeFingerprint(routesState.routeDraft))
        routesState.routeDraft = structuredClone(routes);
      routesState.routeBaseline = structuredClone(routes);
      routesState.routeServerFingerprint = Core.routeFingerprint(routes);
      routesState.routeConflict = false;
      markRoutesDirty();
      toast(options.successMessage || `已保存并热加载 ${changes.length} 项路由更改`);
      await refreshAll(true, true);
      return true;
    } catch (error) {
      if (error.status === 409) await refreshAll(true, true);
      toast(error.status === 409 ? `保存被拒绝：${error.message}` : error.message, true);
      validateAndRenderRoutes();
      return false;
    } finally {
      saving = false;
      renderRouteChangeState(Core.routeValidation(routesState.routeDraft, configuredAccounts()));
    }
  }

  function bind() {
    $('routeSearch').addEventListener(
      'input',
      Runtime.debounce(() => renderRoutes()),
    );
    $('routeList').addEventListener('click', (event) => {
      const button = event.target.closest('[data-route-alias]');
      if (button) {
        routesState.selectedRoute = button.dataset.routeAlias;
        renderRoutes();
      }
    });
    $('addRouteButton').addEventListener('click', addRoute);
    $('saveRoutesButton').addEventListener('click', saveRoutes);
    $('discardRoutesButton').addEventListener('click', () => discardRouteChanges(true));
    $('reloadRoutesButton').addEventListener('click', () => discardRouteChanges(routesState.routesDirty));
    $('routeCreateConnection').addEventListener('change', () => syncRouteCreateModels(true));
    $('routeCreateModel').addEventListener('input', () => syncRouteCreateModels(false));
    $('routeCreateAlias').addEventListener('input', () => {
      routesState.routeCreateAliasTouched = true;
    });
    $('routeCreateForm').addEventListener('submit', createRouteFromDialog);
    $('routeEditor').addEventListener('change', (event) => {
      if (event.target.id === 'routeModeInput') {
        changeRouteMode(event.target.value);
        return;
      }
      if (
        ['routeAliasInput', 'routeModelInput', 'routeEffortInput', 'routeStrategyInput'].includes(
          event.target.id,
        )
      ) {
        updateRouteFromEditor(event.target.id);
        return;
      }
      const row = event.target.closest('[data-target-index]');
      if (row && event.target.dataset.targetField)
        updateTarget(Number(row.dataset.targetIndex), event.target.dataset.targetField, event.target.value);
    });
    $('routeEditor').addEventListener('click', (event) => {
      const targetButton = event.target.closest('[data-target-action]');
      if (targetButton) {
        targetAction(
          Number(targetButton.closest('[data-target-index]').dataset.targetIndex),
          targetButton.dataset.targetAction,
        );
        return;
      }
      const action = event.target.closest('[data-route-action]')?.dataset.routeAction;
      if (action === 'convert-legacy') {
        convertLegacyRoute();
        return;
      }
      if (action === 'add-target') {
        const route = normalizedRoute(routesState.selectedRoute);
        route.targets.push({ account: '', model: '', reasoning_effort: '' });
        routesState.routeDraft[routesState.selectedRoute] = route;
        markRoutesDirty();
        validateAndRenderRoutes();
        one('.target-row:last-child [data-target-field="account"]', $('routeEditor'))?.focus();
      }
      if (action === 'delete') deleteSelectedRoute();
    });
    $('routeEditor').addEventListener('change', (event) => {
      if (event.target.dataset.routeAccountToggle !== undefined)
        toggleRouteAccount(event.target.dataset.routeAccountToggle, event.target.checked);
    });
    $('routeEditor').addEventListener('click', (event) => {
      const action = event.target.closest('[data-route-action]')?.dataset.routeAction;
      if (action === 'duplicate') duplicateSelectedRoute();
      if (action === 'add-all-compatible') addAllCompatibleTargets();
    });
    $('routeEditor').addEventListener(
      'toggle',
      (event) => {
        if (!event.target.classList?.contains('route-target-picker')) return;
        const alias = routesState.selectedRoute;
        if (event.target.open) routesState.routePickerAliases.add(alias);
        else routesState.routePickerAliases.delete(alias);
      },
      true,
    );
  }

  return Object.freeze({ bind, renderRoutes, openRouteCreate, receiveSnapshot, hasUnsavedChanges });
};
