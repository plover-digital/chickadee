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
	"sync"
	"syscall"
	"time"

	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/protocol"
)

var sandboxID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func CheckPolicy() error {
	abi, err := ll.LandlockGetABIVersion()
	if err != nil || abi < 6 {
		return fmt.Errorf("Landlock ABI6 or newer is required")
	}
	if _, err = exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("bubblewrap is required")
	}
	return nil
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
	out := mustPrefixes(values)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.String())
		if err == nil && p.Addr().Is4() {
			out = append(out, netip.PrefixFrom(p.Addr(), 32))
		}
	}
	return out, nil
}

func sandboxArgs(image, exe, runtime string, diskGiB int, probe bool) ([]string, error) {
	for _, p := range []string{image, exe, runtime} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return nil, fmt.Errorf("clean absolute sandbox paths required")
		}
	}
	args := []string{"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup", "--disable-userns", "--assert-userns-disabled", "--uid", "1000", "--gid", "1000", "--hostname", "chickadee-sandbox", "--die-with-parent", "--new-session", "--as-pid-1", "--cap-drop", "ALL", "--clearenv", "--setenv", "PATH", "/usr/bin:/bin", "--setenv", "LANG", "C.UTF-8", "--proc", "/proc", "--dev", "/dev", "--size", fmt.Sprint(int64(diskGiB) << 30), "--tmpfs", "/job", "--dir", "/job/home", "--dir", "/job/home/work", "--dir", "/job/tmp", "--symlink", "/job/home", "/home/runner", "--symlink", "/job/tmp", "/tmp"}
	for _, p := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
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
		args = append(args, "--ro-bind", filepath.Join(runtime, name), "/etc/"+name)
	}
	args = append(args, "--ro-bind", filepath.Join(image, "runner.tar.gz"), "/image/runner.tar.gz", "--ro-bind", exe, "/engine", "--chdir", "/job", "--", "/engine", "-landlock-child")
	if probe {
		args = append(args, "-landlock-probe")
	}
	return args, nil
}

type Runner struct {
	mu                      sync.Mutex
	dir                     string
	cmd                     *exec.Cmd
	input                   io.WriteCloser
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
	if !sandboxID.MatchString(id) || c.Machine != "landlock" || c.Cgroup == nil {
		return nil, fmt.Errorf("invalid Landlock execution configuration")
	}
	if err := CheckPolicy(); err != nil {
		return nil, err
	}
	prefixes, err := deniedAddresses(denied)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(c.StateDir, "vms", id)
	if err = os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	m := &Runner{dir: dir, wait: make(chan struct{}), exited: make(chan struct{})}
	fail := func(err error) (*Runner, error) { return m, err }
	groupPath, err := host.ExecutionGroupPath(c.Cgroup, id)
	if err != nil {
		return fail(err)
	}
	marker, _ := json.Marshal(map[string]any{"version": 1, "backend": "landlock", "id": id, "cgroup_path": groupPath})
	if err = os.WriteFile(filepath.Join(dir, "sandbox.json"), marker, 0600); err != nil {
		return fail(err)
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
	args, err := sandboxArgs(c.ImageDir, exe, dir, c.DiskGiB, false)
	if err != nil {
		return fail(err)
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return fail(err)
	}
	parent := os.NewFile(uintptr(pair[0]), "relay-parent")
	child := os.NewFile(uintptr(pair[1]), "relay-child")
	defer child.Close()
	conn, err := net.FileConn(parent)
	parent.Close()
	if err != nil {
		return fail(err)
	}
	m.relay = newRelay(conn, prefixes)
	m.cmd, err = host.AffinityCommand("bwrap", args, c.AssignedCPUs)
	if err != nil {
		return fail(err)
	}
	m.cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	m.cmd.ExtraFiles = []*os.File{child}
	m.cmd.Stderr = io.Discard
	m.input, err = m.cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	out, err := m.cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
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
	if m.relay != nil {
		m.relay.Close()
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
func Reap(state string, c *host.CgroupConfig) error {
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
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("sandbox identity unavailable")
		}
		raw, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		var value struct {
			Version    int    `json:"version"`
			Backend    string `json:"backend"`
			ID         string `json:"id"`
			CgroupPath string `json:"cgroup_path"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Version != 1 || value.Backend != "landlock" || value.ID != id {
			return fmt.Errorf("sandbox identity mismatch")
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
		if err = os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}
