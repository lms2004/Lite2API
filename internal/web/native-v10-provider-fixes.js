/* Native v10 provider contract fixes.
   A method labelled "API Key" must open with the API-key field visible. */
(() => {
  'use strict';

  function applyProviderDefaults() {
    if (typeof accountTemplates !== 'object') return;
    for (const name of ['deepseek', 'xai', 'openai', 'anthropic', 'gemini']) {
      if (accountTemplates[name]) accountTemplates[name].credential = 'key';
    }
    if (accountTemplates['kimi-api']) accountTemplates['kimi-api'].credential = 'key';
  }

  function init() {
    applyProviderDefaults();
    const original = window.selectAccountTemplate;
    if (typeof original === 'function' && !original.__nativeV10ProviderDefaults) {
      const wrapped = function (...args) {
        applyProviderDefaults();
        return original.apply(this, args);
      };
      Object.defineProperty(wrapped, '__nativeV10ProviderDefaults', { value: true });
      window.selectAccountTemplate = wrapped;
    }
  }

  window.Lite2APINativeV10ProviderDefaults = Object.freeze({ apply: applyProviderDefaults });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init, { once: true });
  else init();
})();
