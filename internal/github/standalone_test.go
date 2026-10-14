//go:build linux && amd64

package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/plover-digital/chickadee/internal/host"
	"github.com/plover-digital/chickadee/internal/worker"
)

type fakeSandboxEngine struct {
	mu         sync.Mutex
	records    map[string]worker.Record
	delivered  int
	retired    int
	drained    bool
	workFolder string
}

func (e *fakeSandboxEngine) Reserve(_ context.Context, i worker.Identity, id, _ string) (worker.Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.drained {
		return worker.Record{}, worker.ErrTransition
	}
	r := worker.Record{Request: worker.Request{Identity: i, AssignmentID: id, VMID: host.NewID(), WorkFolder: e.workFolder}, State: worker.Reserved}
	e.records[id] = r
	return r, nil
}
func (e *fakeSandboxEngine) Seal(_ worker.Identity, id string) (worker.Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.records[id]
	r.State = worker.Sealed
	e.records[id] = r
	return r, nil
}
func (e *fakeSandboxEngine) Deliver(_ worker.Identity, id, jit string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if jit != "one-use-fixture" {
		return errors.New("wrong JIT")
	}
	r := e.records[id]
	r.State = worker.DeliveryIntent
	e.records[id] = r
	e.delivered++
	return nil
}
func (e *fakeSandboxEngine) RetireReservation(_ worker.Identity, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.records[id]
	if r.State == worker.DeliveryIntent {
		return worker.ErrConsumed
	}
	r.State = worker.Terminal
	e.records[id] = r
	e.retired++
	return nil
}
func (e *fakeSandboxEngine) Status(_ worker.Identity, id string) (worker.Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.records[id]
	if !ok {
		return r, worker.ErrNotFound
	}
	return r, nil
}
func (e *fakeSandboxEngine) Inventory() (worker.Inventory, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := worker.Inventory{}
	for _, r := range e.records {
		i.Records = append(i.Records, r)
		if r.State != worker.Terminal {
			i.Used.VMs++
		}
	}
	return i, nil
}
func (e *fakeSandboxEngine) Drain(worker.Identity) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.drained = true
	return nil
}
func (e *fakeSandboxEngine) Wait(context.Context) error { return nil }
func (e *fakeSandboxEngine) complete() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, r := range e.records {
		r.State = worker.Terminal
		e.records[id] = r
	}
}

type fakeSandboxBackend struct {
	mu              sync.Mutex
	desired         chan int
	jitPaths        []string
	removals        []string
	dir             string
	failJIT         bool
	failRemove      bool
	intentBeforeJIT bool
}

func (b *fakeSandboxBackend) Poll(ctx context.Context, _ string, _ int, out chan<- int) error {
	for {
		select {
		case n := <-b.desired:
			select {
			case out <- n:
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (b *fakeSandboxBackend) JITWithWorkFolder(_ context.Context, name, path string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.jitPaths = append(b.jitPaths, path)
	records, err := host.Records(b.dir)
	if err == nil {
		for _, r := range records {
			if r.Name == name {
				b.intentBeforeJIT = true
			}
		}
	}
	if b.failJIT {
		return "", errors.New("ambiguous fixture")
	}
	return "one-use-fixture", nil
}
func (b *fakeSandboxBackend) Remove(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removals = append(b.removals, name)
	if b.failRemove {
		return errors.New("unavailable")
	}
	return nil
}
func standaloneFixture(t *testing.T) (worker.Config, StandaloneConfig, *fakeSandboxEngine, *fakeSandboxBackend, string) {
	t.Helper()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "records"), 0700)
	c := worker.Config{Identity: worker.Identity{WorkerID: "local", BrokerID: "standalone", Generation: 1}, Profiles: []worker.Profile{{ID: "sandbox", Machine: "landlock", CPUs: 1, MemoryMiB: 512}}, Budget: worker.Budget{MaxVMs: 2, MaxCPUs: 2, MaxMemoryMiB: 1024}, BootTimeoutSeconds: 1, JobTimeoutSeconds: 1}
	s := StandaloneConfig{GitHubURL: "https://github.com/example/project", ClientID: "fixture", InstallationID: 1, RunnerGroupID: 1, KeyFile: filepath.Join(dir, "app.pem"), ScaleSet: "sandbox-test"}
	if err := os.WriteFile(s.KeyFile, []byte("nonsecret fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	e := &fakeSandboxEngine{records: map[string]worker.Record{}, workFolder: "/shared/slot-1"}
	b := &fakeSandboxBackend{desired: make(chan int, 4), dir: dir}
	return c, s, e, b, dir
}
func waitSandbox(t *testing.T, condition func() bool) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func TestSandboxStandaloneDemandDrainsDeliveredJob(t *testing.T) {
	c, s, e, b, dir := standaloneFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runStandalone(ctx, c, s, e, b, dir, false) }()
	b.desired <- 1
	waitSandbox(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.delivered == 1 })
	b.desired <- 0 // Demand falling never stops a spent execution.
	if err := reconcileSandbox(context.Background(), c, s, e, b, dir); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	if !b.intentBeforeJIT || len(b.removals) != 0 || len(b.jitPaths) != 1 || b.jitPaths[0] != e.workFolder {
		t.Fatal("unsafe provisioning", b.intentBeforeJIT, b.removals, b.jitPaths)
	}
	b.mu.Unlock()
	cancel()
	select {
	case err := <-done:
		t.Fatal("shutdown canceled active job", err)
	case <-time.After(50 * time.Millisecond):
	}
	e.complete()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not complete")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.removals) == 0 {
		t.Fatal("registration not reconciled after cleanup")
	}
}
func TestSandboxAmbiguousJITRetiresWithoutDelivery(t *testing.T) {
	c, s, e, b, dir := standaloneFixture(t)
	b.failJIT = true
	id := host.NewID()
	if err := provisionSandbox(context.Background(), c, s, e, b, dir, id); err == nil {
		t.Fatal("ambiguous JIT accepted")
	}
	e.mu.Lock()
	if e.delivered != 0 || e.retired != 1 {
		t.Fatal("failed execution reused", e.delivered, e.retired)
	}
	e.mu.Unlock()
	records, err := registrationRecords(dir, s)
	if err != nil || len(records) != 1 {
		t.Fatal("lost registration intent", records, err)
	}
	// Removal failure and late-registration grace both retain durable intent.
	b.failRemove = true
	if reconcileSandbox(context.Background(), c, s, e, b, dir) == nil {
		t.Fatal("removal failure ignored")
	}
	b.failRemove = false
	if err := reconcileSandbox(context.Background(), c, s, e, b, dir); err != nil {
		t.Fatal(err)
	}
	records, _ = registrationRecords(dir, s)
	if len(records) != 1 {
		t.Fatal("late registration guard lost")
	}
}
func TestSandboxRestartScopeFencingAndTerminalCleanup(t *testing.T) {
	c, s, e, b, dir := standaloneFixture(t)
	id := host.NewID()
	if err := host.Save(dir, host.Record{ID: id, Name: s.ScaleSet + "-" + id, GitHubURL: s.GitHubURL, InstallationID: s.InstallationID, RunnerGroupID: s.RunnerGroupID}); err != nil {
		t.Fatal(err)
	}
	if reconcileSandbox(context.Background(), c, s, e, b, dir) == nil {
		t.Fatal("unknown local assignment authorized removal")
	}
	e.records[id] = worker.Record{State: worker.Terminal}
	changed := s
	changed.ScaleSet = "another-queue"
	if _, err := registrationRecords(dir, changed); err == nil {
		t.Fatal("queue identity changed")
	}
	changed = s
	changed.InstallationID++
	if _, err := registrationRecords(dir, changed); err == nil {
		t.Fatal("installation identity changed")
	}
	if err := runStandalone(context.Background(), c, s, e, b, dir, true); err != nil {
		t.Fatal(err)
	}
	records, _ := registrationRecords(dir, s)
	if len(records) != 0 {
		t.Fatal("terminal registration not forgotten")
	}
}
func TestStandaloneScopeAndProfileValidation(t *testing.T) {
	c, s, _, _, _ := standaloneFixture(t)
	if err := s.Validate(c); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"https://evil.example/org", "https://github.com/org/repo/extra", "https://github.com/%2e%2e/repo", "https://github.com/./repo"} {
		bad := s
		bad.GitHubURL = scope
		if err := bad.Validate(c); err == nil {
			t.Fatal("invalid scope accepted", scope)
		}
	}
	c.Profiles = append(c.Profiles, c.Profiles[0])
	if err := s.Validate(c); err == nil {
		t.Fatal("multiple profiles accepted")
	}
	c.Profiles = c.Profiles[:1]
	c.Profiles[0].Machine = "q35"
	if err := s.Validate(c); err == nil {
		t.Fatal("VM profile accepted")
	}
	if _, err := (&Client{}).JITWithWorkFolder(context.Background(), "fixture", strings.Repeat("x", 5000)); err == nil {
		t.Fatal("invalid folder accepted")
	}
}

func TestStandaloneFailedProvisioningBacksOff(t *testing.T) {
	c, s, e, b, dir := standaloneFixture(t)
	b.failJIT = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runStandalone(ctx, c, s, e, b, dir, false) }()
	b.desired <- 1
	waitSandbox(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.retired == 1 })
	time.Sleep(300 * time.Millisecond)
	b.mu.Lock()
	count := len(b.jitPaths)
	b.mu.Unlock()
	if count != 1 {
		t.Fatal("provisioning failure caused immediate retry", count)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("failed provisioning did not drain")
	}
}
func TestStandaloneDemandClampedToResourceBudget(t *testing.T) {
	c, s, e, b, dir := standaloneFixture(t)
	c.Budget.MaxVMs = 32
	c.Budget.MaxMemoryMiB = 32 * 512
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runStandalone(ctx, c, s, e, b, dir, false) }()
	b.desired <- 100
	waitSandbox(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.delivered == 2 })
	cancel()
	e.complete()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("capacity test did not drain")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.delivered != 2 {
		t.Fatal("CPU budget exceeded", e.delivered)
	}
}

type lateJITBackend struct {
	*fakeSandboxBackend
	started chan struct{}
	release chan struct{}
}

func (b *lateJITBackend) JITWithWorkFolder(ctx context.Context, name, path string) (string, error) {
	jit, err := b.fakeSandboxBackend.JITWithWorkFolder(ctx, name, path)
	close(b.started)
	<-b.release // Model a successful HTTP response racing caller cancellation.
	return jit, err
}
func TestCancelledProvisioningNeverDeliversLateJIT(t *testing.T) {
	c, s, e, base, dir := standaloneFixture(t)
	b := &lateJITBackend{fakeSandboxBackend: base, started: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	id := host.NewID()
	go func() { done <- provisionSandbox(ctx, c, s, e, b, dir, id) }()
	select {
	case <-b.started:
	case <-time.After(3 * time.Second):
		t.Fatal("JIT not started")
	}
	cancel()
	close(b.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.delivered != 0 || e.retired != 1 {
		t.Fatal("late credentials delivered after cancellation", e.delivered, e.retired)
	}
	records, err := registrationRecords(dir, s)
	if err != nil || len(records) != 1 {
		t.Fatal("late-registration intent lost", records, err)
	}
}

func TestGHESRequiresExplicitVerifiedHost(t *testing.T) {
	c, s, _, _, _ := standaloneFixture(t)
	s.GitHubURL = "https://ghe.example.net/site/repo"
	if s.Validate(c) == nil {
		t.Fatal("implicit arbitrary GHES host trusted")
	}
	s.GitHubHost = "ghe.example.net"
	if err := s.Validate(c); err != nil {
		t.Fatal(err)
	}
	if s.apiConfig().GitHubHost != s.GitHubHost {
		t.Fatal("GHES host not carried into App client")
	}
	for _, url := range []string{"http://ghe.example.net/site", "https://other.example.net/site", "https://user@ghe.example.net/site", "https://ghe.example.net/site?q=1"} {
		bad := s
		bad.GitHubURL = url
		if bad.Validate(c) == nil {
			t.Fatal("unsafe GHES scope accepted", url)
		}
	}
	for _, host := range []string{"ghe.example.net/path", "user@ghe.example.net", "ghe.example.net?token=1", "ghe.example.net#fragment", "ghe.example.net:bad"} {
		bad := s
		bad.GitHubHost = host
		if bad.Validate(c) == nil {
			t.Fatal("unsafe authority accepted", host)
		}
	}
}
func TestStandaloneRejectsRootBeforeOpeningState(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("run isolated test as root to verify privilege rejection")
	}
	if err := RunStandalone(context.Background(), worker.Config{}, StandaloneConfig{}, false); err == nil || !strings.Contains(err.Error(), "unprivileged") {
		t.Fatal("root controller accepted", err)
	}
}

func TestStartupFailureCategoryNeverRendersRawErrors(t *testing.T) {
	for _, code := range []string{"app-key-unreadable", "github-auth-failed", "scale-set-lookup-failed", "scale-set-create-failed", "engine-open-failed"} {
		err := fmt.Errorf("untrusted credential text: %w", startupFailure(code))
		if got := FailureCategory(err); got != code || strings.Contains(got, "credential") {
			t.Fatal("unsafe reason category", got)
		}
	}
	if got := FailureCategory(errors.New("raw token-like text")); got != "" {
		t.Fatal("raw error entered category", got)
	}
}

type deadlineJITBackend struct{ *fakeSandboxBackend }

func (b *deadlineJITBackend) JITWithWorkFolder(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()
	return "late-fixture", nil
}
func TestJITResponseAfterRequestDeadlineRejectedWithLiveParent(t *testing.T) {
	_, _, _, base, _ := standaloneFixture(t)
	parent := context.Background()
	_, err := requestSandboxJIT(parent, &deadlineJITBackend{base}, "fixture", "/shared/work", 5*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || parent.Err() != nil {
		t.Fatal("request deadline not enforced independently", err)
	}
}
