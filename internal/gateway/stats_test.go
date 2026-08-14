package gateway

import (
	"fmt"
	"testing"
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
