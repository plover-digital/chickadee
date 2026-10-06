//go:build linux && amd64

package host

import (
	"github.com/plover-digital/chickadee/internal/protocol"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
