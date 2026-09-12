/* Pure domain helpers for the Lite2API canonical admin application. */
(function installLite2APIAppCore(root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  root.Lite2APIAppCore = Object.freeze(api);
})(typeof globalThis === 'object' ? globalThis : this, function createLite2APIAppCore() {
  'use strict';

  const VALID_EFFORTS = new Set(['', 'auto', 'none', 'minimal', 'low', 'medium', 'high', 'max', 'xhigh', 'ultra']);
  const VALID_STRATEGIES = new Set(['', 'least_loaded', 'round_robin', 'priority', 'sticky']);

  function uniqueStrings(values) {
    return [...new Set((values || []).map(value => String(value || '').trim()).filter(Boolean))];
  }

  // The admin UI and the public gateway may share a reverse-proxy prefix
  // (for example /lite-admin/ and /lite/v1). Keep this derivation pure so the
  // client generator and its tests cannot accidentally reintroduce /v1 at the
  // wrong layer.
  function gatewayBaseFromPath(origin, pathname) {
    const normalizedOrigin = String(origin || '').replace(/\/+$/, '');
    const path = String(pathname || '').replace(/\/+$/, '');
    let prefix = '';
    if (path.endsWith('-admin')) prefix = path.slice(0, -'-admin'.length);
    else if (path.endsWith('/admin')) prefix = path.slice(0, -'/admin'.length);
    return normalizedOrigin + (prefix === '/' ? '' : prefix);
  }

  function gatewayAPIBaseFromPath(origin, pathname) {
    return gatewayBaseFromPath(origin, pathname) + '/v1';
  }

  function shellQuote(value) {
    return "'" + String(value ?? '').replace(/'/g, "'\\''") + "'";
  }

  function powerShellQuote(value) {
    return "'" + String(value ?? '').replace(/'/g, "''") + "'";
  }

  function claudeCodeShellConfig({ baseUrl = '', model = '', apiKey = '' } = {}) {
    const safeBaseURL = shellQuote(String(baseUrl || '').replace(/\/+$/, ''));
    const safeModel = shellQuote(model);
    const safeAPIKey = shellQuote(apiKey || '<YOUR_API_KEY>');
    const lines = [
      '(',
      '# Claude Code → Lite2API（Bash / Zsh）',
      '# Base URL 故意不包含 /v1；Claude Code 会自动请求 /v1/messages。',
      '# Key 已由 Lite2API 自动填入；命令只在当前子 Shell 内生效。',
      '# 清理可能将 Claude Code 导向旧网关或其他云提供商的高优先级变量。',
      'unset ANTHROPIC_API_KEY ANTHROPIC_API_HOST CLAUDE_CODE_API_BASE_URL',
      'unset CLAUDE_CODE_USE_BEDROCK CLAUDE_CODE_USE_VERTEX CLAUDE_CODE_USE_FOUNDRY CLAUDE_CODE_USE_ANTHROPIC_AWS',
      `export ANTHROPIC_BASE_URL=${safeBaseURL}`,
      `export ANTHROPIC_AUTH_TOKEN=${safeAPIKey}`,
      `export ANTHROPIC_MODEL=${safeModel}`,
      `export ANTHROPIC_DEFAULT_OPUS_MODEL=${safeModel}`,
      `export ANTHROPIC_DEFAULT_SONNET_MODEL=${safeModel}`,
      `export ANTHROPIC_DEFAULT_HAIKU_MODEL=${safeModel}`,
      `export CLAUDE_CODE_SUBAGENT_MODEL=${safeModel}`,
      '',
      '# 只验证 URL、Bearer Key 与模型目录，不产生模型调用。',
      'if curl -fsS --connect-timeout 5 --max-time 15 \\',
      '  --config <(printf \'header = "Authorization: Bearer %s"\\n\' "$ANTHROPIC_AUTH_TOKEN") \\',
      '  "$ANTHROPIC_BASE_URL/v1/models?limit=1000" >/dev/null; then',
      '  claude --model "$ANTHROPIC_MODEL"',
      'else',
      "  printf '%s\\n' '网关验证失败，未启动 Claude Code。' >&2",
      'fi',
      ')',
    ];
    return lines.join('\n');
  }

  function claudeCodePowerShellConfig({ baseUrl = '', model = '' } = {}) {
    const safeBaseURL = powerShellQuote(String(baseUrl || '').replace(/\/+$/, ''));
    const safeModel = powerShellQuote(model);
    const lines = [
      '# Claude Code → Lite2API（PowerShell）',
      '# Base URL 故意不包含 /v1；Claude Code 会自动请求 /v1/messages。',
      '# 输入裸 API Key，不要手动添加 Bearer 前缀。',
      `$env:ANTHROPIC_BASE_URL = ${safeBaseURL}`,
      '$secureToken = Read-Host -Prompt \'Lite2API API Key\' -AsSecureString',
      '$tokenPtr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureToken)',
      'try {',
      '  $env:ANTHROPIC_AUTH_TOKEN = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($tokenPtr)',
      '} finally {',
      '  if ($tokenPtr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($tokenPtr) }',
      '  $secureToken = $null',
      '}',
      `$env:ANTHROPIC_MODEL = ${safeModel}`,
      '$env:CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY = \'1\'',
    ];

    if (!/(claude|anthropic)/i.test(String(model || ''))) {
      lines.push(
        `$env:ANTHROPIC_CUSTOM_MODEL_OPTION = ${safeModel}`,
        `$env:ANTHROPIC_CUSTOM_MODEL_OPTION_NAME = ${powerShellQuote(`${model || 'Lite2API'} (Lite2API)`)}`,
        `$env:ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION = 'Lite2API model route'`,
      );
    }

    lines.push(
      '',
      '# 只验证 URL、Bearer Key 与模型目录，不产生模型调用。',
      '# 验证失败时不会启动 Claude Code；关闭 Claude Code 后会清理当前进程的 Key 环境变量。',
      '$headers = @{ Authorization = "Bearer $env:ANTHROPIC_AUTH_TOKEN" }',
      'try {',
      '  Invoke-RestMethod -Method Get -Uri "$env:ANTHROPIC_BASE_URL/v1/models?limit=1000" -Headers $headers -TimeoutSec 15 -ErrorAction Stop | Out-Null',
      '  claude',
      '} catch {',
      '  Write-Error "Lite2API/Claude Code 启动失败：$($_.Exception.Message)"',
      '  throw',
      '} finally {',
      '  $headers = $null',
      '  Remove-Item Env:ANTHROPIC_AUTH_TOKEN -ErrorAction SilentlyContinue',
      '}',
    );
    return lines.join('\n');
  }

  function claudeCodePersistentShellConfig({ baseUrl = '', model = '' } = {}) {
    const safeBaseURL = shellQuote(String(baseUrl || '').replace(/\/+$/, ''));
    const safeModel = shellQuote(model);
    const lines = [
      '# Claude Code → Lite2API（用户级持久化，Bash / Zsh）',
      '# 需要已安装的 claude、curl、Python 3；输入裸 API Key，不要手动添加 Bearer 前缀。',
      '# Key 只在提示符中输入，不写进 settings.json。',
      `export ANTHROPIC_BASE_URL=${safeBaseURL}`,
      'config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/lite2api"',
      'settings_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"',
      'key_file="$config_dir/api-key"',
      'helper_file="$config_dir/api-key-helper"',
      'settings_file="$settings_dir/settings.json"',
      'if ! command -v claude >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1; then',
      "  printf '%s\\n' '需要已安装的 claude、curl 与 Python 3，未写入配置，也未启动 Claude Code。' >&2",
      'else',
      "printf 'Lite2API API Key（不会显示输入内容）: '",
      'IFS= read -r -s gateway_key',
      "printf '\\n'",
      'if [ -z "$gateway_key" ]; then',
      "  printf '%s\\n' 'Key 不能为空，未写入配置，也未启动 Claude Code。' >&2",
      'else',
      '  if ! curl -fsS --connect-timeout 5 --max-time 15 \\',
      '    --config <(printf \'header = "Authorization: Bearer %s"\\n\' "$gateway_key") \\',
      '    "$ANTHROPIC_BASE_URL/v1/models?limit=1000" >/dev/null; then',
      "    printf '%s\\n' '网关验证失败，未写入配置，也未启动 Claude Code。' >&2",
      '  else',
      '    if ! mkdir -p "$config_dir" "$settings_dir" || ! chmod 700 "$config_dir"; then',
      "      printf '%s\\n' '无法创建安全配置目录，未写入配置，也未启动 Claude Code。' >&2",
      '    else',
      `      if LITE2API_BOOTSTRAP_KEY="$gateway_key" python3 - "$key_file" "$helper_file" "$settings_file" "$ANTHROPIC_BASE_URL" ${safeModel} <<'PY'`,
      'import json',
      'import os',
      'import re',
      'import shlex',
      'import shutil',
      'import sys',
      'import tempfile',
      'import time',
      'from pathlib import Path',
      '',
      'key_path = Path(sys.argv[1]).expanduser().resolve()',
      'helper_path = Path(sys.argv[2]).expanduser().resolve()',
      'settings_path = Path(sys.argv[3]).expanduser().resolve()',
      'base_url = sys.argv[4].rstrip("/")',
      'model = sys.argv[5]',
      'api_key = os.environ.get("LITE2API_BOOTSTRAP_KEY", "")',
      '',
      'if not api_key:',
      '    raise SystemExit("Key 不能为空")',
      'if not base_url:',
      '    raise SystemExit("Base URL 不能为空")',
      '',
      'settings = {}',
      'if settings_path.exists():',
      '    try:',
      '        with settings_path.open(encoding="utf-8") as stream:',
      '            settings = json.load(stream)',
      '    except json.JSONDecodeError as exc:',
      '        raise SystemExit(f"现有 settings.json 不是有效 JSON：{exc}")',
      '    if not isinstance(settings, dict):',
      '        raise SystemExit("现有 settings.json 顶层必须是对象")',
      '',
      'env = settings.setdefault("env", {})',
      'if not isinstance(env, dict):',
      '    raise SystemExit("现有 settings.json 的 env 必须是对象")',
      'for variable in ("ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"):',
      '    if env.get(variable):',
      '        raise SystemExit(f"现有 settings.json 已设置 {variable}；为避免覆盖，请先清理后重试")',
      '',
      'helper_command = shlex.quote(str(helper_path))',
      'existing_helper = settings.get("apiKeyHelper")',
      'if existing_helper and str(existing_helper) != helper_command:',
      '    raise SystemExit("现有 apiKeyHelper 指向其他程序；为避免覆盖，请先人工合并")',
      '',
      'env.update({',
      '    "ANTHROPIC_BASE_URL": base_url,',
      '    "ANTHROPIC_MODEL": model,',
      '    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",',
      '})',
      'if model and not re.search(r"(claude|anthropic)", model, re.IGNORECASE):',
      '    env.update({',
      '        "ANTHROPIC_CUSTOM_MODEL_OPTION": model,',
      '        "ANTHROPIC_CUSTOM_MODEL_OPTION_NAME": f"{model} (Lite2API)",',
      '        "ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION": "Lite2API model route",',
      '    })',
      'settings["apiKeyHelper"] = helper_command',
      '',
      'def atomic_write(path, content, mode):',
      '    path.parent.mkdir(parents=True, exist_ok=True)',
      '    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent, text=True)',
      '    try:',
      '        with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as stream:',
      '            stream.write(content)',
      '            stream.flush()',
      '            os.fsync(stream.fileno())',
      '        os.chmod(temporary, mode)',
      '        os.replace(temporary, path)',
      '    finally:',
      '        if os.path.exists(temporary):',
      '            os.unlink(temporary)',
      '',
      'if settings_path.exists():',
      '    backup = settings_path.with_name(settings_path.name + f".lite2api.bak.{time.time_ns()}")',
      '    shutil.copy2(settings_path, backup)',
      '    os.chmod(backup, 0o600)',
      'atomic_write(key_path, api_key + "\\n", 0o600)',
      'atomic_write(helper_path, "#!/bin/sh\\nexec cat " + shlex.quote(str(key_path)) + "\\n", 0o700)',
      'atomic_write(settings_path, json.dumps(settings, ensure_ascii=False, indent=2, sort_keys=True) + "\\n", 0o600)',
      'print(f"已写入 {settings_path}；Key helper 位于 {helper_path}。")',
      'PY',
      '      then',
      '        unset gateway_key LITE2API_BOOTSTRAP_KEY',
      '        claude',
      '      fi',
      '    fi',
      '  fi',
      'fi',
      'fi',
      'unset gateway_key LITE2API_BOOTSTRAP_KEY',
    ];
    return lines.join('\n');
  }

  // Return only keys backed by checked-in, vendor-provided artwork. Model
  // aliases deliberately resolve to the icon for their effective upstream
  // model so the UI never invents a look for a model family.
  function modelIconKey(value) {
    const model = String(value || '').trim().toLowerCase().replace(/^(?:antigravity|claude-code)\//, '');
    if (!model) return '';
    if (model === 'sol' || model.includes('gpt-5.6-sol')) return 'gpt-5-6-sol';
    if (model === 'terra' || model.includes('gpt-5.6-terra')) return 'gpt-5-6-terra';
    if (model === 'luna' || model.includes('gpt-5.6-luna')) return 'gpt-5-6-luna';
    if (model.includes('gpt-image-2')) return 'gpt-image-2';
    if (model.includes('gpt-oss-120b')) return 'gpt-oss-120b';
    if (model.includes('gpt-5.4-mini')) return 'gpt-5-4-mini';
    if (model.includes('gpt-5.5')) return 'gpt-5-5';
    if (model.includes('gpt-5.4')) return 'gpt-5-4';
    if (model.includes('gpt-5.3-codex')) return 'gpt-5-3-codex';
    if (model === 'codex-auto-review') return 'openai';
    if (/claude|opus|sonnet|haiku|fable|mythos/.test(model)) return 'claude';
    if (/gemini|nano[-_ ]?banana/.test(model)) return 'gemini';
    if (model === 'antigravity') return 'antigravity';
    return '';
  }

  function finiteNumber(value) {
    if (value === null || value === undefined || (typeof value === 'string' && !value.trim())) return null;
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : null;
  }

  function meteredTokenTotal(records) {
    const metered = (records || []).filter(record => record?.usage_available === true);
    if (!metered.length) return null;
    return metered.reduce((sum, record) => sum + (finiteNumber(record.total_tokens) ?? 0), 0);
  }

  function stableValue(value) {
    if (Array.isArray(value)) return value.map(stableValue);
    if (!value || typeof value !== 'object') return value;
    return Object.fromEntries(Object.keys(value).sort().map(key => [key, stableValue(value[key])]));
  }

  function stableStringify(value) {
    return JSON.stringify(stableValue(value));
  }

  function hashText(value) {
    let hash = 0xcbf29ce484222325n;
    for (const byte of new TextEncoder().encode(String(value || ''))) {
      hash ^= BigInt(byte);
      hash = BigInt.asUintN(64, hash * 0x100000001b3n);
    }
    return hash.toString(16).padStart(16, '0');
  }

  function hashedRecordValues(record) {
    return Object.fromEntries(Object.entries(record || {}).map(([key, value]) => [key, hashText(value)]));
  }

  function accountFingerprint(account) {
    const models = uniqueStrings(account?.models).sort();
    return stableStringify({
      type: String(account?.type || '').trim(),
      base_url: String(account?.base_url || '').trim().replace(/\/$/, ''),
      api_key: account?.api_key ? hashText(account.api_key) : '',
      api_key_env: String(account?.api_key_env || '').trim(),
      auth_header: String(account?.auth_header || '').trim().toLowerCase(),
      auth_scheme: String(account?.auth_scheme || '').trim(),
      headers: hashedRecordValues(account?.headers),
      headers_env: account?.headers_env || {},
      proxy_url: account?.proxy_url ? hashText(String(account.proxy_url).trim()) : '',
      models,
      model_map: account?.model_map || {}
    });
  }

  function connectionTestRequired({ editing = false, enabled = true, currentFingerprint = '', originalFingerprint = '', testedFingerprint = '' } = {}) {
    if (!enabled) return false;
    if (!editing) return testedFingerprint !== currentFingerprint;
    const criticalChanged = currentFingerprint !== originalFingerprint;
    return criticalChanged && testedFingerprint !== currentFingerprint;
  }

  function routeFingerprint(routes) {
    return stableStringify(routes || {});
  }

  function normalizeTarget(target = {}) {
    return {
      account: String(target.account || '').trim(),
      credential: String(target.credential || '').trim(),
      model: String(target.model || '').trim(),
      reasoning_effort: String(target.reasoning_effort || '').trim().toLowerCase()
    };
  }

  function normalizeRoute(raw = {}) {
    let targets = Array.isArray(raw.targets) ? raw.targets.map(normalizeTarget) : [];
    const legacyAccounts = Array.isArray(raw.accounts) ? uniqueStrings(raw.accounts) : [];
    // `normalizeRoute` is deliberately idempotent: UI state stores normalized
    // routes, so the legacy marker must survive subsequent render/serialize
    // passes after `accounts` has been expanded into display-only targets.
    const legacy = raw.legacy === true || (targets.length === 0 && (legacyAccounts.length > 0 || raw.all_accounts === true));
    if (legacyAccounts.length && !targets.length) {
      targets = legacyAccounts.map(account => normalizeTarget({
        account,
        model: raw.upstream_model || '',
        reasoning_effort: ''
      }));
    }
    return {
      model: String(raw.model || '').trim(),
      reasoning_effort: String(raw.reasoning_effort || 'auto').trim().toLowerCase() || 'auto',
      strategy: String(raw.strategy || '').trim().toLowerCase(),
      targets,
      legacy,
      all_accounts: legacy && raw.all_accounts === true,
      upstream_model: legacy ? String(raw.upstream_model || '').trim() : ''
    };
  }

  function logicalModels(accounts) {
    return uniqueStrings((accounts || []).flatMap(account =>
      (account.capabilities || []).map(capability => capability?.model)
    )).sort();
  }

  function directModels(account) {
    const declared = (account?.models || []).filter(model => model && model !== '*');
    const mapped = Object.values(account?.model_map || {});
    // Keep this exactly aligned with config.AccountSupportsTargetModel. A
    // capability's upstream model is valid in logical mode, but it is not
    // automatically an advertised direct-routing model.
    return uniqueStrings([...declared, ...mapped]).sort();
  }

  function channelChatSupported(account) {
    const operations = uniqueStrings(account?.operations);
    if (!operations.length) return true;
    return operations.includes('openai.chat') || operations.includes('anthropic.messages');
  }

  function channelChatModels(account) {
    const declared = (account?.models || []).filter(model => model && model !== '*');
    const capabilities = account?.capabilities || [];
    return uniqueStrings([
      ...declared,
      ...capabilities.map(capability => capability?.model),
      ...Object.keys(account?.model_map || {}),
      ...Object.values(account?.model_map || {}),
      ...capabilities.map(capability => capability?.upstream_model)
    ]);
  }

  function chatContentText(value) {
    if (typeof value === 'string') return value.trim();
    if (!Array.isArray(value)) return '';
    return value.map(part => {
      if (typeof part === 'string') return part;
      if (typeof part?.text === 'string') return part.text;
      if (typeof part?.content === 'string') return part.content;
      return '';
    }).filter(Boolean).join('\n').trim();
  }

  function channelChatResponse(response) {
    const choice = response?.choices?.[0] || {};
    const usage = response?.usage || {};
    const inputTokens = finiteNumber(usage.prompt_tokens) ?? finiteNumber(usage.input_tokens);
    const outputTokens = finiteNumber(usage.completion_tokens) ?? finiteNumber(usage.output_tokens);
    let totalTokens = finiteNumber(usage.total_tokens);
    if (totalTokens === null && (inputTokens !== null || outputTokens !== null)) {
      totalTokens = (inputTokens ?? 0) + (outputTokens ?? 0);
    }
    return {
      text: chatContentText(choice.message?.content) || chatContentText(choice.text) || chatContentText(response?.content) || chatContentText(response?.output_text),
      input_tokens: inputTokens,
      output_tokens: outputTokens,
      total_tokens: totalTokens,
      finish_reason: String(choice.finish_reason || response?.stop_reason || '').trim(),
      response_id: String(response?.id || '').trim(),
      model: String(response?.model || '').trim()
    };
  }

  function capabilityResolution(account, model, effort = 'auto') {
    const wantedModel = String(model || '').trim();
    const wantedEffort = String(effort || 'auto').trim().toLowerCase() || 'auto';
    const capability = (account?.capabilities || []).find(item =>
      String(item?.model || '').trim() === wantedModel &&
      (item.reasoning_efforts || []).map(value => String(value).toLowerCase()).includes(wantedEffort)
    );
    if (!capability) return null;
    return {
      ok: true,
      verified: true,
      upstream_model: String(capability.upstream_model || '').trim(),
      reasoning_effort: wantedEffort,
      source: 'capability'
    };
  }

  function directResolution(account, model) {
    const wanted = String(model || '').trim();
    if (!wanted) return { ok: false, verified: false, upstream_model: '', reason: '尚未填写实际上游模型' };
    if ((account?.models || []).includes('*')) {
      return { ok: true, verified: true, upstream_model: wanted, reasoning_effort: '', source: 'wildcard' };
    }
    const declared = directModels(account);
    if (!declared.length) {
      return {
        ok: true,
        verified: false,
        upstream_model: wanted,
        reason: '连接未声明模型目录；将按手动模型保存'
      };
    }
    if (!declared.includes(wanted)) {
      return {
        ok: false,
        verified: true,
        upstream_model: wanted,
        reason: `连接没有声明模型 ${wanted}`
      };
    }
    return { ok: true, verified: true, upstream_model: wanted, reasoning_effort: '', source: 'direct' };
  }

  function targetResolution(account, route, target) {
    if (!account) return { ok: false, verified: false, upstream_model: '', reason: '连接不存在' };
    const normalized = normalizeRoute(route);
    if (normalized.model) return capabilityResolution(account, normalized.model, normalized.reasoning_effort) || {
      ok: false,
      verified: true,
      upstream_model: '',
      reason: `不支持 ${normalized.model} · ${normalized.reasoning_effort}`
    };
    return directResolution(account, target?.model);
  }

  function routeValidation(routes, accounts) {
    const errors = [];
    const warnings = [];
    const accountMap = new Map((accounts || []).map(account => [account.id, account]));
    for (const [alias, raw] of Object.entries(routes || {})) {
      const route = normalizeRoute(raw);
      if (!alias.trim()) errors.push({ alias, message: '客户端别名不能为空' });
      if (alias !== alias.trim()) errors.push({ alias, message: '客户端别名首尾不能有空格' });
      if (new TextEncoder().encode(alias).length > 256) errors.push({ alias, message: '客户端别名不能超过 256 字节' });
      if (!VALID_STRATEGIES.has(route.strategy)) errors.push({ alias, message: `不支持调度策略 ${route.strategy}` });
      if (route.legacy) {
        if (new TextEncoder().encode(route.upstream_model).length > 256) errors.push({ alias, message: '旧式上游模型不能超过 256 字节' });
        const legacyTargets = route.all_accounts
          ? (accounts || []).filter(account => account.enabled !== false).map(account => ({ account: account.id, model: route.upstream_model }))
          : route.targets;
        if (!route.all_accounts && !legacyTargets.length) errors.push({ alias, message: '旧式路由至少需要一个连接' });
        if (!legacyTargets.length && route.all_accounts) warnings.push({ alias, message: '通配路由当前没有已启用连接' });
        if (legacyTargets.length > 64 && !route.all_accounts) errors.push({ alias, message: '旧式路由的连接不能超过 64 个' });
        legacyTargets.forEach((target, targetIndex) => {
          if (new TextEncoder().encode(target.account).length > 128) errors.push({ alias, targetIndex, message: `第 ${targetIndex + 1} 个旧式连接 ID 不能超过 128 字节` });
          const account = accountMap.get(target.account);
          if (!account) {
            errors.push({ alias, targetIndex, message: `第 ${targetIndex + 1} 个旧式连接不存在：${target.account}` });
            return;
          }
          if (route.upstream_model) {
            const resolution = directResolution(account, route.upstream_model);
            if (!resolution.ok) errors.push({ alias, targetIndex, message: `${account.name || account.id}：${resolution.reason}` });
            else if (!resolution.verified) warnings.push({ alias, targetIndex, message: `${account.name || account.id}：${resolution.reason}` });
          }
          if (account.enabled === false && !route.all_accounts) warnings.push({ alias, targetIndex, message: `${account.name || account.id}：连接当前已停用` });
        });
        continue;
      }
      if (!route.targets.length) errors.push({ alias, message: '至少需要一个真实上游目标' });
      if (route.targets.length > 64) errors.push({ alias, message: '真实上游目标不能超过 64 个' });
      if (new TextEncoder().encode(route.model).length > 256) errors.push({ alias, message: '逻辑模型不能超过 256 字节' });
      if (route.model && !VALID_EFFORTS.has(route.reasoning_effort)) errors.push({ alias, message: `不支持推理强度 ${route.reasoning_effort}` });
      const seenTargets = new Set();
      route.targets.forEach((target, targetIndex) => {
        const account = accountMap.get(target.account);
        if (!target.account) {
          errors.push({ alias, targetIndex, message: `第 ${targetIndex + 1} 顺位尚未选择连接` });
          return;
        }
        const targetIdentity = `${target.account}\u0000${target.credential}`;
        if (seenTargets.has(targetIdentity)) errors.push({ alias, targetIndex, message: `${target.account}${target.credential ? ` 的账号 ${target.credential}` : ''} 在同一路由中被重复选择` });
        seenTargets.add(targetIdentity);
        if (!account) {
          errors.push({ alias, targetIndex, message: `第 ${targetIndex + 1} 顺位引用了不存在的连接 ${target.account}` });
          return;
        }
        if (new TextEncoder().encode(target.account).length > 128) errors.push({ alias, targetIndex, message: `第 ${targetIndex + 1} 顺位的连接 ID 不能超过 128 字节` });
        if (new TextEncoder().encode(target.credential).length > 128) errors.push({ alias, targetIndex, message: `第 ${targetIndex + 1} 顺位的认证账号 ID 不能超过 128 字节` });
        if (target.credential && String(account.adapter_id || '').toLowerCase() !== 'cli-proxy-api') errors.push({ alias, targetIndex, message: `${account.name || account.id} 不支持认证账号绑定` });
        if (new TextEncoder().encode(target.model).length > 256) errors.push({ alias, targetIndex, message: `第 ${targetIndex + 1} 顺位的模型不能超过 256 字节` });
        if (!route.model && !VALID_EFFORTS.has(target.reasoning_effort)) errors.push({ alias, targetIndex, message: `第 ${targetIndex + 1} 顺位使用了不支持的推理强度 ${target.reasoning_effort}` });
        const resolution = targetResolution(account, route, target);
        if (!resolution.ok) errors.push({ alias, targetIndex, message: `${account.name || account.id}：${resolution.reason}` });
        else if (!resolution.verified) warnings.push({ alias, targetIndex, message: `${account.name || account.id}：${resolution.reason}` });
        if (account.enabled === false) warnings.push({ alias, targetIndex, message: `${account.name || account.id}：连接当前已停用` });
      });
    }
    return { ok: errors.length === 0, errors, warnings };
  }

  function routeImpact(before, after) {
    const changes = [];
    const previous = before || {};
    const next = after || {};
    const aliases = uniqueStrings([...Object.keys(previous), ...Object.keys(next)]).sort();
    for (const alias of aliases) {
      if (!(alias in previous)) changes.push({ alias, kind: 'added', message: `新增 ${alias}` });
      else if (!(alias in next)) changes.push({ alias, kind: 'removed', message: `删除 ${alias}` });
      else if (routeFingerprint(normalizeRoute(previous[alias])) !== routeFingerprint(normalizeRoute(next[alias]))) {
        changes.push({ alias, kind: 'changed', message: `修改 ${alias}` });
      }
    }
    return changes;
  }

  function accountRouteUsage(routes, accountID) {
    const aliases = [];
    for (const [alias, route] of Object.entries(routes || {})) {
      const normalized = normalizeRoute(route);
      if (normalized.all_accounts || normalized.targets.some(target => target.account === accountID)) aliases.push(alias);
    }
    return aliases.sort();
  }

  function serializeRoute(raw) {
    const route = normalizeRoute(raw);
    if (route.legacy) {
      const payload = {};
      if (route.all_accounts) payload.all_accounts = true;
      else payload.accounts = route.targets.map(target => target.account);
      if (route.upstream_model) payload.upstream_model = route.upstream_model;
      if (route.strategy) payload.strategy = route.strategy;
      return payload;
    }
    const payload = {
      targets: route.targets.map(target => route.model
        ? { account: target.account, ...(target.credential ? { credential: target.credential } : {}), model: '' }
        : {
            account: target.account,
            ...(target.credential ? { credential: target.credential } : {}),
            model: target.model,
            ...(target.reasoning_effort ? { reasoning_effort: target.reasoning_effort } : {})
          })
    };
    if (route.strategy) payload.strategy = route.strategy;
    if (route.model) {
      payload.model = route.model;
      payload.reasoning_effort = route.reasoning_effort || 'auto';
    }
    return payload;
  }

  function defaultRouteForAccount(account, preferredModel = '') {
    const capabilities = account?.capabilities || [];
    const capability = preferredModel
      ? capabilities.find(item => item.model === preferredModel)
      : capabilities[0];
    if (capability) {
      const efforts = uniqueStrings(capability.reasoning_efforts);
      return {
        model: capability.model,
        reasoning_effort: efforts.includes('auto') ? 'auto' : efforts[0] || 'auto',
        strategy: '',
        targets: [{ account: account.id, model: '', reasoning_effort: '' }]
      };
    }
    const model = preferredModel || directModels(account)[0] || '';
    return {
      model: '',
      reasoning_effort: 'auto',
      strategy: '',
      targets: [{ account: account?.id || '', model, reasoning_effort: 'auto' }]
    };
  }

  return {
    accountFingerprint,
    accountRouteUsage,
    capabilityResolution,
    channelChatModels,
    channelChatResponse,
    channelChatSupported,
    claudeCodePersistentShellConfig,
    claudeCodePowerShellConfig,
    claudeCodeShellConfig,
    connectionTestRequired,
    defaultRouteForAccount,
    directModels,
    directResolution,
    finiteNumber,
    gatewayAPIBaseFromPath,
    gatewayBaseFromPath,
    hashText,
    logicalModels,
    meteredTokenTotal,
    modelIconKey,
    normalizeRoute,
    normalizeTarget,
    routeFingerprint,
    routeImpact,
    routeValidation,
    serializeRoute,
    powerShellQuote,
    shellQuote,
    stableStringify,
    targetResolution,
    uniqueStrings
  };
});
