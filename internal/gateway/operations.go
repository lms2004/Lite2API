package gateway

import (
	"sort"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

type HealthState string

const (
	HealthUnknown        HealthState = "unknown"
	HealthReady          HealthState = "ready"
	HealthDegraded       HealthState = "degraded"
	HealthUnavailable    HealthState = "unavailable"
	observationFreshness             = 5 * time.Minute
)

type WindowSnapshot struct {
	Scope           string   `json:"scope"`
	DurationSeconds int64    `json:"duration_seconds"`
	Samples         int      `json:"samples"`
	Successful      int      `json:"successful"`
	SuccessRate     *float64 `json:"success_rate"`
	P95LatencyMS    *int64   `json:"p95_latency_ms"`
	ObservedAt      string   `json:"observed_at,omitempty"`
}

type RouteObservationSnapshot struct {
	AccountID        string `json:"account_id"`
	Operation        string `json:"operation,omitempty"`
	Stream           bool   `json:"stream"`
	ObservedAt       string `json:"observed_at"`
	Outcome          string `json:"outcome"`
	Fresh            bool   `json:"fresh"`
	CredentialID     string `json:"credential_id,omitempty"`
	UpstreamModel    string `json:"upstream_model,omitempty"`
	ReasoningEffort  string `json:"reasoning_effort,omitempty"`
	RequiredFeatures string `json:"required_features,omitempty"`
	RequestContract  string `json:"request_contract,omitempty"`
	ExpectedChoices  int    `json:"expected_choices,omitempty"`
	ToolMode         string `json:"tool_mode,omitempty"`
}

type CapacitySnapshot struct {
	Active      int64    `json:"active"`
	Limit       *int64   `json:"limit"`
	Utilization *float64 `json:"utilization"`
	Unlimited   bool     `json:"unlimited"`
}

type RouteHealthSnapshot struct {
	Alias           string                     `json:"alias"`
	State           HealthState                `json:"state"`
	Reason          string                     `json:"reason"`
	Targets         int                        `json:"targets"`
	ReadyTargets    int                        `json:"ready_targets"`
	DegradedTargets int                        `json:"degraded_targets"`
	UnknownTargets  int                        `json:"unknown_targets"`
	MissingTargets  []string                   `json:"missing_targets,omitempty"`
	Window          WindowSnapshot             `json:"window"`
	Observations    []RouteObservationSnapshot `json:"observations"`
}

type OperationsSnapshot struct {
	GeneratedAt              string                `json:"generated_at"`
	State                    HealthState           `json:"state"`
	Reason                   string                `json:"reason"`
	Window                   WindowSnapshot        `json:"window"`
	Capacity                 CapacitySnapshot      `json:"capacity"`
	Routes                   []RouteHealthSnapshot `json:"routes"`
	ObservationMaxAgeSeconds int64                 `json:"observation_max_age_seconds"`
}

func buildOperationsSnapshot(now time.Time, cfg config.Config, accounts []AccountSnapshot, stats StatsSnapshot) OperationsSnapshot {
	accountByID := make(map[string]AccountSnapshot, len(accounts))
	for _, account := range accounts {
		accountByID[account.ID] = account
	}

	// Historical metrics retain their observation time. Availability uses only
	// recent observations and never treats an old success as present proof.
	requestRows := validOperationRecords(stats.Recent, now)
	// Route health uses only fingerprint-gated, health-relevant observations.
	// Falling back to the general recent ring would reintroduce old-generation
	// and client-side failures that readiness deliberately treats as neutral.
	routeRows := validOperationRecords(stats.RouteLatest, now)
	routes := make([]RouteHealthSnapshot, 0, len(cfg.Routes))
	for alias, route := range cfg.Routes {
		routes = append(routes, routeHealthSnapshot(alias, route, cfg.Accounts, accountByID, routeRows, now))
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Alias < routes[j].Alias })

	result := OperationsSnapshot{
		GeneratedAt:              now.UTC().Format(time.RFC3339Nano),
		Window:                   summarizeLatest(requestRows),
		Capacity:                 summarizeCapacity(accounts, stats.Active),
		Routes:                   routes,
		ObservationMaxAgeSeconds: int64(observationFreshness / time.Second),
	}
	result.State, result.Reason = overallHealth(routes)
	return result
}

func validOperationRecords(records []RequestRecord, now time.Time) []RequestRecord {
	result := make([]RequestRecord, 0, len(records))
	for _, record := range records {
		observed, err := time.Parse(time.RFC3339Nano, record.Time)
		if err != nil || observed.After(now.Add(time.Minute)) {
			continue
		}
		result = append(result, record)
	}
	return result
}

func latestOperationRecords(records []RequestRecord) []RequestRecord {
	var latest RequestRecord
	var latestAt time.Time
	found := false
	for _, record := range records {
		observed, err := time.Parse(time.RFC3339Nano, record.Time)
		if err != nil {
			continue
		}
		if !found || observed.After(latestAt) {
			latest, latestAt, found = record, observed, true
		}
	}
	if !found {
		return nil
	}
	return []RequestRecord{latest}
}

func summarizeLatest(records []RequestRecord) WindowSnapshot {
	records = latestOperationRecords(records)
	result := WindowSnapshot{Samples: len(records), Scope: "latest_logical_request"}
	if len(records) == 0 {
		return result
	}
	result.ObservedAt = records[0].Time
	latencies := make([]int64, 0, 1)
	for _, record := range records {
		if recordSucceeded(record) {
			result.Successful++
		}
		latencies = append(latencies, record.LatencyMS)
	}
	rate := float64(result.Successful) / float64(result.Samples)
	result.SuccessRate = &rate
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	index := (95*len(latencies) + 99) / 100
	value := latencies[index-1]
	result.P95LatencyMS = &value
	return result
}

func summarizeCapacity(accounts []AccountSnapshot, active int64) CapacitySnapshot {
	result := CapacitySnapshot{Active: active}
	var limit int64
	for _, account := range accounts {
		if !account.Enabled {
			continue
		}
		if account.Concurrency <= 0 {
			result.Unlimited = true
			return result
		}
		limit += int64(account.Concurrency)
	}
	if limit <= 0 {
		return result
	}
	utilization := float64(active) / float64(limit)
	result.Limit = &limit
	result.Utilization = &utilization
	return result
}

func routeHealthSnapshot(alias string, route config.Route, configured []config.Account, runtime map[string]AccountSnapshot, records []RequestRecord, now time.Time) RouteHealthSnapshot {
	configuredByID := make(map[string]config.Account, len(configured))
	for _, account := range configured {
		configuredByID[account.ID] = account
	}
	targets := routeHealthTargets(alias, route, configured)
	result := RouteHealthSnapshot{Alias: alias, Targets: len(targets), Observations: make([]RouteObservationSnapshot, 0)}
	freshRecords := make([]RequestRecord, 0)
	for _, record := range filterRecords(records, alias, "") {
		observed, err := time.Parse(time.RFC3339Nano, record.Time)
		fresh := err == nil && now.Sub(observed) <= observationFreshness && !observed.After(now.Add(time.Minute))
		result.Observations = append(result.Observations, RouteObservationSnapshot{AccountID: record.AccountID, Operation: record.Operation, Stream: record.Stream, ObservedAt: record.Time, Outcome: record.Outcome, Fresh: fresh, CredentialID: record.CredentialID, UpstreamModel: record.UpstreamModel, ReasoningEffort: record.ReasoningEffort, RequiredFeatures: record.RequiredFeatures, RequestContract: record.RequestContract, ExpectedChoices: record.ExpectedChoices, ToolMode: record.ToolMode})
		if fresh {
			freshRecords = append(freshRecords, record)
		}
	}
	for _, target := range targets {
		account, exists := runtime[target.accountID]
		configuredAccount, configuredExists := configuredByID[target.accountID]
		if !exists || !configuredExists {
			result.MissingTargets = append(result.MissingTargets, target.accountID)
			continue
		}
		scopedAvailable := func(operation, model, credential string) bool {
			canonicalModel := (&AccountRuntime{Config: configuredAccount}).breakerModelKey(model)
			return account.circuitUntilByScope[breakerKey(operation, canonicalModel, credential)] <= now.UnixNano()
		}
		if !account.Enabled || !target.available(configuredAccount, scopedAvailable) {
			continue
		}
		accountRows := make([]RequestRecord, 0)
		for _, record := range freshRecords {
			if target.matches(record) {
				accountRows = append(accountRows, record)
			}
		}
		if len(accountRows) == 0 {
			result.UnknownTargets++
			continue
		}
		failed := false
		for _, record := range accountRows {
			// Another interface being structurally available does not renew an
			// old success on an interface that is now cooling down. Unpinned
			// pools use the empty credential breaker, just as scheduling does.
			failed = failed || !recordSucceeded(record) || (record.Operation != "" && !scopedAvailable(record.Operation, target.model, target.credential))
		}
		if failed {
			result.DegradedTargets++
		} else {
			result.ReadyTargets++
		}
	}
	result.Window = summarizeLatest(filterRecords(records, alias, ""))
	unavailableTargets := result.Targets - result.ReadyTargets - result.DegradedTargets - result.UnknownTargets
	switch {
	case result.Targets == 0:
		result.State, result.Reason = HealthUnavailable, "路由没有目标"
	case len(result.MissingTargets) > 0 && result.ReadyTargets == 0 && result.DegradedTargets == 0 && result.UnknownTargets == 0:
		result.State, result.Reason = HealthUnavailable, "路由目标不存在"
	case result.ReadyTargets == 0 && result.DegradedTargets == 0 && result.UnknownTargets == 0:
		result.State, result.Reason = HealthUnavailable, "没有可用目标"
	case result.DegradedTargets > 0:
		result.State, result.Reason = HealthDegraded, "最近真实请求存在失败"
	case result.ReadyTargets > 0 && unavailableTargets > 0:
		result.State, result.Reason = HealthDegraded, "部分目标不可用"
	case result.ReadyTargets > 0 && result.Window.Samples > 0 && result.Window.Successful < result.Window.Samples:
		result.State, result.Reason = HealthDegraded, "最近真实请求失败"
	case result.ReadyTargets > 0:
		result.State, result.Reason = HealthReady, "近期调用已完成；验证范围见观测记录"
	case unavailableTargets > 0:
		result.State, result.Reason = HealthDegraded, "可用性未知且部分目标不可用"
	default:
		result.State, result.Reason = HealthUnknown, "尚无近期调用证据；历史记录不证明当前可用"
	}
	return result
}

type routeHealthTarget struct {
	accountID, credential, model, effort string
	route                                config.Route
	target                               config.RouteTarget
}

func (target routeHealthTarget) compatible(account config.Account) bool {
	if len(target.route.Targets) == 0 {
		return config.AccountSupportsTargetModel(account, target.model)
	}
	_, _, ok := config.ResolveRouteTarget(account, target.route, target.target)
	return ok
}

// A target is structurally available when at least one declared interface can
// select it. Use the same model, feature and credential boundary as scheduling,
// never the account's worst-case aggregate breaker.
func (target routeHealthTarget) available(account config.Account, available func(operation, model, credential string) bool) bool {
	if !account.Enabled || !target.compatible(account) {
		return false
	}
	operations := account.Operations
	if len(operations) == 0 {
		operations = config.DefaultOperations(account.Type)
	}
	for _, operation := range operations {
		if operation == config.OperationAnthropic && target.effort != "" && target.effort != "auto" {
			continue
		}
		if requirementsCompatible(account, target.model, operation, RequestRequirements{}, target.credential, target.effort) &&
			available(operation, target.model, target.credential) {
			return true
		}
	}
	return false
}

func (target routeHealthTarget) matches(record RequestRecord) bool {
	if record.AccountID != target.accountID {
		return false
	}
	if target.credential != "" && record.CredentialID != target.credential {
		return false
	}
	if record.UpstreamModel != target.model {
		return false
	}
	if target.effort != "" && target.effort != "auto" && record.ReasoningEffort != target.effort {
		return false
	}
	return true
}

func routeHealthTargets(alias string, route config.Route, accounts []config.Account) []routeHealthTarget {
	if len(route.Targets) > 0 {
		targets := make([]routeHealthTarget, 0, len(route.Targets))
		for _, target := range route.Targets {
			model, effort := target.Model, target.ReasoningEffort
			if account := configuredAccountByID(accounts, target.Account); account != nil && route.Model != "" {
				if resolved, resolvedEffort, compatible := config.ResolveRouteTarget(*account, route, target); compatible {
					model, effort = resolved, resolvedEffort
				}
			}
			targets = append(targets, routeHealthTarget{accountID: target.Account, credential: target.Credential, model: model, effort: effort, route: route, target: target})
		}
		return targets
	}
	if len(route.Accounts) > 0 {
		targets := make([]routeHealthTarget, 0, len(route.Accounts))
		for _, id := range route.Accounts {
			model := route.UpstreamModel
			if account := configuredAccountByID(accounts, id); account != nil {
				model = (&AccountRuntime{Config: *account}).upstreamModel(alias, route.UpstreamModel)
			}
			targets = append(targets, routeHealthTarget{accountID: id, model: model, route: route})
		}
		return targets
	}
	if !route.AllAccounts {
		return nil
	}
	targets := make([]routeHealthTarget, 0, len(accounts))
	for _, account := range accounts {
		if account.Enabled {
			model := (&AccountRuntime{Config: account}).upstreamModel(alias, route.UpstreamModel)
			targets = append(targets, routeHealthTarget{accountID: account.ID, model: model, route: route})
		}
	}
	return targets
}

func filterRecords(records []RequestRecord, alias, accountID string) []RequestRecord {
	result := make([]RequestRecord, 0, len(records))
	for _, record := range records {
		if record.Model == alias && (accountID == "" || record.AccountID == accountID) {
			result = append(result, record)
		}
	}
	return result
}

func overallHealth(routes []RouteHealthSnapshot) (HealthState, string) {
	if len(routes) == 0 {
		return HealthUnknown, "尚未配置模型路由"
	}
	unknown, degraded := false, false
	for _, route := range routes {
		switch route.State {
		case HealthUnavailable:
			return HealthUnavailable, "至少一条模型路由不可用"
		case HealthDegraded:
			degraded = true
		case HealthUnknown:
			unknown = true
		}
	}
	if degraded {
		return HealthDegraded, "至少一条模型路由降级"
	}
	if unknown {
		return HealthUnknown, "路由已配置，等待最近真实请求验证"
	}
	return HealthReady, "所有路由均有近期完成调用；不代表所有接口与特性已验证"
}
