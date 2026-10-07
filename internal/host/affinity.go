//go:build linux && amd64

package host

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const MaxAffinityCPU = 8191

// AllowedCPUs reports the calling thread's allowed Linux CPU mask. The worker
// never changes its own affinity; systemd/cgroup constraints remain authoritative.
func AllowedCPUs() ([]int, error) {
	mask := make([]byte, (MaxAffinityCPU+1)/8)
	n, _, errno := syscall.RawSyscall(syscall.SYS_SCHED_GETAFFINITY, 0, uintptr(len(mask)), uintptr(unsafe.Pointer(&mask[0])))
	if errno != 0 || n == 0 || n > uintptr(len(mask)) {
		return nil, fmt.Errorf("process CPU affinity unavailable")
	}
	var ids []int
	for i := 0; i < int(n)*8; i++ {
		if mask[i/8]&(1<<uint(i%8)) != 0 {
			ids = append(ids, i)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("empty process CPU affinity")
	}
	return ids, nil
}
func onlineCPUs(data string) (map[int]bool, error) {
	online := map[int]bool{}
	for _, part := range strings.Split(strings.TrimSpace(data), ",") {
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return nil, fmt.Errorf("invalid online CPU list")
		}
		first, err := strconv.Atoi(bounds[0])
		if err != nil || first < 0 || first > MaxAffinityCPU {
			return nil, fmt.Errorf("invalid online CPU list")
		}
		last := first
		if len(bounds) == 2 {
			last, err = strconv.Atoi(bounds[1])
			if err != nil || last < first || last > MaxAffinityCPU {
				return nil, fmt.Errorf("invalid online CPU range")
			}
		}
		for id := first; id <= last; id++ {
			if online[id] {
				return nil, fmt.Errorf("duplicate online CPU")
			}
			online[id] = true
		}
	}
	return online, nil
}

// ValidateCPUSet fails closed against current process restrictions, online CPUs
// and the host taskset dependency. An empty set leaves existing behavior intact.
func ValidateCPUSet(ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > 256 {
		return fmt.Errorf("CPU affinity list exceeds limit")
	}
	allowed, err := AllowedCPUs()
	if err != nil {
		return err
	}
	mask := map[int]bool{}
	for _, id := range allowed {
		mask[id] = true
	}
	file, err := os.Open("/sys/devices/system/cpu/online")
	if err != nil {
		return fmt.Errorf("online CPU inventory unavailable")
	}
	defer file.Close()
	data := make([]byte, 64*1024)
	n, err := file.Read(data)
	if err != nil || n == len(data) {
		return fmt.Errorf("online CPU inventory unavailable")
	}
	online, err := onlineCPUs(string(data[:n]))
	if err != nil {
		return err
	}
	seen := map[int]bool{}
	for _, id := range ids {
		if id < 0 || id > MaxAffinityCPU || seen[id] || !mask[id] || !online[id] {
			return fmt.Errorf("configured CPU is duplicate, offline or outside process affinity")
		}
		seen[id] = true
	}
	if _, err = exec.LookPath("taskset"); err != nil {
		return fmt.Errorf("CPU affinity requires util-linux taskset")
	}
	return nil
}

// AffinityCommand confines all descendant threads before execution. It neither
// pins individual vCPU threads nor reserves these CPUs from other host processes.
// Caller retains command environment, inherited FDs and process recovery setup.
func AffinityCommand(command string, args []string, ids []int) (*exec.Cmd, error) {
	if len(ids) == 0 {
		return exec.Command(command, args...), nil
	}
	if err := ValidateCPUSet(ids); err != nil {
		return nil, err
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	pinned := append([]string{"--cpu-list", strings.Join(parts, ","), command}, args...)
	return exec.Command("taskset", pinned...), nil
}
