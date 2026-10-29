package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func verifyAdmin(t *testing.T, g *Gateway, input capabilityVerificationRequest) capabilityVerificationResult {
	t.Helper()
	body, _ := json.Marshal(input)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/accounts/verify", strings.NewReader(string(body)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("verification status=%d body=%s", w.Code, w.Body.String())
	}
	var result capabilityVerificationResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func verificationAccount(url, operation string) config.Account {
	accountType := "openai"
	if operation == config.OperationAnthropic {
		accountType = "anthropic"
	}
	return config.Account{ID: "source", Type: accountType, BaseURL: url + "/v1", APIKey: "fixture-key", Models: []string{"m"}, Operations: []string{operation}, Enabled: true, Concurrency: 1}
}

func TestPromptVerificationPersistsExactCallScopeAndSurvivesReload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{verificationAccount(upstream.URL, config.OperationOpenAIChat)}, nil)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/prompt-test", strings.NewReader(`{"account_id":"source","model":"m","messages":[{"role":"user","content":"hello"}]}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"evidence_saved":true`) {
		t.Fatalf("prompt result=%d %s", w.Code, w.Body.String())
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	account := g.state.Load().cfg.Accounts[0]
	facts := account.Capabilities[0].Features
	if len(facts) != 1 || facts["openai.chat:call"].Status != "verified" || facts["openai.chat:call"].SourceIdentity != config.AccountSourceIdentity(account) || facts["openai.chat:call"].UpstreamModel != "m" {
		t.Fatalf("wrong proof scope: %+v", facts)
	}
	if g.Accounts()[0].Total != 0 {
		t.Fatal("diagnostic probe contaminated route health")
	}
}

func TestCapabilityImageVerificationUsesNativePayloads(t *testing.T) {
	for _, operation := range []string{config.OperationOpenAIChat, config.OperationOpenAIResponses, config.OperationAnthropic} {
		t.Run(operation, func(t *testing.T) {
			var sawImage atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				switch operation {
				case config.OperationOpenAIResponses:
					items := body["input"].([]any)
					content := items[0].(map[string]any)["content"].([]any)
					image := content[1].(map[string]any)
					sawImage.Store(image["type"] == "input_image" && strings.HasPrefix(image["image_url"].(string), "data:image/png;base64,"))
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}}})
				case config.OperationAnthropic:
					messages := body["messages"].([]any)
					content := messages[0].(map[string]any)["content"].([]any)
					image := content[1].(map[string]any)
					source := image["source"].(map[string]any)
					sawImage.Store(image["type"] == "image" && source["type"] == "base64" && source["media_type"] == "image/png")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"type": "message", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": "ok"}}})
				default:
					messages := body["messages"].([]any)
					content := messages[0].(map[string]any)["content"].([]any)
					image := content[1].(map[string]any)
					imageURL := image["image_url"].(map[string]any)
					sawImage.Store(image["type"] == "image_url" && strings.HasPrefix(imageURL["url"].(string), "data:image/png;base64,"))
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "ok"}, "finish_reason": "stop"}}})
				}
			}))
			defer upstream.Close()
			g := newTestGateway(t, []config.Account{verificationAccount(upstream.URL, operation)}, nil)
			result := verifyAdmin(t, g, capabilityVerificationRequest{AccountID: "source", Model: "m", Operation: operation, Feature: "image_input"})
			if !result.OK || !result.Completed || !result.EvidenceSaved || !sawImage.Load() {
				t.Fatalf("image verification=%+v saw_image=%v", result, sawImage.Load())
			}
			key := operation + ":image_input"
			if fact := g.state.Load().cfg.Accounts[0].Capabilities[0].Features[key]; fact.Status != "verified" {
				t.Fatalf("image evidence=%+v", fact)
			}
		})
	}
}

func TestCapabilityStreamVerificationDoesNotPromoteIncompleteOrCatalogResults(t *testing.T) {
	var mode atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		if mode.Load() == 1 {
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{verificationAccount(upstream.URL, config.OperationOpenAIChat)}, nil)
	input := capabilityVerificationRequest{AccountID: "source", Model: "m", Operation: config.OperationOpenAIChat, Feature: "stream"}
	failed := verifyAdmin(t, g, input)
	if failed.OK || failed.Completed || !failed.EvidenceSaved {
		t.Fatalf("incomplete stream falsely verified: %+v", failed)
	}
	fact := g.state.Load().cfg.Accounts[0].Capabilities[0].Features["openai.chat:stream"]
	if fact.Status != "unknown" || fact.LastCheckOutcome != "failed" {
		t.Fatalf("failure became unsupported: %+v", fact)
	}
	mode.Store(1)
	passed := verifyAdmin(t, g, input)
	if !passed.OK || !passed.Completed || !passed.EvidenceSaved {
		t.Fatalf("complete stream not verified: %+v", passed)
	}
	if err := g.syncDiscoveredCapabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	capability := g.state.Load().cfg.Accounts[0].Capabilities[0]
	if capability.Evidence.Status == "verified" || capability.Features["openai.chat:stream"].Status != "verified" {
		t.Fatalf("catalog changed scoped verification: %+v", capability)
	}
	mode.Store(0)
	failed = verifyAdmin(t, g, input)
	fact = g.state.Load().cfg.Accounts[0].Capabilities[0].Features["openai.chat:stream"]
	if failed.OK || fact.Status != "verified" || fact.LastCheckOutcome != "failed" {
		t.Fatalf("historical proof or latest failure lost: %+v %+v", failed, fact)
	}
}

func TestFailedProbeDoesNotInventAReasoningRestriction(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{verificationAccount(upstream.URL, config.OperationOpenAIChat)}, nil)
	result := verifyAdmin(t, g, capabilityVerificationRequest{AccountID: "source", Model: "m"})
	if result.OK || !result.EvidenceSaved {
		t.Fatalf("failed check not retained: %+v", result)
	}
	selection, err := g.state.Load().scheduler.SelectForRequest(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0, RequestRequirements{ReasoningEffort: "high"})
	if err != nil {
		t.Fatalf("storing an unknown result narrowed previously unknown reasoning support: %v", err)
	}
	selection.Release()
}

func TestFailedProbePreservesExplicitUnsupportedScope(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	account := verificationAccount(upstream.URL, config.OperationOpenAIChat)
	account.Capabilities = []config.ChannelCapability{{
		Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"},
		Features: map[string]config.CapabilityEvidence{
			"openai.chat:call": {Status: "unsupported", Source: "operator", UpstreamModel: "m", ReasoningEffort: "auto"},
		},
	}}
	g := newTestGateway(t, []config.Account{account}, nil)
	runtime := g.state.Load().scheduler.Get("source")
	check := newCapabilityVerification(runtime, "m", "m", config.OperationOpenAIChat, "call", "auto", "", "unsupported-preserve")
	result := g.finishCapabilityVerification(check, false, false, errors.New("fixture failure"))
	if !result.EvidenceSaved || result.OK {
		t.Fatalf("failed verification should be saved without changing the outcome: %+v", result)
	}
	fact := g.state.Load().cfg.Accounts[0].Capabilities[0].Features["openai.chat:call"]
	if fact.Status != "unsupported" || fact.LastCheckOutcome != "failed" {
		t.Fatalf("explicit unsupported scope was weakened by failure: %+v", fact)
	}
}

func TestCapabilityToolsVerificationRequiresARealRoundTrip(t *testing.T) {
	for _, setup := range []struct {
		operation, name    string
		pool, changedOwner bool
	}{
		{operation: config.OperationOpenAIChat}, {operation: config.OperationOpenAIResponses}, {operation: config.OperationAnthropic},
		{operation: config.OperationOpenAIChat, name: "pool-pinned", pool: true},
		{operation: config.OperationOpenAIChat, name: "pool-owner-changed", pool: true, changedOwner: true},
	} {
		operation := setup.operation
		t.Run(operation+setup.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if setup.pool {
					w.Header().Set(credentialSelectedHeader, "0123456789abcdef")
				}
				if calls.Add(1) == 1 {
					tools := payload["tools"].([]any)
					tool := tools[0].(map[string]any)
					parameters := tool["parameters"]
					if operation == config.OperationOpenAIChat {
						parameters = tool["function"].(map[string]any)["parameters"]
					}
					if operation == config.OperationAnthropic {
						parameters = tool["input_schema"]
					}
					token := parameters.(map[string]any)["properties"].(map[string]any)["token"].(map[string]any)["enum"].([]any)[0].(string)
					args, _ := json.Marshal(map[string]string{"token": token})
					switch operation {
					case config.OperationOpenAIResponses:
						_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_1", "status": "completed", "output": []any{map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "lite2api_verify", "arguments": string(args)}}})
					case config.OperationAnthropic:
						_ = json.NewEncoder(w).Encode(map[string]any{"type": "message", "stop_reason": "tool_use", "content": []any{map[string]any{"type": "tool_use", "id": "call_1", "name": "lite2api_verify", "input": map[string]string{"token": token}}}})
					default:
						_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "lite2api_verify", "arguments": string(args)}}}}}}})
					}
					return
				}
				if setup.pool {
					if r.Header.Get(credentialPinHeader) != "0123456789abcdef" {
						t.Error("tool result was not pinned to the original pool credential")
					}
					if setup.changedOwner {
						w.Header().Set(credentialSelectedHeader, "fedcba9876543210")
					}
				}
				var resultToken string
				switch operation {
				case config.OperationOpenAIResponses:
					items := payload["input"].([]any)
					last := items[len(items)-1].(map[string]any)
					if last["type"] != "function_call_output" || last["call_id"] != "call_1" {
						t.Error("response tool result lost its call identity")
					}
					resultToken = last["output"].(string)
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp_2", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": resultToken}}}}})
				case config.OperationAnthropic:
					messages := payload["messages"].([]any)
					last := messages[len(messages)-1].(map[string]any)["content"].([]any)[0].(map[string]any)
					if last["type"] != "tool_result" || last["tool_use_id"] != "call_1" {
						t.Error("Messages tool result lost its call identity")
					}
					resultToken = last["content"].(string)
					_ = json.NewEncoder(w).Encode(map[string]any{"type": "message", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": resultToken}}})
				default:
					messages := payload["messages"].([]any)
					last := messages[len(messages)-1].(map[string]any)
					if last["role"] != "tool" || last["tool_call_id"] != "call_1" {
						t.Error("Chat tool result lost its call identity")
					}
					resultToken = last["content"].(string)
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": resultToken}}}})
				}
			}))
			defer upstream.Close()
			account := verificationAccount(upstream.URL, operation)
			if setup.pool {
				t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", upstream.URL)
				account.AdapterID = "cli-proxy-api"
			}
			g := newTestGateway(t, []config.Account{account}, nil)
			result := verifyAdmin(t, g, capabilityVerificationRequest{AccountID: "source", Model: "m", Operation: operation, Feature: "tools"})
			if result.OK == setup.changedOwner || !result.EvidenceSaved || calls.Load() != 2 {
				t.Fatalf("round trip not established: %+v calls=%d", result, calls.Load())
			}
			facts := g.state.Load().cfg.Accounts[0].Capabilities[0].Features
			key := operation + ":tools"
			want := "verified"
			if setup.pool {
				key += "@0123456789abcdef"
			}
			if setup.changedOwner {
				want = "unknown"
			}
			if facts[key].Status != want || len(facts) != 1 {
				t.Fatalf("unexpected generalized proof: %+v", facts)
			}
		})
	}
}

func TestVerificationDoesNotSaveOldResultToChangedSource(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	account := verificationAccount(upstream.URL, config.OperationOpenAIChat)
	g := newTestGateway(t, []config.Account{account}, nil)
	result := make(chan capabilityVerificationResult, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/admin/api/accounts/verify", strings.NewReader(`{"account_id":"source","model":"m","feature":"call"}`))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer admin-secret")
		w := httptest.NewRecorder()
		g.ServeAdminAPI(w, req)
		var observed capabilityVerificationResult
		_ = json.Unmarshal(w.Body.Bytes(), &observed)
		result <- observed
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("verification did not reach its upstream")
	}
	account.APIKey = "replacement-key"
	if err := g.UpsertAccount(account); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	var observed capabilityVerificationResult
	select {
	case observed = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("verification did not finish")
	}
	if !observed.OK || !observed.Completed || observed.EvidenceSaved || !strings.Contains(observed.Message, "changed") {
		t.Fatalf("stale result attached to new source: %+v", observed)
	}
	if len(g.state.Load().cfg.Accounts[0].Capabilities) != 0 {
		t.Fatal("replacement source inherited old verification")
	}
}

func TestVerificationSourceGuardKeepsUnrelatedChangesAndRejectsOwnMappingChange(t *testing.T) {
	account := config.Normalize(config.Config{Accounts: []config.Account{verificationAccount("https://example.invalid", config.OperationOpenAIChat)}}).Accounts[0]
	runtime := newAccountRuntime(account, nil, nil)
	check := newCapabilityVerification(runtime, "m", "m", config.OperationOpenAIChat, "call", "auto", "", "fixture")
	unrelated := cloneAccount(account)
	unrelated.Models = append(unrelated.Models, "other")
	unrelated.Name = "new label"
	if !verificationSourceMatches(check, unrelated) {
		t.Fatal("an unrelated catalog or label invalidated exact-scope evidence")
	}
	unrelated.ModelMap = map[string]string{"m": "other"}
	if verificationSourceMatches(check, unrelated) {
		t.Fatal("a changed logical-to-upstream mapping accepted the old result")
	}
	blocked := cloneAccount(account)
	blocked.Capabilities = []config.ChannelCapability{{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"}, Features: map[string]config.CapabilityEvidence{"openai.chat:call": {Status: "unsupported"}}}}
	if verificationSourceMatches(check, blocked) {
		t.Fatal("a concurrent explicit restriction was overwritten by old work")
	}
}

func TestCapabilityVerificationPassesExecutionProfileAndRejectsWildcardProof(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "real-m" || body["service_tier"] != "priority" {
			t.Errorf("execution profile was ignored: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	account := verificationAccount(upstream.URL, config.OperationOpenAIChat)
	account.Models = []string{"real-m"}
	account.Capabilities = []config.ChannelCapability{{Model: "m@fast", UpstreamModel: "real-m", ReasoningEfforts: []string{"auto"}}}
	g := newTestGateway(t, []config.Account{account}, nil)
	result := verifyAdmin(t, g, capabilityVerificationRequest{AccountID: "source", Model: "m@fast"})
	if !result.OK || !result.EvidenceSaved {
		t.Fatalf("execution profile result=%+v", result)
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/api/accounts/verify", strings.NewReader(`{"account_id":"source","model":"*"}`))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, r)
	if w.Code != 400 || calls.Load() != 1 {
		t.Fatalf("wildcard was accepted as concrete model proof: %d calls=%d", w.Code, calls.Load())
	}
}

func TestSharedPoolVerificationRemainsCredentialScoped(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(credentialSelectedHeader, "0123456789abcdef")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", upstream.URL)
	account := verificationAccount(upstream.URL, config.OperationOpenAIChat)
	account.AdapterID = "cli-proxy-api"
	g := newTestGateway(t, []config.Account{account}, nil)
	result := verifyAdmin(t, g, capabilityVerificationRequest{AccountID: "source", Model: "m", Feature: "call"})
	if !result.OK || !result.EvidenceSaved || result.CredentialID != "0123456789abcdef" {
		t.Fatalf("pool verification=%+v", result)
	}
	facts := g.state.Load().cfg.Accounts[0].Capabilities[0].Features
	if facts["openai.chat:call"].Status == "verified" || facts["openai.chat:call@0123456789abcdef"].Status != "verified" {
		t.Fatalf("one pool credential was generalized: %+v", facts)
	}
}

func TestToolVerificationDoesNotAcceptAnIgnoredToolResult(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			tool := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
			token := tool["parameters"].(map[string]any)["properties"].(map[string]any)["token"].(map[string]any)["enum"].([]any)[0].(string)
			args, _ := json.Marshal(map[string]string{"token": token})
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "lite2api_verify", "arguments": string(args)}}}}}}})
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"I ignored the tool result"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{verificationAccount(upstream.URL, config.OperationOpenAIChat)}, nil)
	result := verifyAdmin(t, g, capabilityVerificationRequest{AccountID: "source", Model: "m", Feature: "tools"})
	fact := g.state.Load().cfg.Accounts[0].Capabilities[0].Features["openai.chat:tools"]
	if result.OK || !result.Completed || !result.EvidenceSaved || calls.Load() != 2 || fact.Status != "unknown" || fact.LastCheckOutcome != "failed" {
		t.Fatalf("HTTP200 was mistaken for tool proof: %+v %+v calls=%d", result, fact, calls.Load())
	}
}

func TestVerificationFrozenIdentityRejectsEnvironmentRotation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer old-env-key" {
			t.Error("probe did not use its frozen credential")
		}
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	t.Setenv("VERIFICATION_RUNTIME_KEY", "old-env-key")
	account := verificationAccount(upstream.URL, config.OperationOpenAIChat)
	account.APIKeyEnv = "VERIFICATION_RUNTIME_KEY"
	account.APIKey = ""
	g := newTestGateway(t, []config.Account{account}, nil)
	done := make(chan capabilityVerificationResult, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/admin/api/accounts/verify", strings.NewReader(`{"account_id":"source","model":"m"}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer admin-secret")
		w := httptest.NewRecorder()
		g.ServeAdminAPI(w, r)
		var result capabilityVerificationResult
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		done <- result
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("probe did not start")
	}
	t.Setenv("VERIFICATION_RUNTIME_KEY", "new-env-key")
	close(release)
	var result capabilityVerificationResult
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not finish")
	}
	if !result.OK || result.EvidenceSaved {
		t.Fatalf("old environment proof saved after rotation: %+v", result)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(g.state.Load().cfg.Accounts[0].Capabilities) != 0 {
		t.Fatal("new environment source inherited proof")
	}
}

func TestVerifiedSourceChangesInvalidateProofThroughUpsertAndImport(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{verificationAccount(upstream.URL, config.OperationOpenAIChat)}, nil)
	input := capabilityVerificationRequest{AccountID: "source", Model: "m"}
	if !verifyAdmin(t, g, input).EvidenceSaved {
		t.Fatal("initial verification missing")
	}
	account := cloneAccount(g.state.Load().cfg.Accounts[0])
	account.APIKey = "upsert-replacement"
	if err := g.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	if g.state.Load().cfg.Accounts[0].Capabilities[0].Features["openai.chat:call"].Status != "unknown" {
		t.Fatal("Upsert transferred a verified capability")
	}
	if !verifyAdmin(t, g, input).EvidenceSaved {
		t.Fatal("replacement verification missing")
	}
	account = cloneAccount(g.state.Load().cfg.Accounts[0])
	enabled := true
	result, err := g.ImportAccounts(context.Background(), AccountImportRequest{Mode: "upsert", Data: newAccountImportData([]AccountImportItem{{ID: "source", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "import-replacement", Models: []string{"m"}, Capabilities: account.Capabilities, Operations: account.Operations, Enabled: &enabled}})})
	if err != nil || result.AccountUpdated != 1 {
		t.Fatalf("import=%+v error=%v", result, err)
	}
	if g.state.Load().cfg.Accounts[0].Capabilities[0].Features["openai.chat:call"].Status != "unknown" {
		t.Fatal("import transferred a verified capability")
	}
}
