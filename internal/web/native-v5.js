/* Lite2API Native v5
   Keeps business functions intact; only coordinates the new static DOM. */
(() => {
  "use strict";

  const BUILD = "Native 5.0 · 2026.08.18";
  const STORAGE = {
    route: "lite2api.native.route",
    source: "lite2api.native.source"
  };
  const state = {
    scheduled: false,
    routeSignature: "",
    createdSecretVisible: false
  };
  const $ = id => document.getElementById(id);
  const all = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const later = fn => requestAnimationFrame(() => requestAnimationFrame(fn));

  function routeCards() {
    return all("#routeRows > .route-card");
  }

  function routeAlias(card) {
    return card?.querySelector(".route-alias")?.value?.trim() || "未命名";
  }

  function routeKey(card) {
    return card?.dataset.routeKey || encodeURIComponent(routeAlias(card));
  }

  function routeModel(card) {
    return card?.querySelector(".route-intent select")?.value?.trim() || "未选择模型";
  }

  function routeTone(card) {
    const badge = card?.querySelector(".route-health-badge");
    if (!badge) return "unknown";
    if (badge.classList.contains("bad")) return "bad";
    if (badge.classList.contains("warn")) return "warn";
    if (badge.classList.contains("unknown")) return "unknown";
    return "ready";
  }

  function refineRouteCopy(cards) {
    cards.forEach(card => {
      const chain = card.querySelector(".route-chain-meta span:last-child");
      if (chain) chain.textContent = chain.textContent.replace(/(\d+)\s*级真实渠道\s*fallback/i, "$1 级真实上游链");
    });
  }

  function routeSignature(cards) {
    return cards.map(card => [routeKey(card), routeAlias(card), routeModel(card), routeTone(card)].join("::")).join("||");
  }

  function selectedRouteKey(cards) {
    const stored = localStorage.getItem(STORAGE.route);
    if (stored && cards.some(card => routeKey(card) === stored)) return stored;
    const legacy = cards.find(card => routeAlias(card) === stored);
    if (legacy) return routeKey(legacy);
    return routeKey(cards[0]);
  }

  function selectRoute(key, focus = false) {
    const cards = routeCards();
    if (!cards.length) return;
    const selected = cards.find(card => routeKey(card) === key || routeAlias(card) === key) || cards[0];
    const value = routeKey(selected);
    localStorage.setItem(STORAGE.route, value);

    cards.forEach(card => {
      const active = card === selected;
      card.hidden = !active;
      card.setAttribute("aria-hidden", String(!active));
    });
    all("#v5RouteList .route-master-item").forEach(button => {
      const active = button.dataset.routeKey === value;
      button.setAttribute("aria-selected", String(active));
      button.tabIndex = active ? 0 : -1;
    });
    if (focus) later(() => selected.querySelector(".route-alias,select,button")?.focus({ preventScroll: true }));
  }

  function syncRouteMaster() {
    const list = $("v5RouteList");
    if (!list) return;
    const cards = routeCards();
    refineRouteCopy(cards);
    const signature = routeSignature(cards);
    const selected = selectedRouteKey(cards);

    if (signature !== state.routeSignature || list.children.length !== cards.length) {
      state.routeSignature = signature;
      list.replaceChildren();
      cards.forEach(card => {
        const alias = routeAlias(card);
        const key = routeKey(card);
        const model = routeModel(card);
        const tone = routeTone(card);
        const button = document.createElement("button");
        button.type = "button";
        button.className = "route-master-item";
        button.dataset.route = alias;
        button.dataset.routeKey = key;
        button.setAttribute("aria-selected", String(key === selected));
        button.innerHTML = `<span class="route-master-copy"><strong></strong><small></small></span><span class="route-master-state ${tone === "ready" ? "" : tone}" aria-hidden="true"></span>`;
        button.querySelector("strong").textContent = alias;
        button.querySelector("small").textContent = model;
        // Pointer selection should keep focus in the master list. Editing the
        // alias remains an explicit action instead of an accidental side effect.
        button.addEventListener("click", () => selectRoute(key, false));
        list.append(button);
      });
    }
    selectRoute(selected, false);
  }

  function selectSource(name, persist = true) {
    const accounts = $("v5SourceAccounts");
    const connections = $("v5SourceConnections");
    if (!accounts || !connections) return;
    const showConnections = name === "connections";
    accounts.hidden = showConnections;
    connections.hidden = !showConnections;
    accounts.setAttribute("aria-hidden", String(showConnections));
    connections.setAttribute("aria-hidden", String(!showConnections));
    all("[data-source-tab]").forEach(button => {
      const active = button.dataset.sourceTab === (showConnections ? "connections" : "accounts");
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", String(active));
      button.tabIndex = active ? 0 : -1;
    });
    if (persist) localStorage.setItem(STORAGE.source, showConnections ? "connections" : "accounts");
  }

  function installSourceTabs() {
    all("[data-source-tab]").forEach(button => {
      if (button.dataset.bound === "1") return;
      button.dataset.bound = "1";
      button.addEventListener("click", () => selectSource(button.dataset.sourceTab));
      button.addEventListener("keydown", event => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const tabs = all("[data-source-tab]");
        const current = tabs.indexOf(button);
        const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
        selectSource(tabs[next].dataset.sourceTab);
        tabs[next].focus();
      });
    });
    selectSource(localStorage.getItem(STORAGE.source) || "accounts", false);
  }

  function keyDialog() {
    return $("v5KeyDialog");
  }

  function openKeyDialog() {
    const dialog = keyDialog();
    if (!dialog) return;
    if (!dialog.open) dialog.showModal();
    later(() => dialog.querySelector(".key-preset.active,.key-preset,button")?.focus({ preventScroll: true }));
  }

  function closeKeyDialog() {
    const dialog = keyDialog();
    if (dialog?.open) dialog.close();
  }

  function syncCreatedKey() {
    const card = $("createdKeyCard");
    const secret = $("createdKey")?.value?.trim();
    const setup = $("clientSetup");
    if (!card || !setup) return;
    const visible = !card.hidden && Boolean(secret);
    // Keep the command generator available on the keys page. Existing keys
    // are intentionally shown with a placeholder because their plaintext is
    // never recoverable; a newly-created key is injected automatically.
    setup.hidden = false;
    setup.classList.toggle("has-secret", visible);
    if (typeof window.renderClientSetup === "function") window.renderClientSetup();
    if (visible && !state.createdSecretVisible) {
      state.createdSecretVisible = true;
      closeKeyDialog();
      later(() => card.scrollIntoView({ behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "start" }));
    }
    if (!visible) state.createdSecretVisible = false;
  }

  function installKeyDialog() {
    const open = $("v5OpenKeyDialog");
    const close = $("v5CloseKeyDialog");
    const dialog = keyDialog();
    if (open && open.dataset.bound !== "1") {
      open.dataset.bound = "1";
      open.addEventListener("click", openKeyDialog);
    }
    if (close && close.dataset.bound !== "1") {
      close.dataset.bound = "1";
      close.addEventListener("click", closeKeyDialog);
    }
    if (dialog && dialog.dataset.bound !== "1") {
      dialog.dataset.bound = "1";
      dialog.addEventListener("click", event => {
        if (event.target === dialog) closeKeyDialog();
      });
    }
    all("[data-key-preset]").forEach((button, index, buttons) => {
      button.tabIndex = button.getAttribute("aria-checked") === "true" ? 0 : -1;
      if (button.dataset.keyboardBound === "1") return;
      button.dataset.keyboardBound = "1";
      button.addEventListener("keydown", event => {
        if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const direction = event.key === "ArrowRight" || event.key === "ArrowDown" ? 1 : -1;
        const next = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : (index + direction + buttons.length) % buttons.length;
        if (typeof window.selectKeyPreset === "function") window.selectKeyPreset(buttons[next].dataset.keyPreset);
        buttons.forEach((item, itemIndex) => { item.tabIndex = itemIndex === next ? 0 : -1; });
        buttons[next].focus();
      });
    });
    syncCreatedKey();
  }

  function simplifyRuntimeLabels() {
    document.documentElement.dataset.ui = "native-v5";
    const subtitle = $("viewSubtitle");
    if (subtitle) subtitle.hidden = true;
  }

  function sync() {
    state.scheduled = false;
    simplifyRuntimeLabels();
    installSourceTabs();
    installKeyDialog();
    syncRouteMaster();
    syncCreatedKey();
  }

  function schedule() {
    if (state.scheduled) return;
    state.scheduled = true;
    later(sync);
  }

  function wrap(name) {
    const original = window[name];
    if (typeof original !== "function" || original.__nativeV5Wrapped) return;
    const wrapped = function (...args) {
      const result = original.apply(this, args);
      if (result && typeof result.finally === "function") result.finally(schedule);
      else schedule();
      return result;
    };
    Object.defineProperty(wrapped, "__nativeV5Wrapped", { value: true });
    window[name] = wrapped;
  }

  function installWrappers() {
    ["render","renderRoutes","renderKeys","renderOAuthAccounts","showView","createQuickKey","createClientKey","saveRoutes"].forEach(wrap);
  }

  function installObservers() {
    const routeRows = $("routeRows");
    if (routeRows) new MutationObserver(schedule).observe(routeRows, { childList: true, subtree: true, attributes: true, attributeFilter: ["class", "hidden", "value"] });
    const created = $("createdKeyCard");
    if (created) new MutationObserver(schedule).observe(created, { attributes: true, subtree: true, childList: true, characterData: true, attributeFilter: ["hidden"] });
    $("createdKey")?.addEventListener("input", schedule);
    document.addEventListener("change", event => {
      if (event.target.closest?.("#routeRows")) schedule();
    });
    document.addEventListener("input", event => {
      if (event.target.closest?.("#routeRows")) schedule();
    });
  }

  function init() {
    simplifyRuntimeLabels();
    installSourceTabs();
    installKeyDialog();
    installWrappers();
    installObservers();
    sync();
  }

  window.Lite2APINativeV5 = Object.freeze({ version: BUILD, selectRoute, selectSource, openKeyDialog, closeKeyDialog, sync });

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init, { once: true });
  else init();
})();
