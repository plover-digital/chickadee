//go:build linux && amd64

package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/worker"
)

var ErrRegistrationGuard = errors.New("late-registration guard pending; retry cleanup after NotBefore")

// StandaloneConfig is operator-owned. One GitHub queue drives one local profile.
type StandaloneConfig struct {
	GitHubHost     string `json:"github_host,omitempty"`
	GitHubURL      string `json:"github_url"`
	ClientID       string `json:"app_client_id"`
	InstallationID int64  `json:"app_installation_id"`
	KeyFile        string `json:"app_key_file"`
	ScaleSet       string `json:"scale_set"`
	RunnerGroupID  int    `json:"runner_group_id"`
}

func (s StandaloneConfig) apiConfig() config.Config {
	return config.Config{GitHubURL: s.GitHubURL, GitHubHost: s.GitHubHost, ClientID: s.ClientID, InstallationID: s.InstallationID, KeyFile: s.KeyFile, ScaleSet: s.ScaleSet, RunnerGroupID: s.RunnerGroupID}
}
func (s StandaloneConfig) Validate(c worker.Config) error {
	if len(c.Profiles) != 1 || !c.IsLandlock() {
		return fmt.Errorf("standalone GitHub mode requires one Landlock profile")
	}
	if !config.Name.MatchString(s.ScaleSet) || s.ClientID == "" || s.InstallationID <= 0 || s.RunnerGroupID < 1 || !filepath.IsAbs(s.KeyFile) {
		return fmt.Errorf("GitHub App, queue and runner group required")
	}
	// Public GitHub is the default; GHES requires an explicit host opt-in.
	if err := validateScope(s.GitHubURL, s.GitHubHost); err != nil {
		return err
	}
	info, err := os.Stat(s.KeyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return startupFailure("app-key-unreadable")
	}
	return nil
}

type sandboxEngine interface {
	Reserve(context.Context, worker.Identity, string, string) (worker.Record, error)
	Seal(worker.Identity, string) (worker.Record, error)
	Deliver(worker.Identity, string, string) error
	RetireReservation(worker.Identity, string) error
	Status(worker.Identity, string) (worker.Record, error)
	Inventory() (worker.Inventory, error)
	Drain(worker.Identity) error
	Wait(context.Context) error
}
type sandboxBackend interface {
	Poll(context.Context, string, int, chan<- int) error
	JITWithWorkFolder(context.Context, string, string) (string, error)
	Remove(context.Context, string) error
}

// RunStandalone takes engine ownership before contacting GitHub. It exposes no
// worker listener, and never puts App credentials into an execution environment.
func RunStandalone(ctx context.Context, c worker.Config, s StandaloneConfig, cleanup bool) (runErr error) {
	if os.Geteuid() == 0 {
		return fmt.Errorf("standalone GitHub controller must be unprivileged")
	}
	c.ProtectedPaths = append(append([]string(nil), c.ProtectedPaths...), s.KeyFile)
	if err := s.Validate(c); err != nil {
		return err
	}
	if cleanup {
		c.Profiles = append([]worker.Profile(nil), c.Profiles...)
		c.Profiles[0].Warm = 0
	}
	e, err := worker.OpenEngine(c)
	if err != nil {
		return startupFailure("engine-open-failed")
	}
	defer func() {
		e.Drain(c.Identity)
		wait, cancel := context.WithTimeout(context.Background(), time.Duration(c.JobTimeoutSeconds+c.BootTimeoutSeconds+180)*time.Second)
		defer cancel()
		if err := e.Wait(wait); runErr == nil {
			runErr = err
		}
	}()
	dir := filepath.Join(c.StateDir, "github")
	for _, p := range []string{dir, filepath.Join(dir, "records")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("GitHub journal must be private and direct")
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("GitHub journal must be service-owned")
		}
	}
	if _, err := registrationRecords(dir, s); err != nil {
		return err
	}
	init, cancel := context.WithTimeout(ctx, 60*time.Second)
	var b *Client
	if cleanup {
		b, err = Existing(init, s.apiConfig())
	} else {
		b, err = New(init, s.apiConfig())
	}
	cancel()
	if err != nil {
		return err
	}
	return runStandalone(ctx, c, s, e, b, dir, cleanup)
}
func registrationRecords(dir string, s StandaloneConfig) ([]host.Record, error) {
	records, err := host.Records(dir)
	if err != nil {
		return nil, err
	}
	if len(records) > worker.MaxRecords {
		return nil, fmt.Errorf("GitHub registration journal full")
	}
	for _, r := range records {
		if config.ScopeKey(r.GitHubURL, "") != config.ScopeKey(s.GitHubURL, "") || r.InstallationID != s.InstallationID || r.RunnerGroupID != s.RunnerGroupID || r.Name != s.ScaleSet+"-"+r.ID {
			return nil, fmt.Errorf("GitHub journal scope changed; restore original configuration for cleanup")
		}
	}
	return records, nil
}
func reconcileSandbox(ctx context.Context, c worker.Config, s StandaloneConfig, e sandboxEngine, b sandboxBackend, dir string) error {
	records, err := registrationRecords(dir, s)
	if err != nil {
		return err
	}
	for _, r := range records {
		status, err := e.Status(c.Identity, r.ID)
		if err != nil {
			return err
		} // Unknown/uncertain local state never authorizes removal.
		if status.State != worker.Terminal {
			continue
		}
		if err = b.Remove(ctx, r.Name); err != nil {
			return err
		}
		// A late ambiguous JIT response can register after an initial removal.
		if time.Now().Before(r.NotBefore) {
			continue
		}
		if err = host.Forget(dir, r.ID); err != nil {
			return err
		}
	}
	return nil
}
func provisionSandbox(ctx context.Context, c worker.Config, s StandaloneConfig, e sandboxEngine, b sandboxBackend, dir, id string) error {
	reserve, cancel := context.WithTimeout(ctx, time.Duration(c.BootTimeoutSeconds+5)*time.Second)
	record, err := e.Reserve(reserve, c.Identity, id, c.Profiles[0].ID)
	cancel()
	if err != nil {
		return err
	}
	// Any provisioning failure destroys the reserved execution, including an
	// ambiguous JIT API result. Never retry JIT against that execution.
	defer e.RetireReservation(c.Identity, id)
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err = e.Seal(c.Identity, id); err != nil {
		return err
	}
	name := s.ScaleSet + "-" + id
	if err = host.Save(dir, host.Record{ID: id, Name: name, GitHubURL: s.GitHubURL, InstallationID: s.InstallationID, RunnerGroupID: s.RunnerGroupID, NotBefore: time.Now().Add(10 * time.Minute)}); err != nil {
		return err
	}
	jit, err := requestSandboxJIT(ctx, b, name, record.Request.WorkFolder, 60*time.Second)
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Deliver(c.Identity, id, jit); err != nil {
		return err
	}
	slog.Info("Runner credential intent committed", "assignment", id)
	return nil
}

// Keep the request deadline distinct from the longer-lived controller context.
func requestSandboxJIT(parent context.Context, b sandboxBackend, name, path string, timeout time.Duration) (string, error) {
	request, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	jit, err := b.JITWithWorkFolder(request, name, path)
	if err == nil {
		err = request.Err()
	}
	return jit, err
}

func pollSandbox(ctx context.Context, s StandaloneConfig, max int, b sandboxBackend, out chan<- int) {
	for ctx.Err() == nil {
		if err := b.Poll(ctx, s.ScaleSet, max, out); ctx.Err() != nil {
			return
		} else if err != nil {
			slog.Warn("GitHub demand listener unavailable; retrying")
		}
		select {
		case out <- 0:
		case <-ctx.Done():
			return
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}
func runStandalone(parent context.Context, c worker.Config, s StandaloneConfig, e sandboxEngine, b sandboxBackend, dir string, cleanup bool) error {
	if cleanup {
		if err := reconcileSandbox(parent, c, s, e, b, dir); err != nil {
			return err
		}
		records, err := registrationRecords(dir, s)
		if err != nil {
			return err
		}
		if len(records) > 0 {
			return ErrRegistrationGuard
		}
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	capacity := min(c.Budget.MaxVMs, c.Budget.MaxCPUs/c.Profiles[0].CPUs, c.Budget.MaxMemoryMiB/c.Profiles[0].MemoryMiB)
	demands := make(chan int, 16)
	type provisionResult struct {
		id     string
		failed bool
	}
	results := make(chan provisionResult, c.Budget.MaxVMs)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); pollSandbox(ctx, s, capacity, b, demands) }()
	pending := map[string]bool{}
	desired := 0
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer func() { cancel(); e.Drain(c.Identity); wg.Wait() }()
	nextCleanup := time.Time{}
	nextProvision := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return drainSandbox(c, s, e, b, dir)
		case n := <-demands:
			next := min(capacity, max(0, n))
			if next != desired {
				slog.Info("Runner demand changed", "requested", next)
			}
			desired = next
		case result := <-results:
			delete(pending, result.id)
			if result.failed {
				nextProvision = time.Now().Add(2 * time.Second)
			}
		case <-tick.C:
		}
		inventory, err := e.Inventory()
		if err != nil {
			return err
		}
		if inventory.Draining {
			return fmt.Errorf("local engine stopped admission")
		}
		active := len(pending)
		for _, r := range inventory.Records {
			if r.State != worker.Terminal && !pending[r.Request.AssignmentID] {
				active++
			}
		}
		if !time.Now().Before(nextCleanup) {
			nextCleanup = time.Now().Add(30 * time.Second)
			call, stop := context.WithTimeout(ctx, 15*time.Second)
			err := reconcileSandbox(call, c, s, e, b, dir)
			stop()
			if err != nil {
				slog.Warn("GitHub registration cleanup pending")
			}
		}
		for active < desired && !time.Now().Before(nextProvision) {
			id := host.NewID()
			pending[id] = true
			active++
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := provisionSandbox(ctx, c, s, e, b, dir, id)
				if err != nil {
					slog.Warn("Runner provisioning failed", "assignment", id)
				}
				select {
				case results <- provisionResult{id, err != nil}:
				case <-ctx.Done():
				}
			}()
		}
	}
}

// Drain waits for local terminal proof before final registration reconciliation;
// Engine.Wait subsequently releases the journal lock. Cancellation stops polling
// and provisioning, while already delivered one-job executions finish normally.
func drainSandbox(c worker.Config, s StandaloneConfig, e sandboxEngine, b sandboxBackend, dir string) error {
	if err := e.Drain(c.Identity); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.JobTimeoutSeconds+c.BootTimeoutSeconds+180)*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		inventory, err := e.Inventory()
		if err != nil {
			return err
		}
		if inventory.Used.VMs == 0 {
			call, stop := context.WithTimeout(ctx, 60*time.Second)
			defer stop()
			if err := reconcileSandbox(call, c, s, e, b, dir); err != nil {
				slog.Warn("GitHub registration cleanup pending after drain")
			}
			return nil
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
