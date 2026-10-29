/* Clients controller. Dependencies are supplied by the composition root. */
globalThis.Lite2APIClients = function createClients(context) {
  'use strict';
  const Core = globalThis.Lite2APIAppCore;
  const {
    UI,
    openDialog,
    request,
    resourcesState,
    clientsState,
    toast,
    $,
    setHTML,
    escapeHTML,
    formatTime,
    number,
    all,
    now,
    closeDialog,
  } = context;
  async function loadKeys() {
    try {
      const result = await request('/client-keys');
      resourcesState.keys = result.data || [];
      renderClients();
    } catch (error) {
      toast(error.message, true);
    }
  }

  const KEY_PRESETS = {
    personal: { rpm: 120, concurrency: 2, days: 30 },
    temporary: { rpm: 30, concurrency: 1, days: 1 },
    service: { rpm: 600, concurrency: 16, days: 90 },
  };

  function renderClients() {
    const select = $('clientModel'),
      selected = select.value,
      createdKey = (resourcesState.keys || []).find((key) => key.id === clientsState.createdKeyID) || clientsState.createdKey,
      models = Core.uniqueStrings([...(resourcesState.data?.models || []), ...Object.keys(resourcesState.data?.config?.routes || {})])
        .filter((model) => (!createdKey?.models?.length || createdKey.models.includes('*') || createdKey.models.includes(model)) &&
          Core.clientModelAvailability(resourcesState.data, model, clientsState.client).possible);
    setHTML(
      select,
      models
        .map((model) => `<option value="${escapeHTML(model)}">${escapeHTML(model)} · 可尝试，客户端待验收</option>`)
        .join(''),
    );
    if ([...select.options].some((option) => option.value === selected)) select.value = selected;
    else select.value = models[0] || '';
    select.disabled = !models.length;
    setHTML(
      $('clientKeyRows'),
      (resourcesState.keys || [])
        .map(
          (key) =>
            `<article class="key-row"><div><strong>${escapeHTML(key.name)} · ${key.enabled === false ? '已停用' : key.expires_at && Date.parse(key.expires_at) <= now() ? '已过期' : '已启用'}</strong><span>${escapeHTML(key.prefix)} · 模型 ${escapeHTML(key.models?.length && !key.models.includes('*') ? key.models.join('、') : '全部')} · ${key.rpm || '∞'} RPM · 并发 ${key.concurrency || '∞'} · ${key.expires_at ? formatTime(key.expires_at) : '不过期'} · ${number(key.total_requests)} 次请求 · 在途 ${number(key.active)}</span></div><button type="button" data-key-delete="${escapeHTML(key.id)}">撤销</button></article>`,
        )
        .join('') + `<div class="empty-copy">托管 Key 按各自的模型、频率、并发和有效期执行；撤销只拒绝后续请求，已通过鉴权的请求继续结束。支持 Bearer 与 X-Api-Key，同时提供时优先 Bearer。管理凭据独立，不能用于模型调用。${resourcesState.data?.access_policy?.legacy?.count ? `<br>另有 ${number(resourcesState.data.access_policy.legacy.count)} 个配置/环境兼容凭据：可访问全部模型，没有单 Key 限额；撤销需移除所有配置与环境来源后热加载，仍受网关和来源资源限制。` : '<br>当前未启用配置/环境兼容凭据。'}</div>`,
    );
    renderClientConfig();
  }

  function gatewayBase() {
    return Core.gatewayBaseFromPath(location.origin, location.pathname);
  }

  function gatewayAPIBase() {
    return Core.gatewayAPIBaseFromPath(location.origin, location.pathname);
  }

  function renderClientConfig() {
    const base = gatewayBase(), apiBase = gatewayAPIBase(),
      key = $('createdSecretValue').value || '<YOUR_API_KEY>',
      model = $('clientModel').value,
      modeSelect = $('clientConfigMode'), modeLabel = $('clientConfigModeLabel');
    if (modeSelect) {
      const mode = ['shell', 'persistent', 'powershell'].includes(clientsState.configMode) ? clientsState.configMode : 'shell';
      modeSelect.value = mode;
      clientsState.configMode = mode;
      if (modeLabel) modeLabel.hidden = clientsState.client !== 'claude';
    }
    const availability = Core.clientModelAvailability(resourcesState.data, model, clientsState.client);
    $('copyClientConfig').disabled = !model || !availability.possible;
    if (!model || !availability.possible) {
      $('clientConfig').textContent = '当前客户端没有允许且兼容的模型。请检查 Key 模型权限、来源接口和路由约定。';
      return;
    }
    let code = '';
    if (clientsState.client === 'claude') {
      clientsState.configMode = modeSelect?.value || clientsState.configMode || 'shell';
      if (clientsState.configMode === 'persistent') code = Core.claudeCodePersistentShellConfig({ baseUrl: base, model });
      else if (clientsState.configMode === 'powershell') code = Core.claudeCodePowerShellConfig({ baseUrl: base, model });
      else code = Core.claudeCodeShellConfig({ baseUrl: base, model, apiKey: key });
    }
    if (clientsState.client === 'codex')
      code = `export LITE2API_API_KEY=${Core.shellQuote(key)}\n\n# ~/.codex/config.toml\nmodel = ${JSON.stringify(model)}\n${availability.effort ? `model_reasoning_effort = ${JSON.stringify(availability.effort)}\n` : ''}model_provider = "lite2api"\n\n[model_providers.lite2api]\nname = "Lite2API"\nbase_url = ${JSON.stringify(apiBase)}\nenv_key = "LITE2API_API_KEY"\nwire_api = "responses"`;
    if (clientsState.client === 'openai')
      code = `from openai import OpenAI\n\nclient = OpenAI(base_url=${JSON.stringify(apiBase)}, api_key=${JSON.stringify(key)})\nresponse = client.responses.create(\n    model=${JSON.stringify(model)},\n    input="连接测试",\n)\nprint(response.output_text)`;
    if (clientsState.client === 'curl')
      code = `curl -sS ${Core.shellQuote(apiBase + '/chat/completions')} \\\n  -H ${Core.shellQuote('Authorization: Bearer ' + key)} \\\n  -H 'Content-Type: application/json' \\\n  -d ${Core.shellQuote(JSON.stringify({ model, messages: [{ role: 'user', content: '连接测试' }] }))}`;
    $('clientConfig').textContent = code;
  }

  function selectKeyPreset(name) {
    clientsState.keyPreset = name;
    all('[data-key-preset]').forEach((button) => {
      const active = button.dataset.keyPreset === name;
      button.classList.toggle('active', active);
      button.setAttribute('aria-pressed', String(active));
    });
    const preset = KEY_PRESETS[name];
    const form = $('createKeyForm');
    form.elements.rpm.value = preset.rpm;
    form.elements.concurrency.value = preset.concurrency;
    form.elements.days.value = preset.days;
  }

  async function createKey(event) {
    event.preventDefault();
    const form = $('createKeyForm'),
      days = Number(form.elements.days.value) || 30,
      input = {
        name: form.elements.name.value.trim() || `个人密钥 ${new Date().toLocaleString('zh-CN')}`,
        models: form.elements.models.value
          .split(',')
          .map((value) => value.trim())
          .filter(Boolean),
        rpm: Number(form.elements.rpm.value) || 0,
        concurrency: Number(form.elements.concurrency.value) || 0,
        expires_at: new Date(now() + days * 86400000).toISOString(),
      };
    try {
      const result = await request('/client-keys', { method: 'POST', body: JSON.stringify(input) });
      clientsState.createdKeyID = result.client_key?.id || '';
      clientsState.createdKey = result.client_key;
      closeDialog('createKeyDialog');
      $('createdSecret').hidden = false;
      $('createdSecretName').textContent = result.client_key?.name || input.name;
      $('createdSecretValue').value = result.secret;
      $('createdSecretResult').textContent = '请立即保存；服务端只保留摘要。';
      renderClients();
      toast('Key 已创建，仅显示这一次');
      await loadKeys();
    } catch (error) {
      toast(error.message, true);
    }
  }

  async function verifySecret() {
    const secret = $('createdSecretValue').value;
    if (!secret) return;
    try {
      const model = $('clientModel').value;
      const response = await fetch(gatewayAPIBase() + '/models?limit=1000', {
          headers: { Authorization: `Bearer ${secret}` },
        }),
        data = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(data.error?.message || `HTTP ${response.status}`);
      const models = Array.isArray(data.data) ? data.data : [];
      const selected = models.some((item) => item?.id === model);
      $('createdSecretResult').textContent =
        `连接验证成功，发现 ${models.length} 个可访问模型；${
          selected ? `${model} 已在目录中` : `${model} 未在目录中，请检查 Key 模型白名单`
        }；未产生模型调用。`;
    } catch (error) {
      $('createdSecretResult').textContent = '验证失败：' + error.message;
    }
  }

  async function deleteKey(id, button) {
    if (
      !(await UI.confirm({
        title: '撤销访问 Key',
        message: '后续请求将立即被拒绝。已经通过鉴权的请求继续按原约定结束。',
        confirmLabel: '撤销 Key',
        destructive: true,
      }))
    )
      return;
    try {
      await UI.runAction(
        button,
        () => request('/client-keys/' + encodeURIComponent(id), { method: 'DELETE' }),
        '撤销中…',
      );
      if (id === clientsState.createdKeyID) {
        clientsState.createdKeyID = '';
        clientsState.createdKey = null;
        $('createdSecret').hidden = true;
        $('createdSecretValue').value = '';
        renderClientConfig();
      }
      toast('Key 已撤销');
      await loadKeys();
    } catch (error) {
      toast(error.message, true);
    }
  }

  function renderAdapters() {
    setHTML(
      $('adapterList'),
      (resourcesState.adapters || [])
        .map(
          (adapter) =>
            `<article class="adapter-card"><h3>${escapeHTML(adapter.name)}</h3><p>${escapeHTML(adapter.description || '')}</p><div class="adapter-meta"><span>${escapeHTML(adapter.status || '未知')}</span><span>${escapeHTML((adapter.account_ids || []).length + ' 个连接')}</span></div></article>`,
        )
        .join('') || '<div class="empty-copy">暂无适配器数据</div>',
    );
  }

  function bind() {
    $('openCreateKey').addEventListener('click', () => {
      selectKeyPreset('personal');
      openDialog('createKeyDialog');
    });
    all('[data-key-preset]').forEach((button) =>
      button.addEventListener('click', () => selectKeyPreset(button.dataset.keyPreset)),
    );
    $('createKeyForm').addEventListener('submit', (event) => {
      event.preventDefault();
      UI.runAction(event.currentTarget.querySelector('[type="submit"]'), () => createKey(event), '创建中…');
    });
    $('clientChooser').addEventListener('click', (event) => {
      const button = event.target.closest('[data-client]');
      if (!button) return;
      clientsState.client = button.dataset.client;
      all('[data-client]').forEach((item) => {
        const active = item === button;
        item.classList.toggle('active', active);
        item.setAttribute('aria-pressed', String(active));
      });
      renderClients();
    });
    $('clientModel').addEventListener('change', renderClientConfig);
    $('clientConfigMode').addEventListener('change', (event) => {
      clientsState.configMode = event.currentTarget.value;
      renderClientConfig();
    });
    $('copyClientConfig').addEventListener('click', async () => {
      try {
        await UI.copy($('clientConfig').textContent);
        toast('配置已复制');
      } catch (error) {
        toast(error.message || '复制失败，请手动复制。', true);
      }
    });
    $('copyCreatedSecret').addEventListener('click', async () => {
      try {
        await UI.copy($('createdSecretValue').value);
        toast('Key 已复制');
      } catch (error) {
        toast(error.message || '复制失败，请手动复制。', true);
      }
    });
    $('verifyCreatedSecret').addEventListener('click', verifySecret);
    $('clientKeyRows').addEventListener('click', (event) => {
      const button = event.target.closest('[data-key-delete]');
      if (button) deleteKey(button.dataset.keyDelete, button);
    });
  }

  return Object.freeze({ bind, renderClients, renderAdapters });
};
