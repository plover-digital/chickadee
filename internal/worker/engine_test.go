//go:build linux && amd64

package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	hostconfig "github.com/plover-digital/chickadee/internal/config"
)

type fakeMachine struct {
	dir          string
	exit         chan struct{}
	finish       chan struct{}
	once         sync.Once
	finishOnce   sync.Once
	mu           sync.Mutex
	runs         int
	cleanupError bool
}

func (f *fakeMachine) Run(_ string, timeout time.Duration, _ string) error {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	select {
	case <-f.finish:
		return nil
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}
func (f *fakeMachine) Exited() <-chan struct{} { return f.exit }
func (f *fakeMachine) Cleanup() error {
	f.mu.Lock()
	bad := f.cleanupError
	f.mu.Unlock()
	if bad {
		return errors.New("unconfirmed exit")
	}
	f.once.Do(func() { close(f.exit) })
	return os.RemoveAll(f.dir)
}
func (f *fakeMachine) complete() { f.finishOnce.Do(func() { close(f.finish) }) }

type fakeFactory struct {
	mu      sync.Mutex
	vms     map[string]*fakeMachine
	configs []hostconfig.Config
	slots   map[string]int
}

func (f *fakeFactory) start(_ context.Context, c hostconfig.Config, slot int, id string) (machine, error) {
	dir := filepath.Join(c.StateDir, "vms", id)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	os.WriteFile(filepath.Join(dir, "disk"), []byte("disposable"), 0600)
	vm := &fakeMachine{dir: dir, exit: make(chan struct{}), finish: make(chan struct{})}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vms[id] = vm
	if f.slots == nil {
		f.slots = map[string]int{}
	}
	f.slots[id] = slot
	f.configs = append(f.configs, c)
	return vm, nil
}
func engineConfig(t *testing.T) Config {
	t.Helper()
	root := privateDir(t)
	return Config{Version: 1, Identity: fixture().Identity, StateDir: filepath.Join(root, "state"), Profiles: []Profile{{ID: "medium", Digest: strings.Repeat("a", 64), ImageDir: filepath.Join(root, "image"), Machine: "q35", CPUs: 2, MemoryMiB: 4096, DiskGiB: 48, Warm: 1}}, Budget: Budget{MaxVMs: 2, MaxCPUs: 4, MaxMemoryMiB: 8192}, BootTimeoutSeconds: 1, JobTimeoutSeconds: 5, ReservationTimeoutSeconds: 1}
}
func testEngine(t *testing.T, c Config) (*Engine, *fakeFactory) {
	t.Helper()
	f := &fakeFactory{vms: map[string]*fakeMachine{}}
	e, err := openEngine(c, f.start, func(string) error { return nil }, func(Config) error { return nil }, unlimitedTestSpace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.Drain(c.Identity)
		f.mu.Lock()
		for _, vm := range f.vms {
			vm.complete()
		}
		f.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = e.Wait(ctx)
	})
	return e, f
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("lifecycle condition timed out")
}
func reserveEngine(t *testing.T, e *Engine, id Identity, assignment, profile string) Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := e.Reserve(ctx, id, assignment, profile)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEngineWarmAliasDispatchDrainAndReplacement(t *testing.T) {
	c := engineConfig(t)
	alias := c.Profiles[0]
	alias.ID = "alias"
	alias.Warm = 0
	c.Profiles = append(c.Profiles, alias)
	e, f := testEngine(t, c)
	eventually(t, func() bool { i, _ := e.Inventory(); return i.Profiles[0].Ready == 1 })
	f.mu.Lock()
	var warmID string
	for id := range f.vms {
		warmID = id
	}
	f.mu.Unlock()
	r := reserveEngine(t, e, c.Identity, "first", "alias")
	if r.Request.VMID != warmID {
		t.Fatal("alias did not reuse credential-free warm VM")
	}
	if _, err := e.Seal(c.Identity, "first"); err != nil {
		t.Fatal(err)
	}
	if err := e.Deliver(c.Identity, "first", "e30="); err != nil {
		t.Fatal(err)
	}
	if err := e.Deliver(c.Identity, "first", "e30="); !errors.Is(err, ErrConsumed) {
		t.Fatalf("duplicate JIT write permitted: %v", err)
	}
	f.mu.Lock()
	vm := f.vms[warmID]
	f.mu.Unlock()
	eventually(t, func() bool { vm.mu.Lock(); defer vm.mu.Unlock(); return vm.runs == 1 })
	// A broker wait/request timeout cannot cancel the ongoing job.
	if err := e.Drain(c.Identity); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if !errors.Is(e.Wait(ctx), context.DeadlineExceeded) {
		t.Fatal("drain stopped credentialed job")
	}
	select {
	case <-vm.exit:
		t.Fatal("job terminated on broker disconnect/drain")
	default:
	}
	vm.complete()
	eventually(t, func() bool {
		record, err := e.Status(c.Identity, "first")
		return err == nil && record.State == Terminal
	})
	f.mu.Lock()
	for _, cfg := range f.configs {
		if cfg.ClientID != "" || cfg.KeyFile != "" || cfg.GitHubURL != "" || cfg.InstallationID != 0 {
			t.Fatal("host received GitHub management fields")
		}
	}
	f.mu.Unlock()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := e.Wait(ctx2); err != nil {
		t.Fatal(err)
	}
}
func TestEngineSealedWithoutJITExpiresAndDestroys(t *testing.T) {
	c := engineConfig(t)
	e, f := testEngine(t, c)
	r := reserveEngine(t, e, c.Identity, "no-jit", "medium")
	if _, err := e.Seal(c.Identity, "no-jit"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := e.Seal(c.Identity, "no-jit"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		record, err := e.Status(c.Identity, "no-jit")
		return err == nil && record.State == Terminal
	})
	f.mu.Lock()
	vm := f.vms[r.Request.VMID]
	f.mu.Unlock()
	vm.mu.Lock()
	runs := vm.runs
	vm.mu.Unlock()
	if runs != 0 {
		t.Fatal("credentials unexpectedly delivered")
	}
	if _, err := os.Stat(vm.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("timed-out disk retained")
	}
	eventually(t, func() bool { i, _ := e.Inventory(); return i.Profiles[0].Ready == 1 })
}
func TestEngineBudgetsAndIncompatibleWarmEviction(t *testing.T) {
	c := engineConfig(t)
	c.Budget = Budget{MaxVMs: 1, MaxCPUs: 2, MaxMemoryMiB: 4096}
	small := c.Profiles[0]
	small.ID = "small"
	small.CPUs = 1
	small.MemoryMiB = 2048
	small.Warm = 0
	c.Profiles = append(c.Profiles, small)
	e, f := testEngine(t, c)
	eventually(t, func() bool { i, _ := e.Inventory(); return i.Profiles[0].Ready == 1 })
	r := reserveEngine(t, e, c.Identity, "small-request", "small")
	if r.Request.CPUs != 1 {
		t.Fatal("wrong resource shape")
	}
	f.mu.Lock()
	count := 0
	for _, vm := range f.vms {
		select {
		case <-vm.exit:
		default:
			count++
		}
	}
	f.mu.Unlock()
	if count != 1 {
		t.Fatalf("worker exceeded max VMs: %d", count)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.Reserve(ctx, c.Identity, "blocked", "medium"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capacity overcommitted: %v", err)
	}
}
func TestEngineCleanupFailureRetainsCapacityAndDoesNotMarkTerminal(t *testing.T) {
	c := engineConfig(t)
	c.Budget.MaxVMs = 1
	e, f := testEngine(t, c)
	r := reserveEngine(t, e, c.Identity, "failure", "medium")
	f.mu.Lock()
	vm := f.vms[r.Request.VMID]
	f.mu.Unlock()
	vm.mu.Lock()
	vm.cleanupError = true
	vm.mu.Unlock()
	eventually(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.fatal != nil })
	records, err := e.journal.Records()
	if err != nil {
		t.Fatal(err)
	}
	if records[0].State == Terminal {
		t.Fatal("uncertain VM marked terminal")
	}
	e.mu.Lock()
	count := len(e.entries)
	e.mu.Unlock()
	if count != 1 {
		t.Fatal("uncertain capacity released")
	}
	if _, err := os.Stat(vm.dir); err != nil {
		t.Fatal("disk deleted without confirmed exit")
	}
	// Test-owned recovery releases resources after assertions.
	vm.mu.Lock()
	vm.cleanupError = false
	vm.mu.Unlock()
	vm.Cleanup()
	e.journal.Close()
}
func TestEngineRestartReapsBeforeDeletingAndFencesOldGeneration(t *testing.T) {
	c := engineConfig(t)
	c.Profiles[0].Warm = 0
	j := openTest(t, c.StateDir, c.Identity)
	r := fixture()
	r.VMID = "0123456789abcdef"
	reserveSeal(t, j, r)
	j.Close()
	os.MkdirAll(filepath.Join(c.StateDir, "vms", r.VMID), 0700)
	os.WriteFile(filepath.Join(c.StateDir, "vms", r.VMID, "disk"), []byte("disk"), 0600)
	c.Identity.Generation++
	reaped := false
	e, err := openEngine(c, func(context.Context, hostconfig.Config, int, string) (machine, error) {
		t.Fatal("unexpected boot")
		return nil, nil
	}, func(dir string) error {
		if _, err := os.Stat(filepath.Join(dir, "vms", r.VMID, "disk")); err != nil {
			t.Fatal("disk removed before reap")
		}
		reaped = true
		return nil
	}, func(Config) error { return nil }, unlimitedTestSpace)
	if err != nil {
		t.Fatal(err)
	}
	if !reaped {
		t.Fatal("recovery skipped")
	}
	record, err := e.Status(c.Identity, r.AssignmentID)
	if err != nil || record.State != Terminal {
		t.Fatalf("recovery state: %v %v", record, err)
	}
	if _, err := e.Seal(r.Identity, r.AssignmentID); !errors.Is(err, ErrFenced) {
		t.Fatal("old broker generation accepted")
	}
	e.Drain(c.Identity)
	if err := e.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEngineRecoveryFailureNeverDeletesDiskOrReleasesIntent(t *testing.T) {
	c := engineConfig(t)
	c.Profiles[0].Warm = 0
	j := openTest(t, c.StateDir, c.Identity)
	r := fixture()
	reserveSeal(t, j, r)
	j.Close()
	disk := filepath.Join(c.StateDir, "vms", r.VMID, "disk")
	os.MkdirAll(filepath.Dir(disk), 0700)
	os.WriteFile(disk, []byte("retained"), 0600)
	if e, err := openEngine(c, nil, func(string) error { return errors.New("exit unconfirmed") }, func(Config) error { return nil }, unlimitedTestSpace); err == nil {
		e.journal.Close()
		t.Fatal("unsafe recovery accepted")
	}
	if _, err := os.Stat(disk); err != nil {
		t.Fatal("disk deleted despite failed process reap")
	}
	j = openTest(t, c.StateDir, c.Identity)
	records, err := j.Records()
	if err != nil || records[0].State != Sealed {
		t.Fatalf("credential intent released: %v %v", records, err)
	}
}

func TestEngineMissingStatusDistinctFromFencingAndMalformedIdentity(t *testing.T) {
	c := engineConfig(t)
	c.Profiles[0].Warm = 0
	e, _ := testEngine(t, c)
	if _, err := e.Status(c.Identity, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	wrong := c.Identity
	wrong.Generation++
	if _, err := e.Status(wrong, "missing"); !errors.Is(err, ErrFenced) {
		t.Fatalf("fenced: %v", err)
	}
	if _, err := e.Status(c.Identity, "../bad"); !errors.Is(err, ErrConflict) {
		t.Fatalf("malformed: %v", err)
	}
}
func TestEngineInsufficientDiskNeverStartsVM(t *testing.T) {
	c := engineConfig(t)
	c.Profiles[0].Warm = 0
	e, f := testEngine(t, c)
	e.mu.Lock()
	e.spaceCheck = func(Config, int64) error { return errors.New("disk reservation unavailable") }
	e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := e.Reserve(ctx, c.Identity, "new", "medium"); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("disk admission did not fail immediately: %v", err)
	}
	f.mu.Lock()
	count := len(f.vms)
	f.mu.Unlock()
	if count != 0 {
		t.Fatal("boot started before disk capacity verified")
	}
	e.journal.Close()
}

func TestRealStarterEarlyFailureDoesNotReturnTypedNilMachine(t *testing.T) {
	c := hostconfig.Config{StateDir: filepath.Join(privateDir(t), "missing-parent")}
	vm, err := startHost(context.Background(), c, 1, "0123456789abcdef")
	if err == nil || vm != nil {
		t.Fatalf("early host failure wrapped typed nil: %v %v", vm, err)
	}
}
func TestStartupDiskReservationFailsBeforeAnyBoot(t *testing.T) {
	c := engineConfig(t)
	c.Profiles[0].DiskGiB = 1024
	c.Profiles[0].Warm = 0
	starts := 0
	e, err := openEngine(c, func(context.Context, hostconfig.Config, int, string) (machine, error) {
		starts++
		return nil, errors.New("unexpected")
	}, func(string) error { return nil }, func(Config) error { return nil }, func(c Config, allocated int64) error { return checkDiskCapacity(c, allocated, 14<<30) })
	if err == nil {
		e.journal.Close()
		t.Fatal("worker started with unavailable reserved capacity")
	}
	if starts != 0 {
		t.Fatal("boot before startup disk reservation")
	}
}

func unlimitedTestSpace(c Config, allocated int64) error {
	return checkDiskCapacity(c, allocated, 1<<50)
}

func TestEngineThreeSmallBudgetFourthDeniedAndCleanedSlotReused(t *testing.T) {
	c := engineConfig(t)
	c.Budget = Budget{MaxVMs: 3, MaxCPUs: 6, MaxMemoryMiB: 12288}
	c.Profiles[0].ID = "small"
	c.Profiles[0].Warm = 0
	e, f := testEngine(t, c)
	records := make([]Record, 3)
	for i, name := range []string{"one", "two", "three"} {
		records[i] = reserveEngine(t, e, c.Identity, name, "small")
		if _, err := e.Seal(c.Identity, name); err != nil {
			t.Fatal(err)
		}
		if err := e.Deliver(c.Identity, name, "e30="); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := e.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Used.VMs != 3 || inventory.Used.CPUs != 6 || inventory.Used.MemoryMiB != 12288 {
		t.Fatalf("incorrect allocated budget: %+v", inventory.Used)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := e.Reserve(ctx, c.Identity, "four", "small"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fourth small guest admitted: %v", err)
	}
	f.mu.Lock()
	first := f.vms[records[0].Request.VMID]
	oldSlot := f.slots[records[0].Request.VMID]
	f.mu.Unlock()
	first.complete()
	eventually(t, func() bool { record, err := e.Status(c.Identity, "one"); return err == nil && record.State == Terminal })
	next := reserveEngine(t, e, c.Identity, "four", "small")
	if next.Request.VMID == records[0].Request.VMID {
		t.Fatal("spent VM reused")
	}
	f.mu.Lock()
	newSlot := f.slots[next.Request.VMID]
	f.mu.Unlock()
	if newSlot != oldSlot {
		t.Fatalf("cleaned TAP slot not reused: got%d want%d", newSlot, oldSlot)
	}
	if _, err := e.Seal(c.Identity, "four"); err != nil {
		t.Fatal(err)
	}
	if err := e.Deliver(c.Identity, "four", "e30="); err != nil {
		t.Fatal(err)
	}
}
func TestEngineTwoMediumDeniedButMediumPlusSmallAllowed(t *testing.T) {
	c := engineConfig(t)
	c.Budget = Budget{MaxVMs: 3, MaxCPUs: 6, MaxMemoryMiB: 12288}
	c.Profiles[0].CPUs = 4
	c.Profiles[0].MemoryMiB = 8192
	c.Profiles[0].Warm = 0
	small := c.Profiles[0]
	small.ID = "small"
	small.CPUs = 2
	small.MemoryMiB = 4096
	c.Profiles = append(c.Profiles, small)
	e, _ := testEngine(t, c)
	reserveEngine(t, e, c.Identity, "medium-one", "medium")
	if _, err := e.Seal(c.Identity, "medium-one"); err != nil {
		t.Fatal(err)
	}
	if err := e.Deliver(c.Identity, "medium-one", "e30="); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := e.Reserve(ctx, c.Identity, "medium-two", "medium"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("two medium guests admitted: %v", err)
	}
	reserveEngine(t, e, c.Identity, "small-one", "small")
	if _, err := e.Seal(c.Identity, "small-one"); err != nil {
		t.Fatal(err)
	}
	if err := e.Deliver(c.Identity, "small-one", "e30="); err != nil {
		t.Fatal(err)
	}
	inventory, err := e.Inventory()
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Used.VMs != 2 || inventory.Used.CPUs != 6 || inventory.Used.MemoryMiB != 12288 {
		t.Fatalf("mixed budget wrong: %+v", inventory.Used)
	}
}
