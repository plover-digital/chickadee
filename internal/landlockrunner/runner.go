//go:build linux && amd64

package landlockrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/protocol"
	"golang.org/x/sys/unix"
)

var sandboxID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func CheckPolicy() error { return CheckPolicyFor(false) }
func CheckPolicyFor(integrated bool) error {
	abi, err := ll.LandlockGetABIVersion()
	minimum := 6
	if integrated {
		minimum = 5
	}
	if err != nil || abi < minimum {
		return fmt.Errorf("Landlock ABI%d or newer is required", minimum)
	}
	if _, err = exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("bubblewrap is required")
	}
	return nil
}

// PolicySummary reports capability choices, never paths, environment or credentials.
func PolicySummary(integrated bool) (string, error) {
	abi, err := ll.LandlockGetABIVersion()
	if err != nil {
		return "", fmt.Errorf("Landlock ABI unavailable")
	}
	if integrated {
		return fmt.Sprintf("HOST_ACCESS abi=%d policy=V5 enforced=fs,truncate,ioctl omitted=ipc-scope network=host identity=service-primary workspace=unbounded supplementary_groups=unsupported remote_containment=none", abi), nil
	}
	return fmt.Sprintf("SEALED abi=%d policy=V6 network=private-relay", abi), nil
}
func ValidateDenyPrefixes(values []string) error {
	if len(values) == 0 || len(values) > 32 {
		return fmt.Errorf("declare the host public egress addresses to deny")
	}
	for _, s := range values {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() || p.Bits() < 8 {
			return fmt.Errorf("invalid denied network prefix")
		}
	}
	return nil
}
func deniedAddresses(values []string) ([]netip.Prefix, error) {
	if err := ValidateDenyPrefixes(values); err != nil {
		return nil, err
	}
	return mustPrefixes(values), nil
}

func publicRuntimeTarget(target string) bool {
	for _, root := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if target == root || strings.HasPrefix(target, root+"/") {
			return true
		}
	}
	return false
}

func sandboxArgs(image, exe, runtime string, diskGiB int, probe bool) ([]string, error) {
	return sandboxArgsWithAccess(image, exe, runtime, diskGiB, probe, nil)
}
func sandboxArgsWithAccess(image, exe, runtime string, diskGiB int, probe bool, plan *AccessPlan) ([]string, error) {
	for _, p := range []string{image, exe, runtime} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return nil, fmt.Errorf("clean absolute sandbox paths required")
		}
	}
	args := []string{"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup", "--disable-userns", "--assert-userns-disabled", "--uid", "1000", "--gid", "1000", "--hostname", "chickadee-sandbox", "--die-with-parent", "--new-session", "--as-pid-1", "--cap-drop", "ALL", "--clearenv", "--setenv", "PATH", "/usr/bin:/bin", "--setenv", "LANG", "C.UTF-8", "--proc", "/proc", "--dev", "/dev", "--size", fmt.Sprint(int64(diskGiB) << 30), "--tmpfs", "/job", "--dir", "/job/home", "--dir", "/job/home/work", "--dir", "/job/tmp", "--symlink", "/job/home", "/home/runner", "--symlink", "/job/tmp", "/tmp"}
	if plan != nil {
		filtered := make([]string, 0, len(args))
		for i := 0; i < len(args); i++ {
			if args[i] == "--unshare-net" {
				continue
			}
			if args[i] == "--uid" {
				filtered = append(filtered, "--uid", fmt.Sprint(plan.UID))
				i++
				continue
			}
			if args[i] == "--gid" {
				filtered = append(filtered, "--gid", fmt.Sprint(plan.GID))
				i++
				continue
			}
			filtered = append(filtered, args[i])
		}
		args = filtered
		payload, _ := json.Marshal(plan)
		args = append(args, "--setenv", "CHICKADEE_HOST_ACCESS", string(payload))
		args = append(args, "--dir", "/var", "--symlink", "/run", "/var/run", "--bind-fd", "3", plan.WorkDir)
		for i, m := range plan.Mounts {
			args = append(args, "--ro-bind-fd", fmt.Sprint(4+i), m.Target)
		}
		for _, name := range []string{"/etc/nsswitch.conf", "/etc/resolv.conf", "/etc/hosts"} {
			if source, err := filepath.EvalSymlinks(name); err == nil {
				info, e := os.Stat(source)
				if e == nil && info.Mode().IsRegular() {
					args = append(args, "--ro-bind", source, name)
				}
			}
		}
	}
	for _, p := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	// Toolchain/library aliases often traverse /etc/alternatives. Normalize only
	// symlinks resolving into the already-exposed read-only runtime, rather than
	// mounting the host's alternatives/configuration directory wholesale.
	aliases, err := os.ReadDir("/etc/alternatives")
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(aliases) > 4096 {
		return nil, fmt.Errorf("runtime alias inventory exceeds bounds")
	}
	for _, alias := range aliases {
		if alias.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := filepath.Join("/etc/alternatives", alias.Name())
		target, err := filepath.EvalSymlinks(name)
		if err != nil || !publicRuntimeTarget(target) {
			continue
		}
		info, err := os.Stat(target)
		if err != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
			continue
		}
		args = append(args, "--ro-bind", target, name)
	}
	// Only public runtime configuration, never the host's /etc or home tree.
	for _, p := range []string{"/etc/ld.so.cache", "/etc/localtime", "/etc/os-release"} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}
	// Normalize only the public CA bundle; distribution symlinks may otherwise
	// point outside the deliberately narrow mount tree.
	ca := ""
	for _, candidate := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/var/lib/ca-certificates/ca-bundle.pem"} {
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			ca = resolved
			break
		}
	}
	if ca == "" {
		return nil, fmt.Errorf("public CA bundle required")
	}
	for _, target := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/cert.pem", "/etc/ssl/ca-bundle.pem", "/etc/pki/tls/certs/ca-bundle.crt"} {
		args = append(args, "--ro-bind", ca, target)
	}
	for _, name := range []string{"passwd", "group"} {
		source := filepath.Join(runtime, name)
		if plan != nil {
			source = "/etc/" + name
		}
		args = append(args, "--ro-bind", source, "/etc/"+name)
	}
	args = append(args, "--ro-bind", filepath.Join(image, "runner.tar.gz"), "/image/runner.tar.gz", "--ro-bind", exe, "/engine", "--chdir", "/job", "--", "/engine", "-landlock-child")
	if probe {
		args = append(args, "-landlock-probe")
	}
	return args, nil
}

type Runner struct {
	workspace               *Workspace
	workspaceOwned          bool
	plan                    *AccessPlan
	pins                    []mountIdentity
	mu                      sync.Mutex
	dir                     string
	cmd                     *exec.Cmd
	input                   io.WriteCloser
	output                  io.ReadCloser
	reader                  *protocol.Reader
	group                   *host.ExecutionGroup
	relay                   *relay
	wait                    chan struct{}
	exited                  chan struct{}
	started, spent, cleaned bool
	exitOnce                sync.Once
	exitMu                  sync.Mutex
}

func Start(ctx context.Context, c config.Config, id string, denied []string) (*Runner, error) {
	return StartWithAccess(ctx, c, id, denied, nil, 1, 1)
}
func StartWithAccess(ctx context.Context, c config.Config, id string, denied []string, access *HostAccess, slot, maxSlots int, protected ...string) (*Runner, error) {
	if !sandboxID.MatchString(id) || c.Machine != "landlock" || c.Cgroup == nil {
		return nil, fmt.Errorf("invalid Landlock execution configuration")
	}
	if err := CheckPolicyFor(access != nil); err != nil {
		return nil, err
	}
	var prefixes []netip.Prefix
	var err error
	if access == nil {
		prefixes, err = deniedAddresses(denied)
		if err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(c.StateDir, "vms", id)
	if err = os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	m := &Runner{dir: dir, wait: make(chan struct{}), exited: make(chan struct{})}
	fail := func(err error) (*Runner, error) { return m, err }
	plan, err := NormalizeAccess(access, slot, maxSlots, protectedPaths(c.StateDir, c.ImageDir, protected))
	if err != nil {
		return fail(err)
	}
	m.plan = plan
	if plan != nil {
		m.workspace, err = LockWorkspace(plan.WorkDir)
		if err != nil {
			return fail(err)
		}
		if err = m.workspace.Empty(); err != nil {
			return fail(err)
		}
	}
	groupPath, err := host.ExecutionGroupPath(c.Cgroup, id)
	if err != nil {
		return fail(err)
	}
	value := sandboxMarker{Version: 1, Backend: "landlock", ID: id, CgroupPath: groupPath}
	if plan != nil {
		value.Version = 2
		value.AccessHash = plan.Hash
		value.WorkDir = plan.WorkDir
		value.Device = m.workspace.Device
		value.Inode = m.workspace.Inode
	}
	marker, _ := json.Marshal(value)
	if err = os.WriteFile(filepath.Join(dir, "sandbox.json"), marker, 0600); err != nil {
		return fail(err)
	}
	markerFile, e := os.OpenFile(filepath.Join(dir, "sandbox.json"), os.O_RDWR, 0)
	if e != nil {
		return fail(e)
	}
	e = markerFile.Sync()
	markerFile.Close()
	if e != nil {
		return fail(e)
	}
	markerDir, e := os.Open(dir)
	if e != nil {
		return fail(e)
	}
	e = markerDir.Sync()
	markerDir.Close()
	if e != nil {
		return fail(e)
	}
	if plan != nil {
		m.workspaceOwned = true
	}
	for name, data := range map[string]string{"passwd": "runner:x:1000:1000:Chickadee:/home/runner:/bin/bash\n", "group": "runner:x:1000:\n"} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			return fail(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	args, err := sandboxArgsWithAccess(c.ImageDir, exe, dir, c.DiskGiB, false, plan)
	if err != nil {
		return fail(err)
	}
	var extra []*os.File
	if plan != nil {
		extra = append(extra, m.workspace.file)
		for _, mount := range plan.Mounts {
			file, identity, e := pinMount(mount)
			if e != nil {
				for _, f := range extra[1:] {
					f.Close()
				}
				return fail(e)
			}
			extra = append(extra, file)
			m.pins = append(m.pins, identity)
		}
		defer func() {
			for _, f := range extra[1:] {
				f.Close()
			}
		}()
	} else {
		pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
		if err != nil {
			return fail(err)
		}
		parent := os.NewFile(uintptr(pair[0]), "relay-parent")
		child := os.NewFile(uintptr(pair[1]), "relay-child")
		defer child.Close()
		extra = append(extra, child)
		conn, err := net.FileConn(parent)
		parent.Close()
		if err != nil {
			return fail(err)
		}
		m.relay = newRelay(conn, prefixes)
	}
	m.cmd, err = host.AffinityCommand("bwrap", args, c.AssignedCPUs)
	if err != nil {
		return fail(err)
	}
	m.cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	m.cmd.ExtraFiles = extra
	m.cmd.Stderr = io.Discard
	m.input, err = m.cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	out, err := m.cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	m.output = out
	m.reader = protocol.NewReader(out)
	m.group, err = host.NewExecutionGroup(c.Cgroup, id, c.CPUs, c.MemoryMiB)
	if err != nil {
		return fail(err)
	}
	m.cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, UseCgroupFD: true, CgroupFD: m.group.FD()}
	err = m.cmd.Start()
	m.group.CloseFD()
	if err != nil {
		return fail(err)
	}
	m.started = true
	go func() { m.cmd.Wait(); close(m.wait); m.confirmExit() }()
	ready := make(chan error, 1)
	go func() {
		f, e := m.reader.Read()
		if e == nil && f.Type != "READY" {
			e = fmt.Errorf("sandbox failed readiness")
		}
		ready <- e
	}()
	select {
	case err = <-ready:
		return m, err
	case <-ctx.Done():
		return fail(fmt.Errorf("sandbox readiness timeout"))
	}
}
func (m *Runner) Exited() <-chan struct{} { return m.exited }
func (m *Runner) Run(jit string, timeout time.Duration, logPath string) error {
	m.mu.Lock()
	if m.spent || m.cleaned {
		m.mu.Unlock()
		return fmt.Errorf("sandbox already consumed")
	}
	m.spent = true
	m.mu.Unlock()
	if err := m.ValidateReady(); err != nil {
		return err
	}
	if err := protocol.Write(m.input, protocol.Frame{V: 1, Type: "CONFIG", JIT: jit}); err != nil {
		return err
	}
	m.input.Close()
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	result := make(chan error, 1)
	go func() {
		var total int
		stage := 0
		for {
			f, err := m.reader.Read()
			if err != nil {
				result <- err
				return
			}
			switch f.Type {
			case "ACK":
				if stage != 0 {
					result <- fmt.Errorf("invalid acknowledgement")
					return
				}
				stage = 1
			case "RUNNING":
				if stage != 1 {
					result <- fmt.Errorf("invalid runner status")
					return
				}
				stage = 2
			case "LOG":
				total += len(f.Data)
				if stage != 2 || total > protocol.MaxLogs {
					result <- fmt.Errorf("diagnostics exceed bounds")
					return
				}
				if protocol.Write(file, f) != nil {
					result <- fmt.Errorf("diagnostics unavailable")
					return
				}
			case "DONE":
				if stage != 2 {
					result <- fmt.Errorf("invalid completion status")
					return
				}
				result <- nil
				return
			default:
				result <- fmt.Errorf("unexpected sandbox frame")
				return
			}
		}
	}()
	select {
	case err = <-result:
		return err
	case <-time.After(timeout):
		// Interrupt the protocol read and join it before closing its log file.
		m.output.Close()
		<-result
		return fmt.Errorf("sandbox job deadline reached")
	}
}
func (m *Runner) confirmExit() error {
	m.exitMu.Lock()
	defer m.exitMu.Unlock()
	select {
	case <-m.exited:
		return nil
	default:
	}
	if m.group != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := m.group.Stop(ctx); err != nil {
			return err
		}
	}
	if m.started {
		select {
		case <-m.wait:
		case <-time.After(30 * time.Second):
			return fmt.Errorf("guardian exit unconfirmed")
		}
	}
	m.exitOnce.Do(func() { close(m.exited) })
	return nil
}
func (m *Runner) Cleanup() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cleaned {
		return nil
	}
	if m.input != nil {
		m.input.Close()
	}
	if err := m.confirmExit(); err != nil {
		return err
	}
	if m.started {
		select {
		case <-m.wait:
		case <-time.After(30 * time.Second):
			return fmt.Errorf("sandbox guardian exit unconfirmed")
		}
	}
	if m.output != nil {
		m.output.Close()
	}
	if m.relay != nil {
		m.relay.Close()
	}
	if m.workspace != nil {
		if m.workspaceOwned {
			if err := m.workspace.Clear(); err != nil {
				return err
			}
		}
		m.workspace.Close()
		m.workspace = nil
	}
	if m.group != nil {
		if err := m.group.Remove(); err != nil {
			return err
		}
	}
	m.exitOnce.Do(func() { close(m.exited) })
	if err := os.RemoveAll(m.dir); err != nil {
		return err
	}
	m.cleaned = true
	return nil
}

// Reap requires matching host-only metadata and the journal lock held by the
// caller. cgroup.kill and populated0 confirm every descendant exited, even if
// the guardian/controller died. Unknown state fails closed without PID kills.
func Reap(state string, c *host.CgroupConfig) error { return ReapWithAccess(state, c, nil) }
func ReapWithAccess(state string, c *host.CgroupConfig, plans []*AccessPlan) error {
	root := filepath.Join(state, "vms")
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := entry.Name()
		if !sandboxID.MatchString(id) || !entry.IsDir() {
			return fmt.Errorf("unknown process runner state")
		}
		dir := filepath.Join(root, id)
		info, err := os.Lstat(dir)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("invalid sandbox runtime ownership")
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("invalid sandbox owner")
		}
		marker := filepath.Join(dir, "sandbox.json")
		info, err = os.Lstat(marker)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("sandbox identity unavailable")
		}
		raw, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		var value sandboxMarker
		if json.Unmarshal(raw, &value) != nil || (value.Version != 1 && value.Version != 2) || value.Backend != "landlock" || value.ID != id {
			return fmt.Errorf("sandbox identity mismatch")
		}
		var workspace *Workspace
		if value.Version == 2 {
			valid := false
			for _, plan := range plans {
				if plan.Hash == value.AccessHash && plan.WorkDir == value.WorkDir {
					valid = true
				}
			}
			if !valid {
				return fmt.Errorf("host access policy mismatch; retain state")
			}
			workspace, err = LockWorkspace(value.WorkDir)
			if err != nil {
				return err
			}
			defer workspace.Close()
			if workspace.Device != value.Device || workspace.Inode != value.Inode {
				return fmt.Errorf("workspace identity mismatch; retain state")
			}
		} else if value.AccessHash != "" || value.WorkDir != "" || value.Device != 0 || value.Inode != 0 {
			return fmt.Errorf("invalid sealed marker")
		}
		expected, err := host.ExecutionGroupPath(c, id)
		if err != nil || value.CgroupPath != expected {
			return fmt.Errorf("sandbox belongs to a different delegated unit; retain state")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = host.RecoverExecutionGroup(ctx, c, id)
		cancel()
		if err != nil {
			return err
		}
		if workspace != nil {
			if err = workspace.Clear(); err != nil {
				return err
			}
			workspace.Close()
		}
		if err = os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

type sandboxMarker struct {
	Version    int    `json:"version"`
	Backend    string `json:"backend"`
	ID         string `json:"id"`
	CgroupPath string `json:"cgroup_path"`
	AccessHash string `json:"access_hash,omitempty"`
	WorkDir    string `json:"work_dir,omitempty"`
	Device     uint64 `json:"workspace_device,omitempty"`
	Inode      uint64 `json:"workspace_inode,omitempty"`
}
type mountIdentity struct {
	target string
	device uint64
	inode  uint64
}

func pinMount(m AccessMount) (*os.File, mountIdentity, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, m.Source, &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return nil, mountIdentity{}, fmt.Errorf("host mount cannot be pinned")
	}
	f := os.NewFile(uintptr(fd), "host-mount")
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, mountIdentity{}, err
	}
	if (m.Kind == "socket" && info.Mode()&os.ModeSocket == 0) || (m.Kind == "dir" && !info.IsDir()) || (m.Kind == "file" && !info.Mode().IsRegular()) {
		f.Close()
		return nil, mountIdentity{}, fmt.Errorf("host mount type changed")
	}
	st := info.Sys().(*syscall.Stat_t)
	return f, mountIdentity{m.Target, uint64(st.Dev), st.Ino}, nil
}
func (m *Runner) ValidateReady() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.plan == nil {
		return nil
	}
	if m.workspace == nil {
		return fmt.Errorf("host workspace unavailable")
	}
	if err := m.workspace.Verify(); err != nil {
		return err
	}
	for _, pin := range m.pins {
		info, err := os.Stat(pin.target)
		if err != nil {
			return fmt.Errorf("host mount changed")
		}
		st := info.Sys().(*syscall.Stat_t)
		if uint64(st.Dev) != pin.device || st.Ino != pin.inode {
			return fmt.Errorf("host mount changed; retire execution")
		}
	}
	return nil
}
