package gateway

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestStatsRecentRingNewestFirst(t *testing.T) {
	stats := NewStats(3)
	for i := 1; i <= 5; i++ {
		stats.Record(RequestRecord{RequestID: fmt.Sprint(i)})
	}
	recent := stats.Snapshot().Recent
	if len(recent) != 3 {
		t.Fatalf("recent length=%d", len(recent))
	}
	want := []string{"5", "4", "3"}
	for i := range want {
		if recent[i].RequestID != want[i] {
			t.Fatalf("recent[%d]=%q want %q", i, recent[i].RequestID, want[i])
		}
	}
}

func TestStatsBoundsClientControlledObservationFields(t *testing.T) {
	stats := NewStats(2)
	stats.Record(RequestRecord{
		Model: strings.Repeat("模型", 1024), UpstreamModel: strings.Repeat("u", 4096),
		ClientKeyName: strings.Repeat("k", 4096), Error: strings.Repeat("e", 4096),
	})
	record := stats.Snapshot().Recent[0]
	if len(record.Model) > maxGatewayModelBytes || len(record.UpstreamModel) > maxGatewayModelBytes || len(record.ClientKeyName) > 128 || len(record.Error) > 1024 {
		t.Fatalf("record exceeded observation budget: model=%d upstream=%d key=%d error=%d", len(record.Model), len(record.UpstreamModel), len(record.ClientKeyName), len(record.Error))
	}
	if !utf8.ValidString(record.Model) {
		t.Fatal("bounded model is not valid UTF-8")
	}
}

func TestTrendLatencyMemoryIsBounded(t *testing.T) {
	stats := NewStatsWithTrend(0, time.Minute)
	now := time.Now().UTC().Truncate(time.Minute)
	for index := 0; index < maxTrendSamples*4; index++ {
		stats.Record(RequestRecord{Time: now.Format(time.RFC3339Nano), Status: 200, LatencyMS: int64(index)})
	}
	if got := len(stats.currentTrend.latencySamples); got != maxTrendSamples {
		t.Fatalf("latency samples=%d want bounded %d", got, maxTrendSamples)
	}
}

func TestTrendUsesOutcomeInsteadOfSuccessfulHeaders(t *testing.T) {
	stats := NewStatsWithTrend(1, time.Minute)
	now := time.Now().UTC().Truncate(time.Minute)
	stats.Record(RequestRecord{Time: now.Format(time.RFC3339Nano), Status: 200, Outcome: "stream_error", Error: "stream interrupted"})
	trend := stats.Trend(now.Add(time.Second), time.Minute)
	if len(trend.Points) != 1 || trend.Points[0].Failed != 1 {
		t.Fatalf("stream failure was counted as success: %+v", trend.Points)
	}
}

func TestStatsTrendUsesMinutePointsAndCircularRetention(t *testing.T) {
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	stats := NewStatsWithTrend(0, 3*time.Minute)
	for _, sample := range []struct {
		offset  time.Duration
		status  int
		latency int64
	}{
		{0, 200, 100},
		{10 * time.Second, 500, 800},
		{time.Minute, 200, 300},
		{2 * time.Minute, 200, 500},
		{3 * time.Minute, 200, 700},
	} {
		stats.Record(RequestRecord{
			Time:      base.Add(sample.offset).Format(time.RFC3339Nano),
			Status:    sample.status,
			LatencyMS: sample.latency,
		})
	}

	trend := stats.Trend(base.Add(3*time.Minute+30*time.Second), 3*time.Minute)
	if len(trend.Points) != 3 {
		t.Fatalf("trend points=%d want 3: %+v", len(trend.Points), trend.Points)
	}
	if trend.Points[0].Time != base.Add(time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("oldest retained point=%s", trend.Points[0].Time)
	}
	if trend.Points[0].Requests != 1 || trend.Points[0].Failed != 0 || trend.Points[0].P95LatencyMS == nil || *trend.Points[0].P95LatencyMS != 300 {
		t.Fatalf("minute point=%+v", trend.Points[0])
	}
}

func TestParseTrendRange(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"", 24 * time.Hour},
		{"1h", time.Hour},
		{"6h", 6 * time.Hour},
		{"24h", 24 * time.Hour},
		{"3d", 3 * 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"all", 7 * 24 * time.Hour},
	} {
		got, err := parseTrendRange(test.value)
		if err != nil || got != test.want {
			t.Fatalf("parseTrendRange(%q)=%s, %v; want %s", test.value, got, err, test.want)
		}
	}
	if _, err := parseTrendRange("30d"); err == nil {
		t.Fatal("unsupported trend range should fail")
	}
}
