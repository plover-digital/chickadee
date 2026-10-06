//go:build linux && amd64

package pool

import (
	"context"
	"errors"
	"github.com/plover-digital/chickadee/internal/config"
	"github.com/plover-digital/chickadee/internal/host"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProfileMachine struct {
	done    chan struct{}
	job     chan struct{}
	once    sync.Once
	dir     string
	running chan string
	profile string
}

func (m *fakeProfileMachine) Run(_ string, _ time.Duration, _ string) error {
	m.running <- m.profile
	select {
	case <-m.job:
		return nil
	case <-m.done:
		return context.Canceled
	}
}
func (m *fakeProfileMachine) Exited() <-chan struct{} { return m.done }
func (m *fakeProfileMachine) Stop() error             { m.once.Do(func() { close(m.done) }); return nil }
func (m *fakeProfileMachine) Cleanup() error          { m.Stop(); return os.RemoveAll(m.dir) }

type profileBackend struct {
	profile string
	jit     chan string
}

func (b *profileBackend) JIT(_ context.Context, name string) (string, error) {
	if !strings.HasPrefix(name, b.profile+"-") {
		return "", errors.New("wrong scale set")
	}
	b.jit <- name
	return "test", nil
}
func (b *profileBackend) Remove(context.Context, string) error { return nil }

type bootedProfile struct {
	c    config.Config
	slot int
	id   string
	m    *fakeProfileMachine
}

func profileFixture(t *testing.T) (config.Config, map[string]Backend, chan bootedProfile, chan string, starter) {
	t.Helper()
	c := config.Config{StateDir: privateTemp(t), Profiles: map[string]config.Profile{"chickadee": {Image: "rocky", Resources: "medium", Max: 1}, "chickadee-small-ubuntu-2404": {Image: "ubuntu", Resources: "small", Max: 2}}, Images: map[string]config.Image{"rocky": {Path: "/images/rocky", DiskGiB: 16}, "ubuntu": {Path: "/images/ubuntu", DiskGiB: 16}}, ResourceClasses: map[string]config.Resources{"small": {CPUs: 2, MemoryMiB: 4096}, "medium": {CPUs: 4, MemoryMiB: 8192}}, Limits: config.Limits{Max: 2, CPUs: 6, MemoryMiB: 12288}, JobSeconds: 60}
	jit := make(chan string, 20)
	backends := map[string]Backend{}
	for name := range c.Profiles {
		backends[name] = &profileBackend{profile: name, jit: jit}
	}
	boots := make(chan bootedProfile, 20)
	running := make(chan string, 20)
	start := func(_ context.Context, c config.Config, slot int, id string) (machine, error) {
		dir := filepath.Join(c.StateDir, "vms", id)
		if e := os.MkdirAll(dir, 0700); e != nil {
			return nil, e
		}
		if e := os.WriteFile(filepath.Join(dir, "disk.qcow2"), nil, 0600); e != nil {
			return nil, e
		}
		m := &fakeProfileMachine{done: make(chan struct{}), job: make(chan struct{}), dir: dir, running: running, profile: c.ScaleSet}
		boots <- bootedProfile{c, slot, id, m}
		return m, nil
	}
	owner, e := Acquire(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { owner.Close() })
	return c, backends, boots, running, start
}
func awaitBoot(t *testing.T, boots <-chan bootedProfile) bootedProfile {
	t.Helper()
	select {
	case b := <-boots:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("no boot")
		return bootedProfile{}
	}
}
func awaitRunning(t *testing.T, running <-chan string) string {
	t.Helper()
	select {
	case p := <-running:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no job")
		return ""
	}
}
func TestProfilesRouteAndShareBudgets(t *testing.T) {
	c, backends, boots, running, start := profileFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demand := make(chan Demand, 10)
	done := make(chan error, 1)
	go func() { done <- runProfiles(ctx, c, backends, demand, start) }()
	demand <- Demand{"chickadee", 1}
	medium := awaitBoot(t, boots)
	if awaitRunning(t, running) != "chickadee" || medium.c.CPUs != 4 || medium.c.MemoryMiB != 8192 || medium.c.ImageDir != "/images/rocky" {
		t.Fatal("wrong default shape/image")
	}
	demand <- Demand{"chickadee-small-ubuntu-2404", 2}
	small := awaitBoot(t, boots)
	if awaitRunning(t, running) != small.c.ScaleSet || small.c.CPUs != 2 || small.c.MemoryMiB != 4096 || small.slot == medium.slot {
		t.Fatal("cross-profile resource/TAP error")
	}
	select {
	case <-boots:
		t.Fatal("aggregate memory/CPU budget exceeded")
	case <-time.After(1100 * time.Millisecond):
	}
	demand <- Demand{"chickadee", 0}
	close(medium.m.job)
	next := awaitBoot(t, boots)
	if next.c.ScaleSet != small.c.ScaleSet || next.id == medium.id {
		t.Fatal("waiting demand not admitted")
	}
	awaitRunning(t, running)
	if _, e := os.Stat(medium.m.dir); !os.IsNotExist(e) {
		t.Fatal("slot reused before disk cleanup")
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	dirs, _ := os.ReadDir(filepath.Join(c.StateDir, "vms"))
	if len(dirs) != 0 {
		t.Fatal("shutdown leaked disks")
	}
}
func TestProfilesReclaimOnlyUncredentialedWarm(t *testing.T) {
	c, backends, boots, running, start := profileFixture(t)
	c.Limits = config.Limits{Max: 1, CPUs: 4, MemoryMiB: 8192}
	smallName := "chickadee-small-ubuntu-2404"
	p := c.Profiles[smallName]
	p.Max = 1
	p.Warm = 1
	c.Profiles[smallName] = p
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demand := make(chan Demand, 10)
	done := make(chan error, 1)
	go func() { done <- runProfiles(ctx, c, backends, demand, start) }()
	warm := awaitBoot(t, boots)
	demand <- Demand{"chickadee", 1}
	medium := awaitBoot(t, boots)
	if medium.c.ScaleSet != "chickadee" {
		t.Fatal("warm blocked demanded default")
	}
	awaitRunning(t, running)
	select {
	case <-warm.m.done:
	default:
		t.Fatal("reused slot before warm exit")
	}
	demand <- Demand{smallName, 1}
	select {
	case <-medium.m.done:
		t.Fatal("evicted credentialed job")
	case <-boots:
		t.Fatal("booted over max")
	case <-time.After(1100 * time.Millisecond):
	}
	demand <- Demand{"chickadee", 0}
	close(medium.m.job)
	replacement := awaitBoot(t, boots)
	if replacement.c.ScaleSet != smallName {
		t.Fatal("queued request starved")
	}
	awaitRunning(t, running)
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestRemovedProfileRecoveryAndScope(t *testing.T) {
	c, _, _, _, _ := profileFixture(t)
	r := host.Record{ID: "0123456789abcdef", Name: "chickadee-medium-rocky-98-0123456789abcdef"}
	if e := host.Save(c.StateDir, r); e != nil {
		t.Fatal(e)
	}
	b := &removalFailure{ok: true}
	if e := reconcileProfiles(context.Background(), c, map[string]Backend{"chickadee-medium-rocky-98": b}); e != nil {
		t.Fatal(e)
	}
	records, _ := host.Records(c.StateDir)
	if len(records) != 0 {
		t.Fatal("removed profile intent leaked")
	}
	r.GitHubURL = "https://github.com/other"
	if _, e := RecordProfile(c, r); e == nil {
		t.Fatal("changed scope accepted")
	}
}

func TestDrainFinishesSpentGuestWithoutNewBoot(t *testing.T) {
	c, backends, boots, running, start := profileFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demand := make(chan Demand, 10)
	drain := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runProfilesWithDrain(ctx, c, backends, demand, start, drain) }()
	demand <- Demand{"chickadee", 1}
	job := awaitBoot(t, boots)
	awaitRunning(t, running)
	close(drain)
	demand <- Demand{"chickadee-small-ubuntu-2404", 2}
	select {
	case <-job.m.done:
		t.Fatal("drain killed job")
	case <-boots:
		t.Fatal("drain booted guest")
	case e := <-done:
		t.Fatalf("drained before job finished: %v", e)
	case <-time.After(1100 * time.Millisecond):
	}
	close(job.m.job)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish")
	}
	if _, e := os.Stat(job.m.dir); !os.IsNotExist(e) {
		t.Fatal("drain left credentialed disk")
	}
}
func TestScopedRecoveryRetainsInstallationIdentity(t *testing.T) {
	c, _, _, _, _ := profileFixture(t)
	scope := config.Scope{GitHubURL: "https://github.com/EXAMPLE-ORG", InstallationID: 123, RunnerGroupID: 2, Profiles: c.Profiles}
	c.Scopes = map[string]config.Scope{"primary": scope}
	c.Profiles = nil
	old := host.Record{ID: "0123456789abcdef", Name: "chickadee-0123456789abcdef", GitHubURL: scope.GitHubURL, RunnerGroupID: scope.RunnerGroupID}
	recovery, e := RecoveryConfig(c, old)
	if e != nil || recovery.InstallationID != 123 || recovery.Key() != config.ScopeKey(scope.GitHubURL, "chickadee") {
		t.Fatal("legacy scope not recovered")
	}
	old.InstallationID = 999
	if _, e := RecoveryConfig(c, old); e == nil {
		t.Fatal("changed installation accepted")
	}
}

func TestSameLabelScopesRouteSeparatelyAndShareCapacity(t *testing.T) {
	c, _, boots, running, start := profileFixture(t)
	label := "chickadee"
	profile := c.Profiles[label]
	c.Profiles = nil
	c.Scopes = map[string]config.Scope{
		"one": {GitHubURL: "https://github.com/one/repo", InstallationID: 11, RunnerGroupID: 1, Profiles: map[string]config.Profile{label: profile}},
		"two": {GitHubURL: "https://github.com/two/repo", InstallationID: 22, RunnerGroupID: 1, Profiles: map[string]config.Profile{label: profile}},
	}
	backends := map[string]Backend{}
	jit := map[string]chan string{}
	for _, p := range c.ProfileConfigs() {
		jit[p.Key()] = make(chan string, 2)
		backends[p.Key()] = &profileBackend{profile: label, jit: jit[p.Key()]}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	demand := make(chan Demand, 4)
	done := make(chan error, 1)
	go func() { done <- runProfiles(ctx, c, backends, demand, start) }()
	one := config.ScopeKey("https://github.com/one/repo", label)
	two := config.ScopeKey("https://github.com/two/repo", label)
	demand <- Demand{one, 1}
	first := awaitBoot(t, boots)
	awaitRunning(t, running)
	if first.c.InstallationID != 11 {
		t.Fatal("wrong installation")
	}
	select {
	case <-jit[one]:
	default:
		t.Fatal("wrong JIT backend")
	}
	demand <- Demand{two, 1}
	select {
	case <-boots:
		t.Fatal("two medium guests exceeded shared RAM")
	case <-jit[two]:
		t.Fatal("credentials issued without capacity")
	case <-time.After(1100 * time.Millisecond):
	}
	demand <- Demand{one, 0}
	close(first.m.job)
	second := awaitBoot(t, boots)
	awaitRunning(t, running)
	if second.c.InstallationID != 22 || second.c.GitHubURL != "https://github.com/two/repo" {
		t.Fatal("scope crossed")
	}
	select {
	case <-jit[two]:
	default:
		t.Fatal("second scope used wrong JIT backend")
	}
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
