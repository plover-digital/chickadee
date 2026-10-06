//go:build linux && amd64

package pool

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/protocol"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type serialMachine struct {
	v    *host.VM
	dir  string
	exit chan struct{}
	once sync.Once
}

func (m *serialMachine) Run(j string, d time.Duration, p string) error { return m.v.Run(j, d, p) }
func (m *serialMachine) Exited() <-chan struct{}                       { return m.exit }
func (m *serialMachine) Stop() error {
	m.once.Do(func() { m.v.Conn.Close(); close(m.exit) })
	return nil
}
func (m *serialMachine) Cleanup() error { m.Stop(); return os.RemoveAll(m.dir) }

type testBackend struct {
	mu                 sync.Mutex
	requested, removed []string
	demand             chan int
	fail               bool
}

func (b *testBackend) JIT(ctx context.Context, name string) (string, error) {
	b.mu.Lock()
	b.requested = append(b.requested, name)
	b.mu.Unlock()
	b.demand <- 0
	if b.fail {
		return "", errors.New("ambiguous JIT request")
	}
	return base64.StdEncoding.EncodeToString([]byte("test configuration")), nil
}
func (b *testBackend) Remove(ctx context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removed = append(b.removed, name)
	return nil
}
func TestPoolSerialLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "ambiguous-jit"}[fail], func(t *testing.T) {
			c := config.Config{StateDir: t.TempDir(), Warm: 1, Max: 1, ScaleSet: "chickadee", JobSeconds: 60}
			demand := make(chan int, 4)
			b := &testBackend{demand: demand, fail: fail}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			boots := make(chan string, 8)
			start := func(ctx context.Context, c config.Config, slot int, id string) (machine, error) {
				dir := filepath.Join(c.StateDir, "vms", id)
				if e := os.MkdirAll(dir, 0700); e != nil {
					return nil, e
				}
				if e := os.WriteFile(filepath.Join(dir, "disk.qcow2"), nil, 0600); e != nil {
					return nil, e
				}
				a, peer := net.Pipe()
				m := &serialMachine{v: &host.VM{Conn: a, Reader: protocol.NewReader(a)}, dir: dir, exit: make(chan struct{})}
				go func() {
					defer peer.Close()
					f, e := protocol.NewReader(peer).Read()
					if e != nil || f.Type != "CONFIG" {
						return
					}
					_ = protocol.Write(peer, protocol.Frame{V: 1, Type: "ACK"})
					_ = protocol.Write(peer, protocol.Frame{V: 1, Type: "RUNNING"})
					_ = protocol.Write(peer, protocol.Frame{V: 1, Type: "LOG", Data: base64.StdEncoding.EncodeToString([]byte("diagnostic"))})
					_ = protocol.Write(peer, protocol.Frame{V: 1, Type: "DONE"})
				}()
				boots <- id
				return m, nil
			}
			done := make(chan error, 1)
			go func() { done <- run(ctx, c, b, demand, start) }()
			demand <- 1
			var first, second string
			select {
			case first = <-boots:
			case <-time.After(5 * time.Second):
				t.Fatal("no initial VM")
			}
			select {
			case second = <-boots:
			case <-time.After(5 * time.Second):
				t.Fatal("no replacement")
			}
			if first == second {
				t.Fatal("reused VM")
			}
			if _, e := os.Stat(filepath.Join(c.StateDir, "vms", first)); !os.IsNotExist(e) {
				t.Fatal("credentialed overlay retained")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				b.mu.Lock()
				n := len(b.removed)
				b.mu.Unlock()
				if n > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("stale registration not removed")
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			if e := <-done; !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.requested) != 1 || len(b.removed) < 1 {
				t.Fatal("credentials reused or registration cleanup missing")
			}
			dirs, _ := os.ReadDir(filepath.Join(c.StateDir, "vms"))
			if len(dirs) != 0 {
				t.Fatal("shutdown did not clean warm VM")
			}
		})
	}
}
func TestRestartReconciliationRetainsFailedRemovals(t *testing.T) {
	c := config.Config{StateDir: t.TempDir(), ScaleSet: "chickadee"}
	_ = os.MkdirAll(filepath.Join(c.StateDir, "records"), 0700)
	_ = os.MkdirAll(filepath.Join(c.StateDir, "vms"), 0700)
	r := host.Record{ID: "0123456789abcdef", Name: "chickadee-0123456789abcdef"}
	if e := host.Save(c.StateDir, r); e != nil {
		t.Fatal(e)
	}
	b := &removalFailure{}
	if reconcile(context.Background(), c, b) == nil {
		t.Fatal("ignored removal failure")
	}
	rs, e := host.Records(c.StateDir)
	if e != nil || len(rs) != 1 {
		t.Fatal("lost cleanup intent")
	}
	b.ok = true
	if reconcile(context.Background(), c, b) != nil {
		t.Fatal("cleanup retry failed")
	}
	rs, _ = host.Records(c.StateDir)
	if len(rs) != 0 {
		t.Fatal("cleanup intent retained after removal")
	}
}

type removalFailure struct{ ok bool }

func (*removalFailure) JIT(context.Context, string) (string, error) { panic("unexpected JIT") }
func (b *removalFailure) Remove(context.Context, string) error {
	if b.ok {
		return nil
	}
	return errors.New("unavailable")
}

func TestAcquireCleansDisksAndEnforcesSingleOwner(t *testing.T) {
	c := config.Config{StateDir: t.TempDir()}
	old := filepath.Join(c.StateDir, "vms", "old", "disk.qcow2")
	if e := os.MkdirAll(filepath.Dir(old), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(old, nil, 0600); e != nil {
		t.Fatal(e)
	}
	owner, e := Acquire(c)
	if e != nil {
		t.Fatal(e)
	}
	defer owner.Close()
	if _, e = os.Stat(old); !os.IsNotExist(e) {
		t.Fatal("stale overlay not recovered")
	}
	second, e := Acquire(c)
	if e == nil {
		second.Close()
		t.Fatal("two controllers acquired state")
	}
}
func TestDelayedRegistrationIntentRetained(t *testing.T) {
	c := config.Config{StateDir: t.TempDir(), ScaleSet: "chickadee"}
	_ = os.MkdirAll(filepath.Join(c.StateDir, "records"), 0700)
	_ = os.MkdirAll(filepath.Join(c.StateDir, "vms"), 0700)
	r := host.Record{ID: "0123456789abcdef", Name: "chickadee-0123456789abcdef", NotBefore: time.Now().Add(time.Hour)}
	if e := host.Save(c.StateDir, r); e != nil {
		t.Fatal(e)
	}
	if e := reconcile(context.Background(), c, &removalFailure{ok: true}); e != nil {
		t.Fatal(e)
	}
	rs, e := host.Records(c.StateDir)
	if e != nil || len(rs) != 1 {
		t.Fatal("discarded intent before delayed-registration grace period")
	}
}
