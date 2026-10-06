//go:build linux && amd64

package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/protocol"
)

type VM struct {
	ID     string
	Slot   int
	Dir    string
	cmd    *exec.Cmd
	exited chan struct{}
	Conn   net.Conn
	Reader *protocol.Reader
}

func NewID() string {
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func Start(ctx context.Context, c config.Config, slot int, id string) (*VM, error) {
	return start(ctx, c, slot, id, false)
}

// StartBootCheck exercises the same boot/serial/disk path with guest networking blocked.
// It cannot deliver GitHub credentials or require host TAP/firewall changes.
func StartBootCheck(ctx context.Context, c config.Config, slot int, id string) (*VM, error) {
	return start(ctx, c, slot, id, true)
}
func start(ctx context.Context, c config.Config, slot int, id string, offline bool) (v *VM, err error) {
	v = &VM{ID: id, Slot: slot, Dir: filepath.Join(c.StateDir, "vms", id), exited: make(chan struct{})}
	if err = os.Mkdir(v.Dir, 0700); err != nil {
		return nil, err
	}
	// Keep directory on any failure; restart reconciliation removes it safely.
	disk := filepath.Join(v.Dir, "disk.qcow2")
	create := exec.CommandContext(ctx, "qemu-img", "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", filepath.Join(c.ImageDir, "base.qcow2"), disk, fmt.Sprintf("%dG", c.DiskGiB))
	create.Env = processEnv()
	if err = create.Run(); err != nil {
		return v, fmt.Errorf("overlay creation failed")
	}
	sock := filepath.Join(v.Dir, "serial.sock")
	netdev := fmt.Sprintf("tap,id=net,ifname=ck%02d,script=no,downscript=no", slot)
	if offline {
		netdev = "user,id=net,restrict=on"
	}
	args := []string{"--fsize=" + strconv.FormatInt(int64(c.DiskGiB+1)<<30, 10) + ":" + strconv.FormatInt(int64(c.DiskGiB+1)<<30, 10), "--", "qemu-system-x86_64",
		"-name", "chickadee-" + id, "-machine", "microvm,isa-serial=on,auto-kernel-cmdline=on", "-enable-kvm", "-cpu", "host", "-smp", strconv.Itoa(c.CPUs), "-m", strconv.Itoa(c.MemoryMiB),
		"-object", "rng-random,id=rng,filename=/dev/urandom", "-device", "virtio-rng-device,rng=rng",
		"-nodefaults", "-no-user-config", "-display", "none", "-monitor", "none", "-no-reboot",
		"-sandbox", "on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny",
		"-kernel", filepath.Join(c.ImageDir, "vmlinuz"), "-initrd", filepath.Join(c.ImageDir, "initrd"),
		"-append", fmt.Sprintf("root=LABEL=chickadee rw console=tty0 quiet panic=1 reboot=t net.ifnames=0 ck.slot=%d", slot),
		"-drive", "if=none,id=root,format=qcow2,file=" + disk, "-device", "virtio-blk-device,drive=root",
		"-netdev", netdev, "-device", "virtio-net-device,netdev=net",
		"-chardev", "socket,id=bootstrap,path=" + sock + ",server=on,wait=on", "-serial", "chardev:bootstrap"}
	v.cmd = exec.Command("prlimit", args...)
	v.cmd.Env = processEnv()
	v.cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	// Never forward untrusted guest console or QEMU arguments to journald.
	v.cmd.Stdout = io.Discard
	qlog, e := os.OpenFile(filepath.Join(c.StateDir, "logs", id+".qemu.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return v, fmt.Errorf("QEMU diagnostic file creation failed")
	}
	v.cmd.Stderr = &cappedWriter{w: qlog, left: 16 * 1024}
	if err = v.cmd.Start(); err != nil {
		qlog.Close()
		return v, fmt.Errorf("QEMU start failed")
	}
	go func() { _ = v.cmd.Wait(); _ = qlog.Close(); close(v.exited) }()
	stopCancel := context.AfterFunc(ctx, func() { _ = v.cmd.Process.Kill() })
	defer stopCancel()
	defer func() {
		if err != nil {
			_ = v.Stop()
		}
	}()
	deadline := time.Now().Add(time.Duration(c.BootSeconds) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-v.exited:
			return v, fmt.Errorf("QEMU exited before READY")
		default:
		}
		v.Conn, err = net.DialTimeout("unix", sock, 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if v.Conn == nil {
		return v, fmt.Errorf("serial connection timeout")
	}
	_ = v.Conn.SetDeadline(deadline)
	v.Reader = protocol.NewReader(v.Conn)
	f, e := v.Reader.Read()
	if e != nil || f.Type != "READY" {
		return v, fmt.Errorf("guest did not send READY")
	}
	_ = v.Conn.SetDeadline(time.Time{})
	return v, nil
}
func (v *VM) Stop() error {
	if v == nil {
		return nil
	}
	if v.Conn != nil {
		_ = v.Conn.Close()
	}
	if v.cmd == nil || v.cmd.Process == nil {
		return nil
	}
	select {
	case <-v.exited:
		return nil
	default:
	}
	_ = v.cmd.Process.Kill()
	select {
	case <-v.exited:
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("QEMU exit unconfirmed; disk retained")
	}
}
func (v *VM) Cleanup() error {
	if v == nil {
		return nil
	}
	if e := v.Stop(); e != nil {
		return e
	}
	return os.RemoveAll(v.Dir)
}
func (v *VM) Exited() <-chan struct{} { return v.exited }
func (v *VM) Run(jit string, timeout time.Duration, logPath string) error {
	_ = v.Conn.SetDeadline(time.Now().Add(15 * time.Second))
	if e := protocol.Write(v.Conn, protocol.Frame{V: 1, Type: "CONFIG", JIT: jit}); e != nil {
		return fmt.Errorf("configuration delivery failed")
	}
	f, e := v.Reader.Read()
	if e != nil || f.Type != "ACK" {
		return fmt.Errorf("configuration acknowledgement failed")
	}
	_ = v.Conn.SetDeadline(time.Now().Add(timeout))
	f, e = v.Reader.Read()
	if e != nil || f.Type != "RUNNING" {
		return fmt.Errorf("runner start status missing")
	}
	log, e := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	total := 0
	for {
		f, e = v.Reader.Read()
		if e != nil {
			return fmt.Errorf("guest status stream failed")
		}
		switch f.Type {
		case "LOG":
			total += len(f.Data) + 60
			if total > protocol.MaxLogs {
				return fmt.Errorf("diagnostic budget exceeded")
			}
			if e = protocol.Write(log, f); e != nil {
				return e
			}
		case "DONE":
			if f.Code != 0 {
				return fmt.Errorf("runner exited unsuccessfully")
			}
			return nil
		default:
			return fmt.Errorf("unexpected guest status")
		}
	}
}

// QEMU stderr is private, bounded, and never forwarded to host console logs.
type cappedWriter struct {
	w    io.Writer
	left int
}

func (w *cappedWriter) Write(b []byte) (int, error) {
	n := len(b)
	take := min(n, w.left)
	if take > 0 {
		written, e := w.w.Write(b[:take])
		w.left -= written
		if e != nil {
			return written, e
		}
	}
	return n, nil
}

// Hypervisor subprocesses need command lookup and a locale, not controller secrets.
func processEnv() []string { return []string{"PATH=" + os.Getenv("PATH"), "LANG=C"} }
