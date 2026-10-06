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
		// /proc state Z means the process exited but its parent has not reaped it.
		deadline := time.Now().Add(10 * time.Second)
		for {
			stat, e := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
			if os.IsNotExist(e) {
				break
			}
			if e != nil {
				syscall.Close(int(fd))
				return e
			}
			tail := strings.LastIndex(string(stat), ") ")
			if tail >= 0 && strings.HasPrefix(string(stat)[tail+2:], "Z ") {
				break
			}
			if time.Now().After(deadline) {
				syscall.Close(int(fd))
				return fmt.Errorf("owned QEMU did not exit; retaining disks")
			}
			time.Sleep(25 * time.Millisecond)
		}
		syscall.Close(int(fd))
	}
	return nil
}
