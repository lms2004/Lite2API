/* Transport and refresh lifecycle. No application state or DOM dependencies. */
(function (root, factory) {
  const api = Object.freeze(factory());
  if (typeof module === 'object' && module.exports) module.exports = api;
  root.Lite2APIRuntime = api;
})(globalThis, function () {
  'use strict';

  function createRequestClient({ base, csrf, unauthorized, fetch: fetcher = globalThis.fetch }) {
    const pending = new Map();

    async function send(path, options) {
      const { timeout = 20000, ...init } = options;
      const method = (init.method || 'GET').toUpperCase();
      const headers = new Headers(init.headers || {});
      if (init.body !== undefined && !headers.has('Content-Type'))
        headers.set('Content-Type', 'application/json');
      if (!['GET', 'HEAD'].includes(method) && csrf()) headers.set('X-CSRF-Token', csrf());
      const controller = new AbortController();
      const abort = () => controller.abort();
      const external = init.signal;
      const timer = setTimeout(abort, timeout);
      if (external?.aborted) abort();
      else external?.addEventListener('abort', abort, { once: true });
      try {
        const response = await fetcher(base + path, {
          credentials: 'same-origin',
          ...init,
          method,
          headers,
          signal: controller.signal,
        });
        const text = await response.text();
        let data;
        try {
          data = text ? JSON.parse(text) : {};
        } catch {
          data = { raw: text };
        }
        if (!response.ok) {
          if (response.status === 401) unauthorized();
          const error = new Error(
            data.error?.message || data.message || response.statusText || `HTTP ${response.status}`,
          );
          error.status = response.status;
          error.payload = data;
          throw error;
        }
        return data;
      } catch (error) {
        if (controller.signal.aborted) {
          const aborted = new Error(
            external?.aborted ? '请求已取消' : `请求超时（${Math.round(timeout / 1000)} 秒）`,
          );
          aborted.name = external?.aborted ? 'AbortError' : 'TimeoutError';
          throw aborted;
        }
        throw error;
      } finally {
        clearTimeout(timer);
        external?.removeEventListener('abort', abort);
      }
    }

    return function request(path, options = {}) {
      // A caller-owned signal must never cancel another caller's request.
      const merge = (!options.method || options.method === 'GET') && !options.signal && !options.headers;
      if (!merge) return send(path, options);
      const key = path + ':' + (options.timeout ?? 20000);
      if (pending.has(key)) return pending.get(key);
      const task = send(path, options).finally(() => pending.delete(key));
      pending.set(key, task);
      return task;
    };
  }

  function createRefreshLoop({
    key,
    load,
    commit,
    failed,
    paused,
    interval,
    busy = () => {},
    setTimer = setTimeout,
    clearTimer = clearTimeout,
  }) {
    let timer = null;
    let current = null;
    let generation = 0;
    function pause() {
      clearTimer(timer);
      timer = null;
      generation++;
      current?.controller.abort();
      current = null;
      busy(false);
    }
    function refresh({ force = false, silent = true } = {}) {
      if (paused()) {
        pause();
        return Promise.resolve();
      }
      const requestedKey = key();
      if (current?.key === requestedKey && !force) return current.promise;
      pause();
      const version = generation;
      const controller = new AbortController();
      const active = () => version === generation && !controller.signal.aborted && key() === requestedKey;
      const task = { key: requestedKey, controller, promise: null };
      current = task;
      busy(true);
      task.promise = Promise.resolve()
        .then(() => load({ key: requestedKey, signal: controller.signal, active, force }))
        .then((result) => {
          if (active()) commit(result, { silent });
        })
        .catch((error) => {
          if (active() && error.name !== 'AbortError') failed(error);
        })
        .finally(() => {
          if (current !== task) return;
          current = null;
          busy(false);
          if (!paused()) timer = setTimer(() => refresh(), interval());
        });
      return task.promise;
    }
    return Object.freeze({ refresh, pause });
  }

  function debounce(callback, delay = 120) {
    let timer;
    const debounced = (...args) => {
      clearTimeout(timer);
      timer = setTimeout(() => callback(...args), delay);
    };
    debounced.cancel = () => clearTimeout(timer);
    return debounced;
  }

  return { createRequestClient, createRefreshLoop, debounce };
});
