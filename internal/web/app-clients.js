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
      models = Object.keys(resourcesState.data?.config?.routes || {});
    setHTML(
      select,
      (models.length ? models : ['<MODEL_ALIAS>'])
        .map((model) => `<option value="${escapeHTML(model)}">${escapeHTML(model)}</option>`)
        .join(''),
    );
    if ([...select.options].some((option) => option.value === selected)) select.value = selected;
    setHTML(
      $('clientKeyRows'),
      (resourcesState.keys || [])
        .map(
          (key) =>
            `<article class="key-row"><div><strong>${escapeHTML(key.name)}</strong><span>${escapeHTML(key.prefix)} · ${key.rpm || '∞'} RPM · 并发 ${key.concurrency || '∞'} · ${key.expires_at ? formatTime(key.expires_at) : '不过期'} · ${number(key.total_requests)} 次请求</span></div><button type="button" data-key-delete="${escapeHTML(key.id)}">撤销</button></article>`,
        )
        .join('') || '<div class="empty-copy">还没有客户端 Key</div>',
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
    const base = gatewayBase(),
      apiBase = gatewayAPIBase(),
      key = $('createdSecretValue').value || '<YOUR_API_KEY>',
      model = $('clientModel').value || '<MODEL_ALIAS>',
      modeSelect = $('clientConfigMode'),
      modeLabel = $('clientConfigModeLabel');
    if (modeSelect) {
      const mode = ['shell', 'persistent', 'powershell'].includes(clientsState.configMode)
        ? clientsState.configMode
        : 'shell';
      modeSelect.value = mode;
      clientsState.configMode = mode;
      if (modeLabel) modeLabel.hidden = clientsState.client !== 'claude';
    }
    let code = '';
    if (clientsState.client === 'claude') {
      clientsState.configMode = modeSelect?.value || clientsState.configMode || 'shell';
      if (clientsState.configMode === 'persistent')
        code = Core.claudeCodePersistentShellConfig({ baseUrl: base, model });
      else if (clientsState.configMode === 'powershell')
        code = Core.claudeCodePowerShellConfig({ baseUrl: base, model });
      else code = Core.claudeCodeShellConfig({ baseUrl: base, model, apiKey: key });
    }
    if (clientsState.client === 'codex')
      code = `export LITE2API_API_KEY='${key}'\n\n# ~/.codex/config.toml\nmodel = "${model}"\nmodel_provider = "lite2api"\n\n[model_providers.lite2api]\nname = "Lite2API"\nbase_url = "${apiBase}"\nenv_key = "LITE2API_API_KEY"\nwire_api = "responses"`;
    if (clientsState.client === 'openai')
      code = `from openai import OpenAI\n\nclient = OpenAI(base_url="${apiBase}", api_key="${key}")\nresponse = client.responses.create(\n    model="${model}",\n    input="连接测试",\n)\nprint(response.output_text)`;
    if (clientsState.client === 'curl')
      code = `curl -sS '${apiBase}/chat/completions' \\\n  -H 'Authorization: Bearer ${key}' \\\n  -H 'Content-Type: application/json' \\\n  -d '{"model":"${model}","messages":[{"role":"user","content":"连接测试"}]}'`;
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
      closeDialog('createKeyDialog');
      $('createdSecret').hidden = false;
      $('createdSecretName').textContent = result.client_key?.name || input.name;
      $('createdSecretValue').value = result.secret;
      $('createdSecretResult').textContent = '请立即保存；服务端只保留摘要。';
      renderClientConfig();
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
        message: '使用此 Key 的客户端将立即失去访问权限。',
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
      renderClientConfig();
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
