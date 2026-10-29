package gateway

import "net/http"

// Maintenance probes perform real inference too. They share the same bounded
// gateway admission as client calls; administrator permission is not a second
// inference-capacity pool.
func (g *Gateway) admitMaintenanceCall(w http.ResponseWriter, state *runtimeState) (func(), bool) {
	if !g.tryAcquireGlobal(int64(state.cfg.Server.MaxInFlightRequests)) {
		w.Header().Set("Retry-After", "1")
		writeAPIErrorCode(w, http.StatusTooManyRequests, "gateway capacity is in use; retry verification after active calls complete", "rate_limit_error", "maintenance_capacity_exhausted")
		return nil, false
	}
	return func() { g.globalActive.Add(-1) }, true
}
