package gateway

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	trendBucketDuration = time.Minute
	trendRetention      = 7 * 24 * time.Hour
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

// TrendPoint is a compact, minute-level data point for the overview charts.
// It intentionally contains no request or account identifiers: detailed
// diagnosis continues to use StatsSnapshot.Recent.
type TrendPoint struct {
	Time         string `json:"time"`
	Requests     int    `json:"requests"`
	Failed       int    `json:"failed"`
	P95LatencyMS *int64 `json:"p95_latency_ms"`
}

type TrendSnapshot struct {
	RangeSeconds     int64        `json:"range_seconds"`
	BucketSeconds    int64        `json:"bucket_seconds"`
	RetentionSeconds int64        `json:"retention_seconds"`
	Points           []TrendPoint `json:"points"`
}

type trendBucket struct {
	start     time.Time
	requests  int
	failed    int
	latencies []int64
}

type Stats struct {
	started      time.Time
	requests     atomic.Int64
	success      atomic.Int64
	failed       atomic.Int64
	failovers    atomic.Int64
	active       atomic.Int64
	mu           sync.RWMutex
	recent       []RequestRecord
	limit        int
	next         int
	trend        []TrendPoint
	trendNext    int
	trendLimit   int
	currentTrend trendBucket
}

func NewStats(limit int) *Stats {
	return NewStatsWithTrend(limit, trendRetention)
}

func NewStatsWithTrend(limit int, retention time.Duration) *Stats {
	trendLimit := int(retention / trendBucketDuration)
	if trendLimit < 1 {
		trendLimit = 1
	}
	return &Stats{started: time.Now(), limit: limit, trendLimit: trendLimit}
}
func (s *Stats) Begin() { s.requests.Add(1); s.active.Add(1) }
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
	observed := recordTime(r.Time)
	s.mu.Lock()
	s.recordTrendLocked(r, observed)
	if s.limit > 0 {
		if len(s.recent) < s.limit {
			s.recent = append(s.recent, r)
			s.next = len(s.recent) % s.limit
		} else {
			s.recent[s.next] = r
			s.next = (s.next + 1) % s.limit
		}
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

func (s *Stats) Trend(now time.Time, duration time.Duration) TrendSnapshot {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	if duration <= 0 {
		duration = 24 * time.Hour
	}
	if duration > trendRetention {
		duration = trendRetention
	}
	cutoff := now.Add(-duration)

	s.mu.RLock()
	points := make([]TrendPoint, 0, len(s.trend)+1)
	if len(s.trend) > 0 {
		start := 0
		if len(s.trend) == s.trendLimit {
			start = s.trendNext
		}
		for offset := 0; offset < len(s.trend); offset++ {
			point := s.trend[(start+offset)%len(s.trend)]
			pointTime, err := time.Parse(time.RFC3339Nano, point.Time)
			if err != nil || pointTime.Before(cutoff) || pointTime.After(now.Add(trendBucketDuration)) {
				continue
			}
			points = append(points, point)
		}
	}
	if !s.currentTrend.start.IsZero() {
		point := trendPoint(s.currentTrend)
		if pointTime, err := time.Parse(time.RFC3339Nano, point.Time); err == nil && !pointTime.Before(cutoff) && !pointTime.After(now.Add(trendBucketDuration)) {
			points = append(points, point)
		}
	}
	s.mu.RUnlock()

	sort.Slice(points, func(i, j int) bool { return points[i].Time < points[j].Time })
	return TrendSnapshot{
		RangeSeconds:     int64(duration.Seconds()),
		BucketSeconds:    int64(trendBucketDuration.Seconds()),
		RetentionSeconds: int64(trendRetention.Seconds()),
		Points:           points,
	}
}

func (s *Stats) recordTrendLocked(record RequestRecord, observed time.Time) {
	if s.trendLimit <= 0 {
		return
	}
	bucketStart := observed.UTC().Truncate(trendBucketDuration)
	if s.currentTrend.start.IsZero() {
		s.currentTrend.start = bucketStart
	} else if bucketStart.After(s.currentTrend.start) {
		s.appendTrendLocked(trendPoint(s.currentTrend))
		s.currentTrend = trendBucket{start: bucketStart}
	} else if bucketStart.Before(s.currentTrend.start) {
		// Gateway records completion time, so out-of-order samples are unusual.
		// Do not rewrite an already closed bucket when a late record arrives.
		return
	}
	s.currentTrend.requests++
	if record.Status < 200 || record.Status >= 400 {
		s.currentTrend.failed++
	}
	latency := record.LatencyMS
	if latency < 0 {
		latency = 0
	}
	s.currentTrend.latencies = append(s.currentTrend.latencies, latency)
}

func (s *Stats) appendTrendLocked(point TrendPoint) {
	if s.trendLimit <= 0 {
		return
	}
	if len(s.trend) < s.trendLimit {
		s.trend = append(s.trend, point)
		s.trendNext = len(s.trend) % s.trendLimit
		return
	}
	s.trend[s.trendNext] = point
	s.trendNext = (s.trendNext + 1) % s.trendLimit
}

func trendPoint(bucket trendBucket) TrendPoint {
	point := TrendPoint{Time: bucket.start.UTC().Format(time.RFC3339Nano), Requests: bucket.requests, Failed: bucket.failed}
	if len(bucket.latencies) == 0 {
		return point
	}
	latencies := append([]int64(nil), bucket.latencies...)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	index := (95*len(latencies) + 99) / 100
	value := latencies[index-1]
	point.P95LatencyMS = &value
	return point
}

func recordTime(value string) time.Time {
	if observed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return observed
	}
	return time.Now().UTC()
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
