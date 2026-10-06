//go:build linux && amd64

package pool_test

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/pool"
	"github.com/plover-digital/chickadee/internal/protocol"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestQEMUHelper simulates just the process and serial boundaries; it is not a VM boot test.
func TestQEMUHelper(t *testing.T) {
	if os.Getenv("CHICKADEE_HELPER") != "1" {
		return
	}
	sock := ""
	disk := ""
	for i, a := range os.Args {
		if a == "-chardev" && i+1 < len(os.Args) {
			for _, x := range strings.Split(os.Args[i+1], ",") {
				if strings.HasPrefix(x, "path=") {
					sock = strings.TrimPrefix(x, "path=")
				}
			}
		}
		if a == "-drive" && i+1 < len(os.Args) {
			for _, x := range strings.Split(os.Args[i+1], ",") {
				if strings.HasPrefix(x, "file=") {
					disk = strings.TrimPrefix(x, "file=")
				}
			}
		}
	}
	if sock == "" || disk == "" {
		os.Exit(2)
	}
	_ = os.WriteFile(filepath.Join(os.Getenv("CHICKADEE_TRACE"), filepath.Base(filepath.Dir(disk))+".boot"), nil, 0600)
	l, e := net.Listen("unix", sock)
	if e != nil {
		_ = os.WriteFile(filepath.Join(os.Getenv("CHICKADEE_TRACE"), "helper-error"), []byte(e.Error()), 0600)
		os.Exit(3)
	}
	defer l.Close()
	c, e := l.Accept()
	if e != nil {
		os.Exit(4)
	}
	defer c.Close()
	r := protocol.NewReader(c)
	_ = protocol.Write(c, protocol.Frame{V: 1, Type: "READY"})
	f, e := r.Read()
	if e != nil || f.Type != "CONFIG" {
		os.Exit(6)
	}
	_ = os.WriteFile(filepath.Join(os.Getenv("CHICKADEE_TRACE"), filepath.Base(filepath.Dir(disk))+".spent"), nil, 0600)
	_ = protocol.Write(c, protocol.Frame{V: 1, Type: "ACK"})
	_ = protocol.Write(c, protocol.Frame{V: 1, Type: "RUNNING"})
	_ = protocol.Write(c, protocol.Frame{V: 1, Type: "LOG", Data: base64.StdEncoding.EncodeToString([]byte("test diagnostic"))})
	_ = protocol.Write(c, protocol.Frame{V: 1, Type: "DONE"})
	for {
		time.Sleep(time.Hour)
	}
}

type backend struct {
	mu      sync.Mutex
	names   []string
	removed []string
	demand  chan int
	fail    bool
}

func (b *backend) JIT(ctx context.Context, name string) (string, error) {
	b.mu.Lock()
	b.names = append(b.names, name)
	b.mu.Unlock()
	b.demand <- 0
	if b.fail {
		return "", errors.New("ambiguous test JIT failure")
	}
	return base64.StdEncoding.EncodeToString([]byte("test-only-config")), nil
}
func (b *backend) Remove(ctx context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removed = append(b.removed, name)
	return nil
}
func helperPATH(t *testing.T, dir string) {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if e := os.Mkdir(bin, 0700); e != nil {
		t.Fatal(e)
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("CHICKADEE_HELPER", "1")
	t.Setenv("CHICKADEE_TEST_BIN", exe)
	t.Setenv("CHICKADEE_TRACE", dir)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	for name, script := range map[string]string{"qemu-img": "#!/bin/sh\ntouch \"$9\"\n", "qemu-system-x86_64": "#!/bin/sh\nexport CHICKADEE_HELPER=1\nexport CHICKADEE_TRACE=" + quoteShell(dir) + "\nexec " + quoteShell(exe) + " -test.run=TestQEMUHelper -- \"$@\"\n"} {
		if e = os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); e != nil {
			t.Fatal(e)
		}
	}
}
func TestLifecycleAndAmbiguousJITFailure(t *testing.T) {
	probe, e := net.Listen("unix", filepath.Join(t.TempDir(), "probe.sock"))
	if e != nil {
		t.Skipf("requires Unix socket binding: %v", e)
	}
	probe.Close()

	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-job", true: "jit-failure"}[fail], func(t *testing.T) {
			// Keep Unix socket paths short even when testing under long checkout paths.
			dir, e := os.MkdirTemp("/tmp", "ck-")
			if e != nil {
				t.Fatal(e)
			}
			defer os.RemoveAll(dir)
			helperPATH(t, dir)
			c := config.Config{StateDir: filepath.Join(dir, "state"), ImageDir: dir, Warm: 1, Max: 1, CPUs: 1, MemoryMiB: 512, DiskGiB: 4, BootSeconds: 10, JobSeconds: 60, ScaleSet: "chickadee"}
			demand := make(chan int, 8)
			b := &backend{demand: demand, fail: fail}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- pool.Run(ctx, c, b, demand) }()
			demand <- 1
			deadline := time.Now().Add(12 * time.Second)
			var first string
			replaced := false
			for time.Now().Before(deadline) {
				b.mu.Lock()
				if len(b.names) > 0 {
					first = strings.TrimPrefix(b.names[0], "chickadee-")
				}
				removed := len(b.removed) > 0
				b.mu.Unlock()
				boots, _ := filepath.Glob(filepath.Join(dir, "*.boot"))
				if first != "" && len(boots) >= 2 && removed {
					if _, e = os.Stat(filepath.Join(c.StateDir, "vms", first)); !os.IsNotExist(e) {
						t.Fatal("old overlay retained after replacement")
					}
					replaced = true
					break
				}
				select {
				case e := <-done:
					t.Fatalf("controller exited early: %v", e)
				default:
				}
				time.Sleep(20 * time.Millisecond)
			}
			cancel()
			if e = <-done; !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			if !replaced {
				errText, _ := os.ReadFile(filepath.Join(dir, "helper-error"))
				t.Fatalf("VM replacement failed; helper: %s", errText)
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.names) != 1 {
				t.Fatalf("delivered configuration %d times", len(b.names))
			}
			vms, _ := os.ReadDir(filepath.Join(c.StateDir, "vms"))
			if len(vms) != 0 {
				t.Fatal("shutdown left overlays")
			}
			if !fail {
				logs, _ := filepath.Glob(filepath.Join(c.StateDir, "logs", "*.jsonl"))
				if len(logs) != 1 {
					t.Fatal("diagnostics not retained")
				}
			}
		})
	}
}

func quoteShell(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
