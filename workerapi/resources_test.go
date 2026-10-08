package workerapi

import "testing"

func TestResourceSummaryBounds(t *testing.T) {
	if !((*ResourceSummary)(nil)).Valid() {
		t.Fatal("legacy absence rejected")
	}
	good := ResourceSummary{Version: 1, Samples: 2, DurationMillis: 1000, MemoryLimitBytes: 1024}
	if !good.Valid() {
		t.Fatal("valid rejected")
	}
	invalid := []ResourceSummary{good, good, good, good}
	invalid[0].Version = 2
	invalid[1].Samples = 50001
	invalid[2].ReadBytes = 1
	invalid[3].PeakCPUPercent = 10001
	for _, s := range invalid {
		if s.Valid() {
			t.Fatalf("invalid accepted: %+v", s)
		}
	}
}
