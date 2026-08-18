/* Lite2API Apple Simple v4 — macro-layout behavior.
   Reorganizes the existing business DOM without changing API contracts. */
(() => {
  "use strict";

  const BUILD = "Apple Simple 4.0 · 2026.08.18";
  const STORAGE = {
    route: "lite2api.apple.route",
    charts: "lite2api.overviewCharts"
  };
  const state = {
    queued: false,
    syncingRoute: false,
    routeSignature: "",
    keySecretSeen: false
  };

  const byId = id => document.getElementById(id);
  const all = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const later = fn => requestAnimationFrame(() => requestAnimationFrame(fn));

  function el(tag, className, attrs = {}) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    Object.entries(attrs).forEach(([key, value]) => {
      if (value === undefined || value === null) return;
      if (key === "text") node.textContent = value;
      else node.setAttribute(key, String(value));
    });
    return node;
  }

  function icon(id) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    svg.setAttribute("class", "icon");
    svg.setAttribute("aria-hidden", "true");
    use.setAttribute("href", `#${id}`);
    svg.append(use);
    return svg;
  }

  function updateIdentity() {
    document.documentElement.dataset.ui = "apple-simple-v4";
    const build = byId("uiBuild");
    if (build) build.textContent = BUILD;
    const brand = document.querySelector(".brand-copy strong");
    if (brand) brand.textContent = "Lite2API";
  }

  function installOverviewSummary() {
    const view = byId("view-monitor");
    const health = byId("healthVerdict");
    const metrics = byId("monitorMetrics");
    if (!view || !health || !metrics) return;

    let summary = view.querySelector(".apple-overview-summary");
    if (!summary) {
      summary = el("section", "apple-overview-summary", { "aria-label": "当前运行状态" });
      health.before(summary);
      summary.append(health, metrics);
    }

    const disclosure = view.querySelector(".qc-chart-disclosure");
    const requestPanel = byId("requests")?.closest(".panel");
    if (disclosure && requestPanel && disclosure.previousElementSibling !== requestPanel) {
      requestPanel.after(disclosure);
    }
    if (disclosure && !disclosure.dataset.appleDefault) {
      disclosure.dataset.appleDefault = "true";
      if (!localStorage.getItem(STORAGE.charts)) disclosure.open = false;
    }
  }

  function routeCards() {
    return all("#routeRows > .route-card");
  }

  function routeAlias(card) {
    return card?.querySelector(".route-alias")?.value?.trim() || "未命名路由";
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
    return "good";
  }

  function routeStateText(card) {
    const badge = card?.querySelector(".route-health-badge");
    const text = (badge?.textContent || "未知").trim();
    return text.split("·")[0].trim() || "未知";
  }

  function ensureRouteWorkspace() {
    const view = byId("view-routes");
    const panel = view?.querySelector(".route-panel");
    if (!view || !panel) return null;

    let workspace = view.querySelector(".apple-route-workspace");
    if (workspace) return workspace;

    workspace = el("section", "apple-route-workspace", { "aria-label": "模型路由编辑器" });
    const sidebar = el("aside", "apple-route-sidebar");
    const head = el("div", "apple-route-sidebar-head", { text: "模型别名" });
    const list = el("nav", "apple-route-list", { "aria-label": "选择模型路由" });
    const foot = el("div", "apple-route-sidebar-foot");
    const add = panel.querySelector(".route-add");
    const count = byId("routeCount");

    panel.before(workspace);
    workspace.append(sidebar, panel);
    sidebar.append(head, list, foot);
    if (add) foot.append(add);
    if (count) foot.append(count);
    return workspace;
  }

  function selectedRouteAlias(cards) {
    const focused = document.activeElement?.closest?.(".route-card");
    const focusedAlias = routeAlias(focused);
    if (focused && cards.includes(focused)) return focusedAlias;

    const stored = localStorage.getItem(STORAGE.route);
    if (stored && cards.some(card => routeAlias(card) === stored)) return stored;
    return routeAlias(cards[0]);
  }

  function selectRoute(alias, focus = false) {
    const cards = routeCards();
    const selected = cards.find(card => routeAlias(card) === alias) || cards[0];
    if (!selected) return;
    const value = routeAlias(selected);
    localStorage.setItem(STORAGE.route, value);

    cards.forEach(card => {
      const active = card === selected;
      if (card.hidden === active) card.hidden = !active;
      if (card.getAttribute("aria-hidden") !== String(!active)) {
        card.setAttribute("aria-hidden", String(!active));
      }
    });
    all(".apple-route-item").forEach(button => {
      const active = button.dataset.routeAlias === value;
      if (button.getAttribute("aria-selected") !== String(active)) {
        button.setAttribute("aria-selected", String(active));
      }
      button.tabIndex = active ? 0 : -1;
    });
    if (focus) later(() => selected.querySelector(".route-alias, select, button")?.focus({ preventScroll: true }));
  }

  function routeSignature(cards) {
    return cards.map(card => [
      routeAlias(card),
      routeModel(card),
      routeTone(card),
      routeStateText(card)
    ].join("::")).join("||");
  }

  function syncRouteWorkspace() {
    if (state.syncingRoute) return;
    const workspace = ensureRouteWorkspace();
    const cards = routeCards();
    const list = workspace?.querySelector(".apple-route-list");
    if (!workspace || !list) return;

    const selected = selectedRouteAlias(cards);
    const signature = routeSignature(cards);
    if (signature === state.routeSignature && list.children.length === cards.length) {
      selectRoute(selected, false);
      return;
    }

    state.syncingRoute = true;
    try {
      state.routeSignature = signature;
      list.replaceChildren();
      cards.forEach(card => {
        const alias = routeAlias(card);
        const model = routeModel(card);
        const tone = routeTone(card);
        const button = el("button", "apple-route-item", {
          type: "button",
          "data-route-alias": alias,
          "aria-selected": String(alias === selected)
        });
        const copy = el("span", "apple-route-copy");
        copy.append(el("strong", "", { text: alias }), el("small", "", { text: model }));
        const status = el("span", `apple-route-state ${tone === "good" ? "" : tone}`.trim(), {
          text: routeStateText(card)
        });
        button.append(copy, status);
        button.addEventListener("click", () => selectRoute(alias, true));
        list.append(button);
      });
      selectRoute(selected, false);
    } finally {
      state.syncingRoute = false;
    }
  }

  function keyCreatorElements() {
    const view = byId("view-keys");
    if (!view) return {};
    return {
      view,
      head: view.querySelector(".page-head"),
      preset: view.querySelector(".key-preset-panel"),
      advanced: byId("keyAdvanced"),
      created: byId("createdKeyCard"),
      keyList: view.querySelector(".key-list-panel")
    };
  }

  function setKeyCreator(open) {
    const creator = byId("appleKeyCreator");
    const trigger = byId("appleKeyCreateTrigger");
    if (!creator || !trigger) return;
    if (creator.hidden === open) creator.hidden = !open;
    trigger.setAttribute("aria-expanded", String(open));
    if (open) later(() => creator.querySelector(".key-preset.active, .key-preset, button")?.focus({ preventScroll: true }));
  }

  function installKeyCreator() {
    const { view, head, preset, advanced, created, keyList } = keyCreatorElements();
    if (!view || !head || !preset || !advanced || !keyList) return;

    let creator = byId("appleKeyCreator");
    if (!creator) {
      let actions = head.querySelector(".actions");
      if (!actions) {
        actions = el("div", "actions");
        head.append(actions);
      }
      const trigger = el("button", "primary apple-key-create-trigger", {
        id: "appleKeyCreateTrigger",
        type: "button",
        "aria-expanded": "false",
        "aria-controls": "appleKeyCreator"
      });
      trigger.append(icon("i-plus"), document.createTextNode("创建 API Key"));
      trigger.addEventListener("click", () => setKeyCreator(creator.hidden));
      actions.append(trigger);

      creator = el("section", "apple-key-creator", { id: "appleKeyCreator", hidden: "hidden" });
      const creatorHead = el("div", "apple-key-creator-head");
      const copy = el("div", "");
      copy.append(
        el("strong", "", { text: "创建新的 API Key" }),
        el("span", "", { text: "先选一个用途；只有需要时再展开高级限制。" })
      );
      const close = el("button", "apple-key-creator-close", { type: "button", "aria-label": "关闭创建面板" });
      close.append(icon("i-close"));
      close.addEventListener("click", () => setKeyCreator(false));
      creatorHead.append(copy, close);
      creator.append(creatorHead, preset, advanced);
      head.after(creator);
    }

    const hasSecret = Boolean(byId("createdKey")?.value?.trim()) && created && !created.hidden;
    if (hasSecret && !state.keySecretSeen) {
      state.keySecretSeen = true;
      setKeyCreator(false);
      later(() => created.scrollIntoView({ behavior: "smooth", block: "start" }));
    }
    if (!hasSecret) state.keySecretSeen = false;
  }

  function simplifyTopbar() {
    const subtitle = byId("viewSubtitle");
    if (subtitle && !subtitle.hidden) subtitle.hidden = true;
    const command = document.querySelector(".qc-command-trigger");
    if (command && command.dataset.appleLabel !== "true") {
      command.dataset.appleLabel = "true";
      const text = Array.from(command.childNodes).find(node => node.nodeType === Node.TEXT_NODE);
      if (text) text.textContent = "搜索";
      command.title = "搜索与快速操作";
    }
  }

  function sync() {
    installOverviewSummary();
    syncRouteWorkspace();
    installKeyCreator();
    simplifyTopbar();
  }

  function schedule() {
    if (state.queued) return;
    state.queued = true;
    later(() => {
      state.queued = false;
      sync();
    });
  }

  function installObserver() {
    const main = document.querySelector(".main");
    if (!main) return;
    new MutationObserver(schedule).observe(main, {
      subtree: true,
      childList: true,
      attributes: true,
      attributeFilter: ["hidden", "class", "disabled", "open"]
    });
  }

  function init() {
    updateIdentity();
    sync();
    installObserver();
    schedule();
  }

  window.Lite2APIAppleSimple = Object.freeze({
    version: BUILD,
    selectRoute,
    setKeyCreator,
    sync
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init, { once: true });
  } else {
    init();
  }
})();
