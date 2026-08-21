/* Lite2API route compatibility layer.
   Keeps capability-aware routing as the preferred path, while preserving
   legacy/direct targets that only advertise models or model_map entries. */
(() => {
  "use strict";

  const BUILD = "Native Route Compat 1.0";
  const reasoningOrder = ["auto", "none", "minimal", "low", "medium", "high", "max", "xhigh", "ultra"];
  const directDefaultEfforts = ["auto", "none"];
  const $ = id => document.getElementById(id);

  function uniq(values) {
    const seen = new Set();
    const result = [];
    for (const raw of values) {
      const value = String(raw || "").trim();
      if (!value || seen.has(value)) continue;
      seen.add(value);
      result.push(value);
    }
    return result;
  }

  function accounts() {
    return Array.isArray(state?.config?.accounts) ? state.config.accounts : [];
  }

  function routes() {
    return state?.config?.routes && typeof state.config.routes === "object" ? state.config.routes : {};
  }

  function capabilities(account) {
    return Array.isArray(account?.capabilities) ? account.capabilities : [];
  }

  function modelMap(account) {
    return account?.model_map && typeof account.model_map === "object" ? account.model_map : {};
  }

  function directModelsFor(account) {
    const models = Array.isArray(account?.models) ? account.models.filter(model => model && model !== "*") : [];
    const mappings = modelMap(account);
    return uniq([...models, ...Object.keys(mappings), ...Object.values(mappings)]);
  }

  function directUpstreamModel(account, model) {
    const requested = String(model || "").trim();
    if (!account || !requested) return "";
    const mappings = modelMap(account);
    const mapped = String(mappings[requested] || "").trim();
    if (mapped) return mapped;
    for (const upstream of Object.values(mappings)) {
      if (String(upstream || "").trim() === requested) return requested;
    }
    const models = Array.isArray(account.models) ? account.models.map(item => String(item || "").trim()).filter(Boolean) : [];
    if (!models.length || models.includes("*") || models.includes(requested)) return requested;
    return "";
  }

  function realCapabilityFor(account, model, effort) {
    const wantedModel = String(model || "").trim();
    const wantedEffort = String(effort || "auto").trim() || "auto";
    return capabilities(account).find(capability =>
      capability.model === wantedModel && Array.isArray(capability.reasoning_efforts) &&
      capability.reasoning_efforts.includes(wantedEffort)
    );
  }

  function directEffortsFor(model) {
    const efforts = [];
    for (const route of Object.values(routes())) {
      const targets = Array.isArray(route?.targets) ? route.targets : [];
      for (const target of targets) {
        if (String(target?.model || "").trim() === model) efforts.push(target.reasoning_effort || "auto");
      }
    }
    return uniq([...directDefaultEfforts, ...efforts]);
  }

  function modelCatalogEntries() {
    const entries = [];
    for (const account of accounts()) {
      for (const model of directModelsFor(account)) {
        entries.push({
          model,
          upstream_model: model,
          source: account.name || account.id || "上游",
          efforts: directDefaultEfforts,
          direct: true
        });
      }
    }
    for (const [alias, route] of Object.entries(routes())) {
      for (const target of Array.isArray(route?.targets) ? route.targets : []) {
        const model = String(target?.model || "").trim();
        if (!model) continue;
        entries.push({
          model,
          upstream_model: model,
          source: `${alias} · 当前直连路由`,
          efforts: [target.reasoning_effort || "auto"],
          direct: true
        });
      }
    }
    return entries;
  }

  function logicalModelsCompat() {
    const capabilityModels = accounts().flatMap(account => capabilities(account).map(capability => capability.model));
    const routeModels = Object.values(routes()).map(route => route?.model);
    const directModels = modelCatalogEntries().map(entry => entry.model);
    return uniq([...capabilityModels, ...routeModels, ...directModels]).sort();
  }

  function reasoningEffortsForCompat(model) {
    const available = new Set();
    for (const account of accounts()) {
      for (const capability of capabilities(account)) {
        if (capability.model !== model) continue;
        for (const effort of capability.reasoning_efforts || []) available.add(effort);
      }
      if (directUpstreamModel(account, model)) {
        for (const effort of directEffortsFor(model)) available.add(effort);
      }
    }
    return reasoningOrder.filter(effort => available.has(effort));
  }

  function capabilityForCompat(account, model, effort) {
    const real = realCapabilityFor(account, model, effort);
    if (real) return real;
    const upstream = directUpstreamModel(account, model);
    if (!upstream) return null;
    return { model, upstream_model: upstream, reasoning_efforts: [effort || "auto"], direct: true };
  }

  function compatibleChannelAccountsCompat(model, effort) {
    return accounts().filter(account => capabilityForCompat(account, model, effort));
  }

  function normalizeTargetCompat(target = {}) {
    return {
      account: String(target.account || ""),
      model: String(target.model || ""),
      reasoning_effort: String(target.reasoning_effort || "auto")
    };
  }

  function inferRouteIntentCompat(route, targets) {
    let model = String(route?.model || "").trim();
    let effort = String(route?.reasoning_effort || "").trim();
    if (model) return { model, effort: effort || "auto" };
    const first = targets[0] || {};
    if (first.model) return { model: first.model, effort: first.reasoning_effort || "auto" };
    const legacy = String(route?.upstream_model || "").trim();
    if (legacy) return { model: legacy, effort: "auto" };
    return { model: "", effort: effort || "auto" };
  }

  function normalizeRouteCompat(route = {}, alias = "") {
    let targets = Array.isArray(route.targets) ? route.targets.map(normalizeTargetCompat).filter(target => target.account) : [];
    if (!targets.length && Array.isArray(route.accounts)) {
      targets = route.accounts.map(account => ({
        account,
        model: String(route.upstream_model || route.model || ""),
        reasoning_effort: "auto"
      }));
    }
    const intent = inferRouteIntentCompat(route, targets);
    const models = logicalModelsCompat();
    if (!intent.model) intent.model = models.find(model => model.includes(alias)) || models[0] || "";
    const efforts = reasoningEffortsForCompat(intent.model);
    if (!efforts.includes(intent.effort)) intent.effort = efforts.includes("auto") ? "auto" : efforts[0] || "auto";
    return {
      model: intent.model,
      reasoning_effort: intent.effort,
      targets: targets.map(target => ({
        account: target.account,
        model: target.model || intent.model,
        reasoning_effort: target.reasoning_effort || intent.effort
      }))
    };
  }

  function syncCompatibleTargetsCompat(route) {
    const allowed = new Set(compatibleChannelAccountsCompat(route.model, route.reasoning_effort).map(account => account.id));
    route.targets = route.targets.filter(target => allowed.has(target.account));
    return route;
  }

  function targetOperationalStateCompat(alias, target, route) {
    const account = configuredAccount(target.account);
    const runtime = runtimeAccount(target.account);
    const resolved = capabilityForCompat(account, route.model, route.reasoning_effort);
    if (!account || !runtime) return { label: "目标缺失", tone: "off", resolved };
    if (!resolved) return { label: "组合不支持", tone: "off", resolved };
    if (!runtime.enabled) return { label: "已停用", tone: "off", resolved };
    if (runtime.circuit_open_until) return { label: "不可用", tone: "off", resolved };
    const cutoff = Date.now() - 300000;
    const rows = (state.stats?.recent || []).filter(row =>
      row.model === alias && row.account_id === target.account && new Date(row.time).getTime() >= cutoff
    );
    if (!rows.length) return { label: resolved.direct ? "直连未观测" : "未知", tone: "unknown", resolved };
    return rows.every(row => Number(row.status) >= 200 && Number(row.status) < 400)
      ? { label: "就绪", tone: "", resolved }
      : { label: "降级", tone: "warn", resolved };
  }

  function saveRoutePayload(raw, alias) {
    const route = normalizeRouteCompat(raw, alias);
    if (!route.model) throw new Error(`路由 ${alias} 尚未选择模型`);
    if (!route.reasoning_effort) throw new Error(`路由 ${alias} 尚未选择推理强度`);
    if (!route.targets.length) throw new Error(`路由 ${alias} 没有兼容渠道`);

    const allCapabilityTargets = route.targets.every(target => {
      const account = configuredAccount(target.account);
      return Boolean(realCapabilityFor(account, route.model, route.reasoning_effort));
    });
    if (allCapabilityTargets) {
      return {
        model: route.model,
        reasoning_effort: route.reasoning_effort,
        targets: route.targets.map(target => ({ account: target.account }))
      };
    }

    return {
      targets: route.targets.map((target, index) => {
        const account = configuredAccount(target.account);
        if (!account) throw new Error(`${alias} 的渠道 ${index + 1} 不存在`);
        const resolved = capabilityForCompat(account, route.model, route.reasoning_effort);
        const upstream = resolved?.upstream_model || directUpstreamModel(account, route.model) || target.model;
        if (!upstream) throw new Error(`${account.name || account.id} 缺少直连上游模型`);
        return { account: target.account, model: upstream, reasoning_effort: route.reasoning_effort };
      })
    };
  }

  function setupModelsCompat() {
    const explicitRoutes = Object.keys(routes()).filter(Boolean);
    if (explicitRoutes.length) return explicitRoutes;
    const accountModels = accounts().flatMap(account => [
      ...capabilities(account).map(capability => capability.model),
      ...(Array.isArray(account.models) ? account.models.filter(model => model && model !== "*") : []),
      ...Object.keys(modelMap(account))
    ]);
    const catalogModels = modelCatalogEntries().map(entry => entry.model);
    return uniq([...accountModels, ...catalogModels]).slice(0, 30);
  }

  async function saveRoutesCompat() {
    try {
      const payload = {};
      for (const [alias, raw] of Object.entries(routeDraft || {})) {
        const name = String(alias || "").trim();
        if (!name) throw new Error("模型别名不能为空");
        if (name.length > 128) throw new Error(`模型别名过长：${name}`);
        payload[name] = saveRoutePayload(raw, name);
      }
      const changes = routeChangeLines();
      if (changes.length && !confirm(`确认保存并立即影响新请求？\n\n${changes.join("\n")}`)) return;
      await api("/routes", { method: "PUT", body: JSON.stringify(payload) });
      routeDraft = JSON.parse(JSON.stringify(payload));
      routesDirty = false;
      renderRouteChangeSummary();
      say("路由已保存并热加载");
      await load();
    } catch (error) {
      say(error.message, true);
    }
  }

  function openRouteJSONCompat() {
    const payload = Object.fromEntries(Object.entries(routeDraft || {}).map(([alias, route]) => {
      try {
        return [alias, saveRoutePayload(route, alias)];
      } catch (_) {
        return [alias, normalizeRouteCompat(route, alias)];
      }
    }));
    $("routes").value = JSON.stringify(payload, null, 2);
    openDialog("routeJSONDialog");
  }

  window.logicalModels = logicalModelsCompat;
  window.reasoningEffortsFor = reasoningEffortsForCompat;
  window.capabilityFor = capabilityForCompat;
  window.compatibleChannelAccounts = compatibleChannelAccountsCompat;
  window.normalizeTarget = normalizeTargetCompat;
  window.normalizeRoute = normalizeRouteCompat;
  window.syncCompatibleTargets = syncCompatibleTargetsCompat;
  window.targetOperationalState = targetOperationalStateCompat;
  window.saveRoutes = saveRoutesCompat;
  window.openRouteJSON = openRouteJSONCompat;
  window.setupModels = setupModelsCompat;
  window.Lite2APIRouteCompat = Object.freeze({
    version: BUILD,
    directUpstreamModel,
    modelCatalogEntries,
    saveRoutePayload,
    setupModels: setupModelsCompat
  });
})();
