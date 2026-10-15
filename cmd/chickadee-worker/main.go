//go:build linux && amd64

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/plover-digital/chickadee/internal/github"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/landlockrunner"
	"github.com/plover-digital/chickadee/internal/worker"
	"github.com/plover-digital/chickadee/workerapi"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type configuration struct {
	worker.Config
	GitHub *github.StandaloneConfig `json:"github,omitempty"`
	Listen string                   `json:"listen"`
	TLS    workerapi.TLSFiles       `json:"tls"`
}

func main() {
	path := flag.String("config", "/etc/chickadee-worker/config.json", "private worker configuration")
	check := flag.Bool("check", false, "validate local worker identity, resources, TLS and isolation without starting VMs or a listener")
	probe := flag.String("sandbox-probe", "", "internal isolation preflight child")
	landlockCheck := flag.Bool("check-landlock", false, "validate process-runner configuration and isolation without TLS or credentials")
	githubMode := flag.Bool("github", false, "standalone GitHub scale-set demand listener for one sandbox profile; no worker API")
	githubCleanup := flag.Bool("cleanup-github", false, "with -github, recover local state and reconcile registrations without starting runners")
	jitStdin := flag.Bool("jit-stdin", false, "standalone one-job Landlock runner; read fresh JIT from stdin")
	child := flag.Bool("landlock-child", false, "internal Landlock sandbox child")
	childProbe := flag.Bool("landlock-probe", false, "internal Landlock negative-isolation probe")
	flag.Parse()
	if *child {
		if err := landlockrunner.Child(*childProbe); err != nil {
			if *childProbe {
				fmt.Fprintln(os.Stderr, err.Error())
			}
			os.Exit(1)
		}
		return
	}
	if (*githubMode && (*jitStdin || *landlockCheck)) || (*githubCleanup && (!*githubMode || *check)) {
		slog.Error("incompatible standalone flags")
		os.Exit(1)
	}
	if *githubMode {
		if os.Geteuid() == 0 {
			slog.Error("standalone GitHub controller must be unprivileged")
			os.Exit(1)
		}
		reason := "config-invalid"
		c, err := load(*path)
		if err == nil && c.GitHub == nil {
			err = fmt.Errorf("github configuration required")
		}
		if err == nil && *check {
			reason = "engine-check-failed"
			err = worker.CheckConfig(c.Config)
			if err == nil {
				err = worker.CheckLandlockSandboxes(c.Config)
			}
		}
		if err == nil && !*check {
			reason = "controller-failed"
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)
			defer stop()
			// This mode has no live reload. Consume SIGHUP without shutting down
			// the listener or disturbing compatible warm/credentialed executions.
			hup := make(chan os.Signal, 1)
			signal.Notify(hup, syscall.SIGHUP)
			defer signal.Stop(hup)
			err = github.RunStandalone(ctx, c.Config, *c.GitHub, *githubCleanup)
		}
		if err != nil {
			if errors.Is(err, github.ErrRegistrationGuard) {
				slog.Error("registration cleanup deferred by late-JIT guard; retry after NotBefore")
			} else {
				if category := github.FailureCategory(err); category != "" {
					reason = category
				}
				slog.Error("standalone GitHub controller stopped; inspect private configuration and diagnostics", "reason", reason)
			}
			os.Exit(1)
		}
		return
	}
	if *landlockCheck {
		c, err := load(*path)
		if err == nil && !c.IsLandlock() {
			err = fmt.Errorf("Landlock configuration required")
		}
		if err == nil {
			err = worker.CheckConfig(c.Config)
		}
		if err == nil {
			err = worker.CheckLandlockSandboxes(c.Config)
		}
		if err != nil {
			slog.Error("Landlock preflight failed", "reason", err.Error())
			os.Exit(1)
		}
		hasCPU, err := host.HasCPUController(c.Cgroup)
		if err != nil {
			slog.Error("CPU capability report unavailable")
			os.Exit(1)
		}
		for _, p := range c.Profiles {
			if p.HostAccess != nil {
				summary, err := landlockrunner.PolicySummary(true)
				if err != nil {
					slog.Error("Landlock capability report unavailable")
					os.Exit(1)
				}
				fmt.Println(summary + " " + cpuModeSummary(hasCPU, c.CPUIDs))
			}
		}
		fmt.Println("LANDLOCK_SANDBOX_VERIFIED")
		return
	}
	if *jitStdin {
		if runJITOnce(*path) != nil {
			slog.Error("one-job runner failed; inspect private state and GitHub job result")
			os.Exit(1)
		}
		return
	}
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
	c.ProtectedPaths = []string{path, c.TLS.KeyFile, c.TLS.CertificateFile, c.TLS.CAFile}
	if c.GitHub != nil {
		c.ProtectedPaths = append(c.ProtectedPaths, c.GitHub.KeyFile, c.GitHub.TokenFile)
		if err := c.GitHub.Validate(c.Config); err != nil {
			return c, err
		}
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
	if c.GitHub != nil {
		return fmt.Errorf("github configuration requires explicit -github mode")
	}
	sc := workerapi.ServerConfig{Listen: c.Listen, Identity: wireIdentity(c.Identity), TLS: c.TLS}
	if err = workerapi.ValidateServerConfig(sc); err != nil {
		return err
	}
	if c.IsLandlock() {
		if err = worker.CheckConfig(c.Config); err != nil {
			return err
		}
		if check {
			return worker.CheckLandlockSandboxes(c.Config)
		}
		if c.Profiles[0].HostAccess == nil {
			if err = landlockrunner.CheckSandbox(c.Profiles[0].ImageDir, c.Profiles[0].DiskGiB); err != nil {
				return err
			}
		}
	} else if err = host.CheckSandbox(); err != nil {
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
	slog.Info("Keyless worker starting authenticated listener", "worker", c.Identity.WorkerID, "controller_pid", os.Getpid())
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
	return workerapi.Record{Request: workerapi.Request{Identity: wireIdentity(q.Identity), AssignmentID: q.AssignmentID, VMID: q.VMID, ProfileDigest: q.ProfileDigest, CPUs: q.CPUs, MemoryMiB: q.MemoryMiB, DiskGiB: q.DiskGiB, WorkFolder: q.WorkFolder}, State: string(r.State), CompletedAt: r.CompletedAt, Resources: r.Resources}
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

func cpuModeSummary(hasQuota bool, ids []int) string {
	if hasQuota {
		return "cpu=quota"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return "cpu=affinity-only(" + strings.Join(parts, ",") + ")"
}
