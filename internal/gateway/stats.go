package gateway

import (
	"sync"
	"sync/atomic"
	"time"
)

type RequestRecord struct {
	Time          string `json:"time"`
	RequestID     string `json:"request_id"`
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model"`
	AccountID     string `json:"account_id"`
	ClientKeyID   string `json:"client_key_id"`
	ClientKeyName string `json:"client_key_name,omitempty"`
	Path          string `json:"path"`
	Status        int    `json:"status"`
	LatencyMS     int64  `json:"latency_ms"`
	Error         string `json:"error,omitempty"`
}

type StatsSnapshot struct {
	StartedAt  string          `json:"started_at"`
	Requests   int64           `json:"requests"`
	Successful int64           `json:"successful"`
	Failed     int64           `json:"failed"`
	Failovers  int64           `json:"failovers"`
	Active     int64           `json:"active"`
	Recent     []RequestRecord `json:"recent"`
}

type Stats struct {
	started   time.Time
	requests  atomic.Int64
	success   atomic.Int64
	failed    atomic.Int64
	failovers atomic.Int64
	active    atomic.Int64
	mu        sync.RWMutex
	recent    []RequestRecord
	limit     int
	next      int
}

func NewStats(limit int) *Stats { return &Stats{started: time.Now(), limit: limit} }
func (s *Stats) Begin()         { s.requests.Add(1); s.active.Add(1) }
func (s *Stats) End(ok bool) {
	s.active.Add(-1)
	if ok {
		s.success.Add(1)
	} else {
		s.failed.Add(1)
	}
}
func (s *Stats) Failover() { s.failovers.Add(1) }
func (s *Stats) Record(r RequestRecord) {
	if s.limit <= 0 {
		return
	}
	s.mu.Lock()
	if len(s.recent) < s.limit {
		s.recent = append(s.recent, r)
		s.next = len(s.recent) % s.limit
	} else {
		s.recent[s.next] = r
		s.next = (s.next + 1) % s.limit
	}
	s.mu.Unlock()
}
func (s *Stats) Snapshot() StatsSnapshot {
	s.mu.RLock()
	recent := make([]RequestRecord, len(s.recent))
	for i := range recent {
		index := (s.next - 1 - i + len(s.recent)) % len(s.recent)
		recent[i] = s.recent[index]
	}
	s.mu.RUnlock()
	return StatsSnapshot{StartedAt: s.started.UTC().Format(time.RFC3339), Requests: s.requests.Load(), Successful: s.success.Load(), Failed: s.failed.Load(), Failovers: s.failovers.Load(), Active: s.active.Load(), Recent: recent}
}
