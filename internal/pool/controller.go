//go:build linux && amd64

package pool

import (
	"context"
	"fmt"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/usage"
	"github.com/plover-digital/chickadee/workerapi"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Backend interface {
	JIT(context.Context, string) (string, error)
	Remove(context.Context, string) error
}
type Event struct {
	Resources *workerapi.ResourceSummary
	ID        string
	Kind      string
	Err       error
}
type entry struct {
	profile    string
	reservedAt time.Time
	state      VM
	slot       int
	assign     chan assignment
	retire     chan struct{}
}

// Assignment binds credentials to a scope only after a credential-free VM is reserved.
type assignment struct {
	Name    string
	Backend Backend
}

func compatibleGuest(a, b config.Config) bool {
	return a.ImageDir == b.ImageDir && a.Machine == b.Machine && a.CPUs == b.CPUs && a.MemoryMiB == b.MemoryMiB && a.DiskGiB == b.DiskGiB
}

type machine interface {
	Run(string, time.Duration, string) error
	Exited() <-chan struct{}
	Cleanup() error
	Stop() error
}
type starter func(context.Context, config.Config, int, string) (machine, error)

// Ownership holds the local lock before any GitHub session or API initialization.
type Ownership struct{ lock *os.File }

func (o *Ownership) Close() error { return o.lock.Close() }
func Acquire(c config.Config) (owner *Ownership, err error) {
	for _, d := range []string{c.StateDir, filepath.Join(c.StateDir, "vms"), filepath.Join(c.StateDir, "records"), filepath.Join(c.StateDir, "logs")} {
		if e := os.MkdirAll(d, 0700); e != nil {
			return nil, e
		}

		info, e := os.Lstat(d)
		if e != nil {
			return nil, e
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("runtime directories must be owned by the controller, private, and not symlinks")
		}
	}
	lock, e := os.OpenFile(filepath.Join(c.StateDir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			lock.Close()
		}
	}()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return nil, fmt.Errorf("another controller owns state_dir")
	}
	records, e := host.Records(c.StateDir)
	if e != nil {
		return nil, e
	}
	for _, r := range records {
		if e = recordScope(c, r); e != nil {
			return nil, e
		}
	}
	limits := c.HostLimits()
	if e = host.CheckCgroupBudget(c.Cgroup, limits.MemoryMiB, limits.Max); e != nil {
		return nil, e
	}
	if e = host.PrepareCgroup(c.Cgroup); e != nil {
		return nil, e
	}
	if e = host.ReapOwned(c.StateDir); e != nil {
		return nil, e
	}
	if e = host.ReapCgroups(c.Cgroup); e != nil {
		return nil, e
	}
	dirs, e := os.ReadDir(filepath.Join(c.StateDir, "vms"))
	if e != nil {
		return nil, e
	}
	for _, d := range dirs {
		if e = os.RemoveAll(filepath.Join(c.StateDir, "vms", d.Name())); e != nil {
			return nil, e
		}
	}
	return &Ownership{lock: lock}, nil
}

// Run is the sole owner of pool state; per-VM workers own QEMU and disk cleanup.
func Run(ctx context.Context, c config.Config, b Backend, desired <-chan int) error {
	return run(ctx, c, b, desired, func(ctx context.Context, c config.Config, slot int, id string) (machine, error) {
		return host.Start(ctx, c, slot, id)
	})
}

// RunOwned requires an Ownership acquired for c, retained until RunOwned returns.
func RunOwned(ctx context.Context, c config.Config, b Backend, desired <-chan int, owner *Ownership) error {
	if owner == nil {
		return fmt.Errorf("state ownership required")
	}
	return runOwned(ctx, c, b, desired, func(ctx context.Context, c config.Config, slot int, id string) (machine, error) {
		return host.Start(ctx, c, slot, id)
	})
}

// Cleanup reconciles intent without demand polling or new VM creation.
func Cleanup(ctx context.Context, c config.Config, b Backend, owner *Ownership) error {
	if owner == nil {
		return fmt.Errorf("state ownership required")
	}
	return reconcile(ctx, c, b)
}
func run(ctx context.Context, c config.Config, b Backend, desired <-chan int, start starter) error {
	owner, e := Acquire(c)
	if e != nil {
		return e
	}
	defer owner.Close()
	return runOwned(ctx, c, b, desired, start)
}

// Demand is authoritative assigned-job demand for one scale set.
type Demand struct {
	Profile  string
	Assigned int
}

func RunProfilesOwned(ctx context.Context, c config.Config, backends map[string]Backend, desired <-chan Demand, owner *Ownership) error {
	if owner == nil {
		return fmt.Errorf("state ownership required")
	}
	return runProfiles(ctx, c, backends, desired, func(ctx context.Context, c config.Config, slot int, id string) (machine, error) {
		return host.Start(ctx, c, slot, id)
	})
}

// RunProfilesDrainOwned retires unspent capacity and lets existing jobs finish
// when drain closes, then releases ownership to a controlled config update.
func RunProfilesDrainOwned(ctx context.Context, c config.Config, backends map[string]Backend, desired <-chan Demand, owner *Ownership, drain <-chan struct{}) error {
	return RunProfilesReloadDrainOwned(ctx, c, backends, desired, owner, drain, nil)
}

// RunProfilesReloadDrainOwned accepts validated live scope updates while keeping
// the same process ownership, physical VM pool and credentialed jobs.
func RunProfilesReloadDrainOwned(ctx context.Context, c config.Config, backends map[string]Backend, desired <-chan Demand, owner *Ownership, drain <-chan struct{}, updates <-chan ProfileUpdate) error {
	if owner == nil {
		return fmt.Errorf("state ownership required")
	}
	return runProfilesWithUpdates(ctx, c, backends, desired, func(ctx context.Context, c config.Config, slot int, id string) (machine, error) {
		return host.Start(ctx, c, slot, id)
	}, drain, updates)
}
func CleanupProfiles(ctx context.Context, c config.Config, backends map[string]Backend, owner *Ownership) error {
	if owner == nil {
		return fmt.Errorf("state ownership required")
	}
	return reconcileProfiles(ctx, c, backends)
}
func runOwned(ctx context.Context, c config.Config, b Backend, desired <-chan int, start starter) error {
	// Compatibility adapter for the original public one-profile API.
	adapterCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	demands := make(chan Demand, 16)
	go func() {
		defer close(demands)
		for {
			select {
			case <-adapterCtx.Done():
				return
			case n, ok := <-desired:
				if !ok {
					return
				}
				select {
				case demands <- Demand{c.Key(), n}:
				case <-adapterCtx.Done():
					return
				}
			}
		}
	}()
	return runProfiles(ctx, c, map[string]Backend{c.Key(): b}, demands, start)
}
func runProfiles(ctx context.Context, c config.Config, backends map[string]Backend, desired <-chan Demand, start starter) error {
	return runProfilesWithDrain(ctx, c, backends, desired, start, nil)
}
func runProfilesWithDrain(ctx context.Context, c config.Config, backends map[string]Backend, desired <-chan Demand, start starter, drain <-chan struct{}) error {
	return runProfilesWithUpdates(ctx, c, backends, desired, start, drain, nil)
}

func runProfilesWithUpdates(ctx context.Context, c config.Config, backends map[string]Backend, desired <-chan Demand, start starter, drain <-chan struct{}, updates <-chan ProfileUpdate) error {
	// Registries belong exclusively to this actor, never to the poll supervisor.
	ownedBackends := make(map[string]Backend, len(backends))
	for name, backend := range backends {
		ownedBackends[name] = backend
	}
	backends = ownedBackends
	if e := reconcileProfiles(ctx, c, backends); e != nil {
		return e
	}
	configs := map[string]config.Config{}
	names := []string{}
	for _, p := range c.ProfileConfigs() {
		if backends[p.Key()] == nil {
			return fmt.Errorf("profile backend missing")
		}
		configs[p.Key()] = p
		names = append(names, p.Key())
	}
	limits := c.HostLimits()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan Event, 2*limits.Max)
	entries := map[string]*entry{}
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	requested := map[string]int{}
	nextBoot := map[string]time.Time{}
	nextReconcile := time.Time{}
	// FIFO admission blocks smaller new boots behind an older request that cannot
	// fit, so sustained small-job traffic cannot starve a medium/large profile.
	waiting := []string{}
	draining := false
	active := map[string]bool{}
	for _, name := range names {
		active[name] = true
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-drain:
			drain = nil
			draining = true
			slog.Info("Pool draining; no new credentials or boots")
		case update, ok := <-updates:
			if !ok {
				updates = nil
				continue
			}
			select {
			case <-drain:
				drain = nil
				draining = true
			default:
			}
			err := ValidateReload(c, update.Config)
			if draining {
				err = fmt.Errorf("pool is draining")
			}
			var profiles []config.Config
			if err == nil {
				profiles = update.Config.ProfileConfigs()
				for _, p := range profiles {
					if update.Backends[p.Key()] == nil && backends[p.Key()] == nil {
						err = fmt.Errorf("profile backend missing")
						break
					}
					for _, previous := range configs {
						if config.ScopeKey(previous.GitHubURL, "") == config.ScopeKey(p.GitHubURL, "") && !sameScopeIdentity(previous, p) {
							err = fmt.Errorf("historical scope identity is still in use")
						}
						if previous.Key() == p.Key() && !compatibleGuest(previous, p) {
							err = fmt.Errorf("historical queue profile is still in use")
						}
					}
				}
			}
			if err == nil {
				newNames := []string{}
				newActive := map[string]bool{}
				newConfigs := make(map[string]config.Config, len(configs)+len(profiles))
				for name, p := range configs {
					newConfigs[name] = p
				}
				for _, p := range profiles {
					newNames = append(newNames, p.Key())
					newActive[p.Key()] = true
					newConfigs[p.Key()] = p
				}
				// Publish the status successfully before touching live entries/maps.
				err = writeStatus(update.Config, newConfigs, newNames, entries, requested, false)
				if err == nil {
					names, active, configs = newNames, newActive, newConfigs
					for _, p := range profiles {
						if update.Backends[p.Key()] != nil {
							backends[p.Key()] = update.Backends[p.Key()]
						}
					}
					for _, v := range entries {
						if !active[v.profile] && (v.state.State == Ready || v.state.State == Booting) {
							v.state.State = Dead
							close(v.retire)
						}
					}
					kept := waiting[:0]
					for _, name := range waiting {
						if active[name] {
							kept = append(kept, name)
						}
					}
					waiting = kept
					for name := range requested {
						if !active[name] {
							delete(requested, name)
						}
					}
					c = update.Config
				}
			}
			if update.Applied != nil {
				update.Applied <- err
			}
			if err != nil {
				slog.Warn("Pool update rejected")
			}
			continue
		case d, ok := <-desired:
			if !ok {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("demand stream closed")
			}
			p, ok := configs[d.Profile]
			if !ok || !active[d.Profile] {
				// A canceled poller may already have a queued final demand value.
				continue
			}
			n := min(p.Max, max(0, d.Assigned))
			if n != requested[d.Profile] {
				slog.Info("Runner demand changed", "profile", d.Profile, "requested", n)
			}
			requested[d.Profile] = n
		case ev := <-events:
			v := entries[ev.ID]
			if v == nil {
				return fmt.Errorf("unknown VM event")
			}
			switch ev.Kind {
			case "ready":
				if v.state.State != Dead {
					v.state.State = Ready
					slog.Info("VM ready", "vm", ev.ID, "profile", v.profile)
				}
			case "done":
				if !v.reservedAt.IsZero() {
					p := configs[v.profile]
					if err := usage.Append(c.StateDir, usage.Record{ID: v.state.ID, Scope: p.GitHubURL, Label: p.ScaleSet, Reserved: v.reservedAt, Completed: time.Now().UTC(), Resources: ev.Resources, CPUs: p.CPUs, MemoryMiB: p.MemoryMiB}); err != nil {
						slog.Warn("usage recording failed")
					}
				}
				v.state.Destroy()
				delete(entries, ev.ID)
				// Registration removal is retried by the bounded periodic reconciliation.
				nextReconcile = time.Time{}
				if ev.Err != nil {
					nextBoot[v.profile] = time.Now().Add(2 * time.Second)
					slog.Warn("VM retired", "vm", ev.ID, "profile", v.profile, "reason", ev.Err.Error())
				} else {
					slog.Info("VM destroyed; overlay removed", "vm", ev.ID, "profile", v.profile)
				}
			case "fatal":
				return ev.Err
			default:
				return fmt.Errorf("unknown VM event kind")
			}
		case <-tick.C:
			activeIDs := map[string]bool{}
			for id := range entries {
				activeIDs[id] = true
			}
			if e := host.PruneLogs(c.StateDir, activeIDs); e != nil {
				return e
			}
			if !time.Now().Before(nextReconcile) {
				nextReconcile = time.Now().Add(30 * time.Second)
				cleanupCtx, cc := context.WithTimeout(ctx, 15*time.Second)
				e := reconcileProfiles(cleanupCtx, c, backends)
				cc()
				if e != nil {
					slog.Warn("registration cleanup pending")
				}
			}
			// Keep historical identities only while a worker or durable intent
			// can still need them. Never prune when journal inspection fails.
			if records, err := host.Records(c.StateDir); err == nil {
				needed := map[string]bool{}
				for _, v := range entries {
					needed[v.profile] = true
				}
				valid := true
				for _, record := range records {
					name, err := RecordProfile(c, record)
					if err != nil {
						valid = false
						break
					}
					needed[name] = true
				}
				if valid {
					for name := range backends {
						if !active[name] && !needed[name] {
							delete(configs, name)
							delete(backends, name)
							delete(nextBoot, name)
						}
					}
				}
			}
		}
		select {
		case <-drain:
			drain = nil
			draining = true
		default:
		}
		if draining {
			for _, v := range entries {
				if v.state.State == Ready || v.state.State == Booting {
					v.state.State = Dead
					close(v.retire)
				}
			}
			if e := writeStatus(c, configs, names, entries, requested, true); e != nil {
				return e
			}
			if len(entries) == 0 {
				return nil
			}
			continue
		}
		counts := func(name string) (total, active, idle int) {
			for _, v := range entries {
				if v.profile != name {
					continue
				}
				total++
				switch v.state.State {
				case Spent, Reserved:
					active++
				case Ready, Booting:
					idle++
				}
			}
			return
		}
		scopeCounts := func(p config.Config) (total, spent int) {
			for _, v := range entries {
				if config.ScopeKey(configs[v.profile].GitHubURL, "") == config.ScopeKey(p.GitHubURL, "") {
					total++
					if v.state.State == Spent {
						spent++
					}
				}
			}
			return
		}
		// Older unmet demand gets first choice of shared credential-free capacity.
		reservationOrder := append([]string(nil), waiting...)
		seenReservation := map[string]bool{}
		for _, name := range reservationOrder {
			seenReservation[name] = true
		}
		for _, name := range names {
			if !seenReservation[name] {
				reservationOrder = append(reservationOrder, name)
			}
		}
		for _, name := range reservationOrder {
			p := configs[name]
			_, active, _ := counts(name)
			for _, v := range entries {
				_, scopeSpent := scopeCounts(p)
				if active >= requested[name] || active >= p.Max || scopeSpent >= p.ScopeLimit() {
					break
				}
				if v.state.State != Ready || !compatibleGuest(configs[v.profile], p) {
					continue
				}
				// Rebinding is allowed only while no credentials have ever been issued.
				v.profile = name
				if e := v.state.Reserve(); e != nil {
					return e
				}
				if e := v.state.Spend(); e != nil {
					return e
				}
				runnerName := p.ScaleSet + "-" + v.state.ID
				if e := host.Save(c.StateDir, host.Record{ID: v.state.ID, Name: runnerName, GitHubURL: p.GitHubURL, RunnerGroupID: p.RunnerGroupID, InstallationID: p.InstallationID, NotBefore: time.Now().Add(10 * time.Minute)}); e != nil {
					return e
				}
				v.reservedAt = time.Now().UTC()
				v.assign <- assignment{Name: runnerName, Backend: backends[name]}
				active++
				slog.Info("VM reserved for one job", "vm", v.state.ID, "profile", p.ScaleSet)
			}
		}
		// Keep waiting order stable across new messages and tick events.
		needs := func(name string) bool {
			total, active, idle := counts(name)
			_, scopeSpent := scopeCounts(configs[name])
			return scopeSpent < configs[name].ScopeLimit() && total < configs[name].Max && active+idle < requested[name]
		}
		kept := waiting[:0]
		queued := map[string]bool{}
		for _, name := range waiting {
			if needs(name) {
				kept = append(kept, name)
				queued[name] = true
			}
		}
		waiting = kept
		for _, name := range names {
			if needs(name) && !queued[name] {
				waiting = append(waiting, name)
			}
		}
		fits := func(p config.Config) bool {
			scopeTotal, _ := scopeCounts(p)
			if scopeTotal >= p.ScopeLimit() {
				return false
			}
			cpu, ram := p.CPUs, p.MemoryMiB
			for _, v := range entries {
				r := configs[v.profile]
				cpu += r.CPUs
				ram += r.MemoryMiB
			}
			return len(entries) < limits.Max && cpu <= limits.CPUs && ram <= limits.MemoryMiB
		}
		retire := func(v *entry) { v.state.State = Dead; close(v.retire) }
		boot := func(name string) {
			used := map[int]bool{}
			for _, v := range entries {
				used[v.slot] = true
			}
			slot := 1
			for used[slot] {
				slot++
			}
			id := host.NewID()
			for entries[id] != nil {
				id = host.NewID()
			}
			v := &entry{profile: name, state: VM{ID: id, State: Booting}, slot: slot, assign: make(chan assignment, 1), retire: make(chan struct{})}
			entries[id] = v
			wg.Add(1)
			bootConfig := configs[name]
			go func() { defer wg.Done(); worker(runCtx, bootConfig, v, events, start) }()
		}
		// Shrink canceled/excess warm capacity, retaining resources until done.
		for _, name := range names {
			total, active, _ := counts(name)
			surplus := total - Target(configs[name].Warm, configs[name].Max, active, requested[name])
			for _, v := range entries {
				if v.profile == name && v.state.State == Dead {
					surplus--
				}
			}
			for _, v := range entries {
				if surplus <= 0 {
					break
				}
				if v.profile == name && (v.state.State == Ready || v.state.State == Booting) {
					retire(v)
					surplus--
				}
			}
		}
		for len(waiting) > 0 {
			name := waiting[0]
			p := configs[name]
			if time.Now().Before(nextBoot[name]) {
				break
			}
			if !fits(p) {
				// Reclaim optional uncredentialed warm guests; never evict job guests.
				for _, other := range names {
					if other == name {
						continue
					}
					_, a, idle := counts(other)
					spare := idle - max(0, requested[other]-a)
					for _, v := range entries {
						if spare <= 0 {
							break
						}
						if v.profile == other && (v.state.State == Ready || v.state.State == Booting) {
							// A compatible in-flight boot will satisfy this demand once READY.
							if v.state.State == Booting && compatibleGuest(configs[other], p) {
								continue
							}
							retire(v)
							spare--
						}
					}
				}
				break
			}
			boot(name)
			waiting = waiting[1:]
			if needs(name) {
				waiting = append(waiting, name)
			}
		}
		// Do not fill optional warms while an older job request needs capacity.
		if len(waiting) == 0 {
			for _, name := range names {
				p := configs[name]
				total, active, _ := counts(name)
				target := Target(p.Warm, p.Max, active, requested[name])
				for total < target && fits(p) && !time.Now().Before(nextBoot[name]) {
					boot(name)
					total++
				}
			}
		}
		if e := writeStatus(c, configs, names, entries, requested, false); e != nil {
			return e
		}
	}
}
func worker(ctx context.Context, c config.Config, v *entry, events chan<- Event, start starter) {
	bootStarted := time.Now()
	vm, e := start(ctx, c, v.slot, v.state.ID)
	report := func(kind string, e error) {
		var resources *workerapi.ResourceSummary
		if kind == "done" {
			if measured, ok := vm.(interface {
				ResourceSummary() *workerapi.ResourceSummary
			}); ok {
				resources = measured.ResourceSummary()
			}
		}
		select {
		case events <- Event{ID: v.state.ID, Kind: kind, Err: e, Resources: resources}:
		case <-ctx.Done():
		}
	}
	if e == nil {
		slog.Info("VM boot completed", "vm", v.state.ID, "duration_ms", time.Since(bootStarted).Milliseconds())
		report("ready", nil)
		select {
		case <-ctx.Done():
			e = ctx.Err()
		case <-v.retire:
			e = nil
		case <-vm.Exited():
			e = fmt.Errorf("warm VM exited")
		case assigned := <-v.assign:
			// Credential intent was committed by the actor before this call.
			jitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			var jit string
			jitStarted := time.Now()
			jit, e = assigned.Backend.JIT(jitCtx, assigned.Name)
			cancel()
			if e == nil {
				slog.Info("JIT configuration generated", "vm", v.state.ID, "duration_ms", time.Since(jitStarted).Milliseconds())
				stop := context.AfterFunc(ctx, func() { _ = vm.Stop() })
				e = vm.Run(jit, time.Duration(c.JobSeconds)*time.Second, filepath.Join(c.StateDir, "logs", v.state.ID+".jsonl"))
				stop()
			}
		}
	}
	if vm != nil {
		if ce := vm.Cleanup(); ce != nil {
			report("fatal", ce)
			return
		}
	}
	report("done", e)
}
func reconcile(ctx context.Context, c config.Config, b Backend) error {
	return reconcileProfiles(ctx, c, map[string]Backend{c.Key(): b})
}

// RecordProfile derives the immutable scale-set identity from a private intent.
// Old records need no migration and removed profiles remain recoverable.
// RecoveryConfig resolves historical scope identity before contacting GitHub.
func RecoveryConfig(c config.Config, r host.Record) (config.Config, error) {
	suffix := "-" + r.ID
	name := strings.TrimSuffix(r.Name, suffix)
	if name == r.Name || !config.Name.MatchString(name) {
		return config.Config{}, fmt.Errorf("invalid journal profile")
	}
	if len(c.Scopes) == 0 {
		if r.GitHubURL != c.GitHubURL || r.RunnerGroupID != c.RunnerGroupID || (r.InstallationID != 0 && r.InstallationID != c.InstallationID) {
			return config.Config{}, fmt.Errorf("journal scope changed; restore original GitHub URL, installation and runner group")
		}
		if len(c.Profiles) == 0 && name != c.ScaleSet {
			return config.Config{}, fmt.Errorf("journal scope changed; restore original scale-set configuration")
		}
		recovery := c.ProfileConfigs()[0]
		recovery.ScaleSet = name
		return recovery, nil
	}
	var match *config.Config
	for _, p := range c.ProfileConfigs() {
		if config.ScopeKey(p.GitHubURL, "") == config.ScopeKey(r.GitHubURL, "") {
			copy := p
			match = &copy
			break
		}
	}
	if match == nil {
		for _, scope := range c.Scopes {
			if config.ScopeKey(scope.GitHubURL, "") == config.ScopeKey(r.GitHubURL, "") {
				copy := c.ProfileConfigs()[0]
				copy.GitHubURL = scope.GitHubURL
				copy.InstallationID = scope.InstallationID
				copy.RunnerGroupID = scope.RunnerGroupID
				match = &copy
				break
			}
		}
	}
	if match != nil {
		if match.RunnerGroupID != r.RunnerGroupID || (r.InstallationID != 0 && match.InstallationID != r.InstallationID) {
			return config.Config{}, fmt.Errorf("journal installation/group changed; restore original scope")
		}
		match.ScaleSet = name
		return *match, nil
	}
	// A removed scope is recoverable only with complete durable authentication
	// identity. Old journals require restoring their scope config rather than guessing.
	if r.InstallationID <= 0 {
		return config.Config{}, fmt.Errorf("restore historical scope config to recover legacy intent")
	}
	recovery := c.ProfileConfigs()[0]
	recovery.ScaleSet = name
	recovery.GitHubURL = r.GitHubURL
	recovery.RunnerGroupID = r.RunnerGroupID
	recovery.InstallationID = r.InstallationID
	if e := recovery.Validate(); e != nil {
		return config.Config{}, fmt.Errorf("invalid historical scope")
	}
	return recovery, nil
}
func RecordProfile(c config.Config, r host.Record) (string, error) {
	recovery, e := RecoveryConfig(c, r)
	if e != nil {
		return "", e
	}
	return recovery.Key(), nil
}
func recordScope(c config.Config, r host.Record) error { _, e := RecordProfile(c, r); return e }
func reconcileProfiles(ctx context.Context, c config.Config, backends map[string]Backend) error {
	records, e := host.Records(c.StateDir)
	if e != nil {
		return e
	}
	for _, r := range records {
		name, e := RecordProfile(c, r)
		if e != nil {
			return e
		}
		b := backends[name]
		if b == nil {
			return fmt.Errorf("journal profile backend missing")
		}
		if _, e = os.Stat(filepath.Join(c.StateDir, "vms", r.ID)); e == nil {
			continue
		} else if !os.IsNotExist(e) {
			return e
		}
		if e = b.Remove(ctx, r.Name); e != nil {
			return e
		}
		if time.Now().Before(r.NotBefore) {
			continue
		}
		if e = host.Forget(c.StateDir, r.ID); e != nil {
			return e
		}
	}
	return nil
}
