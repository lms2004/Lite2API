/* Usage controller. Dependencies are supplied by the composition root. */
globalThis.Lite2APIUsage = function createUsage(context) {
  'use strict';
  const {
    all,
    Runtime,
    refreshAll,
    openChannelChat,
    now,
    RANGE_MS,
    usageState,
    resourcesState,
    navigationState,
    finiteNumber,
    percentile,
    requestOK,
    quotaWindows,
    quotaPercentage,
    $,
    relativeTime,
    number,
    RANGE_LABEL,
    connectionStats,
    setHTML,
    escapeHTML,
    quotaTone,
    providerKey,
    accountStatus,
    quotaWindowHTML,
    providerIconHTML,
    PROVIDER_LABEL,
    accountFingerprint,
    Core,
    connectionProviderKey,
    configuredAccount,
    accountTestModel,
    toast,
    request,
    sleep,
    formatTime,
    modelLabelHTML,
    modelIconHTML,
  } = context;
  let chartSignature = '';
  function recordsInRange() {
    const cutoff = now() - (RANGE_MS[usageState.range] || RANGE_MS['24h']);
    return (resourcesState.data?.stats?.recent || []).filter((record) => {
      const time = new Date(record.time).getTime();
      if (!Number.isFinite(time) || time < cutoff) return false;
      const accountID = usageState.metric === 'quota' ? record.credential_id : record.account_id;
      if (usageState.accountFilter && accountID !== usageState.accountFilter) return false;
      return true;
    });
  }

  function usageEvidence() {
    const records = recordsInRange(),
      latencies = records.map((record) => finiteNumber(record.latency_ms)).filter((value) => value !== null),
      p95 = percentile(latencies, 0.95),
      trendPoints = Array.isArray(resourcesState.trend?.points) ? resourcesState.trend.points : null,
      filtered = !!usageState.accountFilter;
    let calls, failed, source;
    if (!filtered && trendPoints) {
      calls = trendPoints.reduce((sum, point) => sum + (Number(point.requests) || 0), 0);
      failed = trendPoints.reduce((sum, point) => sum + (Number(point.failed) || 0), 0);
      source = 'trend';
    } else {
      calls = records.length;
      failed = records.filter((record) => !requestOK(record)).length;
      source = 'recent';
    }
    const successful = Math.max(0, calls - failed),
      successRate = calls ? (successful / calls) * 100 : null;
    return { records, latencies, p95, calls, failed, successful, successRate, source };
  }

  function allQuotaObservations() {
    return resourcesState.oauth
      .filter(
        (account) =>
          usageState.metric !== 'quota' ||
          !usageState.accountFilter ||
          account.id === usageState.accountFilter,
      )
      .flatMap((account) =>
        quotaWindows(account)
          .map((window) => ({ account, window, used: quotaPercentage(window) }))
          .filter((item) => item.used !== null),
      );
  }

  function tightestQuota() {
    return allQuotaObservations().sort((a, b) => b.used - a.used)[0] || null;
  }

  function recordQuotaHistory() {
    const key = 'canonical_quota_history_v1';
    try {
      if (!usageState.quotaHistory.length)
        usageState.quotaHistory = JSON.parse(localStorage.getItem(key) || '[]');
      const history = usageState.quotaHistory;
      const latest = new Map(history.map((item) => [item.id, item]));
      let changed = false;
      for (const account of resourcesState.oauth)
        for (const window of quotaWindows(account)) {
          const used = quotaPercentage(window);
          if (used === null) continue;
          const id = account.id + '|' + window.kind + '|' + (window.model || '');
          const time = window.observed_at || new Date().toISOString();
          const previous = latest.get(id);
          if (previous && previous.used === used && now() - new Date(previous.time).getTime() < 300000)
            continue;
          const sample = {
            id,
            account_id: account.id,
            kind: window.kind,
            model: window.model || '',
            used,
            time,
            label: window.model || window.label || window.kind,
          };
          history.push(sample);
          latest.set(id, sample);
          changed = true;
        }
      if (!changed) return;
      usageState.quotaHistory = history
        .filter((item) => new Date(item.time).getTime() >= now() - 30 * 86400000)
        .slice(-6000);
      localStorage.setItem(key, JSON.stringify(usageState.quotaHistory));
    } catch {
      usageState.quotaHistory = [];
    }
  }

  function renderUsage() {
    if (!resourcesState.data) return;
    renderUsageFilters();
    const evidence = usageEvidence(),
      { records, successful, failed, successRate, latencies, p95, calls, source } = evidence,
      quota = tightestQuota(),
      recentLimit = resourcesState.data.stats?.recent_limit || records.length;
    $('quotaKpi').textContent = quota ? `${quota.used.toFixed(1)}%` : '—';
    $('quotaKpiNote').textContent = quota
      ? `${quota.window.model || quota.window.label || quota.window.kind} · ${quota.window.reset_at ? relativeTime(quota.window.reset_at) + '重置' : '重置时间未知'}`
      : resourcesState.oauthError
        ? '认证适配器不可读'
        : '暂无上游额度观测';
    $('quotaKpiNote').title = quota?.account.identity || quota?.account.id || '';
    $('callsKpi').textContent = number(calls);
    $('callsKpiNote').textContent =
      source === 'trend'
        ? `${RANGE_LABEL[usageState.range]}完整聚合 · ${number(failed)} 次失败`
        : `最近请求样本 · 最多 ${number(recentLimit)} 条`;
    $('successKpi').textContent = successRate === null ? '—' : `${successRate.toFixed(2)}%`;
    $('successKpiNote').textContent = calls
      ? `${number(successful)} 成功 / ${number(failed)} 失败${source === 'recent' ? ' · 样本口径' : ''}`
      : '没有真实请求';
    $('latencyKpi').textContent = p95 === null ? '—' : `${number(Math.round(p95))} ms`;
    $('latencyKpiNote').textContent = latencies.length
      ? `最近 ${number(latencies.length)} 个完成请求样本`
      : '没有延迟样本';
    const fastest = connectionStats()
        .filter((item) => item.samples > 0)
        .sort((a, b) => a.avg - b.avg)[0],
      unhealthy = connectionStats()
        .filter((item) => item.failures > 0)
        .sort((a, b) => b.failureRate - a.failureRate)[0];
    const clauses = [];
    if (quota)
      clauses.push(
        `${quota.account.identity || quota.account.id} 的 ${quota.window.model || quota.window.label || '额度'} 已用 ${quota.used.toFixed(1)}%`,
      );
    if (calls)
      clauses.push(
        `${RANGE_LABEL[usageState.range]}调用 ${number(calls)} 次，成功率 ${successRate.toFixed(2)}%${source === 'recent' ? '（最近样本）' : ''}`,
      );
    if (fastest) clauses.push(`${fastest.name} 当前最快`);
    if (unhealthy && unhealthy.failureRate >= 0.05) clauses.push(`${unhealthy.name} 需要关注`);
    $('usageHeadline').textContent = clauses.length
      ? clauses.join('；') + '。'
      : '当前还没有足够的真实使用数据。';
    renderInsights({ records, quota, fastest, unhealthy, p95 });
    renderQuotaBoard();
    renderQuality();
    renderRequests();
    drawUsageChart();
  }

  function renderUsageFilters() {
    const select = $('usageAccountFilter'),
      selected = usageState.accountFilter,
      quota = usageState.metric === 'quota',
      accounts = quota
        ? resourcesState.oauth.map((account) => ({ id: account.id, name: account.identity || account.id }))
        : resourcesState.data?.accounts || [];
    setHTML(
      select,
      '<option value="">' +
        (quota ? '全部账号' : '全部渠道') +
        '</option>' +
        accounts
          .map(
            (account) =>
              `<option value="${escapeHTML(account.id)}">${escapeHTML(account.name || account.id)}</option>`,
          )
          .join(''),
    );
    if (accounts.some((account) => account.id === selected)) select.value = selected;
    else usageState.accountFilter = '';
  }

  function renderInsights({ quota, fastest, unhealthy }) {
    const items = [];
    if (unhealthy?.failures && unhealthy.failureRate >= 0.05)
      items.push({
        label: '失败较多',
        text: unhealthy.name + ' · ' + unhealthy.failures + '/' + unhealthy.samples + ' 次失败',
        tone: unhealthy.failureRate >= 0.2 ? 'bad' : 'warn',
      });
    if (resourcesState.oauthError)
      items.push({ label: '额度未更新', text: '认证服务暂时不可读，当前保留上次观测。', tone: 'warn' });
    if (quota && quota.used >= 82)
      items.push({
        label: '额度提醒',
        text: (quota.account.identity || quota.account.id) + ' · 已用 ' + quota.used.toFixed(1) + '%',
        tone: quotaTone(quota.used),
      });
    $('usageInsights').hidden = !items.length;
    setHTML(
      $('usageInsights'),
      items
        .map(
          (item) =>
            '<div class="insight-item ' +
            item.tone +
            '"><span>' +
            escapeHTML(item.label) +
            '</span><strong>' +
            escapeHTML(item.text) +
            '</strong></div>',
        )
        .join(''),
    );
  }

  function renderQuotaBoard() {
    const rows = resourcesState.oauth;
    if (!rows.length) {
      setHTML(
        $('quotaBoard'),
        `<div class="empty-copy"><strong>${resourcesState.oauthError ? '认证额度暂时不可读' : '还没有认证账号'}</strong><br>${resourcesState.oauthError ? '认证服务恢复后会自动刷新；其他使用数据不受影响。' : '添加 OAuth 或订阅账号后显示真实额度。'}</div>`,
      );
      return;
    }
    setHTML(
      $('quotaBoard'),
      rows
        .map((account) => {
          const key = providerKey(account.provider),
            status = accountStatus(account),
            windows = quotaWindows(account),
            tightest = windows
              .map((window) => ({ window, used: quotaPercentage(window) }))
              .filter((item) => item.used !== null)
              .sort((a, b) => b.used - a.used)[0],
            windowHTML = windows.length
              ? windows.slice(0, 4).map(quotaWindowHTML).join('')
              : '<div class="quota-unknown">暂无上游额度观测</div>';
          const caption = tightest
            ? tightest.window.model || tightest.window.label || tightest.window.kind
            : '暂无额度观测';
          return `<article class="quota-account"><details class="quota-details" data-ui-key="quota-${escapeHTML(account.id)}"><summary><div class="quota-identity">${providerIconHTML(key)}<div><strong title="${escapeHTML(account.identity || account.id)}">${escapeHTML(account.identity || '已保存凭据')}</strong><span>${escapeHTML(status.tone ? status.label : account.plan || PROVIDER_LABEL[key] || account.provider)}</span></div></div><div class="quota-reading ${tightest ? quotaTone(tightest.used) : status.tone}"><strong>${tightest ? tightest.used.toFixed(0) + '%' : '—'}</strong><small title="${escapeHTML(caption)}">${escapeHTML(caption)}</small></div><span class="disclosure-chevron" aria-hidden="true"></span></summary><div class="quota-detail-content"><div class="quota-windows">${windowHTML}</div><div class="quota-status"><span class="status-pill ${status.tone}">${escapeHTML(status.label)}</span><small>${escapeHTML(account.updated_at ? relativeTime(account.updated_at) + '更新' : '更新时间未知')}</small></div></div></details></article>`;
        })
        .join(''),
    );
  }

  function qualityKey() {
    return 'canonical_quality_results_v1';
  }

  function loadPersistedQuality() {
    try {
      const saved = JSON.parse(localStorage.getItem(qualityKey()) || '{}');
      usageState.quality = new Map(Object.entries(saved));
    } catch {
      usageState.quality = new Map();
    }
  }

  function saveQuality() {
    try {
      localStorage.setItem(qualityKey(), JSON.stringify(Object.fromEntries(usageState.quality)));
    } catch {}
  }

  function qualityConclusion(result) {
    if (!result) return { label: '未测试', tone: 'neutral' };
    if (result.running) return { label: '测试中', tone: 'neutral' };
    const successes = result.rounds.filter((round) => round.ok).length;
    if (!successes) return { label: '不可用', tone: 'bad' };
    const values = result.rounds.filter((round) => round.ok).map((round) => round.latency_ms),
      avg = values.reduce((a, b) => a + b, 0) / values.length,
      spread = values.length > 1 ? (Math.max(...values) - Math.min(...values)) / Math.max(1, avg) : 0;
    if (successes < result.rounds.length) return { label: '有失败', tone: 'warn' };
    if (spread > 0.45) return { label: '波动较大', tone: 'warn' };
    return { label: '稳定', tone: '' };
  }

  function currentQualityResult(account) {
    const result = usageState.quality.get(account.id);
    if (!result) return null;
    const age = now() - new Date(result.tested_at).getTime();
    if (
      result.connection_fingerprint !== accountFingerprint(account) ||
      !Number.isFinite(age) ||
      age > 24 * 3600000
    )
      return null;
    return result;
  }

  function renderQuality() {
    const rows = connectionStats();
    setHTML(
      $('qualityRows'),
      rows
        .map((item) => {
          const result = currentQualityResult(item.config),
            conclusion = qualityConclusion(result),
            successRate = item.samples
              ? `${(((item.samples - item.failures) / item.samples) * 100).toFixed(1)}%`
              : '—',
            avg = Number.isFinite(item.avg) ? `${number(Math.round(item.avg))} ms` : '—',
            p95 = item.p95 !== null ? `${number(Math.round(item.p95))} ms` : '—',
            chatAvailable = item.config.enabled !== false && Core.channelChatSupported(item.config),
            key = connectionProviderKey(item.config);
          const roundLabels = result
            ? result.rounds.map(
                (round, index) =>
                  `第 ${index + 1} 轮${round.ok ? `成功，${round.latency_ms} ms` : `失败，${round.error || '未知错误'}`}`,
              )
            : [];
          const run = result
            ? result.rounds
                .map(
                  (round) =>
                    `<i class="${round.ok ? 'good' : 'bad'}" title="${escapeHTML(round.ok ? round.latency_ms + ' ms' : round.error || '失败')}" aria-hidden="true"></i>`,
                )
                .join('')
            : '<i aria-hidden="true"></i><i aria-hidden="true"></i><i aria-hidden="true"></i>';
          const runLabel = roundLabels.length ? `三轮直测：${roundLabels.join('；')}` : '尚未执行三轮直测';
          return `<tr data-quality-id="${escapeHTML(item.id)}"><td data-label="渠道"><div class="quota-identity">${providerIconHTML(key)}<div class="cell-main"><strong>${escapeHTML(item.name)}</strong><span>${escapeHTML(item.id)}</span></div></div></td><td data-label="最近使用" class="numeric">${item.samples} 次</td><td data-label="成功率" class="numeric">${successRate}</td><td data-label="平均 / P95" class="numeric">${avg} / ${p95}</td><td data-label="三轮直测"><span class="quality-run" role="img" aria-label="${escapeHTML(runLabel)}">${run}</span></td><td data-label="质量结论"><span class="status-pill ${conclusion.tone}">${conclusion.label}</span></td><td data-label="操作"><div class="row-actions"><button type="button" data-action="channel-chat" data-id="${escapeHTML(item.id)}" ${chatAvailable ? '' : 'disabled'}>聊天</button><button type="button" data-action="quality-test" data-id="${escapeHTML(item.id)}" ${usageState.qualityRunning.has(item.id) ? 'disabled' : ''}>${usageState.qualityRunning.has(item.id) ? '测试中' : '直测 3 轮'}</button></div></td></tr>`;
        })
        .join('') || '<tr><td colspan="7" class="empty-copy">还没有 API 连接</td></tr>',
    );
  }

  async function runQuality(id) {
    const account = configuredAccount(id);
    if (!account || usageState.qualityRunning.has(id)) return;
    const model = accountTestModel(account);
    if (!model) {
      toast('该连接没有可测试模型', true);
      return;
    }
    const connectionFingerprint = accountFingerprint(account),
      result = {
        running: true,
        rounds: [],
        tested_at: new Date().toISOString(),
        model,
        connection_fingerprint: connectionFingerprint,
      };
    usageState.qualityRunning.add(id);
    usageState.quality.set(id, result);
    renderQuality();
    try {
      for (let index = 0; index < 3; index++) {
        try {
          const response = await request('/prompt-test', {
              method: 'POST',
              timeout: 125000,
              body: JSON.stringify({
                account_id: id,
                model,
                messages: [{ role: 'user', content: '质量探针：只回复 OK' }],
                temperature: 0,
                max_tokens: 16,
              }),
            }),
            content = JSON.stringify(response.response || {});
          result.rounds.push({
            ok: content.length > 2,
            latency_ms: Number(response.latency_ms) || 0,
            request_id: response.request_id || '',
            model: response.upstream_model || model,
          });
        } catch (error) {
          result.rounds.push({ ok: false, error: error.message, status: error.status || 0 });
        }
        usageState.quality.set(id, result);
        renderQuality();
        if (index < 2) await sleep(180);
      }
      result.running = false;
      result.tested_at = new Date().toISOString();
      usageState.quality.set(id, result);
      saveQuality();
      toast(`${account.name || id} 三轮直测完成`);
    } finally {
      usageState.qualityRunning.delete(id);
      renderQuality();
    }
  }

  async function runAllQuality() {
    const button = $('testAllChannels');
    button.disabled = true;
    try {
      for (const account of resourcesState.data?.config?.accounts || []) {
        if (account.enabled !== false && accountTestModel(account)) await runQuality(account.id);
      }
    } finally {
      button.disabled = false;
    }
  }

  function chartBucketsFromRecords(metric) {
    const records = recordsInRange(),
      range = RANGE_MS[usageState.range],
      bucket = usageState.range === '24h' ? 3600000 : usageState.range === '7d' ? 6 * 3600000 : 24 * 3600000,
      start = Math.floor((now() - range) / bucket) * bucket,
      count = Math.ceil(range / bucket) + 1,
      buckets = Array.from({ length: count }, (_, index) => ({ time: start + index * bucket, records: [] }));
    for (const record of records) {
      const time = new Date(record.time).getTime(),
        index = Math.floor((time - start) / bucket);
      if (index >= 0 && index < buckets.length) buckets[index].records.push(record);
    }
    return buckets.map((bucketItem) => {
      const rows = bucketItem.records,
        latencies = rows.map((row) => finiteNumber(row.latency_ms)).filter((value) => value !== null);
      let value = null;
      if (rows.length && metric === 'requests') value = rows.length;
      if (rows.length && metric === 'tokens') value = Core.meteredTokenTotal(rows);
      if (metric === 'success')
        value = rows.length ? (rows.filter(requestOK).length / rows.length) * 100 : null;
      if (metric === 'latency') value = percentile(latencies, 0.95);
      return { time: bucketItem.time, value, count: rows.length };
    });
  }

  function chartSeries() {
    if (usageState.metric === 'quota') {
      const cutoff = now() - (RANGE_MS[usageState.range] || RANGE_MS['24h']),
        items = usageState.quotaHistory.filter(
          (item) =>
            new Date(item.time).getTime() >= cutoff &&
            (!usageState.accountFilter || item.account_id === usageState.accountFilter),
        );
      let target = '';
      if (usageState.accountFilter) {
        target = items[0]?.id || '';
      } else {
        const quota = tightestQuota();
        target = quota
          ? `${quota.account.id}|${quota.window.kind}|${quota.window.model || ''}`
          : items[0]?.id || '';
      }
      return items
        .filter((item) => !target || item.id === target)
        .map((item) => ({
          time: new Date(item.time).getTime(),
          value: item.used,
          count: 1,
          label: item.label,
        }))
        .sort((a, b) => a.time - b.time);
    }
    if (usageState.accountFilter || ['tokens', 'success'].includes(usageState.metric))
      return chartBucketsFromRecords(usageState.metric);
    if (resourcesState.trend?.points?.length && ['requests', 'latency'].includes(usageState.metric)) {
      return resourcesState.trend.points
        .map((point) => ({
          time: new Date(point.time).getTime(),
          value:
            usageState.metric === 'requests'
              ? Number(point.requests)
              : point.p95_latency_ms === null
                ? null
                : Number(point.p95_latency_ms),
          count: Number(point.requests) || 0,
        }))
        .filter((point) => Number.isFinite(point.time));
    }
    return chartBucketsFromRecords(usageState.metric);
  }

  function metricLabel() {
    return {
      requests: '调用次数',
      tokens: 'Token',
      success: '成功率',
      latency: 'P95 响应耗时',
      quota: '额度已用比例',
    }[usageState.metric];
  }

  function formatMetricValue(value) {
    if (value === null || !Number.isFinite(Number(value))) return '—';
    if (['success', 'quota'].includes(usageState.metric)) return `${Number(value).toFixed(1)}%`;
    if (usageState.metric === 'latency') return `${number(Math.round(value))} ms`;
    return number(Math.round(value));
  }

  function formatAxisMetricValue(value, span) {
    if (['requests', 'tokens'].includes(usageState.metric) && span < 10)
      return Number(value).toLocaleString('zh-CN', { maximumFractionDigits: 1 });
    return formatMetricValue(value);
  }

  function renderChartData(series) {
    const valid = series.filter((point) => point.value !== null && Number.isFinite(Number(point.value))),
      displayed = valid.slice(-240),
      truncated = displayed.length < valid.length,
      label = `${RANGE_LABEL[usageState.range]} ${metricLabel()}`;
    $('chartDataCaption').textContent = `${label}数据${truncated ? '（仅列出最近 240 个点）' : ''}`;
    $('chartDataSummary').textContent =
      `${number(valid.length)} 个数据点${truncated ? ' · 表格显示最近 240 个' : ''}`;
    if ($('chartDataDisclosure').open)
      setHTML(
        $('chartDataRows'),
        displayed
          .map(
            (point) =>
              `<tr><td>${escapeHTML(formatTime(point.time))}</td><td>${escapeHTML(formatMetricValue(point.value))}</td><td>${point.count === undefined ? '—' : number(point.count)}</td></tr>`,
          )
          .join('') || '<tr><td colspan="3" class="empty-copy">当前范围没有可绘制的真实数据</td></tr>',
      );
    const summary = valid.length
      ? `${label}，共 ${number(valid.length)} 个真实数据点。可用左右方向键逐点浏览。`
      : `${label}，当前没有可绘制的真实数据。`;
    $('usageChart').setAttribute('aria-label', summary);
    $('chartA11ySummary').textContent = summary;
  }

  function drawUsageChart() {
    const canvas = $('usageChart');
    if (!canvas || !canvas.isConnected || navigationState.activeView !== 'usage' || document.hidden) return;
    const series = chartSeries();
    renderChartData(series);
    const box = canvas.getBoundingClientRect(),
      width = Math.max(1, Math.round(box.width)),
      height = Math.max(1, Math.round(box.height)),
      dpr = Math.min(devicePixelRatio || 1, 2);
    const signature = JSON.stringify([
      width,
      height,
      dpr,
      usageState.range,
      usageState.metric,
      usageState.accountFilter,
      series,
    ]);
    if (usageState.chartIndex < 0) $('chartTooltip').hidden = true;
    if (signature === chartSignature) return;
    chartSignature = signature;
    canvas.width = width * dpr;
    canvas.height = height * dpr;
    const ctx = canvas.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);
    const pad = { left: 52, right: 16, top: 18, bottom: 30 },
      plotW = width - pad.left - pad.right,
      plotH = height - pad.top - pad.bottom,
      valid = series.filter((point) => point.value !== null && Number.isFinite(Number(point.value))),
      sampleBased = usageState.accountFilter || ['tokens', 'success'].includes(usageState.metric);
    $('chartSource').textContent =
      `${RANGE_LABEL[usageState.range]} · ${metricLabel()} · ${usageState.metric === 'quota' ? '浏览器额度快照' : sampleBased ? '最近请求样本' : '全局区间记录'}`;
    if (valid.length < 1) {
      ctx.fillStyle = '#67676e';
      ctx.font = '12px ' + getComputedStyle(document.body).fontFamily;
      ctx.textAlign = 'center';
      ctx.fillText('当前范围没有可绘制的真实数据', width / 2, height / 2);
      usageState.chartPoints = [];
      usageState.chartIndex = -1;
      return;
    }
    const minTime = Math.min(...series.map((point) => point.time)),
      maxTime = Math.max(...series.map((point) => point.time)),
      values = valid.map((point) => Number(point.value)),
      minValue = ['success', 'quota'].includes(usageState.metric) ? 0 : Math.min(0, ...values),
      maxValue = ['success', 'quota'].includes(usageState.metric) ? 100 : Math.max(1, ...values) * 1.08;
    ctx.font = '12px ' + getComputedStyle(document.body).fontFamily;
    ctx.textBaseline = 'middle';
    for (let row = 0; row < 5; row++) {
      const ratio = row / 4,
        y = pad.top + ratio * plotH,
        value = maxValue - (maxValue - minValue) * ratio;
      ctx.strokeStyle = '#ececef';
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(pad.left, y);
      ctx.lineTo(width - pad.right, y);
      ctx.stroke();
      ctx.fillStyle = '#67676e';
      ctx.textAlign = 'right';
      ctx.fillText(formatAxisMetricValue(value, maxValue - minValue), pad.left - 7, y);
    }
    const points = [];
    let drawing = false;
    ctx.beginPath();
    for (const point of series) {
      if (point.value === null || !Number.isFinite(Number(point.value))) {
        drawing = false;
        continue;
      }
      const x = pad.left + ((point.time - minTime) / Math.max(1, maxTime - minTime)) * plotW,
        y = pad.top + ((maxValue - Number(point.value)) / Math.max(1, maxValue - minValue)) * plotH;
      points.push({ ...point, x, y });
      if (drawing) ctx.lineTo(x, y);
      else {
        ctx.moveTo(x, y);
        drawing = true;
      }
    }
    ctx.strokeStyle = '#1976d2';
    ctx.lineWidth = 2.4;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.stroke();
    ctx.fillStyle = '#1976d2';
    for (const point of points) {
      ctx.beginPath();
      ctx.arc(point.x, point.y, points.length > 80 ? 1.3 : 2.2, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.fillStyle = '#67676e';
    ctx.textBaseline = 'bottom';
    ctx.textAlign = 'left';
    const formatAxis = (time) =>
      new Date(time).toLocaleString(
        'zh-CN',
        usageState.range === '24h'
          ? { hour: '2-digit', minute: '2-digit' }
          : { month: '2-digit', day: '2-digit' },
      );
    ctx.fillText(formatAxis(minTime), pad.left, height - 2);
    ctx.textAlign = 'right';
    ctx.fillText(formatAxis(maxTime), width - pad.right, height - 2);
    usageState.chartPoints = points;
    if (usageState.chartIndex >= points.length) usageState.chartIndex = points.length - 1;
    if ((document.activeElement === canvas || !$('chartTooltip').hidden) && usageState.chartIndex >= 0)
      showChartPoint(points[usageState.chartIndex], usageState.chartIndex);
  }

  function chartPointDescription(point) {
    return `${metricLabel()} ${formatMetricValue(point.value)}，${formatTime(point.time)}${point.count !== undefined ? `，样本 ${number(point.count)}` : ''}`;
  }

  function showChartPoint(point, index) {
    if (!point) return;
    const canvas = $('usageChart'),
      tooltip = $('chartTooltip'),
      rect = canvas.getBoundingClientRect();
    usageState.chartIndex = index;
    tooltip.hidden = false;
    setHTML(
      tooltip,
      `<strong>${escapeHTML(metricLabel())} ${escapeHTML(formatMetricValue(point.value))}</strong><br>${escapeHTML(formatTime(point.time))}${point.count !== undefined ? `<br>样本 ${number(point.count)}` : ''}`,
    );
    tooltip.style.left = canvas.offsetLeft + Math.min(rect.width - 150, Math.max(8, point.x + 10)) + 'px';
    tooltip.style.top = canvas.offsetTop + Math.max(8, point.y - 24) + 'px';
    $('chartA11ySummary').textContent =
      `第 ${index + 1} / ${usageState.chartPoints.length} 个数据点：${chartPointDescription(point)}`;
  }

  function chartPointer(event) {
    if (!usageState.chartPoints.length) return;
    const rect = $('usageChart').getBoundingClientRect(),
      x = event.clientX - rect.left;
    const points = usageState.chartPoints;
    let low = 0,
      high = points.length - 1;
    while (low < high) {
      const middle = (low + high) >>> 1;
      if (points[middle].x < x) low = middle + 1;
      else high = middle;
    }
    let index = low;
    if (index > 0 && Math.abs(points[index - 1].x - x) <= Math.abs(points[index].x - x)) index--;
    if (index === usageState.chartIndex && !$('chartTooltip').hidden) return;
    showChartPoint(usageState.chartPoints[index], index);
  }

  function chartKeyboard(event) {
    if (
      !usageState.chartPoints.length ||
      !['ArrowLeft', 'ArrowRight', 'Home', 'End', 'Escape'].includes(event.key)
    )
      return;
    event.preventDefault();
    if (event.key === 'Escape') {
      $('chartTooltip').hidden = true;
      return;
    }
    let index = usageState.chartIndex >= 0 ? usageState.chartIndex : usageState.chartPoints.length - 1;
    if (event.key === 'ArrowLeft') index = Math.max(0, index - 1);
    if (event.key === 'ArrowRight') index = Math.min(usageState.chartPoints.length - 1, index + 1);
    if (event.key === 'Home') index = 0;
    if (event.key === 'End') index = usageState.chartPoints.length - 1;
    showChartPoint(usageState.chartPoints[index], index);
  }

  function renderRequests() {
    const credentials = new Map(resourcesState.oauth.map((account) => [account.id, account]));
    const search = $('requestSearch').value.trim().toLowerCase(),
      status = $('requestStatus').value,
      rows = recordsInRange().filter((record) => {
        const credential = credentials.get(record.credential_id),
          text = [
            record.model,
            record.upstream_model,
            record.account_id,
            record.credential_id,
            credential?.identity,
            record.error,
          ]
            .join(' ')
            .toLowerCase();
        return (
          (!search || text.includes(search)) &&
          (!status || (status === 'success' ? requestOK(record) : !requestOK(record)))
        );
      });
    $('requestSummary').textContent =
      `${rows.length} 条 · 最近样本上限 ${resourcesState.data?.stats?.recent_limit || rows.length}`;
    if (!$('requestDisclosure').open) return;
    const pages = Math.max(1, Math.ceil(rows.length / 30));
    usageState.requestPage = Math.max(1, Math.min(usageState.requestPage, pages));
    $('requestPageInfo').textContent = usageState.requestPage + ' / ' + pages + ' 页';
    $('requestPrevious').disabled = usageState.requestPage === 1;
    $('requestNext').disabled = usageState.requestPage === pages;
    setHTML(
      $('requestRows'),
      rows
        .slice((usageState.requestPage - 1) * 30, usageState.requestPage * 30)
        .map((record) => {
          const latency = finiteNumber(record.latency_ms),
            account = configuredAccount(record.account_id),
            credential = credentials.get(record.credential_id),
            key = connectionProviderKey(account || { id: record.account_id }),
            credentialText =
              credential?.identity ||
              (record.credential_id
                ? `账号 ${record.credential_id}`
                : String(account?.adapter_id || '').toLowerCase() === 'cli-proxy-api'
                  ? '自动账号池'
                  : ''),
            credentialLine = credentialText
              ? `<span title="${escapeHTML(record.credential_id || '')}">${escapeHTML(credentialText)}</span>`
              : '';
          return `<tr><td data-label="时间">${escapeHTML(formatTime(record.time))}</td><td data-label="客户端模型"><div class="cell-main"><strong>${modelLabelHTML(record.model, 'request-model-label')}</strong><span>${escapeHTML(record.request_id || '')}</span></div></td><td data-label="真实上游"><div class="request-channel-cell">${providerIconHTML(key, 'provider-mini')}<div><strong>${escapeHTML(record.account_id || '—')}</strong>${credentialLine}<span class="route-preview-model">${modelIconHTML(record.upstream_model, 'inline-model-icon')}${escapeHTML(record.upstream_model || '—')}</span></div></div></td><td data-label="Token" class="numeric">${record.usage_available ? `${number(record.input_tokens)} / ${number(record.output_tokens)}` : '—'}</td><td data-label="结果"><span class="status-pill ${requestOK(record) ? '' : 'bad'}">${requestOK(record) ? '成功' : `HTTP ${record.status || '—'}`}</span></td><td data-label="耗时" class="numeric">${latency === null ? '—' : number(latency) + ' ms'}</td><td data-label="错误"><span class="request-error" title="${escapeHTML(record.error || '')}">${escapeHTML(record.error || '—')}</span></td></tr>`;
        })
        .join('') || '<tr><td colspan="7" class="empty-copy">没有匹配的请求</td></tr>',
    );
  }

  function bind() {
    all('[data-range]').forEach((button) =>
      button.addEventListener('click', () => {
        usageState.range = button.dataset.range;
        usageState.chartIndex = -1;
        resourcesState.trend = null;
        usageState.requestPage = 1;
        all('[data-range]').forEach((item) => {
          const active = item === button;
          item.classList.toggle('active', active);
          item.setAttribute('aria-pressed', String(active));
        });
        refreshAll(true);
      }),
    );
    $('usageMetric').addEventListener('change', (event) => {
      if (usageState.metric === 'quota' || event.target.value === 'quota') usageState.accountFilter = '';
      usageState.metric = event.target.value;
      usageState.chartIndex = -1;
      renderUsage();
    });
    $('usageAccountFilter').addEventListener('change', (event) => {
      usageState.accountFilter = event.target.value;
      usageState.chartIndex = -1;
      renderUsage();
    });
    const usageChart = $('usageChart');
    usageChart.addEventListener('pointermove', chartPointer);
    usageChart.addEventListener('pointerleave', () => {
      if (document.activeElement !== usageChart) $('chartTooltip').hidden = true;
    });
    usageChart.addEventListener('keydown', chartKeyboard);
    usageChart.addEventListener('focus', () => {
      if (usageState.chartPoints.length)
        showChartPoint(
          usageState.chartPoints[
            usageState.chartIndex >= 0 ? usageState.chartIndex : usageState.chartPoints.length - 1
          ],
          usageState.chartIndex >= 0 ? usageState.chartIndex : usageState.chartPoints.length - 1,
        );
    });
    usageChart.addEventListener('blur', () => ($('chartTooltip').hidden = true));
    const redrawChart = Runtime.debounce(() => {
      if (navigationState.activeView === 'usage') drawUsageChart();
    }, 80);
    window.addEventListener('resize', redrawChart, { passive: true });
    const filterRequests = () => {
      usageState.requestPage = 1;
      renderRequests();
    };
    $('requestSearch').addEventListener('input', Runtime.debounce(filterRequests));
    $('requestStatus').addEventListener('change', filterRequests);
    $('requestDisclosure').addEventListener('toggle', () => {
      if ($('requestDisclosure').open) renderRequests();
    });
    $('requestPrevious').addEventListener('click', () => {
      usageState.requestPage--;
      renderRequests();
    });
    $('requestNext').addEventListener('click', () => {
      usageState.requestPage++;
      renderRequests();
    });
    $('chartDataDisclosure').addEventListener('toggle', () => {
      if ($('chartDataDisclosure').open) drawUsageChart();
    });
    $('openChannelChatButton').addEventListener('click', () => openChannelChat());
    $('testAllChannels').addEventListener('click', runAllQuality);
    $('qualityRows').addEventListener('click', (event) => {
      const button = event.target.closest('[data-action]');
      if (!button) return;
      if (button.dataset.action === 'quality-test') runQuality(button.dataset.id);
      if (button.dataset.action === 'channel-chat') openChannelChat(button.dataset.id);
    });
  }

  return Object.freeze({ bind, loadPersistedQuality, recordQuotaHistory, saveQuality, renderUsage });
};
