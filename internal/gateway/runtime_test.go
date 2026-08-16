package gateway

import (
	"context"
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

func TestStickySelectionIsStable(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Routes["m"] = config.Route{Strategy: "sticky"}
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
