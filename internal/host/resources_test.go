//go:build linux && amd64

package host

import (
	"github.com/plover-digital/chickadee/workerapi"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resourceFixture(t *testing.T, path string, cpu, io string) {
	t.Helper()
	for name, value := range map[string]string{"cpu.stat": cpu, "memory.current": "123456", "memory.max": "999999", "memory.events": "oom_kill 2", "io.stat": io} {
		if e := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
}
func TestResourceSamplingBaselineSelectedDeviceAndFailure(t *testing.T) {
	path := t.TempDir()
	resourceFixture(t, path, "usage_usec 1000000\nthrottled_usec 100", "252:0 rbytes=99 wbytes=999\n259:0 rbytes=10 wbytes=20")
	base, e := readResources(path, "259:0")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	s := &resourceSampler{path: path, device: "259:0", cpus: 2, start: now.Add(-2 * time.Second), previousAt: now.Add(-2 * time.Second), base: base, previous: base, summary: workerapi.ResourceSummary{Version: 1, Samples: 1, MemoryLimitBytes: 999999, IOAvailable: true}}
	resourceFixture(t, path, "usage_usec 3000000\nthrottled_usec 300", "252:0 rbytes=9999 wbytes=9999\n259:0 rbytes=110 wbytes=220")
	s.sample()
	r := s.summary
	if r.CPUUsec != 2000000 || r.CPUThrottledUsec != 200 || r.ReadBytes != 100 || r.WriteBytes != 200 || r.PeakMemoryBytes != 123456 || r.PeakCPUPercent < 49 || r.PeakCPUPercent > 50 || r.OOMKills != 0 || !r.Valid() {
		t.Fatalf("incorrect delta: %+v", r)
	}
	os.Remove(filepath.Join(path, "cpu.stat"))
	s.sample()
	if !s.summary.Partial || s.summary.ReadBytes != 100 {
		t.Fatal("missing sample inferred zero")
	}
	if newResourceSampler(path, "259:0", 2) != nil {
		t.Fatal("failed baseline accepted")
	}
}
func TestResourceSamplingWithoutDeviceOmitsIO(t *testing.T) {
	path := t.TempDir()
	resourceFixture(t, path, "usage_usec 1\nthrottled_usec 0", "invalid guest cannot supply this")
	c, e := readResources(path, "")
	if e != nil || c.read != 0 || c.write != 0 {
		t.Fatalf("I/O unavailable %v %+v", e, c)
	}
}
