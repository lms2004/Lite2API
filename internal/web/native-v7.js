/* Lite2API Native v7 — capability-aware route selection.
   Native selects remain the business source of truth; operators choose model,
   reasoning, and execution mode as three independent concepts. */
(() => {
  "use strict";

  const BUILD = "Native 7.2 · 2026.08.18";
  const picker = { activeSelect: null, query: "", group: "all", scheduled: false };
  const $ = id => document.getElementById(id);
  const all = (selector, root = document) => Array.from(root.querySelectorAll(selector));
  const later = fn => requestAnimationFrame(() => requestAnimationFrame(fn));

  function accounts() {
    return Array.isArray(state?.config?.accounts) ? state.config.accounts : [];
  }

  function effortLabel(value) {
    return ({
      auto: "自动", none: "关闭", minimal: "最小", low: "低", medium: "中",
      high: "高", xhigh: "极高", max: "最大", ultra: "Ultra"
    })[value] || value;
  }

  function modelProfile(model) {
    const raw = String(model || "").trim();
    const fast = raw.toLowerCase().endsWith("@fast");
    return { base: fast ? raw.slice(0, -5) : raw, fast };
  }

  function friendlyModelName(model) {
    const base = modelProfile(model).base;
    return ({
      sol: "GPT-5.6 Sol",
      terra: "GPT-5.6 Terra",
      luna: "GPT-5.6 Luna",
      "sol-max": "Sol Max"
    })[base.toLowerCase()] || base;
  }

  function routeDisplayName(model) {
    const profile = modelProfile(model);
    return profile.fast ? `${friendlyModelName(profile.base)} · Fast` : friendlyModelName(profile.base);
  }

  function modelGroup(model) {
    const text = modelProfile(model).base.toLowerCase();
    if (/^(sol|terra|luna|sol-max)$|gpt|codex|openai/.test(text)) return "openai";
    if (/claude|anthropic/.test(text)) return "claude";
    if (/gemini|google/.test(text)) return "gemini";
    if (/grok|xai|x\.ai/.test(text)) return "grok";
    if (/deepseek/.test(text)) return "deepseek";
    return "other";
  }

  function groupLabel(group) {
    return ({
      openai: "OpenAI / Codex", claude: "Claude", gemini: "Gemini",
      grok: "Grok", deepseek: "DeepSeek", other: "其他"
    })[group] || "其他";
  }

  function modelSelects() {
    return all("#routeRows .route-intent").map(intent => all("select", intent)[0]).filter(Boolean);
  }

  // Catalog entries are base models. Execution profiles such as @fast are
  // folded into model metadata so they do not double the picker length.
  function catalog() {
    const byModel = new Map();
    for (const account of accounts()) {
      for (const capability of Array.isArray(account.capabilities) ? account.capabilities : []) {
        const rawModel = String(capability.model || "").trim();
        if (!rawModel) continue;
        const profile = modelProfile(rawModel);
        const model = profile.base;
        let entry = byModel.get(model);
        if (!entry) {
          entry = {
            model,
            group: modelGroup(model),
            sources: new Set(),
            upstreams: new Set(),
            efforts: new Set(),
            tags: new Set(),
            fastAvailable: false
          };
          byModel.set(model, entry);
        }
        entry.sources.add(account.name || account.id || "上游");
        if (capability.upstream_model) entry.upstreams.add(capability.upstream_model);
        for (const effort of capability.reasoning_efforts || []) entry.efforts.add(effort);
        if (profile.fast) entry.fastAvailable = true;
        const observed = `${model} ${capability.upstream_model || ""}`.toLowerCase();
        if (/thinking|reasoner/.test(observed)) entry.tags.add("Thinking");
        if (/image|vision/.test(observed)) entry.tags.add("Vision");
      }
    }

    // Preserve current routes while discovery is temporarily stale.
    for (const select of modelSelects()) {
      const profile = modelProfile(select.value);
      const model = profile.base;
      if (!model) continue;
      let entry = byModel.get(model);
      if (!entry) {
        entry = {
          model,
          group: modelGroup(model),
          sources: new Set(),
          upstreams: new Set(),
          efforts: new Set(),
          tags: new Set(["当前"]),
          fastAvailable: profile.fast
        };
        byModel.set(model, entry);
      } else if (profile.fast) {
        entry.fastAvailable = true;
      }
    }

    const order = ["openai", "claude", "gemini", "grok", "deepseek", "other"];
    return Array.from(byModel.values()).sort((a, b) => {
      const groupDelta = order.indexOf(a.group) - order.indexOf(b.group);
      return groupDelta || friendlyModelName(a.model).localeCompare(friendlyModelName(b.model), "zh-CN", { numeric: true });
    });
  }

  function catalogEntry(model) {
    const base = modelProfile(model).base;
    return catalog().find(entry => entry.model === base);
  }

  function ensureDialog() {
    let dialog = $("v7ModelDialog");
    if (dialog) return dialog;
    dialog = document.createElement("dialog");
    dialog.id = "v7ModelDialog";
    dialog.className = "v7-model-dialog";
    dialog.setAttribute("aria-labelledby", "v7ModelDialogTitle");
    dialog.innerHTML = `
      <div class="v7-model-dialog-head">
        <div><strong id="v7ModelDialogTitle">选择模型</strong><span>来自当前上游实际发现或已配置的能力</span></div>
        <button type="button" class="icon-btn" data-v7-close aria-label="关闭">×</button>
      </div>
      <div class="v7-model-search-wrap"><input id="v7ModelSearch" type="search" autocomplete="off" placeholder="搜索模型、上游或真实模型 ID" aria-label="搜索模型"></div>
      <div id="v7ModelGroups" class="v7-model-groups" role="tablist" aria-label="模型分组"></div>
      <div id="v7ModelResults" class="v7-model-results"></div>
      <div class="v7-model-dialog-foot">模型目录由上游自动同步；推理强度和 Fast 模式在模型之外单独选择。</div>`;
    document.body.append(dialog);
    dialog.querySelector("[data-v7-close]").addEventListener("click", () => dialog.close());
    dialog.addEventListener("click", event => { if (event.target === dialog) dialog.close(); });
    $("v7ModelSearch").addEventListener("input", event => {
      picker.query = event.target.value || "";
      renderDialog();
    });
    return dialog;
  }

  function openDialog(select) {
    picker.activeSelect = select;
    picker.query = "";
    picker.group = "all";
    const dialog = ensureDialog();
    $("v7ModelSearch").value = "";
    renderDialog();
    if (!dialog.open) dialog.showModal();
    later(() => $("v7ModelSearch")?.focus());
  }

  function renderDialog() {
    const entries = catalog();
    const groups = ["all", ...Array.from(new Set(entries.map(entry => entry.group)))];
    const groupNode = $("v7ModelGroups");
    const resultNode = $("v7ModelResults");
    if (!groupNode || !resultNode) return;

    groupNode.replaceChildren(...groups.map(group => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "v7-model-group";
      button.setAttribute("role", "tab");
      button.setAttribute("aria-selected", String(picker.group === group));
      button.textContent = group === "all" ? `全部 ${entries.length}` : groupLabel(group);
      button.addEventListener("click", () => { picker.group = group; renderDialog(); });
      return button;
    }));

    const query = picker.query.trim().toLocaleLowerCase("zh-CN");
    const filtered = entries.filter(entry => {
      if (picker.group !== "all" && entry.group !== picker.group) return false;
      if (!query) return true;
      return [entry.model, friendlyModelName(entry.model), ...entry.sources, ...entry.upstreams, ...entry.tags, entry.fastAvailable ? "fast" : ""]
        .join(" ").toLocaleLowerCase("zh-CN").includes(query);
    });
    if (!filtered.length) {
      resultNode.innerHTML = `<div class="v7-model-empty">没有匹配模型</div>`;
      return;
    }

    const currentBase = modelProfile(picker.activeSelect?.value).base;
    const fragment = document.createDocumentFragment();
    let lastGroup = "";
    for (const entry of filtered) {
      if (picker.group === "all" && entry.group !== lastGroup) {
        const heading = document.createElement("div");
        heading.className = "v7-model-group-heading";
        heading.textContent = groupLabel(entry.group);
        fragment.append(heading);
        lastGroup = entry.group;
      }
      const button = document.createElement("button");
      button.type = "button";
      button.className = "v7-model-result";
      button.setAttribute("aria-current", String(currentBase === entry.model));
      button.innerHTML = `<span class="v7-model-result-main"><strong></strong><small></small></span><span class="v7-model-result-meta"></span>`;
      button.querySelector("strong").textContent = friendlyModelName(entry.model);
      const sources = Array.from(entry.sources);
      button.querySelector("small").textContent = sources.length
        ? `${sources.length} 个上游 · ${sources.slice(0, 2).join("、")}${sources.length > 2 ? "…" : ""}`
        : "当前配置";
      const meta = button.querySelector(".v7-model-result-meta");
      if (entry.fastAvailable) {
        const fast = document.createElement("span");
        fast.className = "v7-model-tag fast";
        fast.textContent = "Fast";
        meta.append(fast);
      }
      for (const tag of Array.from(entry.tags).slice(0, entry.fastAvailable ? 1 : 2)) {
        const chip = document.createElement("span");
        chip.className = "v7-model-tag";
        chip.textContent = tag;
        meta.append(chip);
      }
      const efforts = Array.from(entry.efforts).filter(value => value && value !== "auto");
      if (efforts.length) {
        const chip = document.createElement("span");
        chip.className = "v7-model-tag";
        chip.textContent = `${efforts.length} 档推理`;
        meta.append(chip);
      }
      button.addEventListener("click", () => chooseBaseModel(entry));
      fragment.append(button);
    }
    resultNode.replaceChildren(fragment);
  }

  function setBusinessModel(select, model) {
    if (!Array.from(select.options).some(option => option.value === model)) select.add(new Option(model, model));
    select.value = model;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function chooseBaseModel(entry) {
    const select = picker.activeSelect;
    if (!select) return;
    const wasFast = modelProfile(select.value).fast;
    const target = wasFast && entry.fastAvailable ? `${entry.model}@fast` : entry.model;
    setBusinessModel(select, target);
    $("v7ModelDialog")?.close();
    schedule();
  }

  function enhanceModelSelect(select) {
    if (select.dataset.v7Model !== "1") {
      select.dataset.v7Model = "1";
      select.classList.add("v7-native-select");
      const trigger = document.createElement("button");
      trigger.type = "button";
      trigger.className = "v7-model-trigger";
      trigger.setAttribute("aria-haspopup", "dialog");
      trigger.addEventListener("click", () => openDialog(select));
      select.after(trigger);
    }
    syncModelTrigger(select);
  }

  function syncModelTrigger(select) {
    const trigger = select.parentElement?.querySelector(".v7-model-trigger");
    if (!trigger) return;
    const entry = catalogEntry(select.value);
    const effortCount = entry ? Array.from(entry.efforts).filter(value => value && value !== "auto").length : 0;
    trigger.innerHTML = `<span class="v7-model-trigger-copy"><strong></strong><small></small></span><span class="v7-model-trigger-arrow">⌄</span>`;
    trigger.querySelector("strong").textContent = friendlyModelName(select.value || "选择模型");
    trigger.querySelector("small").textContent = `${entry?.sources.size || 0} 个兼容上游${effortCount ? ` · ${effortCount} 档推理` : ""}${entry?.fastAvailable ? " · 支持 Fast" : ""}`;
  }

  function enhanceEffortSelect(select) {
    if (select.dataset.v7Effort !== "1") {
      select.dataset.v7Effort = "1";
      select.classList.add("v7-effort-select");
      const control = document.createElement("div");
      control.className = "v7-effort-control";
      control.setAttribute("role", "radiogroup");
      control.setAttribute("aria-label", "推理强度");
      select.after(control);
    }
    syncEffortControl(select);
  }

  function syncEffortControl(select) {
    const control = select.parentElement?.querySelector(".v7-effort-control");
    if (!control) return;
    control.replaceChildren(...Array.from(select.options).filter(option => option.value).map(option => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "v7-effort-option";
      button.setAttribute("role", "radio");
      button.setAttribute("aria-checked", String(select.value === option.value));
      button.textContent = effortLabel(option.value);
      button.addEventListener("click", () => {
        select.value = option.value;
        select.dispatchEvent(new Event("change", { bubbles: true }));
        schedule();
      });
      return button;
    }));
  }

  function enhanceModeControl(modelSelect, intent) {
    let field = intent.querySelector(".v7-mode-field");
    if (!field) {
      field = document.createElement("div");
      field.className = "v7-mode-field";
      field.innerHTML = `<span class="v7-field-label">运行模式</span><div class="v7-mode-control" role="radiogroup" aria-label="运行模式"></div>`;
      const note = intent.querySelector(".route-intent-note");
      intent.insertBefore(field, note || null);
    }
    const control = field.querySelector(".v7-mode-control");
    const profile = modelProfile(modelSelect.value);
    const entry = catalogEntry(modelSelect.value);
    const modes = [{ id: "standard", label: "标准", disabled: false }];
    if (entry?.fastAvailable || profile.fast) modes.push({ id: "fast", label: "Fast", disabled: !entry?.fastAvailable && !profile.fast });
    control.replaceChildren(...modes.map(mode => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "v7-mode-option";
      button.setAttribute("role", "radio");
      const active = mode.id === (profile.fast ? "fast" : "standard");
      button.setAttribute("aria-checked", String(active));
      button.disabled = mode.disabled;
      button.textContent = mode.label;
      button.addEventListener("click", () => {
        const base = modelProfile(modelSelect.value).base;
        setBusinessModel(modelSelect, mode.id === "fast" ? `${base}@fast` : base);
        schedule();
      });
      return button;
    }));
  }

  function syncRouteMasterLabels() {
    const cards = all("#routeRows > .route-card");
    const byAlias = new Map(cards.map(card => [card.querySelector(".route-alias")?.value?.trim(), card]));
    all("#v5RouteList .route-master-item").forEach(button => {
      const card = byAlias.get(button.dataset.route);
      const modelSelect = card?.querySelector(".route-intent select");
      const subtitle = button.querySelector("small");
      if (modelSelect && subtitle) subtitle.textContent = routeDisplayName(modelSelect.value);
    });
  }

  function enhanceRoutes() {
    all("#routeRows .route-intent").forEach(intent => {
      const selects = all("select", intent);
      if (selects[0]) {
        enhanceModelSelect(selects[0]);
        enhanceModeControl(selects[0], intent);
      }
      if (selects[1]) enhanceEffortSelect(selects[1]);
    });
    syncRouteMasterLabels();
  }

  function sync() {
    picker.scheduled = false;
    document.documentElement.dataset.ui = "native-v7";
    const build = $("uiBuild");
    if (build) build.textContent = "UI build 2026.08.18-v7";
    enhanceRoutes();
    if ($("v7ModelDialog")?.open) renderDialog();
  }

  function schedule() {
    if (picker.scheduled) return;
    picker.scheduled = true;
    later(sync);
  }

  function wrap(name) {
    const original = window[name];
    if (typeof original !== "function" || original.__nativeV7Wrapped) return;
    const wrapped = function (...args) {
      const result = original.apply(this, args);
      if (result && typeof result.finally === "function") result.finally(schedule);
      else schedule();
      return result;
    };
    Object.defineProperty(wrapped, "__nativeV7Wrapped", { value: true });
    window[name] = wrapped;
  }

  function init() {
    ensureDialog();
    ["render", "renderRoutes", "showView", "saveRoutes"].forEach(wrap);
    const rows = $("routeRows");
    if (rows) new MutationObserver(schedule).observe(rows, { childList: true, subtree: true, attributes: true, attributeFilter: ["value", "hidden"] });
    document.addEventListener("change", event => { if (event.target.closest?.("#routeRows")) schedule(); });
    sync();
  }

  window.Lite2APINativeV7 = Object.freeze({ version: BUILD, openModelPicker: openDialog, sync });
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init, { once: true });
  else init();
})();
