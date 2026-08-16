package gateway

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

type AccountRuntime struct {
	Config        config.Account
	UpstreamKey   string
	CustomHeaders map[string]string
	state         *accountRuntimeState
}

type accountRuntimeState struct {
	active       atomic.Int64
	failures     atomic.Int64
	total        atomic.Int64
	success      atomic.Int64
	latencyNanos atomic.Int64
	circuitUntil atomic.Int64
	lastError    atomic.Value
}

func newAccountRuntime(account config.Account, state *accountRuntimeState) *AccountRuntime {
	if state == nil {
		state = &accountRuntimeState{}
	}
	return &AccountRuntime{Config: account, UpstreamKey: account.ResolvedAPIKey(), CustomHeaders: account.ResolvedHeaders(), state: state}
}

type AccountSnapshot struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Type             string   `json:"type"`
	AdapterID        string   `json:"adapter_id,omitempty"`
	InstanceID       string   `json:"instance_id,omitempty"`
	BaseURL          string   `json:"base_url"`
	Models           []string `json:"models"`
	Operations       []string `json:"operations"`
	Priority         int      `json:"priority"`
	Weight           int      `json:"weight"`
	Concurrency      int      `json:"concurrency"`
	Active           int64    `json:"active"`
	Enabled          bool     `json:"enabled"`
	Failures         int64    `json:"consecutive_failures"`
	Total            int64    `json:"total_requests"`
	Success          int64    `json:"successful_requests"`
	AverageLatencyMS int64    `json:"average_latency_ms"`
	CircuitOpenUntil string   `json:"circuit_open_until,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
}

func (a *AccountRuntime) tryAcquire() bool {
	limit := int64(a.Config.Concurrency)
	if limit <= 0 {
		a.state.active.Add(1)
		return true
	}
	for {
		current := a.state.active.Load()
		if current >= limit {
			return false
		}
		if a.state.active.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (a *AccountRuntime) release() { a.state.active.Add(-1) }

func (a *AccountRuntime) available(now time.Time) bool {
	return a.Config.Enabled && a.state.circuitUntil.Load() <= now.UnixNano()
}

func (a *AccountRuntime) supports(model string) bool {
	if len(a.Config.Models) == 0 {
		return true
	}
	for _, candidate := range a.Config.Models {
		if candidate == "*" || candidate == model {
			return true
		}
	}
	return false
}

func (a *AccountRuntime) upstreamModel(requested, routeModel string) string {
	if routeModel != "" {
		return routeModel
	}
	if mapped := a.Config.ModelMap[requested]; mapped != "" {
		return mapped
	}
	return requested
}

func (a *AccountRuntime) reportSuccess(elapsed time.Duration) {
	a.state.total.Add(1)
	a.state.success.Add(1)
	a.state.latencyNanos.Add(elapsed.Nanoseconds())
	a.state.failures.Store(0)
	a.state.circuitUntil.Store(0)
	a.state.lastError.Store("")
}

func (a *AccountRuntime) reportFailure(message string, threshold int, cooldown time.Duration, forceCircuit bool) {
	a.state.total.Add(1)
	failures := a.state.failures.Add(1)
	a.state.lastError.Store(message)
	if failures >= int64(threshold) || forceCircuit {
		a.state.circuitUntil.Store(time.Now().Add(cooldown).UnixNano())
	}
}

func (a *AccountRuntime) Snapshot() AccountSnapshot {
	total := a.state.total.Load()
	success := a.state.success.Load()
	avg := int64(0)
	if success > 0 {
		avg = a.state.latencyNanos.Load() / success / int64(time.Millisecond)
	}
	var lastError string
	if v := a.state.lastError.Load(); v != nil {
		lastError, _ = v.(string)
	}
	var circuit string
	if until := a.state.circuitUntil.Load(); until > time.Now().UnixNano() {
		circuit = time.Unix(0, until).UTC().Format(time.RFC3339)
	}
	return AccountSnapshot{
		ID: a.Config.ID, Name: a.Config.Name, Type: a.Config.Type,
		AdapterID: a.Config.AdapterID, InstanceID: a.Config.InstanceID, BaseURL: a.Config.BaseURL,
		Models: append([]string(nil), a.Config.Models...), Operations: append([]string(nil), a.Config.Operations...), Priority: a.Config.Priority, Weight: a.Config.Weight,
		Concurrency: a.Config.Concurrency, Active: a.state.active.Load(), Enabled: a.Config.Enabled,
		Failures: a.state.failures.Load(), Total: total, Success: success, AverageLatencyMS: avg,
		CircuitOpenUntil: circuit, LastError: lastError,
	}
}

type Scheduler struct {
	mu         sync.RWMutex
	accounts   map[string]*AccountRuntime
	routes     map[string]config.Route
	roundRobin sync.Map
	notify     chan struct{}
}

type Selection struct {
	Account         *AccountRuntime
	Model           string
	ReasoningEffort string
	Key             string
	Targeted        bool
	release         func()
}

func (s *Selection) Release() {
	if s != nil && s.release != nil {
		s.release()
	}
}

func NewScheduler(cfg config.Config) *Scheduler {
	return NewSchedulerWithPrevious(cfg, nil)
}

func NewSchedulerWithPrevious(cfg config.Config, previous *Scheduler) *Scheduler {
	s := &Scheduler{accounts: make(map[string]*AccountRuntime), routes: cfg.Routes, notify: make(chan struct{}, 1)}
	for _, account := range cfg.Accounts {
		var shared *accountRuntimeState
		if previous != nil {
			previous.mu.RLock()
			if old := previous.accounts[account.ID]; old != nil {
				shared = old.state
			}
			previous.mu.RUnlock()
		}
		s.accounts[account.ID] = newAccountRuntime(account, shared)
	}
	return s
}

func (s *Scheduler) Snapshot() []AccountSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]AccountSnapshot, 0, len(s.accounts))
	for _, account := range s.accounts {
		result = append(result, account.Snapshot())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Scheduler) Models() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := make(map[string]struct{})
	for model, route := range s.routes {
		if s.routeEnabled(route) {
			set[model] = struct{}{}
		}
	}
	for _, account := range s.accounts {
		if !account.Config.Enabled {
			continue
		}
		// Capability-backed account model IDs select a concrete upstream channel
		// and must stay hidden behind stable route aliases.
		if len(account.Config.Capabilities) > 0 {
			continue
		}
		for _, model := range account.Config.Models {
			if model != "*" {
				set[model] = struct{}{}
			}
		}
	}
	models := make([]string, 0, len(set))
	for model := range set {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func (s *Scheduler) routeEnabled(route config.Route) bool {
	if len(route.Targets) > 0 {
		for _, target := range route.Targets {
			if account := s.accounts[target.Account]; account != nil && account.Config.Enabled {
				return true
			}
		}
		return false
	}
	if len(route.Accounts) == 0 {
		for _, account := range s.accounts {
			if account.Config.Enabled {
				return true
			}
		}
		return false
	}
	for _, id := range route.Accounts {
		if account := s.accounts[id]; account != nil && account.Config.Enabled {
			return true
		}
	}
	return false
}

// AttemptLimit returns the full length of an explicit target chain. A target
// chain is an operator-authored failover contract, so it is not silently
// truncated by the legacy account failover limit.
func (s *Scheduler) AttemptLimit(model string, legacyLimit int) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if route, ok := s.routes[model]; ok && len(route.Targets) > 0 {
		return len(route.Targets)
	}
	return legacyLimit
}

func (s *Scheduler) Select(ctx context.Context, model, operation, session string, excluded map[string]struct{}, wait time.Duration) (*Selection, error) {
	deadline := time.Now().Add(wait)
	for {
		selection, eligible := s.trySelect(model, operation, session, excluded)
		if selection != nil {
			return selection, nil
		}
		if !eligible {
			return nil, ErrNoEligibleAccount
		}
		remaining := time.Until(deadline)
		if wait <= 0 || remaining <= 0 {
			return nil, ErrNoCapacity
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
			return nil, ErrNoCapacity
		case <-s.notify:
			timer.Stop()
		}
	}
}

func (s *Scheduler) trySelect(model, operation, session string, excluded map[string]struct{}) (*Selection, bool) {
	s.mu.RLock()
	route, routed := s.routes[model]
	if routed && len(route.Targets) > 0 {
		eligible := false
		now := time.Now()
		skipped := make([]string, 0, len(route.Targets))
		for index, target := range route.Targets {
			key := routeTargetKey(index, target)
			if _, skip := excluded[key]; skip {
				continue
			}
			account := s.accounts[target.Account]
			if account == nil || !account.Config.Enabled || !config.AccountSupportsOperation(account.Config, operation) {
				skipped = append(skipped, key)
				continue
			}
			upstreamModel, reasoningEffort, compatible := config.ResolveRouteTarget(account.Config, route, target)
			if !compatible {
				skipped = append(skipped, key)
				continue
			}
			eligible = true
			if !account.available(now) || !account.tryAcquire() {
				skipped = append(skipped, key)
				continue
			}
			for _, skippedKey := range skipped {
				if excluded != nil {
					excluded[skippedKey] = struct{}{}
				}
			}
			s.mu.RUnlock()
			selected := account
			return &Selection{
				Account: selected, Model: upstreamModel, ReasoningEffort: reasoningEffort,
				Key: key, Targeted: true, release: func() {
					selected.release()
					select {
					case s.notify <- struct{}{}:
					default:
					}
				},
			}, eligible
		}
		s.mu.RUnlock()
		return nil, eligible
	}
	strategy := route.Strategy
	if strategy == "" {
		strategy = "least_loaded"
	}
	allowed := make(map[string]struct{}, len(route.Accounts))
	for _, id := range route.Accounts {
		allowed[id] = struct{}{}
	}
	candidates := make([]*AccountRuntime, 0, len(s.accounts))
	eligible := false
	now := time.Now()
	for id, account := range s.accounts {
		if _, skip := excluded[id]; skip || !account.Config.Enabled {
			continue
		}
		if !config.AccountSupportsOperation(account.Config, operation) {
			continue
		}
		if routed && len(allowed) > 0 {
			if _, ok := allowed[id]; !ok {
				continue
			}
		}
		if !routed && !account.supports(model) {
			continue
		}
		eligible = true
		if !account.available(now) {
			continue
		}
		candidates = append(candidates, account)
	}
	if len(candidates) == 0 {
		s.mu.RUnlock()
		return nil, eligible
	}
	orderCandidates(candidates, strategy, model, session, s.counter(model))
	var selected *AccountRuntime
	for _, candidate := range candidates {
		if candidate.tryAcquire() {
			selected = candidate
			break
		}
	}
	s.mu.RUnlock()
	if selected == nil {
		return nil, eligible
	}
	return &Selection{Account: selected, Model: selected.upstreamModel(model, route.UpstreamModel), Key: selected.Config.ID, release: func() {
		selected.release()
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}}, eligible
}

func routeTargetKey(index int, target config.RouteTarget) string {
	return fmt.Sprintf("target:%d:%s:%s:%s", index, target.Account, target.Model, target.ReasoningEffort)
}

func (s *Scheduler) counter(model string) uint64 {
	value, _ := s.roundRobin.LoadOrStore(model, &atomic.Uint64{})
	counter := value.(*atomic.Uint64)
	return counter.Add(1)
}

func orderCandidates(accounts []*AccountRuntime, strategy, model, session string, counter uint64) {
	if strategy == "sticky" && session != "" {
		sort.Slice(accounts, func(i, j int) bool {
			return rendezvousScore(session, model, accounts[i]) > rendezvousScore(session, model, accounts[j])
		})
		return
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		switch strategy {
		case "priority":
			if a.Config.Priority != b.Config.Priority {
				return a.Config.Priority < b.Config.Priority
			}
		case "round_robin":
			// Stable order below; rotation is applied after sorting.
		default:
			al, bl := load(a), load(b)
			if al != bl {
				return al < bl
			}
			if a.Config.Priority != b.Config.Priority {
				return a.Config.Priority < b.Config.Priority
			}
		}
		return a.Config.ID < b.Config.ID
	})
	if strategy == "round_robin" && len(accounts) > 1 {
		offset := int(counter % uint64(len(accounts)))
		rotated := append([]*AccountRuntime(nil), accounts[offset:]...)
		rotated = append(rotated, accounts[:offset]...)
		copy(accounts, rotated)
	}
}

func load(a *AccountRuntime) float64 {
	if a.Config.Concurrency <= 0 {
		return float64(a.state.active.Load()) / float64(max(a.Config.Weight, 1))
	}
	return float64(a.state.active.Load()) / float64(a.Config.Concurrency) / float64(max(a.Config.Weight, 1))
}

func rendezvousScore(session, model string, account *AccountRuntime) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.Join([]string{session, model, account.Config.ID}, "\x00")))
	weight := uint64(max(account.Config.Weight, 1))
	score := h.Sum64()
	if weight > 1 && score <= math.MaxUint64/weight {
		score *= weight
	}
	return score
}

func (s *Scheduler) Get(id string) *AccountRuntime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accounts[id]
}
