'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const core = require('./admin-core.js');

test('utf8Size follows the server byte contract', () => {
  assert.equal(core.utf8Size('abc'), 3);
  assert.equal(core.utf8Size('凭据'), 6);
});

test('accountDeleteImpact covers target and legacy route shapes', () => {
  const impact = core.accountDeleteImpact({
    primary: { targets: [{ account: 'a' }, { account: 'b' }] },
    only: { accounts: ['a'] },
    mixed: { targets: [], accounts: ['a', 'c'] },
    unrelated: { targets: [{ account: 'c' }] }
  }, 'a');
  assert.deepEqual(impact, [
    { alias: 'primary', remaining: 1, becomesEmpty: false },
    { alias: 'only', remaining: 0, becomesEmpty: true },
    { alias: 'mixed', remaining: 1, becomesEmpty: false }
  ]);
});

test('reconcileSelection removes deleted account ids', () => {
  assert.deepEqual(
    [...core.reconcileSelection(new Set(['live', 'deleted']), [{ id: 'live' }, { id: 'other' }])],
    ['live']
  );
});

test('promptRequest measures the final JSON body and message budget', () => {
  const request = core.promptRequest('account', 'model', [{ role: 'user', content: '你好' }], 0, 8);
  assert.equal(request.bytes, core.utf8Size(request.body));
  assert.equal(core.promptBudgetStatus(request).ok, true);
  const oversized = core.promptRequest('account', 'model', Array.from({ length: 65 }, () => ({ role: 'user', content: 'x' })), 0, 8);
  assert.equal(core.promptBudgetStatus(oversized).ok, false);
});

test('importRequest counts wrapper bytes, not source file bytes', () => {
  const data = { type: 'lite2api-data', version: 1, accounts: [{ id: 'a' }] };
  const request = core.importRequest(data, 'skip', false);
  assert.equal(request.bytes, core.utf8Size(request.body));
  assert.ok(request.bytes > core.utf8Size(JSON.stringify(data)));
});
