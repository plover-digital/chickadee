//go:build linux && amd64

package host

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/plover-digital/chickadee/workerapi"
)

type resourceCounters struct{ cpu, throttle, memory, limit, oom, read, write uint64 }
type resourceSampler struct {
	mu                sync.Mutex
	path, device      string
	cpus              int
	start, previousAt time.Time
	base, previous    resourceCounters
	summary           workerapi.ResourceSummary
	stop, done        chan struct{}
}

func numericFile(path, name string) (uint64, error) {
	data, err := readControl(path, name)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(data, 10, 64)
}
func keyedFile(path, name string) (map[string]uint64, error) {
	data, err := readControl(path, name)
	if err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("invalid counters")
		}
		n, e := strconv.ParseUint(f[1], 10, 64)
		if e != nil {
			return nil, e
		}
		out[f[0]] = n
	}
	return out, nil
}
func readResources(path, device string) (resourceCounters, error) {
	var c resourceCounters
	cpu, e := keyedFile(path, "cpu.stat")
	if e != nil {
		return c, e
	}
	var ok bool
	if c.cpu, ok = cpu["usage_usec"]; !ok {
		return c, fmt.Errorf("missing CPU counter")
	}
	if c.throttle, ok = cpu["throttled_usec"]; !ok {
		return c, fmt.Errorf("missing throttle counter")
	}
	if c.memory, e = numericFile(path, "memory.current"); e != nil {
		return c, e
	}
	if c.limit, e = numericFile(path, "memory.max"); e != nil {
		return c, e
	}
	events, e := keyedFile(path, "memory.events")
	if e != nil {
		return c, e
	}
	if c.oom, ok = events["oom_kill"]; !ok {
		return c, fmt.Errorf("missing OOM counter")
	}
	if device != "" {
		data, e := readControl(path, "io.stat")
		if e != nil {
			return c, e
		}
		// Absence of the selected device means zero I/O has yet been charged.
		for _, line := range strings.Split(data, "\n") {
			f := strings.Fields(line)
			if len(f) == 0 || f[0] != device {
				continue
			}
			seenRead, seenWrite := false, false
			for _, v := range f[1:] {
				kv := strings.SplitN(v, "=", 2)
				if len(kv) != 2 {
					return c, fmt.Errorf("invalid I/O counter")
				}
				n, e := strconv.ParseUint(kv[1], 10, 64)
				if e != nil {
					return c, e
				}
				if kv[0] == "rbytes" {
					c.read = n
					seenRead = true
				}
				if kv[0] == "wbytes" {
					c.write = n
					seenWrite = true
				}
			}
			if !seenRead || !seenWrite {
				return c, fmt.Errorf("missing I/O counters")
			}
		}
	}
	return c, nil
}
func delta(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}
func newResourceSampler(path, device string, cpus int) *resourceSampler {
	c, e := readResources(path, device)
	if e != nil {
		return nil
	}
	now := time.Now()
	s := &resourceSampler{path: path, device: device, cpus: cpus, start: now, previousAt: now, base: c, previous: c, stop: make(chan struct{}), done: make(chan struct{}), summary: workerapi.ResourceSummary{Version: 1, Samples: 1, PeakMemoryBytes: c.memory, MemoryLimitBytes: c.limit, IOAvailable: device != ""}}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.sample()
			case <-s.stop:
				s.sample()
				return
			}
		}
	}()
	return s
}
func (s *resourceSampler) sample() {
	c, e := readResources(s.path, s.device)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e != nil {
		s.summary.Partial = true
		return
	}
	if c.cpu < s.previous.cpu || c.throttle < s.previous.throttle || c.oom < s.previous.oom || c.read < s.previous.read || c.write < s.previous.write {
		s.summary.Partial = true
		return
	}
	elapsed := now.Sub(s.previousAt).Seconds()
	if elapsed <= 0 {
		return
	}
	s.summary.Samples++
	s.summary.DurationMillis = uint64(now.Sub(s.start).Milliseconds())
	s.summary.CPUUsec = delta(c.cpu, s.base.cpu)
	s.summary.CPUThrottledUsec = delta(c.throttle, s.base.throttle)
	percent := uint64(float64(delta(c.cpu, s.previous.cpu)) / 1e6 / elapsed / float64(s.cpus) * 100)
	s.summary.PeakCPUPercent = max(s.summary.PeakCPUPercent, percent)
	s.summary.PeakMemoryBytes = max(s.summary.PeakMemoryBytes, c.memory)
	s.summary.OOMKills = delta(c.oom, s.base.oom)
	s.summary.ReadBytes = delta(c.read, s.base.read)
	s.summary.WriteBytes = delta(c.write, s.base.write)
	s.summary.PeakReadBPS = max(s.summary.PeakReadBPS, uint64(float64(delta(c.read, s.previous.read))/elapsed))
	s.summary.PeakWriteBPS = max(s.summary.PeakWriteBPS, uint64(float64(delta(c.write, s.previous.write))/elapsed))
	s.previous = c
	s.previousAt = now
}
func (s *resourceSampler) finish() *workerapi.ResourceSummary {
	if s == nil {
		return nil
	}
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.summary
	if !out.Valid() {
		return nil
	}
	return &out
}

// StartResourceSampling excludes boot and credential-free warm time. Sampling
// errors are nonfatal and cannot interfere with credential/lifecycle decisions.
func (v *VM) StartResourceSampling() {
	if v.cgroup == nil {
		return
	}
	v.resources = newResourceSampler(v.cgroup.path, v.ioDevice, v.resourceCPUs)
}
func (v *VM) ResourceSummary() *workerapi.ResourceSummary { return v.resourceSummary }
