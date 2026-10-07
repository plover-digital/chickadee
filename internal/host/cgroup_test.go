//go:build linux && amd64

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/plover-digital/chickadee/internal/config"
)

func TestCgroupResourceWritesAndHardIOAreBounded(t *testing.T) {
	c := normalizedCgroup(&CgroupConfig{IOMax: []config.IOLimit{{Device: "259:0", ReadBPS: 1048576, WriteIOPS: 100}}})
	values := map[string]string{}
	err := cgroupLimits(c, 4, 8192, func(name, value string) error { values[name] = value; return nil })
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"cpu.max": "400000 100000", "memory.max": strconv.FormatInt(8704<<20, 10), "memory.swap.max": "0", "memory.oom.group": "1", "pids.max": "128", "io.weight": "default 100", "io.max": "259:0 rbps=1048576 wiops=100"}
	if len(values) != len(expected) {
		t.Fatal("missing resource controller writes")
	}
	for name, value := range expected {
		if values[name] != value {
			t.Fatalf("wrong %s limit", name)
		}
	}
	calls := 0
	err = cgroupLimits(c, 2, 4096, func(name, value string) error {
		calls++
		if name == "memory.swap.max" {
			return errors.New("not writable")
		}
		return nil
	})
	if err == nil || calls != 3 {
		t.Fatal("failed controller configuration did not abort")
	}
}
func TestCgroupConfigAndParentMemoryFailClosed(t *testing.T) {
	for _, c := range []*CgroupConfig{{Root: "/sys/fs/cgroup"}, {Root: "/tmp/group"}, {MemoryOverheadMiB: 127}, {MemoryOverheadMiB: 2049}, {PidsMax: 63}, {IOWeight: 10001}, {IOMax: []config.IOLimit{{Device: "259:0"}}}, {IOMax: []config.IOLimit{{Device: "../x", ReadBPS: 1}}}, {IOMax: []config.IOLimit{{Device: "259:0", ReadBPS: -1}}}} {
		if ValidateCgroupConfig(c) == nil {
			t.Fatal("invalid cgroup config accepted")
		}
	}
	required := int64(14080) << 20
	if checkParentMemory(strconv.FormatInt(13824<<20, 10), required) == nil {
		t.Fatal("parent has no controller headroom")
	}
	if err := checkParentMemory(strconv.FormatInt(required, 10), required); err != nil {
		t.Fatal(err)
	}
	if checkParentMemory("max", required) == nil {
		t.Fatal("unbounded parent accepted")
	}
}
func TestPopulatedCgroupRetainsDiskEvenAfterGuardianExit(t *testing.T) {
	root := t.TempDir()
	disk := filepath.Join(root, "vm")
	group := filepath.Join(root, "cgroup")
	os.Mkdir(disk, 0700)
	os.Mkdir(group, 0700)
	os.WriteFile(filepath.Join(disk, "disk"), []byte("retained"), 0600)
	os.WriteFile(filepath.Join(group, "cgroup.events"), []byte("populated 1\n"), 0600)
	exited := make(chan struct{})
	close(exited)
	vm := &VM{Dir: disk, exited: exited, cgroup: &vmCgroup{path: group}}
	if vm.Cleanup() == nil {
		t.Fatal("populated cgroup cleanup accepted")
	}
	if _, err := os.Stat(filepath.Join(disk, "disk")); err != nil {
		t.Fatal("disk removed before all cgroup tasks exited")
	}
}
func TestInvalidCgroupFDDoesNotStartUnconfinedChild(t *testing.T) {
	root := t.TempDir()
	fd, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	marker := filepath.Join(root, "unconfined")
	cmd := exec.Command("/bin/sh", "-c", "printf started > \"$1\"", "sh", marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(fd.Fd())}
	if cmd.Run() == nil {
		t.Fatal("non-cgroup FD accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("child fell back to unconfined execution")
	}
}
func TestRealDelegatedCgroupPreExecPlacement(t *testing.T) {
	if os.Getenv("CHICKADEE_CGROUP_SMOKE") != "1" {
		t.Skip("requires an explicitly delegated transient service; no live hierarchy changed by default")
	}
	c := &CgroupConfig{}
	if err := PrepareCgroup(c); err != nil {
		t.Fatal(err)
	}
	group, err := createCgroup(c, "0123456789abcdef", 1, 128)
	if err != nil {
		t.Fatal(err)
	}
	defer group.closeFD()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCgroupChildHelper$")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "CHICKADEE_CGROUP_CHILD=1"}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	write.Write([]byte("cgroup-fd-survived"))
	write.Close()
	cmd.ExtraFiles = []*os.File{read}

	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(group.file.Fd())}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pre-exec cgroup helper: %v %s", err, output)
	}
	for name, want := range map[string]string{"cpu.max": "100000 100000", "memory.max": strconv.FormatInt(640<<20, 10), "memory.swap.max": "0", "memory.oom.group": "1", "pids.max": "128", "io.weight": "default 100"} {
		got, err := readControl(group.path, name)
		if err != nil || got != want {
			t.Fatalf("actual %s controller limit mismatch", name)
		}
	}
	if err := removeEmptyCgroup(group.path); err != nil {
		t.Fatal(err)
	}
}
func TestCgroupChildHelper(t *testing.T) {
	if os.Getenv("CHICKADEE_CGROUP_CHILD") != "1" {
		return
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !strings.Contains(string(data), "/chickadee-vm-0123456789abcdef") {
		t.Fatal("process not born in target cgroup")
	}
	threads, err := os.ReadDir("/proc/self/task")
	if err != nil || len(threads) == 0 {
		t.Fatal("thread inventory unavailable")
	}
	for _, thread := range threads {
		data, err := os.ReadFile(filepath.Join("/proc/self/task", thread.Name(), "cgroup"))
		if err != nil || !strings.Contains(string(data), "/chickadee-vm-0123456789abcdef") {
			t.Fatal("thread escaped VM resource group")
		}
	}
	file := os.NewFile(3, "inherited-cgroup-test-fd")
	defer file.Close()
	data, err = io.ReadAll(file)
	if err != nil || string(data) != "cgroup-fd-survived" {
		t.Fatal("pre-exec cgroup placement changed inherited FD mappings")
	}
	// A newly created child must inherit the same VM cgroup before execution.
	cmd := exec.Command("/bin/cat", "/proc/self/cgroup")
	data, err = cmd.Output()
	if err != nil || !strings.Contains(string(data), "/chickadee-vm-0123456789abcdef") {
		t.Fatal("descendant escaped VM resource group")
	}
}
