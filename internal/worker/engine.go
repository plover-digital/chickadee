//go:build linux && amd64

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	hostconfig "github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/landlockrunner"
	"github.com/plover-digital/chickadee/internal/protocol"
	"github.com/plover-digital/chickadee/workerapi"
)

type machine interface {
	Run(string, time.Duration, string) error
	Exited() <-chan struct{}
	Cleanup() error
}
type startMachine func(context.Context, hostconfig.Config, int, string) (machine, error)
type ProfileInventory struct {
	ID        string `json:"id"`
	Digest    string `json:"digest"`
	Machine   string `json:"machine"`
	CPUs      int    `json:"cpus"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGiB   int    `json:"disk_gib"`
	Ready     int    `json:"ready"`
	Booting   int    `json:"booting"`
}
type CapacityUsed struct {
	VMs       int `json:"vms"`
	CPUs      int `json:"cpus"`
	MemoryMiB int `json:"memory_mib"`
}

type Inventory struct {
	Identity Identity           `json:"identity"`
	Draining bool               `json:"draining"`
	Budget   Budget             `json:"budget"`
	Used     CapacityUsed       `json:"used"`
	Profiles []ProfileInventory `json:"profiles"`
	Records  []Record           `json:"records"`
}
type instance struct {
	id            string
	slot          int
	profile       Profile
	state         State
	vm            machine
	request       *Request
	cancel        context.CancelFunc
	failedCleanup bool
	cpuIDs        []int
}
type Engine struct {
	mu         sync.Mutex
	config     Config
	journal    *Journal
	start      startMachine
	spaceCheck func(Config, int64) error
	profiles   map[string]Profile
	entries    map[string]*instance
	changed    chan struct{}
	draining   bool
	fatal      error
	closed     bool
	runResults map[string]bool
	waiters    int
	nextBoot   map[string]time.Time
	// deferred holds the capacity refusal for a profile in boot backoff.
	deferred map[string]error
	// journalFullLogged limits the full-journal warning to once per episode.
	journalFullLogged bool
	// capacityRetry spaces boot attempts while host capacity is unavailable.
	capacityRetry time.Duration
	// diskAvailable measures free bytes on the state filesystem.
	diskAvailable func(Config) (int64, error)
}

// ErrCapacity is a retryable refusal: host capacity such as free disk is
// currently insufficient. It never drains the worker or touches running jobs.
var ErrCapacity = errors.New("worker capacity unavailable")

// defaultCapacityRetry spaces boot attempts while host capacity is unavailable.
const defaultCapacityRetry = 30 * time.Second

func startHost(ctx context.Context, conf hostconfig.Config, slot int, id string) (machine, error) {
	vm, err := host.Start(ctx, conf, slot, id)
	if vm == nil {
		return nil, err
	}
	return vm, err
}
func OpenEngine(c Config) (*Engine, error) {
	c = cloneConfig(c)
	if c.IsLandlock() {
		return openEngine(c, func(ctx context.Context, conf hostconfig.Config, slot int, id string) (machine, error) {
			m, err := landlockrunner.StartWithAccess(ctx, conf, id, c.LandlockDenyPrefixes, c.profileAccess(conf.WorkerProfileID), slot, c.Budget.MaxVMs, c.protectedFor(conf.WorkerProfileID, slot)...)
			if m == nil {
				return nil, err
			}
			return m, err
		}, func(state string) error {
			plans, err := c.accessPlans()
			if err != nil {
				return err
			}
			return landlockrunner.ReapWithAccess(state, c.Cgroup, plans)
		}, verifyImages, checkDiskSpace)
	}

	return openEngine(c, startHost, host.ReapOwned, func(c Config) error {
		if err := host.ValidateCPUSet(c.CPUIDs); err != nil {
			return err
		}
		return verifyImages(c)
	}, checkDiskSpace)
}
func cloneConfig(c Config) Config {
	c.Profiles = append([]Profile(nil), c.Profiles...)
	for i := range c.Profiles {
		c.Profiles[i].HostAccess = landlockrunner.CloneAccess(c.Profiles[i].HostAccess)
	}
	c.ProtectedPaths = append([]string(nil), c.ProtectedPaths...)
	if c.Cgroup != nil {
		copy := *c.Cgroup
		copy.IOMax = append([]hostconfig.IOLimit(nil), c.Cgroup.IOMax...)
		c.Cgroup = &copy
	}
	c.CPUIDs = append([]int(nil), c.CPUIDs...)
	c.LandlockDenyPrefixes = append([]string(nil), c.LandlockDenyPrefixes...)
	return c
}

func openEngine(c Config, start startMachine, reap func(string) error, check func(Config) error, spaceCheck func(Config, int64) error) (*Engine, error) {
	c = cloneConfig(c)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := claimHostState(c.StateDir); err != nil {
		return nil, err
	}
	if c.IsLandlock() {
		if err := landlockrunner.CheckOwnership(c.StateDir); err != nil {
			return nil, err
		}
		plans, err := c.accessPlans()
		if err != nil {
			return nil, err
		}
		for _, plan := range plans {
			if err := landlockrunner.ClaimWorkspace(plan.WorkDir); err != nil {
				return nil, err
			}
		}
	}
	j, err := Open(c.StateDir, c.Identity)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Engine, error) { j.Close(); return nil, err }
	for _, name := range []string{"vms", "logs"} {
		path := filepath.Join(c.StateDir, name)
		if err = os.MkdirAll(path, 0700); err != nil {
			return fail(err)
		}
		info, err := os.Lstat(path)
		if err != nil || !ownedPrivate(info, false) {
			return fail(fmt.Errorf("worker runtime directory must be private and owned"))
		}
	}
	if err = host.CheckCgroupBudget(c.Cgroup, c.Budget.MaxMemoryMiB, c.Budget.MaxVMs); err != nil {
		return fail(err)
	}
	if err = host.PrepareCgroup(c.ResourceCgroup()); err != nil {
		return fail(err)
	}
	if c.IsLandlock() {
		hasCPU, err := host.HasCPUController(c.Cgroup)
		if err != nil {
			return fail(err)
		}
		if !hasCPU && len(c.CPUIDs) == 0 {
			return fail(fmt.Errorf("host_access without CPU quota requires cpu_ids"))
		}
	}
	if err = check(c); err != nil {
		return fail(err)
	}
	if legacy, legacyErr := os.ReadDir(filepath.Join(c.StateDir, "records")); legacyErr == nil && len(legacy) > 0 {
		return fail(fmt.Errorf("standalone registration intents require operator migration before keyless worker mode"))
	} else if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
		return fail(legacyErr)
	}
	// Recovery is strictly local. A broker restart never opens/reaps the worker.
	if err = reap(c.StateDir); err != nil {
		return fail(fmt.Errorf("owned process exit not confirmed"))
	}
	if err = host.ReapCgroups(c.Cgroup); err != nil {
		return fail(err)
	}
	dirs, err := os.ReadDir(filepath.Join(c.StateDir, "vms"))
	if err != nil {
		return fail(err)
	}
	for _, dir := range dirs {
		if !dir.IsDir() || !idPattern.MatchString(dir.Name()) {
			return fail(fmt.Errorf("unexpected VM runtime entry"))
		}
		if err = os.RemoveAll(filepath.Join(c.StateDir, "vms", dir.Name())); err != nil {
			return fail(err)
		}
	}
	records, err := j.Records()
	if err != nil {
		return fail(err)
	}
	for _, record := range records {
		if record.State != Terminal {
			if _, err = j.RecoverTerminal(record.Request.AssignmentID, ExitProof{VMID: record.Request.VMID, ProcessExitConfirmed: true, QEMUExitConfirmed: !c.IsLandlock(), DiskRemoved: true}); err != nil {
				return fail(err)
			}
		}
	}
	if err = spaceCheck(c, 0); err != nil {
		return fail(err)
	}
	e := &Engine{config: c, journal: j, start: start, spaceCheck: spaceCheck, profiles: map[string]Profile{}, entries: map[string]*instance{}, changed: make(chan struct{}), nextBoot: map[string]time.Time{}, deferred: map[string]error{}, capacityRetry: defaultCapacityRetry, diskAvailable: availableDisk}
	for _, profile := range c.Profiles {
		e.profiles[profile.ID] = profile
	}
	e.mu.Lock()
	if err = e.pruneLogsLocked(); err != nil {
		e.mu.Unlock()
		return fail(err)
	}
	e.maintainLocked()
	e.mu.Unlock()
	return e, nil
}
func (e *Engine) notifyLocked() { close(e.changed); e.changed = make(chan struct{}) }
func compatible(a, b Profile) bool {
	return accessEqual(a.HostAccess, b.HostAccess) && a.Digest == b.Digest && a.Machine == b.Machine && a.CPUs == b.CPUs && a.MemoryMiB == b.MemoryMiB && a.DiskGiB == b.DiskGiB
}
func (e *Engine) fitsLocked(p Profile) bool {
	cpus, memory := p.CPUs, p.MemoryMiB
	for _, vm := range e.entries {
		cpus += vm.profile.CPUs
		memory += vm.profile.MemoryMiB
	}
	return len(e.entries) < e.config.Budget.MaxVMs && cpus <= e.config.Budget.MaxCPUs && memory <= e.config.Budget.MaxMemoryMiB
}
func (e *Engine) maintainLocked() {
	if e.draining || e.fatal != nil || e.closed || e.waiters > 0 {
		return
	}
	if e.journal.Full() {
		// No warm VM can be reserved until rotation (#29). Release idle,
		// credential-free capacity instead of booting more.
		retired := false
		for _, v := range e.entries {
			if v.state == "ready" {
				v.state = "retiring"
				go e.cleanup(v)
				retired = true
			} else if v.state == "booting" {
				v.state = "retiring"
				v.cancel()
				retired = true
			}
		}
		if retired {
			e.notifyLocked()
		}
		return
	}
	for _, p := range e.config.Profiles {
		free := 0
		for _, v := range e.entries {
			if compatible(v.profile, p) && (v.state == "ready" || v.state == "booting") {
				free++
			}
		}
		for free < p.Warm && e.fitsLocked(p) && !time.Now().Before(e.nextBoot[p.ID]) {
			if e.bootLocked(p) != nil {
				if e.fatal != nil || e.draining {
					return
				}
				break
			}
			free++
		}
	}
}

// bootLocked starts one VM. ErrCapacity is a retryable refusal with backoff;
// any other error has already latched the engine.
func (e *Engine) bootLocked(p Profile) error {
	if err := e.pruneLogsLocked(); err != nil {
		e.failLocked(err)
		return err
	}
	if e.fatal != nil {
		return e.fatal
	}
	if e.draining {
		return fmt.Errorf("worker draining")
	}
	if err := e.spaceCheck(e.config, e.allocatedDiskLocked()); err != nil {
		// Low disk refuses new boots without draining: capacity returns once space
		// is freed, and existing jobs are unaffected.
		err = fmt.Errorf("%w: %v", ErrCapacity, err)
		e.deferBootLocked(p, err)
		return err
	}
	if e.deferred[p.ID] != nil {
		delete(e.deferred, p.ID)
		slog.Info("VM boot capacity restored", "profile", p.ID)
	}
	var cpuIDs []int
	if len(e.config.CPUIDs) > 0 {
		usedCPUs := map[int]bool{}
		for _, other := range e.entries {
			for _, id := range other.cpuIDs {
				usedCPUs[id] = true
			}
		}
		for _, id := range e.config.CPUIDs {
			if !usedCPUs[id] {
				cpuIDs = append(cpuIDs, id)
				if len(cpuIDs) == p.CPUs {
					break
				}
			}
		}
		if len(cpuIDs) != p.CPUs {
			err := fmt.Errorf("configured CPU pool capacity unavailable")
			e.failLocked(err)
			return err
		}
	}
	used := map[int]bool{}
	for _, v := range e.entries {
		used[v.slot] = true
	}
	slot := 1
	for used[slot] {
		slot++
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(e.config.BootTimeoutSeconds)*time.Second)
	v := &instance{id: host.NewID(), slot: slot, profile: p, state: "booting", cancel: cancel, cpuIDs: cpuIDs}
	e.entries[v.id] = v
	e.notifyLocked()
	go e.boot(ctx, v)
	return nil
}

// allocatedDiskLocked sums allocated overlay blocks, which already count
// against the growth reservation.
func (e *Engine) allocatedDiskLocked() int64 {
	allocated := int64(0)
	for _, v := range e.entries {
		allocated += overlayBlocks(e.config, v.id)
	}
	return allocated
}

// evictionPlanLocked picks the idle, credential-free VMs of other profiles
// that Reserve evicts for p: booting before ready, then by ID, stopping once
// p would fit. It returns none while VMs already retiring will make room. If
// even all of them cannot make p fit, it returns all of them, as before, so
// capacity frees up as running jobs finish.
func (e *Engine) evictionPlanLocked(p Profile) []*instance {
	var idle []*instance
	for _, v := range e.entries {
		if !compatible(v.profile, p) && (v.state == "ready" || v.state == "booting") {
			idle = append(idle, v)
		}
	}
	sort.Slice(idle, func(i, j int) bool {
		if idle[i].state != idle[j].state {
			return idle[i].state == "booting"
		}
		return idle[i].id < idle[j].id
	})
	// Entries already retiring are about to free their share; count them as
	// gone so a wake-up during their cleanup does not pick further victims.
	// Admission itself still counts them through fitsLocked.
	count, cpus, memory := 1, p.CPUs, p.MemoryMiB
	for _, v := range e.entries {
		if v.state == "retiring" && !v.failedCleanup {
			continue
		}
		count++
		cpus += v.profile.CPUs
		memory += v.profile.MemoryMiB
	}
	fits := func() bool {
		return count <= e.config.Budget.MaxVMs && cpus <= e.config.Budget.MaxCPUs && memory <= e.config.Budget.MaxMemoryMiB
	}
	if fits() {
		return nil // Pending cleanup makes room; wait for it.
	}
	for i, v := range idle {
		count, cpus, memory = count-1, cpus-v.profile.CPUs, memory-v.profile.MemoryMiB
		if fits() {
			return idle[:i+1]
		}
	}
	return idle
}

// evictionSpaceLocked refuses an eviction that could not make the boot pass
// its space check, so a refusal never destroys warm capacity it cannot
// replace. Victims and VMs already retiring (their disks are about to be
// freed) are credited from one measurement.
func (e *Engine) evictionSpaceLocked(victims []*instance) error {
	planned := map[*instance]bool{}
	for _, v := range victims {
		planned[v] = true
	}
	allocated, credit := int64(0), int64(0)
	for _, v := range e.entries {
		blocks := overlayBlocks(e.config, v.id)
		allocated += blocks
		if planned[v] || v.state == "retiring" && !v.failedCleanup {
			credit += blocks
		}
	}
	available, err := e.diskAvailable(e.config)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCapacity, err)
	}
	if !evictionWouldFit(e.config, allocated, credit, available) {
		return fmt.Errorf("%w: insufficient disk space even after eviction", ErrCapacity)
	}
	return nil
}

// evictionWouldFit reports whether the boot space check passes once credit
// bytes of overlays are freed. Freeing moves bytes from allocated to
// available: that cancels against the growth reservation but can satisfy
// the fixed 2 GiB and min_free_disk_gib floors.
func evictionWouldFit(c Config, allocated, credit, available int64) bool {
	if credit < 0 || credit > allocated || available < 0 {
		return false
	}
	return checkDiskCapacity(c, allocated-credit, available+credit) == nil
}

func overlayBlocks(c Config, id string) int64 {
	info, err := os.Stat(filepath.Join(c.StateDir, "vms", id, "disk.qcow2"))
	if err != nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Blocks * 512
	}
	return 0
}

// refuseFullJournalLocked logs once per full-journal episode; callers retry often.
func (e *Engine) refuseFullJournalLocked() {
	if !e.journalFullLogged {
		e.journalFullLogged = true
		slog.Warn("Reservation refused; worker journal full", "records", MaxRecords)
	}
}

// deferBootLocked backs off one profile and re-runs maintenance afterwards.
func (e *Engine) deferBootLocked(p Profile, err error) {
	if e.deferred[p.ID] == nil {
		slog.Warn("VM boot deferred; capacity unavailable", "profile", p.ID, "error", err, "retry_seconds", int(e.capacityRetry/time.Second))
	}
	e.deferred[p.ID] = err
	delay := e.capacityRetry
	e.nextBoot[p.ID] = time.Now().Add(delay)
	go func() {
		time.Sleep(delay)
		e.mu.Lock()
		defer e.mu.Unlock()
		e.notifyLocked()
		e.maintainLocked()
	}()
}
func (e *Engine) boot(ctx context.Context, v *instance) {
	started := time.Now()
	c := hostconfig.Config{WorkerProfileID: v.profile.ID, Cgroup: e.config.ProfileCgroup(v.profile), AssignedCPUs: append([]int(nil), v.cpuIDs...), StateDir: e.config.StateDir, ImageDir: v.profile.ImageDir, Machine: v.profile.Machine, CPUs: v.profile.CPUs, MemoryMiB: v.profile.MemoryMiB, DiskGiB: v.profile.DiskGiB, BootSeconds: e.config.BootTimeoutSeconds, JobSeconds: e.config.JobTimeoutSeconds}
	vm, err := e.start(ctx, c, v.slot, v.id)
	if vm == nil && err == nil {
		err = fmt.Errorf("missing VM lifecycle handle")
	}
	v.cancel()
	e.mu.Lock()
	v.vm = vm
	if err != nil && !e.draining {
		e.nextBoot[v.profile.ID] = time.Now().Add(time.Second)
		go func() {
			time.Sleep(time.Second)
			e.mu.Lock()
			defer e.mu.Unlock()
			e.notifyLocked()
			e.maintainLocked()
		}()
	}
	if err != nil || e.draining || v.state == "retiring" {
		v.state = "retiring"
		e.notifyLocked()
		e.mu.Unlock()
		e.cleanup(v)
		return
	}
	v.state = "ready"
	slog.Info("Guest READY", "profile", v.profile.ID, "boot_ms", time.Since(started).Milliseconds(), "controller_pid", os.Getpid(), "execution", v.id, "slot", v.slot)
	e.notifyLocked()
	e.mu.Unlock()
	go func() {
		<-vm.Exited()
		e.mu.Lock()
		if v.state != "retiring" && v.state != DeliveryIntent {
			v.state = "retiring"
			e.notifyLocked()
			e.mu.Unlock()
			e.cleanup(v)
			return
		}
		e.mu.Unlock()
	}()
}
func (e *Engine) cleanup(v *instance) {
	if v.vm != nil {
		if err := v.vm.Cleanup(); err != nil {
			e.cleanupFailed(v, fmt.Errorf("VM cleanup failed; capacity retained"))
			return
		}
		select {
		case <-v.vm.Exited():
		default:
			e.cleanupFailed(v, fmt.Errorf("VM exit unconfirmed; capacity retained"))
			return
		}
	}
	if _, err := os.Lstat(filepath.Join(e.config.StateDir, "vms", v.id)); !errors.Is(err, os.ErrNotExist) {
		e.cleanupFailed(v, fmt.Errorf("VM disk cleanup unconfirmed; capacity retained"))
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if v.request != nil {
		var resources *workerapi.ResourceSummary
		if measured, ok := v.vm.(interface {
			ResourceSummary() *workerapi.ResourceSummary
		}); ok {
			resources = measured.ResourceSummary()
		}
		if _, err := e.journal.RecoverTerminal(v.request.AssignmentID, ExitProof{VMID: v.id, ProcessExitConfirmed: true, QEMUExitConfirmed: v.profile.Machine != "landlock", DiskRemoved: true, Resources: resources}); err != nil {
			v.failedCleanup = true
			e.failLocked(err)
			return
		}
	}
	delete(e.entries, v.id)
	if err := e.pruneLogsLocked(); err != nil {
		e.failLocked(err)
		return
	}
	e.notifyLocked()
	e.maintainLocked()
}
func (e *Engine) failLocked(err error) {
	if e.fatal == nil {
		e.fatal = err
		slog.Error("Worker stopped admission; restart after resolving", "error", err)
	}
	e.draining = true
	for _, idle := range e.entries {
		if idle.state == "ready" {
			idle.state = "retiring"
			go e.cleanup(idle)
		} else if idle.state == "booting" {
			idle.state = "retiring"
			idle.cancel()
		}
	}
	e.notifyLocked()
}
func (e *Engine) cleanupFailed(v *instance, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	v.failedCleanup = true
	e.failLocked(err)
}
func (e *Engine) fail(err error) { e.mu.Lock(); defer e.mu.Unlock(); e.failLocked(err) }
func (e *Engine) authenticateLocked(identity Identity) error {
	if e.closed {
		return ErrUncertain
	}
	if identity != e.config.Identity {
		return ErrFenced
	}
	return nil
}
func (e *Engine) identityLocked(identity Identity) error {
	if err := e.authenticateLocked(identity); err != nil {
		return err
	}
	return e.fatal
}
func (e *Engine) findLocked(assignmentID string) (Record, *instance, error) {
	if !idPattern.MatchString(assignmentID) {
		return Record{}, nil, ErrConflict
	}
	records, err := e.journal.Records()
	if err != nil {
		return Record{}, nil, err
	}
	for _, record := range records {
		if record.Request.AssignmentID == assignmentID {
			return record, e.entries[record.Request.VMID], nil
		}
	}
	return Record{}, nil, ErrNotFound
}
func (e *Engine) Reserve(ctx context.Context, identity Identity, assignmentID, profileID string) (Record, error) {
	e.mu.Lock()
	e.waiters++
	e.mu.Unlock()
	defer func() { e.mu.Lock(); defer e.mu.Unlock(); e.waiters--; e.maintainLocked() }()
	if !idPattern.MatchString(assignmentID) {
		return Record{}, ErrConflict
	}
	for {
		e.mu.Lock()
		if err := e.identityLocked(identity); err != nil {
			e.mu.Unlock()
			return Record{}, err
		}
		p, ok := e.profiles[profileID]
		if !ok {
			e.mu.Unlock()
			return Record{}, ErrConflict
		}
		if record, execution, err := e.findLocked(assignmentID); err == nil {
			// The durable VMID binds this request to an immutable live profile.
			// Image/resource equality alone cannot authorize a different access policy.
			// Recovery makes every prior live request terminal before admission.
			if (execution != nil && !compatible(execution.profile, p)) || (execution == nil && record.State != Terminal) {
				e.mu.Unlock()
				return Record{}, ErrConflict
			}
			if record.Request.Identity != identity || record.Request.ProfileDigest != p.Digest || record.Request.CPUs != p.CPUs || record.Request.MemoryMiB != p.MemoryMiB || record.Request.DiskGiB != p.DiskGiB {
				e.mu.Unlock()
				return Record{}, ErrConflict
			}
			e.mu.Unlock()
			return record, nil
		} else if !errors.Is(err, ErrNotFound) {
			e.mu.Unlock()
			return Record{}, err
		}
		if e.draining {
			e.mu.Unlock()
			return Record{}, fmt.Errorf("worker draining")
		}
		// Refuse before reserving, evicting or booting anything (#29: terminal
		// records are never pruned, so this persists until rotation).
		if e.journal.Full() {
			e.refuseFullJournalLocked()
			e.mu.Unlock()
			return Record{}, ErrRecordBudget
		}
		e.journalFullLogged = false
		booting := false
		for _, v := range e.entries {
			if compatible(v.profile, p) && v.state == "booting" {
				booting = true
			}
			if compatible(v.profile, p) && v.state == "ready" {
				select {
				case <-v.vm.Exited():
					v.state = "retiring"
					go e.cleanup(v)
					continue
				default:
				}
				workFolder := ""
				if p.HostAccess != nil {
					workFolder, _ = p.HostAccess.Workspace(v.slot, e.config.Budget.MaxVMs)
				}
				if checker, ok := v.vm.(interface{ ValidateReady() error }); ok {
					if err := checker.ValidateReady(); err != nil {
						v.state = "retiring"
						go e.cleanup(v)
						continue
					}
				}
				request := Request{WorkFolder: workFolder, Identity: identity, AssignmentID: assignmentID, VMID: v.id, ProfileDigest: p.Digest, CPUs: p.CPUs, MemoryMiB: p.MemoryMiB, DiskGiB: p.DiskGiB}
				record, err := e.journal.Reserve(request)
				if errors.Is(err, ErrRecordBudget) {
					// Nothing was committed; refuse this reservation and keep serving.
					e.refuseFullJournalLocked()
					e.mu.Unlock()
					return Record{}, err
				}
				if err != nil {
					e.failLocked(err)
					e.mu.Unlock()
					return Record{}, err
				}
				v.request = &request
				v.state = Reserved
				e.notifyLocked()
				e.maintainLocked()
				e.mu.Unlock()
				go e.expire(v)
				return record, nil
			}
		}
		if !booting {
			// A capacity refusal in backoff must not evict other profiles' VMs.
			if err := e.deferred[p.ID]; err != nil && time.Now().Before(e.nextBoot[p.ID]) {
				e.mu.Unlock()
				return Record{}, err
			}
		}
		if !booting && e.fitsLocked(p) && !time.Now().Before(e.nextBoot[p.ID]) {
			if err := e.bootLocked(p); errors.Is(err, ErrCapacity) {
				e.mu.Unlock()
				return Record{}, err
			}
		}
		if !booting && !e.fitsLocked(p) {
			// Nothing idle to evict: wait for running work to finish.
			if victims := e.evictionPlanLocked(p); len(victims) > 0 {
				if err := e.evictionSpaceLocked(victims); err != nil {
					e.deferBootLocked(p, err)
					e.mu.Unlock()
					return Record{}, err
				}
				for _, idle := range victims {
					if idle.state == "ready" {
						idle.state = "retiring"
						go e.cleanup(idle)
					} else {
						idle.state = "retiring"
						idle.cancel()
					}
				}
				e.notifyLocked()
			}
		}
		if e.fatal != nil {
			err := e.fatal
			e.mu.Unlock()
			return Record{}, err
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return Record{}, ctx.Err()
		case <-changed:
		}
	}
}
func (e *Engine) expire(v *instance) {
	timer := time.NewTimer(time.Duration(e.config.ReservationTimeoutSeconds) * time.Second)
	defer timer.Stop()
	<-timer.C
	e.mu.Lock()
	if e.entries[v.id] != v || (v.state != Reserved && v.state != Sealed) {
		e.mu.Unlock()
		return
	}
	v.state = "retiring"
	e.notifyLocked()
	e.mu.Unlock()
	e.cleanup(v)
}
func (e *Engine) Seal(identity Identity, assignmentID string) (Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.identityLocked(identity); err != nil {
		return Record{}, err
	}
	record, v, err := e.findLocked(assignmentID)
	if err != nil {
		return Record{}, err
	}
	if record.Request.Identity != identity {
		return Record{}, ErrFenced
	}
	if v == nil || v.state == "retiring" {
		return Record{}, ErrTransition
	}
	if checker, ok := v.vm.(interface{ ValidateReady() error }); ok {
		if err := checker.ValidateReady(); err != nil {
			v.state = "retiring"
			go e.cleanup(v)
			return Record{}, ErrTransition
		}
	}
	record, err = e.journal.Seal(record.Request)
	if err != nil {
		if errors.Is(err, ErrUncertain) {
			e.failLocked(err)
		}
		return Record{}, err
	}
	v.state = record.State
	e.notifyLocked()
	return record, nil
}
func (e *Engine) Deliver(identity Identity, assignmentID, jit string) error {
	if err := protocol.Validate(protocol.Frame{V: 1, Type: "CONFIG", JIT: jit}); err != nil {
		return fmt.Errorf("invalid bounded JIT payload")
	}
	e.mu.Lock()
	if err := e.identityLocked(identity); err != nil {
		e.mu.Unlock()
		return err
	}
	record, v, err := e.findLocked(assignmentID)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	if record.Request.Identity != identity {
		e.mu.Unlock()
		return ErrFenced
	}
	if record.State == DeliveryIntent || record.State == Terminal {
		e.mu.Unlock()
		return ErrConsumed
	}
	if v == nil || v.state == "retiring" {
		e.mu.Unlock()
		return ErrTransition
	}
	if _, err = e.journal.DeliverIntent(record.Request); err != nil {
		if errors.Is(err, ErrUncertain) {
			e.failLocked(err)
		}
		e.mu.Unlock()
		return err
	}
	v.state = DeliveryIntent
	e.notifyLocked()
	e.mu.Unlock()
	go func() {
		runErr := v.vm.Run(jit, time.Duration(e.config.JobTimeoutSeconds)*time.Second, filepath.Join(e.config.StateDir, "logs", v.id+".jsonl"))
		e.mu.Lock()
		if e.runResults == nil {
			e.runResults = make(map[string]bool)
		}
		e.runResults[v.request.AssignmentID] = runErr != nil
		if runErr != nil {
			// Never render guest-controlled errors or configuration.
			slog.Warn("Runner completion not confirmed", "execution", v.id)
		}
		v.state = "retiring"
		e.notifyLocked()
		e.mu.Unlock()
		e.cleanup(v)
	}()
	return nil
}

// RetireReservation destroys an undelivered execution after failed provisioning.
// It never releases capacity before normal cleanup and cannot cancel spent jobs.
// This is a local adapter method, not a new worker API operation.
func (e *Engine) RetireReservation(identity Identity, assignmentID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.identityLocked(identity); err != nil {
		return err
	}
	record, v, err := e.findLocked(assignmentID)
	if err != nil {
		return err
	}
	if record.Request.Identity != identity {
		return ErrFenced
	}
	if record.State == Terminal {
		return nil
	}
	if record.State == DeliveryIntent {
		return ErrConsumed
	}
	if v == nil {
		return ErrTransition
	}
	if v.state != "retiring" {
		v.state = "retiring"
		go e.cleanup(v)
	}
	return nil
}

func (e *Engine) Status(identity Identity, assignmentID string) (Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.authenticateLocked(identity); err != nil {
		return Record{}, err
	}
	record, _, err := e.findLocked(assignmentID)
	return record, err
}

// Health reports admission state with fixed reason codes. It never exposes
// error text, paths or guest output. "refusing" means new boots are refused;
// ready warm VMs can still be reserved.
func (e *Engine) Health() (workerapi.Health, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	records := e.journal.Len()
	h := workerapi.Health{Admission: workerapi.AdmissionOpen, Reasons: []string{}, Journal: workerapi.JournalHealth{Records: records, MaxRecords: MaxRecords}}
	if available, err := e.diskAvailable(e.config); err == nil {
		h.Disk = &workerapi.DiskHealth{AvailableBytes: available, RequiredBytes: requiredDisk(e.config, e.allocatedDiskLocked())}
	}
	switch {
	case e.fatal != nil:
		h.Reasons = append(h.Reasons, workerapi.ReasonFatal)
	case e.draining:
		h.Reasons = append(h.Reasons, workerapi.ReasonDrainRequested)
	}
	// Decide from the live measurement so the reason clears as soon as space
	// returns, even for profiles nothing retries. Fall back to an active boot
	// deferral only when the filesystem cannot be measured.
	lowDisk := h.Disk != nil && h.Disk.AvailableBytes < h.Disk.RequiredBytes
	if h.Disk == nil {
		for id := range e.deferred {
			if time.Now().Before(e.nextBoot[id]) {
				lowDisk = true
			}
		}
	}
	if lowDisk {
		h.Reasons = append(h.Reasons, workerapi.ReasonDiskReserve)
	}
	if records >= MaxRecords {
		h.Reasons = append(h.Reasons, workerapi.ReasonJournalFull)
	}
	// A full journal never recovers without rotation (#29), so it is stopped,
	// matching inventory, which reports it as draining.
	if e.draining || records >= MaxRecords {
		h.Admission = workerapi.AdmissionStopped
	} else if len(h.Reasons) > 0 {
		h.Admission = workerapi.AdmissionRefusing
	}
	return h, nil
}
func (e *Engine) Inventory() (Inventory, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	records, err := e.journal.Records()
	if err != nil {
		return Inventory{}, err
	}
	// A full journal refuses all new work until rotation (#29). Report it as
	// draining so brokers and the standalone adapter place work elsewhere; the
	// engine itself is not latched and keeps serving existing assignments.
	inventory := Inventory{Identity: e.config.Identity, Draining: e.draining || len(records) >= MaxRecords, Budget: e.config.Budget, Records: records}
	for _, v := range e.entries {
		inventory.Used.VMs++
		inventory.Used.CPUs += v.profile.CPUs
		inventory.Used.MemoryMiB += v.profile.MemoryMiB
	}
	for _, p := range e.config.Profiles {
		item := ProfileInventory{ID: p.ID, Digest: p.Digest, Machine: p.Machine, CPUs: p.CPUs, MemoryMiB: p.MemoryMiB, DiskGiB: p.DiskGiB}
		for _, v := range e.entries {
			if compatible(v.profile, p) {
				if v.state == "ready" {
					item.Ready++
				}
				if v.state == "booting" {
					item.Booting++
				}
			}
		}
		inventory.Profiles = append(inventory.Profiles, item)
	}
	sort.Slice(inventory.Records, func(i, j int) bool {
		return inventory.Records[i].Request.AssignmentID < inventory.Records[j].Request.AssignmentID
	})
	return inventory, nil
}
func (e *Engine) Drain(identity Identity) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.authenticateLocked(identity); err != nil {
		return err
	}
	e.draining = true
	for _, v := range e.entries {
		if v.state == "booting" {
			v.state = "retiring"
			v.cancel()
		} else if v.state == "ready" {
			v.state = "retiring"
			go e.cleanup(v)
		}
	}
	e.notifyLocked()
	return nil
}

// Wait closes journal ownership only after a drain and confirmed cleanup. Its
// context bounds the caller's wait; cancellation never terminates running jobs.
func (e *Engine) Wait(ctx context.Context) error {
	for {
		e.mu.Lock()
		if e.fatal != nil {
			pendingCleanup := false
			for _, v := range e.entries {
				if !v.failedCleanup {
					pendingCleanup = true
					break
				}
			}
			if !pendingCleanup {
				err := e.fatal
				e.mu.Unlock()
				return err
			}
		}
		if e.draining && len(e.entries) == 0 {
			e.closed = true
			err := e.journal.Close()
			e.mu.Unlock()
			return err
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func accessEqual(a, b *landlockrunner.HostAccess) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
func (c Config) profileAccess(id string) *landlockrunner.HostAccess {
	for _, p := range c.Profiles {
		if p.ID == id {
			return p.HostAccess
		}
	}
	return nil
}
func (c Config) accessPlans() ([]*landlockrunner.AccessPlan, error) {
	var plans []*landlockrunner.AccessPlan
	for _, p := range c.Profiles {
		if p.HostAccess == nil {
			continue
		}
		for slot := 1; slot <= c.Budget.MaxVMs; slot++ {
			plan, err := landlockrunner.NormalizeAccess(p.HostAccess, slot, c.Budget.MaxVMs, c.protectedFor(p.ID, slot))
			if err != nil {
				return nil, fmt.Errorf("host access recovery plan unavailable: %w", err)
			}
			plans = append(plans, plan)
		}
	}
	return plans, nil
}

// CompletionError reports a local Run failure separately from proven retirement.
// It is process-local and is not a GitHub job outcome or durable billing signal.
func (e *Engine) CompletionError(identity Identity, assignment string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if identity != e.config.Identity {
		return ErrFenced
	}
	if !e.closed || !e.draining || len(e.entries) != 0 {
		return ErrTransition
	}
	failed, known := e.runResults[assignment]
	if !known {
		return ErrNotFound
	}
	if failed {
		return fmt.Errorf("runner completion not confirmed")
	}
	return nil
}
