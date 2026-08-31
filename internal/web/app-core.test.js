'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const core = require('./app-core.js');

const connection = {
  id: 'primary',
  name: 'Primary',
  models: ['upstream-direct'],
  model_map: { alias: 'upstream-mapped' },
  capabilities: [{ model: 'logical', upstream_model: 'upstream-logical', reasoning_efforts: ['auto', 'high'] }]
};

test('connection test fingerprint changes with secrets and advertised models', () => {
  const base = { type: 'openai', base_url: 'https://example.test/v1', api_key: 'one', headers: { 'X-Private': 'header-secret' }, proxy_url: 'http://user:password@proxy.test', models: ['a'] };
  assert.notEqual(core.accountFingerprint(base), core.accountFingerprint({ ...base, api_key: 'two' }));
  assert.notEqual(core.accountFingerprint(base), core.accountFingerprint({ ...base, models: ['b'] }));
  assert.equal(core.accountFingerprint(base), core.accountFingerprint({ ...base, models: ['a', 'a'] }));
  assert.equal(core.accountFingerprint(base).includes('header-secret'), false);
  assert.equal(core.accountFingerprint(base).includes('password'), false);
});

test('connection save only requires a fresh test when an enabled connection is new or materially changed', () => {
  const input = { currentFingerprint: 'current', originalFingerprint: 'original', testedFingerprint: '' };
  assert.equal(core.connectionTestRequired({ ...input, editing: false, enabled: true }), true);
  assert.equal(core.connectionTestRequired({ ...input, editing: false, enabled: true, testedFingerprint: 'current' }), false);
  assert.equal(core.connectionTestRequired({ ...input, editing: true, enabled: true, currentFingerprint: 'original' }), false);
  assert.equal(core.connectionTestRequired({ ...input, editing: true, enabled: true }), true);
  assert.equal(core.connectionTestRequired({ ...input, editing: true, enabled: false }), false);
});

test('blank model declarations never become an automatic global model catalog', () => {
  assert.deepEqual(core.directModels({ models: [], model_map: {} }), []);
  assert.deepEqual(core.directModels({ models: [], capabilities: connection.capabilities }), []);
  const resolution = core.directResolution({ id: 'unknown', models: [] }, 'manual-model');
  assert.equal(resolution.ok, true);
  assert.equal(resolution.verified, false);
  assert.equal(core.directResolution({ id: 'wildcard', models: ['*'] }, 'manual-model').verified, true);
});

test('channel chat exposes accepted aliases and only chat-capable connections', () => {
  assert.deepEqual(core.channelChatModels(connection), [
    'upstream-direct', 'logical', 'alias', 'upstream-mapped', 'upstream-logical'
  ]);
  assert.equal(core.channelChatSupported({ type: 'openai' }), true);
  assert.equal(core.channelChatSupported({ operations: ['openai.chat', 'openai.embeddings'] }), true);
  assert.equal(core.channelChatSupported({ operations: ['anthropic.messages'] }), true);
  assert.equal(core.channelChatSupported({ operations: ['openai.embeddings'] }), false);
});

test('channel chat normalizes OpenAI and Anthropic text, usage, and finish metadata', () => {
  assert.deepEqual(core.channelChatResponse({
    id: 'chat-1', model: 'model-a',
    choices: [{ message: { content: ' hello ' }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 12, completion_tokens: 4, total_tokens: 16 }
  }), {
    text: 'hello', input_tokens: 12, output_tokens: 4, total_tokens: 16,
    finish_reason: 'stop', response_id: 'chat-1', model: 'model-a'
  });
  assert.deepEqual(core.channelChatResponse({
    id: 'msg-1', content: [{ type: 'text', text: 'first' }, { type: 'text', text: 'second' }],
    stop_reason: 'end_turn', usage: { input_tokens: 8, output_tokens: 3 }
  }), {
    text: 'first\nsecond', input_tokens: 8, output_tokens: 3, total_tokens: 11,
    finish_reason: 'end_turn', response_id: 'msg-1', model: ''
  });
});

test('missing observations stay unknown instead of becoming numeric zero', () => {
  assert.equal(core.finiteNumber(null), null);
  assert.equal(core.finiteNumber(''), null);
  assert.equal(core.finiteNumber('0'), 0);
  assert.equal(core.meteredTokenTotal([{ usage_available: false, total_tokens: 99 }]), null);
  assert.equal(core.meteredTokenTotal([{ usage_available: true, total_tokens: 0 }]), 0);
  assert.equal(core.meteredTokenTotal([{ usage_available: true, total_tokens: 12 }, { usage_available: false, total_tokens: 99 }]), 12);
});

test('official model icon mapping follows effective upstream models and aliases', () => {
  const cases = {
    sol: 'gpt-5-6-sol',
    'gpt-5.6-terra': 'gpt-5-6-terra',
    'antigravity/gpt-5.6-luna': 'gpt-5-6-luna',
    'gpt-5.5': 'gpt-5-5',
    'gpt-5.4': 'gpt-5-4',
    'gpt-5.4-mini': 'gpt-5-4-mini',
    'gpt-5.3-codex-spark': 'gpt-5-3-codex',
    'codex-auto-review': 'openai',
    'gpt-image-2': 'gpt-image-2',
    'antigravity/gpt-oss-120b-medium': 'gpt-oss-120b',
    'claude-code/claude-opus-4-6': 'claude',
    'antigravity/gemini-3.7-flash-high': 'gemini'
  };
  for (const [model, icon] of Object.entries(cases)) assert.equal(core.modelIconKey(model), icon, model);
  assert.equal(core.modelIconKey('private-model-without-official-art'), '');
});

test('logical routing accepts only matching capability and effort', () => {
  const good = core.targetResolution(connection, { model: 'logical', reasoning_effort: 'high' }, { account: 'primary' });
  assert.equal(good.upstream_model, 'upstream-logical');
  const bad = core.targetResolution(connection, { model: 'logical', reasoning_effort: 'max' }, { account: 'primary' });
  assert.equal(bad.ok, false);
});

test('route validation retains incompatible targets and reports them precisely', () => {
  const routes = {
    client: { model: 'logical', reasoning_effort: 'max', targets: [{ account: 'primary' }] }
  };
  const validation = core.routeValidation(routes, [connection]);
  assert.equal(validation.ok, false);
  assert.equal(validation.errors[0].alias, 'client');
  assert.equal(validation.errors[0].targetIndex, 0);
});

test('route validation warns for disabled connections and rejects invalid direct effort', () => {
  const disabled = { ...connection, enabled: false };
  const warning = core.routeValidation({ direct: { targets: [{ account: 'primary', model: 'upstream-direct' }] } }, [disabled]);
  assert.equal(warning.ok, true);
  assert.equal(warning.warnings.some(item => item.message.includes('已停用')), true);
  const invalid = core.routeValidation({ direct: { targets: [{ account: 'primary', model: 'upstream-direct', reasoning_effort: 'impossible' }] } }, [connection]);
  assert.equal(invalid.ok, false);
});

test('route validation rejects duplicate connection targets', () => {
  const validation = core.routeValidation({
    duplicate: { targets: [{ account: 'primary', model: 'upstream-direct' }, { account: 'primary', model: 'upstream-direct' }] }
  }, [connection]);
  assert.equal(validation.ok, false);
  assert.equal(validation.errors.some(item => item.message.includes('重复选择')), true);
});

test('direct routes distinguish declared models from explicit unknown models', () => {
  const declared = core.routeValidation({ direct: { targets: [{ account: 'primary', model: 'upstream-direct' }] } }, [connection]);
  assert.equal(declared.ok, true);
  assert.equal(declared.warnings.length, 0);
  const unsupported = core.routeValidation({ direct: { targets: [{ account: 'primary', model: 'invented' }] } }, [connection]);
  assert.equal(unsupported.ok, false);
  const manual = core.routeValidation({ direct: { targets: [{ account: 'unknown', model: 'manual' }] } }, [{ id: 'unknown', models: [] }]);
  assert.equal(manual.ok, true);
  assert.equal(manual.warnings.length, 1);
});

test('route impact and usage are deterministic', () => {
  const before = { a: { targets: [{ account: 'primary', model: 'm' }] } };
  const after = { a: { targets: [{ account: 'other', model: 'm' }] }, b: { targets: [{ account: 'primary', model: 'm' }] } };
  assert.deepEqual(core.routeImpact(before, after).map(change => change.kind), ['changed', 'added']);
  assert.deepEqual(core.accountRouteUsage(after, 'primary'), ['b']);
});

test('default route chooses capability mode before direct mode', () => {
  const logical = core.defaultRouteForAccount(connection, 'logical');
  assert.equal(logical.model, 'logical');
  assert.equal(logical.targets[0].model, '');
  const direct = core.defaultRouteForAccount({ id: 'direct', models: ['m'] });
  assert.equal(direct.model, '');
  assert.equal(direct.targets[0].model, 'm');
  const explicitDirect = core.defaultRouteForAccount(connection, 'upstream-direct');
  assert.equal(explicitDirect.model, '');
  assert.equal(explicitDirect.targets[0].model, 'upstream-direct');
});

test('route serialization keeps logical and direct modes mutually exclusive', () => {
  const logical = core.serializeRoute({
    model: 'logical',
    reasoning_effort: 'high',
    strategy: 'priority',
    targets: [{ account: 'primary', model: 'stale-direct', reasoning_effort: 'low' }]
  });
  assert.deepEqual(logical, {
    model: 'logical',
    reasoning_effort: 'high',
    strategy: 'priority',
    targets: [{ account: 'primary', model: '' }]
  });
  const direct = core.serializeRoute({
    reasoning_effort: 'auto',
    targets: [{ account: 'primary', model: 'upstream-direct', reasoning_effort: 'low' }]
  });
  assert.deepEqual(direct, {
    targets: [{ account: 'primary', model: 'upstream-direct', reasoning_effort: 'low' }]
  });
});

test('valid legacy routes are preserved instead of silently changing semantics', () => {
  const fixed = { accounts: ['primary'], upstream_model: 'upstream-direct' };
  const normalized = core.normalizeRoute(fixed);
  assert.equal(normalized.legacy, true);
  assert.equal(core.routeValidation({ legacy: fixed }, [connection]).ok, true);
  assert.deepEqual(core.serializeRoute(normalized), fixed);

  const wildcard = { all_accounts: true, strategy: 'round_robin' };
  assert.equal(core.routeValidation({ wildcard }, [connection]).ok, true);
  assert.deepEqual(core.serializeRoute(core.normalizeRoute(wildcard)), wildcard);
  assert.deepEqual(core.accountRouteUsage({ wildcard }, 'primary'), ['wildcard']);
});
