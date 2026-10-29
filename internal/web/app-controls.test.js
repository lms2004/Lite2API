'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const Core = require('./app-core.js');
require('./app-onboarding.js');
require('./app-clients.js');
require('./app-chat.js');

function nodes() {
  const values = new Map(), handlers = new Map();
  const $ = (id) => {
    if (!values.has(id)) values.set(id, {
      id, value: '', textContent: '', innerHTML: '', hidden: false, className: '', disabled: false, options: [],
      addEventListener: (kind, callback) => handlers.set(id + ':' + kind, callback),
      setAttribute() {}, focus() {}, querySelector: () => ({ textContent: '', disabled: false }),
      classList: { add() {}, remove() {}, toggle() {} },
    });
    return values.get(id);
  };
  return { $, handlers, setHTML: (node, html) => { node.innerHTML = html; node.options = [...html.matchAll(/<option value="([^"]*)"/g)].map((match) => ({ value: match[1] })); } };
}

function clientFixture(data, client, key = '') {
  const dom = nodes(), clientsState = { client, configMode: 'shell' };
  dom.$('createdSecretValue').value = key;
  const controller = globalThis.Lite2APIClients({
    ...dom, UI: {}, resourcesState: { data, keys: [] }, clientsState, all: () => [],
    now: () => 0, escapeHTML: (value) => String(value).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;'),
    formatTime: String, number: String,
  });
  controller.bind();
  controller.renderClients();
  return { ...dom, clientsState, controller };
}

function parsedPython(program, input) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'lite2api-config-parse-'));
  try {
    const file = path.join(directory, 'generated');
    fs.writeFileSync(file, input, { mode: 0o600 });
    const result = spawnSync('python3', ['-c', 'import sys,pathlib\ntext=pathlib.Path(sys.argv[1]).read_text()\n' + program, file], { encoding: 'utf8', timeout: 5000 });
    assert.equal(result.status, 0, result.stderr || result.error?.message);
    return JSON.parse(result.stdout);
  } finally { fs.rmSync(directory, { recursive: true, force: true }); }
}

test('client selection excludes impossible interfaces and fixed Messages effort while keeping unknown attempts', () => {
  const previousLocation = globalThis.location;
  globalThis.location = { origin: 'https://gateway.invalid', pathname: '/admin' };
  try {
    const data = { models: ['high', 'messages', 'unknown', 'blocked', 'responses-only'], config: {
      accounts: [
        { id: 'shared', type: 'openai', enabled: true, operations: ['openai.chat', 'openai.responses', 'anthropic.messages'], models: ['upstream'],
          capabilities: [{ model: 'logical', upstream_model: 'upstream', reasoning_efforts: ['auto', 'high'] }] },
        { id: 'anthropic', type: 'anthropic', enabled: true, models: ['claude'] },
        { id: 'unknown', type: 'openai', enabled: true, operations: ['openai.responses'], models: [] },
        { id: 'blocked', type: 'openai', enabled: true, models: ['blocked'], capabilities: [{ model: 'blocked', upstream_model: 'blocked',
          reasoning_efforts: ['auto'], features: { 'openai.responses:call': { status: 'unsupported' } } }] },
      ], routes: {
        high: { model: 'logical', reasoning_effort: 'high', targets: [{ account: 'shared' }] },
        messages: { targets: [{ account: 'anthropic', model: 'claude' }] },
        unknown: { targets: [{ account: 'unknown', model: 'manual' }] },
        blocked: { targets: [{ account: 'blocked', model: 'blocked' }] },
        'responses-only': { targets: [{ account: 'unknown', model: 'manual' }] },
      },
    } };
    const ui = clientFixture(data, 'codex', 'fixture');
    assert.equal(ui.$('clientModel').value, 'high');
    assert.deepEqual(ui.$('clientModel').options.map((item) => item.value), ['high', 'unknown', 'responses-only']);
    assert.match(ui.$('clientModel').innerHTML, /可尝试，客户端待验收/);
    const toml = ui.$('clientConfig').textContent.split('\n\n# ~/.codex/config.toml\n')[1];
    const parsed = parsedPython('import json,tomllib; print(json.dumps(tomllib.loads(text)))', toml);
    assert.equal(parsed.model_reasoning_effort, 'high');
    const button = { dataset: { client: 'claude' } };
    ui.handlers.get('clientChooser:click')({ target: { closest: () => button } });
    assert.deepEqual(ui.$('clientModel').options.map((item) => item.value), ['messages']);
    assert.equal(ui.$('clientModel').value, 'messages');
    assert.equal(ui.$('copyClientConfig').disabled, false);
    const unavailable = clientFixture({ models: ['messages'], config: { accounts: data.config.accounts, routes: { messages: data.config.routes.messages } } }, 'codex');
    assert.equal(unavailable.$('clientModel').value, '');
    assert.equal(unavailable.$('copyClientConfig').disabled, true);
  } finally { globalThis.location = previousLocation; }
});

test('generated curl argv, Python values and Codex TOML preserve quotes as data', () => {
  const previousLocation = globalThis.location;
  const origin = 'https://gateway.invalid/q\'"\\$(printf injected)';
  const model = '模型\'"\\\n$(printf injected);#z';
  const key = 'fixture\'"\\$(printf injected)#key';
  globalThis.location = { origin, pathname: '/lite-admin' };
  try {
    const data = { models: [model], config: { routes: {} } };
    const apiBase = Core.gatewayAPIBaseFromPath(origin, '/lite-admin');
    const curl = clientFixture(data, 'curl', key).$('clientConfig').textContent;
    const shell = 'curl() { python3 -c \'import json,sys; print(json.dumps(sys.argv[1:]))\' "$@"; }\n' + curl;
    const captured = spawnSync('bash', ['-c', shell], { encoding: 'utf8', timeout: 5000 });
    assert.equal(captured.status, 0, captured.stderr);
    const argv = JSON.parse(captured.stdout);
    assert.equal(argv[1], apiBase + '/chat/completions');
    assert.equal(argv[argv.indexOf('-H') + 1], 'Authorization: Bearer ' + key);
    assert.equal(JSON.parse(argv[argv.indexOf('-d') + 1]).model, model);
    const python = clientFixture(data, 'openai', key).$('clientConfig').textContent;
    const observed = parsedPython(`import contextlib,io,json,sys,types
seen={}
class Client:
 def __init__(self,**kwargs): seen['client']=kwargs; self.responses=self
 def create(self,**kwargs): seen['request']=kwargs; return types.SimpleNamespace(output_text='fixture')
sys.modules['openai']=types.SimpleNamespace(OpenAI=Client)
with contextlib.redirect_stdout(io.StringIO()): exec(text,{})
print(json.dumps(seen))`, python);
    assert.equal(observed.client.base_url, apiBase);
    assert.equal(observed.client.api_key, key);
    assert.equal(observed.request.model, model);
    const codex = clientFixture(data, 'codex', key).$('clientConfig').textContent;
    const [exportKey, toml] = codex.split('\n\n# ~/.codex/config.toml\n');
    const exported = spawnSync('bash', ['-c', exportKey + '\npython3 -c \'import json,os; print(json.dumps(os.environ["LITE2API_API_KEY"]))\''], { encoding: 'utf8', timeout: 5000 });
    assert.equal(exported.status, 0, exported.stderr);
    assert.equal(JSON.parse(exported.stdout), key);
    const parsed = parsedPython('import json,tomllib; print(json.dumps(tomllib.loads(text)))', toml);
    assert.equal(parsed.model, model);
    assert.equal(parsed.model_providers.lite2api.base_url, apiBase);
    assert.equal(parsed.model_reasoning_effort, undefined);
  } finally { globalThis.location = previousLocation; }
});

test('partial imports retain item failures and require a new review before retry', async () => {
  const dom = nodes(), closed = [], toasts = [];
  const onboardingState = {
    importRevision: 'reviewed', importData: { accounts: [{ id: 'a' }, { id: 'b' }] },
  };
  let refreshed = 0;
  const controller = globalThis.Lite2APIOnboarding({
    ...dom, Core, onboardingState, resourcesState: { data: { config_revision: 'reviewed', config: { accounts: [] } } },
    usageState: {}, UI: {}, all: () => [], escapeHTML: String, number: String,
    request: async () => ({ account_created: 1, account_failed: 1, errors: [{ index: 1, message: 'invalid connection' }] }),
    refreshAll: async () => { refreshed++; }, closeDialog: (id) => closed.push(id),
    toast: (...args) => toasts.push(args),
  });
  controller.bind();
  await dom.handlers.get('applyImport:click')();
  assert.equal(refreshed, 1);
  assert.deepEqual(closed, []);
  assert.match(dom.$('importPreviewRows').innerHTML, /invalid connection/);
  assert.equal(dom.$('applyImport').disabled, true);
  assert.equal(onboardingState.importRevision, '');
  assert.match(toasts.at(-1)[0], /失败项/);
});

test('failed catalog probes block enabled saves while explicit disabled registration remains possible', async () => {
  for (const enabled of [true, false]) {
    const dom = nodes(), calls = [], toasts = [];
    const fields = {
      id: 'channel', name: 'Channel', base_url: 'https://example.invalid/v1', type: 'openai',
      api_key: 'fixture-key', api_key_env: '', auth_header: 'Authorization', auth_scheme: 'Bearer',
      headers: '{}', headers_env: '{}', models: 'm', model_map: '{}', priority: '0', weight: '1',
      concurrency: '1', enabled: String(enabled), proxy_url: '',
    };
    dom.$('manualAccountForm').elements = Object.fromEntries(Object.entries(fields).map(([key, value]) => [key, { value }]));
    dom.$('saveManualAccount').textContent = 'Save';
    const onboardingState = { manualTestGeneration: 1, manualTestFingerprint: '', manualOriginalFingerprint: '', manualTemplate: 'custom', manualConfigRevision: 'review-a' };
    const controller = globalThis.Lite2APIOnboarding({
      ...dom, Core, onboardingState, resourcesState: { data: { config_revision: 'review-a' } }, usageState: { quality: new Map() },
      UI: {}, all: () => [], one: () => ({ value: 'key' }), accountFingerprint: Core.accountFingerprint,
      configuredAccount: () => null, hasRouteChanges: () => false,
      request: async (path, options) => { calls.push({ path, options }); return path === '/accounts/test' ? { ok: false, status: 401 } : { ok: true }; },
      toast: (...args) => toasts.push(args), number: String, escapeHTML: String, saveQuality() {}, closeDialog() {},
      refreshAll: async () => {}, showView() {}, setAccountTab() {},
    });
    controller.bind();
    await dom.handlers.get('manualAccountForm:submit')({ preventDefault() {} });
    assert.deepEqual(calls.map((call) => call.path), enabled ? ['/accounts/test'] : ['/accounts']);
    if (enabled) {
      assert.equal(dom.$('connectionTestResult').className, 'test-result bad');
      assert.equal(onboardingState.manualTestFingerprint, '');
    } else assert.equal(calls[0].options.headers['If-Match'], '"review-a"');
  }
});

test('client management displays actual model scope, disabled status and the compatibility exception', () => {
  const dom = nodes();
  const previousLocation = globalThis.location;
  globalThis.location = { origin: 'https://gateway.invalid', pathname: '/admin' };
  try {
    const controller = globalThis.Lite2APIClients({
      ...dom, UI: {}, resourcesState: {
        data: { models: ['m'], config: { routes: {} }, access_policy: { legacy: { count: 2 } } },
        keys: [{ id: 'key', name: 'Tool', prefix: 'sk-fixture', models: ['m'], enabled: false, rpm: 3, concurrency: 1, total_requests: 2, active: 1 }],
      }, clientsState: { client: 'curl', configMode: 'shell' }, all: () => [], now: () => Date.parse('2026-10-28T00:00:00Z'),
      escapeHTML: String, formatTime: String, number: String,
    });
    controller.renderClients();
    const rendered = dom.$('clientKeyRows').innerHTML;
    assert.match(rendered, /模型 m/);
    assert.match(rendered, /已停用/);
    assert.match(rendered, /在途 1/);
    assert.match(rendered, /2 个配置\/环境兼容凭据/);
    assert.match(rendered, /没有单 Key 限额/);
    assert.match(rendered, /后续请求/);
  } finally { globalThis.location = previousLocation; }
});

test('the existing source dialog verifies the selected operation and feature and shows the scoped outcome', async () => {
  const previousFrame = globalThis.requestAnimationFrame;
  globalThis.requestAnimationFrame = (callback) => callback();
  try {
    for (const feature of ['tools', 'image_input']) for (const passed of [true, false]) {
      const dom = nodes(), calls = [];
      const source = { id: 'source', name: 'Source', enabled: true, operations: ['openai.responses'], models: ['m'] };
      const chatState = { channelChatMessages: [], channelChatGeneration: 0, channelChatPending: false };
      const controller = globalThis.Lite2APIChat({
        ...dom, Core, chatState, resourcesState: { data: { config: { accounts: [source] } }, oauth: [] },
        configuredAccount: () => source, number: String, escapeHTML: String,
        request: async (path, options) => {
          calls.push({ path, input: JSON.parse(options.body) });
          return { ok: passed, completed: passed, evidence_saved: true, operation: 'openai.responses', feature,
            upstream_model: 'm', reasoning_effort: 'auto', observed_at: '2026-10-28T00:00:00Z', request_id: 'fixture-request', message: passed ? `${feature} completed` : `${feature} did not complete` };
        },
      });
      controller.bind();
      for (const [id, value] of Object.entries({ channelChatAccount: 'source', channelChatModel: 'm', channelVerifyOperation: 'openai.responses', channelVerifyFeature: feature, channelVerifyEffort: 'auto', channelVerifyCredential: '' })) dom.$(id).value = value;
      await dom.handlers.get('verifyChannelCapability:click')();
      assert.equal(calls[0].path, '/accounts/verify');
      assert.equal(calls[0].input.operation, 'openai.responses');
      assert.equal(calls[0].input.feature, feature);
      assert.equal(chatState.channelChatMessages[0].role, passed ? 'assistant' : 'error');
      assert.match(dom.$('channelChatMessages').innerHTML, /openai.responses/);
      assert.match(dom.$('channelChatMessages').innerHTML, new RegExp(feature));
      assert.match(dom.$('channelChatMessages').innerHTML, passed ? /验收依据已保存/ : /失败依据已保存/);
      assert.equal(dom.$('sendChannelChat').disabled, true, 'a Responses-only source must use verification instead of the Chat sender');
    }
  } finally { globalThis.requestAnimationFrame = previousFrame; }
});
