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
	one, err := s.Select(context.Background(), "m", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Select(context.Background(), "m", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if one.Account.Config.ID == two.Account.Config.ID {
		t.Fatal("scheduler selected a full account")
	}
	if _, err := s.Select(context.Background(), "m", "", nil, 0); err != ErrNoCapacity {
		t.Fatalf("got %v", err)
	}
	one.Release()
	two.Release()
}

func TestSchedulerWaitsForRelease(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	s := NewScheduler(cfg)
	one, _ := s.Select(context.Background(), "m", "", nil, 0)
	result := make(chan *Selection, 1)
	go func() { selection, _ := s.Select(context.Background(), "m", "", nil, time.Second); result <- selection }()
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
		selection, err := s.Select(context.Background(), "m", "session-1", nil, 0)
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
			selection, err := s.Select(context.Background(), "m", "", nil, 0)
			if err != nil {
				b.Fatal(err)
			}
			selection.Release()
		}
	})
}
