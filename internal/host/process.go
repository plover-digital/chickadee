//go:build linux && amd64

package host

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ReapOwned scans rather than trusting a persisted PID (a crash can precede PID journaling).
// pidfds prevent PID reuse from redirecting the signal to an unrelated process.
func ReapOwned(dir string) error {
	entries, e := os.ReadDir("/proc")
	if e != nil {
		return e
	}
	for _, entry := range entries {
		pid, e := strconv.Atoi(entry.Name())
		if e != nil {
			continue
		}
		exe, e := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if e != nil || strings.TrimSuffix(filepath.Base(exe), " (deleted)") != "qemu-system-x86_64" {
			continue
		}
		cmd, e := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if e != nil {
			continue
		}
		args := bytes.Split(cmd, []byte{0})
		owned := false
		for i, a := range args {
			if string(a) == "-drive" && i+1 < len(args) && strings.HasPrefix(string(args[i+1]), "if=none,id=root,format=qcow2,file="+dir+"/vms/") {
				owned = true
			}
		}
		if !owned {
			continue
		}
		fd, _, errno := syscall.Syscall(434, uintptr(pid), 0, 0)
		if errno == syscall.ESRCH {
			continue
		}
		if errno != 0 {
			return errno
		}
		again, e := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if e != nil || !bytes.Equal(cmd, again) {
			syscall.Close(int(fd))
			return fmt.Errorf("process identity changed during recovery")
		}
		_, _, errno = syscall.Syscall6(424, fd, uintptr(syscall.SIGKILL), 0, 0, 0, 0)
		if errno != 0 && errno != syscall.ESRCH {
			syscall.Close(int(fd))
			return errno
		}
		if e = waitPIDFD(int(fd), 10*time.Second); e != nil {
			syscall.Close(int(fd))
			return e
		}
		syscall.Close(int(fd))
	}
	return nil
}

// A process pidfd becomes readable only after the whole thread group exits.
// Checking /proc state alone could see a zombie leader while other threads remain.
func waitPIDFD(fd int, timeout time.Duration) error {
	type pollFD struct {
		FD      int32
		Events  int16
		Revents int16
	}
	p := pollFD{FD: int32(fd), Events: 1}
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("owned QEMU did not exit; retaining disks")
		}
		ms := (remaining + time.Millisecond - 1) / time.Millisecond
		n, _, errno := syscall.Syscall(syscall.SYS_POLL, uintptr(unsafe.Pointer(&p)), 1, uintptr(ms))
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		if n > 0 {
			if p.Revents&0x11 != 0 {
				return nil
			}
			return fmt.Errorf("invalid pidfd poll result; retaining disks")
		}
	}
}
