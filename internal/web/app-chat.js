/* Chat controller. Dependencies are supplied by the composition root. */
globalThis.Lite2APIChat = function createChat(context) {
  'use strict';
  const {
    resourcesState,
    chatState,
    Core,
    $,
    setHTML,
    number,
    escapeHTML,
    configuredAccount,
    accountTestModel,
    toast,
    openDialog,
    request,
  } = context;
  function channelChatAccounts() {
    return (resourcesState.data?.config?.accounts || []).filter(
      (account) => account.enabled !== false && Core.channelVerificationSupported(account),
    );
  }

  function channelChatErrorText(error) {
    const upstream = error?.payload?.upstream;
    let detail = '';
    if (typeof upstream === 'string') detail = upstream;
    else if (upstream && typeof upstream === 'object')
      detail = upstream.error?.message || upstream.message || '';
    return detail && detail !== error.message
      ? `${error.message}：${detail}`
      : error.message || '渠道聊天测试失败';
  }

  function channelChatRawPreview(value) {
    let raw = '';
    try {
      raw = JSON.stringify(value ?? {}, null, 2);
    } catch {
      raw = String(value ?? '');
    }
    return raw.length > 32768 ? raw.slice(0, 32768) + '\n… 原始响应过长，已截断显示' : raw;
  }

  function renderChannelChat() {
    const container = $('channelChatMessages'),
      items = [...chatState.channelChatMessages];
    if (chatState.channelChatPending)
      items.push({ role: 'pending', content: chatState.channelVerificationPending ? '正在用固定样例验收所选组合…' : '上游正在生成回复…', at: new Date().toISOString() });
    container.setAttribute('aria-busy', String(chatState.channelChatPending));
    if (!items.length) {
      setHTML(
        container,
        '<div class="channel-chat-empty"><strong>开始一次真实渠道对话</strong><span>回复会显示实际模型、耗时、Token 和请求 ID。</span></div>',
      );
      return;
    }
    setHTML(
      container,
      items
        .map((message) => {
          const role = message.role,
            label =
              role === 'user'
                ? '你'
                : role === 'assistant'
                  ? '渠道回复'
                  : role === 'error'
                    ? '请求失败'
                    : '等待中',
            meta = [];
          if (role === 'assistant') {
            if (message.upstream_model) meta.push(`上游 ${message.upstream_model}`);
            if (Number.isFinite(message.latency_ms)) meta.push(`${number(message.latency_ms)} ms`);
            if (message.total_tokens !== null && message.total_tokens !== undefined)
              meta.push(
                `Token ${number(message.total_tokens)}${message.input_tokens !== null && message.output_tokens !== null ? `（${number(message.input_tokens)} + ${number(message.output_tokens)}）` : ''}`,
              );
            if (message.finish_reason) meta.push(message.finish_reason);
            if (message.request_id) meta.push(`请求 ${message.request_id}`);
            if (message.verification) {
              const check = message.verification;
              meta.push(`${check.operation} · ${check.feature} · ${check.reasoning_effort || 'auto'}${check.credential_id ? ' · 凭据 ' + check.credential_id : ''}`);
              meta.push(check.evidence_saved ? `验收依据已保存 · ${check.observed_at}` : `调用已完成，验收依据未保存：${check.message || '请刷新来源后重新验证'}`);
            }
          }
          const raw = message.raw
            ? `<details><summary>查看原始响应</summary><pre>${escapeHTML(message.raw)}</pre></details>`
            : '';
          return `<article class="channel-chat-message ${escapeHTML(role)}"><header><span>${label}</span><time>${new Date(message.at || Date.now()).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</time></header><div class="channel-chat-content">${escapeHTML(message.content)}</div>${meta.length ? `<footer>${meta.map((value) => `<span>${escapeHTML(value)}</span>`).join('')}</footer>` : ''}${raw}</article>`;
        })
        .join(''),
    );
    requestAnimationFrame(() => {
      container.scrollTop = container.scrollHeight;
    });
  }

  function syncChannelChatTarget(resetModel = false) {
    const account = configuredAccount($('channelChatAccount').value),
      modelInput = $('channelChatModel');
    chatState.channelChatAccount = account?.id || '';
    const models = Core.channelChatModels(account);
    setHTML(
      $('channelChatModels'),
      models.map((model) => `<option value="${escapeHTML(model)}"></option>`).join(''),
    );
    if (resetModel || !modelInput.value.trim())
      modelInput.value = accountTestModel(account) || models[0] || '';
    const protocol = !Core.channelChatSupported(account) ? '此连接仅支持接口验收' : account?.type === 'anthropic' ? 'Anthropic Messages' : 'OpenAI Chat Completions';
    const pool = account?.adapter_id === 'cli-proxy-api';
    $('channelVerifyCredentialField').hidden = !pool;
    const selectedCredential = $('channelVerifyCredential').value;
    setHTML($('channelVerifyCredential'), '<option value="">自动选择，记录实际账号</option>' +
      (pool ? resourcesState.oauth || [] : []).filter((item) => !item.disabled)
        .map((item) => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.identity || item.id)} · ${escapeHTML(item.provider || '')}</option>`).join(''));
    if ([...$('channelVerifyCredential').options].some((option) => option.value === selectedCredential))
      $('channelVerifyCredential').value = selectedCredential;
    $('channelChatTarget').textContent = account
      ? `直连 ${account.name || account.id} · 聊天使用 ${protocol} · ${modelInput.value.trim() || '请填写模型'}。验收使用所选接口和固定样例，工具项执行一次往返；均会真实调用并消耗额度。`
      : '当前没有可聊天测试的已启用渠道。';
  }

  function cancelChannelChat() {
    if (chatState.channelChatController && !chatState.channelChatController.signal.aborted)
      chatState.channelChatController.abort();
  }

  function setChannelChatBusy(busy) {
    chatState.channelChatPending = busy;
    for (const id of [
      'channelChatAccount',
      'channelChatModel',
      'channelChatTemperature',
      'channelChatMaxTokens',
      'channelChatSystem',
      'channelChatInput',
      'clearChannelChat',
      'sendChannelChat',
      'channelVerifyOperation', 'channelVerifyFeature', 'channelVerifyEffort', 'channelVerifyCredential', 'verifyChannelCapability',
    ])
      $(id).disabled = busy;
    $('stopChannelChat').hidden = !busy;
    $('sendChannelChat').textContent = busy ? '等待回复' : '发送测试';
    if (!busy) $('sendChannelChat').disabled = !Core.channelChatSupported(configuredAccount($('channelChatAccount').value));
  }

  function resetChannelChat() {
    if (chatState.channelChatPending) return;
    chatState.channelChatGeneration++;
    chatState.channelChatMessages = [];
    renderChannelChat();
    $('channelChatInput').value = '';
    $('channelChatInput').focus();
  }

  function openChannelChat(id = '') {
    const accounts = channelChatAccounts();
    if (!accounts.length) {
      toast('当前没有支持聊天的已启用渠道', true);
      return;
    }
    cancelChannelChat();
    chatState.channelChatGeneration++;
    chatState.channelChatMessages = [];
    chatState.channelChatPending = false;
    chatState.channelChatController = null;
    const selected = accounts.find((account) => account.id === id) || accounts[0],
      select = $('channelChatAccount');
    setHTML(
      select,
      accounts
        .map(
          (account) =>
            `<option value="${escapeHTML(account.id)}">${escapeHTML(account.name || account.id)} · ${escapeHTML(account.id)}</option>`,
        )
        .join(''),
    );
    select.value = selected.id;
    $('channelChatModel').value = '';
    $('channelChatSystem').value = '';
    $('channelChatTemperature').value = '';
    $('channelChatMaxTokens').value = '';
    $('channelChatInput').value = '';
    setChannelChatBusy(false);
    syncChannelChatTarget(true);
    renderChannelChat();
    openDialog('channelChatDialog');
    setTimeout(() => $('channelChatInput').focus(), 0);
  }

  function boundedChannelChatMessages(system) {
    let history = chatState.channelChatMessages
        .filter((message) => ['user', 'assistant'].includes(message.role) && message.included !== false)
        .map((message) => ({ role: message.role, content: message.content })),
      limit = system ? 63 : 64;
    history = history.slice(-limit);
    if (history[0]?.role === 'assistant') history.shift();
    let messages = system ? [{ role: 'system', content: system }, ...history] : history;
    while (messages.length > 1 && new TextEncoder().encode(JSON.stringify(messages)).length > 240 * 1024) {
      history.shift();
      if (history[0]?.role === 'assistant') history.shift();
      messages = system ? [{ role: 'system', content: system }, ...history] : history;
    }
    return messages;
  }

  async function sendChannelChat(event) {
    event.preventDefault();
    if (chatState.channelChatPending) return;
    const account = configuredAccount($('channelChatAccount').value),
      model = $('channelChatModel').value.trim(),
      content = $('channelChatInput').value.trim(),
      temperatureText = $('channelChatTemperature').value.trim(),
      maxTokensText = $('channelChatMaxTokens').value.trim();
    if (!account || account.enabled === false || !Core.channelChatSupported(account)) {
      toast('请选择支持聊天的已启用渠道', true);
      return;
    }
    if (!model) {
      toast('请选择或填写测试模型', true);
      $('channelChatModel').focus();
      return;
    }
    if (!content) {
      toast('请输入要发送的消息', true);
      $('channelChatInput').focus();
      return;
    }
    const temperature = temperatureText === '' ? null : Number(temperatureText),
      maxTokens = maxTokensText === '' ? 0 : Number(maxTokensText);
    if (temperature !== null && (!Number.isFinite(temperature) || temperature < 0 || temperature > 2)) {
      toast('Temperature 必须在 0–2 之间', true);
      return;
    }
    if (!Number.isInteger(maxTokens) || maxTokens < 0 || maxTokens > 32768) {
      toast('最大输出 Token 必须是 0–32768 的整数', true);
      return;
    }
    const userMessage = { role: 'user', content, included: true, at: new Date().toISOString() };
    chatState.channelChatMessages.push(userMessage);
    $('channelChatInput').value = '';
    const generation = ++chatState.channelChatGeneration,
      controller = new AbortController();
    chatState.channelChatController = controller;
    setChannelChatBusy(true);
    renderChannelChat();
    try {
      const body = {
        account_id: account.id,
        model,
        messages: boundedChannelChatMessages($('channelChatSystem').value.trim()),
      };
      if (temperature !== null) body.temperature = temperature;
      if (maxTokens > 0) body.max_tokens = maxTokens;
      const result = await request('/prompt-test', {
        method: 'POST',
        timeout: 125000,
        signal: controller.signal,
        body: JSON.stringify(body),
      });
      if (generation !== chatState.channelChatGeneration) return;
      const parsed = Core.channelChatResponse(result.response);
      if (!parsed.text) userMessage.included = false;
      chatState.channelChatMessages.push({
        role: 'assistant',
        content: parsed.text || '上游请求成功，但响应中没有可显示的文本内容。',
        included: !!parsed.text,
        at: new Date().toISOString(),
        upstream_model: result.upstream_model || parsed.model || model,
        latency_ms: Number(result.latency_ms),
        input_tokens: parsed.input_tokens,
        output_tokens: parsed.output_tokens,
        total_tokens: parsed.total_tokens,
        finish_reason: parsed.finish_reason,
        request_id: result.request_id || parsed.response_id,
        verification: result.verification,
        raw: channelChatRawPreview(result.response),
      });
      toast(`${account.name || account.id} 回复成功 · ${number(result.latency_ms)} ms`);
    } catch (error) {
      if (generation !== chatState.channelChatGeneration) return;
      userMessage.included = false;
      chatState.channelChatMessages.push({
        role: 'error',
        content: channelChatErrorText(error),
        included: false,
        at: new Date().toISOString(),
      });
    } finally {
      if (generation === chatState.channelChatGeneration) {
        chatState.channelChatController = null;
        setChannelChatBusy(false);
        renderChannelChat();
        $('channelChatInput').focus();
      }
    }
  }

  async function verifyChannelCapability() {
    if (chatState.channelChatPending) return;
    const account = configuredAccount($('channelChatAccount').value), model = $('channelChatModel').value.trim();
    if (!account || !model) { toast('请选择连接并填写模型', true); return; }
    const generation = ++chatState.channelChatGeneration, controller = new AbortController();
    chatState.channelChatController = controller;
    chatState.channelVerificationPending = true;
    setChannelChatBusy(true);
    renderChannelChat();
    try {
      const result = await request('/accounts/verify', {
        method: 'POST', timeout: 125000, signal: controller.signal,
        body: JSON.stringify({ account_id: account.id, model, operation: $('channelVerifyOperation').value,
          feature: $('channelVerifyFeature').value, reasoning_effort: $('channelVerifyEffort').value,
          credential_id: account.adapter_id === 'cli-proxy-api' ? $('channelVerifyCredential').value : '' }),
      });
      if (generation !== chatState.channelChatGeneration) return;
      chatState.channelChatMessages.push({ role: result.ok && result.completed ? 'assistant' : 'error',
        content: `${result.ok && result.completed ? '所选调用组合完成验收' : '所选调用组合尚未通过验收'}：${account.name || account.id} · ${model} · ${result.operation || $('channelVerifyOperation').value} · ${result.feature || $('channelVerifyFeature').value}。${result.message || ''}${!result.ok ? result.evidence_saved ? '；本次失败依据已保存。' : '；本次依据未保存，请刷新来源后重试。' : ''}`,
        verification: result, included: false, at: new Date().toISOString(), request_id: result.request_id });
    } catch (error) {
      if (generation === chatState.channelChatGeneration)
        chatState.channelChatMessages.push({ role: 'error', content: error.message, included: false, at: new Date().toISOString() });
    } finally {
      if (generation === chatState.channelChatGeneration) {
        chatState.channelChatController = null;
        chatState.channelVerificationPending = false;
        setChannelChatBusy(false);
        renderChannelChat();
      }
    }
  }

  function bind() {
    $('channelChatForm').addEventListener('submit', sendChannelChat);
    $('verifyChannelCapability').addEventListener('click', verifyChannelCapability);
    $('channelChatAccount').addEventListener('change', () => {
      chatState.channelChatGeneration++;
      chatState.channelChatMessages = [];
      syncChannelChatTarget(true);
      renderChannelChat();
    });
    $('channelChatModel').addEventListener('input', () => syncChannelChatTarget(false));
    $('clearChannelChat').addEventListener('click', resetChannelChat);
    $('stopChannelChat').addEventListener('click', cancelChannelChat);
    $('channelChatInput').addEventListener('keydown', (event) => {
      if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
        event.preventDefault();
        $('channelChatForm').requestSubmit();
      }
    });
    $('channelChatDialog').addEventListener('close', () => {
      chatState.channelChatGeneration++;
      cancelChannelChat();
    });
  }

  return Object.freeze({ bind, openChannelChat, cancelChannelChat });
};
