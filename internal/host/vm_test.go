//go:build linux && amd64

package host

import (
	"context"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/protocol"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCleanupWaitsForProcessExit(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.qcow2")
	if e := os.WriteFile(disk, nil, 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("sleep", "30")
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	v := &VM{Dir: dir, cmd: cmd, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); time.Sleep(100 * time.Millisecond); close(v.exited) }()
	if e := v.Cleanup(); e != nil {
		t.Fatal(e)
	}
	select {
	case <-v.exited:
	default:
		t.Fatal("deleted before Wait completed")
	}
	if _, e := os.Stat(disk); !os.IsNotExist(e) {
		t.Fatal("overlay not removed")
	}
}
func TestUnexpectedStatusAndTimeoutRetire(t *testing.T) {
	for _, typ := range []string{"READY", "timeout"} {
		t.Run(typ, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			v := &VM{Conn: a, Reader: protocol.NewReader(a)}
			go func() {
				_, _ = protocol.NewReader(b).Read()
				_ = protocol.Write(b, protocol.Frame{V: 1, Type: "ACK"})
				_ = protocol.Write(b, protocol.Frame{V: 1, Type: "RUNNING"})
				if typ != "timeout" {
					_ = protocol.Write(b, protocol.Frame{V: 1, Type: typ})
				}
			}()
			if e := v.Run("dGVzdA==", 30*time.Millisecond, filepath.Join(t.TempDir(), "log")); e == nil {
				t.Fatal("invalid guest status accepted")
			}
		})
	}
}
func TestJournalIntentSurvivesWithoutPIDOrRunnerID(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "records"), 0700)
	r := Record{ID: "0123456789abcdef", Name: "chickadee-0123456789abcdef"}
	if e := Save(dir, r); e != nil {
		t.Fatal(e)
	}
	got, e := Records(dir)
	if e != nil || len(got) != 1 || got[0] != r {
		t.Fatalf("intent not recoverable: %v", e)
	}
	if e = Save(dir, r); e == nil {
		t.Fatal("intent overwritten")
	}
}

func TestPIDFDWaitsForRealProcessExit(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	fd, _, errno := syscall.Syscall(434, uintptr(cmd.Process.Pid), 0, 0)
	if errno == syscall.EPERM || errno == syscall.ENOSYS {
		t.Skipf("pidfd unavailable: %v", errno)
	}
	if errno != 0 {
		t.Fatal(errno)
	}
	defer syscall.Close(int(fd))
	if waitPIDFD(int(fd), 20*time.Millisecond) == nil {
		t.Fatal("reported a live process exited")
	}
	if e := cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	if e := waitPIDFD(int(fd), time.Second); e != nil {
		t.Fatal(e)
	}
}

func TestHypervisorEnvironmentExcludesControllerSecrets(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-only-not-a-token")
	cmd := exec.Command("sh", "-c", `test -z "${GH_TOKEN+x}"`)
	cmd.Env = processEnv()
	if e := cmd.Run(); e != nil {
		t.Fatal("controller credential environment reached the child")
	}
}

func TestStartCannotOverwriteAnExistingVMDirectory(t *testing.T) {
	c := config.Config{StateDir: t.TempDir()}
	id := "0123456789abcdef"
	dir := filepath.Join(c.StateDir, "vms", id)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	disk := filepath.Join(dir, "disk.qcow2")
	if e := os.WriteFile(disk, []byte("existing VM disk"), 0600); e != nil {
		t.Fatal(e)
	}
	v, e := Start(context.Background(), c, 1, id)
	if e == nil {
		t.Fatal("existing VM directory reused")
	}
	if e = v.Cleanup(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(disk)
	if e != nil || string(b) != "existing VM disk" {
		t.Fatal("failed start changed existing disk")
	}
}

func TestMachineDeviceFamilies(t *testing.T) {
	for _, name := range []string{"", "microvm", "q35"} {
		machine, disk, network, rng := machineDevices(name)
		if name == "q35" {
			if machine != "q35" || disk != "virtio-blk-pci" || network != "virtio-net-pci" || rng != "virtio-rng-pci" {
				t.Fatal("stock-kernel PCI devices missing")
			}
		} else if !strings.HasPrefix(machine, "microvm,") || disk != "virtio-blk-device" || network != "virtio-net-device" || rng != "virtio-rng-device" {
			t.Fatal("legacy microvm device family changed")
		}
	}
}

func TestPrelaunchFailuresConfirmExitAndAllowOwnedDiskCleanup(t *testing.T) {
	if os.Geteuid() != 0 {
		exercisePrelaunchFailures(t)
		return
	}
	// Start forbids root. Exercise the real path in an unprivileged child rather
	// than skipping the availability regression in root-run test environments.
	dir, err := os.MkdirTemp("", "chickadee-unpriv-start-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err = os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	binary := filepath.Join(dir, "host.test")
	output, err := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("could not prepare unprivileged helper")
	}
	cmd := exec.Command(binary, "-test.run=^TestPrelaunchFailureUnprivilegedHelper$")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "CHICKADEE_PRELAUNCH_HELPER=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534, Groups: []uint32{65534}}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unprivileged Start regression: %v %s", err, output)
	}
}
func TestPrelaunchFailureUnprivilegedHelper(t *testing.T) {
	if os.Getenv("CHICKADEE_PRELAUNCH_HELPER") != "1" {
		return
	}
	if os.Geteuid() == 0 {
		t.Fatal("Start regression helper remained privileged")
	}
	exercisePrelaunchFailures(t)
}
func exercisePrelaunchFailures(t *testing.T) {
	t.Helper()
	for _, stage := range []string{"invalid-backing", "invalid-affinity", "guardian-start"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			image := filepath.Join(root, "image")
			for _, dir := range []string{filepath.Join(state, "vms"), filepath.Join(state, "logs"), image} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			c := config.Config{StateDir: state, ImageDir: image, CPUs: 2, MemoryMiB: 512, DiskGiB: 8, BootSeconds: 1, Machine: "q35"}
			if stage != "invalid-backing" {
				bin := filepath.Join(root, "bin")
				if err := os.Mkdir(bin, 0700); err != nil {
					t.Fatal(err)
				}
				// Only overlay creation is simulated. The actual Start function reaches
				// affinity validation / Cmd.Start without executing any VM or sandbox.
				commands := map[string]string{"qemu-img": "#!/bin/sh\nfor arg; do previous=$last; last=$arg; done\n: > \"$previous\"\n", "bwrap": "#!/bin/sh\nexit 99\n", "qemu-system-x86_64": "#!/bin/sh\nexit 99\n"}
				for name, body := range commands {
					if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0755); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("PATH", bin)
				if stage == "invalid-affinity" {
					c.AssignedCPUs = []int{-1, 0}
				}
			}
			id := "0123456789abcdef"
			vm, err := StartBootCheck(context.Background(), c, 1, id)
			if err == nil || vm == nil {
				t.Fatalf("expected owned prelaunch failure: %v", err)
			}
			if stage == "invalid-affinity" && !strings.Contains(err.Error(), "configured CPU") {
				t.Fatalf("did not reach affinity validation: %v", err)
			}
			if stage == "guardian-start" && !strings.Contains(err.Error(), "QEMU start failed") {
				t.Fatalf("did not reach guardian start: %v", err)
			}
			if vm.cmd != nil && vm.cmd.Process != nil {
				t.Fatal("unexpected guardian launched")
			}
			select {
			case <-vm.Exited():
			default:
				t.Fatal("unstarted owned VM exit was left unconfirmed")
			}
			if _, err := os.Stat(vm.Dir); err != nil {
				t.Fatal("owned directory removed before cleanup")
			}
			if err := vm.Cleanup(); err != nil {
				t.Fatal(err)
			}
			if err := vm.Cleanup(); err != nil {
				t.Fatalf("repeated cleanup unsafe: %v", err)
			}
			if _, err := os.Stat(vm.Dir); !os.IsNotExist(err) {
				t.Fatal("prelaunch disk/directory retained")
			}
		})
	}
}
