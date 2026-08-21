/* Lite2API adapter clarity.
   Separates catalog/install state from auth/readiness and traffic state so
   operators do not mistake a discovered adapter for a serving target. */
(() => {
  "use strict";

  const BUILD = "Native Adapter Clarity 1.0";

  function statePill(label, value, tone = "") {
    return `<div class="adapter-state-pill ${tone}"><span>${esc(label)}</span><strong>${esc(value)}</strong></div>`;
  }

  function installLabel(adapter) {
    const status = adapter.install_status || adapter.status || "catalog";
    return adapterStatusLabels[status] || status;
  }

  function readinessLabel(adapter) {
    if (adapter.readiness === "ready" || adapter.status === "ready" || adapter.status === "built-in") return ["就绪", "good"];
    if (adapter.status === "auth-required") return ["待授权", "warn"];
    if (adapter.status === "running") return ["运行未配置", "blue"];
    if (adapter.status === "stopped") return ["未运行", "bad"];
    if (adapter.status === "configured") return ["已配置待探针", "blue"];
    if (adapter.status === "installed") return ["已安装待配置", "blue"];
    return ["仅目录收录", ""];
  }

  function trafficLabel(adapter) {
    if (adapter.traffic === "enabled") return ["承载流量", "good"];
    return ["未接流量", adapter.status === "catalog" || adapter.status === "candidate" ? "" : "warn"];
  }

  function runtimeLabel(adapter) {
    if (adapter.runtime_status) {
      const parts = [adapter.runtime_status];
      if (adapter.latency_ms) parts.push(`${adapter.latency_ms} ms`);
      if (adapter.model_count) parts.push(`${adapter.model_count} 模型`);
      return parts.join(" · ");
    }
    if (adapter.account_ids?.length) return `${adapter.account_ids.length} 个关联连接`;
    return adapter.local_url ? "等待本机探针" : "无本机运行时";
  }

  function renderAdaptersClarity() {
    const all = state.adapters || [];
    const search = $("adapterSearch").value.trim().toLowerCase();
    const status = $("adapterStatus").value;
    const items = all.filter(adapter => {
      const text = [adapter.id, adapter.name, adapter.category, adapter.description, ...(adapter.platforms || []), ...(adapter.protocols || []), ...(adapter.operations || [])].join(" ").toLowerCase();
      return (!search || text.includes(search)) && (!status || adapter.status === status);
    });
    const statusOrder = ["built-in", "ready", "auth-required", "running", "stopped", "configured", "installed", "candidate", "catalog"];
    const serving = all.filter(adapter => adapter.traffic === "enabled").length;
    const ready = all.filter(adapter => ["ready", "built-in"].includes(adapter.status) || adapter.readiness === "ready").length;
    const attention = all.filter(adapter => ["auth-required", "stopped"].includes(adapter.status)).length;
    $("adapterSummary").textContent = all.length
      ? `${serving} 个承载流量 · ${ready} 个运行就绪 · ${attention} 个需处理 · 探针缓存 60 秒`
      : "等待适配器数据";
    $("adapterChips").innerHTML = `<button class="filter-chip ${!status ? "active" : ""}" onclick="setAdapterStatus('')">全部 ${all.length}</button>` +
      statusOrder.map(value => {
        const count = all.filter(adapter => adapter.status === value).length;
        return count ? `<button class="filter-chip ${status === value ? "active" : ""}" onclick="setAdapterStatus('${value}')">${adapterStatusLabels[value]} ${count}</button>` : "";
      }).join("");
    $("adapters").innerHTML = items.map(adapter => {
      const provider = adapterProvider(adapter);
      const operations = (adapter.operations || []).join(", ") || (adapter.protocols || []).join(", ") || "—";
      const authMode = (adapter.auth_modes || []).join(", ") || "无";
      const [readyText, readyTone] = readinessLabel(adapter);
      const [trafficText, trafficTone] = trafficLabel(adapter);
      const badge = readyTone === "good" ? "good" : readyTone === "warn" ? "warn" : readyTone === "bad" ? "bad" : readyTone === "blue" ? "blue" : "";
      return `<article class="adapter-card"><div class="adapter-card-head"><div class="identity"><div class="provider-icon provider-${provider.key}" title="${esc(provider.label)}">${providerMark(provider.key)}</div><div><h3>${esc(adapter.name)}</h3><span class="subtle">${esc(adapter.id)}</span></div></div><span class="badge ${badge}">${esc(readyText)}</span></div><p>${esc(adapter.description)}</p><div class="adapter-state-grid">${statePill("目录 / 安装", installLabel(adapter))}${statePill("运行 / 鉴权", readyText, readyTone)}${statePill("流量", trafficText, trafficTone)}</div><div class="adapter-facts"><div class="adapter-fact"><span>操作类型</span><strong>${esc(operations)}</strong></div><div class="adapter-fact"><span>认证</span><strong>${esc(authMode)}</strong></div><div class="adapter-fact"><span>探针 / 运行时</span><strong>${esc(runtimeLabel(adapter))}</strong></div></div><div class="adapter-card-foot"><span class="subtle">${esc(adapter.license || "未标注许可")}</span>${adapter.source_url ? `<a href="${esc(adapter.source_url)}" target="_blank" rel="noopener noreferrer">查看详情 ${icon("external")}</a>` : `<span class="badge good">本机能力</span>`}</div></article>`;
    }).join("") || '<div class="empty"><strong>没有匹配的适配器</strong>调整搜索或状态筛选。</div>';
  }

  window.renderAdapters = renderAdaptersClarity;
  window.Lite2APIAdapterClarity = Object.freeze({ version: BUILD, renderAdapters: renderAdaptersClarity });
})();
