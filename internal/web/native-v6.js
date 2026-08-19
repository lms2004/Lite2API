/* Lite2API Native v6 — content-level enhancements.
   Keeps v5's static master-detail DOM and changes only what is shown by default. */
(() => {
  "use strict";

  const BUILD = "Native 6.0 · 2026.08.18";
  const ui = { scheduled: false };
  const byId = id => document.getElementById(id);
  const all = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const later = fn => requestAnimationFrame(() => requestAnimationFrame(fn));

  function syncIdentity() {
    document.documentElement.dataset.ui = "native-v6";
    const build = byId("uiBuild");
    if (build) build.textContent = "UI build 2026.08.18-v6";
  }

  function syncFreshness() {
    const node = byId("lastUpdated");
    if (!node) return;
    const text = (node.textContent || "").trim();
    if (/最近更新[：:]/.test(text)) {
      node.dataset.exact = text;
      node.title = text;
      node.textContent = "刚刚更新";
    }
  }

  function syncEndpoint() {
    const target = byId("v6ClientBaseURL");
    if (!target) return;
    let value = "";
    try {
      if (typeof gatewayAPIBase === "function") value = gatewayAPIBase();
    } catch (_) {}
    if (!value) value = `${location.origin}${location.pathname.replace(/\/admin\/?$/, "")}/v1`;
    target.textContent = value;
    target.title = value;

    const copy = byId("v6CopyBaseURL");
    if (copy && copy.dataset.bound !== "1") {
      copy.dataset.bound = "1";
      copy.addEventListener("click", async () => {
        try {
          await navigator.clipboard.writeText(target.textContent || "");
          copy.textContent = "已复制";
          setTimeout(() => { copy.textContent = "复制"; }, 1200);
        } catch (_) {
          const range = document.createRange();
          range.selectNodeContents(target);
          const selection = window.getSelection();
          selection.removeAllRanges();
          selection.addRange(range);
        }
      });
    }
  }

  function cellText(cell) {
    return (cell?.innerText || cell?.textContent || "").replace(/\s+/g, " ").trim();
  }

  // The stable renderer renames the legacy tbody id from `keys` to `keyRows`
  // before the native layers load. Keep the enhancement compatible with both
  // the remapped production DOM and the unremapped fixture/preview DOM.
  function keyTableBody() {
    return byId("keyRows") || byId("keys");
  }

  function keyRowFromTable(row) {
    const cells = all(":scope > td", row);
    if (cells.length < 7 || cells[0]?.hasAttribute("colspan")) return null;

    const item = document.createElement("div");
    item.className = "key-setting-row";

    const icon = document.createElement("span");
    icon.className = "key-setting-icon";
    const existingIcon = cells[0].querySelector(".icon")?.cloneNode(true);
    if (existingIcon) icon.append(existingIcon);
    else icon.textContent = "•";

    const main = document.createElement("div");
    main.className = "key-setting-main";
    const name = document.createElement("strong");
    name.textContent = cells[0].querySelector("strong")?.textContent?.trim() || cellText(cells[0]) || "API Key";
    const detail = document.createElement("small");
    const prefix = cellText(cells[1]) || "—";
    const requests = cells[0].querySelector("small")?.textContent?.trim() || "";
    detail.textContent = requests ? `${prefix} · ${requests}` : prefix;
    main.append(name, detail);

    const scope = document.createElement("div");
    scope.className = "key-setting-scope";
    const model = document.createElement("strong");
    model.textContent = cellText(cells[2]) || "全部模型";
    const limits = document.createElement("span");
    limits.textContent = `RPM ${cellText(cells[3]) || "—"} · 并发 ${cellText(cells[4]) || "—"}`;
    scope.append(model, limits);

    const status = document.createElement("div");
    status.className = "key-setting-state";
    const badge = cells[5].querySelector(".badge")?.cloneNode(true);
    if (badge) status.append(badge);
    else {
      const text = document.createElement("span");
      text.textContent = cellText(cells[5]) || "—";
      status.append(text);
    }
    const expires = document.createElement("small");
    expires.textContent = cellText(cells[6]) || "—";
    status.append(expires);

    const action = document.createElement("div");
    action.className = "key-setting-action";
    const actionButton = cells[7]?.querySelector("button")?.cloneNode(true);
    if (actionButton) action.append(actionButton);

    item.append(icon, main, scope, status, action);
    return item;
  }

  function syncKeyList() {
    const tbody = keyTableBody();
    const list = byId("v6KeyList");
    if (!tbody || !list) return;
    const rows = all(":scope > tr", tbody);
    const items = rows.map(keyRowFromTable).filter(Boolean);
    list.replaceChildren();
    if (items.length) {
      list.append(...items);
      return;
    }
    const empty = document.createElement("div");
    empty.className = "empty";
    empty.innerHTML = "<strong>还没有 API Key</strong>新建一个密钥后，就可以从客户端连接 Lite2API。";
    list.append(empty);
  }

  function sync() {
    ui.scheduled = false;
    syncIdentity();
    syncFreshness();
    syncEndpoint();
    syncKeyList();
  }

  function schedule() {
    if (ui.scheduled) return;
    ui.scheduled = true;
    later(sync);
  }

  function wrap(name) {
    const original = window[name];
    if (typeof original !== "function" || original.__nativeV6Wrapped) return;
    const wrapped = function (...args) {
      const result = original.apply(this, args);
      if (result && typeof result.finally === "function") result.finally(schedule);
      else schedule();
      return result;
    };
    Object.defineProperty(wrapped, "__nativeV6Wrapped", { value: true });
    window[name] = wrapped;
  }

  function installObservers() {
    const updated = byId("lastUpdated");
    if (updated) new MutationObserver(syncFreshness).observe(updated, { childList: true, subtree: true, characterData: true });
    const keys = keyTableBody();
    if (keys) new MutationObserver(schedule).observe(keys, { childList: true, subtree: true, characterData: true, attributes: true });
  }

  function init() {
    ["render", "renderKeys", "showView", "createQuickKey", "createClientKey"].forEach(wrap);
    installObservers();
    sync();
  }

  window.Lite2APINativeV6 = Object.freeze({ version: BUILD, sync });
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init, { once: true });
  else init();
})();
