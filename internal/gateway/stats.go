package gateway

import (
	"sync"
	"sync/atomic"
	"time"
)

type RequestRecord struct {
	Time             string  `json:"time"`
	RequestID        string  `json:"request_id"`
	Model            string  `json:"model"`
	UpstreamModel    string  `json:"upstream_model"`
	AccountID        string  `json:"account_id"`
	Operation        string  `json:"operation,omitempty"`
	ReasoningEffort  string  `json:"reasoning_effort,omitempty"`
	ClientKeyID      string  `json:"client_key_id"`
	ClientKeyName    string  `json:"client_key_name,omitempty"`
	Path             string  `json:"path"`
	Status           int     `json:"status"`
	LatencyMS        int64   `json:"latency_ms"`
	InputType        string  `json:"input_type,omitempty"`
	OutputType       string  `json:"output_type,omitempty"`
	InputParts       int     `json:"input_parts,omitempty"`
	TextParts        int     `json:"text_parts,omitempty"`
	ImageParts       int     `json:"image_parts,omitempty"`
	AudioParts       int     `json:"audio_parts,omitempty"`
	VideoParts       int     `json:"video_parts,omitempty"`
	FileParts        int     `json:"file_parts,omitempty"`
	RequestBytes     int64   `json:"request_bytes,omitempty"`
	ResponseBytes    int64   `json:"response_bytes,omitempty"`
	Stream           bool    `json:"stream,omitempty"`
	UsageAvailable   bool    `json:"usage_available,omitempty"`
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
	CachedTokens     int64   `json:"cached_tokens,omitempty"`
	CacheWriteTokens int64   `json:"cache_write_tokens,omitempty"`
	CacheRate        float64 `json:"cache_rate,omitempty"`
	CacheRateKnown   bool    `json:"cache_rate_known,omitempty"`
	Error            string  `json:"error,omitempty"`
}

type StatsSnapshot struct {
	StartedAt   string          `json:"started_at"`
	Requests    int64           `json:"requests"`
	Successful  int64           `json:"successful"`
	Failed      int64           `json:"failed"`
	Failovers   int64           `json:"failovers"`
	Active      int64           `json:"active"`
	RecentLimit int             `json:"recent_limit"`
	Recent      []RequestRecord `json:"recent"`
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
	r.Error = truncate(r.Error, 1024)
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
	return StatsSnapshot{StartedAt: s.started.UTC().Format(time.RFC3339), Requests: s.requests.Load(), Successful: s.success.Load(), Failed: s.failed.Load(), Failovers: s.failovers.Load(), Active: s.active.Load(), RecentLimit: s.limit, Recent: recent}
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
