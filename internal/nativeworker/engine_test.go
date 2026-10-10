package nativeworker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plover-digital/chickadee/workerapi"
)

type fakeSession struct {
	ready, done chan struct{}
	once        sync.Once
	calls       atomic.Int32
	cleaned     atomic.Bool
	fail        bool
}

func fake() *fakeSession {
	s := &fakeSession{ready: make(chan struct{}), done: make(chan struct{})}
	close(s.ready)
	return s
}
func (s *fakeSession) Ready() <-chan struct{} { return s.ready }
func (s *fakeSession) Done() <-chan struct{}  { return s.done }
func (s *fakeSession) Healthy() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}
func (s *fakeSession) Cleaned() bool { return s.cleaned.Load() }
func (s *fakeSession) Deliver(string, string) error {
	s.calls.Add(1)
	if s.fail {
		return fmt.Errorf("lost acknowledgement")
	}
	return nil
}
func (s *fakeSession) Stop()             { s.finish(true) }
func (s *fakeSession) finish(clean bool) { s.cleaned.Store(clean); s.once.Do(func() { close(s.done) }) }
func config(t *testing.T) Config {
	return Config{Version: 1, Identity: workerapi.Identity{WorkerID: "native", BrokerID: "broker", Generation: 1}, StateDir: filepath.Join(t.TempDir(), "state"), BaseDir: "/trusted/base", ProfileID: "macos-small", Digest: strings.Repeat("a", 64), Python: "/trusted/python", Pilot: "/trusted/pilot", Native: "/trusted/native", Netproxy: "/trusted/netproxy", DenyPrefixes: []string{"8.8.8.8/32"}, NetworkApproved: true, WarmPool: 1, JobTimeoutSeconds: 600, ReservationTimeoutSeconds: 120}
}
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition timeout")
}
func stop(t *testing.T, e *Engine) {
	t.Helper()
	e.Drain(e.c.Identity)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSharedWarmPoolAndNeverRepeatDelivery(t *testing.T) {
	c := config(t)
	var sessions sync.Map
	var started atomic.Int32
	e, err := open(c, func(id string) (session, error) { s := fake(); sessions.Store(id, s); started.Add(1); return s, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, e)
	waitFor(t, func() bool { v, _ := e.Inventory(); return v.Profiles[0].Ready == 1 })
	r, err := e.Reserve(context.Background(), c.Identity, "tenant-a", c.ProfileID, c.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Reserve(context.Background(), c.Identity, "tenant-b", c.ProfileID, c.Digest); err == nil {
		t.Fatal("per-customer pool or capacity overrun")
	}
	if started.Load() != 1 {
		t.Fatal("more than one physical VM")
	}
	if _, err = e.Seal(c.Identity, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if err = e.Deliver(c.Identity, "tenant-a", "YWJj"); err != nil {
		t.Fatal(err)
	}
	if err = e.Deliver(c.Identity, "tenant-a", "YWJj"); err != workerapi.ErrConsumed {
		t.Fatalf("replay not fenced: %v", err)
	}
	value, _ := sessions.Load(r.Request.VMID)
	s := value.(*fakeSession)
	if s.calls.Load() != 1 {
		t.Fatal("credentials replayed")
	}
	s.finish(true)
	waitFor(t, func() bool {
		v, _ := e.Inventory()
		return v.Profiles[0].Ready == 1 && len(v.Records) == 1 && v.Records[0].State == "terminal"
	})
	r2, err := e.Reserve(context.Background(), c.Identity, "tenant-b", c.ProfileID, c.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Request.VMID == r.Request.VMID {
		t.Fatal("spent VM reused")
	}
	value, _ = sessions.Load(r2.Request.VMID)
	value.(*fakeSession).finish(true)
}

func TestUnconfirmedExitRetainsBudgetAndStopsAdmission(t *testing.T) {
	c := config(t)
	s := fake()
	e, err := open(c, func(string) (session, error) { return s, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { v, _ := e.Inventory(); return v.Profiles[0].Ready == 1 })
	s.finish(false)
	waitFor(t, func() bool { v, _ := e.Inventory(); return v.Draining })
	v, _ := e.Inventory()
	if v.Used.VMs != 1 || v.Used.MemoryMiB != 4096 {
		t.Fatal("uncertain VM capacity released")
	}
	if _, err = e.Reserve(context.Background(), c.Identity, "new", c.ProfileID, c.Digest); err != workerapi.ErrUnavailable {
		t.Fatal("unsafe new admission")
	}
	// Tear down the fake only after asserting capacity stays retained in product.
	e.mu.Lock()
	e.current = nil
	e.mu.Unlock()
	stop(t, e)
}

func TestImageMismatchAndIdentityFence(t *testing.T) {
	c := config(t)
	s := fake()
	e, err := open(c, func(string) (session, error) { return s, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop(t, e)
	waitFor(t, func() bool { v, _ := e.Inventory(); return v.Profiles[0].Ready == 1 })
	if _, err = e.Reserve(context.Background(), c.Identity, "a", c.ProfileID, strings.Repeat("b", 64)); err != workerapi.ErrConflict {
		t.Fatal("wrong image accepted")
	}
	i := c.Identity
	i.Generation++
	if _, err = e.Reserve(context.Background(), i, "a", c.ProfileID, c.Digest); err != workerapi.ErrFenced {
		t.Fatal("generation not fenced")
	}
}
