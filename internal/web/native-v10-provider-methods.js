/* Native v10 provider-specific import methods.
   Lite2API already imports the useful Sub2API account subset and forwards
   OAuth/setup-token credential bundles to the isolated CLIProxy auth pool.
   Surface that capability where the operator expects it: inside the provider
   onboarding branch rather than only as a generic side card. */
(() => {
  'use strict';

  function importMethod(title, detail) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'v10-method-card v10-provider-import-method';
    button.innerHTML = `<span class="v10-method-icon"><svg class="icon"><use href="#i-upload"></use></svg></span><span><strong></strong><small></small></span>`;
    button.querySelector('strong').textContent = title;
    button.querySelector('small').textContent = detail;
    button.addEventListener('click', () => window.v10OpenImportFromOnboarding?.());
    return button;
  }

  function augment(provider) {
    const grid = document.getElementById('v10MethodGrid');
    if (!grid) return;
    grid.querySelectorAll('.v10-provider-import-method').forEach(node => node.remove());
    if (provider === 'anthropic') {
      grid.append(importMethod('Setup Token / 账号文件', '导入 Claude Setup Token、OAuth 或 Sub2API v1 账号文件'));
    } else if (provider === 'codex') {
      grid.append(importMethod('OAuth 账号文件', '导入 Codex OAuth 或 Sub2API v1 账号文件'));
    } else if (provider === 'gemini' || provider === 'antigravity') {
      grid.append(importMethod('Google 账号文件', '导入 Gemini / Antigravity OAuth 或 Sub2API v1 账号文件'));
    }
  }

  function install() {
    const original = window.v10SelectOnboardingProvider;
    if (typeof original !== 'function' || original.__nativeV10ProviderMethods) return;
    const wrapped = function (provider) {
      const result = original.apply(this, arguments);
      augment(provider);
      return result;
    };
    Object.defineProperty(wrapped, '__nativeV10ProviderMethods', { value: true });
    window.v10SelectOnboardingProvider = wrapped;
    const active = document.querySelector('[data-v10-provider].active')?.dataset.v10Provider || 'codex';
    augment(active);
  }

  window.Lite2APINativeV10ProviderMethods = Object.freeze({ augment });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', install, { once: true });
  else install();
})();
