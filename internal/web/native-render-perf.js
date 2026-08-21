/* Lite2API render performance layer.
   Renders the active workspace only; hidden views keep their last DOM until
   the operator opens them or an action explicitly refreshes them. */
(() => {
  "use strict";

  const BUILD = "Native Render Perf 1.0";
  const $ = id => document.getElementById(id);
  const fullRender = window.render;

  function activeView() {
    try {
      return typeof activeViewName === "string" && activeViewName ? activeViewName : "monitor";
    } catch (_) {
      return "monitor";
    }
  }

  function safeCall(name, ...args) {
    const fn = window[name];
    if (typeof fn === "function") return fn(...args);
    return undefined;
  }

  function syncRouteDraftIfClean() {
    try {
      if (!routesDirty) routeDraft = JSON.parse(JSON.stringify(state.config?.routes || {}));
    } catch (_) {}
  }

  function renderMonitorView() {
    safeCall("renderMonitor");
    const subtitle = $("viewSubtitle");
    if (subtitle) subtitle.textContent = "实时状态与最近 " + (state.stats?.recent_limit || 512) + " 条请求";
  }

  function renderAccountsView() {
    safeCall("renderAccountMetrics");
    safeCall("renderOAuthView");
    safeCall("renderAccounts");
  }

  function renderKeysView() {
    safeCall("renderKeys", lastKeys);
    safeCall("renderClientSetup");
  }

  function renderRoutesView() {
    syncRouteDraftIfClean();
    safeCall("renderRoutes");
  }

  function renderActiveView() {
    switch (activeView()) {
    case "accounts":
      renderAccountsView();
      break;
    case "keys":
      renderKeysView();
      break;
    case "routes":
      renderRoutesView();
      break;
    case "adapters":
      safeCall("renderAdapters");
      break;
    case "prompt-test":
      safeCall("renderPromptLab");
      break;
    case "monitor":
    default:
      renderMonitorView();
      break;
    }
    const build = $("uiBuild");
    if (build) build.textContent = "UI build " + UI_BUILD;
  }

  window.render = renderActiveView;
  window.Lite2APIRenderPerf = Object.freeze({ version: BUILD, render: renderActiveView, fullRender });
})();
