package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	webassets "github.com/lms2004/lite2api/internal/web"
)

func (g *Gateway) Run(ctx context.Context) error {
	cfg := g.state.Load().cfg
	mux := http.NewServeMux()
	mux.HandleFunc("/health", g.serveHealth)
	mux.HandleFunc("/admin/api/", g.ServeAdminAPI)
	mux.HandleFunc("/admin", g.serveAdminPage)
	mux.HandleFunc("/admin/", g.serveAdminPage)
	mux.HandleFunc("/v1/", g.ServeGateway)
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
	go g.runCapabilityDiscovery(ctx)

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
		"models":   len(state.scheduler.Models()),
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
	_, _ = w.Write(webassets.IndexHTML)
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
				writeAPIError(w, 500, "internal server error", "gateway_error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
