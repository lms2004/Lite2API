/* Lite2API Quiet Control v3 — behavior layer.
   This script enhances the existing admin application without changing API contracts. */
(() => {
  "use strict";

  const BUILD = "Quiet Control 3.0 · 2026.08.17";
  const STORAGE = {
    density: "lite2api.ui.density",
    routeHelp: "lite2api.routeHelpDismissed",
    sourcePane: "lite2api.sourcesPane"
  };
  const interactiveSelector = "button,a,input,select,textarea,summary,[role='button']";
  const qc = {
    commandIndex: 0,
    commandMatches: [],
    dialogReturnFocus: null,
    sheetReturnFocus: null,
    mutationQueued: false,
    tableSignature: new WeakMap()
  };

  const byId = id => document.getElementById(id);
  const all = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const later = fn => window.requestAnimationFrame(() => window.requestAnimationFrame(fn));

  function isEditable(target) {
    return target instanceof HTMLInputElement ||
      target instanceof HTMLTextAreaElement ||
      target instanceof HTMLSelectElement ||
      Boolean(target?.isContentEditable);
  }

  function safeCall(name, ...args) {
    const fn = window[name];
    if (typeof fn !== "function") return undefined;
    return fn(...args);
  }

  function wrapGlobal(name, after) {
    const original = window[name];
    if (typeof original !== "function" || original.__quietControlWrapped) return;
    const wrapped = function (...args) {
      try {
        const result = original.apply(this, args);
        if (result && typeof result.finally === "function") {
          result.finally(() => later(after));
        } else {
          later(after);
        }
        return result;
      } catch (error) {
        later(after);
        throw error;
      }
    };
    Object.defineProperty(wrapped, "__quietControlWrapped", { value: true });
    window[name] = wrapped;
  }

  function createElement(tag, className, attributes = {}) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    for (const [name, value] of Object.entries(attributes)) {
      if (value === undefined || value === null) continue;
      if (name === "text") node.textContent = value;
      else if (name === "html") node.innerHTML = value;
      else node.setAttribute(name, String(value));
    }
    return node;
  }

  function iconUse(id) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
    svg.setAttribute("class", "icon");
    svg.setAttribute("aria-hidden", "true");
    use.setAttribute("href", `#${id}`);
    svg.append(use);
    return svg;
  }

  function updateBuildLabels() {
    document.documentElement.dataset.ui = "quiet-control-v3";
    const build = byId("uiBuild");
    if (build) build.textContent = BUILD;
    const vpn = document.querySelector(".vpn-auth");
    if (vpn) {
      const consoleLabel = all("span", vpn).find(node => /Console\s*\d/i.test(node.textContent || ""));
      if (consoleLabel) consoleLabel.textContent = "VPN 管理会话";
    }
  }

  function applyDensity(value) {
    const next = value === "comfortable" ? "comfortable" : "compact";
    document.documentElement.dataset.density = next;
    localStorage.setItem(STORAGE.density, next);
    const button = document.querySelector(".qc-density-toggle");
    if (button) {
      button.textContent = next === "compact" ? "紧凑" : "舒适";
      button.setAttribute("aria-pressed", String(next === "compact"));
      button.title = next === "compact" ? "当前为紧凑密度，点击切换为舒适" : "当前为舒适密度，点击切换为紧凑";
    }
  }

  function toggleDensity() {
    applyDensity(document.documentElement.dataset.density === "compact" ? "comfortable" : "compact");
    window.dispatchEvent(new Event("resize"));
  }

  function installTopbarTools() {
    const vpn = document.querySelector(".vpn-auth");
    if (!vpn || document.querySelector(".qc-topbar-tools")) return;

    const tools = createElement("div", "qc-topbar-tools");
    const density = createElement("button", "qc-density-toggle", {
      type: "button",
      "aria-pressed": "true",
      title: "切换信息密度"
    });
    density.addEventListener("click", toggleDensity);

    const command = createElement("button", "qc-command-trigger", {
      type: "button",
      "aria-haspopup": "dialog",
      "aria-controls": "qcCommandDialog",
      title: "打开命令面板"
    });
    command.append(document.createTextNode("命令"));
    const shortcut = createElement("kbd", "", { text: "⌘K" });
    command.append(shortcut);
    command.addEventListener("click", openCommandPalette);

    tools.append(density, command);
    vpn.append(tools);
    applyDensity(localStorage.getItem(STORAGE.density) || "compact");
  }

  function commandDefinitions() {
    const routeSave = byId("routeSaveBtn");
    return [
      {
        label: "前往运行总览",
        detail: "检查健康、异常、容量与请求证据",
        icon: "概",
        shortcut: "G O",
        run: () => safeCall("showView", "monitor")
      },
      {
        label: "前往模型路由",
        detail: "编辑逻辑模型、推理强度与真实渠道顺序",
        icon: "路",
        shortcut: "G R",
        run: () => safeCall("showView", "routes")
      },
      {
        label: "前往上游来源",
        detail: "查看账号额度或路由连接",
        icon: "源",
        shortcut: "G S",
        run: () => safeCall("showView", "accounts")
      },
      {
        label: "前往客户端访问",
        detail: "创建、验证或撤销客户端 Key",
        icon: "钥",
        shortcut: "G C",
        run: () => safeCall("showView", "keys")
      },
      {
        label: "接入新的上游",
        detail: "打开 OAuth、设备授权或手动 API 接入",
        icon: "+",
        run: () => {
          safeCall("showView", "accounts");
          later(() => safeCall("openQuickAdd"));
        }
      },
      {
        label: "创建客户端 Key",
        detail: "使用当前安全预设创建一次性明文 Key",
        icon: "+",
        run: () => {
          safeCall("showView", "keys");
          later(() => byId("quickCreateKeyBtn")?.focus());
        }
      },
      {
        label: "搜索请求记录",
        detail: "前往总览并聚焦请求搜索",
        icon: "搜",
        shortcut: "/",
        run: () => {
          safeCall("showView", "monitor");
          later(() => byId("requestSearch")?.focus());
        }
      },
      {
        label: "仅查看失败请求",
        detail: "筛选限流、上游错误与网关错误",
        icon: "!",
        run: () => {
          safeCall("showView", "monitor");
          later(() => {
            const status = byId("requestStatus");
            if (!status) return;
            status.value = "error";
            status.dispatchEvent(new Event("change", { bubbles: true }));
            safeCall("renderMonitor");
          });
        }
      },
      {
        label: "保存路由修改",
        detail: routeSave?.disabled ? "当前没有未保存修改" : "保存配置并立即热加载",
        icon: "存",
        disabled: !routeSave || routeSave.disabled,
        shortcut: "⌘↵",
        run: () => safeCall("saveRoutes")
      },
      {
        label: "刷新管理状态",
        detail: "重新读取账号、路由与运行快照",
        icon: "刷",
        run: () => {
          if (typeof window.load === "function") safeCall("load");
          else window.location.reload();
        }
      },
      {
        label: "切换界面密度",
        detail: document.documentElement.dataset.density === "compact"
          ? "切换为舒适行高"
          : "切换为紧凑行高",
        icon: "密",
        run: toggleDensity
      },
      {
        label: "打开提示词注入检测",
        detail: "直连真实渠道运行安全探针",
        icon: "检",
        run: () => safeCall("showView", "prompt-test")
      },
      {
        label: "打开适配器目录",
        detail: "查看协议能力、运行状态与候选项目",
        icon: "配",
        run: () => safeCall("showView", "adapters")
      }
    ];
  }

  function installCommandPalette() {
    if (byId("qcCommandDialog")) return;

    const dialog = createElement("dialog", "qc-command-dialog", {
      id: "qcCommandDialog",
      "aria-label": "Lite2API 命令面板"
    });
    const input = createElement("input", "qc-command-search", {
      id: "qcCommandSearch",
      type: "search",
      autocomplete: "off",
      placeholder: "搜索页面、动作或状态…",
      "aria-controls": "qcCommandList",
      "aria-label": "搜索命令"
    });
    const list = createElement("div", "qc-command-list", {
      id: "qcCommandList",
      role: "listbox",
      "aria-label": "可用命令"
    });
    dialog.append(input, list);
    document.body.append(dialog);

    input.addEventListener("input", () => {
      qc.commandIndex = 0;
      renderCommands(input.value);
    });
    input.addEventListener("keydown", event => {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        moveCommandSelection(1);
      } else if (event.key === "ArrowUp") {
        event.preventDefault();
        moveCommandSelection(-1);
      } else if (event.key === "Enter") {
        event.preventDefault();
        runSelectedCommand();
      }
    });
    dialog.addEventListener("close", () => {
      qc.dialogReturnFocus?.focus?.();
      qc.dialogReturnFocus = null;
    });
    dialog.addEventListener("click", event => {
      if (event.target === dialog) dialog.close();
    });
  }

  function openCommandPalette() {
    const dialog = byId("qcCommandDialog");
    const input = byId("qcCommandSearch");
    if (!dialog || !input) return;
    qc.dialogReturnFocus = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
    input.value = "";
    qc.commandIndex = 0;
    renderCommands("");
    if (!dialog.open) dialog.showModal();
    later(() => input.focus());
  }

  function renderCommands(query) {
    const list = byId("qcCommandList");
    if (!list) return;
    const normalized = String(query || "").trim().toLocaleLowerCase("zh-CN");
    qc.commandMatches = commandDefinitions().filter(command => {
      if (!normalized) return true;
      return `${command.label} ${command.detail}`.toLocaleLowerCase("zh-CN").includes(normalized);
    });
    qc.commandIndex = Math.max(0, Math.min(qc.commandIndex, Math.max(0, qc.commandMatches.length - 1)));
    list.replaceChildren();

    if (!qc.commandMatches.length) {
      list.append(createElement("div", "qc-command-empty", { text: "没有匹配命令" }));
      return;
    }

    qc.commandMatches.forEach((command, index) => {
      const button = createElement("button", "qc-command-item", {
        type: "button",
        role: "option",
        "aria-selected": String(index === qc.commandIndex)
      });
      button.disabled = Boolean(command.disabled);
      const icon = createElement("span", "qc-command-icon", { text: command.icon });
      const copy = createElement("span", "qc-command-copy");
      copy.append(
        createElement("strong", "", { text: command.label }),
        createElement("small", "", { text: command.detail })
      );
      const shortcut = createElement("span", "qc-command-shortcut", { text: command.shortcut || "" });
      button.append(icon, copy, shortcut);
      button.addEventListener("pointerenter", () => {
        qc.commandIndex = index;
        syncCommandSelection();
      });
      button.addEventListener("click", () => runCommand(command));
      list.append(button);
    });
  }

  function syncCommandSelection() {
    all(".qc-command-item", byId("qcCommandList")).forEach((item, index) => {
      item.setAttribute("aria-selected", String(index === qc.commandIndex));
      if (index === qc.commandIndex) item.scrollIntoView({ block: "nearest" });
    });
  }

  function moveCommandSelection(delta) {
    if (!qc.commandMatches.length) return;
    let next = qc.commandIndex;
    do {
      next = (next + delta + qc.commandMatches.length) % qc.commandMatches.length;
    } while (qc.commandMatches[next]?.disabled && next !== qc.commandIndex);
    qc.commandIndex = next;
    syncCommandSelection();
  }

  function runSelectedCommand() {
    const command = qc.commandMatches[qc.commandIndex];
    if (command && !command.disabled) runCommand(command);
  }

  function runCommand(command) {
    const dialog = byId("qcCommandDialog");
    if (dialog?.open) dialog.close();
    later(() => command.run());
  }

  function installKeyboardShortcuts() {
    document.addEventListener("keydown", event => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLocaleLowerCase() === "k") {
        event.preventDefault();
        openCommandPalette();
        return;
      }
      if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
        const routeSave = byId("routeSaveBtn");
        if (document.querySelector("#view-routes.active") && routeSave && !routeSave.disabled) {
          event.preventDefault();
          safeCall("saveRoutes");
        }
        return;
      }
      if (event.key === "/" && !isEditable(event.target) && !document.querySelector("dialog[open]")) {
        const active = document.querySelector(".view.active");
        const search = active?.querySelector(".search input");
        if (search) {
          event.preventDefault();
          search.focus();
        }
        return;
      }
      if (event.key === "Escape" && document.body.classList.contains("qc-sheet-open")) {
        event.preventDefault();
        closeSheet();
      }
    });
  }

  function installSourceTabs() {
    const view = byId("view-accounts");
    if (!view || view.dataset.qcSourceTabs === "true") return;

    const pageHead = view.querySelector(".page-head");
    const kicker = view.querySelector(".section-kicker");
    const metrics = byId("metrics");
    const accountsPanel = view.querySelector(".auth-pool-panel");
    const connections = view.querySelector(".route-slots");
    if (!pageHead || !accountsPanel || !connections) return;

    view.dataset.qcSourceTabs = "true";
    const tabs = createElement("div", "qc-source-tabs", {
      role: "tablist",
      "aria-label": "上游来源类型"
    });
    const accountsButton = createElement("button", "qc-source-tab", {
      type: "button",
      role: "tab",
      id: "qcSourceAccountsTab",
      "aria-controls": "qcSourceAccounts",
      text: "账号与额度"
    });
    const connectionsButton = createElement("button", "qc-source-tab", {
      type: "button",
      role: "tab",
      id: "qcSourceConnectionsTab",
      "aria-controls": "qcSourceConnections",
      text: "API 与连接"
    });
    tabs.append(accountsButton, connectionsButton);

    const accountsPane = createElement("div", "qc-source-pane", {
      id: "qcSourceAccounts",
      role: "tabpanel",
      "aria-labelledby": accountsButton.id
    });
    const connectionsPane = createElement("div", "qc-source-pane", {
      id: "qcSourceConnections",
      role: "tabpanel",
      "aria-labelledby": connectionsButton.id
    });
    if (kicker) accountsPane.append(kicker);
    if (metrics) accountsPane.append(metrics);
    accountsPane.append(accountsPanel);
    connections.open = true;
    connectionsPane.append(connections);

    pageHead.after(tabs, accountsPane, connectionsPane);
    accountsButton.addEventListener("click", () => selectSourcePane("accounts"));
    connectionsButton.addEventListener("click", () => selectSourcePane("connections"));
    selectSourcePane(localStorage.getItem(STORAGE.sourcePane) || "accounts");
  }

  function selectSourcePane(name) {
    const accounts = byId("qcSourceAccounts");
    const connections = byId("qcSourceConnections");
    const accountsTab = byId("qcSourceAccountsTab");
    const connectionsTab = byId("qcSourceConnectionsTab");
    if (!accounts || !connections || !accountsTab || !connectionsTab) return;

    const showConnections = name === "connections";
    accounts.hidden = showConnections;
    connections.hidden = !showConnections;
    accountsTab.setAttribute("aria-selected", String(!showConnections));
    connectionsTab.setAttribute("aria-selected", String(showConnections));
    accountsTab.tabIndex = showConnections ? -1 : 0;
    connectionsTab.tabIndex = showConnections ? 0 : -1;
    localStorage.setItem(STORAGE.sourcePane, showConnections ? "connections" : "accounts");
    later(enhanceTables);
  }

  function installRouteHelp() {
    const view = byId("view-routes");
    const help = view?.querySelector(".route-chain-explain");
    const actions = view?.querySelector(".page-head .actions");
    if (!view || !help || !actions || help.dataset.qcHelp === "true") return;

    help.dataset.qcHelp = "true";
    help.classList.add("qc-help-enhanced");
    const close = createElement("button", "qc-route-help-close", {
      type: "button",
      "aria-label": "隐藏路由说明",
      title: "隐藏路由说明"
    });
    close.append(iconUse("i-close"));
    close.addEventListener("click", () => setRouteHelp(false));
    help.append(close);

    const toggle = createElement("button", "qc-route-help-toggle", {
      type: "button",
      "aria-expanded": "true",
      title: "显示路由编辑说明",
      text: "使用说明"
    });
    toggle.addEventListener("click", () => setRouteHelp(help.hidden));
    actions.insertBefore(toggle, actions.firstChild);

    setRouteHelp(localStorage.getItem(STORAGE.routeHelp) !== "1", false);
  }

  function setRouteHelp(show, persist = true) {
    const help = document.querySelector("#view-routes .route-chain-explain");
    const toggle = document.querySelector(".qc-route-help-toggle");
    if (!help || !toggle) return;
    help.hidden = !show;
    toggle.setAttribute("aria-expanded", String(show));
    toggle.textContent = show ? "隐藏说明" : "使用说明";
    if (persist) localStorage.setItem(STORAGE.routeHelp, show ? "0" : "1");
  }

  function syncClientSetupVisibility() {
    const setup = byId("clientSetup");
    const card = byId("createdKeyCard");
    const key = byId("createdKey");
    if (!setup || !card) return;
    const hasCurrentSecret = !card.hidden && Boolean(key?.value?.trim());
    setup.hidden = !hasCurrentSecret;
  }

  function installClientSetupGuard() {
    const card = byId("createdKeyCard");
    if (!card || card.dataset.qcObserved === "true") return;
    card.dataset.qcObserved = "true";
    const observer = new MutationObserver(syncClientSetupVisibility);
    observer.observe(card, { attributes: true, subtree: true, childList: true, characterData: true });
    byId("createdKey")?.addEventListener("input", syncClientSetupVisibility);
    syncClientSetupVisibility();
  }

  function installRouteDraftBar() {
    const save = byId("routeSaveBtn");
    if (!save || byId("qcRouteDraft")) return;

    const bar = createElement("aside", "qc-route-draft", {
      id: "qcRouteDraft",
      "aria-live": "polite",
      hidden: "hidden"
    });
    const copy = createElement("div", "qc-route-draft-copy");
    copy.append(
      createElement("strong", "", { text: "存在未保存的路由修改" }),
      createElement("span", "", { text: "保存后立即热加载；当前运行配置尚未改变。" })
    );
    const actions = createElement("div", "qc-route-draft-actions");
    const review = createElement("button", "", { type: "button", text: "查看差异" });
    const discard = createElement("button", "", { type: "button", text: "放弃修改" });
    const commit = createElement("button", "primary", { type: "button", text: "保存并热加载" });
    review.addEventListener("click", () => {
      const summary = byId("routeChangeSummary");
      if (summary && !summary.hidden) summary.scrollIntoView({ behavior: "smooth", block: "center" });
      else document.querySelector("#view-routes .route-chain-list")?.scrollIntoView({ behavior: "smooth", block: "start" });
    });
    discard.addEventListener("click", () => {
      if (window.confirm("放弃全部未保存的路由修改并重新读取当前配置？")) window.location.reload();
    });
    commit.addEventListener("click", () => safeCall("saveRoutes"));
    actions.append(review, discard, commit);
    bar.append(copy, actions);
    document.body.append(bar);

    const sync = () => {
      const active = Boolean(document.querySelector("#view-routes.active"));
      bar.hidden = save.disabled || !active;
    };
    new MutationObserver(sync).observe(save, { attributes: true, attributeFilter: ["disabled"] });
    new MutationObserver(sync).observe(byId("view-routes"), { attributes: true, attributeFilter: ["class"] });
    sync();
  }

  function installSheet() {
    if (byId("qcDetailsSheet")) return;

    const backdrop = createElement("button", "qc-sheet-backdrop", {
      id: "qcSheetBackdrop",
      type: "button",
      "aria-label": "关闭详情"
    });
    const sheet = createElement("aside", "qc-sheet", {
      id: "qcDetailsSheet",
      "aria-hidden": "true",
      "aria-labelledby": "qcSheetTitle"
    });
    const head = createElement("div", "qc-sheet-head");
    const heading = createElement("div", "");
    heading.append(
      createElement("strong", "", { id: "qcSheetTitle", text: "详情" }),
      createElement("span", "", { id: "qcSheetSubtitle", text: "当前列表项" })
    );
    const close = createElement("button", "qc-sheet-close", {
      type: "button",
      "aria-label": "关闭详情"
    });
    close.append(iconUse("i-close"));
    const body = createElement("div", "qc-sheet-body", { id: "qcSheetBody" });
    close.addEventListener("click", closeSheet);
    backdrop.addEventListener("click", closeSheet);
    head.append(heading, close);
    sheet.append(head, body);
    document.body.append(backdrop, sheet);

    document.addEventListener("click", event => {
      const row = event.target.closest(".data-table tbody tr[data-qc-inspectable='true']");
      if (!row || event.target.closest(interactiveSelector)) return;
      openRowSheet(row);
    });
    document.addEventListener("keydown", event => {
      if (event.key !== "Enter" && event.key !== " ") return;
      const row = event.target.closest(".data-table tbody tr[data-qc-inspectable='true']");
      if (!row) return;
      event.preventDefault();
      openRowSheet(row);
    });
  }

  function openRowSheet(row) {
    const table = row.closest("table");
    if (!table) return;
    qc.sheetReturnFocus = row;
    const headers = all("thead th", table).map(header =>
      (header.textContent || header.getAttribute("aria-label") || "").trim()
    );
    const cells = all(":scope > td", row);
    const fields = cells.map((cell, index) => ({
      label: headers[index] || cell.dataset.label || `字段 ${index + 1}`,
      value: (cell.innerText || cell.textContent || "—").trim()
    })).filter(field => field.label && field.value && field.value !== "—");

    const title = fields.find(field => !/选择|操作/.test(field.label))?.value || "详情";
    const titleNode = byId("qcSheetTitle");
    const subtitleNode = byId("qcSheetSubtitle");
    const body = byId("qcSheetBody");
    if (!titleNode || !subtitleNode || !body) return;

    titleNode.textContent = title.split("\n")[0].slice(0, 80);
    subtitleNode.textContent = table.getAttribute("aria-label") ||
      table.closest(".panel")?.querySelector("h2")?.textContent ||
      "当前列表项";

    const list = createElement("dl", "qc-sheet-grid");
    for (const field of fields) {
      if (/选择|操作/.test(field.label)) continue;
      const item = createElement("div", "qc-sheet-field");
      item.append(
        createElement("dt", "", { text: field.label }),
        createElement("dd", "", { text: field.value })
      );
      list.append(item);
    }
    body.replaceChildren(list);
    document.body.classList.add("qc-sheet-open");
    byId("qcDetailsSheet")?.setAttribute("aria-hidden", "false");
    later(() => document.querySelector(".qc-sheet-close")?.focus());
  }

  function closeSheet() {
    document.body.classList.remove("qc-sheet-open");
    byId("qcDetailsSheet")?.setAttribute("aria-hidden", "true");
    qc.sheetReturnFocus?.focus?.();
    qc.sheetReturnFocus = null;
  }

  function enhanceTables() {
    all(".data-table").forEach(table => {
      const headers = all("thead th", table).map(header =>
        (header.textContent || header.getAttribute("aria-label") || "").trim()
      );
      const signature = headers.join("|");
      if (qc.tableSignature.get(table) !== signature) qc.tableSignature.set(table, signature);

      all("tbody tr", table).forEach(row => {
        const cells = all(":scope > td", row);
        if (cells.length === 1 && cells[0].hasAttribute("colspan")) {
          row.removeAttribute("data-qc-inspectable");
          row.removeAttribute("tabindex");
          return;
        }
        cells.forEach((cell, index) => {
          const label = headers[index] || "";
          cell.dataset.label = label;
        });
        row.dataset.qcInspectable = "true";
        row.tabIndex = 0;
        const first = cells.find(cell => (cell.innerText || "").trim());
        row.setAttribute("aria-label", `查看详情：${(first?.innerText || "当前行").trim().replace(/\s+/g, " ")}`);
      });
    });
  }

  function syncNavAccessibility() {
    all(".nav button, .diagnostics-nav button").forEach(button => {
      if (button.classList.contains("active")) button.setAttribute("aria-current", "page");
      else button.removeAttribute("aria-current");
    });
  }

  function syncFreshness() {
    const updated = byId("lastUpdated");
    if (!updated) return;
    const text = updated.textContent || "";
    const stale = /等待|失败|过期|未知/.test(text);
    updated.classList.toggle("qc-stale", stale);
    updated.classList.toggle("qc-fresh", !stale);
    updated.title = stale
      ? "当前状态尚未完成新鲜度验证"
      : `最近同步：${text}`;
  }

  function afterRender() {
    if (qc.mutationQueued) return;
    qc.mutationQueued = true;
    later(() => {
      qc.mutationQueued = false;
      enhanceTables();
      syncClientSetupVisibility();
      syncNavAccessibility();
      syncFreshness();
      installSourceTabs();
      installRouteHelp();
      installRouteDraftBar();
    });
  }

  function installDialogFocusManagement() {
    const open = window.openDialog;
    if (typeof open === "function" && !open.__quietControlFocusWrapped) {
      const wrappedOpen = function (...args) {
        qc.dialogReturnFocus = document.activeElement instanceof HTMLElement
          ? document.activeElement
          : null;
        const result = open.apply(this, args);
        later(() => {
          const dialog = document.querySelector("dialog[open]");
          if (!dialog) return;
          const focusable = dialog.querySelector(
            "input:not([disabled]),select:not([disabled]),textarea:not([disabled]),button:not([disabled]),summary,[tabindex='0']"
          );
          focusable?.focus({ preventScroll: true });
        });
        return result;
      };
      Object.defineProperty(wrappedOpen, "__quietControlFocusWrapped", { value: true });
      window.openDialog = wrappedOpen;
    }

    const close = window.closeDialog;
    if (typeof close === "function" && !close.__quietControlFocusWrapped) {
      const wrappedClose = function (...args) {
        const result = close.apply(this, args);
        later(() => {
          qc.dialogReturnFocus?.focus?.();
          qc.dialogReturnFocus = null;
        });
        return result;
      };
      Object.defineProperty(wrappedClose, "__quietControlFocusWrapped", { value: true });
      window.closeDialog = wrappedClose;
    }
  }

  function installGlobalWrappers() {
    [
      "showView",
      "renderMonitor",
      "renderRoutes",
      "renderAccounts",
      "renderOAuthAccounts",
      "renderKeys",
      "renderAdapters",
      "renderClientSetup",
      "createQuickKey",
      "createClientKey",
      "saveRoutes"
    ].forEach(name => wrapGlobal(name, afterRender));
  }

  function installMutationObserver() {
    const root = document.querySelector(".main");
    if (!root) return;
    const observer = new MutationObserver(afterRender);
    observer.observe(root, {
      subtree: true,
      childList: true,
      attributes: true,
      attributeFilter: ["class", "hidden", "disabled", "open"]
    });
    const updated = byId("lastUpdated");
    if (updated) {
      new MutationObserver(syncFreshness).observe(updated, {
        subtree: true,
        childList: true,
        characterData: true
      });
    }
  }

  function init() {
    updateBuildLabels();
    installTopbarTools();
    installCommandPalette();
    installKeyboardShortcuts();
    installSheet();
    installSourceTabs();
    installRouteHelp();
    installClientSetupGuard();
    installRouteDraftBar();
    installDialogFocusManagement();
    installGlobalWrappers();
    installMutationObserver();
    afterRender();
  }

  window.Lite2APIQuietControl = Object.freeze({
    version: BUILD,
    openCommandPalette,
    toggleDensity,
    selectSourcePane,
    closeSheet,
    enhanceTables
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init, { once: true });
  } else {
    init();
  }
})();
