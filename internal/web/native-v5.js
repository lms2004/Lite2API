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
    return cards.map(card => [routeAlias(card), routeModel(card), routeTone(card)].join("::")).join("||");
  }

  function selectedRoute(cards) {
    const stored = localStorage.getItem(STORAGE.route);
    if (stored && cards.some(card => routeAlias(card) === stored)) return stored;
    return routeAlias(cards[0]);
  }

  function selectRoute(alias, focus = false) {
    const cards = routeCards();
    if (!cards.length) return;
    const selected = cards.find(card => routeAlias(card) === alias) || cards[0];
    const value = routeAlias(selected);
    localStorage.setItem(STORAGE.route, value);

    cards.forEach(card => {
      const active = card === selected;
      card.hidden = !active;
      card.setAttribute("aria-hidden", String(!active));
    });
    all("#v5RouteList .route-master-item").forEach(button => {
      const active = button.dataset.route === value;
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
    const selected = selectedRoute(cards);

    if (signature !== state.routeSignature || list.children.length !== cards.length) {
      state.routeSignature = signature;
      list.replaceChildren();
      cards.forEach(card => {
        const alias = routeAlias(card);
        const model = routeModel(card);
        const tone = routeTone(card);
        const button = document.createElement("button");
        button.type = "button";
        button.className = "route-master-item";
        button.dataset.route = alias;
        button.setAttribute("aria-selected", String(alias === selected));
        button.innerHTML = `<span class="route-master-copy"><strong></strong><small></small></span><span class="route-master-state ${tone === "ready" ? "" : tone}" aria-hidden="true"></span>`;
        button.querySelector("strong").textContent = alias;
        button.querySelector("small").textContent = model;
        // Pointer selection should keep focus in the master list. Editing the
        // alias remains an explicit action instead of an accidental side effect.
        button.addEventListener("click", () => selectRoute(alias, false));
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
    all("[data-source-tab]").forEach(button => {
      const active = button.dataset.sourceTab === (showConnections ? "connections" : "accounts");
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", String(active));
    });
    if (persist) localStorage.setItem(STORAGE.source, showConnections ? "connections" : "accounts");
  }

  function installSourceTabs() {
    all("[data-source-tab]").forEach(button => {
      if (button.dataset.bound === "1") return;
      button.dataset.bound = "1";
      button.addEventListener("click", () => selectSource(button.dataset.sourceTab));
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
    syncCreatedKey();
  }

  function simplifyRuntimeLabels() {
    document.documentElement.dataset.ui = "native-v5";
    const build = $("uiBuild");
    if (build && !build.textContent.includes("2026.08.18-v5")) build.textContent = "UI build 2026.08.18-v5";
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
