package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	webassets "github.com/lms2004/lite2api/internal/web"
)

func (g *Gateway) Run(ctx context.Context) error {
	runContext, cancelRun := context.WithCancel(ctx)
	discoveryDone := make(chan struct{})
	defer func() {
		cancelRun()
		<-discoveryDone
		g.Close()
	}()
	cfg := g.state.Load().cfg
	mux := http.NewServeMux()
	mux.HandleFunc("/health", g.serveHealth)
	mux.HandleFunc("/health/details", g.serveHealth)
	mux.HandleFunc("/livez", g.serveLiveness)
	mux.HandleFunc("/readyz", g.serveReadiness)
	mux.HandleFunc("/admin/api/", g.ServeAdminAPI)
	mux.HandleFunc("/admin", g.serveAdminPage)
	mux.HandleFunc("/admin/", g.serveAdminPage)
	mux.Handle("/v1/", g.routeExecutionProfileHandler(http.HandlerFunc(g.ServeGateway)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		state := g.state.Load()
		if !adminNetworkAllowed(r, state.adminAllowed, state.trustedProxies) {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/admin", http.StatusTemporaryRedirect)
	})
	server := &http.Server{Addr: cfg.Server.Listen, Handler: securityHeaders(recoverer(mux)), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: cfg.Server.RequestReadTimeout.Duration, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("lite2api listening", "address", cfg.Server.Listen)
		errCh <- server.ListenAndServe()
	}()
	go func() {
		defer close(discoveryDone)
		g.runCapabilityDiscovery(runContext)
	}()

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	for {
		select {
		case <-ctx.Done():
			return shutdown(server)
		case err := <-errCh:
			if err == http.ErrServerClosed {
				return nil
			}
			return err
		case sig := <-signals:
			if sig == syscall.SIGHUP {
				if err := g.Reload(); err != nil {
					slog.Error("reload failed", "error", err)
				} else {
					slog.Info("configuration reloaded")
				}
				continue
			}
			return shutdown(server)
		}
	}
}

func (g *Gateway) serveLiveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "lite2api"})
}

func (g *Gateway) serveReadiness(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	state := g.state.Load()
	stats, accounts := g.Stats(), state.scheduler.Snapshot()
	operations := buildOperationsSnapshot(now, state.cfg, accounts, stats)
	const observationFreshness = 5 * time.Minute
	latestAt := time.Time{}
	routeObservations := make(map[string]RequestRecord, len(state.cfg.Routes))
	for _, record := range stats.RouteLatest {
		fingerprint := state.routeFingerprints[record.Model]
		if fingerprint == "" || record.RouteFingerprint != fingerprint || !routeHealthObservation(record) {
			continue
		}
		routeObservations[record.Model] = record
		if observed, err := time.Parse(time.RFC3339Nano, record.Time); err == nil && observed.After(latestAt) {
			latestAt = observed
		}
	}
	staleRoutes := make([]string, 0)
	unobservedRoutes := make([]string, 0)
	failedRoutes := make([]string, 0)
	unavailableRoutes := make([]string, 0)
	for _, route := range operations.Routes {
		if !state.scheduler.RouteAvailable(route.Alias, now) {
			unavailableRoutes = append(unavailableRoutes, route.Alias)
		}
		record, observedRoute := routeObservations[route.Alias]
		if !observedRoute {
			unobservedRoutes = append(unobservedRoutes, route.Alias)
			continue
		}
		observed, err := time.Parse(time.RFC3339Nano, record.Time)
		routeStale := err != nil || now.Sub(observed) > observationFreshness
		routeFailed := !routeStale && !recordSucceeded(record)
		if routeStale {
			staleRoutes = append(staleRoutes, route.Alias)
		}
		if routeFailed {
			failedRoutes = append(failedRoutes, route.Alias)
		}
	}
	stale := len(staleRoutes) > 0
	statusCode := http.StatusOK
	reason := operations.Reason
	status := operations.State
	if len(unavailableRoutes) > 0 {
		statusCode = http.StatusServiceUnavailable
		status = HealthUnavailable
		reason = "one or more routes have no structurally available target"
	}
	if stale && statusCode == http.StatusOK {
		statusCode = http.StatusServiceUnavailable
		status = HealthDegraded
		reason = "no route observation within 5 minutes"
	}
	if len(failedRoutes) > 0 && statusCode == http.StatusOK {
		statusCode = http.StatusServiceUnavailable
		status = HealthDegraded
		reason = "latest route observation failed"
	}
	if len(unobservedRoutes) > 0 && statusCode == http.StatusOK {
		// Cold start is structurally ready. Treating absence of business traffic
		// as failure creates a readiness deadlock in orchestrators that only send
		// traffic to ready instances.
		status = HealthUnknown
		reason = "routes are structurally ready and awaiting first observation"
	}
	observedAt := ""
	if !latestAt.IsZero() {
		observedAt = latestAt.UTC().Format(time.RFC3339Nano)
	}
	writeJSON(w, statusCode, map[string]any{
		"status": status, "reason": reason, "service": "lite2api",
		"observation_stale": stale, "stale_routes": staleRoutes, "unobserved_routes": unobservedRoutes,
		"failed_routes": failedRoutes, "unavailable_routes": unavailableRoutes, "observed_at": observedAt,
	})
}

func shutdown(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func (g *Gateway) serveHealth(w http.ResponseWriter, _ *http.Request) {
	state := g.state.Load()
	stats, accounts := g.Stats(), state.scheduler.Snapshot()
	operations := buildOperationsSnapshot(time.Now(), state.cfg, accounts, stats)
	statusCode := http.StatusOK
	if operations.State == HealthUnavailable {
		statusCode = http.StatusServiceUnavailable
	}
	writeJSON(w, statusCode, map[string]any{
		"liveness": "ok",
		"status":   operations.State,
		"reason":   operations.Reason,
		"service":  "lite2api",
		"accounts": len(state.cfg.Accounts),
		"models":   len(state.models),
		"routes":   len(operations.Routes),
	})
}
func (g *Gateway) serveAdminPage(w http.ResponseWriter, r *http.Request) {
	state := g.state.Load()
	if !adminNetworkAllowed(r, state.adminAllowed, state.trustedProxies) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Accept-Encoding")
	if acceptsEncoding(r.Header.Get("Accept-Encoding"), "gzip") && len(webassets.IndexHTMLGzip) > 0 {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(webassets.IndexHTMLGzip)
		return
	}
	_, _ = w.Write(webassets.IndexHTML)
}

func acceptsEncoding(header, coding string) bool {
	coding = strings.ToLower(strings.TrimSpace(coding))
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), coding) {
			continue
		}
		for _, param := range fields[1:] {
			param = strings.ToLower(strings.TrimSpace(param))
			if strings.HasPrefix(param, "q=") {
				q := strings.TrimSpace(strings.TrimPrefix(param, "q="))
				return q != "0" && q != "0.0" && q != "0.00" && q != "0.000"
			}
		}
		return true
	}
	return false
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				slog.Error("request panic", "error", fmt.Sprint(value), "stack", string(debug.Stack()))
				operation, _ := operationForGatewayPath(r.URL.Path)
				writeProtocolError(w, operation, 500, "internal server error", "gateway_error", "gateway_error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
