//go:build linux && amd64

package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/landlockrunner"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type Budget struct {
	MaxVMs       int `json:"max_vms"`
	MaxCPUs      int `json:"max_cpus"`
	MaxMemoryMiB int `json:"max_memory_mib"`
}
type Profile struct {
	HostAccess *landlockrunner.HostAccess `json:"host_access,omitempty"`
	ID         string                     `json:"id"`
	Digest     string                     `json:"digest"`
	ImageDir   string                     `json:"image_dir"`
	Machine    string                     `json:"machine"`
	CPUs       int                        `json:"cpus"`
	MemoryMiB  int                        `json:"memory_mib"`
	DiskGiB    int                        `json:"disk_gib"`
	Warm       int                        `json:"warm_pool"`
}
type Config struct {
	ProtectedPaths            []string           `json:"-"`
	LandlockDenyPrefixes      []string           `json:"landlock_deny_prefixes,omitempty"`
	Cgroup                    *host.CgroupConfig `json:"cgroup,omitempty"`
	LogRetentionMiB           int                `json:"log_retention_mib,omitempty"`
	MinFreeDiskGiB            int                `json:"min_free_disk_gib,omitempty"`
	CPUIDs                    []int              `json:"cpu_ids,omitempty"`
	Version                   int                `json:"version"`
	Identity                  Identity           `json:"identity"`
	StateDir                  string             `json:"state_dir"`
	Profiles                  []Profile          `json:"profiles"`
	Budget                    Budget             `json:"budget"`
	BootTimeoutSeconds        int                `json:"boot_timeout_seconds"`
	JobTimeoutSeconds         int                `json:"job_timeout_seconds"`
	ReservationTimeoutSeconds int                `json:"reservation_timeout_seconds"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	file, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 128*1024))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid worker config")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("trailing worker config data")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if err := host.ValidateCgroupConfig(c.Cgroup); err != nil {
		return err
	}
	if c.Version != Version || !validIdentity(c.Identity) {
		return fmt.Errorf("invalid worker version/identity")
	}
	if !filepath.IsAbs(c.StateDir) || filepath.Clean(c.StateDir) != c.StateDir || c.StateDir == "/" {
		return fmt.Errorf("private absolute worker state directory required")
	}
	if c.LogRetentionMiB < 0 || c.LogRetentionMiB > 65536 {
		return fmt.Errorf("invalid diagnostic retention budget")
	}
	if c.MinFreeDiskGiB < 0 || c.MinFreeDiskGiB > 1048576 {
		return fmt.Errorf("invalid free disk reserve")
	}
	if c.Budget.MaxVMs < 1 || c.Budget.MaxVMs > 32 || c.Budget.MaxCPUs < 1 || c.Budget.MaxCPUs > 256 || c.Budget.MaxMemoryMiB < 1 || c.Budget.MaxMemoryMiB > 1<<20 {
		return fmt.Errorf("invalid worker budget")
	}
	if c.BootTimeoutSeconds < 1 || c.BootTimeoutSeconds > 300 || c.JobTimeoutSeconds < 1 || c.JobTimeoutSeconds > 14400 || c.ReservationTimeoutSeconds < 1 || c.ReservationTimeoutSeconds > 180 {
		return fmt.Errorf("invalid worker deadlines")
	}
	if len(c.Profiles) < 1 || len(c.Profiles) > 32 {
		return fmt.Errorf("invalid profile catalog size")
	}
	if len(c.CPUIDs) > 0 {
		if len(c.CPUIDs) > 256 || len(c.CPUIDs) < c.Budget.MaxCPUs {
			return fmt.Errorf("CPU pool must cover the worker CPU budget")
		}
		seenCPUs := map[int]bool{}
		for _, id := range c.CPUIDs {
			if id < 0 || id > host.MaxAffinityCPU || seenCPUs[id] {
				return fmt.Errorf("invalid or duplicate CPU pool ID")
			}
			seenCPUs[id] = true
		}
	}
	seen := map[string]bool{}
	warms, cpus, memory := 0, 0, 0
	for _, p := range c.Profiles {
		if !idPattern.MatchString(p.ID) || seen[p.ID] || !digestPattern.MatchString(p.Digest) || !filepath.IsAbs(p.ImageDir) || filepath.Clean(p.ImageDir) != p.ImageDir || p.ImageDir == "/" || p.ImageDir == c.StateDir || strings.HasPrefix(p.ImageDir, c.StateDir+"/") || strings.HasPrefix(c.StateDir, p.ImageDir+"/") {
			return fmt.Errorf("invalid trusted profile identity/path")
		}
		seen[p.ID] = true
		if p.Machine != "microvm" && p.Machine != "q35" && p.Machine != "landlock" {
			return fmt.Errorf("invalid profile machine")
		}
		if p.CPUs < 1 || p.CPUs > c.Budget.MaxCPUs || p.MemoryMiB < 512 || p.MemoryMiB > c.Budget.MaxMemoryMiB || p.DiskGiB < 8 || p.DiskGiB > 1024 || p.Warm < 0 || p.Warm > c.Budget.MaxVMs {
			return fmt.Errorf("profile exceeds worker resource budget")
		}
		warms += p.Warm
		cpus += p.Warm * p.CPUs
		memory += p.Warm * p.MemoryMiB
	}
	if c.IsLandlock() {
		if c.Cgroup == nil {
			return fmt.Errorf("Landlock runners require delegated cgroups")
		}
		needsRelay := false
		for _, p := range c.Profiles {
			if p.HostAccess == nil {
				needsRelay = true
			}
		}
		if needsRelay {
			if err := landlockrunner.ValidateDenyPrefixes(c.LandlockDenyPrefixes); err != nil {
				return err
			}
		}
		for _, p := range c.Profiles {
			if p.Machine != "landlock" {
				return fmt.Errorf("VM and process profiles require separate workers")
			}
		}
	} else if len(c.LandlockDenyPrefixes) > 0 {
		return fmt.Errorf("Landlock policy on a VM worker")
	}
	workspaceSlots := make(map[string]int)
	for _, p := range c.Profiles {
		if p.HostAccess != nil {
			if p.Machine != "landlock" {
				return fmt.Errorf("host_access is only supported by Landlock")
			}
			for slot := 1; slot <= c.Budget.MaxVMs; slot++ {
				work, err := p.HostAccess.Workspace(slot, c.Budget.MaxVMs)
				if err != nil {
					return err
				}
				if previous, ok := workspaceSlots[work]; ok && previous != slot {
					return fmt.Errorf("host workspaces overlap across slots")
				}
				workspaceSlots[work] = slot
				if _, err := landlockrunner.NormalizeAccess(p.HostAccess, slot, c.Budget.MaxVMs, c.protectedFor(p.ID, slot)); err != nil {
					return err
				}
			}
		}
	}
	// NormalizeAccess rejects nested/overlapping peer paths; workspaceSlots
	// above rejects an exact path reused by different scheduler slots.
	if warms > c.Budget.MaxVMs || cpus > c.Budget.MaxCPUs || memory > c.Budget.MaxMemoryMiB {
		return fmt.Errorf("warm capacity exceeds worker budget")
	}
	return nil
}

// IsLandlock selects a homogeneous process worker, never an implicit VM fallback.
func (c Config) IsLandlock() bool {
	for _, p := range c.Profiles {
		if p.Machine == "landlock" {
			return true
		}
	}
	return false
}

// CheckConfig verifies the trusted local catalog without taking ownership,
// reaping VMs, or starting execution. Use for installation validation.
func CheckConfig(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := host.CheckCgroupBudget(c.Cgroup, c.Budget.MaxMemoryMiB, c.Budget.MaxVMs); err != nil {
		return err
	}
	if err := host.CheckCgroup(c.ResourceCgroup()); err != nil {
		return err
	}
	if c.IsLandlock() {
		hasCPU, err := host.HasCPUController(c.Cgroup)
		if err != nil {
			return err
		}
		if !hasCPU && len(c.CPUIDs) == 0 {
			return fmt.Errorf("host_access without CPU quota requires cpu_ids affinity")
		}
	}
	if err := host.ValidateCPUSet(c.CPUIDs); err != nil {
		return err
	}
	if err := verifyImages(c); err != nil {
		return err
	}
	return checkDiskSpace(c, 0)
}

// Digest identifies the trusted SHA256SUMS content; all execution inputs are
// verified locally before serving. Remote requests cannot supply image paths.
func verifyImages(c Config) error {
	for _, p := range c.Profiles {
		if p.Machine == "landlock" {
			if err := landlockrunner.CheckPolicyFor(p.HostAccess != nil); err != nil {
				return err
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range c.Profiles {
		key := fmt.Sprintf("%s|%s|%s|%d", p.ImageDir, p.Digest, p.Machine, p.DiskGiB)
		if seen[key] {
			continue
		}
		seen[key] = true
		sums, err := os.ReadFile(filepath.Join(p.ImageDir, "SHA256SUMS"))
		if err != nil || len(sums) > 128*1024 {
			return fmt.Errorf("image checksum list unavailable")
		}
		digest := sha256.Sum256(sums)
		if hex.EncodeToString(digest[:]) != p.Digest {
			return fmt.Errorf("image content identity mismatch")
		}
		wanted := map[string]string{}
		for _, line := range strings.Split(string(sums), "\n") {
			if line == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) != 2 || !digestPattern.MatchString(parts[0]) {
				return fmt.Errorf("invalid image checksum list")
			}
			name := strings.TrimPrefix(parts[1], "*")
			if name == "." || name == ".." || filepath.Base(name) != name || name == "SHA256SUMS" || wanted[name] != "" {
				return fmt.Errorf("invalid or duplicate checksum filename")
			}
			wanted[name] = parts[0]
		}
		realDir, err := filepath.EvalSymlinks(p.ImageDir)
		if err != nil {
			return fmt.Errorf("image directory unavailable")
		}
		inputs := []string{"base.qcow2", "vmlinuz", "initrd", "manifest.json"}
		if p.Machine == "landlock" {
			inputs = []string{"runner.tar.gz", "manifest.json"}
		}
		for _, name := range inputs {
			realPath, err := filepath.EvalSymlinks(filepath.Join(p.ImageDir, name))
			if err != nil || filepath.Dir(realPath) != realDir || wanted[filepath.Base(realPath)] == "" {
				return fmt.Errorf("missing verified execution input")
			}
		}
		for name, expected := range wanted {
			realPath, err := filepath.EvalSymlinks(filepath.Join(p.ImageDir, name))
			if err != nil || filepath.Dir(realPath) != realDir {
				return fmt.Errorf("image checksum path escapes bundle")
			}
			file, err := os.Open(realPath)
			if err != nil {
				return fmt.Errorf("image execution input unavailable")
			}
			hash := sha256.New()
			_, err = io.Copy(hash, file)
			file.Close()
			if err != nil || hex.EncodeToString(hash.Sum(nil)) != expected {
				return fmt.Errorf("image execution input checksum mismatch")
			}
		}
		var manifest struct {
			Machine string `json:"machine"`
			DiskGiB int    `json:"disk_gib"`
		}
		data, err := os.ReadFile(filepath.Join(p.ImageDir, "manifest.json"))
		if err != nil || len(data) > 128*1024 || json.Unmarshal(data, &manifest) != nil || manifest.Machine != p.Machine || manifest.DiskGiB != p.DiskGiB {
			return fmt.Errorf("image resource manifest mismatch")
		}
	}
	return nil
}

func diskReservation(c Config) int64 {
	if c.IsLandlock() {
		return 2 << 30
	} // Writable job storage is bounded tmpfs charged to its cgroup.
	largest := 0
	for _, p := range c.Profiles {
		if p.DiskGiB > largest {
			largest = p.DiskGiB
		}
	}
	return int64(c.Budget.MaxVMs*(largest+1)+2) << 30
}

// Existing overlay blocks count toward already allocated reservation, whereas
// apparent sparse-file length does not. Keep another 2GiB for logs/host headroom.
func checkDiskSpace(c Config, allocated int64) error {
	path := c.StateDir
	for {
		_, err := os.Stat(path)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("disk capacity unavailable")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return fmt.Errorf("disk capacity unavailable")
		}
		path = parent
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fmt.Errorf("disk capacity unavailable")
	}
	if stat.Bsize <= 0 || stat.Bavail > uint64(^uint64(0)>>1)/uint64(stat.Bsize) {
		return fmt.Errorf("invalid disk capacity")
	}
	available := int64(stat.Bavail) * stat.Bsize
	return checkDiskCapacity(c, allocated, available)
}

func checkDiskCapacity(c Config, allocated, available int64) error {
	if allocated < 0 || available < 0 {
		return fmt.Errorf("invalid disk capacity")
	}
	required := diskReservation(c) - allocated
	if required < 2<<30 {
		required = 2 << 30
	}
	if floor := int64(c.MinFreeDiskGiB) << 30; floor > required {
		required = floor
	}
	if available < required {
		return fmt.Errorf("insufficient disk space for reserved VM capacity and diagnostics")
	}
	return nil
}

func (c Config) ProfileCgroup(p Profile) *host.CgroupConfig {
	if c.Cgroup == nil {
		return nil
	}
	v := *c.Cgroup
	v.AllowMissingCPUAndIO = p.HostAccess != nil
	return &v
}
func (c Config) ResourceCgroup() *host.CgroupConfig {
	if c.Cgroup == nil {
		return nil
	}
	v := *c.Cgroup
	v.AllowMissingCPUAndIO = true
	for _, p := range c.Profiles {
		if p.HostAccess == nil {
			v.AllowMissingCPUAndIO = false
		}
	}
	return &v
}

func (c Config) protectedFor(id string, slot int) []string {
	paths := append(append([]string(nil), c.ProtectedPaths...), c.StateDir)
	selected := ""
	for _, p := range c.Profiles {
		if p.ID == id && p.HostAccess != nil {
			selected, _ = p.HostAccess.Workspace(slot, c.Budget.MaxVMs)
		}
	}
	for _, p := range c.Profiles {
		paths = append(paths, p.ImageDir)
		if p.HostAccess != nil {
			for i := 1; i <= c.Budget.MaxVMs; i++ {
				work, _ := p.HostAccess.Workspace(i, c.Budget.MaxVMs)
				if work != selected {
					paths = append(paths, work)
				}
			}
		}
	}
	return paths
}

// CheckLandlockSandboxes probes every configured policy without credentials or cleanup.
// Slot paths are all validated by Config.Validate; the child probe uses slot one.
func CheckLandlockSandboxes(c Config) error {
	return checkLandlockSandboxes(c, landlockrunner.CheckSandboxWithAccess)
}
func checkLandlockSandboxes(c Config, probe func(string, int, *landlockrunner.HostAccess, int, int, []string) error) error {
	for _, p := range c.Profiles {
		if p.Machine != "landlock" {
			return fmt.Errorf("Landlock configuration required")
		}
		if err := probe(p.ImageDir, p.DiskGiB, p.HostAccess, 1, c.Budget.MaxVMs, c.protectedFor(p.ID, 1)); err != nil {
			return err
		}
	}
	return nil
}
