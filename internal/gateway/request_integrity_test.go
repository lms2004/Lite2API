package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestAmbiguousRequestFieldsNeverReachUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(completedChat))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	for _, body := range []string{
		`{"model":"m","model":"other"}`,
		`{"model":"m","n":2,"n":1}`,
		`{"model":"m","stream":true,"stream":false}`,
		`{"model":"m","mo\u0064el":"other"}`,
	} {
		w := httptest.NewRecorder()
		g.ServeGateway(w, gatewayRequest(body))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "duplicate_request_field") {
			t.Fatalf("ambiguous request accepted: status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatal("an ambiguous request reached upstream")
	}
}

func TestLogicalRequestIDCorrelatesClientAndObservationWithoutSpoofing(t *testing.T) {
	var forwardedID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(logicalRequestIDHeader) != "" {
			t.Error("client logical identity leaked upstream")
		}
		forwardedID = r.Header.Get("X-Request-Id")
		w.Header().Set(logicalRequestIDHeader, "provider-spoofed")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completedChat))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	r := gatewayRequest(`{"model":"m","messages":[]}`)
	r.Header.Set(logicalRequestIDHeader, "client-spoofed")
	w := httptest.NewRecorder()
	g.ServeGateway(w, r)
	id := w.Header().Get(logicalRequestIDHeader)
	if w.Code != 200 || id == "" || id != forwardedID || id != g.Stats().Recent[0].RequestID || id == "provider-spoofed" || id == "client-spoofed" {
		t.Fatalf("client cannot correlate its logical call: status=%d id=%q", w.Code, id)
	}
}
