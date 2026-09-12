/* One indexed snapshot per server response; renderers share the derived result. */
(function (root, factory) {
  const api = Object.freeze(factory());
  if (typeof module === 'object' && module.exports) module.exports = api;
  root.Lite2APIMetrics = api;
})(globalThis, function () {
  'use strict';
  const finite = (value) =>
    value === null || value === undefined || (typeof value === 'string' && !value.trim())
      ? null
      : Number.isFinite(Number(value))
        ? Number(value)
        : null;
  function percentile(values, fraction) {
    const sorted = values.filter(Number.isFinite).sort((a, b) => a - b);
    return sorted.length
      ? sorted[Math.max(0, Math.min(sorted.length - 1, Math.ceil(sorted.length * fraction) - 1))]
      : null;
  }
  function createIndex() {
    let previous;
    let result;
    return function index(snapshot) {
      if (snapshot === previous && result) return result;
      previous = snapshot;
      const configured = new Map((snapshot?.config?.accounts || []).map((account) => [account.id, account]));
      const runtime = new Map((snapshot?.accounts || []).map((account) => [account.id, account]));
      const requests = new Map();
      for (const row of snapshot?.stats?.recent || []) {
        if (!requests.has(row.account_id)) requests.set(row.account_id, []);
        requests.get(row.account_id).push(row);
      }
      const connections = [...configured.values()].map((config) => {
        const live = runtime.get(config.id) || {};
        const rows = requests.get(config.id) || [];
        const latencies = rows.map((row) => finite(row.latency_ms)).filter((value) => value !== null);
        const failures = rows.reduce((count, row) => count + !(row.status >= 200 && row.status < 400), 0);
        return {
          id: config.id,
          name: config.name || config.id,
          config,
          live,
          rows,
          samples: rows.length,
          failures,
          failureRate: rows.length ? failures / rows.length : 0,
          avg: latencies.length
            ? latencies.reduce((sum, value) => sum + value, 0) / latencies.length
            : (finite(live.average_latency_ms) ?? Infinity),
          p95: percentile(latencies, 0.95),
        };
      });
      result = { configured, runtime, requests, connections };
      return result;
    };
  }
  function createRouteUsageIndex(normalizeRoute) {
    let previous;
    let lookup;
    return function index(routes) {
      if (routes === previous && lookup) return lookup;
      previous = routes;
      const accounts = new Map();
      const wildcard = [];
      for (const alias of Object.keys(routes || {}).sort()) {
        const route = normalizeRoute(routes[alias]);
        if (route.all_accounts) wildcard.push(alias);
        else
          for (const id of new Set(route.targets.map((target) => target.account))) {
            if (!accounts.has(id)) accounts.set(id, []);
            accounts.get(id).push(alias);
          }
      }
      if (wildcard.length)
        for (const [id, aliases] of accounts) accounts.set(id, [...aliases, ...wildcard].sort());
      lookup = (id) => accounts.get(id) || wildcard;
      return lookup;
    };
  }
  return { createIndex, createRouteUsageIndex, percentile };
});
