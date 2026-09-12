/* Shared controller. Dependencies are supplied by the composition root. */
globalThis.Lite2APIShared = function createShared(context) {
  'use strict';
  const { Core, now, indexData, resourcesState } = context;
  const numberFormat = new Intl.NumberFormat('zh-CN');
  const routeUsageIndex = globalThis.Lite2APIMetrics.createRouteUsageIndex(Core.normalizeRoute);
  const accountRouteUsage = (id) => routeUsageIndex(resourcesState.data?.config?.routes)(id);

  const number = (value) => numberFormat.format(Number(value) || 0);

  const finiteNumber = Core.finiteNumber;

  const escapeHTML = (value) =>
    String(value ?? '').replace(
      /[&<>"']/g,
      (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c],
    );

  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

  const percentile = globalThis.Lite2APIMetrics.percentile;

  function formatTime(value) {
    if (!value) return '—';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return String(value);
    return date.toLocaleString('zh-CN', {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    });
  }

  function relativeTime(value) {
    const time = new Date(value).getTime();
    if (!Number.isFinite(time)) return '时间未知';
    const delta = time - now();
    const absolute = Math.abs(delta);
    if (absolute < 60000) return delta >= 0 ? '不到 1 分钟后' : '刚刚';
    if (absolute < 3600000) return `${Math.round(absolute / 60000)} 分钟${delta >= 0 ? '后' : '前'}`;
    if (absolute < 86400000) return `${Math.round(absolute / 3600000)} 小时${delta >= 0 ? '后' : '前'}`;
    return `${Math.round(absolute / 86400000)} 天${delta >= 0 ? '后' : '前'}`;
  }

  function providerKey(...values) {
    const text = values.flat().filter(Boolean).join(' ').toLowerCase();
    if (/antigravity/.test(text)) return 'antigravity';
    if (/anthropic|claude/.test(text)) return 'anthropic';
    if (/gemini|google/.test(text)) return 'gemini';
    if (/deepseek/.test(text)) return 'deepseek';
    if (/xai|x\.ai|grok/.test(text)) return 'grok';
    if (/kimi|moonshot/.test(text)) return 'kimi';
    if (/atomcode|atom-main/.test(text)) return 'atom';
    if (/cliproxy|oauth/.test(text)) return 'codex';
    if (/openai|codex|gpt/.test(text)) return 'codex';
    return 'custom';
  }

  const PROVIDER_LABEL = {
    codex: 'OpenAI / Codex',
    anthropic: 'Claude / Anthropic',
    gemini: 'Google Gemini',
    antigravity: 'Antigravity',
    kimi: 'Kimi / Moonshot',
    grok: 'Grok / xAI',
    deepseek: 'DeepSeek',
    atom: 'AtomCode',
    custom: '自定义服务',
  };

  const PROVIDER_MARK = {
    codex: 'AI',
    anthropic: 'C',
    gemini: 'G',
    antigravity: 'AG',
    kimi: 'K',
    grok: 'X',
    deepseek: 'DS',
    atom: 'A',
    custom: '+',
  };

  const OFFICIAL_ICON_KEYS = new Set([
    'openai',
    'claude',
    'gemini',
    'antigravity',
    'gpt-5-6-sol',
    'gpt-5-6-terra',
    'gpt-5-6-luna',
    'gpt-5-5',
    'gpt-5-4',
    'gpt-5-4-mini',
    'gpt-5-3-codex',
    'gpt-image-2',
    'gpt-oss-120b',
  ]);

  const PROVIDER_ICON = {
    codex: 'openai',
    anthropic: 'claude',
    gemini: 'gemini',
    antigravity: 'antigravity',
  };

  function officialIconHTML(key, className = 'model-icon') {
    return OFFICIAL_ICON_KEYS.has(key)
      ? `<span class="${className} official-icon icon-${key}" aria-hidden="true"></span>`
      : '';
  }

  function providerIconHTML(key, className = 'provider-dot') {
    return (
      officialIconHTML(PROVIDER_ICON[key], className) ||
      `<span class="${className}">${escapeHTML(PROVIDER_MARK[key] || '+')}</span>`
    );
  }

  function modelIconHTML(model, className = 'model-icon') {
    return officialIconHTML(Core.modelIconKey(model), className);
  }

  function modelLabelHTML(model, className = 'model-token') {
    const label = String(model || '—');
    return `<span class="${className}" title="${escapeHTML(label)}">${modelIconHTML(label)}<span>${escapeHTML(label)}</span></span>`;
  }

  function connectionProviderKey(account) {
    return providerKey(
      account?.name,
      account?.id,
      account?.instance_id,
      account?.adapter_id,
      account?.base_url,
    );
  }

  function requestOK(record) {
    return Number(record.status) >= 200 && Number(record.status) < 400;
  }

  function quotaWindows(account) {
    return Array.isArray(account?.quota_windows)
      ? account.quota_windows.filter((window) => window && window.kind)
      : [];
  }

  function quotaPercentage(window) {
    const value = finiteNumber(window?.used_percentage);
    return value === null ? null : Math.max(0, Math.min(100, value));
  }

  function quotaTone(value) {
    return value >= 95 ? 'bad' : value >= 82 ? 'warn' : '';
  }

  function accountStatus(account) {
    const windows = quotaWindows(account),
      warning =
        account.quota_exceeded ||
        windows.some((window) => {
          const value = quotaPercentage(window);
          return value !== null && value >= 82;
        }) ||
        account.status === 'unavailable',
      recentSuccess = Number(account.recent_success) || 0,
      recentFailed = Number(account.recent_failed) || 0,
      success = Number(account.success) || 0,
      failed = Number(account.failed) || 0;
    if (!account.ready)
      return {
        label:
          account.status === 'disabled' ? '已停用' : account.status === 'unavailable' ? '冷却中' : '需检查',
        tone: 'bad',
      };
    if (recentFailed > 0 && recentSuccess === 0) return { label: '最近调用失败', tone: 'bad' };
    if (success === 0 && failed > 0) return { label: '调用未成功', tone: 'bad' };
    if (warning) return { label: '额度预警', tone: 'warn' };
    if (success > 0 || recentSuccess > 0) return { label: '可用', tone: '' };
    return { label: '待实测', tone: 'neutral' };
  }

  function connectionStats() {
    return indexData(resourcesState.data).connections;
  }

  function quotaWindowHTML(window) {
    const used = quotaPercentage(window),
      label = window.model || window.label || window.kind,
      reset = window.reset_at ? relativeTime(window.reset_at) + '重置' : '重置时间未知',
      observed = window.observed_at ? relativeTime(window.observed_at) + '观测' : '观测时间未知',
      source = window.source || '上游未标注',
      icon = modelIconHTML(label, 'inline-model-icon');
    if (used === null) {
      const remaining = finiteNumber(window.remaining),
        value =
          remaining !== null
            ? `${number(remaining)} ${window.unit || ''}`.trim()
            : window.status === 'cooldown'
              ? '冷却中'
              : window.status === 'exhausted'
                ? '已耗尽'
                : '已观测';
      return `<div class="quota-unknown"><div class="quota-unknown-head">${icon}<strong>${escapeHTML(label)}</strong><span>· ${escapeHTML(value)}</span></div><small>${escapeHTML(reset)} · ${escapeHTML(observed)} · ${escapeHTML(source)}</small></div>`;
    }
    const tone = quotaTone(used);
    return `<div class="quota-window ${tone}"><div class="quota-window-head"><span class="quota-model-label" title="${escapeHTML(label)}">${icon}${escapeHTML(label)}</span><strong>${used.toFixed(1)}%</strong></div><div class="quota-bar"><i style="width:${used}%"></i></div><details class="quota-source"><summary title="查看数据来源">${escapeHTML(reset)}</summary><p>${escapeHTML(observed)} · ${escapeHTML(source)}</p></details></div>`;
  }

  function configuredAccount(id) {
    return indexData(resourcesState.data).configured.get(id);
  }

  function accountTestModel(account) {
    const routed = Object.entries(resourcesState.data?.config?.routes || {}).flatMap(([alias, raw]) => {
      const route = Core.normalizeRoute(raw),
        usesAccount = route.all_accounts
          ? account?.enabled !== false
          : route.targets.some((target) => target.account === account?.id);
      if (!usesAccount) return [];
      if (route.legacy) return [route.upstream_model || account?.model_map?.[alias] || alias];
      return route.targets
        .filter((target) => target.account === account?.id)
        .map((target) => target.model)
        .filter(Boolean);
    })[0];
    return (
      (account?.models || []).find((model) => model && model !== '*') ||
      account?.capabilities?.[0]?.upstream_model ||
      routed ||
      account?.capabilities?.[0]?.model ||
      ''
    );
  }

  function accountFingerprint(account) {
    return Core.accountFingerprint(account);
  }

  function findOAuthPoolConnection() {
    return (
      (resourcesState.data?.config?.accounts || []).find(
        (account) =>
          account.adapter_id === 'cli-proxy-api' ||
          account.id === 'cliproxy-oauth' ||
          String(account.base_url || '').includes('127.0.0.1:45682'),
      ) || null
    );
  }

  function configuredAccounts() {
    return resourcesState.data?.config?.accounts || [];
  }

  return Object.freeze({
    accountRouteUsage,
    finiteNumber,
    percentile,
    requestOK,
    quotaWindows,
    quotaPercentage,
    relativeTime,
    number,
    connectionStats,
    escapeHTML,
    quotaTone,
    providerKey,
    accountStatus,
    quotaWindowHTML,
    providerIconHTML,
    PROVIDER_LABEL,
    accountFingerprint,
    connectionProviderKey,
    configuredAccount,
    accountTestModel,
    sleep,
    formatTime,
    modelLabelHTML,
    modelIconHTML,
    findOAuthPoolConnection,
    configuredAccounts,
  });
};
