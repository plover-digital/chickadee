//go:build linux && amd64

package host

import (
	"fmt"
	"github.com/plover-digital/chickadee/internal/config"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// CgroupConfig opts into delegated cgroup-v2 isolation. Zero values choose
// explicit prototype defaults; memory overhead must be measured for workloads.
type CgroupConfig = config.CgroupConfig

func normalizedCgroup(c *CgroupConfig) CgroupConfig {
	out := *c
	if out.MemoryOverheadMiB == 0 {
		out.MemoryOverheadMiB = 512
	}
	if out.PidsMax == 0 {
		out.PidsMax = 128
	}
	if out.IOWeight == 0 {
		out.IOWeight = 100
	}
	return out
}
func ValidateCgroupConfig(c *CgroupConfig) error {
	if c == nil {
		return nil
	}
	return c.Validate()
}
func delegatedRoot(c *CgroupConfig) (string, error) {
	if err := ValidateCgroupConfig(c); err != nil {
		return "", err
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || len(data) > 65536 {
		return "", fmt.Errorf("process cgroup unavailable")
	}
	current := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::") {
			relative := strings.TrimPrefix(line, "0::")
			if !filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
				return "", fmt.Errorf("invalid process cgroup path")
			}
			current = filepath.Clean("/sys/fs/cgroup" + relative)
			break
		}
	}
	if current == "" {
		return "", fmt.Errorf("cgroup v2 is required")
	}
	root := current
	if filepath.Base(current) == "controller" {
		root = filepath.Dir(current)
	}
	if c.Root != "" {
		root = c.Root
	}
	if root == "/sys/fs/cgroup" || !(current == root || current == filepath.Join(root, "controller")) {
		return "", fmt.Errorf("cgroup root must own this service process or its controller leaf")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("delegated cgroup root unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("delegated cgroup root must belong to service user")
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(root, &fs); err != nil || fs.Type != 0x63677270 {
		return "", fmt.Errorf("delegated root is not cgroup v2")
	}
	return root, nil
}
func readControl(root, name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || len(data) > 65536 {
		return "", fmt.Errorf("cgroup control unavailable: %s", name)
	}
	return strings.TrimSpace(string(data)), nil
}
func requireControllers(root string) error {
	value, err := readControl(root, "cgroup.controllers")
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, name := range strings.Fields(value) {
		present[name] = true
	}
	for _, name := range []string{"cpu", "memory", "pids", "io"} {
		if !present[name] {
			return fmt.Errorf("delegation requires cpu memory pids io controllers")
		}
	}
	domain, err := readControl(root, "cgroup.type")
	if err != nil || domain != "domain" {
		return fmt.Errorf("delegation requires a domain cgroup")
	}
	return nil
}

// CheckCgroup is read-only. It must run inside the delegated service unit;
// a shell outside that unit is not a substitute for service isolation preflight.
func CheckCgroup(c *CgroupConfig) error {
	if c == nil {
		return nil
	}
	root, err := delegatedRoot(c)
	if err != nil {
		return err
	}
	if err = requireControllers(root); err != nil {
		return err
	}
	for _, limit := range c.IOMax {
		if _, err = os.Stat(filepath.Join("/sys/dev/block", limit.Device)); err != nil {
			return fmt.Errorf("configured I/O block device unavailable")
		}
	}
	for _, name := range []string{"cgroup.procs", "cgroup.subtree_control"} {
		if err = syscall.Access(filepath.Join(root, name), 2); err != nil {
			return fmt.Errorf("service cgroup delegation is not writable")
		}
	}
	return nil
}
func writeControl(root, name, value string) error {
	file, err := os.OpenFile(filepath.Join(root, name), os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("cgroup control is not writable: %s", name)
	}
	_, err = file.WriteString(value)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("cgroup limit write failed: %s", name)
	}
	return nil
}
func PrepareCgroup(c *CgroupConfig) error {
	if c == nil {
		return nil
	}
	if err := CheckCgroup(c); err != nil {
		return err
	}
	root, _ := delegatedRoot(c)
	procs, err := readControl(root, "cgroup.procs")
	if err != nil {
		return err
	}
	for _, pid := range strings.Fields(procs) {
		if pid != strconv.Itoa(os.Getpid()) {
			return fmt.Errorf("delegated root contains an unrelated process")
		}
	}
	leaf := filepath.Join(root, "controller")
	if err = os.Mkdir(leaf, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("controller cgroup creation failed")
	}
	info, err := os.Lstat(leaf)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid controller cgroup")
	}
	if err = writeControl(leaf, "cgroup.procs", strconv.Itoa(os.Getpid())); err != nil {
		return err
	}
	return writeControl(root, "cgroup.subtree_control", "+cpu +memory +pids +io")
}

var ownedCgroup = regexp.MustCompile(`^chickadee-vm-[0-9a-f]{16}$`)

type vmCgroup struct {
	path string
	file *os.File
}

func cgroupLimits(c CgroupConfig, cpus, memoryMiB int, write func(string, string) error) error {
	if cpus < 1 || cpus > 256 || memoryMiB < 1 || memoryMiB > 1<<20 || c.PidsMax < cpus+32 {
		return fmt.Errorf("VM shape exceeds cgroup limits")
	}
	limits := [][2]string{{"cpu.max", fmt.Sprintf("%d 100000", cpus*100000)}, {"memory.max", strconv.FormatInt(int64(memoryMiB+c.MemoryOverheadMiB)<<20, 10)}, {"memory.swap.max", "0"}, {"memory.oom.group", "1"}, {"pids.max", strconv.Itoa(c.PidsMax)}, {"io.weight", fmt.Sprintf("default %d", c.IOWeight)}}
	for _, limit := range limits {
		if err := write(limit[0], limit[1]); err != nil {
			return err
		}
	}
	for _, limit := range c.IOMax {
		value := limit.Device
		for _, field := range []struct {
			name  string
			value int64
		}{{"rbps", limit.ReadBPS}, {"wbps", limit.WriteBPS}, {"riops", limit.ReadIOPS}, {"wiops", limit.WriteIOPS}} {
			if field.value > 0 {
				value += fmt.Sprintf(" %s=%d", field.name, field.value)
			}
		}
		if err := write("io.max", value); err != nil {
			return err
		}
	}
	return nil
}
func createCgroup(c *CgroupConfig, id string, cpus, memoryMiB int) (*vmCgroup, error) {
	if c == nil {
		return nil, nil
	}
	root, err := delegatedRoot(c)
	if err != nil {
		return nil, err
	}
	name := "chickadee-vm-" + id
	if !ownedCgroup.MatchString(name) {
		return nil, fmt.Errorf("invalid cgroup VM identity")
	}
	path := filepath.Join(root, name)
	if err = os.Mkdir(path, 0755); err != nil {
		return nil, fmt.Errorf("VM cgroup already exists or cannot be created")
	}
	group := &vmCgroup{path: path}
	if err = cgroupLimits(normalizedCgroup(c), cpus, memoryMiB, func(name, value string) error { return writeControl(path, name, value) }); err != nil {
		return group, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return group, fmt.Errorf("VM cgroup descriptor unavailable")
	}
	group.file = os.NewFile(uintptr(fd), path)
	return group, nil
}
func (g *vmCgroup) closeFD() {
	if g != nil && g.file != nil {
		g.file.Close()
		g.file = nil
	}
}
func removeEmptyCgroup(path string) error {
	value, err := readControl(path, "cgroup.events")
	if err != nil {
		return err
	}
	empty := false
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) == "populated 0" {
			empty = true
		}
		if strings.TrimSpace(line) == "populated 1" {
			return fmt.Errorf("VM cgroup still populated; disk retained")
		}
	}
	if !empty {
		return fmt.Errorf("VM cgroup exit state unavailable")
	}
	if err = os.Remove(path); err != nil {
		return fmt.Errorf("VM cgroup removal failed; disk retained")
	}
	return nil
}

// ReapCgroups is called only AFTER owned process reaping has confirmed exit.
// It never signals arbitrary tasks or recursively deletes a populated cgroup.
func ReapCgroups(c *CgroupConfig) error {
	if c == nil {
		return nil
	}
	root, err := delegatedRoot(c)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "chickadee-vm-") {
			continue
		}
		if !ownedCgroup.MatchString(entry.Name()) || !entry.IsDir() {
			return fmt.Errorf("invalid owned cgroup entry")
		}
		if err = removeEmptyCgroup(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// CheckCgroupBudget reserves accounting room above advertised guest RAM. It does
// not raise systemd limits or claim the default headroom fits every workload.
func CheckCgroupBudget(c *CgroupConfig, guestMemoryMiB, maxVMs int) error {
	if c == nil {
		return nil
	}
	root, err := delegatedRoot(c)
	if err != nil {
		return err
	}
	value, err := readControl(root, "memory.max")
	if err != nil {
		return err
	}
	return checkParentMemory(value, int64(guestMemoryMiB+maxVMs*normalizedCgroup(c).MemoryOverheadMiB+256)<<20)
}
func checkParentMemory(value string, required int64) error {
	if value == "max" {
		return fmt.Errorf("delegated memory.max must be explicitly bounded")
	}
	limit, err := strconv.ParseInt(value, 10, 64)
	if err != nil || limit <= 0 || limit < required {
		return fmt.Errorf("delegated memory.max must cover guest budget, per-VM overhead and controller headroom")
	}
	return nil
}
