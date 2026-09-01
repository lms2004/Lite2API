package gateway

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func newTestGateway(t testing.TB, accounts []config.Account, routes map[string]config.Route) *Gateway {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.APIKeys = []string{"gateway-secret"}
	cfg.Server.AdminToken = "admin-secret"
	cfg.Server.AllowPrivateHTTPUpstream = true
	cfg.Server.QueueTimeout = config.Duration{}
	cfg.Accounts = accounts
	cfg.Routes = routes
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.NewStore(path).Save(cfg); err != nil {
		t.Fatal(err)
	}
	g, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func BenchmarkGatewayParallel(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer upstream.Close()
	g := newTestGateway(b, []config.Account{{ID: "bench", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 0, Weight: 1, Enabled: true}}, nil)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			g.ServeGateway(w, gatewayRequest(`{"model":"m","messages":[{"role":"user","content":"ping"}]}`))
			if w.Code != http.StatusOK {
				b.Fatalf("status=%d", w.Code)
			}
		}
	})
}

func gatewayRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer gateway-secret")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestBuildUpstreamURLCompatibilityRoots(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "OpenAI v1 root", base: "https://api.example.com/v1", want: "https://api.example.com/v1/chat/completions?trace=1"},
		{name: "Gemini OpenAI root", base: "https://generativelanguage.googleapis.com/v1beta/openai", want: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions?trace=1"},
		{name: "custom prefix keeps v1", base: "https://api.example.com/proxy", want: "https://api.example.com/proxy/v1/chat/completions?trace=1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildUpstreamURL(test.base, "/v1/chat/completions", "trace=1")
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("URL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildUpstreamURLPreservesConfiguredQuery(t *testing.T) {
	got, err := buildUpstreamURL("https://azure.example/openai?api-version=2026-01-01&fixed=yes", "/v1/chat/completions", "api-version=attacker&trace=1")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("api-version") != "2026-01-01" || query.Get("fixed") != "yes" || query.Get("trace") != "1" {
		t.Fatalf("merged query=%v", query)
	}
}

func TestGatewayRejectsMalformedQueryBeforeSelectingUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "main", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "secret",
		Models: []string{"m"}, Enabled: true, Weight: 1,
	}}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?bad=a;b", strings.NewReader(`{"model":"m"}`))
	request.Header.Set("Authorization", "Bearer gateway-secret")
	w := httptest.NewRecorder()
	g.ServeGateway(w, request)
	if w.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	account := g.Accounts()[0]
	if account.Total != 0 || account.Failures != 0 || account.CircuitOpenUntil != "" {
		t.Fatalf("malformed client query polluted upstream health: %+v", account)
	}
}

func TestGatewayRewritesModelAndAuthenticates(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Errorf("auth=%q", got)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "real-model" {
			t.Errorf("model=%v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "main", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret", Models: []string{"alias"}, ModelMap: map[string]string{"alias": "real-model"}, Concurrency: 2, Weight: 1, Enabled: true}}, nil)
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"alias","messages":[]}`))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	bad := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alias"}`))
	badW := httptest.NewRecorder()
	g.ServeGateway(badW, bad)
	if badW.Code != 401 {
		t.Fatalf("unauthorized status=%d", badW.Code)
	}
}

func TestGatewayRecordsUsageAndModalities(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16,"prompt_tokens_details":{"cached_tokens":3}},"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "main", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true}}, nil)
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	recent := g.Stats().Recent
	if len(recent) != 1 {
		t.Fatalf("recent=%+v", recent)
	}
	got := recent[0]
	if got.InputType != "text+image" || got.OutputType != "text" || got.InputTokens != 12 || got.OutputTokens != 4 || got.CacheRate != 25 {
		t.Fatalf("record=%+v", got)
	}
}

func TestGatewayFailsOverAfterDefinitiveRejection(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer second.Close()
	accounts := []config.Account{{ID: "first", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"m"}, Priority: 0, Concurrency: 1, Weight: 1, Enabled: true}, {ID: "second", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"m"}, Priority: 1, Concurrency: 1, Weight: 1, Enabled: true}}
	g := newTestGateway(t, accounts, map[string]config.Route{"m": {Accounts: []string{"first", "second"}, Strategy: "priority"}})
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"m","input":"hello"}`))
	request.Header.Set("Authorization", "Bearer gateway-secret")
	g.ServeGateway(w, request)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("calls=%d,%d", firstCalls.Load(), secondCalls.Load())
	}
	if g.Stats().Failovers != 1 {
		t.Fatalf("failovers=%d", g.Stats().Failovers)
	}
	recent := g.Stats().Recent
	if len(recent) != 1 || recent[0].Outcome != "success" || recent[0].Error != "" || recent[0].Status != http.StatusOK {
		t.Fatalf("successful failover record is inconsistent: %+v", recent)
	}
}

func TestGatewayDoesNotReplayGenerativePostAfterAmbiguousStatus(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		http.Error(w, "generation may have completed", http.StatusInternalServerError)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer second.Close()
	accounts := []config.Account{
		{ID: "first", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"m"}, Priority: 0, Concurrency: 1, Weight: 1, Enabled: true},
		{ID: "second", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"m"}, Priority: 1, Concurrency: 1, Weight: 1, Enabled: true},
	}
	g := newTestGateway(t, accounts, map[string]config.Route{"m": {Accounts: []string{"first", "second"}, Strategy: "priority"}})
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","messages":[]}`))
	if w.Code != http.StatusBadGateway || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("status=%d calls=%d,%d body=%s", w.Code, firstCalls.Load(), secondCalls.Load(), w.Body.String())
	}
	recent := g.Stats().Recent
	if len(recent) != 1 || recent[0].Outcome != "uncertain_submission" || !strings.Contains(recent[0].Error, "submission outcome uncertain") {
		t.Fatalf("ambiguous generation outcome not recorded: %+v", recent)
	}
}

func TestGatewayFailsOverGenerativePostOnDefinitivePaymentRejection(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		http.Error(w, "subscription exhausted", http.StatusPaymentRequired)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer second.Close()
	accounts := []config.Account{
		{ID: "first", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
		{ID: "second", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
	}
	g := newTestGateway(t, accounts, map[string]config.Route{"m": {Targets: []config.RouteTarget{{Account: "first", Model: "m"}, {Account: "second", Model: "m"}}}})
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","messages":[]}`))
	if w.Code != http.StatusOK || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("status=%d calls=%d,%d body=%s", w.Code, firstCalls.Load(), secondCalls.Load(), w.Body.String())
	}
}

func TestRetryableStatusIncludesProviderOverloadAndRejectsClientErrors(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusNotImplemented, 529, 599} {
		if !retryableStatus(code) {
			t.Errorf("status %d should allow failover", code)
		}
	}
	for _, code := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity} {
		if retryableStatus(code) {
			t.Errorf("status %d should not allow generic failover", code)
		}
	}
}

func TestRetryableStatusPolicyProtectsGenerativeOperations(t *testing.T) {
	for _, code := range []int{http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusInternalServerError, 529} {
		if retry, uncertain := retryableStatusForOperation(config.OperationOpenAIChat, code); retry || !uncertain {
			t.Errorf("generative status %d policy retry=%v uncertain=%v", code, retry, uncertain)
		}
		if retry, uncertain := retryableStatusForOperation(config.OperationEmbeddings, code); retry || !uncertain {
			t.Errorf("embedding status %d policy retry=%v uncertain=%v", code, retry, uncertain)
		}
	}
	for _, code := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests} {
		if retry, uncertain := retryableStatusForOperation(config.OperationOpenAIChat, code); !retry || uncertain {
			t.Fatalf("definitive rejection %d policy retry=%v uncertain=%v", code, retry, uncertain)
		}
	}
}

func TestTransportRetryRequiresProofRequestWasNotWritten(t *testing.T) {
	transportErr := errors.New("connection failed")
	if !retryableTransportError(&upstreamRequestError{err: transportErr, wroteRequest: false}) {
		t.Fatal("pre-write transport failure should permit failover")
	}
	if retryableTransportError(&upstreamRequestError{err: transportErr, wroteRequest: true}) {
		t.Fatal("post-write transport failure has uncertain submission outcome")
	}
}

func TestUpstreamErrorMessageRedactsConfiguredQueryAndUserInfo(t *testing.T) {
	err := &upstreamRequestError{err: &url.Error{
		Op: "Post", URL: "https://user:password@example.com/v1?api-key=private-query", Err: errors.New("connection reset"),
	}, wroteRequest: true}
	message := upstreamErrorMessage(err)
	if strings.Contains(message, "password") || strings.Contains(message, "private-query") || !strings.Contains(message, "example.com/v1") {
		t.Fatalf("unsafe upstream error message: %q", message)
	}
}

func TestGatewayPreservesActionableResponseWhenFinalFailoverTransportFails(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "90")
		writeAPIError(w, http.StatusTooManyRequests, "all credentials cooling down", "rate_limit_error")
	}))
	defer first.Close()
	accounts := []config.Account{
		{ID: "limited", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
		{ID: "offline", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
	}
	g := newTestGateway(t, accounts, map[string]config.Route{"m": {Targets: []config.RouteTarget{{Account: "limited", Model: "m"}, {Account: "offline", Model: "m"}}}})
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"m","input":"hello"}`))
	request.Header.Set("Authorization", "Bearer gateway-secret")
	g.ServeGateway(w, request)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "90" || !strings.Contains(w.Body.String(), "all credentials cooling down") {
		t.Fatalf("status=%d retry-after=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
	recent := g.Stats().Recent
	if len(recent) != 1 || recent[0].AccountID != "limited" || !strings.Contains(recent[0].Error, "final failover failed") {
		t.Fatalf("recent=%+v", recent)
	}
}

func TestGatewayDoesNotReplayEarlierStatusAfterUncertainSubmission(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.Header().Set("X-Private-Canary", "first-response")
		http.Error(w, "first-response-canary", http.StatusTooManyRequests)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = connection.Close()
	}))
	defer second.Close()
	accounts := []config.Account{
		{ID: "limited", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
		{ID: "uncertain", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
	}
	g := newTestGateway(t, accounts, map[string]config.Route{"m": {Targets: []config.RouteTarget{{Account: "limited", Model: "m"}, {Account: "uncertain", Model: "m"}}}})
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"m","input":"hello"}`))
	request.Header.Set("Authorization", "Bearer gateway-secret")
	g.ServeGateway(w, request)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "outcome is uncertain") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") != "" || w.Header().Get("X-Private-Canary") != "" || strings.Contains(w.Body.String(), "first-response-canary") {
		t.Fatalf("earlier response was replayed: headers=%v body=%s", w.Header(), w.Body.String())
	}
	recent := g.Stats().Recent
	if len(recent) != 1 || recent[0].Outcome != "uncertain_submission" || recent[0].AccountID != "uncertain" {
		t.Fatalf("uncertain submission record=%+v", recent)
	}
}

func TestCooldownForSupportsRetryAfterDateAndCapsExtremeValues(t *testing.T) {
	retryAt := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	response := &http.Response{Header: http.Header{"Retry-After": []string{retryAt.Format(http.TimeFormat)}}}
	got := cooldownFor(response, 30*time.Second)
	if got < 110*time.Second || got > 2*time.Minute {
		t.Fatalf("HTTP-date Retry-After cooldown=%v", got)
	}

	response.Header.Set("Retry-After", "999999")
	if got = cooldownFor(response, 30*time.Second); got != 24*time.Hour {
		t.Fatalf("extreme Retry-After cooldown=%v, want 24h", got)
	}

	response.Header.Set("Retry-After", "9223372036854775807")
	if got = cooldownFor(response, 30*time.Second); got != 24*time.Hour {
		t.Fatalf("overflow-sized Retry-After cooldown=%v, want 24h", got)
	}

	response.Header.Set("Retry-After", "invalid")
	if got = cooldownFor(response, 30*time.Second); got != 30*time.Second {
		t.Fatalf("invalid Retry-After cooldown=%v, want fallback", got)
	}
}

func TestGatewayDoesNotReplayUpstreamErrorAcrossRequests(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "17")
		w.Header().Set("X-Provider-Request-Id", "private-canary-header")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"private-canary-body","type":"rate_limit_error","code":"upstream_quota"}}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "main", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true}}, nil)
	cfg := g.Config()
	cfg.Server.FailureThreshold = 1
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}

	first := httptest.NewRecorder()
	g.ServeGateway(first, gatewayRequest(`{"model":"m","messages":[]}`))
	if first.Code != http.StatusTooManyRequests || !strings.Contains(first.Body.String(), "private-canary-body") {
		t.Fatalf("first response status=%d body=%s", first.Code, first.Body.String())
	}

	started := time.Now()
	second := httptest.NewRecorder()
	g.ServeGateway(second, gatewayRequest(`{"model":"m","messages":[]}`))
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("second request waited for queue timeout: %v", elapsed)
	}
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second response status=%d body=%s, want sanitized 503", second.Code, second.Body.String())
	}
	if strings.Contains(second.Body.String(), "private-canary") || second.Header().Get("X-Provider-Request-Id") != "" || second.Header().Get("Retry-After") == "17" {
		t.Fatalf("second response replayed upstream data: headers=%v body=%s", second.Header(), second.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls=%d, want open circuit without replay", got)
	}
}

type trackedBody struct {
	reader io.Reader
	reads  atomic.Int64
}

type repeatingByteReader byte

func (r repeatingByteReader) Read(data []byte) (int, error) {
	for index := range data {
		data[index] = byte(r)
	}
	return len(data), nil
}

func (b *trackedBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.reader.Read(p)
}
func (*trackedBody) Close() error { return nil }

func TestAdmissionRejectsManagedKeyConcurrencyBeforeReadingBody(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	_, secret, err := g.clientKeys.Create(ClientKeyCreate{Name: "bounded", Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	held, failure := g.clientKeys.Authenticate(secret, nil)
	if failure != "" {
		t.Fatal(failure)
	}
	defer held.Complete(false)
	body := &trackedBody{reader: strings.NewReader(`{"model":"m","messages":[]}`)}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Body = body
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	g.routeExecutionProfileHandler(http.HandlerFunc(g.ServeGateway)).ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if reads := body.reads.Load(); reads != 0 {
		t.Fatalf("body read %d times before concurrency admission", reads)
	}
}

func TestBodyMemoryBudgetRejectsBeforeReadingBody(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	budget := requestBodyMemoryBudget(g.Config().Server.MaxBodyBytes)
	g.bodyBytes.Store(budget)
	defer g.bodyBytes.Store(0)
	body := &trackedBody{reader: strings.NewReader(`{"model":"m","messages":[]}`)}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Body = body
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer gateway-secret")
	w := httptest.NewRecorder()
	g.ServeGateway(w, req)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "body_memory_limit_reached") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if reads := body.reads.Load(); reads != 0 {
		t.Fatalf("body read %d times after memory admission failed", reads)
	}
}

func TestTinyChunkedBodyLeaseIsReleasedBeforeLongStream(t *testing.T) {
	streamStarted := make(chan struct{})
	releaseStream := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-streamStarted:
		default:
			close(streamStarted)
		}
		<-releaseStream
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "stream", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test",
		Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true,
	}}, nil)
	firstRequest := gatewayRequest(`{"model":"m","stream":true}`)
	firstRequest.ContentLength = -1
	firstRequest.TransferEncoding = []string{"chunked"}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		g.ServeGateway(httptest.NewRecorder(), firstRequest)
	}()
	select {
	case <-streamStarted:
	case <-time.After(time.Second):
		t.Fatal("stream did not start")
	}
	deadline := time.Now().Add(time.Second)
	for g.bodyBytes.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if retained := g.bodyBytes.Load(); retained != 0 {
		close(releaseStream)
		<-firstDone
		t.Fatalf("tiny chunked upload retained %d body bytes for SSE lifetime", retained)
	}
	secondBody := &trackedBody{reader: strings.NewReader(`{"model":"m"}`)}
	secondRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", secondBody)
	secondRequest.ContentLength = -1
	secondRequest.TransferEncoding = []string{"chunked"}
	secondRequest.Header.Set("Authorization", "Bearer gateway-secret")
	secondResponse := httptest.NewRecorder()
	g.ServeGateway(secondResponse, secondRequest)
	if secondResponse.Code == http.StatusTooManyRequests && strings.Contains(secondResponse.Body.String(), "body_memory_limit_reached") {
		t.Fatalf("tiny chunked stream monopolized body budget: %s", secondResponse.Body.String())
	}
	if secondBody.reads.Load() == 0 {
		t.Fatal("second chunked body was rejected before incremental accounting")
	}
	close(releaseStream)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("stream did not finish")
	}
}

func TestQueuedLargeBodyRemainsChargedUntilUpstreamSubmission(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "queued", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test",
		Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true,
	}}, nil)
	cfg := g.Config()
	cfg.Server.QueueTimeout = config.Duration{Duration: 2 * time.Second}
	cfg.Server.MaxBodyBytes = 8 << 20
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	state := g.state.Load()
	held, err := state.scheduler.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	budget := requestBodyMemoryBudget(state.cfg.Server.MaxBodyBytes)
	baseline := budget - (6 << 20)
	g.bodyBytes.Store(baseline)
	defer g.bodyBytes.Store(0)
	firstPayload := `{"model":"m","padding":"` + strings.Repeat("a", 2<<20) + `"}`
	firstResponse := httptest.NewRecorder()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		g.ServeGateway(firstResponse, gatewayRequest(firstPayload))
	}()
	deadline := time.Now().Add(time.Second)
	for g.bodyBytes.Load() < baseline+int64(len(firstPayload))*2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if retained := g.bodyBytes.Load(); retained < baseline+int64(len(firstPayload))*2 {
		t.Fatalf("queued request released its retained body budget: got=%d baseline=%d", retained, baseline)
	}

	secondPayload := `{"model":"m","padding":"` + strings.Repeat("b", 3<<20) + `"}`
	secondBody := &trackedBody{reader: strings.NewReader(secondPayload)}
	secondRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	secondRequest.Body = secondBody
	secondRequest.ContentLength = int64(len(secondPayload))
	secondRequest.Header.Set("Authorization", "Bearer gateway-secret")
	secondResponse := httptest.NewRecorder()
	g.ServeGateway(secondResponse, secondRequest)
	if secondResponse.Code != http.StatusTooManyRequests || !strings.Contains(secondResponse.Body.String(), "body_memory_limit_reached") {
		t.Fatalf("second retained body escaped budget: status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	if secondBody.reads.Load() != 0 {
		t.Fatalf("known oversized reservation read body %d times", secondBody.reads.Load())
	}
	held.Release()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("queued request did not resume after capacity release")
	}
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("queued request status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	if retained := g.bodyBytes.Load(); retained != baseline {
		t.Fatalf("completed request retained body bytes: got=%d want baseline=%d", retained, baseline)
	}
}

func TestLegalFortyMiBRequestIsNotRejectedByAmplificationAccounting(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "large", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test",
		Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true,
	}}, nil)
	const requestSize = int64(40 << 20)
	prefix := `{"model":"m"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Body = io.NopCloser(io.MultiReader(
		strings.NewReader(prefix),
		io.LimitReader(repeatingByteReader(' '), requestSize-int64(len(prefix))),
	))
	request.ContentLength = requestSize
	request.Header.Set("Authorization", "Bearer gateway-secret")
	response := httptest.NewRecorder()
	g.ServeGateway(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("legal 40MiB request was rejected: status=%d body=%s", response.Code, response.Body.String())
	}
	if retained := g.bodyBytes.Load(); retained != 0 {
		t.Fatalf("large completed request retained %d body bytes", retained)
	}
}

func TestNearLimitFastAliasCoversBothInternalRewrites(t *testing.T) {
	const maxBody = int64(22 << 20)
	budget := requestBodyMemoryBudget(maxBody)
	if want := 3*maxBody + 2*int64(maxInternalRewriteGrowth); budget != want {
		t.Fatalf("combined rewrite budget=%d want=%d", budget, want)
	}
	g := &Gateway{}
	lease := &bodyByteLease{gateway: g, limit: budget}
	if !lease.Acquire(maxBody) || !lease.Acquire(maxBody) {
		t.Fatal("legal inbound and RawMessage copies did not fit")
	}
	internal := rewriteBodyReservation(maxBody)
	if !lease.Acquire(internal) {
		t.Fatal("fast-profile normalization reservation did not fit")
	}
	// Normalization adds at least service_tier, then releases its old source.
	lease.ShrinkTo(2*maxBody + 1)
	if !lease.Acquire(internal) {
		t.Fatal("near-limit fast alias could not reserve its provider-model rewrite")
	}
	lease.Release()
	if retained := g.bodyBytes.Load(); retained != 0 {
		t.Fatalf("combined rewrite accounting retained %d bytes", retained)
	}
}

func TestRewrittenAttemptBodyIsIncludedInMemoryLease(t *testing.T) {
	var g *Gateway
	var observed atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ := io.ReadAll(r.Body)
		if r.ContentLength != int64(len(upstreamBody)) || len(r.TransferEncoding) != 0 {
			t.Errorf("upstream framing content_length=%d body=%d transfer_encoding=%v", r.ContentLength, len(upstreamBody), r.TransferEncoding)
		}
		observed.Store(g.bodyBytes.Load())
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	account := config.Account{
		ID: "rewrite", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test",
		Models: []string{"real"}, Concurrency: 1, Weight: 1, Enabled: true,
		Capabilities: []config.ChannelCapability{{Model: "real", UpstreamModel: "real", ReasoningEfforts: []string{"auto"}}},
	}
	g = newTestGateway(t, []config.Account{account}, map[string]config.Route{
		"alias": {Model: "real", Targets: []config.RouteTarget{{Account: "rewrite"}}},
	})
	payload := `{"model":"alias","padding":"` + strings.Repeat("x", 1<<20) + `"}`
	response := httptest.NewRecorder()
	g.ServeGateway(response, gatewayRequest(payload))
	if response.Code != http.StatusOK {
		t.Fatalf("rewritten request status=%d body=%s", response.Code, response.Body.String())
	}
	if got, wantMinimum := observed.Load(), int64(len(payload))*3-(64<<10); got < wantMinimum {
		t.Fatalf("rewritten attempt body was not charged: observed=%d want_at_least=%d", got, wantMinimum)
	}
	if retained := g.bodyBytes.Load(); retained != 0 {
		t.Fatalf("rewritten completed request retained %d body bytes", retained)
	}
}

func TestUpstreamRequestWriteIsBoundedAndReleasesBodyLease(t *testing.T) {
	const upstreamWriteTimeout = 50 * time.Millisecond
	g := newTestGateway(t, []config.Account{{
		ID: "blocked", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "test",
		Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true,
	}}, nil)
	cfg := g.Config()
	cfg.Server.MaxBodyBytes = 8 << 20
	cfg.Server.ResponseHeaderTimeout = config.Duration{Duration: upstreamWriteTimeout}
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	transport := &blockingUploadRoundTripper{started: make(chan struct{})}
	g.state.Load().clients["blocked"] = &http.Client{Transport: transport}
	request := gatewayRequest(`{"model":"m","padding":"small"}`)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeGateway(response, request)
	}()
	select {
	case <-transport.started:
	case <-time.After(100 * upstreamWriteTimeout):
		t.Fatal("request did not start its controlled upstream upload")
	}
	select {
	case <-done:
	case <-time.After(100 * upstreamWriteTimeout):
		t.Fatal("blocked upstream upload did not obey its pre-header deadline")
	}
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "uncertain_submission") {
		t.Fatalf("blocked upstream upload status=%d body=%s", response.Code, response.Body.String())
	}
	if retained := g.bodyBytes.Load(); retained != 0 {
		t.Fatalf("blocked upload retained %d request bytes", retained)
	}
	if active := g.Accounts()[0].Active; active != 0 {
		t.Fatalf("blocked upload retained account lease: %d", active)
	}
}

func TestTransportErrorRetainsAttemptLeaseUntilAsynchronousBodyClose(t *testing.T) {
	account := config.Account{
		ID: "delayed", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "test",
		Models: []string{"real"}, Concurrency: 1, Weight: 1, Enabled: true,
		Capabilities: []config.ChannelCapability{{Model: "real", UpstreamModel: "real", ReasoningEfforts: []string{"auto"}}},
	}
	g := newTestGateway(t, []config.Account{account}, map[string]config.Route{
		"alias": {Model: "real", Targets: []config.RouteTarget{{Account: "delayed"}}},
	})
	transport := &delayedCloseRoundTripper{returned: make(chan struct{}), release: make(chan struct{})}
	g.state.Load().clients["delayed"] = &http.Client{Transport: transport}
	payload := `{"model":"alias","padding":"` + strings.Repeat("x", 1<<20) + `"}`
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeGateway(response, gatewayRequest(payload))
	}()
	select {
	case <-transport.returned:
	case <-time.After(time.Second):
		t.Fatal("custom transport did not return its error")
	}
	if retained, minimum := g.bodyBytes.Load(), int64(len(payload))*3-(64<<10); retained < minimum {
		close(transport.release)
		<-done
		t.Fatalf("attempt lease dropped before transport Body.Close: retained=%d want_at_least=%d", retained, minimum)
	}
	select {
	case <-done:
		close(transport.release)
		t.Fatal("handler returned before transport relinquished request body ownership")
	default:
	}
	close(transport.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not resume after asynchronous Body.Close")
	}
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "uncertain_submission") {
		t.Fatalf("delayed-close transport status=%d body=%s", response.Code, response.Body.String())
	}
	if retained := g.bodyBytes.Load(); retained != 0 {
		t.Fatalf("delayed-close transport retained %d request bytes after close", retained)
	}
}

func TestGatewayAppliesFastProfileAfterAdmission(t *testing.T) {
	var serviceTier string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		serviceTier, _ = body["service_tier"].(string)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "fast", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"sol@fast"}, Enabled: true, Weight: 1,
		Capabilities: []config.ChannelCapability{{Model: "sol@fast", UpstreamModel: "sol@fast", ReasoningEfforts: []string{"auto"}}},
	}}, map[string]config.Route{
		"coding-fast": {Model: "sol@fast", Targets: []config.RouteTarget{{Account: "fast"}}},
	})
	w := httptest.NewRecorder()
	g.routeExecutionProfileHandler(http.HandlerFunc(g.ServeGateway)).ServeHTTP(w, gatewayRequest(`{"model":"coding-fast","messages":[]}`))
	if w.Code != http.StatusOK || serviceTier != "priority" {
		t.Fatalf("status=%d service_tier=%q body=%s", w.Code, serviceTier, w.Body.String())
	}
}

func TestGatewayRejectsOversizedModelBeforeObservation(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"`+strings.Repeat("m", maxGatewayModelBytes+1)+`","messages":[]}`))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "model_too_long") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if recent := g.Stats().Recent; len(recent) != 0 {
		t.Fatalf("oversized model reached observation ring: %+v", recent)
	}
}

func TestTopLevelEnvelopeBudgetIsEnforcedBeforeMapAllocation(t *testing.T) {
	var body strings.Builder
	body.WriteString(`{"model":"m"`)
	for index := 0; index < maxRequestEnvelopeFields; index++ {
		fmt.Fprintf(&body, `,"field_%d":%d`, index, index)
	}
	body.WriteByte('}')
	data := []byte(body.String())
	if err := validateTopLevelEnvelope(data); !errors.Is(err, errTooManyRequestFields) {
		t.Fatalf("preflight error=%v, want too many fields", err)
	}
	if allocations := testing.AllocsPerRun(100, func() { _ = validateTopLevelEnvelope(data) }); allocations > 0 {
		t.Fatalf("over-cardinality preflight allocated %.1f objects per run", allocations)
	}
	for _, valid := range [][]byte{
		[]byte(`{"model":"m","nested":{"quoted":"} ] \\\"","items":[1,{"ok":true}]}}`),
		[]byte(`{"escaped\\\"key":1}`),
	} {
		if err := validateTopLevelEnvelope(valid); err != nil {
			t.Fatalf("valid nested/escaped envelope rejected: %v body=%s", err, valid)
		}
	}
	escapedKey := []byte(`{"` + strings.Repeat(`\u0061`, 100) + `":1}`)
	if err := validateTopLevelEnvelope(escapedKey); err != nil {
		t.Fatalf("escaped key within decoded limit was rejected: %v", err)
	}
	var escapedEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(escapedKey, &escapedEnvelope); err != nil || len(escapedEnvelope) != 1 {
		t.Fatalf("escaped key fixture invalid: err=%v envelope=%v", err, escapedEnvelope)
	}
	g := newTestGateway(t, nil, nil)
	response := httptest.NewRecorder()
	g.ServeGateway(response, gatewayRequest(string(data)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "too_many_request_fields") {
		t.Fatalf("over-cardinality request status=%d body=%s", response.Code, response.Body.String())
	}
	if retained := g.bodyBytes.Load(); retained != 0 {
		t.Fatalf("rejected field bomb retained %d request bytes", retained)
	}
}

func TestOversizedSessionKeyIsRejectedWithoutDecodedCopy(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	payload := `{"model":"m","user":"` + strings.Repeat("u", 1<<20) + `"}`
	response := httptest.NewRecorder()
	g.ServeGateway(response, gatewayRequest(payload))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "session_key_too_long") {
		t.Fatalf("oversized session key status=%d body=%s", response.Code, response.Body.String())
	}
	if retained := g.bodyBytes.Load(); retained != 0 {
		t.Fatalf("oversized session key retained %d request bytes", retained)
	}
}

type slowResponseWriter struct {
	header  http.Header
	delay   time.Duration
	writing atomic.Bool
	mu      sync.Mutex
	body    []byte
}

type failingResponseWriter struct {
	header      http.Header
	deadlineSet atomic.Bool
}

type testTimeoutError struct{}

func (testTimeoutError) Error() string   { return "downstream write deadline exceeded" }
func (testTimeoutError) Timeout() bool   { return true }
func (testTimeoutError) Temporary() bool { return true }

type deadlineBlockingWriter struct {
	header       http.Header
	mu           sync.Mutex
	deadline     time.Time
	deadlineSet  atomic.Bool
	observedBody atomic.Int64
	bodyBytes    *atomic.Int64
}

type delayedCloseRoundTripper struct {
	returned chan struct{}
	release  chan struct{}
}

type blockingUploadRoundTripper struct {
	started chan struct{}
}

func (t *blockingUploadRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if trace := httptrace.ContextClientTrace(request.Context()); trace != nil && trace.WroteHeaders != nil {
		trace.WroteHeaders()
	}
	close(t.started)
	<-request.Context().Done()
	_ = request.Body.Close()
	return nil, request.Context().Err()
}

func (t *delayedCloseRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if trace := httptrace.ContextClientTrace(request.Context()); trace != nil && trace.WroteHeaders != nil {
		trace.WroteHeaders()
	}
	close(t.returned)
	go func() {
		<-t.release
		_ = request.Body.Close()
	}()
	return nil, errors.New("transport failed after accepting request")
}

func (w *deadlineBlockingWriter) Header() http.Header { return w.header }
func (*deadlineBlockingWriter) WriteHeader(int)       {}
func (w *deadlineBlockingWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	if !deadline.IsZero() {
		w.deadlineSet.Store(true)
	}
	return nil
}
func (w *deadlineBlockingWriter) Write([]byte) (int, error) {
	if w.bodyBytes != nil {
		w.observedBody.Store(w.bodyBytes.Load())
	}
	w.mu.Lock()
	deadline := w.deadline
	w.mu.Unlock()
	if delay := time.Until(deadline); delay > 0 {
		time.Sleep(delay)
	}
	return 0, testTimeoutError{}
}

func (w *failingResponseWriter) Header() http.Header { return w.header }
func (*failingResponseWriter) WriteHeader(int)       {}
func (*failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}
func (w *failingResponseWriter) SetWriteDeadline(time.Time) error {
	w.deadlineSet.Store(true)
	return nil
}

func (w *slowResponseWriter) Header() http.Header { return w.header }
func (*slowResponseWriter) WriteHeader(int)       {}
func (w *slowResponseWriter) Write(data []byte) (int, error) {
	w.writing.Store(true)
	defer w.writing.Store(false)
	time.Sleep(w.delay)
	w.mu.Lock()
	w.body = append(w.body, data...)
	w.mu.Unlock()
	return len(data), nil
}

func TestStreamIdleWatchdogDoesNotTreatSlowDownstreamAsUpstreamIdle(t *testing.T) {
	w := &slowResponseWriter{header: make(http.Header), delay: 50 * time.Millisecond}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("chunk"))}
	started := time.Now()
	if err := streamResponse(w, resp, 500*time.Millisecond); err != nil {
		t.Fatalf("slow downstream was reported as upstream idle: %v", err)
	}
	if elapsed := time.Since(started); elapsed < w.delay || w.writing.Load() {
		t.Fatalf("handler returned before writer completed: elapsed=%v writing=%v", elapsed, w.writing.Load())
	}
}

func TestDownstreamWriteFailureDoesNotTripUpstreamBreaker(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "main", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test",
		Models: []string{"real"}, Concurrency: 1, Weight: 1, Enabled: true,
		Capabilities: []config.ChannelCapability{{Model: "real", UpstreamModel: "real", ReasoningEfforts: []string{"auto"}}},
	}}, map[string]config.Route{"alias": {Model: "real", Targets: []config.RouteTarget{{Account: "main"}}}})
	cfg := g.Config()
	cfg.Server.FailureThreshold = 1
	cfg.Server.StreamIdleTimeout = config.Duration{Duration: 50 * time.Millisecond}
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	w := &failingResponseWriter{header: make(http.Header)}
	g.ServeGateway(w, gatewayRequest(`{"model":"alias"}`))
	if !w.deadlineSet.Load() {
		t.Fatal("downstream write deadline was not installed")
	}
	account := g.Accounts()[0]
	if account.Failures != 0 || account.CircuitOpenUntil != "" {
		t.Fatalf("downstream failure polluted upstream breaker: %+v", account)
	}
	recent := g.Stats().Recent
	if len(recent) != 1 || recent[0].Outcome != "client_cancelled" {
		t.Fatalf("downstream failure outcome=%+v", recent)
	}
	if latest := g.Stats().RouteLatest; len(latest) != 0 {
		t.Fatalf("client-side write failure replaced route health evidence: %+v", latest)
	}
	ready := httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || strings.Contains(ready.Body.String(), `"failed_routes":["alias"]`) {
		t.Fatalf("client-side write failure degraded readiness: status=%d body=%s", ready.Code, ready.Body.String())
	}
}

func TestRouteObservationFromOldRuntimeCannotPolluteReplacementRoute(t *testing.T) {
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	oldUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(oldStarted)
		<-releaseOld
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"old route failed"}`))
	}))
	defer oldUpstream.Close()
	newUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer newUpstream.Close()
	account := config.Account{
		ID: "main", Type: "openai", BaseURL: oldUpstream.URL + "/v1", APIKey: "test",
		Models: []string{"real"}, Concurrency: 1, Weight: 1, Enabled: true,
		Capabilities: []config.ChannelCapability{{Model: "real", UpstreamModel: "real", ReasoningEfforts: []string{"auto"}}},
	}
	g := newTestGateway(t, []config.Account{account}, map[string]config.Route{
		"alias": {Model: "real", Targets: []config.RouteTarget{{Account: "main"}}},
	})
	oldFingerprint := g.state.Load().routeFingerprints["alias"]
	oldResponse := httptest.NewRecorder()
	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		g.ServeGateway(oldResponse, gatewayRequest(`{"model":"alias"}`))
	}()
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old route request did not reach upstream")
	}
	cfg := g.Config()
	cfg.Accounts[0].BaseURL = newUpstream.URL + "/v1"
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	newFingerprint := g.state.Load().routeFingerprints["alias"]
	if newFingerprint == oldFingerprint {
		t.Fatal("changing resolved upstream identity did not advance route fingerprint")
	}
	close(releaseOld)
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old route request did not finish")
	}
	if latest := g.Stats().RouteLatest; len(latest) != 0 {
		t.Fatalf("old-generation request inserted readiness evidence after reload: %+v", latest)
	}
	ready := httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || strings.Contains(ready.Body.String(), `"failed_routes":["alias"]`) {
		t.Fatalf("old-generation failure degraded replacement route: status=%d body=%s", ready.Code, ready.Body.String())
	}

	currentResponse := httptest.NewRecorder()
	g.ServeGateway(currentResponse, gatewayRequest(`{"model":"alias"}`))
	if currentResponse.Code != http.StatusOK {
		t.Fatalf("replacement route status=%d body=%s", currentResponse.Code, currentResponse.Body.String())
	}
	latest := g.Stats().RouteLatest
	if len(latest) != 1 || latest[0].RouteFingerprint != newFingerprint || latest[0].Status != http.StatusOK {
		t.Fatalf("replacement route did not establish current evidence: %+v", latest)
	}
}

func TestUpstreamClientRejectionIsNeutralForRouteReadiness(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"invalid messages"}`))
	}))
	defer upstream.Close()
	account := config.Account{
		ID: "main", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test",
		Models: []string{"real"}, Concurrency: 1, Weight: 1, Enabled: true,
		Capabilities: []config.ChannelCapability{{Model: "real", UpstreamModel: "real", ReasoningEfforts: []string{"auto"}}},
	}
	g := newTestGateway(t, []config.Account{account}, map[string]config.Route{
		"alias": {Model: "real", Targets: []config.RouteTarget{{Account: "main"}}},
	})
	first := httptest.NewRecorder()
	g.ServeGateway(first, gatewayRequest(`{"model":"alias"}`))
	if first.Code != http.StatusOK || len(g.Stats().RouteLatest) != 1 {
		t.Fatalf("success did not establish route evidence: status=%d latest=%+v", first.Code, g.Stats().RouteLatest)
	}
	baseline := g.Stats().RouteLatest[0]
	second := httptest.NewRecorder()
	g.ServeGateway(second, gatewayRequest(`{"model":"alias","messages":"invalid"}`))
	if second.Code != http.StatusUnprocessableEntity {
		t.Fatalf("upstream client rejection status=%d body=%s", second.Code, second.Body.String())
	}
	latest := g.Stats().RouteLatest
	if len(latest) != 1 || latest[0].Time != baseline.Time || latest[0].Status != http.StatusOK {
		t.Fatalf("client rejection replaced healthy route evidence: baseline=%+v latest=%+v", baseline, latest)
	}
	accountSnapshot := g.Accounts()[0]
	if accountSnapshot.Failures != 0 || accountSnapshot.CircuitOpenUntil != "" {
		t.Fatalf("client rejection polluted breaker: %+v", accountSnapshot)
	}
	recent := g.Stats().Recent
	if len(recent) == 0 || recent[0].Outcome != "upstream_rejected" {
		t.Fatalf("client rejection outcome was not typed neutral: %+v", recent)
	}
	ready := httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || strings.Contains(ready.Body.String(), `"failed_routes":["alias"]`) {
		t.Fatalf("client rejection degraded readiness: status=%d body=%s", ready.Code, ready.Body.String())
	}
}

func TestRetryableErrorBodyHasIdleBoundaryAndReleasesLease(t *testing.T) {
	var secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer second.Close()
	accounts := []config.Account{
		{ID: "first", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
		{ID: "second", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
	}
	g := newTestGateway(t, accounts, map[string]config.Route{"m": {Targets: []config.RouteTarget{{Account: "first", Model: "m"}, {Account: "second", Model: "m"}}}})
	cfg := g.Config()
	cfg.Server.StreamIdleTimeout = config.Duration{Duration: 30 * time.Millisecond}
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"m"}`))
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("error body timeout took %s", elapsed)
	}
	if w.Code != http.StatusOK || secondCalls.Load() != 1 {
		t.Fatalf("status=%d second_calls=%d body=%s", w.Code, secondCalls.Load(), w.Body.String())
	}
	for _, account := range g.Accounts() {
		if account.Active != 0 {
			t.Fatalf("error body retained account lease: %+v", account)
		}
	}
}

func TestBufferedFinalResponseHasWriteDeadlineAndReleasesBodyLeaseFirst(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"limited"}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "limited", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test",
		Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true,
	}}, nil)
	cfg := g.Config()
	cfg.Server.StreamIdleTimeout = config.Duration{Duration: 20 * time.Millisecond}
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	writer := &deadlineBlockingWriter{header: make(http.Header), bodyBytes: &g.bodyBytes}
	payload := `{"model":"m","padding":"` + strings.Repeat("x", 1<<20) + `"}`
	started := time.Now()
	g.ServeGateway(writer, gatewayRequest(payload))
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("buffered downstream write exceeded deadline boundary: %s", elapsed)
	}
	if !writer.deadlineSet.Load() {
		t.Fatal("buffered downstream response installed no write deadline")
	}
	if retained := writer.observedBody.Load(); retained != 0 {
		t.Fatalf("buffered downstream write observed %d retained request bytes", retained)
	}
	if retained := g.bodyBytes.Load(); retained != 0 {
		t.Fatalf("timed-out buffered write retained %d request bytes", retained)
	}
}

func TestAnthropicGatewayErrorsUseAnthropicEnvelope(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m"}`))
	w := httptest.NewRecorder()
	g.ServeGateway(w, req)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"type":"error"`) || strings.Contains(w.Body.String(), `"param"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestConnectionNamedHeadersAreNotForwarded(t *testing.T) {
	source := http.Header{"Connection": {"X-Hop, X-Other"}, "X-Hop": {"secret"}, "X-Other": {"secret-2"}, "X-End-To-End": {"ok"}}
	destination := make(http.Header)
	copyRequestHeaders(destination, source)
	if destination.Get("X-Hop") != "" || destination.Get("X-Other") != "" || destination.Get("X-End-To-End") != "ok" {
		t.Fatalf("forwarded headers=%v", destination)
	}
}

func TestCredentialPinIsTrustedAndSelectedCredentialIsRecorded(t *testing.T) {
	const configuredCredential = "0123456789abcdef"
	const selectedCredential = "fedcba9876543210"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(credentialPinHeader); got != configuredCredential {
			t.Errorf("credential pin header = %q, want %q", got, configuredCredential)
		}
		w.Header().Set(credentialSelectedHeader, selectedCredential)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer upstream.Close()

	g := newTestGateway(t, []config.Account{{
		ID: "pool", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: upstream.URL + "/v1", APIKey: "test",
		Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true,
	}}, map[string]config.Route{
		"alias": {Targets: []config.RouteTarget{{Account: "pool", Credential: configuredCredential, Model: "m"}}},
	})
	request := gatewayRequest(`{"model":"alias","messages":[{"role":"user","content":"ping"}]}`)
	request.Header.Set(credentialPinHeader, "client-spoofed")
	request.Header.Set(credentialSelectedHeader, "client-spoofed")
	recorder := httptest.NewRecorder()
	g.ServeGateway(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get(credentialSelectedHeader) != "" {
		t.Fatal("internal selected credential header leaked downstream")
	}
	recent := g.stats.Snapshot().Recent
	if len(recent) != 1 || recent[0].CredentialID != selectedCredential {
		t.Fatalf("recent credential observation = %+v", recent)
	}
}

func TestCredentialRetrySafeHeaderAllowsTrustedFailover(t *testing.T) {
	const credential = "0123456789abcdef"
	var fallbackCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(credentialRetrySafeHeader); got != "" {
			t.Errorf("client retry-safe header reached upstream: %q", got)
		}
		w.Header().Set(credentialRetrySafeHeader, "true")
		http.Error(w, "credential was rejected before provider submission", http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer second.Close()

	g := newTestGateway(t, []config.Account{
		{ID: "pool", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
		{ID: "fallback", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true},
	}, map[string]config.Route{
		"alias": {Targets: []config.RouteTarget{
			{Account: "pool", Credential: credential, Model: "m"},
			{Account: "fallback", Model: "m"},
		}},
	})
	request := gatewayRequest(`{"model":"alias","messages":[{"role":"user","content":"ping"}]}`)
	request.Header.Set(credentialRetrySafeHeader, "true")
	recorder := httptest.NewRecorder()
	g.ServeGateway(recorder, request)
	if recorder.Code != http.StatusOK || fallbackCalls.Load() != 1 {
		t.Fatalf("status=%d fallback_calls=%d body=%s", recorder.Code, fallbackCalls.Load(), recorder.Body.String())
	}
	if recorder.Header().Get(credentialRetrySafeHeader) != "" {
		t.Fatal("internal retry-safe header leaked downstream")
	}
	if g.Stats().Failovers != 1 {
		t.Fatalf("failovers=%d, want 1", g.Stats().Failovers)
	}
}

func TestReloadReusesUnchangedRequestLogWriter(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	before := g.requestLog.Load()
	cfg := g.Config()
	cfg.Server.MaxInFlightRequests++
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	if after := g.requestLog.Load(); after != before {
		t.Fatal("unchanged log configuration created a second writer")
	}
}

func TestReloadReconfiguresLongLivedRequestLogWriter(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	before := g.requestLog.Load()
	cfg := g.Config()
	cfg.Server.RequestLogMaxBytes += 64 << 10
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	after := g.requestLog.Load()
	if after != before || after.Status().MaxBytes != cfg.Server.RequestLogMaxBytes {
		t.Fatalf("log actor was replaced or not reconfigured: before=%p after=%p status=%+v", before, after, after.Status())
	}
}

func TestGatewayCloseDrainsAndClosesRequestLog(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	logger := g.requestLog.Load()
	logger.Enqueue(RequestRecord{Time: time.Now().UTC().Format(time.RFC3339Nano), RequestID: "before-close"})
	g.Close()
	if g.requestLog.Load() != nil {
		t.Fatal("gateway retained request log after close")
	}
	select {
	case <-logger.done:
	default:
		t.Fatal("request log worker was not drained")
	}
	contents, err := os.ReadFile(logger.path)
	if err != nil || !strings.Contains(string(contents), "before-close") {
		t.Fatalf("drained log missing final record: err=%v body=%s", err, contents)
	}
	if err := g.Reload(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed gateway accepted reload: %v", err)
	}
}

func TestReloadRejectsRestartRequiredServerFields(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	before := g.Config()
	changed := before
	changed.Server.Listen = "127.0.0.1:65530"
	if err := g.store.Save(changed); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err == nil || !strings.Contains(err.Error(), "process restart") {
		t.Fatalf("restart-required reload error=%v", err)
	}
	if got := g.Config().Server.Listen; got != before.Server.Listen {
		t.Fatalf("running listener config changed to %q", got)
	}
}

func TestRewriteRequestAppliesTargetReasoning(t *testing.T) {
	envelope := map[string]json.RawMessage{
		"model":            json.RawMessage(`"alias"`),
		"reasoning_effort": json.RawMessage(`"low"`),
		"messages":         json.RawMessage(`[]`),
	}
	body, err := rewriteRequest(envelope, "real-model", "high")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "real-model" || got["reasoning_effort"] != "high" {
		t.Fatalf("rewritten body = %s", body)
	}
	body, err = rewriteRequest(envelope, "real-model", "none")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "reasoning_effort") {
		t.Fatalf("none should remove reasoning_effort: %s", body)
	}
}

func TestRewriteRequestBodyReusesOriginalWhenNoRewriteNeeded(t *testing.T) {
	original := []byte("{\n  \"messages\": [],\n  \"model\": \"m\",\n  \"reasoning_effort\": \"low\"\n}")
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(original, &envelope); err != nil {
		t.Fatal(err)
	}
	body, err := rewriteRequestBody(original, envelope, "m", "m", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(original) {
		t.Fatalf("unchanged request should reuse original bytes:\n%s", body)
	}
}

func TestGatewayUsesOrderedTargetsAndFallsBackOnMissingModel(t *testing.T) {
	var firstBody, secondBody map[string]any
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&firstBody)
		http.Error(w, "model missing", http.StatusNotFound)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&secondBody)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer second.Close()
	accounts := []config.Account{
		{ID: "first", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"same-model"}, Concurrency: 1, Weight: 1, Enabled: true},
		{ID: "second", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"same-model"}, Concurrency: 1, Weight: 1, Enabled: true},
	}
	routes := map[string]config.Route{"alias": {Targets: []config.RouteTarget{
		{Account: "first", Model: "same-model", ReasoningEffort: "low"},
		{Account: "second", Model: "same-model", ReasoningEffort: "high"},
	}}}
	g := newTestGateway(t, accounts, routes)
	w := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"alias","input":"hello"}`))
	request.Header.Set("Authorization", "Bearer gateway-secret")
	g.ServeGateway(w, request)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if firstBody["model"] != "same-model" || firstBody["reasoning_effort"] != "low" {
		t.Fatalf("first target body = %#v", firstBody)
	}
	if secondBody["model"] != "same-model" || secondBody["reasoning_effort"] != "high" {
		t.Fatalf("fallback target body = %#v", secondBody)
	}
	if g.Stats().Failovers != 1 {
		t.Fatalf("failovers=%d", g.Stats().Failovers)
	}
}

func TestAdminPageUsesPrecompressedHTMLWhenAccepted(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Accept-Encoding", "br, gzip")
	w := httptest.NewRecorder()
	g.serveAdminPage(w, req)
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding=%q, want gzip", got)
	}
	reader, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Lite2API") {
		t.Fatalf("decompressed admin page looks wrong: %.80q", data)
	}

	disabled := httptest.NewRequest(http.MethodGet, "/admin", nil)
	disabled.RemoteAddr = "127.0.0.1:12345"
	disabled.Header.Set("Accept-Encoding", "gzip;q=0")
	plain := httptest.NewRecorder()
	g.serveAdminPage(plain, disabled)
	if got := plain.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding=%q, want identity when gzip q=0", got)
	}
}

func TestAuthenticationFailureTripsCircuit(t *testing.T) {
	var firstCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer second.Close()
	accounts := []config.Account{{ID: "first", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "bad", Models: []string{"m"}, Priority: 0, Concurrency: 1, Weight: 1, Enabled: true}, {ID: "second", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "ok", Models: []string{"m"}, Priority: 1, Concurrency: 1, Weight: 1, Enabled: true}}
	g := newTestGateway(t, accounts, map[string]config.Route{"m": {Accounts: []string{"first", "second"}, Strategy: "priority"}})
	for index := 0; index < 2; index++ {
		response := httptest.NewRecorder()
		g.ServeGateway(response, gatewayRequest(`{"model":"m"}`))
		if response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("circuit did not skip failed credential: calls=%d", got)
	}
}

func TestStreamingHoldsConcurrencySlot(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		close(started)
		<-release
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "stream", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true}}, nil)
	cfg := g.Config()
	cfg.Server.MaxInFlightRequests = 1
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeGateway(httptest.NewRecorder(), gatewayRequest(`{"model":"m","stream":true}`))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream did not start")
	}
	if active := g.Accounts()[0].Active; active != 1 {
		t.Fatalf("active=%d, want 1 while stream is open", active)
	}
	updated := g.Config().Accounts[0]
	updated.Name = "updated while streaming"
	if err := g.UpsertAccount(updated); err != nil {
		t.Fatalf("hot reload: %v", err)
	}
	if active := g.Accounts()[0].Active; active != 1 {
		t.Fatalf("active=%d after hot reload, want shared active slot", active)
	}
	secondW := httptest.NewRecorder()
	g.ServeGateway(secondW, gatewayRequest(`{"model":"m"}`))
	if secondW.Code != http.StatusTooManyRequests {
		t.Fatalf("global limit status=%d, want 429", secondW.Code)
	}
	selection, err := g.state.Load().scheduler.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != ErrNoCapacity || selection != nil {
		t.Fatalf("second selection=%v err=%v", selection, err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gateway did not finish")
	}
	if active := g.Accounts()[0].Active; active != 0 {
		t.Fatalf("active=%d after stream", active)
	}
}

func TestStreamIdleTimeoutReleasesSlot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "idle", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true}}, nil)
	cfg := g.Config()
	cfg.Server.StreamIdleTimeout = config.Duration{Duration: 30 * time.Millisecond}
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	started := time.Now()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","stream":true}`))
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("idle timeout took %s", elapsed)
	}
	if active := g.Accounts()[0].Active; active != 0 {
		t.Fatalf("active=%d after idle timeout", active)
	}
	if recent := g.Stats().Recent; len(recent) == 0 || !strings.Contains(recent[0].Error, "stream idle") || recent[0].Outcome != "stream_error" {
		t.Fatalf("idle timeout not recorded: %+v", recent)
	}
}

func TestModelsListsAliases(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", Models: []string{"direct"}, Enabled: true, Weight: 1}}, map[string]config.Route{"alias": {Accounts: []string{"a"}}})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer gateway-secret")
	w := httptest.NewRecorder()
	g.ServeGateway(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if body := w.Body.String(); body == "" || !strings.Contains(body, "alias") || !strings.Contains(body, "direct") {
		t.Fatalf("body=%s", body)
	}
}

func TestAdminAuthenticationAndRouteUpdate(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "upstream-secret", Headers: map[string]string{"X-Private": "header-secret"}, Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	unauthorized := httptest.NewRecorder()
	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	unauthorizedReq.RemoteAddr = "127.0.0.1:12345"
	g.ServeAdminAPI(unauthorized, unauthorizedReq)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("admin unauthorized=%d", unauthorized.Code)
	}
	body := `{"alias":{"accounts":["a"],"upstream_model":"m","strategy":"sticky"}}`
	req := httptest.NewRequest(http.MethodPut, "/admin/api/routes", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, req)
	if w.Code != 200 || g.Config().Routes["alias"].Strategy != "sticky" {
		t.Fatalf("route update status=%d config=%v", w.Code, g.Config().Routes)
	}
	stateReq := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	stateReq.RemoteAddr = "127.0.0.1:12345"
	stateReq.Header.Set("Authorization", "Bearer admin-secret")
	stateW := httptest.NewRecorder()
	g.ServeAdminAPI(stateW, stateReq)
	if strings.Contains(stateW.Body.String(), "upstream-secret") || strings.Contains(stateW.Body.String(), "header-secret") {
		t.Fatalf("admin response leaked a secret: %s", stateW.Body.String())
	}
}

func TestAdminAPIUsesGzipWhenAccepted(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "upstream-secret", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, req)
	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding=%q, want gzip", got)
	}
	reader, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"models"`) || strings.Contains(string(data), "upstream-secret") {
		t.Fatalf("unexpected compressed admin state: %s", data)
	}
}

func TestAdminPromptTestTargetsSelectedAccount(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Errorf("auth=%q", got)
		}
		if got := r.Header.Get("X-CSRF-Token"); got != "" {
			t.Errorf("admin CSRF header leaked upstream: %q", got)
		}
		var body struct {
			Model    string              `json:"model"`
			Messages []promptTestMessage `json:"messages"`
			Stream   bool                `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if body.Model != "real-model" || body.Stream || len(body.Messages) != 2 || body.Messages[1].Content != "second round" {
			t.Errorf("request=%+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat-1","choices":[{"message":{"role":"assistant","content":"observed"},"finish_reason":"stop"}],"usage":{"prompt_tokens":23,"completion_tokens":2,"total_tokens":25}}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "selected", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret",
		Models: []string{"alias"}, ModelMap: map[string]string{"alias": "real-model"}, Concurrency: 1, Weight: 1, Enabled: true,
	}}, nil)
	body := `{"account_id":"selected","model":"alias","messages":[{"role":"user","content":"first round"},{"role":"user","content":"second round"}],"temperature":0,"max_tokens":128}`
	req := httptest.NewRequest(http.MethodPost, "/admin/api/prompt-test", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer admin-secret")
	req.Header.Set("X-CSRF-Token", "admin-csrf-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		AccountID     string `json:"account_id"`
		UpstreamModel string `json:"upstream_model"`
		LatencyMS     int64  `json:"latency_ms"`
		Response      struct {
			Usage struct {
				PromptTokens int `json:"prompt_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AccountID != "selected" || result.UpstreamModel != "real-model" || result.Response.Usage.PromptTokens != 23 {
		t.Fatalf("result=%+v", result)
	}
	if snapshot := g.Accounts()[0]; snapshot.Total != 0 || snapshot.Failures != 0 {
		t.Fatalf("diagnostic request contaminated route health: %+v", snapshot)
	}
}

func TestAdminPromptTestRejectsUnsupportedAccount(t *testing.T) {
	g := newTestGateway(t, []config.Account{{
		ID: "embeddings-only", Type: "openai", BaseURL: "https://api.example.com", APIKey: "secret",
		Operations: []string{config.OperationEmbeddings}, Models: []string{"embed"}, Weight: 1, Enabled: true,
	}}, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/prompt-test", strings.NewReader(`{"account_id":"embeddings-only","model":"embed","messages":[{"role":"user","content":"test"}]}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "does not support a chat operation") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminPromptTestSupportsAnthropicMessages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "anthropic-secret" {
			t.Errorf("x-api-key=%q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if body["model"] != "claude-real" || body["max_tokens"] != float64(1024) {
			t.Errorf("request=%+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg-1","content":[{"type":"text","text":"anthropic observed"}],"stop_reason":"end_turn","usage":{"input_tokens":31,"output_tokens":4}}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "anthropic", Type: "anthropic", BaseURL: upstream.URL + "/v1", APIKey: "anthropic-secret",
		Models: []string{"claude"}, ModelMap: map[string]string{"claude": "claude-real"}, Concurrency: 1, Weight: 1, Enabled: true,
	}}, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/prompt-test", strings.NewReader(`{"account_id":"anthropic","model":"claude","messages":[{"role":"user","content":"test"}]}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "anthropic observed") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminAccountUpdatePreservesRedactedSecrets(t *testing.T) {
	g := newTestGateway(t, []config.Account{{
		ID: "a", Name: "Before", Type: "openai", BaseURL: "https://api.example.com/v1",
		APIKey: "upstream-secret", Headers: map[string]string{"X-Private": "header-secret"},
		Models: []string{"m"}, Enabled: true, Weight: 1,
	}}, nil)
	account := redactedConfig(g.Config()).Accounts[0]
	account.Name = "After"
	account.APIKey = ""
	if err := g.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	updated := g.Config().Accounts[0]
	if updated.Name != "After" || updated.APIKey != "upstream-secret" || updated.Headers["X-Private"] != "header-secret" {
		t.Fatalf("updated account=%+v", updated)
	}
}

func TestCloneConfigDeepCopiesMutableFields(t *testing.T) {
	cfg := config.Defaults()
	cfg.Server.APIKeys = []string{"key"}
	cfg.Server.AdminAllowedCIDRs = []string{"127.0.0.0/8"}
	cfg.Accounts = []config.Account{{
		ID: "a", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "secret",
		Headers: map[string]string{"X-Test": "one"}, HeadersEnv: map[string]string{"X-Env": "ENV"},
		Models: []string{"m"}, ModelMap: map[string]string{"alias": "real"},
		Capabilities: []config.ChannelCapability{{Model: "alias", UpstreamModel: "real", ReasoningEfforts: []string{"auto"}}},
		Operations:   []string{config.OperationOpenAIChat}, Enabled: true, Weight: 1,
	}}
	cfg.Routes = map[string]config.Route{"alias": {Accounts: []string{"a"}, Targets: []config.RouteTarget{{Account: "a", Model: "real", ReasoningEffort: "auto"}}}}

	cloned := cloneConfig(cfg)
	cloned.Server.APIKeys[0] = "changed"
	cloned.Server.AdminAllowedCIDRs[0] = "10.0.0.0/8"
	cloned.Accounts[0].Headers["X-Test"] = "changed"
	cloned.Accounts[0].HeadersEnv["X-Env"] = "OTHER"
	cloned.Accounts[0].Models[0] = "changed"
	cloned.Accounts[0].ModelMap["alias"] = "changed"
	cloned.Accounts[0].Capabilities[0].ReasoningEfforts[0] = "high"
	cloned.Accounts[0].Operations[0] = config.OperationEmbeddings
	route := cloned.Routes["alias"]
	route.Accounts[0] = "changed"
	route.Targets[0].Model = "changed"
	cloned.Routes["alias"] = route

	if cfg.Server.APIKeys[0] != "key" || cfg.Server.AdminAllowedCIDRs[0] != "127.0.0.0/8" {
		t.Fatalf("server slices were shared: %+v", cfg.Server)
	}
	account := cfg.Accounts[0]
	if account.Headers["X-Test"] != "one" || account.HeadersEnv["X-Env"] != "ENV" || account.Models[0] != "m" || account.ModelMap["alias"] != "real" || account.Capabilities[0].ReasoningEfforts[0] != "auto" || account.Operations[0] != config.OperationOpenAIChat {
		t.Fatalf("account mutable fields were shared: %+v", account)
	}
	if cfg.Routes["alias"].Accounts[0] != "a" || cfg.Routes["alias"].Targets[0].Model != "real" {
		t.Fatalf("route mutable fields were shared: %+v", cfg.Routes["alias"])
	}
}

func TestGatewayConfigReturnsDeepCopy(t *testing.T) {
	g := newTestGateway(t, []config.Account{{
		ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "secret", Models: []string{"m"},
		Headers: map[string]string{"X-Test": "original"}, Enabled: true, Weight: 1,
	}}, map[string]config.Route{"alias": {Accounts: []string{"a"}}})
	copyConfig := g.Config()
	copyConfig.Accounts[0].Models[0] = "changed"
	copyConfig.Accounts[0].Headers["X-Test"] = "changed"
	route := copyConfig.Routes["alias"]
	route.Accounts[0] = "changed"
	copyConfig.Routes["alias"] = route
	runtimeConfig := g.Config()
	if runtimeConfig.Accounts[0].Models[0] != "m" || runtimeConfig.Accounts[0].Headers["X-Test"] != "original" || runtimeConfig.Routes["alias"].Accounts[0] != "a" {
		t.Fatalf("caller mutated runtime snapshot: %+v", runtimeConfig)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	cfg := g.Config()
	cfg.Server.MaxBodyBytes = 16
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","padding":"this is too large"}`))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestInvalidUpdateDoesNotReplaceRunningConfig(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	invalid := g.Config().Accounts[0]
	invalid.BaseURL = "file:///etc/passwd"
	if err := g.UpsertAccount(invalid); err == nil {
		t.Fatal("expected invalid update to fail")
	}
	if got := g.Config().Accounts[0].BaseURL; got != "http://127.0.0.1:1/v1" {
		t.Fatalf("running config changed to %q", got)
	}
	if err := g.Reload(); err != nil {
		t.Fatalf("persisted config was corrupted: %v", err)
	}
}
