package gateway

import (
	"sort"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

type HealthState string

const (
	HealthUnknown     HealthState = "unknown"
	HealthReady       HealthState = "ready"
	HealthDegraded    HealthState = "degraded"
	HealthUnavailable HealthState = "unavailable"
)

type WindowSnapshot struct {
	DurationSeconds int64    `json:"duration_seconds"`
	Samples         int      `json:"samples"`
	Successful      int      `json:"successful"`
	SuccessRate     *float64 `json:"success_rate"`
	P95LatencyMS    *int64   `json:"p95_latency_ms"`
	ObservedAt      string   `json:"observed_at,omitempty"`
}

type CapacitySnapshot struct {
	Active      int64    `json:"active"`
	Limit       *int64   `json:"limit"`
	Utilization *float64 `json:"utilization"`
	Unlimited   bool     `json:"unlimited"`
}

type RouteHealthSnapshot struct {
	Alias           string         `json:"alias"`
	State           HealthState    `json:"state"`
	Reason          string         `json:"reason"`
	Targets         int            `json:"targets"`
	ReadyTargets    int            `json:"ready_targets"`
	DegradedTargets int            `json:"degraded_targets"`
	UnknownTargets  int            `json:"unknown_targets"`
	MissingTargets  []string       `json:"missing_targets,omitempty"`
	Window          WindowSnapshot `json:"window"`
}

type OperationsSnapshot struct {
	GeneratedAt string                `json:"generated_at"`
	State       HealthState           `json:"state"`
	Reason      string                `json:"reason"`
	Window      WindowSnapshot        `json:"window"`
	Capacity    CapacitySnapshot      `json:"capacity"`
	Routes      []RouteHealthSnapshot `json:"routes"`
}

func buildOperationsSnapshot(now time.Time, cfg config.Config, accounts []AccountSnapshot, stats StatsSnapshot) OperationsSnapshot {
	accountByID := make(map[string]AccountSnapshot, len(accounts))
	for _, account := range accounts {
		accountByID[account.ID] = account
	}

	// Health metrics intentionally use the latest real request retained by the
	// gateway, regardless of its age. A quiet installation should not become
	// unknown merely because the last verified request was days ago.
	requestRows := validOperationRecords(stats.Recent, now)
	routes := make([]RouteHealthSnapshot, 0, len(cfg.Routes))
	for alias, route := range cfg.Routes {
		routes = append(routes, routeHealthSnapshot(alias, route, cfg.Accounts, accountByID, requestRows))
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Alias < routes[j].Alias })

	result := OperationsSnapshot{
		GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Window:      summarizeLatest(requestRows),
		Capacity:    summarizeCapacity(accounts, stats.Active),
		Routes:      routes,
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
	result := WindowSnapshot{Samples: len(records)}
	if len(records) == 0 {
		return result
	}
	result.ObservedAt = records[0].Time
	latencies := make([]int64, 0, 1)
	for _, record := range records {
		if record.Status >= 200 && record.Status < 400 {
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

func routeHealthSnapshot(alias string, route config.Route, configured []config.Account, runtime map[string]AccountSnapshot, records []RequestRecord) RouteHealthSnapshot {
	configuredByID := make(map[string]config.Account, len(configured))
	for _, account := range configured {
		configuredByID[account.ID] = account
	}
	targetIDs := routeTargetIDs(route, configured)
	result := RouteHealthSnapshot{Alias: alias, Targets: len(targetIDs)}
	for _, id := range targetIDs {
		account, exists := runtime[id]
		configuredAccount, configuredExists := configuredByID[id]
		if !exists || !configuredExists {
			result.MissingTargets = append(result.MissingTargets, id)
			continue
		}
		if !routeTargetCompatible(route, configuredAccount, id) || !account.Enabled || account.CircuitOpenUntil != "" {
			continue
		}
		accountRows := latestOperationRecords(filterRecords(records, alias, id))
		if len(accountRows) == 0 {
			result.UnknownTargets++
			continue
		}
		accountWindow := summarizeLatest(accountRows)
		if accountWindow.Successful == accountWindow.Samples {
			result.ReadyTargets++
		} else {
			result.DegradedTargets++
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
		result.State, result.Reason = HealthReady, "最近真实请求已验证"
	case unavailableTargets > 0:
		result.State, result.Reason = HealthDegraded, "可用性未知且部分目标不可用"
	default:
		result.State, result.Reason = HealthUnknown, "尚无真实请求样本"
	}
	return result
}

func routeTargetIDs(route config.Route, accounts []config.Account) []string {
	if len(route.Targets) > 0 {
		ids := make([]string, 0, len(route.Targets))
		for _, target := range route.Targets {
			ids = append(ids, target.Account)
		}
		return ids
	}
	if len(route.Accounts) > 0 {
		return append([]string(nil), route.Accounts...)
	}
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if account.Enabled {
			ids = append(ids, account.ID)
		}
	}
	return ids
}

func routeTargetCompatible(route config.Route, account config.Account, id string) bool {
	if len(route.Targets) == 0 {
		return true
	}
	for _, target := range route.Targets {
		if target.Account != id {
			continue
		}
		_, _, compatible := config.ResolveRouteTarget(account, route, target)
		return compatible
	}
	return false
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
	return HealthReady, "所有模型路由均已由最近真实请求验证"
}
