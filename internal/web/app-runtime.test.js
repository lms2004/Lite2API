'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { createRequestClient, createRefreshLoop } = require('./app-runtime.js');
const { createIndex, createRouteUsageIndex } = require('./app-metrics.js');
const Core = require('./app-core.js');

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
const response = (data, status = 200) => ({
  ok: status < 400,
  status,
  text: async () => JSON.stringify(data),
});

test('GET requests share in-flight work and mutations retain their CSRF boundary', async () => {
  const calls = [];
  const gate = deferred();
  const request = createRequestClient({
    base: '/api',
    csrf: () => 'csrf-test',
    unauthorized: () => {},
    fetch: (path, init) => {
      calls.push({ path, init });
      return gate.promise;
    },
  });
  const first = request('/state');
  const second = request('/state');
  assert.equal(first, second);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].init.headers.has('X-CSRF-Token'), false);
  gate.resolve(response({ version: 1 }));
  assert.deepEqual(await first, { version: 1 });
  await request('/routes', { method: 'POST', body: '{}' });
  assert.equal(calls[1].init.headers.get('X-CSRF-Token'), 'csrf-test');
  await request('/state');
  assert.equal(calls.length, 3, 'completed GET results are not a stale response cache');
});

test('caller cancellation is isolated and abort listeners are removed after completion', async () => {
  const controller = new AbortController();
  let removed = 0;
  const remove = controller.signal.removeEventListener.bind(controller.signal);
  controller.signal.removeEventListener = (...args) => {
    removed++;
    remove(...args);
  };
  const request = createRequestClient({
    base: '/api',
    csrf: () => '',
    unauthorized: () => {},
    fetch: (_path, init) =>
      new Promise((resolve, reject) => {
        if (init.signal.aborted) reject(new DOMException('Aborted', 'AbortError'));
        else init.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
      }),
  });
  const task = request('/state', { signal: controller.signal });
  controller.abort();
  await assert.rejects(task, { name: 'AbortError', message: '请求已取消' });
  assert.equal(removed, 1);
});

test('401 invalidates the session once and exposes a structured error', async () => {
  let invalidated = 0;
  const request = createRequestClient({
    base: '/api',
    csrf: () => '',
    unauthorized: () => invalidated++,
    fetch: async () => response({ error: { message: 'expired' } }, 401),
  });
  await assert.rejects(request('/state'), { status: 401, message: 'expired' });
  assert.equal(invalidated, 1);
});

test('navigation and range changes cancel old refreshes and discard late results', async () => {
  let key = 'usage:24h';
  const requests = [];
  const committed = [];
  const timers = new Map();
  let timerID = 0;
  const loop = createRefreshLoop({
    key: () => key,
    paused: () => false,
    interval: () => 10000,
    load: (options) => {
      const gate = deferred();
      requests.push({ ...gate, ...options });
      return gate.promise;
    },
    commit: (value) => committed.push(value),
    failed: (error) => {
      throw error;
    },
    setTimer: (fn) => {
      timers.set(++timerID, fn);
      return timerID;
    },
    clearTimer: (id) => timers.delete(id),
  });
  const first = loop.refresh();
  await Promise.resolve();
  assert.equal(first, loop.refresh());
  key = 'usage:7d';
  const second = loop.refresh();
  await Promise.resolve();
  assert.equal(requests[0].signal.aborted, true);
  assert.equal(requests[0].active(), false);
  requests[1].resolve('7d');
  await second;
  requests[0].resolve('24h');
  await first;
  assert.deepEqual(committed, ['7d']);
  assert.equal(timers.size, 1);
  loop.pause();
  assert.equal(timers.size, 0);
});

test('hidden or unauthenticated views stop polling, including an in-flight refresh', async () => {
  let hidden = false;
  const gate = deferred();
  let loads = 0,
    commits = 0;
  const loop = createRefreshLoop({
    key: () => 'accounts',
    paused: () => hidden,
    interval: () => 15000,
    load: () => {
      loads++;
      return gate.promise;
    },
    commit: () => commits++,
    failed: () => {},
    setTimer: () => {
      throw new Error('paused loop scheduled work');
    },
    clearTimer: () => {},
  });
  const task = loop.refresh();
  await Promise.resolve();
  hidden = true;
  await loop.refresh();
  gate.resolve({});
  await task;
  assert.equal(loads, 1);
  assert.equal(commits, 0);
});

test('forced refresh cannot be satisfied by a pre-mutation response', async () => {
  const requests = [];
  const committed = [];
  const loop = createRefreshLoop({
    key: () => 'routes',
    paused: () => false,
    interval: () => 10000,
    load: () => {
      const gate = deferred();
      requests.push(gate);
      return gate.promise;
    },
    commit: (data) => committed.push(data),
    failed: () => {},
    setTimer: () => 1,
    clearTimer: () => {},
  });
  const old = loop.refresh();
  await Promise.resolve();
  const fresh = loop.refresh({ force: true });
  await Promise.resolve();
  requests[0].resolve('before-save');
  requests[1].resolve('after-save');
  await Promise.all([old, fresh]);
  assert.deepEqual(committed, ['after-save']);
  loop.pause();
});

test('indexed metrics preserve unknown latency, account isolation, and snapshot invalidation', () => {
  const index = createIndex();
  const state = {
    config: { accounts: [{ id: 'a', name: 'A' }, { id: 'b' }, { id: 'empty' }] },
    accounts: [],
    stats: {
      recent: [
        { account_id: 'a', status: 200, latency_ms: 100 },
        { account_id: 'b', status: 429, latency_ms: 20 },
        { account_id: 'a', status: 500, latency_ms: 300 },
        { account_id: 'empty', status: 200 },
      ],
    },
  };
  const first = index(state);
  assert.equal(index(state), first);
  assert.deepEqual(
    first.connections.map((item) => [item.id, item.samples, item.failures, item.avg, item.p95]),
    [
      ['a', 2, 1, 200, 300],
      ['b', 1, 1, 20, 20],
      ['empty', 1, 0, Infinity, null],
    ],
  );
  assert.equal(first.configured.get('a').name, 'A');
  const next = index({ ...state, stats: { recent: [] } });
  assert.notEqual(next, first);
  assert.equal(next.connections[0].samples, 0);
});

test('route usage indexes retain wildcard and legacy semantics without duplicate references', () => {
  const index = createRouteUsageIndex(Core.normalizeRoute);
  const routes = {
    wildcard: { all_accounts: true },
    legacy: { accounts: ['a', 'b'] },
    pinned: {
      targets: [
        { account: 'a', credential_id: 'first' },
        { account: 'a', credential_id: 'second' },
      ],
    },
  };
  const lookup = index(routes);
  for (const id of ['a', 'b', 'unknown']) assert.deepEqual(lookup(id), Core.accountRouteUsage(routes, id));
  assert.equal(index(routes), lookup);
  assert.deepEqual(index({ pinned: { targets: [{ account: 'b' }] } })('a'), []);
});
