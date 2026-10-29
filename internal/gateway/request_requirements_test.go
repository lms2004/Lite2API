package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestRequirementsInspectLargeRequestsAndEscapedImageTypes(t *testing.T) {
	// Feature admission must work beyond the 1 MiB observability cap, without
	// confusing an ordinary prompt containing image-related words with media.
	prompt, _ := json.Marshal(strings.Repeat("x", 2<<20))
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":`+string(prompt)+`},{"type":"input_\u0069mage","image_url":"data:img"}]}],"tools":[{"type":"function"}],"stream":true}`), &envelope); err != nil {
		t.Fatal(err)
	}
	requirements, err := requestRequirements(envelope, config.OperationOpenAIChat)
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"image_input", "tools", "stream"} {
		if !slices.Contains(requirements.Features, feature) {
			t.Fatalf("missing %s in %+v", feature, requirements)
		}
	}
	if containsImageInput([]byte(`[{"content":"type image image_url"}]`)) {
		t.Fatal("ordinary text was mistaken for image input")
	}
}

func TestOperationSpecificFieldsAreRejectedBeforeSubmission(t *testing.T) {
	for _, item := range []struct {
		operation, body string
	}{
		{config.OperationOpenAIChat, `{"previous_response_id":"resp_1"}`},
		{config.OperationOpenAIChat, `{"reasoning":{"effort":"high"}}`},
		{config.OperationOpenAIChat, `{"background":true}`},
		{config.OperationEmbeddings, `{"stream":true}`},
		{config.OperationEmbeddings, `{"tools":[]}`},
		{config.OperationImages, `{"reasoning_effort":"high"}`},
		{config.OperationRerank, `{"parallel_tool_calls":false}`},
	} {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(item.body), &envelope); err != nil {
			t.Fatal(err)
		}
		if err := validateOperationRequest(envelope, item.operation); err == nil {
			t.Fatalf("accepted unsupported field for %s: %s", item.operation, item.body)
		}
	}
}

func TestToolRequirementsDistinguishDisabledCallsFromToolHistory(t *testing.T) {
	for _, item := range []struct {
		body, operation string
		want            bool
	}{
		{`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function"}],"tool_choice":"none"}`, config.OperationOpenAIChat, false},
		{`{"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"test"}],"tool_choice":{"type":"none"}}`, config.OperationAnthropic, false},
		{`{"messages":[{"role":"tool","tool_call_id":"c1","content":"result"}],"tool_choice":"none"}`, config.OperationOpenAIChat, true},
		{`{"input":[{"type":"function_call_output","call_id":"c1","output":"result"}],"tool_choice":"none"}`, config.OperationOpenAIResponses, true},
		{`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"result"}]}],"tool_choice":{"type":"none"}}`, config.OperationAnthropic, true},
	} {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal([]byte(item.body), &envelope)
		requirements, err := requestRequirements(envelope, item.operation)
		if err != nil || slices.Contains(requirements.Features, "tools") != item.want {
			t.Fatalf("requirements=%+v want=%v error=%v body=%s", requirements, item.want, err, item.body)
		}
	}
}

func TestGatewayDisabledToolsDoNotBlockPlainCallsButToolResultsKeepTheirContract(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["tool_choice"] != "none" {
			t.Error("disabled tool choice was silently changed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	account := verificationAccount(upstream.URL, config.OperationOpenAIChat)
	account.Capabilities = []config.ChannelCapability{{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"}, Features: map[string]config.CapabilityEvidence{"openai.chat:tools": {Status: "unsupported", Source: "operator"}}}}
	g := newTestGateway(t, []config.Account{account}, nil)
	for _, item := range []struct {
		body   string
		status int
	}{
		{`{"model":"m","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"test","parameters":{"type":"object"}}}],"tool_choice":"none"}`, 200},
		{`{"model":"m","messages":[{"role":"user","content":"run tool"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"test","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"result"}],"tools":[{"type":"function","function":{"name":"test","parameters":{"type":"object"}}}],"tool_choice":"none"}`, 503},
	} {
		w := httptest.NewRecorder()
		g.ServeGateway(w, gatewayRequest(item.body))
		if w.Code != item.status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, item.status, w.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("tool result reached an incompatible source: calls=%d", calls.Load())
	}
}

func TestSelectionExcludesKnownUnsupportedFeaturesAndPreservesUnknown(t *testing.T) {
	cfg := schedulerConfig()
	for index := range cfg.Accounts {
		cfg.Accounts[index].Capabilities = []config.ChannelCapability{{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"}}}
	}
	cfg.Accounts[0].Capabilities[0].Features = map[string]config.CapabilityEvidence{
		"openai.chat:tools": {Status: "unsupported", Source: "fixture"},
	}
	cfg.Routes["alias"] = config.Route{Targets: []config.RouteTarget{{Account: "a", Model: "m"}, {Account: "b", Model: "m"}}}
	s := NewScheduler(cfg)
	selection, err := s.SelectForRequest(context.Background(), "alias", config.OperationOpenAIChat, "", nil, 0, RequestRequirements{Features: []string{"tools"}})
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Release()
	if selection.Account.Config.ID != "b" {
		t.Fatalf("selected unsupported source %s", selection.Account.Config.ID)
	}
	if selection.Account.Config.Capabilities[0].Features["openai.chat:tools"].Status != "" {
		t.Fatal("an allowed unknown must remain unknown")
	}
}

func TestScopedFeatureRejectionsFollowActualEffortAndCredential(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	cfg.Accounts[0].AdapterID = "cli-proxy-api"
	cfg.Accounts[0].Capabilities = []config.ChannelCapability{{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"high"}, Features: map[string]config.CapabilityEvidence{
		"openai.chat:tools#high@0123456789abcdef": {Status: "unsupported", Source: "operator", CredentialID: "0123456789abcdef", ReasoningEffort: "high"},
	}}}
	cfg.Routes["alias"] = config.Route{Model: "m", ReasoningEffort: "high", Targets: []config.RouteTarget{{Account: "a", Credential: "0123456789abcdef"}, {Account: "a", Credential: "fedcba9876543210"}}}
	selection, err := NewScheduler(cfg).SelectForRequest(context.Background(), "alias", config.OperationOpenAIChat, "", nil, 0, RequestRequirements{Features: []string{"tools"}})
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Release()
	if selection.Credential != "fedcba9876543210" {
		t.Fatal("scoped known rejection was ignored or spread to another credential")
	}
	account := cfg.Accounts[0]
	account.Capabilities[0].Features["openai.chat:call"] = config.CapabilityEvidence{Status: "unsupported"}
	if requirementsCompatible(account, "m", config.OperationOpenAIChat, RequestRequirements{}) {
		t.Fatal("basic interface rejection was ignored when no optional feature was requested")
	}
}

func TestSelectionDistinguishesCoolingFromCapacityAndUnsupported(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	s := NewScheduler(cfg)
	a := s.accounts["a"]
	a.reportFailure(a.beginAttempt(config.OperationOpenAIChat, "m"), "HTTP 429", 1, time.Minute, true)
	if _, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, time.Second); err != ErrKnownUnavailable {
		t.Fatalf("cooldown error = %v", err)
	}
	if _, err := s.Select(context.Background(), "other", config.OperationOpenAIChat, "", nil, time.Second); err != ErrNoEligibleAccount {
		t.Fatalf("unsupported error = %v", err)
	}
}

func TestDynamicReasoningChoosesMatchingUpstreamAndRejectsConflict(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	cfg.Accounts[0].Models = []string{"m-low", "m-high"}
	cfg.Accounts[0].Capabilities = []config.ChannelCapability{
		{Model: "m", UpstreamModel: "m-low", ReasoningEfforts: []string{"auto", "low"}},
		{Model: "m", UpstreamModel: "m-high", ReasoningEfforts: []string{"high"}},
	}
	cfg.Routes["alias"] = config.Route{Model: "m", ReasoningEffort: "auto", Targets: []config.RouteTarget{{Account: "a"}}}
	s := NewScheduler(cfg)
	selection, err := s.SelectForRequest(context.Background(), "alias", config.OperationOpenAIChat, "", nil, 0, RequestRequirements{ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	selection.Release()
	if selection.Model != "m-high" || selection.ReasoningEffort != "high" {
		t.Fatalf("request effort lost: %+v", selection)
	}
	if validateRouteRequirements(config.Route{ReasoningEffort: "low"}, RequestRequirements{ReasoningEffort: "high"}) == nil {
		t.Fatal("conflicting alias silently accepted")
	}
	selection, err = s.SelectForRequest(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0, RequestRequirements{ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	selection.Release()
	if selection.Model != "m-high" {
		t.Fatalf("direct logical model ignored effort: %+v", selection)
	}
}

func TestGatewayFeatureFilteringRunsBeforeUpstreamSubmission(t *testing.T) {
	var unsupportedCalls, supportedCalls atomic.Int32
	unsupported := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		unsupportedCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer unsupported.Close()
	supported := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		supportedCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer supported.Close()
	accounts := schedulerConfig().Accounts
	accounts[0].BaseURL, accounts[1].BaseURL = unsupported.URL+"/v1", supported.URL+"/v1"
	for index := range accounts {
		accounts[index].APIKey = "fixture-upstream-key"
		accounts[index].Capabilities = []config.ChannelCapability{{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"}}}
	}
	accounts[0].Capabilities[0].Features = map[string]config.CapabilityEvidence{"openai.chat:tools": {Status: "unsupported", Source: "fixture"}}
	g := newTestGateway(t, accounts, map[string]config.Route{"alias": {Targets: []config.RouteTarget{{Account: "a", Model: "m"}, {Account: "b", Model: "m"}}}})
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"alias","messages":[{"role":"user","content":"call a tool"}],"tools":[{"type":"function","function":{"name":"test","parameters":{"type":"object"}}}]}`))
	if w.Code != http.StatusOK || unsupportedCalls.Load() != 0 || supportedCalls.Load() != 1 {
		t.Fatalf("status=%d unsupported=%d supported=%d body=%s", w.Code, unsupportedCalls.Load(), supportedCalls.Load(), w.Body.String())
	}
}

func TestDiscoveryPreservesVerifiedFeatureFacts(t *testing.T) {
	previous := config.ChannelCapability{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"}, Features: map[string]config.CapabilityEvidence{
		"openai.chat:tools": {Status: "verified", Source: "actual-client-check", ObservedAt: "2026-10-28T00:00:00Z"},
	}}
	inferred := config.ChannelCapability{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto", "high"}, Evidence: config.CapabilityEvidence{Status: "declared", Source: "model_catalog"}}
	for _, merged := range [][]config.ChannelCapability{mergeSyncedCapabilities([]config.ChannelCapability{previous}, []config.ChannelCapability{inferred}), func() []config.ChannelCapability {
		result, _ := reconcileDiscoveredCapabilities([]config.ChannelCapability{previous}, []config.ChannelCapability{inferred})
		return result
	}()} {
		if merged[0].Features["openai.chat:tools"].Status != "verified" {
			t.Fatalf("verified fact overwritten by catalog: %+v", merged)
		}
	}
}

func TestResponsesReasoningRewritePreservesNativeFieldsAndNone(t *testing.T) {
	original := []byte(`{"model":"alias","reasoning_effort":"none","reasoning":{"summary":"auto"},"input":"hello"}`)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(original, &envelope); err != nil {
		t.Fatal(err)
	}
	if !operationRequestRewriteRequired(config.OperationOpenAIResponses, original, envelope, "alias", "alias", "auto") {
		t.Fatal("normalization must reserve its body allocation")
	}
	updated, err := rewriteOperationRequestBody(config.OperationOpenAIResponses, original, envelope, "alias", "m", "none")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(updated, &body) != nil {
		t.Fatal("invalid rewritten body")
	}
	var reasoning map[string]string
	if json.Unmarshal(body["reasoning"], &reasoning) != nil || reasoning["effort"] != "none" || reasoning["summary"] != "auto" || body["reasoning_effort"] != nil {
		t.Fatalf("invalid native reasoning rewrite: %s", updated)
	}
}

func TestGatewayResponsesReasoningUsesNativeContract(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model           string            `json:"model"`
			ReasoningEffort json.RawMessage   `json:"reasoning_effort"`
			Reasoning       map[string]string `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		call := calls.Add(1)
		want := "high"
		if call == 2 {
			want = "none"
		}
		if body.Model != "real-m" || body.ReasoningEffort != nil || body.Reasoning["effort"] != want || body.Reasoning["summary"] != "auto" {
			t.Errorf("native reasoning contract violated: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok","status":"completed","output":[]}`))
	}))
	defer upstream.Close()
	account := config.Account{ID: "a", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "fixture-key", Models: []string{"real-m"}, Enabled: true, Concurrency: 1,
		Capabilities: []config.ChannelCapability{{Model: "m", UpstreamModel: "real-m", ReasoningEfforts: []string{"auto", "high", "none"}}}}
	g := newTestGateway(t, []config.Account{account}, map[string]config.Route{
		"alias": {Model: "m", ReasoningEffort: "auto", Targets: []config.RouteTarget{{Account: "a"}}},
		"fixed": {Model: "m", ReasoningEffort: "high", Targets: []config.RouteTarget{{Account: "a"}}},
	})
	for _, item := range []struct {
		body string
		want int
	}{
		{`{"model":"alias","input":"hi","reasoning":{"effort":"high","summary":"auto"}}`, http.StatusOK},
		{`{"model":"alias","input":"hi","reasoning_effort":"none","reasoning":{"summary":"auto"}}`, http.StatusOK},
		{`{"model":"fixed","input":"hi","reasoning":{"effort":"none"}}`, http.StatusBadRequest},
	} {
		req := gatewayRequest(item.body)
		req.URL.Path = "/v1/responses"
		w := httptest.NewRecorder()
		g.ServeGateway(w, req)
		if w.Code != item.want {
			t.Fatalf("status=%d want=%d body=%s", w.Code, item.want, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("conflicting request reached upstream; calls=%d", calls.Load())
	}
}

func TestGatewayFeatureContractAlsoRestrictsFallback(t *testing.T) {
	var fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota blocked"}}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer fallback.Close()
	accounts := schedulerConfig().Accounts
	accounts[0].BaseURL, accounts[1].BaseURL = primary.URL+"/v1", fallback.URL+"/v1"
	for index := range accounts {
		accounts[index].APIKey = "fixture-key"
		accounts[index].Capabilities = []config.ChannelCapability{{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"}}}
	}
	accounts[1].Capabilities[0].Features = map[string]config.CapabilityEvidence{"openai.chat:tools": {Status: "unsupported", Source: "fixture"}}
	g := newTestGateway(t, accounts, map[string]config.Route{"alias": {Targets: []config.RouteTarget{{Account: "a", Model: "m"}, {Account: "b", Model: "m"}}}})
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"alias","messages":[],"tools":[{"type":"function","function":{"name":"test"}}]}`))
	if w.Code != http.StatusTooManyRequests || fallbackCalls.Load() != 0 {
		t.Fatalf("fallback violated feature contract: status=%d calls=%d body=%s", w.Code, fallbackCalls.Load(), w.Body.String())
	}
}
