//go:build linux && amd64

// chickadee-guest is a root bootstrap; runner processes run without root privileges.
package main

import (
	"encoding/base64"
	"fmt"
	"github.com/plover-digital/chickadee/internal/protocol"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

func raw(f *os.File) error {
	var t syscall.Termios
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	if e != 0 {
		return e
	}
	t.Iflag = 0
	t.Oflag = 0
	t.Lflag = 0
	t.Cflag = syscall.B115200 | syscall.CS8 | syscall.CREAD | syscall.CLOCAL
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	_, _, e = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&t)))
	if e != 0 {
		return e
	}
	return nil
}
func bootstrap() error {
	s, e := os.OpenFile("/dev/ttyS0", os.O_RDWR, 0)
	if e != nil {
		return e
	}
	defer s.Close()
	if e = os.Chmod("/dev/ttyS0", 0600); e != nil {
		return e
	}
	if e = raw(s); e != nil {
		return e
	}
	environment, e := runnerEnvironment()
	if e != nil {
		return e
	}
	credential, e := runnerCredential()
	if e != nil {
		return e
	}
	r := protocol.NewReader(s)
	if e = protocol.Write(s, protocol.Frame{V: 1, Type: "READY"}); e != nil {
		return e
	}
	f, e := r.Read()
	if e != nil || f.Type != "CONFIG" {
		return fmt.Errorf("configuration rejected")
	}
	if e = protocol.Write(s, protocol.Frame{V: 1, Type: "ACK"}); e != nil {
		return e
	}
	cmd := exec.Command("/opt/actions-runner/bin/Runner.Listener", "run", "--jitconfig", f.JIT)
	cmd.Dir = "/opt/actions-runner"
	cmd.Env = environment
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	code := 0
	if e = cmd.Start(); e != nil {
		code = 1
	} else {
		if e = protocol.Write(s, protocol.Frame{V: 1, Type: "RUNNING"}); e != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return e
		}
		if e = cmd.Wait(); e != nil {
			code = 1
			if x, ok := e.(*exec.ExitError); ok && x.ExitCode() >= 0 {
				code = x.ExitCode()
			}
		}
	}
	diagnostics(s)
	_ = protocol.Write(s, protocol.Frame{V: 1, Type: "DONE", Code: code})
	// Do not reboot until DONE has drained. Host owns destruction, including failure paths.
	for {
		time.Sleep(time.Hour)
	}
}
func diagnostics(w io.Writer) {
	paths, _ := filepath.Glob("/opt/actions-runner/_diag/*.log")
	budget := protocol.MaxLogs / 2
	for _, p := range paths {
		if budget <= 0 {
			return
		}
		fd, e := syscall.Open(p, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			continue
		}
		file := os.NewFile(uintptr(fd), p)
		stat, e := file.Stat()
		if e != nil || !stat.Mode().IsRegular() {
			file.Close()
			continue
		}
		header := []byte("\n--- " + filepath.Base(p) + " ---\n")
		if len(header) > budget {
			file.Close()
			return
		}
		if send(w, header) != nil {
			file.Close()
			return
		}
		budget -= len(header)
		buf := make([]byte, 4096)
		for budget > 0 {
			n, e := file.Read(buf[:min(len(buf), budget)])
			if n > 0 {
				if send(w, buf[:n]) != nil {
					file.Close()
					return
				}
				budget -= n
			}
			if e != nil {
				break
			}
		}
		file.Close()
	}
}
func send(w io.Writer, b []byte) error {
	return protocol.Write(w, protocol.Frame{V: 1, Type: "LOG", Data: base64.StdEncoding.EncodeToString(b)})
}
func main() {
	if bootstrap() != nil {
		os.Exit(1)
	}
}
