/* Shared, side-effect-free contracts for the embedded admin console.
   Keep this file usable from both the browser and Node's built-in test runner. */
(function installAdminCore(root, factory) {
  'use strict';
  const core = Object.freeze(factory());
  if (typeof module === 'object' && module.exports) module.exports = core;
  if (root) root.Lite2APIAdminCore = core;
})(typeof globalThis === 'undefined' ? this : globalThis, function createAdminCore() {
  'use strict';

  const encoder = new TextEncoder();

  function utf8Size(value) {
    return encoder.encode(String(value ?? '')).byteLength;
  }

  function reconcileSelection(selection, accounts) {
    const valid = new Set((accounts || []).map(account => String(account?.id ?? '')).filter(Boolean));
    return new Set(Array.from(selection || [], id => String(id)).filter(id => valid.has(id)));
  }

  function routeAccountIDs(route) {
    const targets = Array.isArray(route?.targets)
      ? route.targets.map(target => String(target?.account ?? '')).filter(Boolean)
      : [];
    const accounts = Array.isArray(route?.accounts)
      ? route.accounts.map(account => String(account ?? '')).filter(Boolean)
      : [];
    return [...new Set([...targets, ...accounts])];
  }

  function accountDeleteImpact(routes, accountID) {
    const id = String(accountID ?? '');
    return Object.entries(routes || {}).flatMap(([alias, route]) => {
      const accounts = routeAccountIDs(route);
      if (!accounts.includes(id)) return [];
      const remaining = accounts.filter(candidate => candidate !== id);
      return [{ alias, remaining: remaining.length, becomesEmpty: remaining.length === 0 }];
    });
  }

  function promptRequest(accountID, model, messages, temperature, maxTokens) {
    const payload = {
      account_id: String(accountID ?? ''),
      model: String(model ?? ''),
      messages: (messages || []).map(message => ({
        role: String(message?.role ?? ''),
        content: String(message?.content ?? '')
      })),
      temperature: Number(temperature),
      max_tokens: Number(maxTokens)
    };
    const body = JSON.stringify(payload);
    return { payload, body, bytes: utf8Size(body), messageCount: payload.messages.length };
  }

  function promptBudgetStatus(request, limits = {}) {
    const maxMessages = Number(limits.maxMessages) || 64;
    const maxBytes = Number(limits.maxBytes) || 256 * 1024;
    return {
      ok: request.messageCount <= maxMessages && request.bytes <= maxBytes,
      maxMessages,
      maxBytes,
      messagesRemaining: Math.max(0, maxMessages - request.messageCount),
      bytesRemaining: Math.max(0, maxBytes - request.bytes)
    };
  }

  function importRequest(data, mode, dryRun) {
    const payload = { data, mode: String(mode || 'skip'), dry_run: Boolean(dryRun) };
    const body = JSON.stringify(payload);
    return { payload, body, bytes: utf8Size(body) };
  }

  return {
    utf8Size,
    reconcileSelection,
    routeAccountIDs,
    accountDeleteImpact,
    promptRequest,
    promptBudgetStatus,
    importRequest
  };
});
