//go:build linux && amd64

package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// sandboxArgs deliberately exposes no controller state, keys, sibling VMs or
// host network. Runtime libraries are read-only; only this VM's directory is
// writable. Paths retain their host spelling for qcow2 backing-file resolution.
func sandboxArgs(vmDir, imageDir, executable string) ([]string, error) {
	args := []string{"--unshare-user", "--disable-userns", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--die-with-parent", "--new-session", "--as-pid-1", "--cap-drop", "ALL", "--clearenv", "--setenv", "LANG", "C", "--setenv", "PATH", "/usr/bin", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp"}
	// Library locations differ between Debian and RPM hosts. No /etc, /home,
	// /run, complete /usr or host /proc bind is permitted.
	for _, p := range []string{"/lib", "/lib64", "/usr/lib", "/usr/lib64", "/usr/share/qemu", "/usr/share/seabios"} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	for _, p := range []string{"/dev/kvm", "/dev/urandom"} {
		args = append(args, "--dev-bind", p, p)
	}
	for _, p := range []string{vmDir, imageDir, executable} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return nil, fmt.Errorf("sandbox paths must be clean and absolute")
		}
	}
	args = append(args, "--ro-bind", executable, executable, "--dir", imageDir)
	for _, name := range []string{"base.qcow2", "vmlinuz", "initrd"} {
		p := filepath.Join(imageDir, name)
		args = append(args, "--ro-bind", p, p)
	}
	args = append(args, "--bind", vmDir, vmDir, "--chdir", vmDir, "--", executable)
	return args, nil
}

func qemuSandbox(vmDir, imageDir string, qemuArgs []string) ([]string, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap is required; refusing unsandboxed QEMU")
	}
	qemu, err := exec.LookPath("qemu-system-x86_64")
	if err != nil {
		return nil, err
	}
	qemu, err = filepath.Abs(qemu)
	if err != nil {
		return nil, err
	}
	args, err := sandboxArgs(vmDir, imageDir, qemu)
	if err != nil {
		return nil, err
	}
	return append(append([]string{bwrap}, args...), qemuArgs...), nil
}

// Open an already provisioned TAP, never create/configure host networking here.
// The fd remains attached to the host TAP after QEMU enters a private network
// namespace. The sandbox never receives /dev/net/tun or network capabilities.
func openOwnedTAP(slot int) (*os.File, error) {
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	var req [40]byte
	copy(req[:16], fmt.Sprintf("ck%02d", slot))
	// Persistent TAP existence is checked before attachment.
	*(*uint16)(unsafe.Pointer(&req[16])) = 0x0002 | 0x1000
	if _, err = os.Stat(fmt.Sprintf("/sys/class/net/ck%02d", slot)); err != nil {
		f.Close()
		return nil, fmt.Errorf("owned TAP is absent")
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), 0x400454ca, uintptr(unsafe.Pointer(&req[0])))
	if errno != 0 {
		f.Close()
		return nil, errno
	}
	return f, nil
}

// CheckSandbox executes the same mount/network policy as a VM without starting
// a guest, touching a TAP, loading config, or reading GitHub credentials.
func CheckSandbox() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("run isolation preflight as the unprivileged service user")
	}
	// Opening for write does not change a sysctl. Verify host permissions before
	// entering a user namespace; the actual tunable is never written.
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	capsFound := false
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			capsFound = true
			if strings.TrimSpace(strings.TrimPrefix(line, "CapEff:")) != "0000000000000000" {
				return fmt.Errorf("controller must have no effective host capabilities")
			}
		}
	}
	if !capsFound {
		return fmt.Errorf("cannot verify host capabilities")
	}
	if _, err := os.Stat("/proc/sys/kernel/hostname"); err != nil {
		return fmt.Errorf("cannot verify host sysctl permissions")
	}
	if f, err := os.OpenFile("/proc/sys/kernel/hostname", os.O_WRONLY, 0); err == nil {
		f.Close()
		return fmt.Errorf("controller can open host kernel tunables for write")
	}
	root, err := os.MkdirTemp("", "chickadee-sandbox-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	vm, image := filepath.Join(root, "own"), filepath.Join(root, "image")
	for _, p := range []string{vm, image, filepath.Join(root, "sibling")} {
		if err = os.Mkdir(p, 0700); err != nil {
			return err
		}
	}
	for _, p := range []string{filepath.Join(root, "key"), filepath.Join(image, "base.qcow2"), filepath.Join(image, "vmlinuz"), filepath.Join(image, "initrd")} {
		if err = os.WriteFile(p, nil, 0600); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args, err := sandboxArgs(vm, image, exe)
	if err != nil {
		return err
	}
	cmd := exec.Command("bwrap", append(args, "-sandbox-probe="+root)...)
	cmd.Env = processEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("QEMU isolation preflight failed: %s", out)
	}
	return nil
}

// SandboxProbe is reached only by the controller's private preflight child.
func SandboxProbe(root string) error {
	if os.Getpid() != 1 {
		return fmt.Errorf("private PID namespace unavailable")
	}
	for _, p := range []string{filepath.Join(root, "key"), filepath.Join(root, "sibling"), "/etc/chickadee", "/home", "/run"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			return fmt.Errorf("controller filesystem visible")
		}
	}
	if err := os.WriteFile(filepath.Join(root, "image", "base.qcow2"), nil, 0600); err == nil {
		return fmt.Errorf("immutable image writable")
	}
	if err := os.WriteFile(filepath.Join(root, "own", "probe"), nil, 0600); err != nil {
		return err
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("KVM unavailable inside sandbox")
	}
	f.Close()
	// /sys is intentionally not mounted; /proc reports only our network namespace.
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, ":") && !strings.HasPrefix(strings.TrimSpace(line), "lo:") {
			return fmt.Errorf("host networking visible")
		}
	}
	return nil
}
