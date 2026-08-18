package gateway

import (
	"fmt"
	"testing"
	"time"
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
