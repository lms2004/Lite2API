package gateway

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func schedulerConfig() config.Config {
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{
		{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Models: []string{"m"}, Concurrency: 1, Enabled: true, Weight: 1},
		{ID: "b", Type: "openai", BaseURL: "http://127.0.0.1:2/v1", Models: []string{"m"}, Concurrency: 1, Enabled: true, Weight: 1},
	}
	return cfg
}

func TestSchedulerUsesAvailableAccount(t *testing.T) {
	s := NewScheduler(schedulerConfig())
	one, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if one.Account.Config.ID == two.Account.Config.ID {
		t.Fatal("scheduler selected a full account")
	}
	if _, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0); err != ErrNoCapacity {
		t.Fatalf("got %v", err)
	}
	one.Release()
	two.Release()
}

func TestEmptyRouteFailsClosedAndExplicitAllAccountsRoutes(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Routes["empty"] = config.Route{}
	s := NewScheduler(cfg)
	if selection, err := s.Select(context.Background(), "empty", config.OperationOpenAIChat, "", nil, 0); selection != nil || err != ErrNoEligibleAccount {
		t.Fatalf("empty route selected account: selection=%+v err=%v", selection, err)
	}
	if containsModel(s.Models(), "empty") {
		t.Fatal("empty route was advertised")
	}
	cfg.Routes["all"] = config.Route{AllAccounts: true}
	s = NewScheduler(cfg)
	selection, err := s.Select(context.Background(), "all", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatalf("explicit all_accounts route failed: %v", err)
	}
	selection.Release()
}

func TestSchedulerWaitsForRelease(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	s := NewScheduler(cfg)
	one, _ := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	result := make(chan *Selection, 1)
	go func() {
		selection, _ := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, time.Second)
		result <- selection
	}()
	time.Sleep(20 * time.Millisecond)
	one.Release()
	select {
	case got := <-result:
		if got == nil {
			t.Fatal("waiter did not acquire slot")
		}
		got.Release()
	case <-time.After(time.Second):
		t.Fatal("waiter was not notified")
	}
}

func TestSchedulerBroadcastWakesAllWaitersForReleasedCapacity(t *testing.T) {
	s := NewScheduler(schedulerConfig())
	first, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan *Selection, 2)
	var started sync.WaitGroup
	started.Add(2)
	for range 2 {
		go func() {
			started.Done()
			selection, _ := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, time.Second)
			results <- selection
		}()
	}
	started.Wait()
	time.Sleep(20 * time.Millisecond)
	first.Release()
	second.Release()
	for index := range 2 {
		select {
		case selection := <-results:
			if selection == nil {
				t.Fatalf("waiter %d did not acquire released capacity", index)
			}
			defer selection.Release()
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("waiter %d was not broadcast-woken", index)
		}
	}
}

func TestSchedulerReleaseBeforeReloadWakesNewGeneration(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	oldScheduler := NewScheduler(cfg)
	held, err := oldScheduler.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	newScheduler := NewSchedulerWithPrevious(cfg, oldScheduler)
	result := make(chan *Selection, 1)
	go func() {
		selection, _ := newScheduler.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, time.Second)
		result <- selection
	}()
	time.Sleep(20 * time.Millisecond)
	held.Release()
	select {
	case selection := <-result:
		if selection == nil {
			t.Fatal("new scheduler did not acquire old generation's released slot")
		}
		selection.Release()
	case <-time.After(250 * time.Millisecond):
		t.Fatal("old generation release did not wake new scheduler")
	}
}

func TestSchedulerCapacitySurvivesRemoveAndReaddAcrossGenerations(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	cfg.Accounts[0].Concurrency = 1
	first := NewScheduler(cfg)
	held, err := first.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	removedConfig := cfg
	removedConfig.Accounts = nil
	removed := NewSchedulerWithPrevious(removedConfig, first)
	readded := NewSchedulerWithPrevious(cfg, removed)
	if selection, err := readded.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0); selection != nil || err != ErrNoCapacity {
		t.Fatalf("re-added account bypassed old lease: selection=%v err=%v", selection, err)
	}
	held.Release()
	selection, err := readded.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatalf("re-added account did not observe old release: %v", err)
	}
	selection.Release()
}

func TestSelectionReleaseIsIdempotent(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	s := NewScheduler(cfg)
	selection, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	selection.Release()
	selection.Release()
	if active := s.Snapshot()[0].Active; active != 0 {
		t.Fatalf("double release changed active capacity to %d", active)
	}
}

func TestSchedulerResetsHealthWhenAccountIdentityChanges(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	oldScheduler := NewScheduler(cfg)
	oldAccount := oldScheduler.Get("a")
	oldAccount.reportFailure(oldAccount.beginAttempt(config.OperationOpenAIChat, "m"), "bad credential", 1, time.Hour, true)
	if oldAccount.available(time.Now(), config.OperationOpenAIChat, "m") {
		t.Fatal("old account circuit did not open")
	}
	cfg.Accounts[0].BaseURL = "http://127.0.0.1:9/v1"
	newScheduler := NewSchedulerWithPrevious(cfg, oldScheduler)
	if !newScheduler.Get("a").available(time.Now(), config.OperationOpenAIChat, "m") {
		t.Fatal("changed upstream identity inherited old circuit")
	}
}

func TestLateOlderSuccessDoesNotCloseNewerCircuit(t *testing.T) {
	s := NewScheduler(schedulerConfig())
	account := s.Get("a")
	older := account.beginAttempt(config.OperationOpenAIChat, "m")
	newer := account.beginAttempt(config.OperationOpenAIChat, "m")
	account.reportFailure(newer, "bad credential", 1, time.Hour, true)
	account.reportSuccess(older, time.Millisecond)
	if account.available(time.Now(), config.OperationOpenAIChat, "m") {
		t.Fatal("late result from an older attempt closed the newer circuit")
	}
}

func TestConcurrentFailuresCountWhenNewestCompletesFirst(t *testing.T) {
	s := NewScheduler(schedulerConfig())
	account := s.Get("a")
	attempts := make([]healthAttempt, 100)
	for index := range attempts {
		attempts[index] = account.beginAttempt(config.OperationOpenAIChat, "m")
	}
	for index := len(attempts) - 1; index >= 0; index-- {
		account.reportFailure(attempts[index], "overloaded", 3, time.Hour, false)
	}
	if account.available(time.Now(), config.OperationOpenAIChat, "m") {
		t.Fatal("newest-first concurrent failures did not reach the breaker threshold")
	}
	if failures := account.Snapshot().Failures; failures != int64(len(attempts)) {
		t.Fatalf("failures=%d, want %d", failures, len(attempts))
	}
}

func TestCircuitIsScopedByOperationAndUpstreamModel(t *testing.T) {
	s := NewScheduler(schedulerConfig())
	account := s.Get("a")
	attempt := account.beginAttempt(config.OperationOpenAIChat, "m")
	account.reportFailure(attempt, "model-specific quota", 1, time.Hour, true)
	if account.available(time.Now(), config.OperationOpenAIChat, "m") {
		t.Fatal("failed model scope remained available")
	}
	if !account.available(time.Now(), config.OperationOpenAIResponses, "m") {
		t.Fatal("chat breaker blocked responses operation")
	}
	if !account.available(time.Now(), config.OperationOpenAIChat, "other-model") {
		t.Fatal("one model breaker blocked another upstream model")
	}
}

func TestCircuitIsScopedByExplicitCredential(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	cfg.Accounts[0].AdapterID = "cli-proxy-api"
	cfg.Routes["pinned"] = config.Route{Targets: []config.RouteTarget{
		{Account: "a", Credential: "credential-a", Model: "m"},
		{Account: "a", Credential: "credential-b", Model: "m"},
	}}
	scheduler := NewScheduler(cfg)
	first, err := scheduler.Select(context.Background(), "pinned", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Credential != "credential-a" {
		t.Fatalf("first credential = %q", first.Credential)
	}
	first.Account.reportFailure(first.Account.beginAttempt(config.OperationOpenAIChat, first.Model, first.Credential), "quota", 1, time.Hour, true)
	first.Release()

	if first.Account.available(time.Now(), config.OperationOpenAIChat, "m", "credential-a") {
		t.Fatal("failed credential scope remained available")
	}
	if !first.Account.available(time.Now(), config.OperationOpenAIChat, "m", "credential-b") {
		t.Fatal("one credential breaker blocked another credential")
	}
	second, err := scheduler.Select(context.Background(), "pinned", config.OperationOpenAIChat, "", nil, 0)
	if err != nil {
		t.Fatalf("fallback credential was not selected: %v", err)
	}
	defer second.Release()
	if second.Credential != "credential-b" {
		t.Fatalf("fallback credential = %q, want credential-b", second.Credential)
	}
}

func TestWildcardModelRuntimeStateHasBoundedCardinality(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	cfg.Accounts[0].Models = []string{"*"}
	scheduler := NewScheduler(cfg)
	account := scheduler.Get("a")
	for index := 0; index < 10_000; index++ {
		model := fmt.Sprintf("unbounded-client-model-%d", index)
		attempt := account.beginAttempt(config.OperationOpenAIChat, model)
		account.reportNeutral(attempt)
		selection, err := scheduler.Select(context.Background(), model, config.OperationOpenAIChat, "", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		selection.Release()
	}
	breakerCount := 0
	account.state.breakers.Range(func(_, _ any) bool {
		breakerCount++
		return true
	})
	if breakerCount > maxWildcardBreakerBuckets {
		t.Fatalf("wildcard breaker entries=%d, want <=%d", breakerCount, maxWildcardBreakerBuckets)
	}
	counterCount := 0
	scheduler.roundRobin.Range(func(_, _ any) bool {
		counterCount++
		return true
	})
	if counterCount != 0 {
		t.Fatalf("least-loaded scheduler retained %d per-model counters", counterCount)
	}
}

func TestLegacyRoundRobinStartsWithFirstAccount(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Routes["m"] = config.Route{Accounts: []string{"a", "b"}, Strategy: "round_robin"}
	s := NewScheduler(cfg)
	want := []string{"a", "b", "a"}
	for index, wantID := range want {
		selection, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if selection.Account.Config.ID != wantID {
			t.Fatalf("selection %d=%q want %q", index, selection.Account.Config.ID, wantID)
		}
		selection.Release()
	}
}

func TestStickySelectionIsStable(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Routes["m"] = config.Route{AllAccounts: true, Strategy: "sticky"}
	s := NewScheduler(cfg)
	var id string
	for i := 0; i < 5; i++ {
		selection, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "session-1", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if id == "" {
			id = selection.Account.Config.ID
		} else if selection.Account.Config.ID != id {
			t.Fatalf("sticky moved from %s to %s", id, selection.Account.Config.ID)
		}
		selection.Release()
	}
}

func TestSchedulerFiltersByOperation(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts[0].Operations = []string{config.OperationOpenAIChat}
	cfg.Accounts[1].Operations = []string{config.OperationOpenAIResponses}
	s := NewScheduler(cfg)
	chat, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
	if err != nil || chat.Account.Config.ID != "a" {
		t.Fatalf("chat selection=%+v err=%v", chat, err)
	}
	chat.Release()
	responses, err := s.Select(context.Background(), "m", config.OperationOpenAIResponses, "", nil, 0)
	if err != nil || responses.Account.Config.ID != "b" {
		t.Fatalf("responses selection=%+v err=%v", responses, err)
	}
	responses.Release()
}

func TestSchedulerRejectsUnsupportedOperationWithoutQueueWait(t *testing.T) {
	cfg := schedulerConfig()
	for index := range cfg.Accounts {
		cfg.Accounts[index].Operations = []string{config.OperationOpenAIChat}
	}
	s := NewScheduler(cfg)
	started := time.Now()
	selection, err := s.Select(context.Background(), "m", config.OperationEmbeddings, "", nil, time.Second)
	if selection != nil || err != ErrNoEligibleAccount {
		t.Fatalf("selection=%+v err=%v", selection, err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("static incompatibility waited for queue timeout: %v", elapsed)
	}
}

func TestOrderedTargetChainPreservesModelReasoningAndFallbackOrder(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts[0].Models = []string{"model-primary", "model-secondary"}
	cfg.Accounts[1].Models = []string{"model-primary"}
	cfg.Routes["alias"] = config.Route{Targets: []config.RouteTarget{
		{Account: "a", Model: "model-primary", ReasoningEffort: "high"},
		{Account: "a", Model: "model-secondary", ReasoningEffort: "low"},
		{Account: "b", Model: "model-primary", ReasoningEffort: "medium"},
	}}
	s := NewScheduler(cfg)
	excluded := map[string]struct{}{}
	first, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", excluded, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Account.Config.ID != "a" || first.Model != "model-primary" || first.ReasoningEffort != "high" || !first.Targeted {
		t.Fatalf("first selection = %+v", first)
	}
	excluded[first.Key] = struct{}{}
	first.Release()
	second, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", excluded, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Account.Config.ID != "a" || second.Model != "model-secondary" || second.ReasoningEffort != "low" {
		t.Fatalf("second selection = %+v", second)
	}
	excluded[second.Key] = struct{}{}
	second.Release()
	third, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", excluded, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Release()
	if third.Account.Config.ID != "b" || third.Model != "model-primary" || third.ReasoningEffort != "medium" {
		t.Fatalf("third selection = %+v", third)
	}
	if limit := s.AttemptLimit("alias", 1); limit != 3 {
		t.Fatalf("attempt limit = %d, want full target chain", limit)
	}
}

func TestOrderedTargetChainDoesNotMoveBackAfterSkippingBusyTarget(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = append(cfg.Accounts, config.Account{
		ID: "c", Type: "openai", BaseURL: "http://127.0.0.1:3/v1", Models: []string{"m"}, Concurrency: 1, Enabled: true, Weight: 1,
	})
	cfg.Routes["alias"] = config.Route{Targets: []config.RouteTarget{
		{Account: "a", Model: "m"}, {Account: "b", Model: "m"}, {Account: "c", Model: "m"},
	}}
	s := NewScheduler(cfg)
	if !s.accounts["a"].tryAcquire() {
		t.Fatal("failed to occupy primary target")
	}
	excluded := map[string]struct{}{}
	second, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", excluded, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Account.Config.ID != "b" {
		t.Fatalf("selected %s, want b", second.Account.Config.ID)
	}
	excluded[second.Key] = struct{}{}
	second.Release()
	s.accounts["a"].release()
	third, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", excluded, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Release()
	if third.Account.Config.ID != "c" {
		t.Fatalf("selection moved backward to %s after target a was skipped", third.Account.Config.ID)
	}
}

func TestTargetRoutePriorityStrategyUsesAccountPriorityAndFailsOver(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts[0].Priority = 20
	cfg.Accounts[1].Priority = 5
	cfg.Routes["alias"] = config.Route{
		Strategy: "priority",
		Targets: []config.RouteTarget{
			{Account: "a", Model: "m"},
			{Account: "b", Model: "m"},
		},
	}
	s := NewScheduler(cfg)
	excluded := map[string]struct{}{}

	first, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", excluded, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Account.Config.ID != "b" {
		t.Fatalf("priority selection = %s, want lower-valued account b", first.Account.Config.ID)
	}
	excluded[first.Key] = struct{}{}
	first.Release()

	second, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", excluded, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if second.Account.Config.ID != "a" {
		t.Fatalf("priority failover = %s, want account a", second.Account.Config.ID)
	}
}

func TestTargetRouteRoundRobinRotatesAuthoredTargets(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Routes["alias"] = config.Route{
		Strategy: "round_robin",
		Targets:  []config.RouteTarget{{Account: "a", Model: "m"}, {Account: "b", Model: "m"}},
	}
	s := NewScheduler(cfg)
	want := []string{"a", "b", "a"}
	for index, wantID := range want {
		selection, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", nil, 0)
		if err != nil {
			t.Fatalf("selection %d: %v", index, err)
		}
		if selection.Account.Config.ID != wantID {
			t.Fatalf("selection %d = %s, want %s", index, selection.Account.Config.ID, wantID)
		}
		selection.Release()
	}
}

func TestTargetRouteStickyStrategyIsStable(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Routes["alias"] = config.Route{
		Strategy: "sticky",
		Targets:  []config.RouteTarget{{Account: "a", Model: "m"}, {Account: "b", Model: "m"}},
	}
	s := NewScheduler(cfg)
	selectedID := ""
	for index := 0; index < 4; index++ {
		selection, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "session-target", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if selectedID == "" {
			selectedID = selection.Account.Config.ID
		} else if selection.Account.Config.ID != selectedID {
			t.Fatalf("sticky target changed from %s to %s", selectedID, selection.Account.Config.ID)
		}
		selection.Release()
	}
}

func TestSchedulerResolvesLogicalModelPerRealChannel(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts[0].Models = []string{"antigravity/claude-opus-4-6-thinking"}
	cfg.Accounts[0].Capabilities = []config.ChannelCapability{{
		Model: "claude-opus-4-6", UpstreamModel: "antigravity/claude-opus-4-6-thinking", ReasoningEfforts: []string{"high"},
	}}
	cfg.Accounts[1].Models = []string{"claude-code/claude-opus-4-6"}
	cfg.Accounts[1].Capabilities = []config.ChannelCapability{{
		Model: "claude-opus-4-6", UpstreamModel: "claude-code/claude-opus-4-6", ReasoningEfforts: []string{"low", "high"},
	}}
	cfg.Routes["claude"] = config.Route{
		Model: "claude-opus-4-6", ReasoningEffort: "high",
		Targets: []config.RouteTarget{{Account: "a"}, {Account: "b"}},
	}
	s := NewScheduler(cfg)
	excluded := map[string]struct{}{}
	first, err := s.Select(context.Background(), "claude", config.OperationOpenAIChat, "", excluded, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Model != "antigravity/claude-opus-4-6-thinking" || first.ReasoningEffort != "high" {
		t.Fatalf("first = %+v", first)
	}
	excluded[first.Key] = struct{}{}
	first.Release()
	second, err := s.Select(context.Background(), "claude", config.OperationOpenAIChat, "", excluded, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if second.Model != "claude-code/claude-opus-4-6" || second.ReasoningEffort != "high" {
		t.Fatalf("second = %+v", second)
	}
}

func TestSchedulerAdvertisesAndResolvesUnroutedLogicalCapability(t *testing.T) {
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{{
		ID: "codex", Type: "openai", BaseURL: "http://127.0.0.1:1/v1",
		Models: []string{"gpt-5.6-sol"}, Enabled: true, Weight: 1,
		Capabilities: []config.ChannelCapability{{
			Model: "sol", UpstreamModel: "gpt-5.6-sol", ReasoningEfforts: []string{"auto", "max"},
		}},
	}}
	s := NewScheduler(cfg)
	if models := s.Models(); len(models) != 1 || models[0] != "sol" {
		t.Fatalf("models=%v, want direct logical capability", models)
	}
	selection, err := s.Select(context.Background(), "sol", config.OperationOpenAIResponses, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Model != "gpt-5.6-sol" {
		t.Fatalf("direct capability selected model=%q", selection.Model)
	}
	selection.Release()

	cfg.Routes["sol-max"] = config.Route{
		Model: "sol", ReasoningEffort: "max",
		Targets: []config.RouteTarget{{Account: "codex"}},
	}
	s = NewScheduler(cfg)
	models := s.Models()
	if !containsModel(models, "sol-max") || containsModel(models, "sol") {
		t.Fatalf("routed capability models=%v, want route alias only", models)
	}
	selection, err = s.Select(context.Background(), "sol-max", config.OperationOpenAIResponses, "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Release()
	if selection.Model != "gpt-5.6-sol" || selection.ReasoningEffort != "max" {
		t.Fatalf("routed capability selection=%+v", selection)
	}
}

func containsModel(models []string, wanted string) bool {
	for _, model := range models {
		if model == wanted {
			return true
		}
	}
	return false
}

func TestModelsHideDisabledAccounts(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = append(cfg.Accounts, config.Account{
		ID: "disabled", Type: "openai", BaseURL: "http://127.0.0.1:3/v1",
		Models: []string{"hidden"}, Enabled: false, Weight: 1,
	})
	cfg.Routes["hidden-alias"] = config.Route{Accounts: []string{"disabled"}}
	models := NewScheduler(cfg).Models()
	for _, model := range models {
		if model == "hidden" || model == "hidden-alias" {
			t.Fatal("disabled account or route model was advertised")
		}
	}
}

func BenchmarkSchedulerParallel(b *testing.B) {
	cfg := config.Defaults()
	for i := 0; i < 16; i++ {
		cfg.Accounts = append(cfg.Accounts, config.Account{
			ID: string(rune('a' + i)), Type: "openai", BaseURL: "http://127.0.0.1:1/v1",
			Models: []string{"m"}, Enabled: true, Weight: 1,
		})
	}
	s := NewScheduler(cfg)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			selection, err := s.Select(context.Background(), "m", config.OperationOpenAIChat, "", nil, 0)
			if err != nil {
				b.Fatal(err)
			}
			selection.Release()
		}
	})
}
