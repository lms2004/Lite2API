package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

// This uses real loopback HTTP connections on both sides of the gateway. A
// canceled handler alone would not prove that the upstream transport observes
// cancellation or that the same managed key can immediately call again.
func TestHTTPClientCancellationAndTimeoutReleaseAllLeases(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			upstreamCanceled, cleanup := make(chan struct{}), make(chan struct{})
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if upstreamCalls.Add(1) != 1 {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(completedChat))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"started\"}}]}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(upstreamCanceled)
				case <-cleanup:
				}
			}))
			defer upstream.Close()
			defer close(cleanup)
			g := newTestGateway(t, []config.Account{{ID: "source", Type: "openai", BaseURL: upstream.URL, APIKey: "fixture-upstream", Models: []string{"m"}, Operations: []string{config.OperationOpenAIChat}, Enabled: true, Concurrency: 1}}, nil)
			_, secret, err := g.clientKeys.Create(ClientKeyCreate{Name: "cancellation fixture", Models: []string{"m"}, Concurrency: 1})
			if err != nil {
				t.Fatal(err)
			}
			firstFinished := make(chan struct{})
			var gatewayCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				first := gatewayCalls.Add(1) == 1
				g.ServeGateway(w, r)
				if first {
					close(firstFinished)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
			}
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"fixture"}]}`))
			request.Header.Set("Authorization", "Bearer "+secret)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, "started") {
				t.Fatalf("stream did not start: line=%q err=%v", line, err)
			}
			if mode == "cancel" {
				cancel()
			}
			if _, err := io.ReadAll(reader); err == nil {
				t.Fatal("incomplete canceled stream ended successfully")
			}
			for name, done := range map[string]<-chan struct{}{"upstream context": upstreamCanceled, "gateway handler": firstFinished} {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatalf("%s did not finish after client %s", name, mode)
				}
			}
			if g.globalActive.Load() != 0 || g.bodyBytes.Load() != 0 || g.Accounts()[0].Active != 0 || g.clientKeys.List()[0].Active != 0 || g.Stats().Active != 0 {
				t.Fatalf("leases not released: global=%d body_bytes=%d account=%d key=%d stats=%d", g.globalActive.Load(), g.bodyBytes.Load(), g.Accounts()[0].Active, g.clientKeys.List()[0].Active, g.Stats().Active)
			}
			if recent := g.Stats().Recent; len(recent) != 1 || recent[0].Outcome != "client_cancelled" || recent[0].AttemptCount != 1 {
				t.Fatalf("cancellation outcome/attempts=%+v", recent)
			}
			followup, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"fixture"}]}`))
			followup.Header.Set("Authorization", "Bearer "+secret)
			response, err = server.Client().Do(followup)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK || validateBufferedCompletion(config.OperationOpenAIChat, "/v1/chat/completions", body) != nil || upstreamCalls.Load() != 2 {
				t.Fatalf("same key could not complete after cancellation: status=%d calls=%d err=%v", response.StatusCode, upstreamCalls.Load(), err)
			}
		})
	}
}
