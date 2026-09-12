(() => {
  'use strict';

  const API = location.pathname.replace(/\/?$/, '/') + 'api';

  const $ = (id) => document.getElementById(id);

  const one = (selector, root = document) => root.querySelector(selector);

  const all = (selector, root = document) => Array.from(root.querySelectorAll(selector));

  const now = () => Date.now();

  const Core = globalThis.Lite2APIAppCore;

  if (!Core) throw new Error('Lite2API application core was not loaded');

  const state = {
    session: {
      csrf: '',
    },
    navigation: {
      activeView: 'usage',
    },
    feedback: {
      noticeTimer: 0,
      toastTimer: 0,
    },
    resources: {
      data: null,
      trend: null,
      oauth: [],
      oauthError: '',
      oauthRouting: null,
      adapters: [],
      keys: [],
    },
    usage: {
      range: '24h',
      metric: 'requests',
      accountFilter: '',
      quality: new Map(),
      qualityRunning: new Set(),
      quotaHistory: [],
      chartPoints: [],
      chartIndex: -1,
      requestPage: 1,
    },
    accounts: {
      accountTab: 'auth',
    },
    onboarding: {
      oauthSession: null,
      oauthGeneration: 0,
      selectedProvider: 'codex',
      onboardingStep: 'provider',
      lastOnboardingResult: null,
      manualTemplate: 'openai',
      manualEditing: '',
      manualOriginalFingerprint: '',
      manualTestFingerprint: '',
      manualTestGeneration: 0,
      manualDiscoveredModels: [],
      importFiles: [],
      importData: null,
      importPreview: null,
    },
    clients: {
      client: 'claude',
      configMode: 'shell',
      keyPreset: 'personal',
    },
    chat: {
      channelChatAccount: '',
      channelChatMessages: [],
      channelChatPending: false,
      channelChatController: null,
      channelChatGeneration: 0,
    },
  };

  const UI = globalThis.Lite2APIUI.create();
  const { setHTML } = UI;
  const Runtime = globalThis.Lite2APIRuntime;
  const indexData = globalThis.Lite2APIMetrics.createIndex();
  const apiClient = Runtime.createRequestClient({
    base: API,
    csrf: () => state.session.csrf,
    unauthorized: () => {
      state.session.csrf = '';
      sessionStorage.removeItem('lite2api_csrf');
      $('loginOverlay').hidden = false;
      $('connectionDot').className = 'bad';
      $('connectionText').textContent = '认证已过期';
      refreshLoop.pause();
    },
  });
  const resourceCache = new Map();
  let resourceVersion = 0;
  const refreshLoop = Runtime.createRefreshLoop({
    key: () => state.navigation.activeView + ':' + state.usage.range,
    paused: () => document.hidden || !state.session.csrf,
    interval: () => ({ usage: 10000, accounts: 15000, routes: 30000 })[state.navigation.activeView] || 60000,
    busy: (busy) => {
      $('refreshButton').setAttribute('aria-busy', String(busy));
      $('refreshButton').disabled = busy;
    },
    load: loadSnapshot,
    commit: (_, { silent }) => {
      $('lastUpdated').textContent =
        '更新于 ' + new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
      $('connectionDot').className = 'ready';
      $('connectionText').textContent = '已连接';
      if (!silent) toast('数据已更新');
    },
    failed: (error) => {
      showNotice(error.message, true);
      $('connectionDot').className = 'bad';
      $('connectionText').textContent = error.status === 401 ? '认证已过期' : '连接异常';
    },
  });

  async function loadSnapshot({ signal, active, force }) {
    const version = resourceVersion;
    const current = () => active() && version === resourceVersion;
    const view = state.navigation.activeView;
    const range = state.usage.range;
    const update = () => {
      if (current())
        UI.scheduleRender(() => {
          if (current()) renderActiveView();
        });
    };
    const optional = async (path, ttl, apply, fallback) => {
      try {
        const cached = resourceCache.get(path);
        const result =
          !force && cached && now() - cached.time < ttl
            ? cached.value
            : await request(path, { signal, timeout: 5000 });
        if (!current()) return;
        resourceCache.set(path, { time: cached?.value === result ? cached.time : now(), value: result });
        apply(result);
        update();
      } catch (error) {
        if (current() && error.name !== 'AbortError') {
          fallback(error);
          update();
        }
      }
    };
    const stateTask = request('/state', { signal });
    const tasks = [];
    if (view === 'usage')
      tasks.push(
        optional(
          '/trends?range=' + range,
          5000,
          (result) => {
            state.resources.trend = result;
          },
          () => {
            state.resources.trend = null;
          },
        ),
      );
    if (['usage', 'accounts', 'routes'].includes(view))
      tasks.push(
        optional(
          '/oauth/accounts',
          10000,
          (result) => {
            state.resources.oauth = result.data || [];
            state.resources.oauthError = result.warning || '';
            usage.recordQuotaHistory();
          },
          (error) => {
            state.resources.oauthError = error.message;
          },
        ),
      );
    if (view === 'accounts')
      tasks.push(
        optional(
          '/oauth/routing',
          30000,
          (result) => {
            state.resources.oauthRouting = result;
          },
          () => {
            state.resources.oauthRouting = null;
          },
        ),
      );
    if (view === 'clients' || view === 'diagnostics') {
      const clientsView = view === 'clients';
      tasks.push(
        optional(
          clientsView ? '/client-keys' : '/adapters',
          clientsView ? 0 : 10000,
          (result) => {
            state.resources[clientsView ? 'keys' : 'adapters'] = result.data || [];
          },
          (error) => showNotice(error.message, true),
        ),
      );
    }
    const snapshot = await stateTask;
    if (!current()) return;
    routes.receiveSnapshot(snapshot.config?.routes || {});
    state.resources.data = snapshot;
    update();
    await Promise.all(tasks);
  }

  const VIEW_COPY = {
    usage: ['使用概览', '运行状态与调用趋势'],
    accounts: ['账号', '订阅账号与 API 连接'],
    routes: ['模型路由', '对外模型名到真实上游'],
    clients: ['客户端', 'Key 与接入配置'],
    diagnostics: ['诊断', '适配器与低频系统能力'],
  };

  const RANGE_MS = { '24h': 86400000, '3d': 3 * 86400000, '7d': 7 * 86400000 };

  const RANGE_LABEL = { '24h': '24 小时', '3d': '3 天', '7d': '7 天' };

  function showNotice(message, bad = false) {
    clearTimeout(state.feedback.noticeTimer);
    const node = $('notice');
    node.textContent = message;
    node.className = 'notice show' + (bad ? ' bad' : '');
    node.setAttribute('role', bad ? 'alert' : 'status');
    state.feedback.noticeTimer = setTimeout(() => (node.className = 'notice'), 5000);
  }

  function toast(message, bad = false) {
    clearTimeout(state.feedback.toastTimer);
    const node = $('toast');
    node.textContent = message;
    node.className = 'toast show' + (bad ? ' bad' : '');
    node.setAttribute('role', bad ? 'alert' : 'status');
    state.feedback.toastTimer = setTimeout(() => (node.className = 'toast'), 3200);
  }

  function request(path, options = {}) {
    const mutation = options.method && !['GET', 'HEAD'].includes(options.method.toUpperCase());
    if (!mutation) return apiClient(path, options);
    resourceVersion++;
    resourceCache.clear();
    return apiClient(path, options).finally(() => {
      resourceVersion++;
      resourceCache.clear();
    });
  }

  async function establishSession(token = '') {
    try {
      let response = await fetch(API + '/session', { credentials: 'same-origin' });
      let data = await response.json().catch(() => ({}));
      if (!response.ok) {
        response = await fetch(API + '/login', {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ token }),
        });
        data = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(data.error?.message || '管理认证失败');
      }
      state.session.csrf = data.csrf || sessionStorage.getItem('lite2api_csrf') || '';
      if (state.session.csrf) sessionStorage.setItem('lite2api_csrf', state.session.csrf);
      $('loginOverlay').hidden = true;
      $('connectionDot').className = 'ready';
      $('connectionText').textContent = '已连接';
      $('adminToken').value = '';
      await refreshAll(true);
    } catch (error) {
      $('connectionDot').className = 'bad';
      $('connectionText').textContent = '未认证';
      $('loginOverlay').hidden = false;
      if (token) {
        $('loginError').hidden = false;
        $('loginError').textContent = error.message;
      }
    }
  }

  function showView(name, force = false) {
    if (!VIEW_COPY[name]) return false;
    const changed = state.navigation.activeView !== name;
    state.navigation.activeView = name;
    UI.closeMenus();
    all('.view').forEach((view) => view.classList.toggle('active', view.id === 'view-' + name));
    all('.sidebar [data-view]').forEach((button) => {
      const active = button.dataset.view === name;
      button.classList.toggle('active', active);
      if (active) button.setAttribute('aria-current', 'page');
      else button.removeAttribute('aria-current');
    });
    document.title = VIEW_COPY[name][0] + ' · Lite2API';
    one('#view-' + name + ' .page-heading').append($('workspaceSync'));
    if (location.hash !== '#' + name) history.pushState(null, '', '#' + name);
    if (state.resources.data) renderActiveView();
    if (changed || force) {
      if (state.session.csrf) refreshAll(true);
      window.scrollTo({ top: 0, behavior: 'instant' });
    }
    return true;
  }

  function refreshAll(silent = false, force = false) {
    return refreshLoop.refresh({ silent, force });
  }

  function renderActiveView() {
    if (!state.resources.data || document.hidden) return;
    if (state.navigation.activeView === 'usage') usage.renderUsage();
    if (state.navigation.activeView === 'accounts') accounts.renderAccounts();
    if (state.navigation.activeView === 'routes') routes.renderRoutes();
    if (state.navigation.activeView === 'clients') clients.renderClients();
    if (state.navigation.activeView === 'diagnostics') clients.renderAdapters();
  }

  function openDialog(id) {
    UI.openDialog(id);
  }

  function closeDialog(id) {
    UI.closeDialog(id);
  }

  function bindEvents() {
    UI.bind();
    for (const feature of [usage, accounts, onboarding, chat, routes, clients]) feature.bind();
    $('loginForm').addEventListener('submit', (event) => {
      event.preventDefault();
      $('loginError').hidden = true;
      establishSession($('adminToken').value);
    });
    all('[data-view]').forEach((button) =>
      button.addEventListener('click', () => showView(button.dataset.view)),
    );
    $('refreshButton').addEventListener('click', () => refreshAll(false, true));
    document.addEventListener('visibilitychange', () => {
      if (document.hidden) refreshLoop.pause();
      else refreshAll(true);
    });
    window.addEventListener('pagehide', () => {
      refreshLoop.pause();
      chat.cancelChannelChat();
    });
    window.addEventListener('pageshow', (event) => {
      if (event.persisted) refreshAll(true);
    });
    window.addEventListener('hashchange', () => showView(location.hash.slice(1) || 'usage'));
    window.addEventListener('beforeunload', (event) => {
      if (!routes.hasUnsavedChanges()) return;
      event.preventDefault();
      event.returnValue = '';
    });
  }

  async function init() {
    usage.loadPersistedQuality();
    bindEvents();
    let initial = location.hash.slice(1);
    if (!VIEW_COPY[initial]) initial = 'usage';
    showView(initial);
    await establishSession();
  }
  /* Explicit feature composition; no global render overrides. */
  const shared = globalThis.Lite2APIShared({ Core, now, indexData, resourcesState: state.resources });
  const usage = globalThis.Lite2APIUsage({
    all,
    Runtime,
    refreshAll,
    openChannelChat: (...args) => chat.openChannelChat(...args),
    now,
    RANGE_MS,
    usageState: state.usage,
    resourcesState: state.resources,
    navigationState: state.navigation,
    finiteNumber: shared.finiteNumber,
    percentile: shared.percentile,
    requestOK: shared.requestOK,
    quotaWindows: shared.quotaWindows,
    quotaPercentage: shared.quotaPercentage,
    $,
    relativeTime: shared.relativeTime,
    number: shared.number,
    RANGE_LABEL,
    connectionStats: shared.connectionStats,
    setHTML,
    escapeHTML: shared.escapeHTML,
    quotaTone: shared.quotaTone,
    providerKey: shared.providerKey,
    accountStatus: shared.accountStatus,
    quotaWindowHTML: shared.quotaWindowHTML,
    providerIconHTML: shared.providerIconHTML,
    PROVIDER_LABEL: shared.PROVIDER_LABEL,
    accountFingerprint: shared.accountFingerprint,
    Core,
    connectionProviderKey: shared.connectionProviderKey,
    configuredAccount: shared.configuredAccount,
    accountTestModel: shared.accountTestModel,
    toast,
    request,
    sleep: shared.sleep,
    formatTime: shared.formatTime,
    modelLabelHTML: shared.modelLabelHTML,
    modelIconHTML: shared.modelIconHTML,
  });
  const chat = globalThis.Lite2APIChat({
    resourcesState: state.resources,
    chatState: state.chat,
    Core,
    $,
    setHTML,
    number: shared.number,
    escapeHTML: shared.escapeHTML,
    configuredAccount: shared.configuredAccount,
    accountTestModel: shared.accountTestModel,
    toast,
    openDialog,
    request,
  });
  const accounts = globalThis.Lite2APIAccounts({
    UI,
    accountRouteUsage: shared.accountRouteUsage,
    all,
    Runtime,
    startOAuth: (...args) => onboarding.startOAuth(...args),
    openManualAccount: (...args) => onboarding.openManualAccount(...args),
    openChannelChat: (...args) => chat.openChannelChat(...args),
    openRouteCreate: (...args) => routes.openRouteCreate(...args),
    testExistingConnection: (...args) => onboarding.testExistingConnection(...args),
    configuredAccount: shared.configuredAccount,
    deleteConnection: (...args) => onboarding.deleteConnection(...args),
    $,
    resourcesState: state.resources,
    accountsState: state.accounts,
    Core,
    number: shared.number,
    accountStatus: shared.accountStatus,
    setHTML,
    quotaWindows: shared.quotaWindows,
    quotaPercentage: shared.quotaPercentage,
    providerKey: shared.providerKey,
    escapeHTML: shared.escapeHTML,
    providerIconHTML: shared.providerIconHTML,
    PROVIDER_LABEL: shared.PROVIDER_LABEL,
    quotaWindowHTML: shared.quotaWindowHTML,
    relativeTime: shared.relativeTime,
    connectionStats: shared.connectionStats,
    connectionProviderKey: shared.connectionProviderKey,
    modelLabelHTML: shared.modelLabelHTML,
    request,
    toast,
    refreshAll,
    one,
  });
  const onboarding = globalThis.Lite2APIOnboarding({
    renderConnections: (...args) => accounts.renderConnections(...args),
    UI,
    onboardingState: state.onboarding,
    resourcesState: state.resources,
    usageState: state.usage,
    hasRouteChanges: () => routes.hasUnsavedChanges(),
    $,
    all,
    setHTML,
    providerIconHTML: shared.providerIconHTML,
    escapeHTML: shared.escapeHTML,
    openDialog,
    closeDialog,
    toast,
    request,
    refreshAll,
    findOAuthPoolConnection: shared.findOAuthPoolConnection,
    one,
    configuredAccount: shared.configuredAccount,
    accountFingerprint: shared.accountFingerprint,
    Core,
    number: shared.number,
    saveQuality: (...args) => usage.saveQuality(...args),
    showView,
    openRouteCreate: (...args) => routes.openRouteCreate(...args),
    setAccountTab: (...args) => accounts.setAccountTab(...args),
    PROVIDER_LABEL: shared.PROVIDER_LABEL,
    providerKey: shared.providerKey,
    modelLabelHTML: shared.modelLabelHTML,
    sleep: shared.sleep,
  });
  const routes = globalThis.Lite2APIRoutes({
    UI,
    Runtime,
    resourcesState: state.resources,
    Core,
    configuredAccounts: shared.configuredAccounts,
    $,
    setHTML,
    configuredAccount: shared.configuredAccount,
    escapeHTML: shared.escapeHTML,
    modelIconHTML: shared.modelIconHTML,
    one,
    toast,
    accountStatus: shared.accountStatus,
    modelLabelHTML: shared.modelLabelHTML,
    connectionProviderKey: shared.connectionProviderKey,
    providerIconHTML: shared.providerIconHTML,
    openDialog,
    closeDialog,
    showView,
    request,
    refreshAll,
  });
  const clients = globalThis.Lite2APIClients({
    UI,
    openDialog,
    request,
    resourcesState: state.resources,
    clientsState: state.clients,
    toast,
    $,
    setHTML,
    escapeHTML: shared.escapeHTML,
    formatTime: shared.formatTime,
    number: shared.number,
    all,
    now,
    closeDialog,
  });
  init();
})();
