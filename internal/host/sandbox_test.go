//go:build linux && amd64

package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSandboxHidesControllerAndSiblingState(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "sandbox-probe" {
		return
	}
	requireSandboxKVM(t)
	root := t.TempDir()
	vm := filepath.Join(root, "own")
	image := filepath.Join(root, "image")
	sibling := filepath.Join(root, "other")
	key := filepath.Join(root, "app.pem")
	for _, p := range []string{vm, image, sibling} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{key, filepath.Join(sibling, "disk"), filepath.Join(image, "base.qcow2")} {
		if err := os.WriteFile(p, []byte("test fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"vmlinuz", "initrd"} {
		os.WriteFile(filepath.Join(image, name), nil, 0600)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args, err := sandboxArgs(vm, image, exe)
	if err != nil {
		t.Fatal(err)
	}
	args = append(args, "-test.run=TestSandboxProbeHelper", "--", vm, image, key, sibling, strconv.Itoa(os.Getpid()), "sandbox-probe")
	cmd := exec.Command("bwrap", args...)
	cmd.Env = processEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox probe failed (namespaces must be available): %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(vm, "written")); err != nil {
		t.Fatal("own directory unavailable")
	}
}
func TestSandboxProbeHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "sandbox-probe" {
		return
	}
	a := os.Args[len(os.Args)-6:]
	vm, image, key, sibling := a[0], a[1], a[2], a[3]
	fail := func(msg string) { fmt.Fprintln(os.Stderr, msg); os.Exit(2) }
	for _, p := range []string{key, sibling, "/etc/chickadee", "/home", "/run"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			fail("host path visible: " + p)
		}
	}
	if err := os.WriteFile(filepath.Join(image, "base.qcow2"), []byte("bad"), 0600); err == nil {
		fail("immutable image writable")
	}
	if err := os.WriteFile(filepath.Join(vm, "written"), nil, 0600); err != nil {
		fail("own directory not writable")
	}
	pid, _ := strconv.Atoi(a[4])
	if pid != 1 && syscall.Kill(pid, 0) != syscall.ESRCH {
		fail("controller PID visible")
	}
	net, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		fail("private network unavailable")
	}
	for _, line := range strings.Split(string(net), "\n") {
		if strings.Contains(line, ":") && !strings.HasPrefix(strings.TrimSpace(line), "lo:") {
			fail("host network interface visible")
		}
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		fail("KVM permissions lost in user namespace")
	}
	f.Close()
	os.Exit(0)
}
func TestCleanupRetainsDiskWhenSandboxChildExitUnconfirmed(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.qcow2")
	os.WriteFile(disk, nil, 0600)
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	done := make(chan struct{})
	close(done)
	v := &VM{Dir: dir, cmd: cmd, exited: done, exitErr: fmt.Errorf("child exit unconfirmed")}
	if err := v.Cleanup(); err == nil {
		t.Fatal("guardian exit treated as QEMU exit")
	}
	if _, err := os.Stat(disk); err != nil {
		t.Fatal("unconfirmed child disk removed")
	}
}
func TestGuardianExitDoesNotConfirmSandboxChildExit(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	fd, _, errno := syscall.Syscall(434, uintptr(child.Process.Pid), 0, 0)
	if errno != 0 {
		t.Skipf("pidfd unavailable: %v", errno)
	}
	guardian := exec.Command("true")
	if err := guardian.Start(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.qcow2")
	os.WriteFile(disk, nil, 0600)
	v := &VM{Dir: dir, cmd: guardian, exited: make(chan struct{})}
	go v.awaitSandboxExit(nil, int(fd), false)
	cleaned := make(chan error, 1)
	go func() { cleaned <- v.Cleanup() }()
	select {
	case err := <-cleaned:
		t.Fatalf("cleanup finished with live QEMU child: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := os.Stat(disk); err != nil {
		t.Fatal("disk deleted before child exit")
	}
	child.Process.Kill()
	select {
	case err := <-cleaned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("child exit did not release cleanup")
	}
	if _, err := os.Stat(disk); !os.IsNotExist(err) {
		t.Fatal("disk retained after confirmed exit")
	}
}

func TestSandboxChildIdentityIsVisibleForRecovery(t *testing.T) {
	requireSandboxKVM(t)
	root := t.TempDir()
	vm, image := filepath.Join(root, "own"), filepath.Join(root, "image")
	os.Mkdir(vm, 0700)
	os.Mkdir(image, 0700)
	for _, name := range []string{"base.qcow2", "vmlinuz", "initrd"} {
		os.WriteFile(filepath.Join(image, name), nil, 0600)
	}
	exe, _ := os.Executable()
	args, err := sandboxArgs(vm, image, exe)
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	for i, a := range args {
		if a == "--" {
			args = append(args[:i], append([]string{"--info-fd", "3"}, args[i:]...)...)
			break
		}
	}
	args = append(args, "-test.run=TestSandboxRecoveryHelper", "--", "sandbox-wait")
	cmd := exec.Command("bwrap", args...)
	cmd.Env = processEnv()
	cmd.ExtraFiles = []*os.File{w}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	w.Close()
	r.SetReadDeadline(time.Now().Add(time.Second))
	var info struct {
		PID int `json:"child-pid"`
	}
	if err = json.NewDecoder(r).Decode(&info); err != nil {
		t.Fatal(err)
	}
	fd, _, errno := syscall.Syscall(434, uintptr(info.PID), 0, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	defer syscall.Close(int(fd))
	// Once exec completes, the existing recovery path must still see executable
	// and argv across the user/PID namespace boundary.
	deadline := time.Now().Add(time.Second)
	for {
		path, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", info.PID))
		if err == nil && path == exe {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sandbox identity inaccessible to recovery: %s %v", path, err)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", info.PID)); err != nil {
		t.Fatal("sandbox argv unavailable to recovery")
	}
	cmd.Process.Kill()
	if err = waitPIDFD(int(fd), 10*time.Second); err != nil {
		t.Fatal("guardian death did not stop actual sandbox child within production exit bound")
	}
}
func TestSandboxRecoveryHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "sandbox-wait" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestInheritedTAPUsesQEMU82CompatibleOptions(t *testing.T) {
	if got := inheritedTAPNetdev(); got != "tap,id=net,fd=3" {
		t.Fatalf("fd backend must omit script/downscript: %s", got)
	}
}

func TestOnlyPCINetworkDeviceDisablesOptionROM(t *testing.T) {
	if got := networkDeviceArgs("virtio-net-pci"); got != "virtio-net-pci,netdev=net,romfile=" {
		t.Fatal(got)
	}
	if got := networkDeviceArgs("virtio-net-device"); got != "virtio-net-device,netdev=net" {
		t.Fatal("microvm does not expose a PCI ROM property")
	}
}

// A device node alone is not a usable KVM prerequisite. CI guests can contain
// /dev/kvm while denying access or lacking nested virtualization. Namespace
// isolation integration is required only when the same caller can use KVM.
func requireSandboxKVM(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("integration prerequisite unavailable: bubblewrap not installed")
	}
	if err := usableKVM("/dev/kvm"); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.ENOTTY) {
			t.Skipf("integration prerequisite unavailable: caller cannot use KVM: %v", err)
		}
		t.Fatalf("KVM prerequisite failed unexpectedly: %v", err)
	}
}
func usableKVM(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	version, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), 0xae00, 0) // KVM_GET_API_VERSION
	if errno != 0 {
		return errno
	}
	if version != 12 {
		return fmt.Errorf("unsupported KVM API version %d", version)
	}
	return nil
}
func TestKVMPrerequisiteRejectsPresentNonKVMNode(t *testing.T) {
	file := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := usableKVM(file); !errors.Is(err, syscall.ENOTTY) {
		t.Fatalf("device existence was mistaken for usable KVM: %v", err)
	}
}
