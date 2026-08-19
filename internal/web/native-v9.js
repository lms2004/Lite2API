/* Lite2API Native v9 — small semantic refinements for the reconstructed pages. */
(() => {
  "use strict";

  const BUILD = "Native 9.1 · 2026.08.19";

  // “最近异常” is an incident surface, not a duplicate of “最近请求”. A
  // successful recovery request must not erase the most recent actionable
  // failure from the operator's first screen.
  const baseRenderIncidents = window.renderIncidents;
  if (typeof baseRenderIncidents === "function") {
    window.renderIncidents = function renderNativeV9Incidents(input) {
      let incidents = Array.isArray(input) ? input : [];
      try {
        const recent = typeof state !== "undefined" && Array.isArray(state?.stats?.recent)
          ? state.stats.recent
          : [];
        const failures = recent.filter(record => {
          const status = Number(record?.status);
          return !Number.isFinite(status) || status < 200 || status >= 400;
        });
        if (failures.length) incidents = failures;
      } catch (_) {}
      return baseRenderIncidents(incidents);
    };
  }

  window.Lite2APINativeV9 = Object.freeze({ version: BUILD });
})();
