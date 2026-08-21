/* Lite2API Native v10 — task-level behavior for usage, quota, quality, and onboarding. */
(() => {
  'use strict';

  const qualityResults = new Map();
  let selectedOnboardingProvider = 'codex';
  let syncScheduled = false;

  const $ = id => document.getElementById(id);
  const all = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const number = value => new Intl.NumberFormat('zh-CN').format(Number(value) || 0);
  const clamp = (value, min, max) => Math.max(min, Math.min(max, value));
  const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, character => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  })[character]);

  const providers = {
    codex: {
      title: 'OpenAI / Codex',
      summary: 'ChatGPT 订阅账号走 OAuth 账号池；官方或兼容接口走 API Key。两种方式不要混在同一个连接里。',
      methods: [
        ['oauth', 'ChatGPT OAuth', '浏览器授权后加入 Codex 账号池', 'codex'],
        ['manual', 'OpenAI API Key', '官方 API 或 OpenAI 兼容服务', 'openai'],
        ['manual', 'OAuth 聚合连接', '已有 CLIProxyAPI 时只添加稳定连接', 'cliproxy'],
      ],
      checks: [['OAuth 凭据', '授权后由隔离适配器保存'], ['API Key', '保存前可测试 /models'], ['额度', 'OAuth 账号会显示 5h / 7d 窗口']],
    },
    anthropic: {
      title: 'Claude / Anthropic',
      summary: 'Claude Code 订阅优先使用 OAuth；Anthropic Console 使用 API Key。Lite2API 会按原生 Messages 协议发送。',
      methods: [
        ['oauth', 'Claude OAuth', '通过隔离适配器完成授权', 'anthropic'],
        ['manual', 'Anthropic API Key', 'Console API Key 与原生 Messages', 'anthropic'],
        ['manual', 'OAuth 聚合连接', '已有 Claude 账号池的稳定入口', 'cliproxy'],
      ],
      checks: [['协议', 'Anthropic Messages 与 x-api-key'], ['额度', '可展示 5h、7d、Sonnet、Opus 窗口'], ['测试', '质量测试直连该账号，不走 fallback']],
    },
    gemini: {
      title: 'Google Gemini',
      summary: '可选择 Gemini CLI OAuth、官方 API Key，或把 Web Cookie 交给隔离适配器。核心网关不保存 Web Cookie。',
      methods: [
        ['oauth', 'Gemini CLI OAuth', 'Google 账号授权并加入账号池', 'gemini'],
        ['manual', 'Gemini API Key', 'Google 官方 OpenAI 兼容端点', 'gemini'],
        ['web', 'Gemini Web', '整理 Cookie 并交给 Gemini Web2API', 'gemini-web'],
      ],
      checks: [['OAuth', '适合 Gemini CLI 订阅账号'], ['API Key', '适合官方付费接口'], ['Web', '敏感 Cookie 只进入隔离适配器']],
    },
    antigravity: {
      title: 'Antigravity',
      summary: 'Antigravity 使用 Google OAuth 凭据池。授权完成后，额度、冷却和可用状态会出现在账号列表。',
      methods: [['oauth', 'Google OAuth', '授权 Antigravity 账号', 'antigravity']],
      checks: [['凭据隔离', 'OAuth 文件由适配器保存'], ['冷却', '不可用与重试时间单独展示'], ['路由', '授权完成后再绑定稳定连接']],
    },
    kimi: {
      title: 'Kimi / Moonshot',
      summary: '订阅账号可使用设备授权，官方开放平台使用 API Key。两种账号的额度来源不同。',
      methods: [
        ['oauth', 'Kimi 设备授权', '打开设备码页面并自动轮询', 'kimi'],
        ['manual', 'Moonshot API Key', '官方 OpenAI 兼容接口', 'kimi-api'],
      ],
      checks: [['设备授权', '适合订阅账号池'], ['API Key', '按官方余额与调用计费'], ['模型', '保存后由上游目录自动发现']],
    },
    grok: {
      title: 'Grok / xAI',
      summary: '官方接口使用 xAI API Key；多账号或 Web/Console 凭据放进 Grok2API 隔离适配器。',
      methods: [
        ['manual', 'xAI API Key', '官方 Grok API', 'xai'],
        ['manual', 'Grok2API 连接', '连接本机多账号适配器', 'grok-adapter'],
        ['web', 'Web / Console SSO', '本地整理后交给 Grok2API', 'grok-web'],
      ],
      checks: [['官方 API', '保存前测试模型目录'], ['多账号', '由 Grok2API 管理凭据轮换'], ['质量', '可对每个稳定连接连续测试 3 次']],
    },
    deepseek: {
      title: 'DeepSeek',
      summary: '使用官方 API Key，支持 Chat 与 Reasoner。建议同时配置主连接与备用连接，再在路由页明确排序。',
      methods: [['manual', 'DeepSeek API Key', '官方 Chat / Reasoner 接口', 'deepseek']],
      checks: [['主连接', '先测试地址与认证'], ['备用连接', '需要时单独添加第二个连接'], ['fallback', '只在路由页明确配置，不会自动追加']],
    },
    atom: {
      title: 'AtomCode',
      summary: '连接本机 AtomCode 订阅适配器。Lite2API 只访问回环 API，不直接保存订阅凭据。',
      methods: [['manual', 'AtomCode 适配器', '连接 127.0.0.1 上的兼容服务', 'atom']],
      checks: [['隔离', '订阅凭据由适配器保存'], ['连通性', '保存前检查 /models'], ['路由', '保存后再选择对应逻辑模型']],
    },
    custom: {
      title: '自定义兼容服务',
      summary: '为任意 OpenAI 或 Anthropic 兼容服务填写 Base URL、认证头、代理、模型映射和固定请求头。',
      methods: [['manual', '自定义连接', '完整控制协议和请求头', 'custom']],
      checks: [['地址', '必须是绝对 URL'], ['认证', '可选择 API Key、环境变量或无认证'], ['验证', '保存前测试模型目录与代理']],
    },
  };

  function ensureAdditionalTemplates() {
    if (typeof accountTemplates !== 'object') return;
    if (!accountTemplates['kimi-api']) {
      accountTemplates['kimi-api'] = {
        id: 'kimi-main', name: 'Moonshot API', type: 'openai', adapter_id: 'generic-openai', instance_id: 'moonshot',
        operations: ['openai.chat'], base_url: 'https://api.moonshot.cn/v1', env: 'MOONSHOT_API_KEY', credential: 'key',
        models: ['kimi-k2.5', 'kimi-k2-turbo-preview'], concurrency: 4, auth_header: 'authorization', auth_scheme: 'Bearer',
        note: 'Moonshot 官方 OpenAI 兼容接口。订阅设备授权请返回上一步选择 Kimi 设备授权。'
      };
    }
  }

  function rangeMilliseconds() {
    return ({ '1h': 3600000, '6h': 21600000, '24h': 86400000, '3d': 259200000, '7d': 604800000 })[$('chartRange')?.value] || 86400000;
  }

  function percentile(values, ratio) {
    const sorted = values.filter(Number.isFinite).sort((a, b) => a - b);
    if (!sorted.length) return null;
    return sorted[Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * ratio) - 1))];
  }

  function recentInRange() {
    const cutoff = Date.now() - rangeMilliseconds();
    return (state?.stats?.recent || []).filter(record => {
      const observed = new Date(record.time).getTime();
      return Number.isFinite(observed) && observed >= cutoff;
    });
  }

  function syncUsageMetrics() {
    const points = Array.isArray(state?.trend?.points) ? state.trend.points : [];
    const calls = points.reduce((sum, point) => sum + (Number(point.requests) || 0), 0);
    const failed = points.reduce((sum, point) => sum + (Number(point.failed) || 0), 0);
    const recent = recentInRange();
    const fallbackCalls = Number(state?.stats?.failovers) || 0;
    const latencies = recent.map(record => Number(record.latency_ms)).filter(Number.isFinite);
    const trendP95 = points.map(point => Number(point.p95_latency_ms)).filter(Number.isFinite);
    const p95 = percentile(latencies, .95) ?? (trendP95.length ? trendP95[trendP95.length - 1] : null);
    const sampleCalls = calls || recent.length;
    const sampleFailed = calls ? failed : recent.filter(record => Number(record.status) < 200 || Number(record.status) >= 400).length;
    const successRate = sampleCalls ? ((sampleCalls - sampleFailed) / sampleCalls) * 100 : null;
    const rangeLabel = $('chartRange')?.selectedOptions?.[0]?.textContent || '最近 24 小时';

    if ($('v10CallCount')) $('v10CallCount').textContent = sampleCalls ? number(sampleCalls) : '0';
    const callSource = calls ? `${points.length} 个趋势点` : `${recent.length} 条保留明细`;
    if ($('v10CallContext')) $('v10CallContext').textContent = `${rangeLabel} · ${callSource}`;
    if ($('v10SuccessRate')) $('v10SuccessRate').textContent = successRate === null ? '—' : `${successRate.toFixed(2)}%`;
    if ($('v10FailureContext')) $('v10FailureContext').textContent = `${number(sampleFailed)} 次失败 · ${calls ? '来自趋势桶' : '来自保留明细'}`;
    if ($('v12FailoverContext')) $('v12FailoverContext').textContent = fallbackCalls ? `进程累计 ${number(fallbackCalls)} 次自动切换` : '';
    if ($('v10P95Latency')) $('v10P95Latency').textContent = p95 === null ? '—' : `${number(Math.round(p95))} ms`;
    if ($('v10LatencyContext')) {
      const average = latencies.length ? Math.round(latencies.reduce((sum, value) => sum + value, 0) / latencies.length) : null;
      $('v10LatencyContext').textContent = average === null ? '暂无速度明细样本' : `保留明细平均 ${number(average)} ms`;
    }
    if ($('v10FailoverCount')) $('v10FailoverCount').textContent = number(fallbackCalls);
    if ($('v10ChannelUsageScope')) $('v10ChannelUsageScope').textContent = `${rangeLabel}内保留的 ${recent.length} 条明细`;
    if ($('chartRetention')) $('chartRetention').textContent = '本地趋势最多保留 7 天；请求明细来自当前保留样本';
  }

  function providerMeta(provider) {
    const key = typeof providerKey === 'function' ? providerKey(provider) : provider;
    const labels = { codex: 'OpenAI / Codex', openai: 'OpenAI / Codex', anthropic: 'Claude', gemini: 'Gemini', antigravity: 'Antigravity', kimi: 'Kimi' };
    const mark = typeof providerMark === 'function' ? providerMark(key) : '';
    return { key, label: labels[provider] || provider || '未知渠道', mark };
  }

  function quotaWindows(account) {
    return Array.isArray(account?.quota_windows) ? account.quota_windows.filter(Boolean) : [];
  }

  function quotaSeverity(account) {
    if (!account?.ready || account?.quota_exceeded) return 3;
    let severity = 0;
    for (const window of quotaWindows(account)) {
      const used = Number(window.used_percentage);
      if (['exhausted', 'cooldown'].includes(String(window.status || ''))) severity = Math.max(severity, 3);
      else if (Number.isFinite(used) && used >= 90) severity = Math.max(severity, 2);
      else if (Number.isFinite(used) && used >= 75) severity = Math.max(severity, 1);
    }
    return severity;
  }

  function quotaReset(window) {
    if (!window?.reset_at) return '重置时间未知';
    const value = new Date(window.reset_at);
    if (Number.isNaN(value.getTime())) return '重置时间未知';
    const minutes = Math.max(0, Math.round((value.getTime() - Date.now()) / 60000));
    if (minutes < 60) return `${minutes} 分钟后重置`;
    if (minutes < 1440) return `${Math.floor(minutes / 60)} 小时 ${minutes % 60} 分后重置`;
    return `${Math.floor(minutes / 1440)} 天后重置`;
  }

  function tightestQuota() {
    let tightest = null;
    for (const account of state?.oauth_accounts || []) {
      for (const window of quotaWindows(account)) {
        const used = Number(window.used_percentage);
        if (!Number.isFinite(used)) continue;
        if (!tightest || used > tightest.used) tightest = { account, window, used };
      }
    }
    return tightest;
  }

  function overviewChannelGroups() {
    const groups = new Map();
    for (const record of recentInRange()) {
      const id = record.account_id || 'unknown';
      if (!groups.has(id)) groups.set(id, { id, count: 0, success: 0, latencies: [], models: new Set() });
      const group = groups.get(id);
      group.count += 1;
      if (Number(record.status) >= 200 && Number(record.status) < 400) group.success += 1;
      const latency = Number(record.latency_ms);
      if (Number.isFinite(latency)) group.latencies.push(latency);
      if (record.upstream_model || record.model) group.models.add(record.upstream_model || record.model);
    }
    return [...groups.values()].map(group => {
      const configured = (state?.config?.accounts || []).find(account => account.id === group.id);
      return {
        ...group,
        name: configured?.name || group.id,
        p95: percentile(group.latencies, .95),
        rate: group.count ? (group.success / group.count) * 100 : null,
      };
    });
  }

  function syncOverviewStory() {
    const quota = tightestQuota();
    const groups = overviewChannelGroups();
    const total = groups.reduce((sum, group) => sum + group.count, 0);
    const primary = [...groups].sort((a, b) => b.count - a.count)[0] || null;
    const slowest = [...groups].filter(group => Number.isFinite(group.p95)).sort((a, b) => b.p95 - a.p95)[0] || null;
    const rangeLabel = $('chartRange')?.selectedOptions?.[0]?.textContent || '最近 24 小时';

    if ($('v12InsightRange')) $('v12InsightRange').textContent = rangeLabel;
    if ($('v12QuotaUsed')) $('v12QuotaUsed').textContent = quota ? `${quota.used.toFixed(quota.used % 1 ? 1 : 0)}%` : '—';
    if ($('v12QuotaContext')) {
      const meta = quota ? providerMeta(quota.account.provider) : null;
      const label = quota?.window?.model || quota?.window?.label || quota?.window?.kind || '额度窗口';
      $('v12QuotaContext').textContent = quota ? `${meta.label} · ${label} · ${quotaReset(quota.window)}` : '暂无可量化额度';
    }
    if ($('v12QuotaRemaining')) $('v12QuotaRemaining').textContent = quota ? `剩余 ${Math.max(0, 100 - quota.used).toFixed(0)}%` : '';

    let headline = '当前运行平稳，继续观察调用与速度。';
    if (quota?.used >= 80 && slowest?.p95 >= 3000) headline = '额度承压，同时有渠道响应偏慢。';
    else if (quota?.used >= 80) headline = '当前主要限制来自账号额度。';
    else if (slowest?.p95 >= 3000) headline = '调用可用，但慢请求集中在个别渠道。';
    else if (total) headline = '调用与速度整体稳定。';
    if ($('v12InsightTitle')) $('v12InsightTitle').textContent = headline;
    if ($('v12OverviewSummary')) {
      const fragments = [];
      if (quota) fragments.push(`<strong>${escapeHTML(providerMeta(quota.account.provider).label)} 最紧张额度已用 ${quota.used.toFixed(0)}%</strong>`);
      if (total) fragments.push(`${rangeLabel}共 ${number(total)} 次保留请求`);
      if (slowest?.p95 != null) fragments.push(`${escapeHTML(slowest.name)} P95 为 ${number(Math.round(slowest.p95))} ms`);
      $('v12OverviewSummary').innerHTML = fragments.length ? `${fragments.join('；')}。` : '尚无足够的额度或请求样本，页面会在真实数据到达后自动更新。';
    }

    const insights = [];
    if (quota) {
      const meta = providerMeta(quota.account.provider);
      insights.push({ tone: quota.used >= 95 ? 'bad' : quota.used >= 80 ? 'warn' : 'good', title: `${meta.label} 额度${quota.used >= 80 ? '需要关注' : '仍有余量'}`, detail: `已用 ${quota.used.toFixed(1)}%，${quotaReset(quota.window)}。`, action: '查看账号', target: 'accounts' });
    }
    if (slowest) insights.push({ tone: slowest.p95 >= 3000 ? 'bad' : slowest.p95 >= 1500 ? 'warn' : 'good', title: `${slowest.name} ${slowest.p95 >= 1500 ? '响应偏慢' : '速度正常'}`, detail: `P95 ${number(Math.round(slowest.p95))} ms，成功率 ${slowest.rate?.toFixed(1) ?? '—'}%。`, action: '查看质量', target: 'quality' });
    if (primary) insights.push({ tone: primary.rate != null && primary.rate < 95 ? 'warn' : 'good', title: `${primary.name} 承担 ${total ? Math.round(primary.count / total * 100) : 0}% 调用`, detail: `${number(primary.count)} 次请求，成功率 ${primary.rate?.toFixed(1) ?? '—'}%。`, action: '查看路由', target: 'routes' });
    if ($('v12InsightList')) $('v12InsightList').innerHTML = insights.length
      ? insights.slice(0, 3).map(item => `<div class="v12-insight-row ${item.tone}"><i></i><div><strong>${escapeHTML(item.title)}</strong><span>${escapeHTML(item.detail)}</span></div><button type="button" data-v12-insight-target="${escapeHTML(item.target)}">${escapeHTML(item.action)}</button></div>`).join('')
      : '<div class="v12-insight-empty">暂无可分析的真实样本。发起请求或接入可读取额度的 OAuth 账号后，这里会给出处理建议。</div>';
  }

  function quotaWindowHTML(window) {
    const label = window.model || window.label || ({ five_hour: '5 小时', seven_day: '7 天', daily: '每日', credits: 'Credits', model_quota: '模型额度' })[window.kind] || window.kind || '额度';
    const used = Number(window.used_percentage);
    const hasPercent = Number.isFinite(used);
    const status = String(window.status || '');
    const tone = status === 'exhausted' || status === 'cooldown' || (hasPercent && used >= 95) ? 'bad' : hasPercent && used >= 80 ? 'warn' : '';
    let value = hasPercent ? `${clamp(used, 0, 100).toFixed(1)}%` : '已观测';
    if (!hasPercent && Number.isFinite(Number(window.remaining))) value = `${number(window.remaining)} ${window.unit || ''}`.trim();
    if (status === 'cooldown') value = '冷却中';
    if (status === 'exhausted') value = '已耗尽';
    return `<div class="v10-quota-window ${tone}"><div class="v10-quota-window-head"><span>${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong></div>${hasPercent ? `<div class="v10-quota-meter"><i style="width:${clamp(used, 0, 100)}%"></i></div>` : ''}<small>${escapeHTML(quotaReset(window))}</small></div>`;
  }

  function syncQuotaBoard() {
    const target = $('v10QuotaBoard');
    if (!target) return;
    const accounts = [...(state?.oauth_accounts || [])].sort((a, b) => quotaSeverity(b) - quotaSeverity(a));
    if (!accounts.length) {
      target.innerHTML = `<div class="v10-empty"><strong>还没有可读取额度的认证账号</strong><br>在“账号与渠道”中添加 OAuth 账号，或确认隔离适配器已连接。</div>`;
      return;
    }
    target.innerHTML = accounts.map(account => {
      const meta = providerMeta(account.provider);
      const windows = quotaWindows(account);
      const severity = quotaSeverity(account);
      const status = account.ready ? (severity >= 2 ? '额度预警' : '可用') : account.status === 'unavailable' ? '冷却中' : '不可用';
      const tone = account.ready ? (severity >= 2 ? 'warn' : 'good') : 'bad';
      return `<article class="v10-quota-account"><div class="v10-quota-identity"><span class="provider-icon provider-${escapeHTML(meta.key)}">${meta.mark}</span><span><strong>${escapeHTML(account.identity || '已保存凭据')}</strong><small>${escapeHTML(meta.label)} · ${escapeHTML(account.plan || '套餐未知')}</small></span></div><div class="v10-quota-windows">${windows.length ? windows.slice(0, 4).map(quotaWindowHTML).join('') : `<div class="v10-quota-unknown">上游尚未返回可量化额度；不会显示伪造的 0%。</div>`}</div><div class="v10-quota-state"><span class="badge ${tone}">${status}</span><small>${number(account.success)} 成功 / ${number(account.failed)} 失败</small></div></article>`;
    }).join('');
  }

  function syncChannelUsage() {
    const target = $('v10ChannelUsageRows');
    if (!target) return;
    const groups = new Map();
    for (const record of recentInRange()) {
      const id = record.account_id || 'unknown';
      if (!groups.has(id)) groups.set(id, { id, count: 0, success: 0, latencies: [], tokens: 0, models: new Set() });
      const group = groups.get(id);
      group.count += 1;
      if (Number(record.status) >= 200 && Number(record.status) < 400) group.success += 1;
      if (Number.isFinite(Number(record.latency_ms))) group.latencies.push(Number(record.latency_ms));
      if (record.usage_available) group.tokens += Number(record.total_tokens) || 0;
      if (record.upstream_model || record.model) group.models.add(record.upstream_model || record.model);
    }
    const rows = [...groups.values()].sort((a, b) => b.count - a.count);
    if (!rows.length) {
      target.innerHTML = '<div class="v10-empty">所选范围内没有保留的请求明细。</div>';
      return;
    }
    target.innerHTML = rows.map(group => {
      const configured = (state?.config?.accounts || []).find(account => account.id === group.id);
      const average = group.latencies.length ? Math.round(group.latencies.reduce((sum, value) => sum + value, 0) / group.latencies.length) : null;
      const p95 = percentile(group.latencies, .95);
      const rate = group.count ? (group.success / group.count) * 100 : 0;
      return `<div class="v10-channel-row"><div><strong>${escapeHTML(configured?.name || group.id)}</strong><small>${escapeHTML([...group.models].slice(0, 2).join('、') || group.id)}</small></div><div>${number(group.count)}</div><div>${rate.toFixed(1)}%</div><div>${average === null ? '—' : `${number(average)} ms`}</div><div>${p95 === null ? '—' : `${number(Math.round(p95))} ms`}</div><div>${group.tokens ? number(group.tokens) : '—'}</div></div>`;
    }).join('');
  }

  function testableAccounts() {
    return (state?.config?.accounts || []).filter(account => account.enabled !== false && (!Array.isArray(account.operations) || !account.operations.length || account.operations.includes('openai.chat') || account.operations.includes('anthropic.messages')));
  }

  function qualityModel(account) {
    return account.models?.find(model => model && model !== '*') || account.capabilities?.[0]?.upstream_model || Object.keys(account.model_map || {})[0] || '';
  }

  function responseText(response) {
    const content = response?.choices?.[0]?.message?.content ?? response?.choices?.[0]?.text ?? response?.content ?? response?.output_text;
    if (typeof content === 'string') return content.trim();
    if (Array.isArray(content)) return content.map(item => typeof item === 'string' ? item : item?.text || '').join('').trim();
    return '';
  }

  function qualityVerdict(result) {
    if (!result || result.state === 'idle') return ['未测试', ''];
    if (result.state === 'running') return ['测试中', ''];
    if (!result.successes) return ['不可用', 'bad'];
    const availability = result.successes / result.attempts;
    if (availability === 1 && result.p95 < 1200 && result.consistent) return ['优秀', 'good'];
    if (availability >= .67 && result.p95 < 3000) return ['可用', 'good'];
    if (availability >= .34) return ['不稳定', 'warn'];
    return ['不可用', 'bad'];
  }

  function syncQualityRows() {
    const target = $('v10QualityRows');
    if (!target) return;
    const accounts = testableAccounts();
    if (!accounts.length) {
      target.innerHTML = '<div class="v10-empty">没有已启用且支持聊天的连接。</div>';
      return;
    }
    target.innerHTML = accounts.map(account => {
      const result = qualityResults.get(account.id) || { state: 'idle', attempts: 3, successes: 0 };
      const [label, tone] = qualityVerdict(result);
      const availability = result.state === 'idle' ? '—' : result.state === 'running' ? `${result.completed || 0}/3` : `${result.successes}/${result.attempts}`;
      const speed = result.p50 == null ? '—' : `${number(result.p50)} ms`;
      const output = result.state === 'idle' ? '—' : result.successes ? (result.consistent ? '一致' : '有差异') : '失败';
      return `<div class="v10-quality-row ${result.state === 'running' ? 'is-running' : ''}" data-quality-account="${escapeHTML(account.id)}"><div class="v10-quality-channel"><strong>${escapeHTML(account.name || account.id)}</strong><small>${escapeHTML(qualityModel(account) || '未声明测试模型')}</small></div><div class="v10-quality-value">${availability}<small>成功 / 3</small></div><div class="v10-quality-value">${speed}<small>P50</small></div><div class="v10-quality-value">${output}<small>最短回复</small></div><div><span class="v10-quality-result ${tone}">${label}</span></div><button type="button" class="btn-ghost" ${result.state === 'running' ? 'disabled' : ''} onclick="v10TestChannel('${encodeURIComponent(account.id)}')">${result.state === 'running' ? '测试中' : '测试'}</button></div>`;
    }).join('');
  }

  async function testChannel(encodedID) {
    const id = decodeURIComponent(encodedID);
    const account = testableAccounts().find(item => item.id === id);
    if (!account) return;
    const model = qualityModel(account);
    if (!model) {
      qualityResults.set(id, { state: 'done', attempts: 3, successes: 0, errors: ['连接没有可测试模型'] });
      syncQualityRows();
      return;
    }
    const result = { state: 'running', attempts: 3, completed: 0, successes: 0, latencies: [], outputs: [], errors: [] };
    qualityResults.set(id, result);
    syncQualityRows();
    for (let attempt = 0; attempt < 3; attempt += 1) {
      try {
        const data = await api('/prompt-test', {
          method: 'POST',
          body: JSON.stringify({ account_id: id, model, messages: [{ role: 'user', content: '只回复 OK' }], temperature: 0, max_tokens: 8 })
        });
        result.successes += 1;
        result.latencies.push(Number(data.latency_ms) || 0);
        result.outputs.push(responseText(data.response));
      } catch (error) {
        result.errors.push(error.message || '请求失败');
      }
      result.completed = attempt + 1;
      qualityResults.set(id, { ...result });
      syncQualityRows();
    }
    const outputs = result.outputs.map(value => value.replace(/\s+/g, ' ').trim().toLowerCase()).filter(Boolean);
    result.state = 'done';
    result.p50 = percentile(result.latencies, .5);
    result.p95 = percentile(result.latencies, .95) ?? Infinity;
    result.consistent = outputs.length > 0 && new Set(outputs).size === 1;
    qualityResults.set(id, result);
    syncQualityRows();
  }

  async function testAllChannels() {
    const button = $('v10TestAllChannels');
    if (button) { button.disabled = true; button.textContent = '测试中…'; }
    try {
      for (const account of testableAccounts()) await testChannel(encodeURIComponent(account.id));
    } finally {
      if (button) { button.disabled = false; button.textContent = '测试全部'; }
    }
  }

  function methodCard(method) {
    const [kind, title, detail, target] = method;
    const iconName = kind === 'oauth' ? 'external' : kind === 'web' ? 'gateway' : 'key';
    const action = kind === 'oauth'
      ? `startOAuth('${target}')`
      : kind === 'web'
        ? `closeDialog('quickAuthDialog');openWebProxyGuide('${target}')`
        : `v10OpenManualAccount('${target}')`;
    return `<button type="button" class="v10-method-card" ${kind === 'oauth' ? `data-oauth-provider="${escapeHTML(target)}"` : ''} onclick="${action}"><span class="v10-method-icon"><svg class="icon"><use href="#i-${iconName}"></use></svg></span><span><strong>${escapeHTML(title)}</strong><small>${escapeHTML(detail)}</small></span></button>`;
  }

  function selectOnboardingProvider(provider) {
    selectedOnboardingProvider = providers[provider] ? provider : 'custom';
    all('[data-v10-provider]').forEach(button => button.classList.toggle('active', button.dataset.v10Provider === selectedOnboardingProvider));
    const config = providers[selectedOnboardingProvider];
    if ($('v10ProviderSummary')) $('v10ProviderSummary').innerHTML = `<span class="v10-eyebrow">${escapeHTML(config.title)}</span><strong>${escapeHTML(config.title)}</strong><span>${escapeHTML(config.summary)}</span>`;
    if ($('v10MethodGrid')) $('v10MethodGrid').innerHTML = config.methods.map(methodCard).join('');
    if ($('v10OnboardingChecklist')) $('v10OnboardingChecklist').innerHTML = config.checks.map(([title, detail]) => `<div class="v10-check-item"><strong>${escapeHTML(title)}</strong><span>${escapeHTML(detail)}</span></div>`).join('');
    if ($('oauthSession')) $('oauthSession').hidden = true;
  }

  function openManualAccount(template) {
    closeDialog('quickAuthDialog');
    openAccount();
    setTimeout(() => {
      if (typeof selectAccountTemplate === 'function') selectAccountTemplate(template);
      syncManualTemplate();
    }, 0);
  }

  function openImportFromOnboarding() {
    closeDialog('quickAuthDialog');
    openImport();
  }

  function syncManualTemplate() {
    const selected = document.querySelector('#accountPlatformChoices .platform-option.selected');
    const result = $('v10AccountTestResult');
    if (result) { result.className = 'v10-test-result'; result.textContent = '尚未测试'; }
    if (selected && $('accountDialogTitle') && !accountEditingID) $('accountDialogTitle').textContent = `添加 ${selected.querySelector('strong')?.textContent || 'API'} 连接`;
  }

  function parseObjectField(value, label) {
    try {
      const parsed = JSON.parse(value || '{}');
      if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error();
      return parsed;
    } catch (_) {
      throw new Error(`${label}必须是 JSON 对象`);
    }
  }

  function accountFormPayload() {
    const form = $('accountForm');
    const fields = form.elements;
    const mode = fields.credential_mode.value;
    return {
      id: String(fields.id.value || 'connection-test').trim(),
      name: String(fields.name.value || '连接测试').trim(),
      type: fields.type.value,
      adapter_id: accountTemplates[accountTemplateSelected]?.adapter_id || '',
      instance_id: accountTemplates[accountTemplateSelected]?.instance_id || '',
      operations: accountTemplates[accountTemplateSelected]?.operations || [],
      base_url: String(fields.base_url.value || '').trim(),
      api_key: mode === 'key' ? String(fields.api_key.value || '').trim() : '',
      api_key_env: mode === 'env' ? String(fields.api_key_env.value || '').trim() : '',
      auth_header: fields.auth_header.value,
      auth_scheme: String(fields.auth_scheme.value || '').trim(),
      headers: parseObjectField(fields.headers.value, '固定请求头'),
      headers_env: parseObjectField(fields.headers_env.value, '环境变量请求头'),
      proxy_url: String(fields.proxy_url.value || '').trim(),
      models: String(fields.models.value || '').split(',').map(item => item.trim()).filter(Boolean),
      model_map: parseObjectField(fields.model_map.value, '模型映射'),
      concurrency: Number(fields.concurrency.value) || 0,
      priority: Number(fields.priority.value) || 0,
      weight: Number(fields.weight.value) || 1,
      enabled: fields.enabled.value === 'true'
    };
  }

  async function testAccountConnection() {
    const target = $('v10AccountTestResult');
    const button = $('v10TestAccountBtn');
    if (!target || !button) return;
    target.className = 'v10-test-result running';
    target.textContent = '正在验证地址、认证、代理和模型目录…';
    button.disabled = true;
    try {
      const result = await api('/accounts/test', { method: 'POST', body: JSON.stringify({ account: accountFormPayload() }) });
      target.className = `v10-test-result ${result.ok ? 'good' : 'bad'}`;
      target.textContent = result.ok
        ? `连接成功 · HTTP ${result.status} · ${number(result.latency_ms)} ms · 发现 ${number(result.model_count)} 个模型${result.models?.length ? `：${result.models.slice(0, 4).join('、')}` : ''}`
        : `连接失败 · HTTP ${result.status} · ${number(result.latency_ms)} ms · ${result.message}`;
    } catch (error) {
      target.className = 'v10-test-result bad';
      target.textContent = `测试失败：${error.message || '无法连接上游'}`;
    } finally {
      button.disabled = false;
    }
  }

  function syncImportState() {
    const idle = $('v10ImportIdle');
    const result = $('importResult');
    if (idle && result) idle.hidden = !result.hidden;
  }

  function syncAll() {
    syncScheduled = false;
    if (!document.querySelector('.v10-usage')) return;
    syncUsageMetrics();
    syncOverviewStory();
    syncQuotaBoard();
    syncChannelUsage();
    syncQualityRows();
    syncImportState();
  }

  function scheduleSync() {
    if (syncScheduled) return;
    syncScheduled = true;
    requestAnimationFrame(() => requestAnimationFrame(syncAll));
  }

  function wrap(name, after) {
    const original = window[name];
    if (typeof original !== 'function' || original.__nativeV10Wrapped) return;
    const wrapped = function (...args) {
      const result = original.apply(this, args);
      const done = () => { try { after?.(...args); } finally { scheduleSync(); } };
      if (result && typeof result.finally === 'function') result.finally(done); else done();
      return result;
    };
    Object.defineProperty(wrapped, '__nativeV10Wrapped', { value: true });
    window[name] = wrapped;
  }

  function installWrappers() {
    ['render', 'renderMonitor', 'renderOAuthView', 'renderAccounts', 'drawRequestChart', 'loadTrend', 'showView'].forEach(name => wrap(name));
    wrap('openQuickAdd', () => setTimeout(() => selectOnboardingProvider(selectedOnboardingProvider), 0));
    wrap('openAccount', () => setTimeout(syncManualTemplate, 0));
    wrap('selectAccountTemplate', syncManualTemplate);
    wrap('showImportResult', syncImportState);
    wrap('openImport', () => { if ($('v10ImportIdle')) $('v10ImportIdle').hidden = false; });
  }

  function installObservers() {
    const quota = $('oauthAccounts');
    if (quota) new MutationObserver(scheduleSync).observe(quota, { childList: true, subtree: true, characterData: true });
    const importResult = $('importResult');
    if (importResult) new MutationObserver(syncImportState).observe(importResult, { attributes: true, childList: true, subtree: true });
    $('chartRange')?.addEventListener('change', scheduleSync);
    const metricButtons = all('[data-overview-metric]');
    const selectMetric = (button, moveFocus = false) => {
      const metric = button.dataset.overviewMetric;
      metricButtons.forEach(item => {
        const active = item === button;
        item.classList.toggle('active', active);
        item.setAttribute('aria-selected', String(active));
        item.tabIndex = active ? 0 : -1;
      });
      all('[data-overview-chart]').forEach(pane => {
        const active = pane.dataset.overviewChart === metric;
        pane.classList.toggle('active', active);
        pane.setAttribute('aria-hidden', String(!active));
        pane.toggleAttribute('inert', !active);
      });
      if ($('v12TrendTitle')) $('v12TrendTitle').textContent = metric === 'latency' ? 'P95 响应速度' : '调用次数';
      if (moveFocus) button.focus();
      requestAnimationFrame(() => window.drawRequestChart?.());
    };
    metricButtons.forEach(button => {
      button.addEventListener('click', () => selectMetric(button));
      button.addEventListener('keydown', event => {
        if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
        event.preventDefault();
        const current = metricButtons.indexOf(button);
        const next = event.key === 'Home' ? 0 : event.key === 'End' ? metricButtons.length - 1 : (current + (event.key === 'ArrowRight' ? 1 : -1) + metricButtons.length) % metricButtons.length;
        selectMetric(metricButtons[next], true);
      });
    });
    $('v12InsightList')?.addEventListener('click', event => {
      const button = event.target.closest('[data-v12-insight-target]');
      if (!button) return;
      const target = button.dataset.v12InsightTarget;
      if (target === 'accounts' || target === 'routes') {
        showView(target);
        return;
      }
      if (target === 'quality') {
        const panel = document.querySelector('.v10-quality-panel');
        if (!panel) return;
        const behavior = matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth';
        panel.scrollIntoView({ behavior, block: 'start' });
        const heading = panel.querySelector('h2');
        if (heading) {
          heading.tabIndex = -1;
          heading.focus({ preventScroll: true });
        }
      }
    });
  }

  function init() {
    ensureAdditionalTemplates();
    if ($('chartRange') && typeof chartRange !== 'undefined') {
      chartRange = $('chartRange').value;
      if (typeof trendLoadedRange !== 'undefined') trendLoadedRange = '';
    }
    installWrappers();
    installObservers();
    selectOnboardingProvider('codex');
    scheduleSync();
  }

  window.v10SelectOnboardingProvider = selectOnboardingProvider;
  window.v10OpenManualAccount = openManualAccount;
  window.v10OpenImportFromOnboarding = openImportFromOnboarding;
  window.v10TestAccountConnection = testAccountConnection;
  window.v10TestChannel = testChannel;
  window.v10TestAllChannels = testAllChannels;
  window.Lite2APINativeV10 = Object.freeze({ sync: syncAll, testChannel, testAllChannels });

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();
