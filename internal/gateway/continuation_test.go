package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestContinuationParameterHasExplicitTypeAndEmptySemantics(t *testing.T) {
	for _, item := range []struct {
		raw     string
		want    bool
		invalid bool
	}{
		{`"resp_1"`, true, false}, {`null`, false, false}, {`""`, false, false},
		{`123`, false, true}, {`[]`, false, true}, {`{}`, false, true}, {`true`, false, true},
	} {
		requirements, err := requestRequirements(map[string]json.RawMessage{"previous_response_id": json.RawMessage(item.raw)}, config.OperationOpenAIResponses)
		if (err != nil) != item.invalid || requirements.Continuation != item.want {
			t.Fatalf("%s: continuation=%v error=%v", item.raw, requirements.Continuation, err)
		}
	}
}

func TestGatewayContinuationRejectsAdaptersAndMultipleOwnersBeforeSubmission(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	for _, item := range []struct {
		name     string
		adapter  string
		multiple bool
		cooling  bool
	}{
		{name: "CLIProxy HTTP cannot preserve continuation", adapter: "cli-proxy-api"},
		{name: "other adapter has no native continuation contract", adapter: "gemini-web2api"},
		{name: "multiple native targets", multiple: true},
		{name: "cooling source remains an ownership candidate", multiple: true, cooling: true},
	} {
		t.Run(item.name, func(t *testing.T) {
			accounts := []config.Account{{ID: "a", Type: "openai", AdapterID: item.adapter, BaseURL: upstream.URL + "/v1", APIKey: "fixture-key", Models: []string{"m"}, Enabled: true, Concurrency: 1}}
			targets := []config.RouteTarget{{Account: "a", Model: "m"}}
			if item.multiple {
				second := accounts[0]
				second.ID = "b"
				accounts = append(accounts, second)
				targets = append(targets, config.RouteTarget{Account: "b", Model: "m"})
			}
			g := newTestGateway(t, accounts, map[string]config.Route{"alias": {Targets: targets}})
			if item.cooling {
				account := g.state.Load().scheduler.accounts["a"]
				account.reportFailure(account.beginAttempt(config.OperationOpenAIResponses, "m"), "HTTP 429", 1, time.Minute, true)
			}
			req := gatewayRequest(`{"model":"alias","input":"next turn","previous_response_id":"resp_previous"}`)
			req.URL.Path = "/v1/responses"
			w := httptest.NewRecorder()
			g.ServeGateway(w, req)
			if w.Code != http.StatusBadRequest || calls.Load() != 0 {
				t.Fatalf("unsafe continuation submitted: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
			}
		})
	}
}

func TestGatewayContinuationPreservesSingleNativeOwnerAndDoesNotReplay(t *testing.T) {
	for _, adapter := range []string{"", "generic-openai"} {
		t.Run("adapter="+adapter, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != "real-m" || body["previous_response_id"] != "resp_previous" {
					t.Errorf("native continuation lost: %+v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				if calls.Add(1) == 1 {
					_, _ = w.Write([]byte(`{"id":"resp_next","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
					return
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"quota blocked"}}`))
			}))
			defer upstream.Close()
			account := config.Account{ID: "a", Type: "openai", AdapterID: adapter, BaseURL: upstream.URL + "/v1", APIKey: "fixture-key", Models: []string{"real-m"}, Enabled: true, Concurrency: 1}
			g := newTestGateway(t, []config.Account{account}, map[string]config.Route{"alias": {Targets: []config.RouteTarget{{Account: "a", Model: "real-m"}}}})
			invalid := gatewayRequest(`{"model":"alias","input":"next turn","previous_response_id":123}`)
			invalid.URL.Path = "/v1/responses"
			rejected := httptest.NewRecorder()
			g.ServeGateway(rejected, invalid)
			if rejected.Code != http.StatusBadRequest || calls.Load() != 0 {
				t.Fatalf("invalid continuation reached upstream: status=%d calls=%d", rejected.Code, calls.Load())
			}
			for _, want := range []int{http.StatusOK, http.StatusTooManyRequests} {
				req := gatewayRequest(`{"model":"alias","input":"next turn","previous_response_id":"resp_previous"}`)
				req.URL.Path = "/v1/responses"
				w := httptest.NewRecorder()
				g.ServeGateway(w, req)
				if w.Code != want {
					t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("native continuation replayed after rejection: calls=%d", calls.Load())
			}
			owner := g.state.Load().scheduler.accounts["a"]
			owner.reportFailure(owner.beginAttempt(config.OperationOpenAIResponses, "real-m"), "quota blocked", 1, time.Minute, true)
			unavailable := gatewayRequest(`{"model":"alias","input":"next turn","previous_response_id":"resp_previous"}`)
			unavailable.URL.Path = "/v1/responses"
			blocked := httptest.NewRecorder()
			g.ServeGateway(blocked, unavailable)
			if blocked.Code != http.StatusServiceUnavailable || calls.Load() != 2 {
				t.Fatalf("unavailable native owner was replaced or retried: status=%d calls=%d", blocked.Code, calls.Load())
			}
		})
	}
}

func TestContinuationRejectsVariableModelMapping(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	cfg.Accounts[0].Models = []string{"m-low", "m-high"}
	cfg.Accounts[0].Capabilities = []config.ChannelCapability{
		{Model: "m", UpstreamModel: "m-low", ReasoningEfforts: []string{"auto", "low"}},
		{Model: "m", UpstreamModel: "m-high", ReasoningEfforts: []string{"high"}},
	}
	cfg.Routes["alias"] = config.Route{Model: "m", ReasoningEffort: "auto", Targets: []config.RouteTarget{{Account: "a"}}}
	if err := NewScheduler(cfg).ValidateContinuation("alias", config.OperationOpenAIResponses, RequestRequirements{Continuation: true, ReasoningEffort: "high"}); err == nil {
		t.Fatal("continuation accepted an alias with multiple concrete model mappings")
	}
}
