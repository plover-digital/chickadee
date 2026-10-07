//go:build linux && amd64

package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	hostconfig "github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/protocol"
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
	waiters    int
	nextBoot   map[string]time.Time
}

func startHost(ctx context.Context, conf hostconfig.Config, slot int, id string) (machine, error) {
	vm, err := host.Start(ctx, conf, slot, id)
	if vm == nil {
		return nil, err
	}
	return vm, err
}
func OpenEngine(c Config) (*Engine, error) {
	return openEngine(c, startHost, host.ReapOwned, verifyImages, checkDiskSpace)
}
func openEngine(c Config, start startMachine, reap func(string) error, check func(Config) error, spaceCheck func(Config, int64) error) (*Engine, error) {
	c.Profiles = append([]Profile(nil), c.Profiles...)
	if err := c.Validate(); err != nil {
		return nil, err
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
			if _, err = j.RecoverTerminal(record.Request.AssignmentID, ExitProof{VMID: record.Request.VMID, QEMUExitConfirmed: true, DiskRemoved: true}); err != nil {
				return fail(err)
			}
		}
	}
	if err = spaceCheck(c, 0); err != nil {
		return fail(err)
	}
	e := &Engine{config: c, journal: j, start: start, spaceCheck: spaceCheck, profiles: map[string]Profile{}, entries: map[string]*instance{}, changed: make(chan struct{}), nextBoot: map[string]time.Time{}}
	for _, profile := range c.Profiles {
		e.profiles[profile.ID] = profile
	}
	e.mu.Lock()
	e.maintainLocked()
	e.mu.Unlock()
	return e, nil
}
func (e *Engine) notifyLocked() { close(e.changed); e.changed = make(chan struct{}) }
func compatible(a, b Profile) bool {
	return a.Digest == b.Digest && a.Machine == b.Machine && a.CPUs == b.CPUs && a.MemoryMiB == b.MemoryMiB && a.DiskGiB == b.DiskGiB
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
	for _, p := range e.config.Profiles {
		free := 0
		for _, v := range e.entries {
			if compatible(v.profile, p) && (v.state == "ready" || v.state == "booting") {
				free++
			}
		}
		for free < p.Warm && e.fitsLocked(p) && !time.Now().Before(e.nextBoot[p.ID]) {
			e.bootLocked(p)
			free++
		}
	}
}
func (e *Engine) bootLocked(p Profile) {
	if e.fatal != nil || e.draining {
		return
	}
	allocated := int64(0)
	for _, v := range e.entries {
		info, err := os.Stat(filepath.Join(e.config.StateDir, "vms", v.id, "disk.qcow2"))
		if err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				allocated += stat.Blocks * 512
			}
		}
	}
	if err := e.spaceCheck(e.config, allocated); err != nil {
		e.failLocked(err)
		return
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
	v := &instance{id: host.NewID(), slot: slot, profile: p, state: "booting", cancel: cancel}
	e.entries[v.id] = v
	e.notifyLocked()
	go e.boot(ctx, v)
}
func (e *Engine) boot(ctx context.Context, v *instance) {
	c := hostconfig.Config{StateDir: e.config.StateDir, ImageDir: v.profile.ImageDir, Machine: v.profile.Machine, CPUs: v.profile.CPUs, MemoryMiB: v.profile.MemoryMiB, DiskGiB: v.profile.DiskGiB, BootSeconds: e.config.BootTimeoutSeconds, JobSeconds: e.config.JobTimeoutSeconds}
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
		if _, err := e.journal.RecoverTerminal(v.request.AssignmentID, ExitProof{VMID: v.id, QEMUExitConfirmed: true, DiskRemoved: true}); err != nil {
			v.failedCleanup = true
			e.failLocked(err)
			return
		}
	}
	delete(e.entries, v.id)
	e.notifyLocked()
	e.maintainLocked()
}
func (e *Engine) failLocked(err error) {
	if e.fatal == nil {
		e.fatal = err
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
		if record, _, err := e.findLocked(assignmentID); err == nil {
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
				request := Request{Identity: identity, AssignmentID: assignmentID, VMID: v.id, ProfileDigest: p.Digest, CPUs: p.CPUs, MemoryMiB: p.MemoryMiB, DiskGiB: p.DiskGiB}
				record, err := e.journal.Reserve(request)
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
		if !booting && e.fitsLocked(p) && !time.Now().Before(e.nextBoot[p.ID]) {
			e.bootLocked(p)
		}
		if !booting && !e.fitsLocked(p) {
			for _, idle := range e.entries {
				if compatible(idle.profile, p) {
					continue
				}
				if idle.state == "ready" {
					idle.state = "retiring"
					go e.cleanup(idle)
					break
				}
				if idle.state == "booting" {
					idle.state = "retiring"
					idle.cancel()
					break
				}
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
		_ = v.vm.Run(jit, time.Duration(e.config.JobTimeoutSeconds)*time.Second, filepath.Join(e.config.StateDir, "logs", v.id+".jsonl"))
		e.mu.Lock()
		v.state = "retiring"
		e.notifyLocked()
		e.mu.Unlock()
		e.cleanup(v)
	}()
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
func (e *Engine) Inventory() (Inventory, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	records, err := e.journal.Records()
	if err != nil {
		return Inventory{}, err
	}
	inventory := Inventory{Identity: e.config.Identity, Draining: e.draining, Budget: e.config.Budget, Records: records}
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
