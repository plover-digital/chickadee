//go:build linux && amd64

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/worker"
	"github.com/plover-digital/chickadee/workerapi"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type configuration struct {
	worker.Config
	Listen string             `json:"listen"`
	TLS    workerapi.TLSFiles `json:"tls"`
}

func main() {
	path := flag.String("config", "/etc/chickadee-worker/config.json", "private worker configuration")
	check := flag.Bool("check", false, "validate local worker identity, resources, TLS and isolation without starting VMs or a listener")
	probe := flag.String("sandbox-probe", "", "internal isolation preflight child")
	flag.Parse()
	if *probe != "" {
		if host.SandboxProbe(*probe) != nil {
			os.Exit(1)
		}
		return
	}
	if err := run(*path, *check); err != nil {
		slog.Error("worker stopped; inspect local configuration and bounded diagnostics")
		os.Exit(1)
	}
}
func load(path string) (configuration, error) {
	var c configuration
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0037 != 0 || info.Size() > 128*1024 {
		return c, fmt.Errorf("worker configuration must be private and regular")
	}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 128*1024+1))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("invalid worker configuration")
	}
	return c, c.Config.Validate()
}
func wireIdentity(i worker.Identity) workerapi.Identity {
	return workerapi.Identity{WorkerID: i.WorkerID, BrokerID: i.BrokerID, Generation: i.Generation}
}
func localIdentity(i workerapi.Identity) worker.Identity {
	return worker.Identity{WorkerID: i.WorkerID, BrokerID: i.BrokerID, Generation: i.Generation}
}
func run(path string, check bool) error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("worker must be unprivileged")
	}
	c, err := load(path)
	if err != nil {
		return err
	}
	sc := workerapi.ServerConfig{Listen: c.Listen, Identity: wireIdentity(c.Identity), TLS: c.TLS}
	if err = workerapi.ValidateServerConfig(sc); err != nil {
		return err
	}
	if err = host.CheckSandbox(); err != nil {
		return err
	}
	if check {
		return worker.CheckConfig(c.Config)
	}
	engine, err := worker.OpenEngine(c.Config)
	if err != nil {
		return err
	}
	adapter := &engineAdapter{engine: engine}
	srv, err := workerapi.NewServer(sc, adapter)
	if err != nil {
		engine.Drain(c.Identity)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.BootTimeoutSeconds+180)*time.Second)
		defer cancel()
		engine.Wait(ctx)
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serveWorker(ctx, c.Config, engine, srv, func() error { return srv.ListenAndServeTLS("", "") })
}

type drainEngine interface {
	Drain(worker.Identity) error
	Wait(context.Context) error
}

// The listener stays available for reconciliation until local jobs finish.
// This uses a fresh bounded wait context, never the canceled shutdown signal.
func serveWorker(ctx context.Context, c worker.Config, engine drainEngine, srv *http.Server, listen func() error) error {
	var err error
	result := make(chan error, 1)
	go func() { result <- listen() }()
	slog.Info("Keyless worker starting authenticated listener", "worker", c.Identity.WorkerID)
	select {
	case <-ctx.Done():
	case err = <-result:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	engine.Drain(c.Identity)
	// Keep status available while credentialed jobs finish. An HTTP/broker outage
	// never cancels a running VM; the engine enforces each configured deadline.
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Duration(c.JobTimeoutSeconds+c.BootTimeoutSeconds+180)*time.Second)
	defer cancel()
	waitErr := engine.Wait(waitCtx)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	if waitErr != nil {
		return waitErr
	}
	return err
}

type localEngine interface {
	Inventory() (worker.Inventory, error)
	Reserve(context.Context, worker.Identity, string, string) (worker.Record, error)
	Seal(worker.Identity, string) (worker.Record, error)
	Deliver(worker.Identity, string, string) error
	Status(worker.Identity, string) (worker.Record, error)
	Drain(worker.Identity) error
}
type engineAdapter struct{ engine localEngine }

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, worker.ErrNotFound):
		return workerapi.ErrNotFound
	case errors.Is(err, worker.ErrFenced):
		return workerapi.ErrFenced
	case errors.Is(err, worker.ErrConsumed):
		return workerapi.ErrConsumed
	case errors.Is(err, worker.ErrConflict), errors.Is(err, worker.ErrTransition):
		return workerapi.ErrConflict
	default:
		return workerapi.ErrUnavailable
	}
}
func wireRecord(r worker.Record) workerapi.Record {
	q := r.Request
	return workerapi.Record{Request: workerapi.Request{Identity: wireIdentity(q.Identity), AssignmentID: q.AssignmentID, VMID: q.VMID, ProfileDigest: q.ProfileDigest, CPUs: q.CPUs, MemoryMiB: q.MemoryMiB, DiskGiB: q.DiskGiB}, State: string(r.State), CompletedAt: r.CompletedAt}
}
func (a *engineAdapter) Inventory() (workerapi.Inventory, error) {
	v, e := a.engine.Inventory()
	out := workerapi.Inventory{Identity: wireIdentity(v.Identity), Draining: v.Draining, Used: workerapi.CapacityUsed{VMs: v.Used.VMs, CPUs: v.Used.CPUs, MemoryMiB: v.Used.MemoryMiB}, Budget: workerapi.Budget{MaxVMs: v.Budget.MaxVMs, MaxCPUs: v.Budget.MaxCPUs, MaxMemoryMiB: v.Budget.MaxMemoryMiB}}
	for _, p := range v.Profiles {
		out.Profiles = append(out.Profiles, workerapi.ProfileInventory{ID: p.ID, Digest: p.Digest, Machine: p.Machine, CPUs: p.CPUs, MemoryMiB: p.MemoryMiB, DiskGiB: p.DiskGiB, Ready: p.Ready, Booting: p.Booting})
	}
	for _, r := range v.Records {
		out.Records = append(out.Records, wireRecord(r))
	}
	return out, translate(e)
}
func (a *engineAdapter) Reserve(ctx context.Context, i workerapi.Identity, id, profile, digest string) (workerapi.Record, error) {
	v, e := a.engine.Inventory()
	if e != nil {
		return workerapi.Record{}, translate(e)
	}
	matched := false
	for _, p := range v.Profiles {
		if p.ID == profile && p.Digest == digest {
			matched = true
			break
		}
	}
	if !matched {
		return workerapi.Record{}, workerapi.ErrConflict
	}
	r, e := a.engine.Reserve(ctx, localIdentity(i), id, profile)
	return wireRecord(r), translate(e)
}
func (a *engineAdapter) Seal(i workerapi.Identity, id string) (workerapi.Record, error) {
	r, e := a.engine.Seal(localIdentity(i), id)
	return wireRecord(r), translate(e)
}
func (a *engineAdapter) Deliver(i workerapi.Identity, id, jit string) error {
	return translate(a.engine.Deliver(localIdentity(i), id, jit))
}
func (a *engineAdapter) Status(i workerapi.Identity, id string) (workerapi.Record, error) {
	r, e := a.engine.Status(localIdentity(i), id)
	return wireRecord(r), translate(e)
}
func (a *engineAdapter) Drain(i workerapi.Identity) error {
	return translate(a.engine.Drain(localIdentity(i)))
}
