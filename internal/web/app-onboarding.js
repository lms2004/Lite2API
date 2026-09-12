/* Onboarding controller. Dependencies are supplied by the composition root. */
globalThis.Lite2APIOnboarding = function createOnboarding(context) {
  'use strict';
  const {
    renderConnections,
    UI,
    onboardingState,
    resourcesState,
    usageState,
    hasRouteChanges,
    $,
    all,
    setHTML,
    providerIconHTML,
    escapeHTML,
    openDialog,
    closeDialog,
    toast,
    request,
    refreshAll,
    findOAuthPoolConnection,
    one,
    configuredAccount,
    accountFingerprint,
    Core,
    number,
    saveQuality,
    showView,
    openRouteCreate,
    setAccountTab,
    PROVIDER_LABEL,
    providerKey,
    modelLabelHTML,
    sleep,
  } = context;
  const PROVIDERS = {
    codex: {
      title: 'OpenAI / Codex',
      subtitle: 'ChatGPT OAuth 或 OpenAI API Key',
      summary: '订阅账号走 OAuth 账号池；官方或兼容接口走 API Key。两种凭据不会混在同一个连接中。',
      methods: [
        ['oauth', 'ChatGPT OAuth', '浏览器授权后加入 Codex 账号池', 'codex'],
        ['manual', 'OpenAI API Key', '官方 API 或任意 OpenAI 兼容服务', 'openai'],
        ['manual', 'OAuth 聚合连接', '已有 CLIProxyAPI 时添加稳定连接', 'cliproxy'],
        ['import', '批量导入', 'Lite2API / Sub2API 账号文件', ''],
      ],
      checks: [
        ['凭据隔离', 'OAuth 令牌由 CLIProxyAPI 保存'],
        ['额度同步', '支持 5 小时与 7 天窗口'],
        ['保存前测试', 'API Key 可先发现模型'],
      ],
    },
    anthropic: {
      title: 'Claude / Anthropic',
      subtitle: 'OAuth、Setup Token 或 API Key',
      summary: 'Claude Code 订阅优先使用 OAuth 或 Setup Token；Anthropic Console 使用原生 Messages API Key。',
      methods: [
        ['oauth', 'Claude OAuth', '浏览器授权后加入 Claude 账号池', 'anthropic'],
        ['setup', 'Claude Setup Token', '预检查后导入隔离认证池', 'anthropic'],
        ['manual', 'Anthropic API Key', '原生 Messages 协议', 'anthropic'],
        ['import', '批量导入', 'Sub2API Claude 账号或 Lite2API JSON', ''],
      ],
      checks: [
        ['协议', 'API Key 使用 Anthropic Messages'],
        ['额度', '订阅账号显示 5h / 周窗口'],
        ['直测', '保存前验证 /models 或连接'],
      ],
    },
    gemini: {
      title: 'Google Gemini',
      subtitle: 'Google OAuth、API Key 或 Web',
      summary: 'Gemini CLI 使用 Google OAuth；官方开发者接口使用 API Key；Web Cookie 仅进入隔离适配器。',
      methods: [
        ['oauth', 'Google OAuth', '加入 Gemini CLI 账号池', 'gemini'],
        ['manual', 'Gemini API Key', 'Google 官方 OpenAI 兼容端点', 'gemini'],
        ['web', 'Gemini Web Cookie', '浏览器内整理后交给隔离适配器', 'gemini'],
        ['import', '批量导入', '账号文件与代理引用预检查', ''],
      ],
      checks: [
        ['额度', 'OAuth 账号显示每日或模型窗口'],
        ['Web 安全', 'Cookie 不写入 Lite2API 核心'],
        ['模型发现', 'API Key 保存前直测'],
      ],
    },
    antigravity: {
      title: 'Antigravity',
      subtitle: 'Google OAuth 账号池',
      summary: 'Antigravity 凭据由隔离 OAuth 适配器保存；模型冷却和恢复时间在额度页面展示。',
      methods: [
        ['oauth', 'Antigravity OAuth', 'Google 授权后加入账号池', 'antigravity'],
        ['import', '导入账号文件', '预检现有凭据包', ''],
      ],
      checks: [
        ['隔离', '凭据不进入核心配置'],
        ['冷却', '显示模型冷却与下次重试'],
        ['路由', '通过 OAuth 聚合连接使用'],
      ],
    },
    kimi: {
      title: 'Kimi / Moonshot',
      subtitle: '设备授权或 API Key',
      summary: 'Kimi 订阅可使用设备授权；Moonshot 官方接口使用 API Key。',
      methods: [
        ['oauth', 'Kimi 设备授权', '页面自动轮询授权结果', 'kimi'],
        ['manual', 'Moonshot API Key', '官方 OpenAI 兼容接口', 'kimi'],
        ['import', '批量导入', '账号或连接 JSON', ''],
      ],
      checks: [
        ['设备授权', '无需粘贴回调 URL'],
        ['API Key', '保存前发现模型'],
        ['额度', '上游返回时显示真实窗口'],
      ],
    },
    grok: {
      title: 'Grok / xAI',
      subtitle: 'xAI API Key、Grok2API 或 SSO',
      summary: '官方 xAI 接口使用 API Key；Web、Build 或多账号凭据由 Grok2API 隔离管理。',
      methods: [
        ['manual', 'xAI API Key', '官方 OpenAI 兼容 API', 'xai'],
        ['manual', 'Grok2API 连接', '连接本机多账号适配器', 'grok-adapter'],
        ['web', 'Grok SSO / Cookie', '本地整理并交给 Grok2API', 'grok'],
        ['import', '批量导入', '支持可识别的账号数据', ''],
      ],
      checks: [
        ['隔离', 'SSO 只交给 Grok2API'],
        ['质量', '可直接执行三轮测试'],
        ['错误', '保留 401 / 429 / 5xx 类型'],
      ],
    },
    deepseek: {
      title: 'DeepSeek',
      subtitle: '官方 API Key',
      summary: '使用官方 OpenAI 兼容接口；Chat 与 Reasoner 可以在保存前发现并验证。',
      methods: [
        ['manual', 'DeepSeek API Key', 'Chat / Reasoner 官方接口', 'deepseek'],
        ['import', '批量导入', '连接 JSON 预检查', ''],
      ],
      checks: [
        ['认证', 'Bearer API Key'],
        ['模型', 'deepseek-chat / reasoner'],
        ['直测', '不经过 fallback'],
      ],
    },
    atom: {
      title: 'AtomCode',
      subtitle: '本机订阅适配器',
      summary: 'Lite2API 连接本机 AtomCode2API；登录态与协议逻辑由隔离进程管理。',
      methods: [
        ['manual', 'AtomCode2API 连接', '连接 127.0.0.1 本机服务', 'atom'],
        ['import', '导入连接配置', '预检查 Base URL 与环境变量', ''],
      ],
      checks: [
        ['本机隔离', '核心只保存回环连接'],
        ['模型发现', '直测适配器 /models'],
        ['额度', '仅展示适配器真实返回'],
      ],
    },
    custom: {
      title: '自定义兼容服务',
      subtitle: 'OpenAI 或 Anthropic 兼容',
      summary: '填写任意受信任 Base URL、认证方式、代理、模型映射与请求头。',
      methods: [
        ['manual', '自定义连接', 'OpenAI / Anthropic 兼容服务', 'custom'],
        ['import', '批量导入', 'Lite2API 标准账号 JSON', ''],
      ],
      checks: [
        ['URL', '校验 HTTPS 或允许的本机地址'],
        ['凭据', '支持 API Key 与环境变量'],
        ['直测', '保存前模型发现'],
      ],
    },
  };

  function setOnboardingStep(step) {
    onboardingState.onboardingStep = step;
    const order = ['provider', 'method', 'action'],
      index = order.indexOf(step);
    $('providerStep').hidden = step !== 'provider';
    $('methodStep').hidden = step !== 'method';
    $('actionStep').hidden = step !== 'action';
    $('onboardingBack').hidden = step === 'provider';
    all('[data-onboarding-progress]').forEach((node, nodeIndex) => {
      const active = nodeIndex === index;
      node.classList.toggle('active', active);
      node.classList.toggle('complete', nodeIndex < index);
      if (active) node.setAttribute('aria-current', 'step');
      else node.removeAttribute('aria-current');
    });
    $('onboardingTitle').textContent =
      step === 'provider'
        ? '其他添加方式'
        : step === 'method'
          ? PROVIDERS[onboardingState.selectedProvider]?.title || '选择接入方式'
          : '完成接入';
  }

  function renderProviderWizard() {
    const keys = Object.keys(PROVIDERS);
    setHTML(
      $('providerList'),
      keys
        .map((key) => {
          const provider = PROVIDERS[key];
          return `<button type="button" class="${onboardingState.selectedProvider === key ? 'active' : ''}" data-provider="${key}">${providerIconHTML(key)}<span><strong>${escapeHTML(provider.title)}</strong><small>${escapeHTML(provider.subtitle)}</small></span></button>`;
        })
        .join(''),
    );
    const provider = PROVIDERS[onboardingState.selectedProvider] || PROVIDERS.codex;
    $('providerName').textContent = provider.title;
    $('providerEyebrow').textContent = provider.subtitle;
    $('providerSummary').textContent = provider.summary;
    setHTML(
      $('providerMethods'),
      provider.methods
        .map(
          ([kind, title, description, value]) =>
            `<button type="button" class="method-card" data-method="${kind}" data-value="${escapeHTML(value)}"><span class="method-icon">${kind === 'oauth' ? '↗' : kind === 'manual' ? '⌘' : kind === 'import' ? '⇩' : kind === 'setup' ? '#' : '◎'}</span><span><strong>${escapeHTML(title)}</strong><small>${escapeHTML(description)}</small></span><span>›</span></button>`,
        )
        .join(''),
    );
    setHTML(
      $('providerChecklist'),
      provider.checks
        .map(
          ([title, detail]) =>
            `<div class="check-item"><i>✓</i><span><strong>${escapeHTML(title)}</strong><span>${escapeHTML(detail)}</span></span></div>`,
        )
        .join(''),
    );
  }

  function selectOnboardingProvider(provider) {
    onboardingState.selectedProvider = PROVIDERS[provider] ? provider : 'custom';
    renderProviderWizard();
    setOnboardingStep('method');
  }

  const PRESETS = {
    openai: {
      id: 'openai-main',
      name: 'OpenAI API',
      type: 'openai',
      base_url: 'https://api.openai.com/v1',
      api_key_env: 'OPENAI_API_KEY',
      models: ['gpt-5.4-mini', 'gpt-5.4'],
      concurrency: 4,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: 'generic-openai',
      instance_id: 'official',
      operations: ['openai.chat', 'openai.responses', 'openai.embeddings', 'openai.images'],
    },
    anthropic: {
      id: 'anthropic-main',
      name: 'Anthropic API',
      type: 'anthropic',
      base_url: 'https://api.anthropic.com/v1',
      api_key_env: 'ANTHROPIC_API_KEY',
      models: ['claude-sonnet-4-5', 'claude-haiku-4-5'],
      concurrency: 4,
      auth_header: 'x-api-key',
      auth_scheme: '',
      adapter_id: 'generic-anthropic',
      instance_id: 'official',
      operations: ['anthropic.messages'],
    },
    gemini: {
      id: 'gemini-api-main',
      name: 'Gemini API',
      type: 'openai',
      base_url: 'https://generativelanguage.googleapis.com/v1beta/openai',
      api_key_env: 'GEMINI_API_KEY',
      models: ['gemini-2.5-flash', 'gemini-2.5-pro'],
      concurrency: 4,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: 'gemini-openai',
      instance_id: 'official',
      operations: ['openai.chat', 'openai.embeddings'],
    },
    deepseek: {
      id: 'deepseek-main',
      name: 'DeepSeek API',
      type: 'openai',
      base_url: 'https://api.deepseek.com/v1',
      api_key_env: 'DEEPSEEK_API_KEY',
      models: ['deepseek-chat', 'deepseek-reasoner'],
      concurrency: 4,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: 'generic-openai',
      instance_id: 'deepseek',
      operations: ['openai.chat'],
    },
    xai: {
      id: 'xai-main',
      name: 'xAI API',
      type: 'openai',
      base_url: 'https://api.x.ai/v1',
      api_key_env: 'XAI_API_KEY',
      models: ['grok-4', 'grok-4-fast'],
      concurrency: 4,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: 'generic-openai',
      instance_id: 'xai',
      operations: ['openai.chat'],
    },
    kimi: {
      id: 'kimi-main',
      name: 'Moonshot API',
      type: 'openai',
      base_url: 'https://api.moonshot.cn/v1',
      api_key_env: 'MOONSHOT_API_KEY',
      models: ['kimi-k2.5', 'moonshot-v1-auto'],
      concurrency: 4,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: 'generic-openai',
      instance_id: 'moonshot',
      operations: ['openai.chat'],
    },
    cliproxy: {
      id: 'cliproxy-oauth',
      name: 'CLIProxy OAuth Pool',
      type: 'openai',
      base_url: 'http://127.0.0.1:45682/v1',
      api_key_env: 'CLIPROXYAPI_KEY',
      models: ['gpt-5.4-mini', 'claude-haiku-4-5-20251001', 'gemini-2.5-flash'],
      concurrency: 8,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: 'cli-proxy-api',
      instance_id: 'local',
      operations: ['openai.chat', 'openai.responses', 'anthropic.messages'],
    },
    'grok-adapter': {
      id: 'grok-local',
      name: 'Grok2API',
      type: 'openai',
      base_url: 'http://127.0.0.1:45680/v1',
      api_key_env: 'GROK2API_KEY',
      models: ['grok-4.5', 'grok-chat-fast'],
      concurrency: 4,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: 'grok2api',
      instance_id: 'local',
      operations: ['openai.chat'],
    },
    'gemini-web': {
      id: 'gemini-local',
      name: 'Gemini Web',
      type: 'openai',
      base_url: 'http://127.0.0.1:45681/v1',
      api_key_env: 'GEMINI_WEB2API_KEY',
      models: ['gemini-auto', 'gemini-flash-lite'],
      concurrency: 2,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: 'gemini-web2api',
      instance_id: 'local',
      operations: ['openai.chat'],
    },
    atom: {
      id: 'atom-main',
      name: 'AtomCode',
      type: 'openai',
      base_url: 'http://127.0.0.1:45678/v1',
      api_key_env: 'ATOMCODE2API_KEY',
      models: ['deepseek-v4-flash', 'Qwen/Qwen3-VL-8B-Instruct'],
      concurrency: 2,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: 'atomcode2api',
      instance_id: 'local',
      operations: ['openai.chat'],
    },
    custom: {
      id: 'custom-main',
      name: '自定义兼容服务',
      type: 'openai',
      base_url: '',
      api_key_env: 'CUSTOM_API_KEY',
      models: [],
      concurrency: 2,
      auth_header: 'authorization',
      auth_scheme: 'Bearer',
      adapter_id: '',
      instance_id: 'custom',
      operations: ['openai.chat'],
    },
  };

  function nextID(base) {
    const used = new Set((resourcesState.data?.config?.accounts || []).map((account) => account.id));
    if (!used.has(base)) return base;
    let index = 2;
    while (used.has(`${base}-${index}`)) index++;
    return `${base}-${index}`;
  }

  function openAddAccount() {
    onboardingState.selectedProvider = 'codex';
    renderProviderWizard();
    setOnboardingStep('provider');
    openDialog('addAccountDialog');
  }

  function providerMethod(kind, value) {
    if (kind === 'oauth') {
      closeDialog('addAccountDialog');
      startOAuth(value);
      return;
    }
    if (kind === 'manual') {
      closeDialog('addAccountDialog');
      openManualAccount(value);
      return;
    }
    if (kind === 'import') {
      closeDialog('addAccountDialog');
      openImportDialog();
      return;
    }
    if (kind === 'setup') {
      renderSetupToken(value);
      return;
    }
    if (kind === 'web') {
      renderWebCredential(value);
      return;
    }
  }

  function renderSetupToken(provider) {
    setOnboardingStep('action');
    $('providerActionTitle').textContent = '导入 Claude Setup Token';
    $('providerActionDescription').textContent = '格式预检通过后会直接写入认证池。';
    const panel = $('providerActionPanel');
    setHTML(
      panel,
      `<label>Claude Setup Token<input id="setupTokenInput" type="password" autocomplete="off" placeholder="粘贴 setup-token"></label><p class="field-help">凭据不会写入 Lite2API 连接配置；认证池会通过稳定连接供模型路由使用。</p><button id="importSetupToken" class="primary" type="button">验证并添加</button>`,
    );
    $('importSetupToken').addEventListener('click', async () => {
      const token = $('setupTokenInput').value.trim();
      if (!token) {
        toast('请填写 Setup Token', true);
        return;
      }
      const data = {
        type: 'sub2api-data',
        version: 1,
        accounts: [
          {
            id: `claude-setup-${Date.now()}`,
            name: 'Claude Setup Token',
            platform: provider,
            type: 'oauth',
            credentials: { setup_token: token },
          },
        ],
      };
      try {
        const preview = await request('/accounts/import', {
          method: 'POST',
          body: JSON.stringify({ data, mode: 'skip', dry_run: true }),
        });
        if ((preview.oauth_failed || 0) > 0)
          throw new Error(preview.errors?.[0]?.message || 'Setup Token 预检失败');
        await request('/accounts/import', {
          method: 'POST',
          body: JSON.stringify({ data, mode: 'skip', dry_run: false }),
        });
        closeDialog('addAccountDialog');
        await refreshAll(true, true);
        finishCredentialOnboarding({
          kind: 'credential',
          provider,
          connection_id: findOAuthPoolConnection()?.id || '',
          title: 'Setup Token 已加入认证池',
        });
      } catch (error) {
        toast(error.message, true);
      }
    });
  }

  function normalizeCookie(value) {
    let pairs = [];
    try {
      const parsed = JSON.parse(value);
      if (Array.isArray(parsed))
        pairs = parsed
          .filter((item) => item?.name && item.value !== undefined)
          .map((item) => [item.name, item.value]);
    } catch {}
    if (!pairs.length && value.includes('\t'))
      pairs = value
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter((line) => line && !line.startsWith('#'))
        .map((line) => line.split('\t'))
        .filter((parts) => parts.length >= 7)
        .map((parts) => [parts[5], parts[6]]);
    if (!pairs.length)
      pairs = value
        .split(';')
        .map((part) => {
          const at = part.indexOf('=');
          return at > 0 ? [part.slice(0, at).trim(), part.slice(at + 1).trim()] : null;
        })
        .filter(Boolean);
    return [...new Map(pairs).entries()].map(([name, val]) => `${name}=${val}`).join('; ');
  }

  function renderWebCredential(provider) {
    setOnboardingStep('action');
    const panel = $('providerActionPanel'),
      gemini = provider === 'gemini';
    $('providerActionTitle').textContent = gemini ? '准备 Gemini Web 凭据' : '准备 Grok SSO / Cookie';
    $('providerActionDescription').textContent =
      '这是凭据准备指南，不会在点击复制后假装已经添加账号。完成适配器侧写入后，再创建稳定 API 连接。';
    setHTML(
      panel,
      `<label>${gemini ? 'Gemini Web Cookie' : 'Grok SSO / Cookie'}<textarea id="webCredentialInput" placeholder="粘贴 Cookie-Editor JSON、Netscape Cookie 或适配器 SSO 文本"></textarea></label><div class="security-note">敏感内容只在当前浏览器整理，不会提交给 Lite2API。请把整理结果写入对应隔离适配器的凭据存储。</div><div class="heading-actions"><button id="normalizeCredential" class="secondary" type="button">本地整理并复制</button><button id="openAdapterConnection" class="primary" type="button">下一步：配置稳定连接</button></div>`,
    );
    $('normalizeCredential').addEventListener('click', async () => {
      let value = $('webCredentialInput').value.trim();
      if (!value) {
        toast('请先粘贴凭据', true);
        return;
      }
      if (gemini) value = normalizeCookie(value);
      else {
        try {
          value = JSON.stringify(JSON.parse(value), null, 2);
        } catch {}
      }
      $('webCredentialInput').value = value;
      try {
        await UI.copy(value);
        toast('已整理并复制；尚未写入任何服务');
      } catch {
        toast('已整理，请手动复制');
      }
    });
    $('openAdapterConnection').addEventListener('click', () => {
      closeDialog('addAccountDialog');
      openManualAccount(gemini ? 'gemini-web' : 'grok-adapter');
    });
  }

  function fillManualForm(preset, existing) {
    const form = $('manualAccountForm');
    form.reset();
    const source = existing || preset,
      localAdapter = String(preset.base_url || '').startsWith('http://127.0.0.1'),
      mode = existing ? (existing.api_key_env ? 'env' : 'key') : localAdapter ? 'env' : 'key';
    form.elements.name.value = source.name || '';
    form.elements.base_url.value = source.base_url || '';
    form.elements.id.value = existing?.id || nextID(preset.id);
    form.elements.id.readOnly = !!existing;
    form.elements.type.value = source.type || 'openai';
    form.elements.concurrency.value = source.concurrency ?? 2;
    form.elements.enabled.value = String(source.enabled !== false);
    form.elements.priority.value = source.priority ?? 0;
    form.elements.weight.value = source.weight ?? 1;
    form.elements.proxy_url.value = source.proxy_url || '';
    form.elements.auth_header.value = source.auth_header || '';
    form.elements.auth_scheme.value = source.auth_scheme || '';
    form.elements.models.value = existing ? (source.models || []).join(',') : '';
    form.elements.model_map.value = JSON.stringify(source.model_map || {});
    form.elements.headers.value = JSON.stringify(source.headers || {});
    form.elements.headers_env.value = JSON.stringify(source.headers_env || {});
    form.elements.api_key.value = '';
    form.elements.api_key.placeholder =
      existing && existing.api_key ? '留空保留现有 API Key' : '粘贴 API Key';
    form.elements.api_key_env.value = source.api_key_env || preset.api_key_env || '';
    one(`input[name="credential_mode"][value="${mode}"]`, form).checked = true;
    toggleCredentialFields();
    renderManualModelHints(preset.models || []);
  }

  function openManualAccount(template = 'custom', id = '') {
    const preset = PRESETS[template] || PRESETS.custom,
      existing = id ? configuredAccount(id) : null;
    onboardingState.manualTemplate = template;
    onboardingState.manualEditing = id;
    onboardingState.manualTestFingerprint = '';
    onboardingState.manualTestGeneration++;
    onboardingState.manualDiscoveredModels = [];
    $('testManualAccount').disabled = false;
    $('manualAccountTitle').textContent = existing ? '编辑 API 连接' : `添加 ${preset.name}`;
    $('saveManualAccount').textContent = existing ? '保存修改' : '测试、保存并建路由';
    fillManualForm(preset, existing);
    onboardingState.manualOriginalFingerprint = accountFingerprint(accountPayloadFromForm());
    resetConnectionTest(existing ? '未修改关键配置时直接保存；有修改会自动重新测试。' : '保存时自动测试');
    openDialog('manualAccountDialog');
  }

  function toggleCredentialFields() {
    const mode = one('input[name="credential_mode"]:checked', $('manualAccountForm'))?.value || 'key';
    $('manualKeyField').hidden = mode !== 'key';
    $('manualEnvField').hidden = mode !== 'env';
  }

  function parseObject(value, label) {
    try {
      const object = JSON.parse(value || '{}');
      if (!object || Array.isArray(object) || typeof object !== 'object') throw new Error();
      return object;
    } catch {
      throw new Error(`${label}必须是 JSON 对象`);
    }
  }

  function accountPayloadFromForm() {
    const form = $('manualAccountForm'),
      mode = one('input[name="credential_mode"]:checked', form)?.value || 'key',
      preset = PRESETS[onboardingState.manualTemplate] || PRESETS.custom,
      existing = onboardingState.manualEditing ? configuredAccount(onboardingState.manualEditing) : null;
    return {
      ...(existing || {}),
      id: form.elements.id.value.trim(),
      name: form.elements.name.value.trim(),
      type: form.elements.type.value,
      adapter_id: existing?.adapter_id || preset.adapter_id || '',
      instance_id: existing?.instance_id || preset.instance_id || '',
      operations: existing?.operations?.length ? [...existing.operations] : [...(preset.operations || [])],
      base_url: form.elements.base_url.value.trim(),
      api_key: mode === 'key' ? form.elements.api_key.value.trim() : '',
      api_key_env: mode === 'env' ? form.elements.api_key_env.value.trim() : '',
      auth_header: form.elements.auth_header.value.trim(),
      auth_scheme: form.elements.auth_scheme.value.trim(),
      headers: parseObject(form.elements.headers.value, '固定请求头'),
      headers_env: parseObject(form.elements.headers_env.value, '环境变量请求头'),
      models: form.elements.models.value
        .split(',')
        .map((value) => value.trim())
        .filter(Boolean),
      model_map: parseObject(form.elements.model_map.value, '模型映射'),
      priority: Number(form.elements.priority.value) || 0,
      weight: Number(form.elements.weight.value) || 1,
      concurrency: Number(form.elements.concurrency.value) || 0,
      enabled: form.elements.enabled.value === 'true',
      proxy_url: form.elements.proxy_url.value.trim(),
    };
  }

  function renderManualModelHints(models) {
    setHTML(
      $('manualModelHints'),
      models
        .map(
          (model) =>
            `<button type="button" data-model-hint="${escapeHTML(model)}">${escapeHTML(model)}</button>`,
        )
        .join(''),
    );
  }

  function resetConnectionTest(message = '尚未测试') {
    onboardingState.manualTestFingerprint = '';
    all('#connectionTestSteps li').forEach((item) => (item.className = ''));
    const node = $('connectionTestResult');
    node.className = 'test-result';
    node.textContent = message;
  }

  async function testManualAccount() {
    let account;
    try {
      account = accountPayloadFromForm();
    } catch (error) {
      toast(error.message, true);
      return;
    }
    const generation = onboardingState.manualTestGeneration,
      testedFingerprint = accountFingerprint(account),
      button = $('testManualAccount');
    button.disabled = true;
    resetConnectionTest('正在直接访问上游…');
    all('#connectionTestSteps li').forEach((item, index) => {
      if (index === 0) item.className = 'good';
    });
    try {
      const result = await request('/accounts/test', { method: 'POST', body: JSON.stringify({ account }) });
      if (generation !== onboardingState.manualTestGeneration) return;
      const currentFingerprint = accountFingerprint(accountPayloadFromForm());
      if (currentFingerprint !== testedFingerprint) {
        resetConnectionTest('测试期间关键配置发生了变化；本次结果未用于保存，请重新测试。');
        toast('连接已变化，本次测试结果已作废', true);
        return false;
      }
      all('#connectionTestSteps li').forEach((item) => (item.className = 'good'));
      onboardingState.manualDiscoveredModels = Core.uniqueStrings(result.models || []);
      if (onboardingState.manualDiscoveredModels.length && !account.models.length) {
        $('manualAccountForm').elements.models.value = onboardingState.manualDiscoveredModels.join(',');
        renderManualModelHints(onboardingState.manualDiscoveredModels);
        account = accountPayloadFromForm();
      }
      const node = $('connectionTestResult');
      node.className = 'test-result good';
      setHTML(
        node,
        `连接成功 · HTTP ${result.status}<br>${number(result.latency_ms)} ms · 发现 ${result.model_count} 个模型<br><small>${escapeHTML(result.endpoint)}</small>`,
      );
      onboardingState.manualTestFingerprint = accountFingerprint(account);
      return true;
    } catch (error) {
      if (generation !== onboardingState.manualTestGeneration) return false;
      all('#connectionTestSteps li').forEach((item, index) => {
        if (index > 0) item.className = 'bad';
      });
      const node = $('connectionTestResult');
      node.className = 'test-result bad';
      node.textContent = error.message;
      toast('连接测试失败', true);
      return false;
    } finally {
      if (generation === onboardingState.manualTestGeneration) button.disabled = false;
    }
  }

  async function saveManualAccount(event) {
    event.preventDefault();
    let account;
    try {
      account = accountPayloadFromForm();
    } catch (error) {
      toast(error.message, true);
      return;
    }
    if (!account.id || !account.name || !account.base_url) {
      toast('请完整填写名称、ID 和 Base URL', true);
      return;
    }
    const editing = !!onboardingState.manualEditing,
      button = $('saveManualAccount'),
      label = button.textContent;
    button.disabled = true;
    try {
      let fingerprint = accountFingerprint(account),
        criticalChanged = fingerprint !== onboardingState.manualOriginalFingerprint;
      const needsTest = Core.connectionTestRequired({
        editing,
        enabled: account.enabled,
        currentFingerprint: fingerprint,
        originalFingerprint: onboardingState.manualOriginalFingerprint,
        testedFingerprint: onboardingState.manualTestFingerprint,
      });
      if (needsTest) {
        button.textContent = '正在测试…';
        if (!(await testManualAccount())) return;
        account = accountPayloadFromForm();
        fingerprint = accountFingerprint(account);
        criticalChanged = fingerprint !== onboardingState.manualOriginalFingerprint;
      }
      button.textContent = '正在保存…';
      await request('/accounts', { method: 'PUT', body: JSON.stringify(account) });
      if (criticalChanged) {
        usageState.quality.delete(account.id);
        saveQuality();
      }
      closeDialog('manualAccountDialog');
      await refreshAll(true, true);
      const saved = configuredAccount(account.id) || account,
        models = Core.directModels(saved);
      if (!editing && saved.enabled !== false && models.length) {
        showView('routes', true);
        openRouteCreate(saved.id, models[0]);
        toast('连接已保存；确认对外模型名即可启用路由');
      } else {
        showView('accounts', true);
        setAccountTab('connections');
        toast(editing ? '连接已更新并热加载' : '连接已保存并热加载');
      }
    } catch (error) {
      toast(error.message, true);
    } finally {
      button.disabled = false;
      button.textContent = label;
    }
  }

  async function testExistingConnection(id) {
    const account = configuredAccount(id);
    if (!account) return;
    try {
      const result = await request('/accounts/test', { method: 'POST', body: JSON.stringify({ account }) });
      toast(`${account.name || id} 可用 · ${result.model_count} 个模型 · ${result.latency_ms} ms`);
    } catch (error) {
      toast(error.message, true);
    }
  }

  async function deleteConnection(id) {
    const account = configuredAccount(id);
    if (!account) return;
    if (hasRouteChanges()) {
      toast('请先保存或放弃未保存的路由更改，再删除连接', true);
      return;
    }
    const routes = resourcesState.data?.config?.routes || {},
      usage = Core.accountRouteUsage(routes, id),
      wildcards = usage.filter((alias) => Core.normalizeRoute(routes[alias]).all_accounts),
      fixed = usage.filter((alias) => !wildcards.includes(alias)),
      deletedRoutes = fixed.filter((alias) => {
        const targets = Core.normalizeRoute(routes[alias]).targets;
        return targets.length > 0 && targets.every((target) => target.account === id);
      }),
      details = [];
    if (fixed.length) details.push(`会从固定路由移除此目标：${fixed.join('、')}`);
    if (deletedRoutes.length) details.push(`${deletedRoutes.join('、')} 将因没有剩余目标而被删除`);
    if (wildcards.length) details.push(`通配路由 ${wildcards.join('、')} 会保留，并继续使用其他已启用连接`);
    if (
      !(await UI.confirm({
        title: '删除 API 连接',
        message: `将删除“${account.name || id}”。${details.length ? `\n\n${details.join('。\n')}。` : ''}`,
        confirmLabel: '删除连接',
        destructive: true,
      }))
    )
      return;
    try {
      await request('/accounts/' + encodeURIComponent(id), { method: 'DELETE' });
      usageState.quality.delete(id);
      saveQuality();
      toast('连接及其路由引用已删除');
      await refreshAll(true, true);
    } catch (error) {
      toast(error.message, true);
    }
  }

  function finishCredentialOnboarding(result) {
    const connection = result.connection_id ? configuredAccount(result.connection_id) : null;
    if (!connection) {
      showOnboardingResult(result);
      return;
    }
    showView('accounts', true);
    setAccountTab('auth');
    toast(`${PROVIDER_LABEL[providerKey(result.provider)] || '认证'}账号已添加并自动进入现有路由`);
  }

  function showOnboardingResult(result) {
    const connection = result.connection_id ? configuredAccount(result.connection_id) : null,
      provider =
        PROVIDER_LABEL[providerKey(result.provider, connection?.name, connection?.adapter_id)] ||
        result.provider ||
        '认证池',
      models = Core.uniqueStrings(
        result.models?.length
          ? result.models
          : [...Core.logicalModels(connection ? [connection] : []), ...Core.directModels(connection)],
      );
    onboardingState.lastOnboardingResult = { ...result, models };
    $('onboardingResultTitle').textContent = result.title || '上游已就绪';
    $('onboardingResultSubtitle').textContent =
      result.kind === 'credential'
        ? '凭据已进入隔离认证池，并通过稳定连接供路由使用。'
        : 'API 连接已保存并热加载。';
    const rows = [];
    if (result.kind === 'credential') rows.push(['凭据结果', `${provider} 认证账号已加入账号池`]);
    if (connection) rows.push(['稳定连接', `${connection.name || connection.id} · ${connection.id}`]);
    else rows.push(['稳定连接', '尚未发现可用于路由的稳定连接']);
    rows.push(['下一步', connection ? '创建模型路由，或把它加入现有 fallback' : '先检查适配器连接状态']);
    setHTML(
      $('onboardingResultSummary'),
      rows
        .map(
          ([label, value]) =>
            `<article><span>${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong></article>`,
        )
        .join(''),
    );
    setHTML(
      $('onboardingResultModels'),
      models.length
        ? models
            .slice(0, 18)
            .map((model) => modelLabelHTML(model))
            .join('')
        : '<span>模型能力未知，请先测试连接</span>',
    );
    $('resultCreateRoute').disabled = !connection;
    $('resultViewConnection').disabled = !connection;
    openDialog('onboardingResultDialog');
  }

  async function startOAuth(provider) {
    const generation = ++onboardingState.oauthGeneration;
    onboardingState.oauthSession = null;
    $('oauthTitle').textContent = (PROVIDER_LABEL[provider] || provider) + ' 授权';
    $('oauthDescription').textContent = '凭据由本机隔离适配器保存；Lite2API 只读取状态和额度。';
    $('oauthLinkBlock').hidden = true;
    $('oauthCallbackBlock').hidden = true;
    setOAuthProgress('正在生成授权链接', '请稍候', '');
    openDialog('oauthDialog');
    try {
      const session = await request('/oauth/start', { method: 'POST', body: JSON.stringify({ provider }) });
      if (generation !== onboardingState.oauthGeneration) return;
      onboardingState.oauthSession = session;
      $('oauthURL').value = session.url;
      $('oauthLinkBlock').hidden = false;
      $('oauthCallbackBlock').hidden = !session.callback_required;
      setOAuthProgress(
        '授权链接已就绪',
        session.callback_required ? '完成认证后粘贴完整回调 URL' : '打开链接后页面会自动轮询设备授权',
        '',
      );
      pollOAuth(1600, generation);
    } catch (error) {
      if (generation === onboardingState.oauthGeneration)
        setOAuthProgress('无法开始授权', error.message, 'bad');
    }
  }

  function setOAuthProgress(title, detail, tone = '') {
    const node = $('oauthProgress');
    node.className = 'oauth-progress ' + tone;
    setHTML(
      node,
      `<i></i><div><strong>${escapeHTML(title)}</strong><span>${escapeHTML(detail)}</span></div>`,
    );
  }

  async function submitOAuthCallback() {
    if (!onboardingState.oauthSession) return;
    const redirect_url = $('oauthCallbackURL').value.trim();
    if (!/^https?:\/\//.test(redirect_url)) {
      toast('请粘贴完整回调 URL', true);
      return;
    }
    try {
      await request('/oauth/callback', {
        method: 'POST',
        body: JSON.stringify({
          provider: onboardingState.oauthSession.provider,
          state: onboardingState.oauthSession.state,
          redirect_url,
        }),
      });
      setOAuthProgress('回调已提交', '正在交换凭据并加入账号池', '');
      pollOAuth(500);
    } catch (error) {
      setOAuthProgress('回调提交失败', error.message, 'bad');
    }
  }

  async function pollOAuth(delay = 1600, generation = onboardingState.oauthGeneration) {
    if (!onboardingState.oauthSession || generation !== onboardingState.oauthGeneration) return;
    await sleep(delay);
    if (!$('oauthDialog').open || generation !== onboardingState.oauthGeneration) return;
    try {
      const session = onboardingState.oauthSession,
        result = await request('/oauth/status', {
          method: 'POST',
          body: JSON.stringify({ state: session.state, provider: session.provider }),
        });
      if (generation !== onboardingState.oauthGeneration) return;
      if (result.status === 'ok') {
        setOAuthProgress(
          '授权成功',
          result.pool_ready
            ? `认证池当前 ${result.credential_count || 1} 个凭据可用`
            : result.warning || '凭据已保存',
          'good',
        );
        await refreshAll(true, true);
        const connection = findOAuthPoolConnection();
        onboardingState.oauthSession = null;
        closeDialog('oauthDialog');
        finishCredentialOnboarding({
          kind: 'credential',
          provider: session.provider,
          connection_id: connection?.id || '',
          title: '认证账号已加入账号池',
          models: Core.directModels(connection),
        });
        return;
      }
      if (result.status === 'error') {
        setOAuthProgress('授权失败', result.error || '适配器返回错误', 'bad');
        return;
      }
      pollOAuth(1600, generation);
    } catch {
      pollOAuth(2500, generation);
    }
  }

  function openImportDialog() {
    onboardingState.importFiles = [];
    onboardingState.importData = null;
    onboardingState.importPreview = null;
    $('importFilesInput').value = '';
    setHTML($('importFileList'), '');
    setHTML($('importTotals'), '<span>等待预检查</span>');
    setHTML($('importPreviewRows'), '<div class="empty-copy">选择文件后执行预检查</div>');
    $('applyImport').disabled = true;
    openDialog('importDialog');
  }

  function setImportFiles(files) {
    const candidates = Array.from(files || []).filter(
        (file) => file.name.toLowerCase().endsWith('.json') || file.type === 'application/json',
      ),
      total = candidates.reduce((sum, file) => sum + file.size, 0);
    onboardingState.importFiles = total <= 900 * 1024 ? candidates : [];
    if (total > 900 * 1024) toast('导入文件总大小不能超过 900 KiB', true);
    onboardingState.importData = null;
    onboardingState.importPreview = null;
    $('applyImport').disabled = true;
    setHTML(
      $('importFileList'),
      onboardingState.importFiles
        .map(
          (file, index) =>
            `<div class="file-row"><span>${escapeHTML(file.name)} · ${number(file.size)} B</span><button type="button" data-remove-import="${index}">×</button></div>`,
        )
        .join(''),
    );
    setHTML(
      $('importPreviewRows'),
      onboardingState.importFiles.length
        ? '<div class="empty-copy">文件已选择，尚未预检查</div>'
        : '<div class="empty-copy">选择文件后执行预检查</div>',
    );
  }

  function normalizeImportObject(value) {
    if (Array.isArray(value)) return { type: 'lite2api-data', version: 1, accounts: value, proxies: [] };
    if (value?.data && !value.accounts) value = value.data;
    if (value?.accounts)
      return {
        type: value.type || 'lite2api-data',
        version: value.version ?? 1,
        exported_at: value.exported_at,
        accounts: value.accounts,
        proxies: value.proxies || [],
      };
    if (value && typeof value === 'object')
      return { type: 'lite2api-data', version: 1, accounts: [value], proxies: [] };
    throw new Error('不是可识别的账号 JSON');
  }

  async function readImportData() {
    if (!onboardingState.importFiles.length) throw new Error('请先选择 JSON 文件');
    const payloads = [];
    for (const file of onboardingState.importFiles) {
      let value;
      try {
        value = JSON.parse(await file.text());
      } catch {
        throw new Error(`${file.name} 不是有效 JSON`);
      }
      payloads.push(normalizeImportObject(value));
    }
    const types = new Set(payloads.map((payload) => String(payload.type || 'lite2api-data').toLowerCase())),
      data = {
        type: types.size === 1 ? [...types][0] : 'lite2api-data',
        version: 1,
        exported_at: new Date().toISOString(),
        accounts: payloads.flatMap((payload) => payload.accounts || []),
        proxies: payloads.flatMap((payload) => payload.proxies || []),
      };
    if (data.accounts.length > 500) throw new Error('一次最多导入 500 个账号');
    if (
      new TextEncoder().encode(JSON.stringify({ data, mode: 'upsert', dry_run: true })).length >
      1000 * 1024
    )
      throw new Error('解析后的导入请求超过 1000 KiB，请拆分文件');
    return data;
  }

  function renderImportResult(result, data) {
    const counts = [
      ['新增', result.account_created],
      ['更新', result.account_updated],
      ['跳过', result.account_skipped],
      ['失败', result.account_failed],
      ['OAuth', result.oauth_imported],
      ['代理失败', result.proxy_failed],
    ].filter(([, value]) => Number(value) > 0);
    setHTML(
      $('importTotals'),
      counts.length
        ? counts.map(([label, value]) => `<span>${label} ${number(value)}</span>`).join('')
        : '<span>没有可应用的变更</span>',
    );
    const errors = new Map((result.errors || []).map((error) => [Number(error.index), error]));
    const existing = new Set((resourcesState.data?.config?.accounts || []).map((account) => account.id));
    setHTML(
      $('importPreviewRows'),
      (data.accounts || [])
        .map((account, index) => {
          const error = errors.get(index),
            id = account.id || account.extra?.lite2api_id || `第 ${index + 1} 条`,
            action = error
              ? '不会导入'
              : existing.has(account.id)
                ? $('importMode').value === 'skip'
                  ? '跳过'
                  : '更新'
                : String(account.type || '')
                      .toLowerCase()
                      .includes('oauth') ||
                    account.credentials?.refresh_token ||
                    account.credentials?.setup_token
                  ? '导入认证池'
                  : '创建';
          return `<div class="import-preview-row"><div><strong>${escapeHTML(account.name || id)}</strong><span>${escapeHTML(error?.message || [account.platform, account.base_url].filter(Boolean).join(' · ') || '格式可识别')}</span></div><span class="status-pill ${error ? 'bad' : action === '跳过' ? 'neutral' : ''}">${escapeHTML(action)}</span></div>`;
        })
        .join('') || '<div class="empty-copy">文件中没有账号</div>',
    );
  }

  async function runImport(dryRun) {
    const previewButton = $('previewImport'),
      applyButton = $('applyImport');
    previewButton.disabled = true;
    applyButton.disabled = true;
    try {
      const data = onboardingState.importData || (await readImportData()),
        result = await request('/accounts/import', {
          method: 'POST',
          body: JSON.stringify({ data, mode: $('importMode').value, dry_run: dryRun }),
        });
      onboardingState.importData = data;
      onboardingState.importPreview = result;
      renderImportResult(result, data);
      if (dryRun) {
        applyButton.disabled = false;
        toast('预检查完成，尚未写入配置');
      } else {
        toast('导入完成并已热加载');
        closeDialog('importDialog');
        await refreshAll(true, true);
        showView('accounts', true);
        setAccountTab(result.account_created || result.account_updated ? 'connections' : 'auth');
      }
    } catch (error) {
      toast(error.message, true);
    } finally {
      previewButton.disabled = false;
      if (dryRun && onboardingState.importPreview) applyButton.disabled = false;
    }
  }

  function downloadTemplate() {
    const data = {
        type: 'lite2api-data',
        version: 1,
        exported_at: new Date().toISOString(),
        proxies: [],
        accounts: [
          {
            id: 'example-main',
            name: '示例连接',
            type: 'openai',
            base_url: 'https://api.example.com/v1',
            api_key_env: 'EXAMPLE_API_KEY',
            models: ['example-model'],
            concurrency: 2,
            priority: 0,
            weight: 1,
            enabled: false,
          },
        ],
      },
      url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })),
      link = document.createElement('a');
    link.href = url;
    link.download = 'lite2api-account-template.json';
    link.click();
    URL.revokeObjectURL(url);
  }

  function bind() {
    $('openAddAccount').addEventListener('click', openAddAccount);
    $('openImport').addEventListener('click', openImportDialog);
    $('onboardingImport').addEventListener('click', () => {
      closeDialog('addAccountDialog');
      openImportDialog();
    });
    $('providerList').addEventListener('click', (event) => {
      const button = event.target.closest('[data-provider]');
      if (button) selectOnboardingProvider(button.dataset.provider);
    });
    $('providerMethods').addEventListener('click', (event) => {
      const button = event.target.closest('[data-method]');
      if (button) providerMethod(button.dataset.method, button.dataset.value);
    });
    $('onboardingBack').addEventListener('click', () =>
      setOnboardingStep(onboardingState.onboardingStep === 'action' ? 'method' : 'provider'),
    );
    const invalidateManualTest = (event) => {
      if (event.target.name === 'credential_mode') toggleCredentialFields();
      try {
        const fingerprint = accountFingerprint(accountPayloadFromForm());
        if (onboardingState.manualTestFingerprint && fingerprint !== onboardingState.manualTestFingerprint)
          resetConnectionTest('关键连接信息已变化，请重新测试。');
      } catch {
        // A partially typed form is expected while editing. Validation belongs
        // to the field and submit action, not a toast on each keystroke.
        if (onboardingState.manualTestFingerprint) resetConnectionTest('连接信息已修改，保存时会重新测试。');
      }
    };
    $('manualAccountForm').addEventListener('submit', saveManualAccount);
    $('manualAccountForm').addEventListener('input', invalidateManualTest);
    $('manualAccountForm').addEventListener('change', invalidateManualTest);
    $('testManualAccount').addEventListener('click', testManualAccount);
    $('manualModelHints').addEventListener('click', (event) => {
      const button = event.target.closest('[data-model-hint]');
      if (!button) return;
      const input = $('manualAccountForm').elements.models,
        values = new Set(
          input.value
            .split(',')
            .map((value) => value.trim())
            .filter(Boolean),
        );
      values.has(button.dataset.modelHint)
        ? values.delete(button.dataset.modelHint)
        : values.add(button.dataset.modelHint);
      input.value = [...values].join(',');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    $('copyOAuthURL').addEventListener('click', async () => {
      try {
        await UI.copy($('oauthURL').value);
        toast('授权链接已复制');
      } catch (error) {
        toast(error.message || '复制失败，请手动复制。', true);
      }
    });
    $('openOAuthURL').addEventListener('click', () =>
      window.open($('oauthURL').value, '_blank', 'noopener,noreferrer'),
    );
    $('submitOAuthCallback').addEventListener('click', submitOAuthCallback);
    const drop = $('importDropzone');
    drop.addEventListener('keydown', (event) => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault();
        $('importFilesInput').click();
      }
    });
    $('importFilesInput').addEventListener('change', (event) => setImportFiles(event.target.files));
    ['dragenter', 'dragover'].forEach((name) =>
      drop.addEventListener(name, (event) => {
        event.preventDefault();
        drop.classList.add('drag');
      }),
    );
    ['dragleave', 'drop'].forEach((name) =>
      drop.addEventListener(name, (event) => {
        event.preventDefault();
        drop.classList.remove('drag');
      }),
    );
    drop.addEventListener('drop', (event) => setImportFiles(event.dataTransfer.files));
    $('importFileList').addEventListener('click', (event) => {
      const button = event.target.closest('[data-remove-import]');
      if (button) {
        onboardingState.importFiles.splice(Number(button.dataset.removeImport), 1);
        setImportFiles(onboardingState.importFiles);
      }
    });
    $('previewImport').addEventListener('click', () => runImport(true));
    $('applyImport').addEventListener('click', () => runImport(false));
    $('downloadImportTemplate').addEventListener('click', downloadTemplate);
    $('importMode').addEventListener('change', () => {
      $('applyImport').disabled = true;
      onboardingState.importPreview = null;
    });
    $('resultLater').addEventListener('click', () => closeDialog('onboardingResultDialog'));
    $('resultViewConnection').addEventListener('click', () => {
      closeDialog('onboardingResultDialog');
      showView('accounts', true);
      setAccountTab('connections');
      $('connectionSearch').value = onboardingState.lastOnboardingResult?.connection_id || '';
      renderConnections();
    });
    $('resultCreateRoute').addEventListener('click', () => {
      const result = onboardingState.lastOnboardingResult;
      closeDialog('onboardingResultDialog');
      openRouteCreate(result?.connection_id || '', result?.models?.[0] || '');
    });
  }

  return Object.freeze({ bind, startOAuth, openManualAccount, testExistingConnection, deleteConnection });
};
