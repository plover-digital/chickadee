package workerapi

// ResourceSummary v1 contains host cgroup measurements for one credentialed VM.
// Missing summaries are unavailable, never inferred zero. No guest strings or
// physical device/host identities are included. CPU peaks are interval averages.
type ResourceSummary struct {
	Version          int    `json:"version"`
	Samples          uint64 `json:"samples"`
	DurationMillis   uint64 `json:"duration_ms"`
	CPUUsec          uint64 `json:"cpu_usec"`
	CPUThrottledUsec uint64 `json:"cpu_throttled_usec"`
	PeakCPUPercent   uint64 `json:"peak_cpu_percent"`
	PeakMemoryBytes  uint64 `json:"peak_memory_bytes"`
	MemoryLimitBytes uint64 `json:"memory_limit_bytes"`
	OOMKills         uint64 `json:"oom_kills"`
	ReadBytes        uint64 `json:"read_bytes"`
	WriteBytes       uint64 `json:"write_bytes"`
	PeakReadBPS      uint64 `json:"peak_read_bps"`
	PeakWriteBPS     uint64 `json:"peak_write_bps"`
	IOAvailable      bool   `json:"io_available"`
	Partial          bool   `json:"partial"`
}

func (s *ResourceSummary) Valid() bool {
	if s == nil {
		return true
	}
	if s.Version != 1 || s.Samples < 1 || s.Samples > 50000 || s.DurationMillis > 25*3600*1000 || s.PeakCPUPercent > 10000 || s.MemoryLimitBytes == 0 || s.MemoryLimitBytes > 1<<41 || s.PeakMemoryBytes > 1<<41 || s.CPUUsec > 25*3600*1000000*256 || s.CPUThrottledUsec > 25*3600*1000000*256 || s.OOMKills > 100000 || s.ReadBytes > 1<<60 || s.WriteBytes > 1<<60 || s.PeakReadBPS > 1<<50 || s.PeakWriteBPS > 1<<50 {
		return false
	}
	return s.IOAvailable || s.ReadBytes == 0 && s.WriteBytes == 0 && s.PeakReadBPS == 0 && s.PeakWriteBPS == 0
}
