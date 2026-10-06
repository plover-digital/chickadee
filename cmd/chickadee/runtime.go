//go:build linux && amd64

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/github"
	"github.com/plover-digital/chickadee/internal/pool"
)

type pollResult struct {
	key        string
	generation uint64
	pool       bool
	err        error
}
type runningPoll struct {
	cancel     context.CancelFunc
	generation uint64
	config     config.Config
	finished   <-chan struct{}
}

// Queue registrations may change without restarting the physical VM fleet.
// The pool remains the sole owner of scheduling, credentials and resource budgets.
func serve(parent context.Context, c config.Config, path string, backends map[string]pool.Backend, clients map[string]*github.Client, owner *pool.Ownership) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	demand := make(chan pool.Demand, 128)
	updates := make(chan pool.ProfileUpdate)
	results := make(chan pollResult, 128)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGUSR1, syscall.SIGHUP)
	defer signal.Stop(signals)
	drain := make(chan struct{})
	polls := map[string]runningPoll{}
	var generation uint64
	var wg sync.WaitGroup
	report := func(r pollResult) {
		select {
		case results <- r:
		case <-ctx.Done():
		}
	}
	startPoll := func(p config.Config, client *github.Client) {
		generation++
		g := generation
		pollCtx, stop := context.WithCancel(ctx)
		finished := make(chan struct{})
		polls[p.Key()] = runningPoll{stop, g, p, finished}
		wg.Add(1)
		go func() {
			defer wg.Done()
			values := make(chan int, 16)
			done := make(chan error, 1)
			go func() {
				err := client.Poll(pollCtx, p.ScaleSet, p.Max, values)
				close(finished) // Session.Close has finished before a replacement starts.
				done <- err
			}()
			for {
				select {
				case n := <-values:
					select {
					case demand <- pool.Demand{Profile: p.Key(), Assigned: n}:
					case <-pollCtx.Done():
						report(pollResult{key: p.Key(), generation: g, err: <-done})
						return
					}
				case e := <-done:
					report(pollResult{key: p.Key(), generation: g, err: e})
					return
				case <-pollCtx.Done():
					report(pollResult{key: p.Key(), generation: g, err: <-done})
					return
				}
			}
		}()
	}
	for _, p := range c.ProfileConfigs() {
		startPoll(p, clients[p.Key()])
	}
	poolStopped := make(chan struct{})
	var poolErr error
	wg.Add(1)
	go func(initial config.Config, initialBackends map[string]pool.Backend) {
		defer wg.Done()
		poolErr = pool.RunProfilesReloadDrainOwned(ctx, initial, initialBackends, demand, owner, drain, updates)
		close(poolStopped)
		report(pollResult{pool: true, err: poolErr})
	}(c, backends)
	defer func() { cancel(); wg.Wait() }()
	draining := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sig := <-signals:
			if sig == syscall.SIGUSR1 {
				if !draining {
					draining = true
					close(drain)
					for _, p := range polls {
						p.cancel()
					}
					slog.Info("Draining for controlled host configuration update")
				}
				continue
			}
			if draining {
				continue
			}
			next, digest, e := loadQueueConfig(path, c)
			nextBackends := make(map[string]pool.Backend, len(backends))
			for k, b := range backends {
				nextBackends[k] = b
			}
			nextClients := make(map[string]*github.Client, len(clients))
			for k, b := range clients {
				nextClients[k] = b
			}
			if e == nil {
				initCtx, stop := context.WithTimeout(ctx, 120*time.Second)
				for _, p := range next.ProfileConfigs() {
					if _, exists := polls[p.Key()]; !exists {
						b, err := github.New(initCtx, p)
						if err != nil {
							e = err
							break
						}
						nextBackends[p.Key()] = b
						nextClients[p.Key()] = b
					}
				}
				stop()
			}
			if e == nil {
				applied := make(chan error, 1)
				select {
				case updates <- pool.ProfileUpdate{Config: next, Backends: nextBackends, Applied: applied}:
				case <-poolStopped:
					return poolErr
				case <-ctx.Done():
					return ctx.Err()
				}
				select {
				case e = <-applied:
				case <-poolStopped:
					return poolErr
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if e == nil {
				wanted := map[string]config.Config{}
				for _, p := range next.ProfileConfigs() {
					wanted[p.Key()] = p
				}
				var stopping []runningPoll
				for key, p := range polls {
					n, ok := wanted[key]
					if !ok || n.Max != p.config.Max {
						p.cancel()
						stopping = append(stopping, p)
						delete(polls, key)
					}
				}
				for _, p := range stopping {
					select {
					case <-p.finished:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				for key, p := range wanted {
					if _, ok := polls[key]; !ok {
						startPoll(p, nextClients[key])
					}
				}
				c = next
				backends = nextBackends
				clients = nextClients
				slog.Info("Queue configuration applied without restarting VMs", "queues", len(wanted))
			} else {
				slog.Warn("Queue configuration reload rejected", "reason", e.Error())
			}
			if err := writeReloadResult(c.StateDir, digest, e); err != nil {
				return err
			}
		case r := <-results:
			if !r.pool {
				p, active := polls[r.key]
				if draining || !active || p.generation != r.generation {
					continue
				}
				if r.err == nil {
					return fmt.Errorf("active queue demand poller stopped unexpectedly")
				}
			}
			return r.err
		}
	}
}

func loadQueueConfig(path string, current config.Config) (config.Config, string, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return config.Config{}, "", e
	}
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	next, e := config.Load(path)
	if e != nil {
		return next, digest, e
	}
	again, e := os.ReadFile(path)
	if e != nil {
		return next, digest, e
	}
	if sha256.Sum256(again) != hash {
		return next, digest, fmt.Errorf("configuration changed during reload")
	}
	return next, digest, pool.ValidateReload(current, next)
}

func writeReloadResult(dir, digest string, reloadErr error) error {
	status := "applied"
	reason := ""
	if reloadErr != nil {
		status = "rejected"
		reason = reloadErr.Error()
	}
	b, e := json.Marshal(struct {
		Digest  string    `json:"config_sha256"`
		Status  string    `json:"status"`
		Reason  string    `json:"reason,omitempty"`
		Updated time.Time `json:"updated_at"`
	}{digest, status, reason, time.Now().UTC()})
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".reload-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), filepath.Join(dir, "reload.json"))
}
