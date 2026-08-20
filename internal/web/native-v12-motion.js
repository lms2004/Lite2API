/* Lite2API Native v12 motion — restrained, interruptible interaction feedback
   and truthful, monotone-smoothed operational charts. */
(() => {
  'use strict';

  const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)');
  const chartFrames = new WeakMap();
  const $ = id => document.getElementById(id);
  const finite = value => value !== null && value !== undefined && Number.isFinite(Number(value));
  const cssColor = (name, fallback) => getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
  const easeOut = value => 1 - Math.pow(1 - value, 3);

  function splitRuns(points) {
    const runs = [];
    let run = [];
    points.forEach(point => {
      if (point) run.push(point);
      else if (run.length) { runs.push(run); run = []; }
    });
    if (run.length) runs.push(run);
    return runs;
  }

  // Fritsch–Carlson monotone cubic interpolation. It rounds corners without
  // inventing overshoot above or below the observed values.
  function monotonePath(ctx, points) {
    if (!points.length) return;
    ctx.moveTo(points[0].x, points[0].y);
    if (points.length === 1) return;
    const slopes = points.slice(1).map((point, index) => {
      const previous = points[index];
      return (point.y - previous.y) / Math.max(1, point.x - previous.x);
    });
    const tangents = points.map((_, index) => {
      if (index === 0) return slopes[0];
      if (index === points.length - 1) return slopes[slopes.length - 1];
      const before = slopes[index - 1], after = slopes[index];
      if (before === 0 || after === 0 || Math.sign(before) !== Math.sign(after)) return 0;
      return (2 * before * after) / (before + after);
    });
    slopes.forEach((slope, index) => {
      if (slope === 0) { tangents[index] = 0; tangents[index + 1] = 0; return; }
      const a = tangents[index] / slope, b = tangents[index + 1] / slope;
      const magnitude = Math.hypot(a, b);
      if (magnitude > 3) {
        const scale = 3 / magnitude;
        tangents[index] = scale * a * slope;
        tangents[index + 1] = scale * b * slope;
      }
    });
    for (let index = 0; index < points.length - 1; index++) {
      const point = points[index], next = points[index + 1], distance = next.x - point.x;
      ctx.bezierCurveTo(
        point.x + distance / 3, point.y + tangents[index] * distance / 3,
        next.x - distance / 3, next.y - tangents[index + 1] * distance / 3,
        next.x, next.y
      );
    }
  }

  function chartTooltip(canvas) {
    const pane = canvas.closest('.v12-chart-pane') || canvas.parentElement;
    let tooltip = pane?.querySelector('.v12-chart-tooltip');
    if (!tooltip && pane) {
      tooltip = document.createElement('div');
      tooltip.className = 'v12-chart-tooltip';
      tooltip.setAttribute('role', 'status');
      tooltip.hidden = true;
      pane.appendChild(tooltip);
    }
    return tooltip;
  }

  function installPointer(canvas) {
    if (canvas.__v12PointerInstalled) return;
    canvas.__v12PointerInstalled = true;
    const update = event => {
      const model = canvas.__v12ChartModel;
      if (!model) return;
      const bounds = canvas.getBoundingClientRect();
      const x = event.clientX - bounds.left;
      model.hover = Math.max(0, Math.min(model.count - 1, Math.round((x - model.pad.left) / Math.max(1, model.plotW) * (model.count - 1))));
      model.paint(1);
    };
    canvas.addEventListener('pointermove', update, { passive: true });
    canvas.addEventListener('pointerdown', update, { passive: true });
    canvas.addEventListener('pointerleave', () => {
      const model = canvas.__v12ChartModel;
      if (!model) return;
      model.hover = null;
      const tooltip = chartTooltip(canvas);
      if (tooltip) tooltip.hidden = true;
      model.paint(1);
    });
  }

  function drawChart(canvas, series, emptyText, timing) {
    const box = canvas.getBoundingClientRect();
    if (box.width < 2 || box.height < 2) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const width = Math.max(260, Math.round(box.width)), height = Math.max(150, Math.round(box.height));
    canvas.width = Math.round(width * dpr); canvas.height = Math.round(height * dpr);
    const ctx = canvas.getContext('2d');
    const values = series.flatMap(item => item.values.filter(finite).map(Number));
    const count = series[0]?.values.length || 0;
    const pad = { left: 46, right: 14, top: 18, bottom: 31 };
    const plotW = width - pad.left - pad.right, plotH = height - pad.top - pad.bottom;
    const maxRaw = Math.max(1, ...values);
    const magnitude = Math.pow(10, Math.floor(Math.log10(maxRaw)));
    const maxValue = Math.ceil(maxRaw / magnitude) * magnitude;
    const grid = cssColor('--line', '#243244');
    const label = cssColor('--muted', '#8290a2');
    const surface = cssColor('--surface', '#0c1b2a');
    const tooltip = chartTooltip(canvas);
    const pointsFor = item => item.values.map((value, index) => finite(value) ? ({
      x: pad.left + plotW * index / Math.max(1, count - 1),
      y: pad.top + plotH * (1 - Math.max(0, Math.min(1, Number(value) / maxValue))),
      value: Number(value), index
    }) : null);
    const plotted = series.map(item => ({ ...item, points: pointsFor(item) }));

    const model = {
      count, pad, plotW, hover: null,
      paint(progress) {
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
        ctx.clearRect(0, 0, width, height);
        if (!values.length) {
          ctx.fillStyle = label; ctx.font = '12px -apple-system, BlinkMacSystemFont, system-ui';
          ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
          ctx.fillText(emptyText, width / 2, height / 2);
          return;
        }
        ctx.font = '11px -apple-system, BlinkMacSystemFont, system-ui';
        ctx.textBaseline = 'middle';
        for (let row = 0; row < 4; row++) {
          const ratio = row / 3, y = pad.top + plotH * ratio;
          ctx.strokeStyle = grid; ctx.globalAlpha = .72; ctx.lineWidth = 1;
          ctx.beginPath(); ctx.moveTo(pad.left, y + .5); ctx.lineTo(width - pad.right, y + .5); ctx.stroke();
          ctx.globalAlpha = 1; ctx.fillStyle = label; ctx.textAlign = 'right';
          ctx.fillText(formatNumber(Math.round(maxValue * (1 - ratio))), pad.left - 8, y);
        }
        ctx.save();
        ctx.beginPath(); ctx.rect(pad.left, pad.top - 4, plotW * progress, plotH + 8); ctx.clip();
        plotted.forEach((item, seriesIndex) => {
          const runs = splitRuns(item.points);
          if (seriesIndex === 0) runs.forEach(run => {
            if (run.length < 2) return;
            ctx.beginPath(); monotonePath(ctx, run);
            ctx.lineTo(run[run.length - 1].x, pad.top + plotH); ctx.lineTo(run[0].x, pad.top + plotH); ctx.closePath();
            const fill = ctx.createLinearGradient(0, pad.top, 0, pad.top + plotH);
            fill.addColorStop(0, `${item.color}24`); fill.addColorStop(1, `${item.color}00`);
            ctx.fillStyle = fill; ctx.fill();
          });
          ctx.strokeStyle = item.color; ctx.lineWidth = seriesIndex ? 1.8 : 2.4;
          ctx.lineJoin = 'round'; ctx.lineCap = 'round';
          runs.forEach(run => {
            ctx.beginPath(); monotonePath(ctx, run);
            if (run.length === 1) { ctx.arc(run[0].x, run[0].y, 1.8, 0, Math.PI * 2); ctx.fillStyle = item.color; ctx.fill(); }
            else ctx.stroke();
          });
        });
        ctx.restore();

        const formatTime = value => timing.rangeMS >= 86400000
          ? value.toLocaleDateString([], { month: '2-digit', day: '2-digit' }) + ' ' + value.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
          : value.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
        ctx.fillStyle = label; ctx.textBaseline = 'bottom'; ctx.textAlign = 'left';
        ctx.fillText(formatTime(new Date(timing.start)), pad.left, height - 2);
        ctx.textAlign = 'right'; ctx.fillText(formatTime(new Date(timing.end)), width - pad.right, height - 2);

        if (model.hover !== null && count) {
          const index = model.hover, x = pad.left + plotW * index / Math.max(1, count - 1);
          ctx.strokeStyle = cssColor('--line-strong', '#40536a'); ctx.lineWidth = 1;
          ctx.beginPath(); ctx.moveTo(x + .5, pad.top); ctx.lineTo(x + .5, pad.top + plotH); ctx.stroke();
          const rows = [];
          plotted.forEach(item => {
            const point = item.points[index];
            if (!point) return;
            ctx.beginPath(); ctx.arc(point.x, point.y, 4, 0, Math.PI * 2); ctx.fillStyle = surface; ctx.fill();
            ctx.lineWidth = 2; ctx.strokeStyle = item.color; ctx.stroke();
            rows.push(`<span><i style="background:${item.color}"></i>${item.label}<strong>${formatNumber(point.value)}${item.unit || ''}</strong></span>`);
          });
          if (tooltip && rows.length) {
            const observed = new Date(timing.start + (index + .5) * timing.bucketMS);
            tooltip.innerHTML = `<time>${formatTime(observed)}</time>${rows.join('')}`;
            tooltip.hidden = false;
            tooltip.style.left = `${Math.max(8, Math.min(width - 142, x + (x > width * .68 ? -136 : 12)))}px`;
            tooltip.style.top = `${pad.top + 8}px`;
          } else if (tooltip) tooltip.hidden = true;
        }
      }
    };
    canvas.__v12ChartModel = model;
    installPointer(canvas);
    const signature = series.map(item => item.values.join(',')).join('|') + `@${width}x${height}`;
    const previous = chartFrames.get(canvas);
    if (previous) cancelAnimationFrame(previous.frame);
    if (reducedMotion.matches || canvas.dataset.v12ChartSignature === signature) {
      canvas.dataset.v12ChartSignature = signature; model.paint(1); return;
    }
    canvas.dataset.v12ChartSignature = signature;
    const started = performance.now(), duration = 360;
    const animate = now => {
      const progress = easeOut(Math.min(1, (now - started) / duration));
      model.paint(progress);
      if (progress < 1) {
        const frame = requestAnimationFrame(animate);
        chartFrames.set(canvas, { frame });
      } else chartFrames.delete(canvas);
    };
    const frame = requestAnimationFrame(animate);
    chartFrames.set(canvas, { frame });
  }

  function drawSmoothRequestChart() {
    const quality = $('requestChart'), latencyCanvas = $('latencyChart');
    if (!quality || !latencyCanvas || !quality.isConnected) return;
    const trend = state.trend || {}, now = Date.now();
    const retentionSeconds = Number(trend.retention_seconds) || 604800;
    const requestedRangeSeconds = Math.min(Number(trend.range_seconds) || 86400, retentionSeconds);
    const bucketSeconds = Math.max(1, Number(trend.bucket_seconds) || 60), bucketMS = bucketSeconds * 1000;
    const points = (Array.isArray(trend.points) ? trend.points : []).map(point => ({ ...point, _time: new Date(point.time).getTime() })).filter(point => Number.isFinite(point._time));
    let plotStart = now - requestedRangeSeconds * 1000, plotEnd = now;
    if (chartRange === 'all' && points.length) {
      let first = Infinity, last = -Infinity;
      points.forEach(point => { first = Math.min(first, point._time); last = Math.max(last, point._time); });
      const span = Math.max(bucketMS, last - first), padding = Math.max(bucketMS * 2, span * .04);
      plotStart = first - padding; plotEnd = Math.min(now + bucketMS, last + padding);
      if (plotEnd <= plotStart) plotEnd = plotStart + bucketMS * 2;
    }
    const plotRangeMS = Math.max(bucketMS * 2, plotEnd - plotStart);
    const widthHint = Math.max(260, Math.round(quality.getBoundingClientRect().width || 0));
    const maxBuckets = Math.max(80, Math.floor(widthHint / 2));
    const displayBucketMS = Math.max(bucketMS, Math.ceil(plotRangeMS / (maxBuckets * bucketMS)) * bucketMS);
    const buckets = Math.max(2, Math.ceil(plotRangeMS / displayBucketMS));
    const groups = Array.from({ length: buckets }, () => null);
    points.forEach(point => {
      if (point._time < plotStart || point._time > plotEnd + displayBucketMS) return;
      const index = Math.min(buckets - 1, Math.max(0, Math.floor((point._time - plotStart) / displayBucketMS)));
      if (!groups[index]) groups[index] = { requests: 0, failed: 0, p95: [] };
      groups[index].requests += Number(point.requests) || 0; groups[index].failed += Number(point.failed) || 0;
      if (finite(point.p95_latency_ms)) groups[index].p95.push(Number(point.p95_latency_ms));
    });
    const requests = groups.map(group => group ? group.requests : null);
    const failures = groups.map(group => group ? group.failed : null);
    const p95 = groups.map(group => group?.p95.length ? Math.round(group.p95.reduce((sum, value) => sum + value, 0) / group.p95.length) : null);
    const rangeLabel = chartRangeLabel(chartRange);
    const sampleCount = points.reduce((sum, point) => sum + (Number(point.requests) || 0), 0);
    const failedCount = points.reduce((sum, point) => sum + (Number(point.failed) || 0), 0);
    const displayBucketSeconds = Math.max(bucketSeconds, Math.round(displayBucketMS / 1000));
    const durationLabel = seconds => seconds >= 86400 ? `${Math.round(seconds / 86400)} 天` : seconds >= 3600 ? `${Math.round(seconds / 3600)} 小时` : `${Math.max(1, Math.round(seconds / 60))} 分钟`;
    const displayBucketLabel = durationLabel(displayBucketSeconds);
    const timing = { start: plotStart, end: plotEnd, rangeMS: plotRangeMS, bucketMS: displayBucketMS };
    drawChart(quality, [
      { values: requests, color: cssColor('--blue', '#5a9dff'), label: '请求' },
      { values: failures, color: cssColor('--red', '#ff7185'), label: '失败' }
    ], `${rangeLabel}没有请求数据点`, timing);
    drawChart(latencyCanvas, [{ values: p95, color: cssColor('--blue', '#5a9dff'), label: 'P95', unit: ' ms' }], `${rangeLabel}没有延迟数据点`, timing);
    const dataLabel = sampleCount ? `${formatNumber(sampleCount)} 次真实请求 · ${formatNumber(points.length)} 个原始点` : '没有真实请求';
    $('chartWindowLabel').textContent = `${rangeLabel} · ${dataLabel}`;
    $('chartWindow').textContent = points.length ? `${formatNumber(points.length)} 个原始点 · 按 ${displayBucketLabel} 聚合` : '无数据点';
    $('chartRetention').textContent = `本地趋势保留 ${chartDurationLabel(retentionSeconds)} · 原始 ${durationLabel(bucketSeconds)} · 显示 ${displayBucketLabel} · 曲线平滑不改变原始值`;
    $('chartSummary').textContent = sampleCount ? `${rangeLabel}包含 ${sampleCount} 次真实请求和 ${points.length} 个原始数据点，图表按 ${displayBucketLabel} 聚合展示，其中 ${failedCount} 次失败；曲线使用不越过原始值的单调平滑，空白时段保持断开。` : `${rangeLabel}没有真实请求数据，图表保持空白；趋势数据保留 ${chartDurationLabel(retentionSeconds)}。`;
  }

  window.drawRequestChart = drawSmoothRequestChart;
  window.Lite2APINativeV12Motion = Object.freeze({ drawChart, monotonePath, redraw: drawSmoothRequestChart });
  new MutationObserver(() => requestAnimationFrame(drawSmoothRequestChart)).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme-resolved'] });
})();
