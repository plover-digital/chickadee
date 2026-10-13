//go:build linux && amd64

package landlockrunner

import (
	"archive/tar"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/landlock-lsm/go-landlock/landlock"
	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"github.com/plover-digital/chickadee/internal/protocol"
)

func restrict() error {
	// Runner path validation lists every ancestor. Permit directory metadata only
	// in the already-private mount tree, without broad file read/write rights.
	rules := []landlock.Rule{landlock.PathAccess(landlock.AccessFSSet(ll.AccessFSReadDir), "/"), landlock.RWDirs("/job").WithRefer(), landlock.RODirs("/proc", "/etc", "/image"), landlock.ROFiles("/engine"), landlock.RWFiles("/dev/null"), landlock.ROFiles("/dev/zero", "/dev/random", "/dev/urandom")}
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if _, err := os.Stat(dir); err == nil {
			rules = append(rules, landlock.RODirs(dir))
		}
	}
	// Strict V6 requires filesystem/truncate/ioctl and IPC-scope enforcement.
	// Local test servers may use any loopback port. Outbound networking is
	// constrained by the empty network namespace and the public-only relay,
	// since Landlock port rules cannot distinguish host/private IP destinations.
	policy := landlock.V6
	policy.HandledAccessNet = 0
	return policy.Restrict(rules...)
}

// Child is invoked only inside the worker's mandatory private namespace setup.
// It reports READY before receiving credentials and runs exactly one listener.
func Child(probe bool) error {
	if os.Getpid() != 1 {
		return fmt.Errorf("private PID namespace required")
	}
	if err := restrict(); err != nil {
		return fmt.Errorf("required Landlock policy unavailable")
	}
	if probe {
		return probeIsolation()
	}
	if err := extractRunner("/image/runner.tar.gz", "/job/runner"); err != nil {
		return err
	}
	for _, dir := range []string{"/job/home", "/job/tmp", "/job/work", "/job/tools"} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	server, err := childProxy(os.NewFile(3, "private-relay"))
	if err != nil {
		return err
	}
	defer server.Close()
	if err = protocol.Write(os.Stdout, protocol.Frame{V: 1, Type: "READY"}); err != nil {
		return err
	}
	frame, err := protocol.NewReader(os.Stdin).Read()
	if err != nil || frame.Type != "CONFIG" {
		return fmt.Errorf("invalid one-time configuration")
	}
	os.Stdin.Close()
	if err = protocol.Write(os.Stdout, protocol.Frame{V: 1, Type: "ACK"}); err != nil {
		return err
	}
	command := exec.Command("/job/runner/run.sh")
	command.Dir = "/job/runner"
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/home/runner", "TMPDIR=/tmp", "LANG=C.UTF-8", "RUNNER_TOOL_CACHE=/job/tools", "https_proxy=http://127.0.0.1:3128", "http_proxy=http://127.0.0.1:3128", "HTTPS_PROXY=http://127.0.0.1:3128", "HTTP_PROXY=http://127.0.0.1:3128", "NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1", "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt", "CURL_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt", "GIT_SSL_CAINFO=/etc/ssl/certs/ca-certificates.crt", "NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt", "ACTIONS_RUNNER_INPUT_JITCONFIG=" + frame.JIT}
	frame.JIT = ""
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err = protocol.Write(os.Stdout, protocol.Frame{V: 1, Type: "RUNNING"}); err != nil {
		return err
	}
	err = command.Run()
	code := 0
	if err != nil {
		code = 1
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() >= 0 && exit.ExitCode() <= 255 {
			code = exit.ExitCode()
		}
	}
	diagnostics()
	return protocol.Write(os.Stdout, protocol.Frame{V: 1, Type: "DONE", Code: code})
}

func extractRunner(archive, destination string) error {
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var total int64
	for count := 0; ; count++ {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil || count >= 50000 {
			return fmt.Errorf("invalid or oversized runner archive")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name == "" || name == "." {
			continue
		}
		if path.IsAbs(name) || path.Clean(name) != strings.TrimSuffix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("archive path escapes runner")
		}
		name = strings.TrimSuffix(name, "/")
		if err = root.MkdirAll(path.Dir(name), 0700); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err = root.MkdirAll(name, 0700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > 1<<30 || total+header.Size > 2<<30 {
				return fmt.Errorf("runner archive exceeds bounds")
			}
			total += header.Size
			out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0755)
			if err != nil {
				return err
			}
			_, err = io.CopyN(out, reader, header.Size)
			closeErr := out.Close()
			if err != nil || closeErr != nil {
				return fmt.Errorf("runner extraction failed")
			}
		case tar.TypeSymlink:
			target := path.Clean(path.Join(path.Dir(name), header.Linkname))
			if path.IsAbs(header.Linkname) || target == ".." || strings.HasPrefix(target, "../") {
				return fmt.Errorf("runner symlink escapes archive")
			}
			if err = root.Symlink(header.Linkname, name); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported runner archive entry")
		}
	}
}

// Open nonblocking first, then verify the opened inode: the job can replace a
// directory entry between ReadDir and open with a FIFO, device or symlink.
func openDiagnostic(directory *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(directory.Fd()), name, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "diagnostic")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("diagnostic is not a regular file")
	}
	return file, nil
}

func diagnostics() {
	// Copy only bounded regular files, never follow workflow-created symlinks or
	// render their contents. Chunks remain opaque private protocol diagnostics.
	fd, err := syscall.Open("/job/runner/_diag", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	directory := os.NewFile(uintptr(fd), "diagnostic-directory")
	defer directory.Close()
	// Bound directory work as well as copied bytes; job-created entries are untrusted.
	entries, err := directory.ReadDir(64)
	if err != nil && err != io.EOF {
		return
	}
	remaining := int64(256 * 1024)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || remaining == 0 {
			continue
		}
		file, err := openDiagnostic(directory, entry.Name())
		if err != nil {
			continue
		}
		buf := make([]byte, 4096)
		for remaining > 0 {
			n, _ := file.Read(buf[:min(int64(len(buf)), remaining)])
			if n == 0 {
				break
			}
			remaining -= int64(n)
			if protocol.Write(os.Stdout, protocol.Frame{V: 1, Type: "LOG", Data: base64.StdEncoding.EncodeToString(buf[:n])}) != nil {
				file.Close()
				return
			}
		}
		file.Close()
	}
}
func probeIsolation() error {
	for _, directory := range []string{"/", "/job", "/job/home"} {
		if _, err := os.ReadDir(directory); err != nil {
			return fmt.Errorf("runner ancestor directory enumeration denied")
		}
	}
	if err := os.WriteFile("/job/allowed", []byte("ok"), 0600); err != nil {
		return err
	}
	for _, p := range []string{"/root", "/run/docker.sock", "/etc/chickadee", "/sys", "/var/lib/chickadee"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			return fmt.Errorf("host management path visible")
		}
	}
	if err := os.WriteFile("/usr/chickadee-denied", nil, 0600); err == nil {
		return fmt.Errorf("host runtime writable")
	}
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, ":") && !strings.HasPrefix(strings.TrimSpace(line), "lo:") {
			return fmt.Errorf("host network interface visible")
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	conn, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		return fmt.Errorf("private loopback unavailable")
	}
	conn.Close()
	if conn, err = net.DialTimeout("tcp4", "1.1.1.1:443", time.Second); err == nil {
		conn.Close()
		return fmt.Errorf("direct outbound network reachable")
	}
	if err := protocol.Write(os.Stdout, protocol.Frame{V: 1, Type: "READY"}); err != nil {
		return err
	}
	return nil
}
