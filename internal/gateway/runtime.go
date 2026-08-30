package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

type accountCapacityState struct {
	active atomic.Int64
}

// accountCapacityRegistry is shared by every scheduler generation. Entries
// intentionally survive temporary account removal: an old request may still
// hold (or acquire from its pinned runtime generation) a lease, and a later
// re-add of the same account ID must observe that lease rather than creating a
// second independent concurrency counter.
type accountCapacityRegistry struct {
	mu     sync.Mutex
	states map[string]*accountCapacityState
}

func newAccountCapacityRegistry() *accountCapacityRegistry {
	return &accountCapacityRegistry{states: make(map[string]*accountCapacityState)}
}

func (r *accountCapacityRegistry) getOrCreate(id string) *accountCapacityState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if capacity := r.states[id]; capacity != nil {
		return capacity
	}
	capacity := &accountCapacityState{}
	r.states[id] = capacity
	return capacity
}

type accountRuntimeState struct {
	capacity     *accountCapacityState
	identity     [sha256.Size]byte
	total        atomic.Int64
	success      atomic.Int64
	latencyNanos atomic.Int64
	breakers     sync.Map
}

type breakerRuntime struct {
	resultMu            sync.Mutex
	failures            atomic.Int64
	circuitUntil        atomic.Int64
	lastError           atomic.Value
	attemptSeq          atomic.Uint64
	latestFailureSeq    uint64
	latestNonFailureSeq uint64
}

type healthAttempt struct {
	breaker  *breakerRuntime
	sequence uint64
}

func newAccountRuntime(account config.Account, state *accountRuntimeState, capacity *accountCapacityState) *AccountRuntime {
	if state == nil {
		if capacity == nil {
			capacity = &accountCapacityState{}
		}
		state = &accountRuntimeState{capacity: capacity, identity: accountIdentity(account)}
	} else if state.capacity == nil {
		state.capacity = &accountCapacityState{}
	}
	return &AccountRuntime{Config: account, UpstreamKey: account.ResolvedAPIKey(), CustomHeaders: account.ResolvedHeaders(), state: state}
}

// accountIdentity separates health history from capacity history. Active
// leases must survive a reload, but a changed endpoint, credential or adapter
// must not inherit a circuit opened by the previous logical upstream.
func accountIdentity(account config.Account) [sha256.Size]byte {
	identity := struct {
		Type, AdapterID, InstanceID, BaseURL, APIKey, AuthHeader, AuthScheme, ProxyURL string
		Headers                                                                        map[string]string
	}{
		Type: account.Type, AdapterID: account.AdapterID, InstanceID: account.InstanceID,
		BaseURL: account.BaseURL, APIKey: account.ResolvedAPIKey(), AuthHeader: account.AuthHeader,
		AuthScheme: account.AuthScheme, ProxyURL: account.ProxyURL, Headers: account.ResolvedHeaders(),
	}
	encoded, _ := json.Marshal(identity)
	return sha256.Sum256(encoded)
}

// buildRouteFingerprints creates a credential-safe identity for readiness
// evidence. The hash covers authored routing plus every selectable account's
// resolved endpoint/credential identity and model/operation resolution data.
// Only the hash is written to request logs.
func buildRouteFingerprints(cfg config.Config) map[string]string {
	type routeAccountIdentity struct {
		ID           string
		Identity     [sha256.Size]byte
		Enabled      bool
		Models       []string
		ModelMap     map[string]string
		Capabilities []config.ChannelCapability
		Operations   []string
	}
	accounts := make(map[string]config.Account, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		accounts[account.ID] = account
	}
	fingerprints := make(map[string]string, len(cfg.Routes))
	for alias, route := range cfg.Routes {
		selectedIDs := make(map[string]struct{})
		if len(route.Targets) > 0 {
			for _, target := range route.Targets {
				selectedIDs[target.Account] = struct{}{}
			}
		} else if route.AllAccounts {
			for id := range accounts {
				selectedIDs[id] = struct{}{}
			}
		} else {
			for _, id := range route.Accounts {
				selectedIDs[id] = struct{}{}
			}
		}
		ids := make([]string, 0, len(selectedIDs))
		for id := range selectedIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		routeAccounts := make([]routeAccountIdentity, 0, len(ids))
		for _, id := range ids {
			account, exists := accounts[id]
			if !exists {
				// A missing target is still part of the authored Route below. If it
				// appears on a later reload, the populated identity changes the hash.
				continue
			}
			routeAccounts = append(routeAccounts, routeAccountIdentity{
				ID: id, Identity: accountIdentity(account), Enabled: account.Enabled,
				Models: account.Models, ModelMap: account.ModelMap,
				Capabilities: account.Capabilities, Operations: account.Operations,
			})
		}
		encoded, _ := json.Marshal(struct {
			Alias    string
			Route    config.Route
			Accounts []routeAccountIdentity
		}{Alias: alias, Route: route, Accounts: routeAccounts})
		fingerprint := sha256.Sum256(encoded)
		fingerprints[alias] = fmt.Sprintf("%x", fingerprint[:])
	}
	return fingerprints
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
	AverageLatencyMS *int64   `json:"average_latency_ms"`
	CircuitOpenUntil string   `json:"circuit_open_until,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
}

func (a *AccountRuntime) tryAcquire() bool {
	limit := int64(a.Config.Concurrency)
	if limit <= 0 {
		a.state.capacity.active.Add(1)
		return true
	}
	for {
		current := a.state.capacity.active.Load()
		if current >= limit {
			return false
		}
		if a.state.capacity.active.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (a *AccountRuntime) release() { a.state.capacity.active.Add(-1) }

const maxWildcardBreakerBuckets = 64

func breakerKey(operation, model string) string { return operation + "\x00" + model }

func (a *AccountRuntime) breakerModelKey(model string) string {
	for _, candidate := range a.Config.Models {
		if candidate == model {
			return model
		}
	}
	for _, candidate := range a.Config.ModelMap {
		if candidate == model {
			return model
		}
	}
	for _, capability := range a.Config.Capabilities {
		if capability.UpstreamModel == model {
			return model
		}
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(model))
	return fmt.Sprintf("wildcard:%02d", hash.Sum32()%maxWildcardBreakerBuckets)
}

func (a *AccountRuntime) breaker(operation, model string) *breakerRuntime {
	value, _ := a.state.breakers.LoadOrStore(breakerKey(operation, a.breakerModelKey(model)), &breakerRuntime{})
	return value.(*breakerRuntime)
}

func (a *AccountRuntime) available(now time.Time, operation, model string) bool {
	if !a.Config.Enabled {
		return false
	}
	value, exists := a.state.breakers.Load(breakerKey(operation, a.breakerModelKey(model)))
	return !exists || value.(*breakerRuntime).circuitUntil.Load() <= now.UnixNano()
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
	for _, capability := range a.Config.Capabilities {
		if capability.Model == model {
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
	for _, capability := range a.Config.Capabilities {
		if capability.Model == requested {
			return capability.UpstreamModel
		}
	}
	return requested
}

func (a *AccountRuntime) beginAttempt(operation, model string) healthAttempt {
	breaker := a.breaker(operation, model)
	return healthAttempt{breaker: breaker, sequence: breaker.attemptSeq.Add(1)}
}

func (a *AccountRuntime) reportSuccess(attempt healthAttempt, elapsed time.Duration) {
	a.state.total.Add(1)
	a.state.success.Add(1)
	a.state.latencyNanos.Add(elapsed.Nanoseconds())
	if attempt.breaker == nil {
		return
	}
	attempt.breaker.resultMu.Lock()
	defer attempt.breaker.resultMu.Unlock()
	if attempt.sequence <= attempt.breaker.latestFailureSeq {
		return
	}
	if attempt.sequence > attempt.breaker.latestNonFailureSeq {
		attempt.breaker.latestNonFailureSeq = attempt.sequence
	}
	attempt.breaker.failures.Store(0)
	attempt.breaker.circuitUntil.Store(0)
	attempt.breaker.lastError.Store("")
}

func (a *AccountRuntime) reportNeutral(attempt healthAttempt) {
	a.state.total.Add(1)
	// Neutral client/status responses are deliberately not health evidence and
	// therefore do not fence older concurrent failures or close a circuit.
}

func (a *AccountRuntime) reportFailure(attempt healthAttempt, message string, threshold int, cooldown time.Duration, forceCircuit bool) {
	a.state.total.Add(1)
	if attempt.breaker == nil {
		return
	}
	message = truncate(message, 1024)
	attempt.breaker.resultMu.Lock()
	defer attempt.breaker.resultMu.Unlock()
	if attempt.sequence <= attempt.breaker.latestNonFailureSeq {
		return
	}
	if attempt.sequence > attempt.breaker.latestFailureSeq {
		attempt.breaker.latestFailureSeq = attempt.sequence
	}
	failures := attempt.breaker.failures.Add(1)
	attempt.breaker.lastError.Store(message)
	if failures >= int64(threshold) || forceCircuit {
		attempt.breaker.circuitUntil.Store(time.Now().Add(cooldown).UnixNano())
	}
}

func (a *AccountRuntime) Snapshot() AccountSnapshot {
	total := a.state.total.Load()
	success := a.state.success.Load()
	var avg *int64
	if success > 0 {
		value := a.state.latencyNanos.Load() / success / int64(time.Millisecond)
		avg = &value
	}
	var failures int64
	var lastError string
	var circuit string
	var circuitUntil int64
	a.state.breakers.Range(func(_, value any) bool {
		breaker := value.(*breakerRuntime)
		if current := breaker.failures.Load(); current > failures {
			failures = current
			if value := breaker.lastError.Load(); value != nil {
				lastError, _ = value.(string)
			}
		}
		if until := breaker.circuitUntil.Load(); until > circuitUntil {
			circuitUntil = until
			if value := breaker.lastError.Load(); value != nil {
				lastError, _ = value.(string)
			}
		}
		return true
	})
	if circuitUntil > time.Now().UnixNano() {
		circuit = time.Unix(0, circuitUntil).UTC().Format(time.RFC3339)
	}
	return AccountSnapshot{
		ID: a.Config.ID, Name: a.Config.Name, Type: a.Config.Type,
		AdapterID: a.Config.AdapterID, InstanceID: a.Config.InstanceID, BaseURL: a.Config.BaseURL,
		Models: append([]string(nil), a.Config.Models...), Operations: append([]string(nil), a.Config.Operations...), Priority: a.Config.Priority, Weight: a.Config.Weight,
		Concurrency: a.Config.Concurrency, Active: a.state.capacity.active.Load(), Enabled: a.Config.Enabled,
		Failures: failures, Total: total, Success: success, AverageLatencyMS: avg,
		CircuitOpenUntil: circuit, LastError: lastError,
	}
}

type Scheduler struct {
	mu               sync.RWMutex
	accounts         map[string]*AccountRuntime
	routes           map[string]config.Route
	roundRobin       sync.Map
	notify           *schedulerNotifier
	capacityRegistry *accountCapacityRegistry
}

// schedulerNotifier is a generation-based broadcast signal. Closing the
// current channel wakes every waiter, and schedulers created by hot reload
// share the same notifier so releases from old leases wake new-generation
// requests as well.
type schedulerNotifier struct {
	mu      sync.Mutex
	changed chan struct{}
}

func newSchedulerNotifier() *schedulerNotifier {
	return &schedulerNotifier{changed: make(chan struct{})}
}

func (n *schedulerNotifier) subscribe() <-chan struct{} {
	n.mu.Lock()
	changed := n.changed
	n.mu.Unlock()
	return changed
}

func (n *schedulerNotifier) broadcast() {
	n.mu.Lock()
	close(n.changed)
	n.changed = make(chan struct{})
	n.mu.Unlock()
}

type Selection struct {
	Account         *AccountRuntime
	Model           string
	ReasoningEffort string
	Key             string
	Targeted        bool
	release         func()
	releaseOnce     sync.Once
}

type routeTargetCandidate struct {
	index           int
	account         *AccountRuntime
	key             string
	model           string
	reasoningEffort string
}

func (s *Selection) Release() {
	if s != nil && s.release != nil {
		s.releaseOnce.Do(s.release)
	}
}

func NewScheduler(cfg config.Config) *Scheduler {
	return NewSchedulerWithPrevious(cfg, nil)
}

func NewSchedulerWithPrevious(cfg config.Config, previous *Scheduler) *Scheduler {
	notify := newSchedulerNotifier()
	capacityRegistry := newAccountCapacityRegistry()
	if previous != nil && previous.notify != nil {
		notify = previous.notify
		if previous.capacityRegistry != nil {
			capacityRegistry = previous.capacityRegistry
		}
	}
	s := &Scheduler{
		accounts: make(map[string]*AccountRuntime), routes: cfg.Routes,
		notify: notify, capacityRegistry: capacityRegistry,
	}
	for _, account := range cfg.Accounts {
		var shared *accountRuntimeState
		capacity := capacityRegistry.getOrCreate(account.ID)
		if previous != nil {
			previous.mu.RLock()
			if old := previous.accounts[account.ID]; old != nil {
				if old.state.identity == accountIdentity(account) {
					shared = old.state
				}
			}
			previous.mu.RUnlock()
		}
		s.accounts[account.ID] = newAccountRuntime(account, shared, capacity)
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
	routedLogicalModels := make(map[string]struct{})
	for model, route := range s.routes {
		if s.routeEnabled(route) {
			set[model] = struct{}{}
			if route.Model != "" {
				routedLogicalModels[route.Model] = struct{}{}
			}
		}
	}
	for _, account := range s.accounts {
		if !account.Config.Enabled {
			continue
		}
		// Capability-backed accounts expose their logical models until an enabled
		// route claims them. Once routed, the stable route alias is advertised
		// instead of the concrete channel model.
		if len(account.Config.Capabilities) > 0 {
			coveredUpstreamModels := make(map[string]struct{}, len(account.Config.Capabilities))
			for _, capability := range account.Config.Capabilities {
				coveredUpstreamModels[capability.UpstreamModel] = struct{}{}
				if capability.Model == "" {
					continue
				}
				if _, routed := routedLogicalModels[capability.Model]; !routed {
					set[capability.Model] = struct{}{}
				}
			}
			for _, model := range account.Config.Models {
				if model != "*" {
					if _, covered := coveredUpstreamModels[model]; !covered {
						set[model] = struct{}{}
					}
				}
			}
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
	if route.AllAccounts {
		for _, account := range s.accounts {
			if account.Config.Enabled {
				return true
			}
		}
		return false
	}
	if len(route.Accounts) == 0 {
		return false
	}
	for _, id := range route.Accounts {
		if account := s.accounts[id]; account != nil && account.Config.Enabled {
			return true
		}
	}
	return false
}

// RouteAvailable reports structural, scoped readiness without acquiring a
// lease. Unlike AccountSnapshot's legacy worst-case aggregate, this checks the
// actual (operation, upstream model) breaker used by request selection.
func (s *Scheduler) RouteAvailable(alias string, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	route, exists := s.routes[alias]
	if !exists {
		return false
	}
	availableForAnyOperation := func(account *AccountRuntime, upstreamModel string) bool {
		if account == nil || !account.Config.Enabled {
			return false
		}
		operations := account.Config.Operations
		if len(operations) == 0 {
			operations = config.DefaultOperations(account.Config.Type)
		}
		for _, operation := range operations {
			if account.available(now, operation, upstreamModel) {
				return true
			}
		}
		return false
	}
	if len(route.Targets) > 0 {
		for _, target := range route.Targets {
			account := s.accounts[target.Account]
			if account == nil {
				continue
			}
			upstreamModel, _, compatible := config.ResolveRouteTarget(account.Config, route, target)
			if compatible && availableForAnyOperation(account, upstreamModel) {
				return true
			}
		}
		return false
	}
	allowed := make(map[string]struct{}, len(route.Accounts))
	for _, id := range route.Accounts {
		allowed[id] = struct{}{}
	}
	for id, account := range s.accounts {
		if !route.AllAccounts {
			if _, ok := allowed[id]; !ok {
				continue
			}
		}
		if availableForAnyOperation(account, account.upstreamModel(alias, route.UpstreamModel)) {
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
		// Subscribe before inspecting capacity so a release between the failed
		// selection and the select below cannot be lost.
		changed := s.notify.subscribe()
		selection, eligible, capacityBlocked := s.trySelect(model, operation, session, excluded)
		if selection != nil {
			// Claude Code may send or select a reasoning effort while using the
			// Anthropic Messages endpoint. Lite2API accepts it for compatibility,
			// but deliberately discards it before forwarding because upstream
			// Anthropic-compatible schemas do not consistently accept the legacy
			// top-level reasoning_effort field.
			if operation == config.OperationAnthropic {
				selection.ReasoningEffort = "none"
			}
			return selection, nil
		}
		if !eligible {
			return nil, ErrNoEligibleAccount
		}
		if !capacityBlocked {
			return nil, ErrNoCapacity
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
		case <-changed:
			timer.Stop()
		}
	}
}

func (s *Scheduler) trySelect(model, operation, session string, excluded map[string]struct{}) (*Selection, bool, bool) {
	s.mu.RLock()
	route, routed := s.routes[model]
	if routed && len(route.Targets) > 0 {
		eligible := false
		capacityBlocked := false
		now := time.Now()
		skipped := make([]string, 0, len(route.Targets))
		candidates := make([]routeTargetCandidate, 0, len(route.Targets))
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
			candidates = append(candidates, routeTargetCandidate{
				index: index, account: account, key: key,
				model: upstreamModel, reasoningEffort: reasoningEffort,
			})
		}
		counter := uint64(0)
		if route.Strategy == "round_robin" && len(candidates) > 1 {
			counter = s.counter(model)
		}
		orderRouteTargetCandidates(candidates, route.Strategy, model, session, counter)
		for _, candidate := range candidates {
			account := candidate.account
			available := account.available(now, operation, candidate.model)
			if !available || !account.tryAcquire() {
				if available {
					capacityBlocked = true
				}
				skipped = append(skipped, candidate.key)
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
				Account: selected, Model: candidate.model, ReasoningEffort: candidate.reasoningEffort,
				Key: candidate.key, Targeted: true, release: func() {
					selected.release()
					s.notify.broadcast()
				},
			}, eligible, capacityBlocked
		}
		s.mu.RUnlock()
		return nil, eligible, capacityBlocked
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
		if routed && !route.AllAccounts {
			if _, ok := allowed[id]; !ok {
				continue
			}
		}
		if !routed && !account.supports(model) {
			continue
		}
		eligible = true
		if !account.available(now, operation, account.upstreamModel(model, route.UpstreamModel)) {
			continue
		}
		candidates = append(candidates, account)
	}
	if len(candidates) == 0 {
		s.mu.RUnlock()
		return nil, eligible, false
	}
	counter := uint64(0)
	if strategy == "round_robin" {
		counter = s.counter(model)
	}
	orderCandidates(candidates, strategy, model, session, counter)
	var selected *AccountRuntime
	for _, candidate := range candidates {
		if candidate.tryAcquire() {
			selected = candidate
			break
		}
	}
	s.mu.RUnlock()
	if selected == nil {
		return nil, eligible, true
	}
	return &Selection{Account: selected, Model: selected.upstreamModel(model, route.UpstreamModel), Key: selected.Config.ID, release: func() {
		selected.release()
		s.notify.broadcast()
	}}, eligible, false
}

// releaseAccount is used by direct diagnostic leases that bypass Select.
// Normal and diagnostic traffic must share the same capacity notification
// contract or a prompt test can leave queued requests asleep until timeout.
func (s *Scheduler) releaseAccount(account *AccountRuntime) {
	if account == nil {
		return
	}
	account.release()
	s.notify.broadcast()
}

func routeTargetKey(index int, target config.RouteTarget) string {
	return fmt.Sprintf("target:%d:%s:%s:%s", index, target.Account, target.Model, target.ReasoningEffort)
}

// orderRouteTargetCandidates applies an optional scheduling policy to an
// explicit target set. An empty strategy deliberately preserves the authored
// order for backward compatibility. Strategy-bearing target routes now honor
// the same account controls as legacy account routes while retaining each
// target's provider-specific model mapping.
func orderRouteTargetCandidates(candidates []routeTargetCandidate, strategy, model, session string, counter uint64) {
	strategy = strings.TrimSpace(strategy)
	if len(candidates) < 2 || strategy == "" {
		return
	}
	if strategy == "round_robin" {
		// counter starts at one; make the first request use the first authored
		// target and rotate from there.
		offset := 0
		if counter > 0 {
			offset = int((counter - 1) % uint64(len(candidates)))
		}
		if offset > 0 {
			rotated := append([]routeTargetCandidate(nil), candidates[offset:]...)
			rotated = append(rotated, candidates[:offset]...)
			copy(candidates, rotated)
		}
		return
	}
	if strategy == "sticky" && session != "" {
		sort.SliceStable(candidates, func(i, j int) bool {
			iKey := model + "\x00" + candidates[i].key
			jKey := model + "\x00" + candidates[j].key
			iScore := rendezvousScore(session, iKey, candidates[i].account)
			jScore := rendezvousScore(session, jKey, candidates[j].account)
			if iScore != jScore {
				return iScore > jScore
			}
			return candidates[i].index < candidates[j].index
		})
		return
	}

	// Sticky without a stable session follows the least-loaded policy, matching
	// the legacy scheduler. Priority values are lower-first in Lite2API.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i].account, candidates[j].account
		if strategy != "priority" {
			if aLoad, bLoad := load(a), load(b); aLoad != bLoad {
				return aLoad < bLoad
			}
		}
		if a.Config.Priority != b.Config.Priority {
			return a.Config.Priority < b.Config.Priority
		}
		if a.Config.ID != b.Config.ID && strategy != "priority" {
			return a.Config.ID < b.Config.ID
		}
		return candidates[i].index < candidates[j].index
	})
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
		offset := 0
		if counter > 0 {
			offset = int((counter - 1) % uint64(len(accounts)))
		}
		rotated := append([]*AccountRuntime(nil), accounts[offset:]...)
		rotated = append(rotated, accounts[:offset]...)
		copy(accounts, rotated)
	}
}

func load(a *AccountRuntime) float64 {
	if a.Config.Concurrency <= 0 {
		return float64(a.state.capacity.active.Load()) / float64(max(a.Config.Weight, 1))
	}
	return float64(a.state.capacity.active.Load()) / float64(a.Config.Concurrency) / float64(max(a.Config.Weight, 1))
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
