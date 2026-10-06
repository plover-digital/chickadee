//go:build linux && amd64

package pool

import (
	"context"
	"fmt"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Backend interface {
	JIT(context.Context, string) (string, error)
	Remove(context.Context, string) error
}
type Event struct {
	ID   string
	Kind string
	Err  error
}
type entry struct {
	state  VM
	slot   int
	assign chan string
	retire chan struct{}
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
	if e = host.ReapOwned(c.StateDir); e != nil {
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
func runOwned(ctx context.Context, c config.Config, b Backend, desired <-chan int, start starter) error {
	var e error
	// Fail closed on reconciliation failure rather than create further registrations.
	if e = reconcile(ctx, c, b); e != nil {
		return e
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan Event, 2*c.Max)
	entries := map[string]*entry{}
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	requested := 0
	nextBoot := time.Time{}
	nextReconcile := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case n, ok := <-desired:
			if !ok {
				return fmt.Errorf("demand stream closed")
			}
			requested = min(c.Max, max(0, n))
		case ev := <-events:
			v := entries[ev.ID]
			if v == nil {
				return fmt.Errorf("unknown VM event")
			}
			switch ev.Kind {
			case "ready":
				v.state.State = Ready
				slog.Info("VM ready", "vm", ev.ID)
			case "done":
				v.state.Destroy()
				delete(entries, ev.ID)
				cleanupCtx, cc := context.WithTimeout(ctx, 15*time.Second)
				cleanupErr := reconcile(cleanupCtx, c, b)
				cc()
				nextReconcile = time.Now().Add(30 * time.Second)
				if cleanupErr != nil {
					slog.Warn("registration cleanup pending")
				}
				if ev.Err != nil {
					nextBoot = time.Now().Add(2 * time.Second)
					slog.Warn("VM retired", "vm", ev.ID, "reason", ev.Err.Error())
				} else {
					slog.Info("VM destroyed; overlay removed", "vm", ev.ID)
				}
			case "fatal":
				return ev.Err
			}
		case <-tick.C:
			activeIDs := map[string]bool{}
			for id := range entries {
				activeIDs[id] = true
			}
			if e = host.PruneLogs(c.StateDir, activeIDs); e != nil {
				return e
			}
			if time.Now().Before(nextReconcile) {
				break
			}
			nextReconcile = time.Now().Add(30 * time.Second)
			cleanupCtx, cc := context.WithTimeout(ctx, 15*time.Second)
			e = reconcile(cleanupCtx, c, b)
			cc()
			if e != nil {
				slog.Warn("registration cleanup pending")
			}
		}
		active := 0
		for _, v := range entries {
			if v.state.State == Spent || v.state.State == Reserved {
				active++
			}
		}
		for _, v := range entries {
			if active >= requested {
				break
			}
			if v.state.State != Ready {
				continue
			}
			if e = v.state.Reserve(); e != nil {
				return e
			}
			if e = v.state.Spend(); e != nil {
				return e
			}
			name := c.ScaleSet + "-" + v.state.ID
			if e = host.Save(c.StateDir, host.Record{ID: v.state.ID, Name: name, NotBefore: time.Now().Add(10 * time.Minute)}); e != nil {
				return e
			}
			v.assign <- name
			active++
			slog.Info("VM reserved for one job", "vm", v.state.ID)
		}
		target := Target(c.Warm, c.Max, active, requested)
		surplus := len(entries) - target
		for _, v := range entries {
			if surplus <= 0 {
				break
			}
			if v.state.State == Ready {
				v.state.State = Dead
				close(v.retire)
				surplus--
			}
		}
		for len(entries) < target && !time.Now().Before(nextBoot) {
			used := map[int]bool{}
			for _, v := range entries {
				used[v.slot] = true
			}
			slot := 1
			for used[slot] {
				slot++
			}
			id := host.NewID()
			v := &entry{state: VM{ID: id, State: Booting}, slot: slot, assign: make(chan string, 1), retire: make(chan struct{})}
			entries[id] = v
			wg.Add(1)
			go func(v *entry) { defer wg.Done(); worker(runCtx, c, b, v, events, start) }(v)
		}
	}
}
func worker(ctx context.Context, c config.Config, b Backend, v *entry, events chan<- Event, start starter) {
	vm, e := start(ctx, c, v.slot, v.state.ID)
	report := func(kind string, e error) {
		select {
		case events <- Event{ID: v.state.ID, Kind: kind, Err: e}:
		case <-ctx.Done():
		}
	}
	if e == nil {
		report("ready", nil)
		select {
		case <-ctx.Done():
			e = ctx.Err()
		case <-v.retire:
			e = nil
		case <-vm.Exited():
			e = fmt.Errorf("warm VM exited")
		case name := <-v.assign:
			// Credential intent was committed by the actor before this call.
			jitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			var jit string
			jit, e = b.JIT(jitCtx, name)
			cancel()
			if e == nil {
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
	records, e := host.Records(c.StateDir)
	if e != nil {
		return e
	}
	for _, r := range records {
		// An existing VM directory means that registration may still be active.
		if _, e = os.Stat(filepath.Join(c.StateDir, "vms", r.ID)); e == nil {
			continue
		} else if !os.IsNotExist(e) {
			return e
		}
		if r.Name != c.ScaleSet+"-"+r.ID {
			return fmt.Errorf("journal scale set mismatch; restore original configuration")
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
