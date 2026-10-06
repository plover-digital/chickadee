package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUsageIsScopeBoundAndSplitsUTCIntervals(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	r := Record{ID: "one", Scope: "https://github.com/a/repo", Label: "chickadee", Reserved: time.Date(2026, 10, 5, 23, 59, 0, 0, time.UTC), Completed: time.Date(2026, 10, 6, 0, 1, 0, 0, time.UTC)}
	days := Aggregate([]Record{r}, r.Scope, now)
	if days[5].VMSeconds != 60 || days[6].VMSeconds != 60 || days[6].VMs != 1 {
		t.Fatal(days)
	}
	other := Aggregate([]Record{r}, "https://github.com/b/repo", now)
	for _, day := range other {
		if day.VMSeconds != 0 || day.VMs != 0 {
			t.Fatal("customer usage crossed scopes")
		}
	}
}
func TestCompletedUsageIsPrivateAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	r := Record{ID: "one", Scope: "https://github.com/a/repo", Label: "chickadee", Reserved: now.Add(-time.Minute), Completed: now}
	if err := Append(dir, r); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, r); err != nil {
		t.Fatal(err)
	}
	records, err := Load(dir)
	if err != nil || len(records) != 1 {
		t.Fatal("duplicate accounting")
	}
	info, _ := os.Stat(filepath.Join(dir, "usage.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("usage metadata exposed")
	}
	r.Completed = r.Reserved.Add(-time.Second)
	if Append(dir, r) == nil {
		t.Fatal("negative interval accepted")
	}
}
