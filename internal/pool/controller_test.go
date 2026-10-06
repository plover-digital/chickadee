//go:build linux && amd64

package pool

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/protocol"
)

// This is a real process boundary, not a QEMU boot. The injected starter keeps
// lifecycle tests independent of host TAP provisioning and mandatory sandboxing.
func TestPoolProcessHelper(t *testing.T) {
	if os.Getenv("CHICKADEE_POOL_PROCESS_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

type processMachine struct {
	vm     *host.VM
	dir    string
	cmd    *exec.Cmd
	exited chan struct{}
	stop   sync.Once
}

func (m *processMachine) Run(jit string, timeout time.Duration, path string) error {
	return m.vm.Run(jit, timeout, path)
}
func (m *processMachine) Exited() <-chan struct{} { return m.exited }
func (m *processMachine) Stop() error {
	m.stop.Do(func() { m.vm.Conn.Close(); _ = m.cmd.Process.Kill() })
	select {
	case <-m.exited:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("test process exit unconfirmed")
	}
}
func (m *processMachine) Cleanup() error {
	if err := m.Stop(); err != nil {
		return err
	}
	return os.RemoveAll(m.dir)
}

type processBackend struct {
	mu             sync.Mutex
	names, removed []string
	demand         chan int
	fail           bool
}

func (b *processBackend) JIT(ctx context.Context, name string) (string, error) {
	b.mu.Lock()
	b.names = append(b.names, name)
	b.mu.Unlock()
	select {
	case b.demand <- 0:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if b.fail {
		return "", errors.New("ambiguous test JIT failure")
	}
	return base64.StdEncoding.EncodeToString([]byte("test-only-config")), nil
}
func (b *processBackend) Remove(ctx context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removed = append(b.removed, name)
	return nil
}

func TestLifecycleAndAmbiguousJITFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-job", true: "jit-failure"}[fail], func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "ck-life-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			c := config.Config{StateDir: filepath.Join(dir, "state"), ImageDir: dir, Warm: 1, Max: 1, CPUs: 1, MemoryMiB: 512, DiskGiB: 8, BootSeconds: 10, JobSeconds: 60, ScaleSet: "chickadee", GitHubURL: "https://github.com/example-org/example-repo"}
			demand := make(chan int, 8)
			b := &processBackend{demand: demand, fail: fail}
			var machinesMu sync.Mutex
			machines := map[string]*processMachine{}
			boots := make(chan string, 8)
			delivered := make(chan string, 8)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			start := func(ctx context.Context, c config.Config, slot int, id string) (machine, error) {
				vmDir := filepath.Join(c.StateDir, "vms", id)
				if err := os.Mkdir(vmDir, 0700); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(vmDir, "disk.qcow2"), nil, 0600); err != nil {
					return nil, err
				}
				a, peer := net.Pipe()
				cmd := exec.Command(executable, "-test.run=^TestPoolProcessHelper$")
				cmd.Env = []string{"CHICKADEE_POOL_PROCESS_HELPER=1"}
				if err := cmd.Start(); err != nil {
					a.Close()
					peer.Close()
					return nil, err
				}
				m := &processMachine{vm: &host.VM{ID: id, Conn: a, Reader: protocol.NewReader(a)}, dir: vmDir, cmd: cmd, exited: make(chan struct{})}
				go func() { _ = cmd.Wait(); close(m.exited) }()
				machinesMu.Lock()
				machines[id] = m
				machinesMu.Unlock()
				go func() {
					defer peer.Close()
					f, err := protocol.NewReader(peer).Read()
					if err != nil || f.Type != "CONFIG" {
						return
					}
					delivered <- id
					_ = protocol.Write(peer, protocol.Frame{V: 1, Type: "ACK"})
					_ = protocol.Write(peer, protocol.Frame{V: 1, Type: "RUNNING"})
					_ = protocol.Write(peer, protocol.Frame{V: 1, Type: "LOG", Data: base64.StdEncoding.EncodeToString([]byte("test diagnostic"))})
					_ = protocol.Write(peer, protocol.Frame{V: 1, Type: "DONE"})
				}()
				boots <- id
				return m, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- run(ctx, c, b, demand, start) }()
			demand <- 1
			var first, second string
			for index := 0; index < 2; index++ {
				select {
				case id := <-boots:
					if index == 0 {
						first = id
					} else {
						second = id
					}
				case err := <-done:
					t.Fatalf("controller exited before replacement: %v", err)
				case <-time.After(8 * time.Second):
					t.Fatal("VM replacement failed")
				}
			}
			if first == second {
				t.Fatal("credential-intent VM reused")
			}
			machinesMu.Lock()
			old := machines[first]
			machinesMu.Unlock()
			select {
			case <-old.exited:
			default:
				t.Fatal("replacement before actual process exit")
			}
			if _, err = os.Stat(filepath.Join(c.StateDir, "vms", first)); !os.IsNotExist(err) {
				t.Fatal("old overlay retained after replacement")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				b.mu.Lock()
				removedCount := len(b.removed)
				b.mu.Unlock()
				if removedCount > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("stale spent registration not reconciled")
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			select {
			case err = <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown did not confirm process cleanup")
			}
			b.mu.Lock()
			names := append([]string(nil), b.names...)
			removed := append([]string(nil), b.removed...)
			b.mu.Unlock()
			if len(names) != 1 {
				t.Fatalf("generated JIT %d times", len(names))
			}
			if len(removed) < 1 {
				t.Fatal("spent runner registration not removed")
			}
			for _, name := range removed {
				if name != names[0] {
					t.Fatal("removed unrelated registration")
				}
			}

			if strings.TrimPrefix(names[0], "chickadee-") != first {
				t.Fatal("credentials assigned to wrong VM")
			}
			if fail {
				select {
				case <-delivered:
					t.Fatal("ambiguous JIT failure reached guest")
				default:
				}
			} else {
				select {
				case id := <-delivered:
					if id != first {
						t.Fatal("credentials reached replacement")
					}
				default:
					t.Fatal("guest did not receive configuration")
				}
			}
			select {
			case <-delivered:
				t.Fatal("configuration delivered more than once")
			default:
			}
			vms, err := os.ReadDir(filepath.Join(c.StateDir, "vms"))
			if err != nil || len(vms) != 0 {
				t.Fatal("shutdown left overlays", err)
			}
			machinesMu.Lock()
			defer machinesMu.Unlock()
			for _, m := range machines {
				select {
				case <-m.exited:
				default:
					t.Fatal("shutdown left helper process")
				}
			}
			if !fail {
				logs, _ := filepath.Glob(filepath.Join(c.StateDir, "logs", "*.jsonl"))
				if len(logs) != 1 {
					t.Fatal("diagnostics not retained")
				}
				data, err := os.ReadFile(logs[0])
				if err != nil || !strings.Contains(string(data), base64.StdEncoding.EncodeToString([]byte("test diagnostic"))) {
					t.Fatal("real serial diagnostic handler lost log")
				}
			}
		})
	}
}
