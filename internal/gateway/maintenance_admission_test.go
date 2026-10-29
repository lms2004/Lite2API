package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestMaintenanceInferenceSharesClientAdmissionAndReleasesIt(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completedChat))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Concurrency: 2, Weight: 1}}, nil)
	cfg := g.Config()
	cfg.Server.MaxInFlightRequests = 1
	if err := config.NewStore(g.configPath).Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeGateway(httptest.NewRecorder(), gatewayRequest(`{"model":"m","messages":[]}`))
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		t.Fatal("client request did not start")
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	for _, probe := range []string{"capability", "prompt"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/admin/api/accounts/verify", strings.NewReader(`{"account_id":"a","model":"m","messages":[{"role":"user","content":"hello"}]}`))
		if probe == "capability" {
			r = httptest.NewRequest(http.MethodPost, "/admin/api/accounts/verify", strings.NewReader(`{"account_id":"a","model":"m","operation":"openai.chat","feature":"call"}`))
			g.serveCapabilityVerification(w, r, g.state.Load())
		} else {
			g.servePromptTest(w, r, g.state.Load())
		}
		if w.Code != 429 || !strings.Contains(w.Body.String(), "maintenance_capacity_exhausted") || calls.Load() != 1 || g.globalActive.Load() != 1 {
			t.Fatalf("maintenance bypassed active client capacity: %s %d %s", probe, w.Code, w.Body.String())
		}
	}
	close(release)
	<-done
	w := httptest.NewRecorder()
	g.serveCapabilityVerification(w, httptest.NewRequest(http.MethodPost, "/admin/api/accounts/verify", strings.NewReader(`{"account_id":"a","model":"m","operation":"openai.chat","feature":"call"}`)), g.state.Load())
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || calls.Load() != 2 || g.globalActive.Load() != 0 {
		t.Fatalf("verification failed after capacity release: %d %s", w.Code, w.Body.String())
	}
}
